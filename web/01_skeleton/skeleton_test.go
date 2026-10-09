package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：任何测试遗留的 goroutine 都算失败。
// 客户端统一用 noKeepAliveClient（禁 keep-alive），避免运输层空闲连接的
// 后台 goroutine 撞上门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// noKeepAliveClient：每个请求独立连接、用完即弃——测试里最干净的客户端形态。
var noKeepAliveClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// quietConfig：测试用配置——所有等待压到毫秒级，测试总量仍秒级完成。
func quietConfig(addr string) Config {
	return Config{
		Addr:            addr,
		ShutdownTimeout: 2 * time.Second,
		DrainDelay:      30 * time.Millisecond,
		SlowDelay:       150 * time.Millisecond,
	}
}

// testServer：把 *httptest.Server 适配成 shutdowner——
// 它的 Config 字段就是真正在服务的底层 *http.Server。
type testServer struct {
	*httptest.Server
}

func (s testServer) Shutdown(ctx context.Context) error { return s.Config.Shutdown(ctx) }

// do：测试 goroutine 里发请求取状态码，出错直接判失败。
func do(t *testing.T, method, url string) int {
	t.Helper()
	code, err := probe(method, url)
	if err != nil {
		t.Fatalf("请求 %s: %v", url, err)
	}
	return code
}

// probe：不带 testing.T 的版本——可在普通 goroutine 里安全使用。
func probe(method, url string) (int, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return -1, err
	}
	resp, err := noKeepAliveClient.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// TestProbeSeparation：验收②的可执行化。
// 摘除态（ready=false）下 readyz 必须 503、healthz 必须仍 200——
// 两个探针回答的是不同问题：能不能接新流量 vs 进程要不要重启。
func TestProbeSeparation(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	ts := httptest.NewServer(newMux(&ready, quietConfig("127.0.0.1:0")))
	defer ts.Close()

	if got := do(t, http.MethodGet, ts.URL+"/healthz"); got != http.StatusOK {
		t.Errorf("就绪态 /healthz = %d， want 200", got)
	}
	if got := do(t, http.MethodGet, ts.URL+"/readyz"); got != http.StatusOK {
		t.Errorf("就绪态 /readyz = %d， want 200", got)
	}

	ready.Store(false) // 模拟收到 SIGTERM 后的第一步
	if got := do(t, http.MethodGet, ts.URL+"/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("摘除态 /readyz = %d， want 503", got)
	}
	if got := do(t, http.MethodGet, ts.URL+"/healthz"); got != http.StatusOK {
		t.Errorf("摘除态 /healthz = %d， want 200", got)
	}
}

// TestMethodRouting：Go 1.22 ServeMux 方法路由的语义断言。
func TestMethodRouting(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	ts := httptest.NewServer(newMux(&ready, quietConfig("127.0.0.1:0")))
	defer ts.Close()

	if got := do(t, http.MethodGet, ts.URL+"/healthz"); got != http.StatusOK {
		t.Errorf("GET /healthz = %d， want 200", got)
	}
	// 路径对、方法不对 → 405，而不是 404：方法路由把"存在但方法不匹配"显式化了
	if got := do(t, http.MethodPost, ts.URL+"/healthz"); got != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d， want 405", got)
	}
	// 未注册路径 → 404
	if got := do(t, http.MethodGet, ts.URL+"/api/v2/nope"); got != http.StatusNotFound {
		t.Errorf("GET /api/v2/nope = %d， want 404", got)
	}
	// 业务占位必须是合法 JSON 且带 bookmarks 字段
	resp, err := noKeepAliveClient.Get(ts.URL + "/api/v1/bookmarks")
	if err != nil {
		t.Fatalf("GET /api/v1/bookmarks: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("业务占位不是合法 JSON: %v", err)
	}
	if _, ok := payload["bookmarks"]; !ok {
		t.Errorf("业务占位缺 bookmarks 字段: %v", payload)
	}
}

