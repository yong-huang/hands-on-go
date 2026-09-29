# 20 · 扇出扇入：一个 channel 喂多个 worker，结果合流回一个

> 扇出扇入（fan-out / fan-in）是给流水线提速的标准手段：fan-out 让多个 worker 共享消费同一个源 channel，把工作分摊出去；fan-in 再把多路结果合并回一个 channel 统一处理。难点不在分发，而在收尾——合并 channel 该由谁、在什么时机 close。读完本篇你将掌握用 `sync.WaitGroup` 把关的标准收尾写法、worker 数量的选择依据，以及若干典型坑的成因与解法。

## Background

流水线模式（见 [19_pipeline](../19_pipeline/)）的每个 stage 只有一个消费者，这意味着一个 stage 慢，整条线就慢。生产速度超过单个消费者的处理速度时，直觉做法是"多开几个消费者"分摊工作。

问题随之而来：多个消费者各自产出结果，下游却只想对着一个 channel 用 `for range` 读。要有人把多路合成一路，还要在**所有输入都结束后**关闭这个合并 channel——关早了丢数据，不关则 range 永远不退出。

Go 的 channel 语义恰好为前半个问题提供了免费方案：一条消息只会被一个接收者拿到。多个 goroutine 读同一个 channel，元素不会重复、不会丢失。真正需要设计的只剩收尾，这正是本实验的主题。

## What

fan-out（扇出）是一种分发方式：让多个 worker goroutine 共享消费同一个源 channel，每个元素只投递给其中一个 worker。fan-in（扇入）是一种合流方式：为每个输入 channel 起一个转发 goroutine，把多路输出写进同一个合并 channel。

可以把 fan-out 想象成银行大厅的取号机配多个柜台：每张号票只会被一个柜台叫到，不会有两个柜台处理同一单。但和真实叫号不同的是，Go 不承诺按先来后到决定哪个柜台拿到哪张票——运行时从等待的接收者中挑选，顺序不可预测。

**心智模型一句话**：fan-out 靠"一条消息只被一个 worker 收到"的天然语义分发。

fan-in 靠 `sync.WaitGroup`（等待一组 goroutine 全部完成的标准库计数器）等齐所有输入 channel 的 close，再由 merge（合并 channel 的唯一生产者）执行 `close(merged)`。

## When to Use

- 给流水线提速时：某一级处理慢成为瓶颈，就把这一级 fan-out 给多个 worker、用 merge 汇回一条通道，其余各级不用动。这是流水线与扇出扇入最常见的组合方式。
- 消费速度跟不上生产速度时：日志解析、消息消费这类"一个源持续产出、单消费者吃不下"的场景，多个 worker 分摊能直接提高吞吐。
- 需要限制并发度时：fan-out 的 worker 数量由你决定，天然就是一个并发度上限。

何时不用它：

- 任务量小、单消费者绰绰有余：直接单 worker，合流与关闭的复杂度不值得
- 需要的是广播而不是分摊：fan-out 一条消息只被一个 worker 收到，不会复制；要"每个消费者都收到全部消息"，得为每个消费者单独发一份或另行实现（见 [15_select](../15_select/)）
- 任务来自固定队列、只需限制并发量：worker pool（固定 N 个 worker 抢任务队列）更直接

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 扇出/扇入 | 多 worker 消费同一 channel，结果合流回一路 | 流水线某级慢、要分摊并合流 |
| Worker pool | 固定 N 个 worker 抢固定任务队列，强调限制并发量 | 任务来自队列、只需控制并发上限（见 [11_worker_pool](../11_worker_pool/)） |
| 广播式多 channel | 每个消费者各有一条 channel，各收全量 | 每个下游都需要完整数据 |
| 单 worker | 无分发无合流 | 任务量小，简单直接 |

## Quick Start

前置条件：已安装 Go 并可用 `go run`，无第三方依赖。

```bash
cd concepts/20_fan_in_out
go run main.go
```

真实输出节选（**worker 完成顺序每次运行不同**，属于正常调度抖动；末行恒定）：

