# 11 · Goroutine 池：固定 N 个 worker，用 channel 接活、用 WaitGroup 收工

> 开 100 个 goroutine（Go 的并发执行单元）干 100 个活，并发度就失控了：任务再多，你也不想无上限地开 goroutine。Goroutine 池反过来想——固定 N 个 worker（常驻取活的工作 goroutine），任务投进 channel（Go 的并发安全队列原语）排队，谁空闲谁取。读完本篇，你能跑通任务 chan 扇出、结果 chan 汇合与 WaitGroup 关闭这三个关键件，并掌握 close 与 Wait 的时序规则。

## Background

没有池，最直接的做法是"一个任务一个 goroutine"：来 10000 个任务就开 10000 个。这会撞上两堵墙。

第一堵是**无上限并发**：内存和调度压力全在自己身上，任务量波动时并发度完全不可控，下游被压垮的风险也无法预估。

第二堵是**生命周期没人管**：main 不知道哪些 goroutine 还在跑，结果没收齐就退出了——"开"有 `go` 关键字，"收"却没有任何默认机制。

池的思路是把"一个任务一个 goroutine"换成"N 个 worker + 任务队列"：并发度由 N 决定，任务多少只影响队列长度；每个 worker 何时退出由队列的关闭信号决定，main 等所有人收工。这是 Go 并发里"channel 传数据 + WaitGroup 管生命周期"的标准合体。

## What

**定义**：Goroutine 池是一种固定 worker 数量、用任务队列分发工作的并发模式——并发度由 N 决定，任务多少只影响队列长度；WaitGroup（sync 包的等待计数器，`Wait` 阻塞到计数归零）负责把生命周期收干净。

**心智模型一句话**：jobs chan 是任务队列，5 个 worker 各自 `for range` 抢活（扇出——一个队列被多个消费者同时消费），处理完写进同一个 results chan（汇合点——多路写入、一处收集）。

`close(jobs)` 是"没有更多任务"的广播，`workers.Wait()` 之后才能 `close(results)`。

可以把池想象成银行网点的叫号系统：柜台数固定（N 个 worker），顾客取号排队（任务队列），办完即走，加开窗口就是调 N。但和叫号系统不同的是，这里的排队、分发、结果收集由 channel 语义保证并发安全，不需要人工维持秩序；而"停止放号"（close）必须由发送方显式调用，运行时不会替你判断任务是否发完。

## When to Use

这份判断力在三类场景用得上：

- 服务需要向下游（数据库、第三方 API）发起大量请求时：固定 worker 数做有界并发，压力可预估、下游不被冲垮
- 批量处理任务（消息消费、文件转码）时：并发度与任务数解耦，调吞吐只改 N 一个常量
- CPU 密集任务想对齐核数时：worker 数取 GOMAXPROCS 上下，避免过量并发只添调度开销

何时不用：

- 任务量小、生命周期短：直接 `go` 出去更简单，池的固定成本不划算
- IO 等待为主且不需要限流时：goroutine 数可以远大于核数，设池反而限制了吞吐

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 每任务一个 goroutine | 并发度等于任务数，无上限 | 任务量小、IO 等待为主、无需限流 |
| 固定 worker 池（本篇） | 并发度有界，生命周期可管 | 要控制资源占用与下游压力 |
| 信号量限流（带缓冲 chan 计数） | 不常驻 worker，只在入口限并发 | 已有"每任务一 goroutine"的代码，只想加上限 |

## Quick Start

前置条件：安装 Go 即可（demo 用到 Go 1.25+ 的 `WaitGroup.Go`），纯标准库。

```bash
cd concepts/11_worker_pool
go run main.go
```

预期输出（节选，**结果顺序与 worker 分配每次运行都不同**，下面是一次真实运行的片段）：

```text
worker 5 处理任务 1 (输入 1)
worker 5 处理任务 6 (输入 6)
worker 5 处理任务 7 (输入 7)
worker 5 处理任务 8 (输入 8)
worker 1 处理任务 54 (输入 54)
worker 2 处理任务 2 (输入 2)

共 5 个 worker，收到 100/100 个结果，全部正确且无重复: true
注意：哪个 worker 处理哪个任务是调度决定的，每次运行的分配都不同——结果集相同，过程日志不同。
```

最后一行是程序自校验：100 个结果一个不少、每个 ID 恰好出现一次、数值全部正确。中间的处理日志每次运行的顺序和分配都不一样——这正是扇出的意义。

## How It Works

### 任务 chan 扇出：谁空闲谁取

5 个 worker 执行同一段 `for j := range jobs`。channel 天然是并发安全的队列：一个任务只会被一个 worker 收到，忙碌的 worker 不取、空闲的自动补位。不需要给 worker 编号排队，也不需要锁——扇出是 channel 语义自带的。

你在输出里看到的"worker 5 处理任务 1"、"worker 1 处理任务 54"，worker 编号与任务号的配对每次运行都不同——取哪个任务由调度决定，这正是"谁空闲谁取"的实证。

### 结果 chan 汇合：多路写入一个 chan

每个 worker 把结果写进同一个 `results` chan。多个 goroutine 向一个 chan 发送是安全的，chan 在这里就是汇合点，收集者只管从一头读。容量设成 100（任务数）后 worker 写结果基本不阻塞，池不会因为下游慢而卡住上游。

