// 实验 15：OTel 链路追踪——父 HTTP span、子 DB span、透传与采样。
//
//	第 1 步：层级结构——一次请求产生 父 span(GET /work) + 子 span(db.query)，
//	        同 TraceID、子挂父
//	第 2 步：透传——外部传入 traceparent，服务端延续同一 TraceID（跨服务接力）
//	第 3 步：采样 10%——100 次请求，导出的 trace 数量在 10 上下（ParentBased 比例采样）
//
// 演示模式把 span 输出到 stdout（json 行，无 collector 依赖）；
// 测试用内存导出器做精确断言。
//
// 运行方式：go run .（服务 127.0.0.1:19623，跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	listenOn   = "127.0.0.1:19623"
	sampleRate = 0.1
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// newStdoutExporter：span 输出 stdout（json 行），演示模式零依赖。
func newStdoutExporter() (sdktrace.SpanExporter, error) {
	return stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
}

func main() {
	tp, err := SetupTracing(sampleRate, nil)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, r *http.Request) {
		// handler 里再开子 span：父 = HTTP span（ctx 由中间件传入）
		_, done := DBQuerySpan(r.Context(), tp, "SELECT url, title FROM bookmarks")
		defer done()
		fmt.Fprintln(w, "done")
	})
	h := TracingMiddleware(tp)(mux)

	ln, err := net.Listen("tcp", listenOn)
	if err != nil {
		fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}

	fmt.Println("== 第 1 步：父 HTTP span + 子 DB span，同 TraceID ==")
	resp, err := client.Get("http://" + listenOn + "/api/v1/work")
	if err != nil {
		fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	fmt.Println("  span 已导出 stdout——层级关系由测试断言（TraceID 相同、子挂父）")

	fmt.Println("== 第 2 步：透传——外部 traceparent 延续同一 TraceID ==")
	req, _ := http.NewRequestWithContext(context.Background(),
		"GET", "http://"+listenOn+"/api/v1/work", nil)
	req.Header.Set("traceparent",
		BuildTraceparent("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"))
	resp, err = client.Do(req)
	if err != nil {
		fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	fmt.Printf("  传入 trace ID 4bf92f35...4736 → 服务端 span 延续它（测试断言）\n")

	fmt.Printf("== 第 3 步：采样 10%%——100 次请求，导出 ~10 条 trace ==\n")
	for i := 0; i < 100; i++ {
		// 不带 traceparent：真根 span，按 10% 比例决定采样
		req, _ := http.NewRequestWithContext(context.Background(),
			"GET", "http://"+listenOn+"/api/v1/work", nil)
		resp, err := client.Do(req)
		if err != nil {
			fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	fmt.Printf("  导出数量由内存导出器断言（区间 3~25；概率波动入档）\n")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	_ = tp.ForceFlush(context.Background())
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

// newTraceID/newSpanID：随机 16/8 字节 hex（模拟上游服务生成）。
func newTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(b)
}

func newSpanID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		fatal(err)
	}
	return hex.EncodeToString(b)
}
