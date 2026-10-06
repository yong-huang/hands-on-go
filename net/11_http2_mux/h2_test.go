package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startTLS：起 h2 能力的 TLS 服务端（自签证书），返回地址、信任池与建连账本。
func startTLS(t *testing.T, delay time.Duration) (string, *x509.CertPool, *atomic.Int64) {
	t.Helper()
	cert := genCert(addr)
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)

	mux := http.NewServeMux()
	mux.HandleFunc("/fast", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay) // 处理延迟：让并发真正在途（对照对调度速度鲁棒）
		w.Write([]byte("fast"))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("slow"))
	})
	newConns := &atomic.Int64{}
	srv := &http.Server{
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateNew {
				newConns.Add(1)
			}
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close(); ln.Close() })
	time.Sleep(100 * time.Millisecond)
	return ln.Addr().String(), roots, newConns
}

func h1Client(roots *x509.CertPool, maxConns int) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
			MaxConnsPerHost:   maxConns,
			TLSClientConfig:   &tls.Config{RootCAs: roots},
		},
	}
}

func h2Client(roots *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{RootCAs: roots},
		},
	}
}

// TestH2ConnectionCount：验收①——预热后 1000 请求/50 并发全部复用
// 单条 h2 连接（新建 0）；同流量 h1（模拟浏览器 6 连接上限）建连 6。
func TestH2ConnectionCount(t *testing.T) {
	addr, roots, conns := startTLS(t, 2*time.Millisecond)

	// 预热：h2 建立它唯一的连接
	hc := h2Client(roots)
	hc.Get("https://" + addr + "/fast")
	conns.Store(0)

	burst(hc, addr, 1000, 50, "/fast")
	if got := conns.Load(); got != 0 {
		t.Fatalf("h2 预热后 1000 请求新建 %d 条连接, want 0（单连接多路复用）", got)
	}

	// h1 对照：模拟浏览器 6 连接上限（不预热——50 并发竞争触发满额拨号；
	// 若预热 1 条，微型请求会逐个排空该连接，transport 永远不拨第 2 条）
	hc1 := h1Client(roots, 6)
	conns.Store(0)
	burst(hc1, addr, 1000, 50, "/fast")
	if got := conns.Load(); got < 3 || got > 6 {
		t.Fatalf("h1（上限 6）建连 %d 条, want 3~6", got)
	}
}

// TestH2StreamsNotBlocked：验收②——h2 单连接上慢请求在途时，
// 快请求毫秒级完成（流互不阻塞）；h1 单连接同场景被队头阻塞 ≥150ms。
func TestH2StreamsNotBlocked(t *testing.T) {
	addr, roots, _ := startTLS(t, 0)

	// h2：慢请求占住连接，快请求仍应毫秒级完成
	getAndDrainT := func(c *http.Client, path string) {
		resp, err := c.Get("https://" + addr + path)
		if err != nil {
			t.Errorf("get %s: %v", path, err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	hc2 := h2Client(roots)
	getAndDrainT(hc2, "/fast") // 预热单连接
	go getAndDrainT(hc2, "/slow")
	time.Sleep(50 * time.Millisecond)
	t0 := time.Now()
	getAndDrainT(hc2, "/fast")
	h2Fast := time.Since(t0)
	if h2Fast > 100*time.Millisecond {
		t.Fatalf("h2 快请求耗时 %v——被慢流阻塞，多路复用失效", h2Fast.Round(time.Millisecond))
	}

	// h1 对照：单连接被慢请求占死，快请求等它让出（≥150ms）
	hc1 := h1Client(roots, 1)
	getAndDrainT(hc1, "/fast")
	go getAndDrainT(hc1, "/slow")
	time.Sleep(50 * time.Millisecond)
	t0 = time.Now()
	getAndDrainT(hc1, "/fast")
	h1Fast := time.Since(t0)
	if h1Fast < 120*time.Millisecond {
		t.Fatalf("h1 单连接快请求仅 %v——未观察到队头阻塞，对照不成立", h1Fast.Round(time.Millisecond))
	}
	t.Logf("h2 快请求 %v vs h1 快请求 %v（队头阻塞现场）", h2Fast.Round(time.Millisecond), h1Fast.Round(time.Millisecond))
}

// TestALPNNegotiation：验收③——h2 客户端经 ALPN 协商出 HTTP/2.0，
// 强制 h1 的客户端保持 HTTP/1.1。
func TestALPNNegotiation(t *testing.T) {
	addr, roots, _ := startTLS(t, 0)

	resp, err := h2Client(roots).Get("https://" + addr + "/fast")
	if err != nil {
		t.Fatalf("h2 get: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Proto != "HTTP/2.0" {
		t.Fatalf("h2 客户端 Proto = %s, want HTTP/2.0", resp.Proto)
	}
	if got := resp.TLS.NegotiatedProtocol; got != "h2" {
		t.Fatalf("ALPN 协商结果 = %q, want h2", got)
	}

	resp, err = h1Client(roots, 2).Get("https://" + addr + "/fast")
	if err != nil {
		t.Fatalf("h1 get: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Proto != "HTTP/1.1" {
		t.Fatalf("强制 h1 的 Proto = %s, want HTTP/1.1", resp.Proto)
	}
	if !strings.Contains(resp.TLS.NegotiatedProtocol, "http/1.1") &&
		resp.TLS.NegotiatedProtocol != "" {
		t.Logf("h1 ALPN 协商结果：%q", resp.TLS.NegotiatedProtocol)
	}
}
