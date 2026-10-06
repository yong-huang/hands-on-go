package main

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// setupServer：起指定配置的服务端，返回地址与关闭函数。
func setupServer(t *testing.T, hardened bool, states map[string]*atomic.Int64) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := buildServer(hardened, states)
	go srv.Serve(ln)
	return ln.Addr().String(), func() { srv.Close(); ln.Close() }
}

// TestConnStateTransitions：验收③——一条连接的状态事件全部被钩子观测到：
// new（接入）→ active（处理请求）→ idle（空闲）→ closed（回收）。
func TestConnStateTransitions(t *testing.T) {
	states := map[string]*atomic.Int64{
		"new": {}, "active": {}, "idle": {}, "closed": {},
	}
	addr, closeFn := setupServer(t, true, states)
	defer closeFn()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n\r\n"))
	buf := make([]byte, 64)
	c.Read(buf)
	time.Sleep(200 * time.Millisecond) // IdleTimeout 未到：应停在 idle
	c.Close()
	time.Sleep(400 * time.Millisecond) // 空闲连接被服务端回收

	for _, st := range []string{"new", "active", "idle", "closed"} {
		if states[st].Load() < 1 {
			t.Errorf("ConnState 未观测到 %s 事件", st)
		}
	}
}

// TestSlowlorisVulnerable：验收①（按实测更正）——慢连接攻击无超时服务：
// 正常请求保持健康（Go 每连接一个 goroutine，无 worker 池可占满，
// 教科书式 slowloris 塌陷不成立），但 goroutine 涨 ~600 且攻击期间不落。
func TestSlowlorisVulnerable(t *testing.T) {
	states := map[string]*atomic.Int64{"closed": {}}
	addr, closeFn := setupServer(t, false, states)
	defer closeFn()

	g0, _ := mem()
	cancel := make(chan struct{})
	attackDone := make(chan struct{})
	go func() {
		wg := slowloris(addr, 300, 3*time.Second, cancel)
		wg.Wait()
		close(attackDone)
	}()
	time.Sleep(2 * time.Second) // 攻击充分展开

	g1, _ := mem()
	if d := probe(addr); d < 0 {
		t.Error("攻击期间正常请求失败——结构性免疫不成立（与预期相反）")
	}
	if g1-g0 < 300 {
		t.Errorf("攻击期间 goroutine 仅 Δ%+d，want ≥+300（连接驻留）", g1-g0)
	}
	close(cancel)
	<-attackDone
}

// TestSlowlorisHardened：验收②——三超时服务在同样攻击下：
// 健康度不变，且攻击停止后 goroutine 全额回落（ReadHeaderTimeout 掐断）。
func TestSlowlorisHardened(t *testing.T) {
	states := map[string]*atomic.Int64{"closed": {}}
	addr, closeFn := setupServer(t, true, states)
	defer closeFn()

	g0, _ := mem()
	cancel := make(chan struct{})
	attackDone := make(chan struct{})
	go func() {
		wg := slowloris(addr, 300, 2*time.Second, cancel)
		wg.Wait()
		close(attackDone)
	}()
	time.Sleep(1 * time.Second) // 攻击展开（含服务端 500ms 掐断）

	if d := probe(addr); d < 0 {
		t.Fatal("加固服务在攻击下失活——三超时配置未生效")
	}
	if states["closed"].Load() < 250 {
		t.Errorf("服务端掐断事件 %d 次, want ≥250（ReadHeaderTimeout 生效）",
			states["closed"].Load())
	}
	close(cancel)
	<-attackDone
	time.Sleep(500 * time.Millisecond) // 等连接资源回收

	g1, _ := mem()
	if g1-g0 > 10 {
		t.Errorf("攻击结束后 goroutine Δ%+d, want ≤+10（应全额回落）", g1-g0)
	}
}
