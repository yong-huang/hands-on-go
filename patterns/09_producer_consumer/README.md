# 09 · Producer-Consumer：多生产者多消费者与背压

> 多个生产者向共享队列写数据，多个消费者从队列读出并处理——生产者-消费者是并发编程中最经典的协作方式。在 Go 里一条带缓冲 channel 就同时充当线程安全队列和同步原语：队列满时生产者阻塞，这个阻塞本身就是背压（backpressure）。本篇用标准库实现完整的多生产者多消费者模型，讲清 close 语义与优雅关闭顺序。

## Background

最直接的做法是把"取数据"和"处理数据"写进同一段顺序代码。问题很明显：生产与消费耦合在一条调用链里，无法并行，处理慢会拖住生产。

第二种常见做法是共享变量加 Mutex（互斥锁，同一时刻只允许一个持有者进入被保护的代码段）：生产者往共享 slice 追加，消费者加锁取走。它灵活，但锁的粒度、谁来通知谁、队列满了怎么办，全部要手工管理，出错空间大。

于是问题变成：能不能有一条"自带锁、自带等待"的队列——满了写的人自动等，空了读的人自动等，谁都不用手工唤醒谁。Go 的 channel 就是这个答案。

## What

**生产者-消费者（Producer-Consumer）**是一种并发模式：多个生产者 goroutine 向共享队列写数据，多个消费者 goroutine 从队列读出并处理，二者只通过队列交互、互不直接调用。channel 是 Go 在 goroutine 之间传递数据的管道。

心智模型一句话：带缓冲 channel 就是有界队列（容量有限的队列）——左边三个生产者 `queue <- item`，满则阻塞（背压）；右边两个消费者 `for range` 排空，空则阻塞。

main 在下方按 `prodWg.Wait → close → consWg.Wait` 的固定顺序收尾，保证 50 个 item 不丢不重。

可以把带缓冲 channel 想象成一条有容量上限的传送带：满了上游就得暂停投料，空了下游暂停取料，两个"暂停"都不需要人去按开关。但和普通传送带不同的是：channel 的 `close` 是明确的"停产"信号——下游仍能把带上剩余的料全部取完才收工，不丢料。

demo 配置对应这个模型：3 个生产者、2 个消费者、共 50 个 item，队列容量 10。

## When to Use

这节回答"什么样的数据流值得套这个模式"。

典型场景：

- **产生速度与处理速度解耦**：日志或事件采集端持续产数据，落盘、上报端速度波动——中间放一条有界 channel，高峰期生产者自动被限速，而不是内存无限上涨。
- **多路汇入、多路处理**：多个请求源并发写入，一组消费者并行处理，比如抓取结果入库、消息消费。
- **要求"不丢不重"的收尾**：退出前要保证已提交的数据全部处理完，方便统计与对账。

何时不用：

- 顺序执行就够：生产消费在同一函数里一步完成，不必引入 goroutine 与队列。
- 只需要限并发数、不需要队列排队：`sem.Weighted` 一类信号量代码更少。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 直接调用 | 无并发；生产与消费耦合在一条调用链里 | 顺序执行即可，无需并行 |
| 共享变量 + Mutex | 灵活；但加锁、唤醒、满队处理全要手工管理 | channel 语义不够用、需要复杂结构操作时 |
| **带缓冲 channel** | 一行得到线程安全有界队列，阻塞即背压 | 绝大多数生产-消费解耦场景 |

## Quick Start

前置条件：只需安装 Go，纯标准库实现。

```bash
cd patterns/09_producer_consumer
go run producer_consumer.go
```

预期输出（节选；**逐条打印的顺序每次运行不同**——并发调度是随机的，但统计结果恒定）：

```text
  Producer 2: produced item 2000
  Producer 3: produced item 3000
  Consumer 1: consumed item 3000
  ...
  Producer 1: finished (17 items)
  Producer 2: finished (17 items)
  Producer 3: finished (16 items)
All producers done, queue closed.
  Consumer 1: finished
  Consumer 2: finished

Total produced: 50
Total consumed: 50
```

50 = 3 个生产者 × (17/17/16)：`totalItems/numProducers=16`，前 `remainder=2` 个生产者各多分 1 个。`Total consumed` 恒等于 50，说明关闭顺序正确、数据不丢。

队列本身只需一行：

```go
queue := make(chan int, 10)  // 容量 10 的有界队列
queue <- item                 // 写入（队列满时阻塞 = 背压）
item := <-queue               // 读取（队列空时阻塞）
close(queue)                  // 关闭后消费者仍可排空剩余数据
```

这段在做什么：`make` 的第二个参数就是缓冲容量；发送在满时挂起、接收在空时挂起，两个方向的等待都由 channel 运行时自动完成。

