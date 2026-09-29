# 19 · Pipeline 模式：数据在 stage 间的 channel 之河

> Pipeline 模式把一个大计算拆成一串单职责的 stage，stage 之间用 channel 连接，数据像水一样逐个流过整条管线。读完本篇你将掌握 stage 函数的标准形态与接线方式、close 沿流水线逐级传播的收尾机制、末端接收作为同步点的原理，以及若干典型坑的成因与解法。

## Background

没有这种组织方式之前，多步计算通常写成顺序函数调用：以"求 1..10 的平方和"为例，惯用写法是两层 for 循环——先算完所有平方存进切片，再遍历切片求和。这个做法在三处撞墙：

- 每一步都要等上一步全部做完才开始，步骤之间没有任何并发
- 中间结果全部驻留内存，10 个数无所谓，换成上千万条记录就是实打实的开销
- 多步逻辑搅在一个函数里，想替换其中一步或单测其中一步都很困难

Go 的并发哲学是"不要用共享内存来通信，要用通信来共享内存"。顺着这条哲学，把每一步变成独立运行的并发单元、步与步之间用消息传递衔接，就得到了 Pipeline 模式——这也是 Go 并发编程里最常用的组合方式之一。

## What

Pipeline 模式是一种把多步计算组织成"一串单职责处理步骤"的并发结构，数据沿管线逐级流动、逐个处理。

每个 stage（流水线中的一个处理步骤）是一个 goroutine（Go 运行时调度的轻量级执行流）：从上游 channel（Go 的类型化并发队列，用于在 goroutine 之间传递值）读数据，只做一件事，把结果写进下游 channel。

可以把每个 stage 想象成一个"只做一件事"的加工工位：上游 channel 是它的进水管，下游 channel 是出水管，数据从进到出逐个流过。但和真实水管不同的是，水管里的水流是连续压入的，而 channel 里的元素必须等下游取走，上游才能继续发送——发送本身就是一次同步。

在本实验的三级流水线里，数据形态沿管线逐步变化：`int 1..10` 的原始数值流，经过 square 变成平方值流，最终在 sum 折叠成单一结果 385。

## When to Use

判断一个活儿适不适合流水线，看的是"工序"而不是"功能"：

- 做 ETL 或数据清洗时：一批记录要依次经过解析、校验、过滤、汇总几道异构工序。每道工序单独成一个 stage，替换其中一道（比如换个过滤规则）不影响其余各道。
- 做流式处理时：数据源持续产出（逐行读大文件、消费消息队列），不能等全部到齐再算。流水线的 stage 逐个处理元素，边产出边加工。
- 做可复用的并发构件时：每个 stage 的输入输出都是 channel，可以像命令行管道那样自由拼接组合，`sum(square(generate(10)))` 一行完成接线。

何时不用它：

- 步骤只有一两步、数据量小：直接顺序函数调用，并发开销与阅读成本都不值得
- 单一重活需要并行加速：那属于 worker pool（用一组同质 worker 分摊任务）的领域，不是多步串联
- 需要中途取消或超时控制：裸流水线没有取消路径，要先补上 context 传播（见 [22_context](../22_context/)）才能安全使用

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 顺序函数调用 | 无并发，中间结果落内存 | 步骤少、数据量小 |
| Pipeline | 多个异构步骤串联，数据逐个流过 | 多道工序、要流式处理或自由组合 |
| Worker pool | 单个步骤内用多个同质 worker 分摊 | 只有一步太慢，任务同构可并行 |
| 扇出/扇入 | 把流水线中最慢的一级摊给多个 worker，再汇回一条通道 | 流水线中某一级成为瓶颈（见 [20_fan_in_out](../20_fan_in_out/)） |

## Quick Start

前置条件：已安装 Go 并可用 `go run`，本实验无第三方依赖。

```bash
cd concepts/19_pipeline
go run main.go
```

预期输出（节选，stage1/stage2 并发打印，顺序可能交错）：

```text
========== 三级流水线：generate → square → sum ==========
[stage1] generate 产出: 1
[stage2] square 收到 1 → 产出 1
[stage1] generate 产出: 2
[stage1] generate 产出: 3
[stage2] square 收到 2 → 产出 4
...
[stage2] square 收到 10 → 产出 100
[stage3] sum: 聚合完成，总和 = 385
最终结果: 1²+2²+...+10² = 385
========== 收尾：close 沿 stage 逐级传播，无需计数信号 ==========
```

注意看交错的行：stage1 还在产 `5` 的时候 stage2 已经在处理 `2` 了——三个 stage 是同时在跑的，数据逐个流过，这就是流水线。

接线代码在 `main` 里，这段在做什么：把三个 stage 的输出依次接到下一个 stage 的输入上，最后从末端收结果：

```go
// 接线：每个 stage 的输出直接作为下一个 stage 的输入。
// 返回类型是 <-chan，赋给变量后只能收，数据流向一目了然。
nums := generate(10)
squares := square(nums)
results := sum(squares)

// 主 goroutine 从末端收结果：sum 要等 squares 全部处理完，
// 所以这次接收天然是整条流水线的同步点。
total := <-results
```

## How It Works

三个 stage 函数签名高度一致：接收上游 channel，返回 `out := make(chan int)` 起的 goroutine 干活，干完 `defer close(out)`。写熟了之后，流水线就是"把函数调用串起来"。下面按数据流经的顺序逐级拆开。

### stage 1：generate——把"一批数"变成"一条流"

这段在做什么：起一个 goroutine 逐个产出 1..nums，产完即 close 通知下游"没有更多了"：

