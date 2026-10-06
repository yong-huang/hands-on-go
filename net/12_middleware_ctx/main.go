// 项目 12：中间件链与 context 贯穿——洋葱模型、断连感知与 panic 兜底。
//
// 中间件（middleware）是包在 handler 外、对所有路由统一生效的函数链：
//
//	logging(recover(timeout(mux)))  ← 调用像洋葱：请求穿入、响应穿出
//
// 三个实验（127.0.0.1:19595）：
//
//	第 1 步：五层中间件的执行顺序——进入序与退出序镜像（洋葱模型的名字来源）
//	第 2 步：客户端断连感知——handler 在途时客户端断开，
//	        r.Context() 收到取消，服务端 100ms 内退出（不白算 10 秒）
//	第 3 步：panic 兜底——handler panic 被 recover 中间件转成 500，
//	        进程不死，下一个请求照常 200
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Middleware：包一层 handler 的函数。
type Middleware func(http.Handler) http.Handler

// Chain：按顺序组装中间件——Chain(a, b, c).Then(h) 的执行序为 a→b→c→h。
func Chain(mws ...Middleware) interface {
	Then(http.Handler) http.Handler
} {
	chain := func(next http.Handler) http.Handler {
		for i := len(mws) - 1; i >= 0; i-- { // 从最后一个往前包：最先声明的最外层
			mw := mws[i]
			next = mw(next)
		}
		return next
	}
	return thenabler(chain)
}

type thenabler func(http.Handler) http.Handler

func (f thenabler) Then(h http.Handler) http.Handler { return f(h) }

// orderRecorder：进入/退出顺序的记录器（实验 1 的观测点，测试共用）。
type orderRecorder struct {
	mu     sync.Mutex
	events []string
}

func (o *orderRecorder) record(event string) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *orderRecorder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

// traceMW：记录进入与退出的中间件（洋葱模型的可视化）。
func traceMW(o *orderRecorder, name string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o.record(name + ".in")
			defer o.record(name + ".out")
			next.ServeHTTP(w, r)
		})
	}
}

// recoverMW：把 handler 的 panic 转成 500，进程不死；自身也是洋葱的一层。
func recoverMW(o *orderRecorder) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			o.record("C-recover.in")
			defer o.record("C-recover.out")
			defer func() {
				if rec := recover(); rec != nil {
					o.record("RECOVER: " + fmt.Sprint(rec))
					http.Error(w, "internal error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// mux：业务路由——/panic 会炸，/slow 等 ctx 或 10s，/health 即刻返回。
func mux(o *orderRecorder) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) {
		panic("boom from handler")
	})
	m.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		o.record("handler: waiting (ctx or 10s)")
		select {
		case <-r.Context().Done(): // 客户端断开/超时：立即退出，不算账
			o.record("handler: ctx canceled, exit early")
		case <-time.After(10 * time.Second):
			o.record("handler: full 10s elapsed")
			w.Write([]byte("slow done"))
		}
	})
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		o.record("handler: health")
		w.Write([]byte("ok"))
	})
	return m
}

func main() {
	o := &orderRecorder{}

	fmt.Println("== 第 1 步：五层洋葱——进入序与退出序镜像 ==")
	chain := Chain(
		traceMW(o, "A-auth"),
		traceMW(o, "B-log"),
		recoverMW(o),
		traceMW(o, "D-timeout"),
		traceMW(o, "E-biz"),
	)
	srv := &http.Server{Addr: "127.0.0.1:19595", Handler: chain.Then(mux(o))}
	ln := listenAndServe(srv)
	defer func() { srv.Close(); ln.Close() }()
	time.Sleep(100 * time.Millisecond)

	// 触发一次请求
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:19595/health")
	if err != nil {
		fmt.Println("get:", err)
		return
	}
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	events := o.snapshot()
	fmt.Println("  " + strings.Join(events, " → "))
	mirror := true
	n := len(events)
	for i := 0; i < n/2; i++ {
		in, out := events[i], events[n-1-i]
		if strings.TrimSuffix(in, ".in") != strings.TrimSuffix(out, ".out") {
			mirror = false
		}
	}
	fmt.Printf("  ⇒ 进入序与退出序镜像（对称=%v）——洋葱模型的名字就是这么来的。\n", mirror)

	fmt.Println()
	fmt.Println("== 第 2 步：客户端断连感知——handler 不白算 10 秒 ==")
	// 独立服务端：/slow 等 ctx 或 10s，退出时刻从专用通道回报
	exitAt := make(chan time.Duration, 1)
	slowSrv := &http.Server{Addr: "127.0.0.1:19596", Handler: http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			select {
			case <-r.Context().Done():
				exitAt <- time.Since(start) // 客户端断开：提前退出
			case <-time.After(10 * time.Second):
				exitAt <- time.Since(start)
			}
		})}
	ln2 := listenAndServe(slowSrv)
	defer func() { slowSrv.Close(); ln2.Close() }()

	// 客户端：用裸 TCP 发出请求（不读响应），50ms 后断开这条连接——
	// 服务端的 r.Context() 会因连接消失而立即取消
	tcp, derr := net.Dial("tcp", "127.0.0.1:19596")
	if derr != nil {
		fmt.Println("dial:", derr)
		return
	}
	tcp.Write([]byte("GET /slow HTTP/1.1\r\nHost: x\r\n\r\n"))
	time.Sleep(50 * time.Millisecond)
	tcp.Close() // 连接消失：服务端读到 EOF/RST → context 取消
	fmt.Printf("  handler 在 %v 后提前退出（验收 ≤100ms，而非等满 10s）\n", (<-exitAt).Round(time.Millisecond))

	fmt.Println()
	fmt.Println("== 第 3 步：panic 兜底——炸的是请求，不是进程 ==")
	resp, err = client.Get("http://127.0.0.1:19595/panic")
	if err != nil {
		fmt.Println("panic 请求:", err)
		return
	}
	resp.Body.Close()
	fmt.Printf("  /panic → %d（被 recover 中间件转成 500）\n", resp.StatusCode)
	resp, err = client.Get("http://127.0.0.1:19595/health")
	if err != nil {
		fmt.Println("后续请求:", err)
		return
	}
	resp.Body.Close()
	fmt.Printf("  下一个 /health → %d（进程存活，服务如常）\n", resp.StatusCode)
	fmt.Println("  ⇒ recover 拦在中间件层：单请求的爆炸不外溢。")
}

// listenAndServe：起服务端（独立 goroutine）。
func listenAndServe(srv *http.Server) net.Listener {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatal(err)
	}
	go srv.Serve(ln)
	return ln
}
