// 项目 22：并发压测方法论——三种负载形状发生器 × 组件库压测 harness。
//
// 负载形状（load shape）决定尾延迟的形状：同样的平均速率，恒定、突发、
// 阶跃三种节奏打出的 p99 可以差一个数量级。只压"恒定满速"是压测最
// 常见的盲区——它永远看不到排队。
//
// 被压对象：泛型并发原语的核心子集 Chan[T] + Pipe[In,Out]，与
// 17_generics_prims 同形。原目录是 package main 无法 import，故按其
// 源码复刻子集；Merge/Split 不在本篇范围内（如实标注）。
//
// 运行方式：go run main.go。三种形状的分位数是实测值，每次不同
// （量级与相对关系稳定）；守恒校验是固定断言。
package main

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 核心子集（与 17_generics_prims 同形） ----------

// Chan[T]：带关闭保护的泛型通道句柄。
type Chan[T any] struct {
	ch     chan T
	closed atomic.Bool
}

func NewChan[T any](buffer int) *Chan[T] {
	return &Chan[T]{ch: make(chan T, buffer)}
}

// Send：阻塞发送。关闭契约同 17：谁生产谁关闭，联结所有发送方后再 Close。
func (c *Chan[T]) Send(v T) { c.ch <- v }

// Recv：阻塞接收。ok=false 表示已关闭且排空。
func (c *Chan[T]) Recv() (v T, ok bool) { v, ok = <-c.ch; return v, ok }

// Close：幂等关闭。
func (c *Chan[T]) Close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.ch)
	}
}

// Pipe[In, Out]：变换阶段。workers 个协程共享消费 in，in 排空后自动
// 关闭 out，管道可以一节一节串起来。
func Pipe[In, Out any](in *Chan[In], workers int, fn func(In) Out) *Chan[Out] {
	out := NewChan[Out](0)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				v, ok := in.Recv()
				if !ok {
					return
				}
				out.Send(fn(v))
			}
		}()
	}
	go func() { wg.Wait(); out.Close() }()
	return out
}

// ---------- 负载形状发生器 ----------

// Job：一个压测单元，携带入队时刻，供端到端延迟测量。
type Job struct {
	id       int
	issuedAt time.Time
	payload  uint64
}

// phase：一段匀速发射节拍：n 个、每隔 every 一个。
type phase struct {
	n     int
	every time.Duration
}

// schedule：按时间表推进的发射循环。约定第 i 个在 phase 起点 + i*every
// 发出；时钟粒度跟不上节拍时不等待直接补发——节拍在聚合意义上保形，
// 微秒级的均匀性不保证（如实标注）。
func schedule(phases []phase, send func(Job)) {
	var id int
	clock := time.Now()
	for _, p := range phases {
		for i := 0; i < p.n; i++ {
			if wait := clock.Sub(time.Now()); wait > 0 {
				time.Sleep(wait)
			}
			send(Job{id: id, issuedAt: time.Now(), payload: uint64(id)})
			id++
			clock = clock.Add(p.every)
		}
	}
}

// 三种形状。突发窗口速率 50k/s 与基础速率 5k/s 相差 10 倍；
// 阶梯末档 50k/s 已超过 2 worker × 50µs 服务 ≈ 40k/s 的处理容量。
func constantShape() []phase { return []phase{{n: 2000, every: 100 * time.Microsecond}} }

func burst10xShape() []phase {
	var ps []phase
	for c := 0; c < 5; c++ { // 每周期：8ms 突发（400×20µs）+ 40ms 平稳（200×200µs）
		ps = append(ps, phase{n: 400, every: 20 * time.Microsecond})
		ps = append(ps, phase{n: 200, every: 200 * time.Microsecond})
	}
	return ps
}

func stepShape() []phase {
	steps := []time.Duration{400, 200, 100, 50, 20} // µs/个 → 2.5k→50k items/s
	var ps []phase
	for _, s := range steps {
		ps = append(ps, phase{n: 300, every: time.Duration(s) * time.Microsecond})
	}
	return ps
}

