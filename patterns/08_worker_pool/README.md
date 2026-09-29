# 08 · Worker Pool：固定 worker 扇出消费，channel 背压限流

> 无限制地 `go func()` 处理海量任务，轻则内存暴涨，重则压垮下游。Worker Pool（工作池）用固定数量的 worker goroutine 消费同一个任务 channel：并发度恒定、资源可控、错误可收集。本篇用纯标准库手写一个完整的池，讲清扇出/汇合、背压、优雅关闭三个核心机制。

## Background

先看默认做法：来一个任务就 `go func()` 一个 goroutine——Go 的轻量级执行单元，由运行时调度，创建成本很低。goroutine 便宜，但**不免费**：每个都有栈、有调度开销。

任务量大时，无限制并发会在三个方向同时撞墙：

- **内存不可控**：goroutine 数量随任务数线性增长，没有上限；
- **并发度失控**：大量请求同时打向 DB 或外部 API，触发连接耗尽与限流（下游拒绝超量请求的保护机制）；
- **错误难收集**：任务散落在各自独立的 goroutine 里，失败原因没有统一的出口。

手动限流是常见的第一反应。`golang.org/x/sync/semaphore` 的 `sem.Weighted`（带权重的信号量，限制同时进入同一段代码的 goroutine 数量）能把并发数封顶，但它没有内置队列：每个任务仍要新建一个 goroutine、用完即销毁，排队逻辑得自己写。

真正需要的组合是：数量固定的长驻执行者、一条能自动排队的通道、一个统一的错误出口。Go 里恰好有一条原语天生带队列——channel。

## What

**Worker Pool（工作池）**是一种并发模式：预先启动固定数量的 worker goroutine，循环地从同一个任务 channel 里领任务、执行，再把结果写回结果 channel，用来在并发度（同时执行的任务数）可控的前提下批量处理任务。

心智模型一句话：所有 worker 挤在同一个 `tasks` channel 前抢任务（**扇出**：一份任务分给多个执行者），各自把结果写回同一个 `results` channel（**汇合**：多路结果收拢成一路）。

队列满时 `Submit` 阻塞，背压（backpressure：下游的处理速度反过来限制上游的提交速度）由带缓冲任务队列天然提供——上游想塞任务，队列满了就得先等。

可以把 Worker Pool 想象成银行营业厅：柜员数量固定（worker），客户取号排队（有界任务队列），号发完就停止放人进门（`Submit` 阻塞，即背压）。但和银行不同的是：柜员不把结果当面交还客户，而是统一投进结果通道，由专门的收集者在结尾汇总错误。

它也是**生产者-消费者**模型（一方生产数据、另一方消费数据，中间用队列解耦）的特例：`Submit` 是生产者，worker 是消费者，`tasks` channel 是有界队列（容量有限的队列，满则写入者等待）。

## When to Use

这节回答"什么活该交给池干，什么活不必"。

典型场景：

- **调用有限流的下游**：批量请求外部 API 或数据库，下游能承受的并发数有明确上限，而任务总量远超这个上限。
- **持续的批量作业**：给上传的图片批量生成缩略图、消费消息队列里的消息——任务源源不断，固定 worker 复用比每任务新建 goroutine 更稳。
- **失败要统一上报**：批量任务中预计有一部分会失败，需要把每个失败连同任务 ID 汇总后一次返回。

何时不用：

- 任务就几个、并发上限无所谓：直接 `go` 加 `sync.WaitGroup`（等待一组 goroutine 结束的计数器）代码更短。
- 只需要限并发、不需要排队和结果通道：`sem.Weighted` 代码最少。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 无限制 goroutine | 实现简单；但内存不可控、无法限制并发度、错误难收集 | 任务量小且确定压不垮下游 |
| **Worker Pool** | 并发数固定、可排队、可收集错误；需要协调 channel 与 WaitGroup | 持续批量处理，需要并发上限与失败收集 |
| `sem.Weighted` | 代码最少；但无内置队列，每任务创建销毁 goroutine | 只需限并发数，不需要队列与结果通道 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/08_worker_pool
go run worker_pool.go
```

demo 参数：5 个 worker、20 个任务；任务 `i` 耗时 `50+i*10` 毫秒，其中 7 和 14 号任务故意失败。

预期输出（节选，总耗时每次运行不同，实测约 0.6s）：

```text
Worker Pool: 5 workers, 20 tasks

  [OK]   task 1 completed
  ...
All tasks submitted, waiting for completion...
  [OK]   task 20 completed

Done in 601.355541ms. Errors: 2
  [ERR]  task 7: simulated failure (divisible by 7)
  [ERR]  task 14: simulated failure (divisible by 7)