```text
worker 2 处理 1 -> 1
worker 2 处理 4 -> 16
worker 2 处理 5 -> 25
worker 3 处理 2 -> 4
worker 1 处理 3 -> 9
worker 3 处理 9 -> 81
worker 2 处理 6 -> 36
worker 1 处理 7 -> 49
worker 3 处理 8 -> 64
共收到 9 个平方值，总和 = 285（= 1²+2²+…+9²）
```

9 个元素一条不漏，平方和恒为 285——谁处理了哪几个元素每次都不同，但结果确定。

接线代码在 `main` 里，这段在做什么：3 个 worker 绑定同一个源 channel（fan-out），再用 merge 把三路结果合成一路（fan-in），最后统一求和：

```go
	// fan-out：3 个 worker 共享消费同一个 source
	workers := make([]<-chan int, 0, 3)
	for id := 1; id <= 3; id++ {
		workers = append(workers, worker(id, src))
	}

	// fan-in：合流后统一消费
	merged := merge(workers...)

	sum := 0
	count := 0
	for v := range merged {
		sum += v
		count++
	}
```

## How It Works

按数据流经的顺序拆开：source 产出 → 3 个 worker 分摊 → merge 合流 → main 汇总。

### source：产完即 close

这段在做什么：产出 1..9 后 close 源 channel，close 是下游 `for range` 退出的信号：

```go
// source 生产 1..9，产完即 close——close 是下游 for range 退出的信号。
func source() <-chan int {
	out := make(chan int)
	go func() {
		for i := 1; i <= 9; i++ {
			out <- i
		}
		close(out)
	}()
	return out
}
```

### fan-out：共享消费即分发

3 个 worker goroutine 拿到的是**同一个** source channel。Go 运行时对 channel 接收的串行化保证：每个元素只投递给其中一个等待中的接收者。

不需要任何分发器代码，`worker(1, src)`、`worker(2, src)`、`worker(3, src)` 就是 fan-out 全部：

```go
// worker 从共享的 source channel 消费（fan-out 的关键：3 个 worker 绑定同一个 channel）。
// 一条消息只会被其中一个 worker 的 <-in 接走，各 worker 互不重复。
// 处理完（for range 因 close 退出）后关闭自己的结果 channel。
func worker(id int, in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for v := range in {
			fmt.Printf("worker %d 处理 %d -> %d\n", id, v, v*v)
			out <- v * v
		}
	}()
	return out
}
```

输出里每一行 `worker N 处理 x -> y` 就来自这个循环；同一元素不会出现在两个 worker 的行里，这是"一条消息只被一个 worker 收到"的直接体现。

### fan-in：多路转发 + WaitGroup 把关

这段在做什么：每个输入 channel 配一个转发 goroutine，全部转空后由 merge 这个唯一生产者 close 合并通道：

```go
// merge 扇入：把多个结果 channel 合并为一个。
// 每个 channel 配一个 goroutine 转发，wg.Done 在转发完（即该 channel close）后调用；
// wg.Wait() 返回说明所有输入都已排空，此时才安全 close(merged)——
// 谁生产谁 close，merge 是 merged 的唯一生产者，close 权归它。
func merge(ins ...<-chan int) <-chan int {
	merged := make(chan int)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, in := range ins {
		go func(c <-chan int) {
			defer wg.Done()
			for v := range c {
				merged <- v
			}
		}(in)
	}
	go func() {
		wg.Wait()
		close(merged)
	}()
	return merged
}
```

`merge` 为每个输入 channel 起一个转发 goroutine：`for v := range c { merged <- v }`，channel 被 close 后 range 退出、`wg.Done()`。

最后另一个 goroutine 执行 `wg.Wait(); close(merged)`——`Wait` 返回意味着 3 路输入都已排空，此刻 close 才不丢数据。

Quick Start 输出末行的"共收到 9 个"，正说明 merged 关闭前 9 个值全部送达。

### 谁生产谁 close

`merged` 只有一个生产者（merge 内部的转发 goroutine 们），所以 close 权归 merge 所有。

