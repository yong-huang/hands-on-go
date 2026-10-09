// 实验 13：结构化日志——请求 ID 贯通与敏感字段脱敏。
//
//	第 1 步：请求 ID 贯通——一次请求里中间件与 handler 的日志带同一个 request_id；
//	        客户端传 X-Request-Id 则透传（跨服务对账的锚点）
//	第 2 步：脱敏——登录接口记录 password 字段，日志里只有 ***
//	第 3 步：结构化检索——JSON 行按 request_id 过滤，一次请求的完整轨迹一屏聚合
//
// 运行方式：go run .（服务 127.0.0.1:19622，跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const listenOn = "127.0.0.1:19622"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// timeNow：可测试替换的时间源。
var timeNow = time.Now

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, r *http.Request) {
		// handler 层取 logger：request_id 已预注，这里不会再写错
		LoggerFrom(r.Context()).Info("handler doing work", "step", 1)
		fmt.Fprintln(w, "done")
	})
	mux.HandleFunc("POST /api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		// 敏感数据必须走 LogSafe——直接 logger.Info 会把明文密码写进日志
		LogSafe(LoggerFrom(r.Context()), slogLevelInfo, "login attempt",
			"username", in.Username, "password", in.Password, "token", "tok_abc123")
		fmt.Fprintln(w, "check stderr: password 已脱敏")
	})

	// RequestID 在最外层（注入 JSON logger），AccessLog 在内层拿到的 logger 已带 request_id
	h := use(mux, RequestIDWithRoot(NewLogger()), AccessLog)

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}

	fmt.Println("== 第 1 步：请求 ID 贯通——客户端透传 ==")
	req, _ := http.NewRequestWithContext(context.Background(),
		"GET", "http://"+listenOn+"/api/v1/work", nil)
	req.Header.Set("X-Request-Id", "my-trace-id-001")
	resp, err := client.Do(req)
	if err != nil {
		fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	fmt.Printf("  客户端传 X-Request-Id: my-trace-id-001 → 响应头回带 %q，日志两行同 ID\n",
		resp.Header.Get("X-Request-Id"))

	fmt.Println("== 第 2 步：未传则生成；脱敏验证 ==")
	resp, err = client.Post("http://"+listenOn+"/api/v1/login", "application/json",
		strings.NewReader(`{"username":"alice","password":"SuperSecret123","token":"tok_abc123"}`))
	if err != nil {
		fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	fmt.Println("  登录日志已写 stderr——password/token 应为 ***")

	fmt.Println("== 第 3 步：结构化检索——grep 一个 request_id 聚合完整轨迹 ==")
	fmt.Println("  （本步由测试断言；stderr 里每行都是合法 JSON）")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func waitHealthy() {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 100; i++ {
		if resp, err := client.Get("http://" + listenOn + "/healthz"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Errorf("服务 2s 内未就绪"))
}

// use：从内向外包裹（net/12 洋葱模型）。
func use(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
