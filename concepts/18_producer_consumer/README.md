# 18 · 生产者-消费者：多生产多消费与两级 WaitGroup 优雅关闭

> 生产者-消费者是并发程序最基本的骨架：生产者只管发，消费者只管收，channel 是两者之间唯一的交接点。人多之后真正的难点不是收发，而是**收尾**——什么时候关 channel、谁来关、怎么让所有 goroutine 体面退出。本篇用 3 生产者、2 消费者的可运行 demo 演示"两级 WaitGroup + 第三方 close"的标准姿势；读完你能把这套关闭纪律扩展到任意数量的生产者与消费者。

## Background

Go 里单个 goroutine（语言内置的轻量级线程）发、单个收的版本随手就能写：发送方发完 `close`，接收方 `for range` 到排空自然退出。

一旦生产者变成 3 个、消费者变成 2 个，新问题立刻出现：某个生产者发完了，怎么知道**别的生产者**也发完了？谁有资格 close channel？main 怎么确认 12 个任务一个不少地被处理了？

这些问题的答案是同一套关闭顺序纪律：先等生产者全部完成，再由"第三方"close，最后等消费者退出。顺序错一步就是 panic 或死锁。

## What

一句话定义：多生产多消费者模式是用一条 channel（goroutine 间传递数据的管道）连接 N 个生产者与 M 个消费者、并用两级 WaitGroup（带计数器的等待原语，Add/Done/Wait 三件套）分层管理退出的并发骨架——每级只管一类参与者，关闭动作夹在两级等待之间。

心智模型：两级 WaitGroup 各管一边——wgProducers 管发送、wgConsumers 管接收；close 是发送方的"再无发送"宣言，必须先由收尾 goroutine 用 `wgProducers.Wait()` 证明前提成立，再交给消费者的 `for range` 自然终结。

可以把两级 WaitGroup 想象成仓库的两道关卡：第一道关卡登记所有发货人离场，全部离场后管理员才贴"停止收货"的封条，第二道关卡等所有提货人清完存货离开。但和"直接锁仓库大门"不同的是，贴封条后货架上已有的货仍可提走——close 只封入口，不清空存量。

## When to Use

在做什么事的时候选它：

- 在做任务分发/工作池时：一个或多个任务源往同一条 channel 投任务，任意数量的 worker 消费。
- 在做扇入（fan-in，多个数据源汇聚到一条 channel）的数据合并时：多个生产者写同一条 channel，下游统一消费。
- 在做"多路生产、统一收尾、对账不丢不重"的后台作业时：两级等待 + 第三方 close + 统计对账。

何时不用：单个生产者单个消费者时，发完直接 close 即可，两级 WaitGroup 是多余的复杂度；"生产→加工→汇总"多级串联的阶段式结构见 [19_pipeline](../19_pipeline/)。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 单生产者 + `for range` | 发完即 close，连 WaitGroup 都不用 | 一对一的最简单交接 |
| 两级 WaitGroup + 第三方 close（本篇） | 支持任意数量的生产者/消费者 | 多对多、需要对账 |
| 泛型组件封装 | 收尾逻辑封成可复用组件（含扇入合并） | 同样的收尾要在多处复用，见 [../../patterns/09_producer_consumer/](../../patterns/09_producer_consumer/) |

## Quick Start

前置条件：本机装有 Go，无第三方依赖。

```bash
cd concepts/18_producer_consumer
go run main.go
```

预期输出（节选。**发送与处理的行序每次运行都不同**——调度器决定谁先跑，下面只是其中一次的真实快照）：

```text
  [C1] 处理任务 100（来自 P1）
[P3] 发送任务 300
  [C1] 处理任务 301（来自 P3）
[P2] 发送任务 201
[P1] 全部发送完毕，退出
--- tasks channel 已关闭（所有生产者都完成了）---
  [C2] channel 已关闭，退出
  [C1] channel 已关闭，退出

对账：发送 12 = 接收 12？true
  P1 发了 4、被收了 4
  P2 发了 4、被收了 4
  P3 发了 4、被收了 4
```

不变量是：12 条发送对应 12 条处理、对账行恒为 `true`、每个生产者的 4 个任务都被收齐、两个消费者都以"channel 已关闭"退出。行序随机恰恰说明了并发的本质。

生产者骨架（3 个生产者、每个发 4 个任务；Done 用 defer 保证调用）：

```go
	for p := 1; p <= numProducers; p++ {
		wgProducers.Add(1)
		go func(producerID int) {
			defer wgProducers.Done() // 发送结束后由 defer 保证 Done
			for i := 0; i < tasksPerProducer; i++ {
				t := Task{ID: producerID*100 + i, From: producerID}
				tasks <- t
				fmt.Printf("[P%d] 发送任务 %d\n", producerID, t.ID)
			}
			fmt.Printf("[P%d] 全部发送完毕，退出\n", producerID)
		}(p) // 循环变量作为参数传入，避免闭包共享
	}
```

收尾 goroutine（关闭的全部前提就藏在这两行里）：

```go
go func() {
    wgProducers.Wait() // 等到"所有生产者都完成"这一事实成立
    close(tasks)       // 此时才满足 close 的前提
}()
```

## How It Works

### 两级 WaitGroup：各管一边

`wgProducers.Add(1)` 在启动生产者前调用，生产者 `defer wgProducers.Done()`；`wgConsumers` 同理管理两个消费者。

两级分开的意义是**关闭顺序可以被精确表达**：先等生产归零，再关 channel，最后等消费归零。如果混用一个 WaitGroup，将无法说清"该等谁"。

Quick Start 输出里"--- tasks channel 已关闭 ---"一定出现在所有 `[P*] 全部发送完毕` 之后、所有 `[C*] channel 已关闭，退出` 之前——这正是两级等待钉死的顺序。

