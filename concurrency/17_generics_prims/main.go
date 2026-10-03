// 项目 17：泛型并发原语库——Chan[T]/Pipe[T]/Merge[T]/Split[T] 四组件
// 与三级流水线 demo。
//
// 组件语义约定：
//   - Chan[T]：内置 channel + 幂等关闭。向已关闭通道 Send 与内置一致地
//     panic——库的关闭契约是"谁生产谁关闭，联结所有发送方后再 Close"
//     （Pipe/Merge/Split 用 wg.Wait 联结保证），不用 recover 兜底：
//     并发 Send/Close 是使用级数据竞态，-race 会直接判红。
//   - Pipe[In,Out]：N 个 worker 共享消费 in，对每个元素应用 fn 写入 out；
//     in 关闭且排空后自动关闭 out（下游的关闭是级联的）。
//   - Merge[T]：扇入。每路一个转发 goroutine，全部路关闭后关闭 out。
//   - Split[T]：扇出，按轮转（round-robin）把元素轮流分给 n 个分支；
//     某分支不消费会阻塞整个分发——它是负载均衡，不是广播。
//
// 运行方式：go run main.go。各阶段吞吐为实测值，每次不同；总量守恒
// 校验是固定断言。
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 组件库 ----------

// Chan[T]：带关闭保护的泛型通道句柄。
type Chan[T any] struct {
	ch     chan T
	closed atomic.Bool
}

func NewChan[T any](buffer int) *Chan[T] {
	return &Chan[T]{ch: make(chan T, buffer)}
}

// Send：阻塞发送。向已关闭通道发送会 panic（与内置 channel 一致），
// 关闭契约见包注释：联结所有发送方之后再 Close。
func (c *Chan[T]) Send(v T) {
	c.ch <- v
}

// Recv：阻塞接收。ok=false 表示已关闭且排空（与内置 `<-ch` 一致）。
func (c *Chan[T]) Recv() (v T, ok bool) {
	v, ok = <-c.ch
	return v, ok
}

// Close：幂等关闭。重复调用与并发发送都是安全的。
func (c *Chan[T]) Close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.ch)
	}
}

// Pipe[In, Out]：变换阶段。workers 个 goroutine 共享消费 in，out 在
// in 排空后自动关闭，因此管道可以一节一节串起来而无需手工收尾。
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

// Merge[T]：扇入。每路一个转发 goroutine，全部路排空后关闭 out。
func Merge[T any](ins ...*Chan[T]) *Chan[T] {
	out := NewChan[T](0)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, in := range ins {
		go func(in *Chan[T]) {
			defer wg.Done()
			for {
				v, ok := in.Recv()
				if !ok {
					return
				}
				out.Send(v)
			}
		}(in)
	}
	go func() { wg.Wait(); out.Close() }()
	return out
}

// Split[T]：扇出（轮转分发）。第 i 个元素给分支 i%n；某分支阻塞时
// 整个分发停摆，所以每个分支都要有人消费。
func Split[T any](in *Chan[T], n int) []*Chan[T] {
	outs := make([]*Chan[T], n)
	for i := range outs {
		outs[i] = NewChan[T](0)
	}
	go func() {
		i := 0
		for {
			v, ok := in.Recv()
			if !ok {
				break
			}
			outs[i%n].Send(v)
			i++
		}
		for _, o := range outs {
			o.Close()
		}
	}()
	return outs
}

// ---------- 三级流水线 demo ----------

func main() {
	const n = 200_000
	fmt.Println("== 第 1 步：组件清单 ==")
	fmt.Println("Chan[T] 通道 / Pipe[T,U] 变换 / Merge[T] 扇入 / Split[T] 扇出。")
	fmt.Println()

	fmt.Println("== 第 2 步：三级流水线：生成 → 平方(Pipe×4) → Split×2 → Merge ==")
	stageRate := make([]float64, 3)

	src := NewChan[int](64)
	go func() { // 生成阶段
		start := time.Now()
		for i := 1; i <= n; i++ {
			src.Send(i)
		}
		src.Close()
		stageRate[0] = float64(n) / time.Since(start).Seconds()
	}()

	sq := Pipe(src, 4, func(x int) int { return x * x }) // 阶段 1：变换

	brIn := NewChan[int](0)
	go func() { // 阶段 1 出口计速，原值转发进 Split
		start := time.Now()
		for {
			v, ok := sq.Recv()
			if !ok {
				break
			}
			brIn.Send(v)
		}
		brIn.Close() // 转发完必须关闭，下游才收得到结束信号
		stageRate[1] = float64(n) / time.Since(start).Seconds()
	}()

	merged := Merge(Split(brIn, 2)...) // 阶段 2：分流再合流

	start := time.Now()
	var cnt int64
	for {
		_, ok := merged.Recv()
		if !ok {
			break
		}
		cnt++
	}
	stageRate[2] = float64(cnt) / time.Since(start).Seconds()

	for i, r := range stageRate {
		fmt.Printf("阶段 %d 吞吐：%8.0f items/s（每次不同）\n", i, r)
	}
	fmt.Printf("总量守恒：收到 %d / 发出 %d（固定，必须相等）\n", cnt, n)
	fmt.Println()

	fmt.Println("== 第 3 步：值校验——带变换的完整守恒 ==")
	// 生成 → 平方 → Split×2 → 各支加偏移(1e6/2e6) → Merge。
	// 轮转分发下奇数序号进分支 0、偶数序号进分支 1，各 n/2 个，
	// 所以期望值可以精确算出：Σx² + 500*1e6 + 500*2e6。
	src2 := NewChan[int](0)
	sq2 := Pipe(src2, 2, func(x int) int64 { return int64(x) * int64(x) })
	parts := Split(sq2, 2)
	tagged := Merge(
		Pipe(parts[0], 2, func(x int64) int64 { return x + 1_000_000 }),
		Pipe(parts[1], 2, func(x int64) int64 { return x + 2_000_000 }),
	)
	go func() {
		for i := 1; i <= 1000; i++ {
			src2.Send(i)
		}
		src2.Close()
	}()
	var got int64
	for {
		v, ok := tagged.Recv()
		if !ok {
			break
		}
		got += v
	}
	want := int64(1000*1001*2001)/6 + 500*1_000_000 + 500*2_000_000
	fmt.Printf("Σ(x²+分支偏移) = %d（期望 %d，固定，必须相等）\n", got, want)
}
