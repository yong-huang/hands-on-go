// 实验 01：marklet 骨架——路由、配置与优雅退出。
//
// marklet 是 web/01~20 共同生长的书签/稍后读服务，本篇先把骨架立起来：
//
//	配置：环境变量进 Config 结构，main 只认 Config，测试只造 Config
//	路由：Go 1.22 ServeMux 方法路由——GET /healthz 存活、GET /readyz 就绪、
//	      GET /api/v1/bookmarks 业务占位
//	退出：SIGTERM → readyz 先摘（等 LB 停止派发）→ Shutdown（等在途请求）
//	      → 超时按失败退出，交给进程管理器强杀兜底
//
// 演示（127.0.0.1:19610，父进程拉起自身副本为子进程，自演 SIGTERM 全过程）：
//
//	第 1 步：正常服务——healthz/readyz/api 三个端点各司其职，全部 200
//	第 2 步：摘除窗口——SIGTERM 后 readyz 立即 503、healthz 仍 200，
//	        且 SIGTERM 前发出的 /slow 在途请求照常拿到 200
//	第 3 步：退出收尾——进程远小于 2s 退出（验收线），端口归还，新连接被拒
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ---------- 配置：环境变量 → Config ----------

// Config：marklet 的全部运行参数。后续实验加能力只往这里加字段，
// 注入方式不变。
type Config struct {
	Addr            string        // 监听地址
	ShutdownTimeout time.Duration // Shutdown 等在途请求的上限，超时按失败退出
	DrainDelay      time.Duration // readyz 摘除到 Shutdown 之间的观察窗口
	SlowDelay       time.Duration // /slow 演示端点的处理耗时（模拟慢业务）
}

func loadConfig() Config {
	return Config{
		Addr:            envOr("MARKLET_ADDR", "127.0.0.1:19610"),
		ShutdownTimeout: envDurationOr("MARKLET_SHUTDOWN_TIMEOUT", 5*time.Second),
		DrainDelay:      envDurationOr("MARKLET_DRAIN_DELAY", 0),
		SlowDelay:       envDurationOr("MARKLET_SLOW_DELAY", 800*time.Millisecond),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDurationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// ---------- 路由：一个 ready 开关决定 readyz 的生死 ----------

// newMux：路由骨架。ready 用 atomic.Bool 而非普通 bool：
// 信号处理路径与 HTTP handler goroutine 会并发读写它。
func newMux(ready *atomic.Bool, cfg Config) *http.ServeMux {
	mux := http.NewServeMux()

	// 存活探针：只要进程在就 200。它回答"要不要重启这个进程"，
	// 摘流量期间答案依然是不用——所以它不看 ready。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	// 就绪探针：回答"能不能接新请求"。drain 一开始立即 503，
	// 负载均衡/编排器据此把实例摘出池子。
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !ready.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ready")
	})

	// 业务占位：本篇只立骨架，书签数据从实验 02 开始接入。
	mux.HandleFunc("GET /api/v1/bookmarks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"bookmarks": []any{},
			"note":      "skeleton: data layer lands in lab 02",
		})
	})

	// 慢端点：给优雅退出当"在途请求"的活体样本。select ctx.Done()
	// 让它在停机取消时提前退场，而不是傻睡满 SlowDelay。
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(cfg.SlowDelay):
			fmt.Fprintln(w, "slow done")
		case <-r.Context().Done():
		}
	})

	return mux
}

// ---------- 优雅退出：摘流量 → 等在途 → 超时兜底 ----------

// shutdowner：*http.Server 与 *httptest.Server 都满足，测试可以直接喂测试服务器。
type shutdowner interface {
	Shutdown(context.Context) error
}

// graceful：停机序列，顺序即语义：
//  1. readyz 先摘——新请求不该再进来（DrainDelay 是给 LB 的观察期）
//  2. Shutdown——监听器立刻关，在途请求等它们自然收尾
//  3. 超时未收完返回 DeadlineExceeded，由调用方决定退出码
func graceful(srv shutdowner, ready *atomic.Bool, cfg Config, log *slog.Logger) error {
	ready.Store(false)
	log.Info("readyz 已摘除，等待负载均衡停止派发", "drain_delay", cfg.DrainDelay.String())
	time.Sleep(cfg.DrainDelay)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	log.Info("开始 Shutdown：拒绝新连接，等在途请求收尾")
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Shutdown 超时，仍有在途请求未完成", "err", err)
		return err
	}
	log.Info("drain 完成：HTTP 已停，进程即将退出")
	return nil
}