### 为什么 close 必须由"第三方"来做

`close(tasks)` 的语义是"**从此再也不会有人向它发送**"。三个生产者各自只知道自己的进度，任何一个都无权替别人宣布——P1 发完就 close，P2/P3 的下一次发送就是 panic（send on closed channel）。

`Wait()` 返回的那一刻，"再无发送"被证明，close 才是安全的。Quick Start 里的收尾 goroutine 干的就是这件事。

### 为什么消费者侧绝不能 close

从语言语义看有三层：

- **权力**：close 是发送侧的操作，消费者根本无法知道"还有没有生产者要发"，它没有资格替未来做保证。
- **时机**：消费者提前 close 会让还在发送的生产者当场 panic，而消费者对此毫不知情。
- **无益**：消费者想退出去等关闭信号就行（`for range` 在 close 后自动结束），主动 close 不解决任何它面对的问题，只制造竞态。

Go 干脆规定"仅发送方应 close"，编译器还用单向 channel（`chan<-`/`<-chan`，把方向写进类型的 channel 视图）把这种权力分配固化到类型上。

消费者的完整骨架（range 到关闭自动退出；统计写入有锁保护）：

```go
		go func(consumerID int) {
			defer wgConsumers.Done()
			// for range tasks：channel 被关闭且排空后循环自动结束。
			// 消费者侧绝不能 close：第一，close 只该由发送方触发，
			// 消费者无法知道"还有没有生产者要发"；第二，向已关闭
			// channel 发送会 panic，而消费者连有没有同伴在收都管不着。
			for t := range tasks {
				mu.Lock()
				received[t.From]++
				mu.Unlock()
				fmt.Printf("  [C%d] 处理任务 %d（来自 P%d）\n", consumerID, t.ID, t.From)
			}
			fmt.Printf("  [C%d] channel 已关闭，退出\n", consumerID)
		}(c)
```

### 对账：证明没有丢也没有重

多个消费者并发写同一个统计切片需要 `sync.Mutex`（互斥锁）保护；main 在 `wgConsumers.Wait()` 之后读统计做对账——此时消费者全部退出，数据不再有并发写，读是安全的。"发送 12 = 接收 12"就是这个模式健康运行的收据。

输出末尾的对账行与三个"发了 4、被收了 4"，对应的就是这段统计逻辑。

关键写法速查：

| 写法 | 含义 | 为什么重要 |
|---|---|---|
| `tasks := make(chan Task)` | 无缓冲 channel | 每次发送与接收都是一次握手，行为最纯粹 |
| `wgProducers.Add(1)` / `defer Done()` | 计数管理与自愈释放 | Add 必须在 `go` 之前，否则 Wait 可能提前返回 |
| `wgProducers.Wait(); close(tasks)` | 收尾 goroutine 的两步 | close 的前提由 Wait 证明 |
| `for t := range tasks` | 消费直到关闭 | 关闭且排空后循环自动结束，无需额外信号 |
| `go func(id int){...}(p)` | 循环变量作参数传入 | 避免多个 goroutine 闭包共享同一变量 |

## Pitfalls & Q&A

坑清单（每条按"现象 → 原因 → 解法"读）：

- **现象**：让某个生产者自己 `close(tasks)`，其他生产者再发送就 panic。**原因**：单个生产者只知道自己的进度，无权替别人宣布"再无发送"。**解法**：改用收尾 goroutine，先 `wgProducers.Wait()` 再 close（可把 demo 改成生产者 close 亲手复现这个 panic）。
- **现象**：消费者侧 close，同样 panic。**原因**：违反"发送方关闭"的语义约定，且消费者无法预知生产者行为。**解法**：消费者只等 `for range` 自然终结。
- **现象**：重复 close 同一 channel，panic（close of closed channel）。**原因**：close 是一次性的状态变更。**解法**：收尾 goroutine 全局唯一，或用 `sync.Once` 收敛为恰好一次。
- **现象**：`wg.Add(1)` 写进了 goroutine 内部，main 的 `Wait()` 提前通过。**原因**：Wait 可能在 Add 生效前就返回，计数从未被等到。**解法**：Add 必须在 `go` 语句之前调用。
- **现象**：消费者并发写统计切片不加锁，`go run -race` 直接报数据竞争。**原因**：多个 goroutine 同时读写同一切片。**解法**：用 `sync.Mutex` 保护写入（demo 里的 `mu.Lock()`/`mu.Unlock()`）。

**Q：close 之后还能读出数据吗？**
A：能。close 不是"清空"，而是"封口"：缓冲里（以及正在传递中）的数据仍可继续接收，排空之后才返回零值。`for range` 正是利用这一点把剩余任务处理完才退出。

**Q：为什么不让 main 直接 `wgProducers.Wait()` 然后 close？**
A：可以，功能等价。

但 main 随后还要等消费者，把"等生产、关通道"包进一个收尾 goroutine 能让 main 的职责只剩"等消费 + 对账"，关闭逻辑也被封装成一个可复用的惯用法（很多库函数叫 `drain` 或 `closeWhenDone`）。

**Q：对账时为什么不需要锁就能读 `received`？**
A：锁的必要性来自并发访问。

`wgConsumers.Wait()` 返回后所有消费者已退出，写操作全部结束，`Wait` 与后续读之间有 happens-before 关系（一个操作的结果对另一个操作可见的保证）——这正是 WaitGroup 除了"等待"之外的同步语义。

**Q：把 channel 换成有缓冲的会改变关闭顺序吗？**
A：不会。close 的前提始终是"再无发送"，与缓冲无关；有缓冲只是解耦了发送与接收的时机（生产者不被消费者阻塞），两级 WaitGroup 的收尾流程一字不改。