worker 各自 close 自己的结果 channel（`defer close(out)`），source close 源 channel——每个 channel 都由它唯一的生产者关闭，这是关闭语义的铁律。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func source() <-chan int` | 返回只读 channel | 把"只能收"写进类型，防止调用方误 close/发送 |
| `worker(id, src)` × 3 | 多 goroutine 共享同一接收 channel | fan-out 本体：一条消息只被一个 worker 拿到 |
| `defer close(out)` | worker 处理完关闭自己的结果 chan | 触发 merge 端 `for range` 退出 |
| `wg.Add(len(ins))` | 按输入路数计数 | 每路一个转发 goroutine，Done 一次 |
| `go func(){ wg.Wait(); close(merged) }()` | 等 3 路全空再 close | fan-in 的收尾关键：关早丢数据，不关 range 不退出 |

## Pitfalls & Q&A

踩坑清单，每条按"现象 → 原因 → 解法"展开：

- **在 worker 里 close 源 channel**。现象：panic: close of closed channel。原因：多个 worker 同时消费共享输入，谁都不该 close 它——close 权属于唯一生产者 source。解法：worker 只 close 自己的结果 channel。
- **`wg.Wait()` 后忘记 close(merged)**。现象：main 的 `for range merged` 永远等下去，程序迟迟不退出。原因：range 只有收到 close 才结束，goroutine 泄漏。解法：把 `wg.Wait(); close(merged)` 成对写进同一个 goroutine。
- **先 close(merged) 再等 WaitGroup**。现象：panic: send on closed channel。原因：转发 goroutine 还在往已关闭的 channel 发送。解法：close 必须放在 `Wait` 返回之后。
- **for 循环变量直接传给闭包**（Go 1.22 之前）。现象：worker 编号全是循环最后一个值。原因：闭包捕获同一个变量。解法：用参数传入（本实验 `func(id int, ...)`）或升级到 Go 1.22+。
- **以为 fan-out 会复制消息**。现象：期待每个 worker 都拿到全部数据，实际各只拿到一部分。原因：一条消息只被一个 worker 收到。解法：要广播就为每个下游各发一份（见 [15_select](../15_select/)）。

**Q：为什么 fan-out 不需要任何"分发器"？**

A：channel 接收本身是原子的串行化操作——运行时从等待接收者中挑一个交付元素。多个 goroutine 阻塞在同一个 channel 的接收上时，每个元素只交付给其中一个，天然互斥，不存在重复消费或竞态。

**Q：merge 里为什么要再起一个 goroutine 做 `wg.Wait(); close(merged)`，不直接在当前 goroutine 做？**

A：`merge` 需要立刻返回 `merged` channel 让下游接上（本实验的 pipeline 风格是"函数即阶段"）。`Wait` 是阻塞调用，若不放进 goroutine，merge 会卡到所有输入结束才返回，下游连 channel 都拿不到。

**Q：结果 channel 需要缓冲吗？加了会怎样？**

A：不需要也能正确工作。给 worker 的结果 chan 加缓冲能让 worker 在 merge 转发慢时少阻塞一点（吞吐微调），但不改变正确性；merged 不缓冲则转发方每次发送都要等下游接收（背压——下游不取、上游就停）。缓冲是性能参数，不是正确性参数。

**Q：扇出的 worker 数量该怎么定？越多越快吗？**

A：CPU 密集型任务上限约是 `runtime.GOMAXPROCS(0)`（当前可同时执行的 goroutine 数上限，见 [10_gomaxprocs](../10_gomaxprocs/)）；IO 密集可以更多。

worker 太多反而增加调度与 channel 争用开销——worker 数是并发度旋钮，不是正确性开关。

**Q：如果某个 worker panic 了会怎样？**

A：panic 会沿 goroutine 崩溃整个程序，且它来不及 `close(out)`，即便 recover 也要补 close 才不拖垮 merge。

并发程序的错误处理要么在每个 worker 内 recover 兜住，要么把 error 当数据通过 channel 传回（见 [25_panic_recover](../25_panic_recover/)）。
