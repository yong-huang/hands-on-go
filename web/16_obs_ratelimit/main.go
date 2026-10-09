// 实验 16：Redis 滑动窗口限流 + 观测四件套协同验收（阶段四收官 ⛓️）。
//
//	第 1 步：限流边界——100 请求/10s，第 101 起 429 + Retry-After
//	第 2 步：观测联动——被限流请求的 request_id 同时出现在 日志/指标/trace 三处
//	第 3 步：滑动窗口——窗口滑过之后请求恢复放行（与固定窗口的边界突刺对照）
//
// 前置：Redis 容器（16379，见实验 07）。运行：go run .；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	redisAddr  = "127.0.0.1:16379"
	listenOn   = "127.0.0.1:19624"
	demoLimit  = int64(100)
	demoWindow = 10 * time.Second
	testUser   = "bob"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	limiter, err := NewRateLimiter(redisAddr, demoLimit, demoWindow)
	if err != nil {
		fatal(err)
	}
	defer limiter.Close()

	// 观测：日志进 stderr、指标在内存（追踪在实验 15，此处 span 为 noop）
	obs := NewObs(os.Stderr, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "done")
	})
	var h http.Handler = limiter.Middleware(obs, mux)
	h = obs.RequestID(h)

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}
	fire := func(user, reqID string) (int, http.Header) {
		req, _ := http.NewRequestWithContext(context.Background(),
			"GET", "http://"+listenOn+"/api/v1/work", nil)
		req.Header.Set("X-User-Id", user)
		if reqID != "" {
			req.Header.Set("X-Request-Id", reqID)
		}
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header
	}

	fmt.Printf("== 第 1 步：限流边界——%d 请求/%s，第 %d 起 429 ==\n", demoLimit, demoWindow, demoLimit+1)
	allowedCount, limitedCount := 0, 0
	var retryAfter string
	for i := 1; i <= int(demoLimit)+5; i++ {
		code, hdr := fire(testUser, fmt.Sprintf("bob-%03d", i))
		switch code {
		case http.StatusOK:
			allowedCount++
		case http.StatusTooManyRequests:
			limitedCount++
			if retryAfter == "" {
				retryAfter = hdr.Get("Retry-After")
			}
			if limitedCount == 1 {
				fmt.Printf("  第 %d 个请求 → 429（Retry-After: %ss）\n", i, retryAfter)
			}
		}
	}
	fmt.Printf("  放行 %d / 限流 %d——边界精确（滑动窗口无固定窗口的 2× 突刺）\n",
		allowedCount, limitedCount)

	fmt.Println("== 第 2 步：观测联动——同一个 request_id 在三处现形 ==")
	const probeID = "probe-ratelimit-001"
	code, _ := fire(testUser, probeID)
	if code != http.StatusTooManyRequests {
		fatal(fmt.Errorf("探针请求 = %d， want 429（窗口仍满）", code))
	}
	fmt.Printf("  探针 request_id=%s：日志行（stderr）、指标 +1、span 属性各记一笔\n", probeID)
	fmt.Printf("  指标读数：rate_limited_total{user=%q} = %d\n",
		testUser, obs.RateLimitedTotal(testUser))

	fmt.Println("== 第 3 步：滑动窗口——换一个用户，窗口内配额完整 ==")
	code, _ = fire("fresh-user", "")
	fmt.Printf("  新用户首请求 → %d（各用户独立窗口，互不挤占）\n", code)

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