输出自校验行"收到 100/100 个结果，全部正确且无重复"，就是多路写入一个 chan 后在收集端清点的结果。

### 优雅关闭：close 是广播，Wait 是栅栏

- `close(jobs)` 由生产者在投完任务后调用，所有 worker 的 `for range` 会在缓冲排空后自动退出——不需要发"毒丸"哨兵任务（用特殊任务值通知结束的做法）。
- `close(results)` 必须在 `workers.Wait()` **之后**：所有 worker 都退出了，才确定不会再有写入；顺序反了收集者会漏收结果，提前 close 还会被 worker 的写入 panic（向上传播的运行时错误）。
- 收集者自己也是一个 goroutine，main 用第二个 WaitGroup（`WaitGroup.Go`，Go 1.25+ 自动配对 Add/Done）等它排空。

### 并发度与任务数解耦

100 个任务 5 个 worker：并发度恒为 5，任务只是排队。想调吞吐，改一个常量 `numWorkers` 就行——这正是池比"每任务开一个 goroutine"好管理的地方。

写法速览：

| 写法 | 含义 | 为什么重要 |
| --- | --- | --- |
| `make(chan Job, numJobs)` | 带缓冲任务队列 | 生产者可一次性投完，不必与 worker 同步节奏 |
| `for j := range jobs` | 取到 chan 关闭且排空为止 | worker 退出全靠这一个机制，无哨兵、无标志位 |
| `close(jobs)`（只由发送方调） | 广播"没有更多任务" | 接收方 close 或重复 close 都会 panic |
| `workers.Wait()` 后 `close(results)` | 关闭时序栅栏 | 保证关闭前所有写入已完成，收集者不漏不炸 |
| `go func(id int)` 参数传值 | 把循环变量拷贝进 goroutine | 闭包直接捕获循环变量，多个 goroutine 可能共享同一个值 |
| `wg.Go(f)`（Go 1.25+） | 自动 Add(1)+Done | 替代 `wg.Add(1); go func(){defer wg.Done(); ...}` 三件套；再手动 Add 会多计一次、Wait 永久阻塞 |

想看这个模式封装成可复用组件（带并发压测对比）的样子，见 [../../patterns/08_worker_pool/](../../patterns/08_worker_pool/)。

## Pitfalls & Q&A

坑清单（每条都可在 demo 代码里复现），每条按"现象 → 原因 → 解法"展开：

- **现象**：去掉 `close(jobs)` 后程序永久卡死。**原因**：所有 worker 处理完缓冲后阻塞在接收上，`workers.Wait()` 永不返回，直接死锁。**解法**：生产者投完任务后 close 任务 chan。
- **现象**：panic: send on closed channel。**原因**：`close(results)` 提到 `workers.Wait()` 之前，worker 向已关闭的 chan 发送。**解法**：保持"先 Wait 再 close"的顺序。
- **现象**：worker 全部退出，程序仍卡在收尾。**原因**：不 close `results`，收集者的 `for range` 永不退出。**解法**：`workers.Wait()` 之后 close 结果 chan。
- **现象**：日志里 worker 编号全是同一个值。**原因**：worker 的闭包（引用了外部变量的函数值）直接捕获循环变量 `w`，多个 goroutine 共享同一个变量。**解法**：`go func(id int)` 参数传值。
- **现象**：`Wait()` 永久阻塞。**原因**：`collector.Add(1)` 和 `collector.Go(f)` 同时用，计数多了 1（`Go` 内部已含 Add）。**解法**：二选一，不混用。

**Q1：worker 数设成多少合适？**

I/O 密集型可以远大于 CPU 核数（瓶颈在等待）；CPU 密集型一般取 GOMAXPROCS 上下。池的价值不在"最优 N"而在"有界 N"——并发度可控、压力可预估。

**Q2：为什么用 `for range jobs` 而不是 `j, ok := <-jobs` 循环？**

两者语义等价，但 `for range` 把"退出条件"交给语法：chan 关闭且排空后循环自动结束，不会误读 closed chan 的零值。手写 `ok` 判断忘了 `break` 就会疯狂消费零值任务。

**Q3：为什么"先 Wait 再 close(results)"，而不是反过来让收集者自己判断？**

close 的所有权在发送方。results 的发送方是"所有 worker 的集合"，只有 `workers.Wait()` 能证明这个集合全部结束。任何"猜差不多收完了"的写法都在赌调度。

**Q4：任务处理到一半 worker panic 了怎么办？**

这份 demo 假设 process 不 panic。生产实现里 worker 函数要包 `defer recover()`（内建函数，捕获 panic 后让程序恢复正常执行），否则一个任务 panic 会带崩整个进程——panic 不能被其他 goroutine 捕获。

recover 后 `wg.Done()` 已由 defer 保证，不会卡住 Wait。

**Q5：jobs 缓冲设多大？**

设成任务总数（本例 100）生产者可无阻塞投完；设小（比如 N）则生产者与 worker 自然流水，内存更省。缓冲不改变正确性，只影响节奏——这也是 close/Wait 时序必须严格的原因。

若设为 0，收发就变成"双方同时就绪才能握手"的同步语义，见 [12_unbuffered_chan](../12_unbuffered_chan/)。
