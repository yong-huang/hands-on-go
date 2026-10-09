// loadtest：全链路压测——并发 worker 持续打目标端点，输出成功率与延迟分位数。
//
//	用法：go run ./20_release/loadtest -url http://127.0.0.1:8080/healthz \
//	        -concurrency 100 -duration 60s
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/healthz", "目标 URL")
	conc := flag.Int("concurrency", 100, "并发 worker 数")
	dur := flag.Duration("duration", 60*time.Second, "压测时长")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: *conc}}
	var mu sync.Mutex
	var total, failed int
	firstErr := "" // 首错采样：失败分布的入口线索
	latencies := make([]time.Duration, 0, 1<<20)

	deadline := time.Now().Add(*dur)
	var wg sync.WaitGroup
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				t0 := time.Now()
				resp, err := client.Get(*url)
				d := time.Since(t0)
				// 先读干再关闭：连接才能回池复用（未读透就 Close 会被丢弃重拨）
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				mu.Lock()
				total++
				if err != nil || resp.StatusCode != http.StatusOK {
					failed++
					if firstErr == "" {
						if err != nil {
							firstErr = err.Error()
						} else {
							firstErr = fmt.Sprintf("status %d", resp.StatusCode)
						}
					}
				}
				latencies = append(latencies, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(q float64) time.Duration {
		return latencies[int(float64(len(latencies)-1)*q)]
	}
	rate := float64(total) / dur.Seconds()
	success := 100 * float64(total-failed) / float64(total)
	fmt.Printf("总请求=%d 失败=%d 成功率=%.3f%% 吞吐=%.0f req/s\n", total, failed, success, rate)
	if firstErr != "" {
		fmt.Printf("首错采样: %s\n", firstErr)
	}
	fmt.Printf("p50=%v p95=%v p99=%v max=%v\n",
		pct(0.50), pct(0.95), pct(0.99), latencies[len(latencies)-1])
	if success < 99.9 {
		fmt.Fprintln(os.Stderr, "成功率低于 99.9%——压测不达标")
		os.Exit(1)
	}
}