```go
// generate 产生 1..nums。返回值是单向接收通道 <-chan：
// 调用方只能收不能发，类型系统层面保证 stage 职责边界。
func generate(nums int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out) // 产完即 close，通知下游"没有更多了"
		for i := 1; i <= nums; i++ {
			fmt.Printf("[stage1] generate 产出: %d\n", i)
			out <- i
		}
	}()
	return out
}
```

你在输出里看到的每一行 `[stage1] generate 产出: n`，就来自这个循环里的 Printf；最后的 `close(out)` 是整条关闭链的起点。

### stage 2：square——转换一个、发一个

这段在做什么：用 `for range` 逐个收上游的值，平方后发给下游：

```go
// square 从 in 逐个读入，平方后发给 out。
// for range 一个 channel：通道被 close 且排空后循环自动退出，
// 因此这里不需要知道上游一共发多少个。
// 为了演示"数据形态变化"，每产出一个值就打印一行。
func square(in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out) // in 排空 → 循环结束 → close(out)，close 向下游传播
		for v := range in {
			sq := v * v
			fmt.Printf("[stage2] square 收到 %d → 产出 %d\n", v, sq)
			out <- sq
		}
	}()
	return out
}
```

`generate` 产完 10 个数后 close；square 的 `for range in` 检测到通道关闭且排空，循环退出，随即 `close(out)` 通知下一级。整条管线不需要 WaitGroup（等待一组 goroutine 结束的计数器）、不需要约定个数——"上一级关闭"本身就是结束信号。

### 单向 channel 是职责边界

`generate` 返回 `<-chan int`（只收方向），调用方拿到后想发也编译不过；`square` 的参数 `in <-chan int` 同样收不到"往里写"的能力。

方向约束写在类型上，谁生产、谁消费一目了然，接反了立刻编译报错。单向的转换发生在函数边界：双向 channel 在 `return out` 时自动收窄为 `<-chan`。

### stage 3：sum——把"元素流"折叠成"单个结果"

这段在做什么：累加上游所有值，先发结果、再 close：

```go
// sum 聚合阶段：把上游所有值累加，最终结果（而非逐个元素）
// 经 out 发出。流水线末端把"元素流"折叠成"单个结果"。
func sum(in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		total := 0
		for v := range in {
			total += v
		}
		out <- total // 先发结果再 close，顺序不能反
		close(out)
	}()
	return out
}
```

`total := <-results` 这一行要等 sum 把上游全部累加完才会返回——它天然是整条流水线的汇合点。main 不用管中间两级跑没跑完，收末端结果就是等全管线。输出里 `[stage3] sum: 聚合完成，总和 = 385` 只有在平方流全部排空后才会打印。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func generate(n int) <-chan int` | 返回单向接收通道 | 把"只能收"写进类型，调用方无法破坏流向 |
| `func square(in <-chan int) <-chan int` | 收单向、产单向 | stage 职责边界的标准签名 |
| `defer close(out)` | stage 结束时关闭下游 | close 沿管线传播的源头动作 |
| `for v := range in` | 收到 close 且排空后自动退出 | 下游无需知道上游个数 |
| `out <- total; close(out)` | 先发结果再关闭 | 顺序反了接收方读到零值 |

## Pitfalls & Q&A

踩坑清单，每条按"现象 → 原因 → 解法"展开：

- **两个消费者分一个 stage 的输出**。现象：额外开一个 goroutine 去观察 `squares`，sum 的结果就错了。原因：同一个 channel 的元素会被多个接收方瓜分，两边都拿不到完整数据。解法：要观察请在 stage 内部打印，不要从流上"分叉"。
- **忘记 close 或 close 错层级**。现象：下游 `for range` 永远不退出。原因：close 的责任在发送方，下游接收方 close 上游通道是运行时 panic。解法：每个 stage 只 close 自己的 `out`，用 `defer` 保证。
- **向已关闭的 channel 发送**。现象：panic: send on closed channel。原因：close 之后通道不再接受任何值。解法：close 必须放在"确定不再发送"之后，本实验用 `defer` 保证。
- **`for range` 不结束导致泄漏**。现象：程序结束前有 goroutine 卡在接收上。原因：上游忘了 close，下游 range 永远阻塞，goroutine 泄漏。解法：排查与预防见 [08_leak_detect](../08_leak_detect/)。
- **未使用的局部变量是编译错**。现象：加了观察变量后编译不过。原因：Go 把未使用的局部变量定为编译错误。解法：示例里每个变量都"被用掉"了，仿照即可。

**Q：为什么 stage 要用 goroutine 而不是普通函数调用？**

A：如果 `generate` 在 main 里同步跑完再返回 channel，三级就退化成串行批处理——square 必须等 10 个数全部生成完才开始。放进 goroutine 后，stage1 产一个、stage2 立刻能处理一个，各 stage 按各自节奏推进。

**Q：通道要不要加缓冲？**

A：无缓冲时上游发送会阻塞到下游接收，数据逐个"接力"，背压（下游处理速度反向制约上游发送的机制）天然存在；加缓冲能让快 stage 领先慢 stage 若干个元素，但只是平滑抖动，瓶颈仍由最慢 stage 决定。学习阶段建议无缓冲，行为最好预测。

**Q：sum 之后为什么还要 close？**

A：末端通道没人再发了，close 是把"可以安全地 range/收完即走"这个事实告诉未来可能的接收方；不 close 的末端通道在只收一次的场景不会泄漏，但只要有人 `for range` 它就会卡死。保持"谁发送谁关闭"的习惯最稳。

**Q：想给流水线加"中途取消"怎么办？**

A：本例的关闭链是"正常跑完"的路径；若下游提前退出，上游会阻塞在发送上造成泄漏。标准解法是给每个 stage 传入 `ctx context.Context`（携带取消信号的上下文对象），select 里同时监听 `ctx.Done()`——见 [22_context](../22_context/)。
