package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// echoBackend：带请求计数与 XFF 回显的后端（httptest 包装）。
func echoBackend(t *testing.T, name string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		xff := r.Header.Get("X-Forwarded-For")
		fmt.Fprintf(w, "%s|xff_entries=%d", name, len(splitXFF(xff)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gatewayOn：把网关挂到临时端口，返回网关地址。
func gatewayOn(t *testing.T, g *Gateway) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 2 * time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); ln.Close() })
	return ln.Addr().String()
}

// get：发一条请求，返回 (状态码, 响应体)。
func get(t *testing.T, addr, path string) (int, string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestRoundRobinDistribution：验收①——1000 请求在两后端 50/50±5%。
func TestRoundRobinDistribution(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	a := echoBackend(t, "A", &hitsA)
	b := echoBackend(t, "B", &hitsB)
	gw, err := NewGateway(time.Second, a.URL, b.URL)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	gwAddr := gatewayOn(t, gw)

	const total = 1000
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := get(t, gwAddr, "/")
			if code != 200 {
				t.Errorf("status %d", code)
			}
		}()
	}
	wg.Wait()

	gotA, gotB := hitsA.Load(), hitsB.Load()
	t.Logf("分布：A=%d B=%d（%.1f%% / %.1f%%）", gotA, gotB, float64(gotA)/10, float64(gotB)/10)
	if gotA+gotB != total {
		t.Fatalf("总命中 %d, want %d", gotA+gotB, total)
	}
	lo, hi := int64(total*95/100/2), int64(total*105/100/2)
	if gotA < lo || gotA > hi {
		t.Fatalf("A=%d 超出 50%%±5%% 区间 [%d, %d]", gotA, lo, hi)
	}
	if gotB < lo || gotB > hi {
		t.Fatalf("B=%d 超出 50%%±5%% 区间 [%d, %d]", gotB, lo, hi)
	}
}

// TestPassiveHealthFailover：验收②——关闭后端 A 后，
// 1 秒内全部流量切到 B（被动健康检查拉黑 + 轮询跳过）。
func TestPassiveHealthFailover(t *testing.T) {
	var hitsA, hitsB atomic.Int64
	a := echoBackend(t, "A", &hitsA)
	b := echoBackend(t, "B", &hitsB)
	gw, err := NewGateway(time.Second, a.URL, b.URL)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	gwAddr := gatewayOn(t, gw)

	get(t, gwAddr, "/") // 预热
	hitsB.Store(0)
	hitsA.Store(0)

	a.Close() // 后端 A 下线
	time.Sleep(100 * time.Millisecond)

	deadline := time.Now().Add(1 * time.Second)
	var bOnly, failovers int
	for time.Now().Before(deadline) {
		code, body := get(t, gwAddr, "/")
		if code != 200 {
			// 被动健康检查的定义：踩中下线后端的那 1 条请求得到 502，
			// 同时把该后端拉黑——之后不应再出现非 200。
			failovers++
			if failovers > 1 {
				t.Fatalf("多次非 200（%d 次）——拉黑未生效", failovers)
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if strings.HasPrefix(body, "B") {
			bOnly++
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("1 秒窗口内 %d 条请求全部命中 B（触发探针 %d 次非 200，A 已拉黑）", bOnly, failovers)
	if hitsA.Load() != 0 {
		t.Fatalf("A 下线后仍收到 %d 条请求", hitsA.Load())
	}
	if bOnly == 0 {
		t.Fatal("窗口内零请求——切换未生效")
	}
}

// TestXFFChain：验收③——两层网关串联后，后端看到的 XFF 链长为 2
// （直连时链长为 1）。XFF 头由 SetXForwarded 追加客户端地址形成。
func TestXFFChain(t *testing.T) {
	var hits atomic.Int64
	var lastXFF atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		xff := r.Header.Get("X-Forwarded-For")
		lastXFF.Store(xff)
		fmt.Fprintf(w, "xff_entries=%d", len(splitXFF(xff)))
	}))
	defer backend.Close()

	inner, err := NewGateway(time.Second, backend.URL) // 第二跳
	if err != nil {
		t.Fatalf("inner: %v", err)
	}
	innerSrv := httptest.NewServer(inner.Handler())
	defer innerSrv.Close()

	outer, err := NewGateway(time.Second, innerSrv.URL) // 第一跳
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	outerSrv := httptest.NewServer(outer.Handler())
	defer outerSrv.Close()

	_, body := get(t, strings.TrimPrefix(outerSrv.URL, "http://"), "/")
	if body != "xff_entries=2" {
		t.Fatalf("两层网关后 XFF 链 = %q, want 2 项", body)
	}
	t.Logf("后端看到的 XFF 链：%q", lastXFF.Load())

	// 对照：单层网关 → 链长 1
	single, err := NewGateway(time.Second, backend.URL)
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	singleSrv := httptest.NewServer(single.Handler())
	defer singleSrv.Close()
	if _, body := get(t, strings.TrimPrefix(singleSrv.URL, "http://"), "/"); body != "xff_entries=1" {
		t.Fatalf("单层网关 XFF 链 = %q, want 1 项", body)
	}
}
