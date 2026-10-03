// 项目 14：调度延迟测量——Runnable→Running 的 p50/p95/p99。
//
// 方法：runtime/metrics 的 /sched/latencies:seconds 直方图（运行时官方维护的
// "G 从就绪到被运行"延迟分布）。对空闲、满载两个场景各测一个窗口，
// 取窗口前后直方图差值，算分位数报表。
//
// 运行：go run ./14_sched_latency/
// 对照：go run ./14_sched_latency/ -trace=sched.out 生成 trace 后可用
//
//	go tool trace -pprof=sched sched.out > sched.pb.gz
//	go tool pprof -raw sched.pb.gz
//
// 得到样本总数与延迟总和（均值），做交叉验证（见 README 的方法差异说明）。
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/metrics"
	"runtime/trace"
	"sync"
	"sync/atomic"
	"time"
)

var sink atomic.Uint64 // 接收自旋结果，防止烧 CPU 的循环被优化掉

func main() {
	tracePath := flag.String("trace", "", "可选：把测量窗口录进该 trace 文件")
	flag.Parse()

	fmt.Printf("== 调度延迟测量（%s，GOMAXPROCS=%d）==\n", runtime.Version(), runtime.GOMAXPROCS(0))

	report := func(name string, s sampleStats) {
		fmt.Printf("  %-6s 样本=%6d  p50=%-10v p95=%-10v p99=%v\n",
			name, s.count, dur(s.p50), dur(s.p95), dur(s.p99))
	}

	if *tracePath != "" {
		f, err := os.Create(*tracePath)
		if err != nil {
			fmt.Fprintln(flag.CommandLine.Output(), "trace 文件创建失败:", err)
			return
		}
		trace.Start(f)
		idle := measureWindow(false)
		sat := measureWindow(true)
		trace.Stop()
		f.Close()
		report("空闲", idle)
		report("满载", sat)
		fmt.Printf("trace 已写入 %s，可用 go tool trace -pprof=sched 交叉验证\n", *tracePath)
		return
	}

	report("空闲", measureWindow(false))
	report("满载", measureWindow(true))
}

// measureWindow 测一个窗口内的调度延迟分布。
// saturated=true 时先用 GOMAXPROCS 个 CPU 密集 G 占满所有 P，
// 再在窗口里产生大量短命 G，放大"就绪后排不上 P"的延迟。
// 诚实预期：空闲窗口 p50 在微秒级；满载窗口 p50/p99 高 2~3 个数量级，
// 且具体数值每次运行不同。
func measureWindow(saturated bool) sampleStats {
	stop := burn(saturated)
	if saturated {
		time.Sleep(50 * time.Millisecond) // 让烧 CPU 的 G 先站稳所有 P
	}
	before := schedHist()
	spawnShort(40, 200) // 40 批 × 200 个短 G：8000 次 就绪→运行 迁移
	after := schedHist()
	stop()

	diff := histDelta(before, after)
	return sampleStats{
		count: total(diff),
		p50:   quantile(diff, 0.50),
		p95:   quantile(diff, 0.95),
		p99:   quantile(diff, 0.99),
	}
}

// spawnShort 分批创建短命 G。每批一个 wg：批次之间给调度器留呼吸口，
// 让"产生延迟"的迁移与"测量"发生在同一窗口。
func spawnShort(batches, perBatch int) {
	for i := 0; i < batches; i++ {
		var wg sync.WaitGroup
		wg.Add(perBatch)
		for j := 0; j < perBatch; j++ {
			go func() {
				defer wg.Done()
			}()
		}
		wg.Wait()
		time.Sleep(150 * time.Microsecond)
	}
}

// burn 满载时启动 GOMAXPROCS 个纯 CPU G；返回的 stop 会等它们退出。
func burn(on bool) func() {
	done := make(chan struct{})
	var wg sync.WaitGroup
	if on {
		n := runtime.GOMAXPROCS(0)
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					default:
						sink.Add(1)
					}
				}
			}()
		}
	}
	return func() {
		close(done)
		wg.Wait()
	}
}

// ---- runtime/metrics 直方图工具 ----

func schedHist() *metrics.Float64Histogram {
	samples := []metrics.Sample{{Name: "/sched/latencies:seconds"}}
	metrics.Read(samples)
	return samples[0].Value.Float64Histogram()
}

// histDelta 计算窗口内新增的分布（直方图只增不减，差值即窗口内迁移）。
func histDelta(a, b *metrics.Float64Histogram) *metrics.Float64Histogram {
	out := &metrics.Float64Histogram{
		Buckets: b.Buckets,
		Counts:  make([]uint64, len(b.Counts)),
	}
	for i := range b.Counts {
		if b.Counts[i] >= a.Counts[i] {
			out.Counts[i] = b.Counts[i] - a.Counts[i]
		}
	}
	return out
}

func total(h *metrics.Float64Histogram) uint64 {
	var n uint64
	for _, c := range h.Counts {
		n += c
	}
	return n
}

// quantile 在直方图上按累计计数定位分位桶，桶内线性插值。
func quantile(h *metrics.Float64Histogram, q float64) float64 {
	n := total(h)
	if n == 0 {
		return 0
	}
	target := q * float64(n)
	var cum uint64
	for i, c := range h.Counts {
		cum += c
		if float64(cum) >= target {
			upper := h.Buckets[i]
			if math.IsInf(upper, 1) {
				return upper
			}
			lower := math.Inf(-1)
			if i > 0 {
				lower = h.Buckets[i-1]
			}
			prev := float64(cum - c)
			frac := (target - prev) / float64(c)
			return lower + frac*(upper-lower)
		}
	}
	return 0
}

func dur(sec float64) time.Duration {
	return time.Duration(sec * float64(time.Second)).Round(time.Microsecond)
}

type sampleStats struct {
	count         uint64
	p50, p95, p99 float64
}
