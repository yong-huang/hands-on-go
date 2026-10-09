// 实验 14：/metrics 与 pprof 端点——看得见的两大标准件。
//
//	第 1 步：/metrics 主端口——RED 指标（请求数/延迟直方图）随请求实时增长
//	第 2 步：直方图分位数对照组——p50/p99 的估计值与真实延迟对照入档
//	第 3 步：pprof 独立端口 19639 可达，主端口 19622 上不存在（404）
//
// pprof 挂独立端口是安全纪律：性能剖析端点能泄露内存里的函数名、栈、
// 连接信息——它应该只有运维网络能碰到，而不是公网用户。
//
// 运行方式：go run .（跑完自动退出）；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

const (
	mainPort  = "127.0.0.1:19622"
	pprofPort = "127.0.0.1:19639"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	metrics := NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond) // 模拟业务耗时（落在 5-10ms 桶）
		fmt.Fprintln(w, "done")
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		io.WriteString(w, metrics.Render())
	})
	// 主端口故意不挂 /debug/pprof——它只活在独立端口上

	// pprof 独立端口：同进程，不同监听。pprof 的四个核心 handler 手工挂载，
	// 不用 pprof.Handler() 整包（它还会带 /debug/pprof/ 的 HTML 索引页）
	pprofMux := http.NewServeMux()
	pprofMux.HandleFunc("GET /debug/pprof/", pprof.Index)
	pprofMux.HandleFunc("GET /debug/pprof/heap", pprof.Index)
	pprofMux.HandleFunc("GET /debug/pprof/goroutine", pprof.Index)
	pprofMux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)

	mainLn, err := net.Listen("tcp", mainPort)
	if err != nil {
		fatal(err)
	}
	mainSrv := &http.Server{Handler: metrics.ObserveRequest(mux), ReadHeaderTimeout: 5 * time.Second}
	go mainSrv.Serve(mainLn)

	pprofLn, err := net.Listen("tcp", pprofPort)
	if err != nil {
		fatal(err)
	}
	pprofSrv := &http.Server{Handler: pprofMux, ReadHeaderTimeout: 5 * time.Second}
	go pprofSrv.Serve(pprofLn)
	waitHealthy()

	client := &http.Client{Timeout: 2 * time.Second}

	fmt.Println("== 第 1 步：/metrics——RED 指标随请求增长 ==")
	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://" + mainPort + "/api/v1/work")
		if err != nil {
			fatal(err)
		}
		resp.Body.Close()
	}
	printMetrics(client)

	fmt.Println("== 第 2 步：直方图分位数对照——估计值 vs 实测 ==")
	// 对照组：注入已知延迟的样本（100 个 ~4ms + 2 个 ~300ms），
	// p50 应落在 5ms 桶附近、p99 应落在 250-500ms 桶附近
	for i := 0; i < 100; i++ {
		metrics.record("/synthetic", 200, 4*time.Millisecond)
	}
	metrics.record("/synthetic", 200, 300*time.Millisecond)
	metrics.record("/synthetic", 200, 310*time.Millisecond)
	p50 := metrics.Quantile(0.50)
	p99 := metrics.Quantile(0.99)
	fmt.Printf("  合成样本：100 个 4ms + 2 个 ~305ms → p50=%.1fms（want ≈4-5） p99=%.1fms（want ≈250-300）\n",
		p50*1000, p99*1000)

	fmt.Println("== 第 3 步：pprof 端口隔离 ==")
	resp, err := client.Get("http://" + pprofPort + "/debug/pprof/heap?debug=1")
	if err != nil {
		fatal(err)
	}
	head, _ := readN(resp.Body, 60)
	fmt.Printf("  19639 /debug/pprof/heap → %d，%q...\n", resp.StatusCode, head)
	resp.Body.Close()
	resp, err = client.Get("http://" + mainPort + "/debug/pprof/heap")
	if err != nil {
		fatal(err)
	}
	resp.Body.Close()
	fmt.Printf("  19622 /debug/pprof/heap → %d（主端口不可达——安全隔离）\n", resp.StatusCode)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mainSrv.Shutdown(ctx)
	pprofSrv.Shutdown(ctx)
}

func printMetrics(client *http.Client) {
	resp, err := client.Get("http://" + mainPort + "/metrics")
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, line := range stringsLines(string(body)) {
		if strings.HasPrefix(line, "http_requests_total") ||
			strings.HasPrefix(line, "http_request_duration_seconds_count") {
			fmt.Println(" ", line)
		}
	}
	fmt.Printf("  （Content-Type=%q；直方图桶与 sum 行见 go test 输出）\n", resp.Header.Get("Content-Type"))
}

func waitHealthy() {
	client := &http.Client{Timeout: time.Second}
	for i := 0; i < 100; i++ {
		if resp, err := client.Get("http://" + mainPort + "/healthz"); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Errorf("服务 2s 内未就绪"))
}

func readN(r io.Reader, n int) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(n)))
	return string(b), err
}

func stringsLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