// TestGracefulDrainsInflight：SIGTERM 前的在途请求必须善终——
// graceful 返回时它已拿到 200，且耗时 ≥ SlowDelay（没有被腰斩）。
func TestGracefulDrainsInflight(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	cfg := quietConfig("127.0.0.1:0")
	ts := httptest.NewServer(newMux(&ready, cfg))
	defer ts.Close()

	type result struct {
		status  int
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		status, _ := probe(http.MethodGet, ts.URL+"/slow")
		done <- result{status: status, elapsed: time.Since(start)}
	}()
	time.Sleep(30 * time.Millisecond) // /slow 已被服务端受理

	gErr := make(chan error, 1)
	go func() { gErr <- graceful(testServer{ts}, &ready, cfg, quietLogger()) }()

	// 摘除窗口内（ready 已翻、Shutdown 未开始）：readyz 必须已是 503
	drained := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if code, err := probe(http.MethodGet, ts.URL+"/readyz"); err == nil {
			if code != http.StatusServiceUnavailable {
				t.Fatalf("摘除窗口内 /readyz = %d， want 503", code)
			}
			drained = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !drained {
		t.Fatal("摘除窗口内未观测到 /readyz 503")
	}

	// 在途请求必须善终：拿到 200，且耗满 SlowDelay（没有被腰斩）
	r := <-done
	if r.status != http.StatusOK {
		t.Errorf("在途 /slow = %d， want 200（Shutdown 必须等它收尾）", r.status)
	}
	if r.elapsed < cfg.SlowDelay {
		t.Errorf("在途 /slow 只跑了 %v < SlowDelay %v，被腰斩了", r.elapsed, cfg.SlowDelay)
	}
	if err := <-gErr; err != nil {
		t.Fatalf("graceful 返回错误: %v", err)
	}
}

// TestShutdownTimeoutReportsError：在途请求卡死时，Shutdown 到点必须返回
// DeadlineExceeded（调用方以此决定非零退出码，交给进程管理器强杀兜底）。
func TestShutdownTimeoutReportsError(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // 模拟卡死的在途请求
		fmt.Fprintln(w, "finally")
	}))
	defer ts.Close()

	done := make(chan int, 1)
	go func() {
		status, _ := probe(http.MethodGet, ts.URL+"/block")
		done <- status
	}()
	time.Sleep(30 * time.Millisecond) // 卡死请求已在途

	cfg := quietConfig("127.0.0.1:0")
	cfg.DrainDelay = 0
	cfg.ShutdownTimeout = 100 * time.Millisecond
	var ready atomic.Bool
	ready.Store(true)

	err := graceful(testServer{ts}, &ready, cfg, quietLogger())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("graceful err = %v， want DeadlineExceeded", err)
	}
	close(release) // 松手，让在途请求收尾，goroutine 清零（goleak 门禁）
	if got := <-done; got != http.StatusOK {
		t.Errorf("松手后的请求 = %d， want 200", got)
	}
}

// TestChildExitsOnSIGTERMWithin2s：验收①的可执行化——
// 现场编译独立二进制，真发 SIGTERM，2s 内必须干净退出。
func TestChildExitsOnSIGTERMWithin2s(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 跳过子进程集成测试")
	}
	bin := filepath.Join(t.TempDir(), "marklet")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0") // 只要一个空闲端口号
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "MARKLET_ADDR="+addr, "MARKLET_DEMO_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			cmd.Process.Kill() // 测试失败时的清理，防子进程悬挂
		}
	}()

	deadline := time.Now().Add(30 * time.Second) // 等子进程就绪：全模块并行跑时编译与启动都变慢，放宽到 30s
	for {
		if code, err := probe(http.MethodGet, "http://"+addr+"/readyz"); err == nil && code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("子进程 2s 内未就绪")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("发 SIGTERM: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("子进程未干净退出: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM 后 2s 内未退出（验收① 失败）")
	}
}
