package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// startChained：起五层洋葱链服务端，返回地址与 orderRecorder。
func startChained(t *testing.T) (string, *orderRecorder) {
	t.Helper()
	o := &orderRecorder{}
	chain := Chain(
		traceMW(o, "A-auth"),
		traceMW(o, "B-log"),
		recoverMW(o),
		traceMW(o, "D-timeout"),
		traceMW(o, "E-biz"),
	)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: chain.Then(mux(o))}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); ln.Close() })
	time.Sleep(100 * time.Millisecond)
	return ln.Addr().String(), o
}

// getAndDrain：请求 + 排干 + 关闭。
func getAndDrain(t *testing.T, addr, path string) int {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// TestMiddlewareOrderMirrors：验收①——五层中间件进入序与退出序严格镜像。
func TestMiddlewareOrderMirrors(t *testing.T) {
	addr, o := startChained(t)
	if code := getAndDrain(t, addr, "/health"); code != 200 {
		t.Fatalf("/health = %d", code)
	}
	time.Sleep(100 * time.Millisecond)
	want := []string{
		"A-auth.in", "B-log.in", "C-recover.in", "D-timeout.in", "E-biz.in",
		"handler: health",
		"E-biz.out", "D-timeout.out", "C-recover.out", "B-log.out", "A-auth.out",
	}
	got := o.snapshot()
	if len(got) != len(want) {
		t.Fatalf("事件数 %d, want %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("事件 %d = %q, want %q（完整序列 %v）", i, got[i], want[i], got)
		}
	}
}

// TestClientDisconnectEarlyExit：验收②——客户端断开 TCP 后，
// 服务端 handler 经 r.Context().Done() 在 100ms 内退出（而非等满 10s）。
func TestClientDisconnectEarlyExit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	exitAt := make(chan time.Duration, 1)
	slowSrv := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			select {
			case <-r.Context().Done():
				exitAt <- time.Since(start)
			case <-time.After(10 * time.Second):
				exitAt <- time.Since(start)
			}
		})}
	go slowSrv.Serve(ln)
	t.Cleanup(func() { slowSrv.Close(); ln.Close() })

	// 裸 TCP 客户端：发出请求（不读响应），50ms 后断开连接
	tcp, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tcp.Write([]byte("GET /slow HTTP/1.1\r\nHost: x\r\n\r\n"))
	time.Sleep(50 * time.Millisecond)
	tcp.Close() // 客户端消失：服务端读到连接关闭 → ctx 取消

	select {
	case d := <-exitAt:
		if d > 100*time.Millisecond {
			t.Fatalf("handler 在 %v 后才退出, want ≤100ms", d.Round(time.Millisecond))
		}
		t.Logf("handler 在 %v 内感知断连并退出", d.Round(time.Millisecond))
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内未退出——客户端断连未被感知")
	}
}

// TestPanicRecovery500AndAlive：验收③——handler panic 被 recover 中间件
// 转成 500，进程不死，后续请求照常 200。
func TestPanicRecovery500AndAlive(t *testing.T) {
	addr, o := startChained(t)
	if code := getAndDrain(t, addr, "/panic"); code != 500 {
		t.Fatalf("/panic = %d, want 500（recover 应转 500）", code)
	}
	if code := getAndDrain(t, addr, "/health"); code != 200 {
		t.Fatalf("后续 /health = %d, want 200（进程应存活）", code)
	}
	found := false
	for _, e := range o.snapshot() {
		if strings.Contains(e, "RECOVER: boom") {
			found = true
		}
	}
	if !found {
		t.Fatal("orderRecorder 未记录 RECOVER 事件")
	}
	// 并发轰炸下服务依然存活（panic 不积累）
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			getAndDrain(t, addr, "/panic")
		}()
	}
	wg.Wait()
	if code := getAndDrain(t, addr, "/health"); code != 200 {
		t.Fatalf("20 次 panic 轰炸后 /health = %d", code)
	}
	fmt.Println("  20 次 panic 轰炸后服务存活")
}
