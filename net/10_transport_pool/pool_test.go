package main

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// startCountingServer：起一个带计数的独立服务端（随机端口），返回地址与账本。
func startCountingServer(t *testing.T) (string, *counters) {
	t.Helper()
	cnt := &counters{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := newServer(ln.Addr().String(), cnt)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), cnt
}

// tunedClient：MaxIdleConnsPerHost 与并发数匹配的客户端。
func tunedClient(idlePerHost int) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        idlePerHost,
			MaxIdleConnsPerHost: idlePerHost,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

// TestDefaultVsTuned：验收①——同流量下，默认 Transport 的建连次数
// 远多于调优版（MaxIdleConnsPerHost=2 的池只留 2 条空闲）。
// handler 带 2ms 处理延迟：让 50 并发持续在途，对照对调度速度鲁棒
// （-race 拖慢执行时仍成立——开发实录）。
func TestDefaultVsTuned(t *testing.T) {
	addr, cnt := startCountingServer(t)
	cnt.delayNs.Store(2_000_000)

	n0, _, _ := snap(cnt)
	burst(&http.Client{Timeout: 5 * time.Second}, addr, 500, 50)
	n1, _, _ := snap(cnt)
	defaultNew := n1 - n0

	n0, _, _ = snap(cnt)
	burst(tunedClient(50), addr, 500, 50)
	n1, _, _ = snap(cnt)
	tunedNew := n1 - n0

	t.Logf("默认建连 %d 次 vs 调优建连 %d 次（同 500 请求 / 50 并发）", defaultNew, tunedNew)
	if tunedNew > 100 {
		t.Errorf("调优后建连 %d 次，应 ≤100（池容量级别）", tunedNew)
	}
	if defaultNew < tunedNew*2 {
		t.Fatalf("默认建连 %d 不足调优 %d 的 2 倍——对照不成立", defaultNew, tunedNew)
	}
}

// TestUnreadBodyForcesRedial：验收②——64KB 响应体未读尽时，Transport
// 无法复用连接：10 次请求建 10 条连接；读尽后 10 次请求只建 1 条。
func TestUnreadBodyForcesRedial(t *testing.T) {
	addr, cnt := startCountingServer(t)
	client := tunedClient(100)

	// 对照组：读尽 + 关闭 → 单连接复用
	n0, _, _ := snap(cnt)
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://" + addr + "/big")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	n1, _, _ := snap(cnt)
	if d := n1 - n0; d != 1 {
		t.Fatalf("读尽后建连 %d 次, want 1（应全程复用单连接）", d)
	}

	// 泄漏组：Body 未读未关 → 每次请求重建连接
	n0, _, _ = snap(cnt)
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://" + addr + "/big")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = resp // 故意不读不关
	}
	time.Sleep(300 * time.Millisecond)
	n1, _, _ = snap(cnt)
	if d := n1 - n0; d < 9 {
		t.Fatalf("未读尽时建连 %d 次, want ≥9（每条连接用完即弃）", d)
	}
}

// TestIdleConnTimeoutReaps：IdleConnTimeout 到点后，客户端主动关闭
// 池中空闲连接——服务端 closed 计数追平建连计数。
func TestIdleConnTimeoutReaps(t *testing.T) {
	addr, cnt := startCountingServer(t)
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 50,
			IdleConnTimeout:     200 * time.Millisecond,
		},
	}
	burst(client, addr, 100, 50)
	time.Sleep(700 * time.Millisecond) // > IdleConnTimeout

	newC, closedC, _ := snap(cnt)
	if closedC < newC {
		t.Fatalf("空闲超时后 closed(%d) < new(%d)——空闲连接未被回收", closedC, newC)
	}
}