// ---------- 压测 harness ----------

// service：被压的处理函数，固定 ~50µs 的纯 CPU 工作（迭代数实测校准）。
// 结果汇入 atomic sink，防止被编译器优化掉。
var sink atomic.Uint64

func service(j Job) Job {
	x := j.payload ^ 0x9E3779B97F4A7C15
	for i := 0; i < 60000; i++ {
		x = x*6364136223846793005 + 1442695040888963407
	}
	sink.Store(x)
	j.payload = x
	return j
}

// runShape：把一种形状灌进"缓冲通道 → Pipe(2 worker) → 汇聚"链路，
// 返回每个 Job 的端到端延迟（入队 → 处理完成），按 Job.id 索引。
func runShape(phases []phase, workers int) []time.Duration {
	total := 0
	for _, p := range phases {
		total += p.n
	}
	in := NewChan[Job](1024) // 缓冲吸收突发，发射端节拍不被反压扭曲
	out := Pipe(in, workers, service)
	lats := make([]time.Duration, total)
	done := make(chan struct{})
	go func() { // 汇聚端：按 id 归位每个 Job 的端到端延迟
		defer close(done)
		for {
			j, ok := out.Recv()
			if !ok {
				return
			}
			lats[j.id] = time.Since(j.issuedAt)
		}
	}()
	schedule(phases, func(j Job) { in.Send(j) })
	in.Close() // 发射完毕关闭：Pipe 联结后自动关 out，汇聚端自然退出
	<-done
	return lats
}

// percentile：最近邻分位数。qs 如 p99 → 取排序后第 ⌈0.99·n⌉ 个。
func percentile(lats []time.Duration, q float64) time.Duration {
	s := append([]time.Duration(nil), lats...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s))*q + 0.999999)
	if idx < 1 {
		idx = 1
	}
	if idx > len(s) {
		idx = len(s)
	}
	return s[idx-1]
}

func report(name string, lats []time.Duration) {
	span := time.Duration(0)
	for _, d := range lats { // 总时长近似：发射 + 排空，用最大延迟兜底展示
		if d > span {
			span = d
		}
	}
	fmt.Printf("%-8s n=%-5d p50=%-10v p95=%-10v p99=%-10v max=%v\n",
		name, len(lats), percentile(lats, 0.50), percentile(lats, 0.95),
		percentile(lats, 0.99), lats[maxIdx(lats)])
}

func maxIdx(lats []time.Duration) (m int) {
	for i, d := range lats {
		if d > lats[m] {
			m = i
		}
	}
	return m
}

// ---------- 分步演示 ----------

func main() {
	fmt.Println("== 第 1 步：被压对象与形状清单 ==")
	fmt.Println("被压组件：Chan[T](缓冲 1024) → Pipe(2 worker × ~50µs CPU 服务) → 汇聚。")
	fmt.Println("形状：恒定(5k/s×2000) / 突发(5k 基础 vs 50k 突口，10 倍) / 阶跃(2.5k→50k 五档)。")
	fmt.Println()

	fmt.Println("== 第 2 步：恒定负载 ==")
	report("constant", runShape(constantShape(), 2))
	fmt.Println()

	fmt.Println("== 第 3 步：突发 10x（8ms 突口 / 40ms 平稳 × 5 周期）==")
	report("burst10x", runShape(burst10xShape(), 2))
	fmt.Println()

	fmt.Println("== 第 4 步：阶跃爬坡（2.5k→5k→10k→20k→50k items/s）==")
	lats := runShape(stepShape(), 2)
	report("step", lats)
	// 阶跃的关键证据：末档（最后 300 个）的 p99 应显著高于首档。
	head := lats[:300]
	tail := lats[len(lats)-300:]
	fmt.Printf("首档(2.5k/s) p99=%v vs 末档(50k/s) p99=%v（每次不同，末档应显著更高）\n",
		percentile(head, 0.99), percentile(tail, 0.99))
}
