# 11 · Goroutine 池：固定 N 个 worker，用 channel 接活、用 WaitGroup 收工

> 开 100 个 goroutine 干 100 个活，并发是够了，但并发度失控了：任务再多你也不想无上限地开 goroutine。Goroutine 池的反过来想——固定 N 个 worker 常驻，任务投进 channel 排队，谁空闲谁取。本实验用 100 个任务、5 个 worker，把这个模式的三个关键件跑给你看：任务 chan 扇出、结果 chan 汇合、WaitGroup 优雅关闭。

## 1. 为什么需要它

没有池，你会面对两个问题：一是**无上限并发**——来 10000 个任务就开 10000 个 goroutine，内存和调度压力全在自己身上；二是**生命周期没人管**——main 不知道哪些 goroutine 还在跑，结果没收齐就退出了。池的思路是把"一个任务一个 goroutine"换成"N 个 worker + 任务队列"：并发度由 N 决定，任务多少只影响队列长度；每个 worker 何时退出由 channel 关闭信号决定，main 用 WaitGroup 等所有人收工。这个模式是 Go 并发里"channel 传数据 + WaitGroup 管生命周期"的标准合体。

## 2. 总览：核心机制一图看懂

![Goroutine 池：任务 chan 扇出 × 结果 chan 汇合](images/pool.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/11_worker_pool/images/pool.html)
> （或本地打开 [`images/pool.html`](images/pool.html)）。

**心智模型一句话**：jobs chan 是任务队列，5 个 worker 各自 `for range` 抢活（扇出），处理完写进同一个 results chan（天然汇合点），`close(jobs)` 是"没有更多任务"的广播，`workers.Wait()` 之后才能 `close(results)`。

## 3. 快速开始

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

最后一行是程序自校验：100 个结果一个不少、每个 ID 恰好出现一次、数值全部正确。中间的 100 行处理日志每次运行的顺序和分配都不一样——这正是扇出的意义。

## 4. 核心概念

### 4.1 任务 chan 扇出：谁空闲谁取

5 个 worker 执行同一段 `for j := range jobs`。channel 天然是并发安全的队列：一个任务只会被一个 worker 收到，忙碌的 worker 不取、空闲的自动补位。你不需要给 worker 编号排队，也不需要锁——扇出是 channel 语义白送的。

### 4.2 结果 chan 汇合：多路写入一个 chan

每个 worker 把结果写进同一个 `results` chan。多个 goroutine 向一个 chan 发送是安全的，chan 在这里就是"汇合点"，收集者只管从一头读。容量设成 100（任务数）后 worker 写结果基本不阻塞，池不会因为下游慢而卡住上游。

### 4.3 优雅关闭：close 是广播，Wait 是栅栏

- `close(jobs)` 由生产者在投完任务后调用，所有 worker 的 `for range` 会在缓存排空后自动退出——不需要发"毒丸"哨兵任务。
- `close(results)` 必须在 `workers.Wait()` **之后**：所有 worker 都退出了，才确定不会再有写入；顺序反了收集者会漏收结果，提前 close 还会被 worker 的写入 panic。
- 收集者自己也是一个 goroutine，main 用第二个 WaitGroup（`WaitGroup.Go`，Go 1.25+ 自动配对 Add/Done）等它排空。

### 4.4 并发度与任务数解耦

100 个任务 5 个 worker：并发度恒为 5，任务只是排队。想调吞吐，改一个常量 `numWorkers` 就行——这正是池比"每任务开一个 goroutine"好管理的地方。

## 5. 关键字段与坑清单