```

读这份输出有一个要点：失败任务**不在**执行流里打印——worker 只把 `err` 装进 result。

所有错误由一个负责排空 results 通道的收尾 goroutine（drain goroutine，即 `for r := range wp.results` 的收集者）汇总后在结尾统一输出（机制见 How It Works）。

池的两条核心 channel 在初始化时创建：

```go
tasks:   make(chan task, workers*2)   // 缓冲 10
results: make(chan result, workers*2)
```

这段在做什么：任务与结果各一条带缓冲 channel，容量都是 `workers*2`（demo 中 5×2=10）。缓冲是任务排队的地方，也是背压的来源。

## How It Works

按一个任务的生命周期走：`Submit` 入队 → worker 领走执行 → 结果回传 → 全部完成后收尾。

### 扇出：N 个 worker 抢一个 channel

```go
for i := 0; i < wp.workers; i++ {
    wp.workerWg.Add(1)
    go func(workerID int) {
        defer wp.workerWg.Done()
        for t := range wp.tasks {          // channel 关闭后 range 自动退出
            err := t.fn()
            wp.results <- result{taskID: t.id, err: err}
            wp.taskWg.Done()
        }
    }(i)
}
```

这段在做什么：启动 `workers` 个 goroutine，每个都在 `for range wp.tasks` 上阻塞等任务。channel 自带锁，保证每个任务只被一个 worker 领走——扇出不需要任何额外同步。你在输出里看到 20 个任务全部完成、而同时执行的任务数不超过 5，就来自这里。

### 背压：队列满时 Submit 阻塞

连续 `Submit` 20 个任务：前 10 个填满缓冲（期间 worker 陆续领走），队列再满时 `tasks <- task` 阻塞生产者——这就是背压，不需要任何显式限流代码。

缓冲大小的取舍：缓冲为 0 时背压最强（`Submit` 必须等空闲 worker）；缓冲越大吞吐越平滑，但内存占用越高、积压越隐蔽。输出里 `All tasks submitted, waiting for completion...` 之前已有任务完成，正是 worker 在提交阶段并行消化队列的痕迹。

### 汇合与错误收集

`Wait()` 先启动一个 drain goroutine 执行 `for r := range wp.results`，收集非 nil 的 err；主流程等任务全部完成后，再按固定顺序关闭两条 channel 收尾。你在输出结尾看到的统一错误列表，就来自这个 drain goroutine。

### 优雅关闭：Wait 内部的固定顺序

```go
wp.taskWg.Wait()      // 1. 所有任务执行完
close(wp.tasks)       // 2. 关任务队列 → worker 的 range 退出
wp.workerWg.Wait()    // 3. 等 worker 全部退出（不再有新 result）
close(wp.results)     // 4. 关结果队列 → drain 的 range 退出
<-done                // 5. drain 收完所有错误
```

顺序不可乱：先 `close(results)` 会丢掉 worker 还没写入的结果；不 `close(tasks)` 则 worker 的 `range` 永远不退出。原则就一句话：谁发送谁关闭、关完再等收尾。

### 关键字段一览

| 字段/原语 | 作用 | 为什么重要 |
|---|---|---|
| `tasks chan task` | 有界任务队列 | 缓冲 `workers*2`，满则 Submit 阻塞 = 背压 |
| `results chan result` | 结果汇合通道 | 错误经此回传，drain 统一收集 |
| `taskWg` | 已提交任务计数 | `Submit` 时 +1，worker 完成 -1；归零即全部做完 |
| `workerWg` | worker 存活计数 | 保证 `close(results)` 前所有 worker 已退出 |
| `counter` | 任务 ID 发生器 | 只有 Submit 单写者，无需加锁 |

### 数据视角：串行 vs 池化

按 demo 的耗时模型推算：20 个任务串行执行需 3100ms；池化后（模拟调度）约 700ms；`go run` 实测约 601ms——同量级，差异来自调度细节与计时方式。

调度形态是"先空闲的 worker 领下一个任务"，因此耗时最长的任务（大 id）成为尾部瓶颈。仓库里的 `python3 viz.py` 可按同一耗时模型复算这组模拟值（需 Python 3 与 numpy；模拟值，非实测逐任务时间戳）。

## Pitfalls & Q&A

先列踩坑清单（每条：现象 + 原因 + 解法），再答有增量的问题。

- **两个 WaitGroup 不能合并成一个**：现象是判断不了何时能安全 `close(results)`。原因："任务完成"和"worker 退出"是两种事件，一个计数器分不开。解法：`taskWg` 计任务、`workerWg` 计 worker（见上表）。
- **忘写 `close(tasks)`**：现象是 `workerWg.Wait()` 死锁。原因：channel 不关闭，worker 的 `for range` 永不退出。解法：在 `Wait()` 的固定顺序里由发送方关闭。
- **在 worker 里关 results**：现象是偶发 panic（close of closed channel）。原因：多 worker 竞争下会对已关闭的 channel 再次 close。解法：只由 `Wait()` 单点关闭。
- **Submit 与 close(tasks) 并发**：现象是 panic（send on closed channel）。原因：向已关闭 channel 发送。解法：本实现在 Submit 全部完成后才调 `Wait()`；生产环境需用 `sync.WaitGroup`/`Done` 或 `context` 协调"停止接单"。
- **误以为一个任务失败会中断整批**：事实相反——err 只装进 result，worker 循环继续，失败被隔离在单个任务粒度。

**Q：任务失败怎么处理？和 `sem.Weighted` 的区别？**

A：err 通过 results channel 回传，`Wait()` 收集后统一返回；也可改成快速失败（首个 err 触发 cancel context，即用 `context` 广播取消信号通知所有相关 goroutine 停止）或在 worker 内重试。

与 `sem.Weighted` 相比：池的 goroutine 持久复用、自带队列与结果通道，适合持续批量处理；信号量只限并发数，适合简单限流。

**Q：如何控制并发数、背压怎么实现、关闭顺序为何固定？**
A：三问的机制都在 How It Works 一节展开——并发数 = worker 数（扇出小节）；背压 = 带缓冲队列满时阻塞 `Submit`（背压小节）；关闭顺序 = 双 WaitGroup 的五步固定次序（优雅关闭小节）。
