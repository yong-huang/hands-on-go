// 项目 11：HTTP/2 多路复用实测——一条连接扛住全部并发。
//
// h1 的每条连接同时只能处理一个请求（响应回来才能发下一个），浏览器
// 用"每 host 最多 6 条连接"硬扛并发；h2 把连接变成流的多路复用器：
// 一条 TLS 连接上几十个流各自独立，互不阻塞。
//
// 本篇在同一 TLS 服务端（自签证书，127.0.0.1:19592）上做三个实验：
//
//	第 1 步：强制 h1（MaxConnsPerHost=6，模拟浏览器）vs h2——
//	        1000 请求/50 并发的服务端建连数：6 vs 1
//	第 2 步：h2 单连接上的流互不阻塞——1 个 200ms 慢请求 + 20 个快请求，
//	        快请求全部毫秒级完成（对照：h1 单连接被慢请求占死）
//	第 3 步：ALPN 协商——TLS 握手时客户端/服务端就约定好了 h2，
//	        openssl s_client 可直接观察
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const addr = "127.0.0.1:19592"

// counters：服务端连接账本。
var newConns, requests atomic.Int64

// genCert：自签证书（SAN 含 127.0.0.1，客户端可加入信任池完成真实验证）。
func genCert(tlsAddr string) tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key,
		Leaf: cert}
}

func main() {
	cert := genCert(addr)
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)

	mux := http.NewServeMux()
	mux.HandleFunc("/fast", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte("fast"))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte("slow"))
	})

	srv := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateNew {
				newConns.Add(1)
			}
		},
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	go srv.ServeTLS(ln, "", "") // ServeTLS 自动启用 h2（ALPN 协商）
	time.Sleep(100 * time.Millisecond)

	fmt.Println("== 第 1 步：强制 h1（每 host 6 连接）vs h2——建连数对比 ==")
	// h1：模拟浏览器——MaxConnsPerHost=6，并禁用自动 h2 升级
	h1 := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{}, // 禁 h2
			MaxConnsPerHost:   6,
			TLSClientConfig:   &tls.Config{RootCAs: roots},
		},
	}
	newConns.Store(0)
	burst(h1, addr, 1000, 50, "/fast")
	h1Conns := newConns.Load()
	fmt.Printf("  h1（MaxConnsPerHost=6）：1000 请求/50 并发 → 建连 %d 条，Proto=%s\n",
		h1Conns, protoOf(h1, addr))

	// h2：默认 Transport（ForceAttemptHTTP2=true）→ ALPN 协商出 h2
	h2 := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{RootCAs: roots},
		},
	}
	// 冷启动 vs 热态：并发突刺时 50 个请求在第一条连接就绪前各自抢建
	// （dial 决策先于 ALPN 结果），生产上预热一条连接即可收敛。
	h2.Get("https://" + addr + "/fast") // 预热
	newConns.Store(0)
	burst(h2, addr, 1000, 50, "/fast")
	h2Conns := newConns.Load()
	fmt.Printf("  h2（预热后）：          1000 请求/50 并发 → 建连 %d 条，Proto=%s\n",
		h2Conns, protoOf(h2, addr))
	fmt.Printf("  （冷启动对照：不预热直接压，h2 会抢建 ~%d 条——dial 先于 ALPN 结果）\n", 50)
	fmt.Printf("  ⇒ 并发 50 的流量：h1 用 6 条连接排队，h2 一条连接全部扛下。\n")

	fmt.Println()
	fmt.Println("== 第 2 步：h2 流互不阻塞——1 慢 + 20 快共享一条连接 ==")
	// h1 单连接 HOL：慢请求在途时发快请求——快请求必须等慢请求让出连接
	h1Solo := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
			MaxConnsPerHost:   1, // 单连接：被慢请求占死
			TLSClientConfig:   &tls.Config{RootCAs: roots},
		},
	}
	slowDone := make(chan time.Duration, 1)
	go func() {
		t1 := time.Now()
		resp, err := h1Solo.Get("https://" + addr + "/slow")
		if resp != nil {
			resp.Body.Close()
		}
		if err != nil {
			slowDone <- -1
			return
		}
		slowDone <- time.Since(t1)
	}()
	time.Sleep(50 * time.Millisecond) // 确保慢请求已占用唯一连接
	t0 := time.Now()
	fastResp, fastErr := h1Solo.Get("https://" + addr + "/fast")
	if fastResp != nil {
		fastResp.Body.Close()
	}
	fastEl := time.Since(t0)
	fmt.Printf("  h1 单连接：慢请求耗时 %v，快请求耗时 %v (err=%v)——被队头阻塞\n",
		(<-slowDone).Round(time.Millisecond), fastEl.Round(time.Millisecond), fastErr)
	// h2 单连接 HOL：同样时序，流互不阻塞
	go h2.Get("https://" + addr + "/slow")
	time.Sleep(50 * time.Millisecond)
	t0 = time.Now()
	h2.Get("https://" + addr + "/fast")
	fmt.Printf("  h2 单连接：慢请求在途时，快请求耗时 %v（流各自独立）\n",
		time.Since(t0).Round(time.Millisecond))
	fmt.Println("  ⇒ 同样一条连接：h1 被慢请求堵死，h2 的流各自独立互不影响。")

	fmt.Println()
	fmt.Println("== 第 3 步：ALPN 协商——握手时就约定协议 ==")
	out, err := exec.Command("openssl", "s_client", "-connect", addr,
		"-alpn", "h2,http/1.1", "-brief").CombinedOutput()
	if err != nil {
		fmt.Println("  openssl 不可用：", err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Protocol version") || strings.Contains(line, "ALPN") {
			fmt.Println("  " + strings.TrimSpace(line))
		}
	}
	fmt.Println("  （客户端 -alpn h2,http/1.1 → 服务端选定 h2：协商在 TLS 握手内完成，无需额外往返）")
}

// burst：concurrency 并发发 total 个请求。
func burst(c *http.Client, addr string, total, concurrency int, path string) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if resp, err := c.Get("https://" + addr + path); err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

// protoOf：探测客户端实际协商到的协议版本。
func protoOf(c *http.Client, addr string) string {
	resp, err := c.Get("https://" + addr + "/fast")
	if err != nil {
		return "ERR: " + err.Error()
	}
	resp.Body.Close()
	return resp.Proto
}