| 写法 | 含义 | 为什么重要 |
| --- | --- | --- |
| `make(chan Job, numJobs)` | 带缓冲任务队列 | 生产者可一次性投完，不必与 worker 同步节奏 |
| `for j := range jobs` | 取到 chan 关闭且排空为止 | worker 退出全靠这一个机制，无哨兵、无标志位 |
| `close(jobs)`（只由发送方调） | 广播"没有更多任务" | 接收方 close 或重复 close 都会 panic |
| `workers.Wait()` 后 `close(results)` | 关闭时序栅栏 | 保证关闭前所有写入已完成，收集者不漏不炸 |
| `go func(id int)` 参数传值 | 把循环变量拷贝进 goroutine | 闭包直接捕获循环变量，多个 goroutine 可能共享同一个值 |
| `wg.Go(f)`（Go 1.25+） | 自动 Add(1)+Done | 替代 `wg.Add(1); go func(){defer wg.Done(); ...}` 三件套；再手动 Add 会多计一次、Wait 永久阻塞 |

坑清单（每条都可在代码里复现）：

1. **去掉 `close(jobs)`** → 所有 worker 处理完缓存后阻塞在接收上，`workers.Wait()` 永不返回，直接死锁。
2. **`close(results)` 提到 `workers.Wait()` 之前** → worker 向已关闭 chan 发送，panic: send on closed channel。
3. **不 close `results` 就等收集者** → 收集者的 `for range` 永不退出，死锁。
4. **worker 里闭包直接用循环变量 `w`** → 日志里 worker 编号可能全是同一个值。
5. **`collector.Add(1)` 和 `collector.Go(f)` 同时用** → 计数多了 1，`Wait()` 永久阻塞（`Go` 内部已含 Add）。

## 6. 文件结构

```text
concepts/11_worker_pool/
├── README.md              # 本文档：8 节结构
├── main.go                # 池的完整实现：~100 行，go run main.go 直接跑
└── images/
    ├── pool.workflow.json # 图源 JSON（workflow 类型）
    ├── pool.html          # 交互式架构图（浏览器打开）
    └── pool.svg           # README 内嵌的静态图
```

## 7. 深入要点

**Q1：worker 数设成多少合适？** I/O 密集型可以远大于 CPU 核数（瓶颈在等待）；CPU 密集型一般取 GOMAXPROCS 上下。池的价值不在"最优 N"而在"有界 N"——并发度可控、压力可预估。

**Q2：为什么用 `for range jobs` 而不是 `j, ok := <-jobs` 循环？** 两者语义等价，但 `for range` 把"退出条件"交给语法：chan 关闭且排空后循环自动结束，不会误读 closed chan 的零值。手写 `ok` 判断忘了 `break` 就会疯狂消费零值任务。

**Q3：为什么"先 Wait 再 close(results)"，而不是反过来让收集者自己判断？** close 的所有权在发送方。results 的发送方是"所有 worker 的集合"，只有 `workers.Wait()` 能证明这个集合全部结束。任何"猜差不多收完了"的写法都在赌调度。

**Q4：任务处理到一半 worker panic 了怎么办？** 这份 demo 假设 process 不 panic。生产实现里 worker 函数要包 `defer recover()`，否则一个任务 panic 会带崩整个进程（panic 不能被其他 goroutine 捕获）。recover 后记得 `wg.Done()` 已由 defer 保证，不会卡住 Wait。

**Q5：jobs 缓冲设多大？** 设成任务总数（本例 100）生产者可无阻塞投完；设小（比如 N）则生产者与 worker 自然流水，内存更省。缓冲不改变正确性，只影响节奏——这是 close/Wait 那套时序依然必须严格的原因。

## 8. 总结

Goroutine 池 = 固定 worker 消费任务 chan + 结果 chan 汇合 + WaitGroup 把生命周期关干净。记住三条铁律：close 只由发送方调、关闭前先等写入方全退出、worker 编号用参数传。语言层面就这三板斧，剩下的都是业务逻辑。想看这个模式封装成可复用组件（带并发压测对比）的样子，见 [../../patterns/08_worker_pool/](../../patterns/08_worker_pool/)；下一个实验看并发同步的最小单位——[12 · 无缓冲 channel](../12_unbuffered_chan/)。