## How It Works

按数据的流向走：生产者写入 → 缓冲队列 → 消费者排空 → main 按固定顺序收尾。

### 生产者与方向约束

```go
func producer(id int, count int, queue chan<- int, wg *sync.WaitGroup) {
    defer wg.Done()
    for i := 0; i < count; i++ {
        item := id*1000 + i // unique item number
        queue <- item
    }
}
```

这段在做什么：每个生产者写 `count` 个 item 后退出。三个要点：

- item 带唯一前缀（1000/2000/3000），编号全局不重，日志才能对账；
- `chan<- int` 只能发送、`<-chan int` 只能接收——方向约束在编译期防止误用；
- `defer wg.Done()` 保证任何退出路径都通知 WaitGroup（等待一组 goroutine 结束的计数器）。

### 消费者与共享计数器

```go
for item := range queue {   // channel 关闭且排空后自动退出
    mu.Lock()
    *consumed++
    mu.Unlock()
}
```

这段在做什么：`range` 先排空剩余元素、感知到关闭后自动退出。多个消费者并发更新 `totalConsumed`，用 Mutex 保护——你在输出里看到 `Total consumed: 50` 恒定，就是这段计数与优雅关闭共同保证的。（延伸：单纯计数也可以用 `atomic.Int64` 免锁。）

### 优雅关闭四步曲

```go
prodWg.Wait()   // 1. 等所有生产者写完
close(queue)    // 2. 关闭 channel（消费者可排空剩余数据）
consWg.Wait()   // 3. 等所有消费者排空退出
fmt.Printf(...) // 4. 打印统计
```

顺序不可颠倒：生产者还在写就 close，发送端 panic；不等消费者就退出，可能丢失正在处理的数据。这也是 close 的归属原则——**由发送方（且只有发送方）close**。

### 关键字段一览

| 字段/写法 | 含义 | 为什么重要 |
|---|---|---|
| `make(chan int, 10)` | 容量 10 的缓冲 channel | 有界队列 + 天然背压 |
| `chan<- / <-chan` | 只发送 / 只接收 | 编译期约束函数用法 |
| `prodWg / consWg` | 两组 WaitGroup | 分别跟踪生产者、消费者的完成 |
| `defer wg.Done()` | 退出时计数 -1 | 不 defer 则 panic 路径漏通知 |
| `close(queue)` | 只能发送方调用一次 | 重复 close / 接收方 close 都会 panic |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **在生产者未结束时 close**：现象是 panic（send on closed channel）。原因：close 早于最后一次发送。解法：先 `prodWg.Wait()`，再由发送方 close。
- **消费者用 `for { v := <-ch }` 裸接收**：现象是 close 后永远取到零值、死循环不退出。原因：裸接收无法感知关闭。解法：用 `for range` 或 `v, ok := <-ch`。
- **`wg.Done()` 没 defer**：现象是 panic 路径下 `Wait()` 永久阻塞。原因：异常退出跳过了计数 -1。解法：`defer wg.Done()`。
- **忘了先起消费者**：现象是演示一开始全员阻塞。原因：队列很快填满，生产者挂起后无人接收。解法：本程序先起 2 个消费者，保证生产者发送时有人接收。
- **把 close 当清空**：close 不是清空——关闭后剩余元素仍可读，`range` 会先排空再退出。
- **把某次日志顺序当确定行为**：并发输出有随机性，逐条日志的顺序每次不同，恒定的只有统计结果（item 编号唯一让 `Total consumed: 50` 可以对账）。

**Q：close 之后接收方还能读到数据吗？done channel 是什么？**
A：能。close 只承诺不再有新数据，`range` 会先排空剩余元素再退出；`v, ok := <-ch` 中 ok 变 false 表示"已关闭且取空"。

`done := make(chan struct{})` + `close(done)` 是广播"该停了"的惯用法——所有 `<-done` 同时解除阻塞，且关闭后的 channel 永远可读，不会有人漏听。

**Q：背压是怎么产生的？为什么选有界队列而不是无界？**
A：缓冲 channel 满时 `ch <- v` 的 goroutine 被挂起，直到消费者取走元素——阻塞即背压，不需要额外限流组件。

无界队列（slice + mutex）没有这层保护，消费跟不上生产时内存无上限增长直至 OOM（Out of Memory，内存耗尽）。

**Q：如何优雅关闭一个多生产者多消费者的 channel？**
A：机制见上文"优雅关闭四步曲"——核心一句：close 必须发生在所有发送之后，且由发送方执行。

**Q：和 Worker Pool 是什么关系？**
A：Worker Pool 是它的特例：Submit 是（隐式的）生产者，固定 N 个 worker 是消费者，task channel 是中间队列——背压同样由带缓冲任务队列提供。
