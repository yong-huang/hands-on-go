package main

// 10_gomaxprocs：GOMAXPROCS 与并行度控制
//
// 并发是"结构"（goroutine 交织执行），并行是"执行"（多个核同时算）。
// GOMAXPROCS 决定 Go 运行时最多用多少个 OS 线程同时执行 goroutine。
// 本程序用一个固定的 CPU 密集任务（递归 fib(30)，总工作量恒定），分别把
// GOMAXPROCS 设为 1/2/4/8，测量总耗时并算加速比：
//   - goroutine 数量不变（=P），每个分到 1/P 的工作量
//   - P=1 时所有 goroutine 串行排队 → 最慢
//   - P 越大（且核够多）越接近真并行 → 越快
//   - P 超过物理核数不会更快，可能因调度开销略微变慢（如实输出）
//
// 运行：go run main.go          （人读输出）
//       go run main.go -json    （供 viz.py 采集数据画柱状图）

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// fib 递归计算斐波那契：纯 CPU 计算，无 IO、无锁，是理想的并行度压测负载
func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

type result struct {
	P        int     `json:"p"`
	Ms       float64 `json:"ms"`
	Speedup  float64 `json:"speedup"`
}

func main() {
	jsonOut := flag.Bool("json", false, "以 JSON 输出测量数据（供 viz.py 使用）")
	flag.Parse()

	procs := []int{1, 2, 4, 8}
	var results []result
	var base float64

	for _, p := range procs {
		runtime.GOMAXPROCS(p) // 设置 P 的数量：同时执行 goroutine 的线程数上限

		const unit = 30 // fib(30) 单次约 10ms，×次数足够稳定又不太久
		times := 32 / p  // 总工作量固定 32 次 fib，P 个 goroutine 平分，各做 32/P 次
		var wg sync.WaitGroup
		start := time.Now()
		for i := 0; i < p; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < times; j++ {
					_ = fib(unit) // 结果丢弃：只关心"算完花了多久"
				}
			}()
		}
		wg.Wait()
		ms := float64(time.Since(start).Microseconds()) / 1000.0
		if p == 1 {
			base = ms
		}
		results = append(results, result{P: p, Ms: ms, Speedup: base / ms})
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"numcpu": runtime.NumCPU(), "results": results})
		return
	}

	fmt.Println("========== GOMAXPROCS 并行度实测（CPU 密集：总工作量恒定 32 次 fib(30)） ==========")
	for _, r := range results {
		bar := strings.Repeat("█", int(r.Ms/60)+1)
		fmt.Printf("P=%d  %8.1f ms  加速比 %.2fx  %s\n", r.P, r.Ms, r.Speedup, bar)
	}
	fmt.Println("\n结论：P=1 时 goroutine 只能并发交织、不能并行，最慢；")
	fmt.Println("P 增大到接近物理核数时接近线性加速；P 超过核数无收益甚至更慢。")
	fmt.Println("本机 runtime.NumCPU() =", runtime.NumCPU(), "（GOMAXPROCS 默认值，结果与机器相关，每次测量有波动）")
}