// run：装配并常驻，收到 SIGTERM/SIGINT 后走 graceful，返回值决定进程退出码。
func run(cfg Config, log *slog.Logger) error {
	var ready atomic.Bool
	ready.Store(true)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           newMux(&ready, cfg),
		ReadHeaderTimeout: 5 * time.Second, // 慢连接防线
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}
	log.Info("marklet 骨架已启动", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }() // Serve 只在启动失败或 Shutdown 后返回

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(sig)

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case s := <-sig:
		log.Info("收到信号，开始停机", "signal", s.String())
		return graceful(srv, &ready, cfg, log)
	}
}

// ---------- 演示：父进程拉起自身副本，自演 SIGTERM 全过程 ----------

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if os.Getenv("MARKLET_DEMO_CHILD") == "1" {
		if err := run(loadConfig(), log); err != nil {
			log.Error("marklet 异常退出", "err", err)
			os.Exit(1)
		}
		return
	}
	demo(log)
}

func demo(log *slog.Logger) {
	cfg := loadConfig()
	exe, err := os.Executable()
	if err != nil {
		log.Error("定位自身可执行文件失败", "err", err)
		os.Exit(1)
	}

	base := time.Now()
	at := func() string { return fmt.Sprintf("[%5dms]", time.Since(base).Milliseconds()) }

	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "MARKLET_DEMO_CHILD=1", "MARKLET_DRAIN_DELAY=400ms")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		log.Error("拉起子进程失败", "err", err)
		os.Exit(1)
	}
	waitReady(cfg.Addr, 2*time.Second)

	fmt.Println("== 第 1 步：正常服务——三个端点各司其职 ==")
	show(cfg.Addr, "/healthz")
	show(cfg.Addr, "/readyz")
	show(cfg.Addr, "/api/v1/bookmarks")

	// 先发出一个慢请求（在途），紧接着 SIGTERM——
	// 摘除生效、存活如常、在途善终三件事同框。
	slowStatus := make(chan int, 1)
	go func() { slowStatus <- status(cfg.Addr, "/slow") }()
	time.Sleep(100 * time.Millisecond) // 确保 /slow 已被服务端受理

	fmt.Println("== 第 2 步：SIGTERM——摘除窗口里 readyz 与 healthz 分裂 ==")
	tKill := time.Now()
	fmt.Printf("%s kill -TERM\n", at())
	_ = cmd.Process.Signal(syscall.SIGTERM)
	fmt.Printf("%s GET /readyz  → %d 摘除已生效\n", at(), status(cfg.Addr, "/readyz"))
	fmt.Printf("%s GET /healthz → %d 存活如常\n", at(), status(cfg.Addr, "/healthz"))
	slowCode := <-slowStatus // 阻塞到在途请求收尾，再取时间戳——时间线才真实
	fmt.Printf("%s SIGTERM 前的在途 /slow → %d，Shutdown 等它收尾\n", at(), slowCode)

	fmt.Println("== 第 3 步：退出收尾——限时退出，端口归还 ==")
	waitErr := cmd.Wait()
	fmt.Printf("%s 进程退出：耗时 %dms（验收 ≤2000ms），退出码 %d\n",
		at(), time.Since(tKill).Milliseconds(), cmd.ProcessState.ExitCode())
	if conn, derr := net.DialTimeout("tcp", cfg.Addr, 300*time.Millisecond); derr != nil {
		fmt.Printf("%s 新连接 → 已拒绝（%s）——端口已归还\n", at(), briefErr(derr))
	} else {
		conn.Close()
		fmt.Printf("%s 新连接 → 竟然成功（异常：进程应已退出）\n", at())
	}
	if waitErr != nil {
		log.Error("子进程未干净退出", "err", waitErr)
		os.Exit(1)
	}
}

// status：只取状态码，不打印（摘除窗口的连发探针用）。
func status(addr, path string) int {
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// show：取状态码并打印一行（第 1 步的正常巡检用）。
func show(addr, path string) int {
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		fmt.Printf("GET %-22s → 请求失败: %v\n", path, err)
		return -1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 80))
	fmt.Printf("GET %-22s → %d %s\n", path, resp.StatusCode, strings.TrimSpace(string(body)))
	return resp.StatusCode
}

// waitReady：轮询 readyz 直到 200，给子进程留出启动时间。
func waitReady(addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if status(addr, "/readyz") == http.StatusOK {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "子进程 2s 内未就绪")
	os.Exit(1)
}

// briefErr：连接错误里的有效部分（"connect: connection refused"）。
func briefErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "connect:"); i >= 0 {
		return msg[i:]
	}
	return msg
}
