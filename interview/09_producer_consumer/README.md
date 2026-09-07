# 09 · Producer-Consumer：多生产者多消费者与背压

> 生产者-消费者是并发编程中最经典的模式：多个生产者向共享队列写数据，多个消费者从队列读并处理。在 Go 里一个带缓冲 channel 就同时充当线程安全队列和同步原语——队列满时生产者阻塞，这个阻塞本身就是背压（backpressure）。本实验用标准库实现完整的多生产者多消费者模型，重点讲清 close 语义与优雅关闭顺序，这是面试的最高频追问。

## 1. 为什么需要它

直接调用会让生产与消费耦合、无法并行；共享变量 + Mutex 灵活但锁管理容易出错。Go 的 channel 既是**通信机制**又是**同步机制**，`make(chan int, 10)` 一行就得到有界队列：满则发送方挂起（天然限流，防止生产远快于消费导致内存爆炸），空则接收方挂起。程序演示三件事：多生产者并发写入、多消费者 `range` 排空、"等生产者 → close → 等消费者" 的优雅关闭。

## 2. 总览：核心机制一图看懂

![多生产者 → 缓冲 channel → 多消费者：阻塞即背压](images/pc.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/09_producer_consumer/images/pc.html)
> （或本地打开 [`images/pc.html`](images/pc.html)）。

**心智模型一句话**：带缓冲 channel 就是有界队列——左边三个生产者 `queue <- item`，满则阻塞（背压）；右边两个消费者 `for range` 排空，空则阻塞；main 在下方按 `prodWg.Wait → close → consWg.Wait` 的固定顺序收尾，保证 50 个 item 不丢不重。

## 3. 快速开始

```bash
cd interview/09_producer_consumer
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

## 4. 核心概念

### 4.1 Buffered Channel — 有界队列

```go
queue := make(chan int, 10)  // 容量 10 的有界队列
queue <- item                 // 写入（队列满时阻塞 = 背压）
item := <-queue               // 读取（队列空时阻塞）
close(queue)                  // 关闭后消费者仍可排空剩余数据
```

close 的语义要记牢：close 只通知"不会再有新数据"，不会丢弃未读元素。消费者继续 `range` 把剩余元素读完，读完后循环自动退出。对已关闭的 channel 再 close 会 panic；向已关闭的 channel 发送也会 panic。

### 4.2 生产者与方向约束

```go
func producer(id int, count int, queue chan<- int, wg *sync.WaitGroup) {
    defer wg.Done()
    for i := 0; i < count; i++ {
        item := id*1000 + i // unique item number
        queue <- item
    }
}
```

- 每个 producer 的 item 有唯一前缀（1000/2000/3000），编号全局不重
- `chan<- int` 只能发送、`<-chan int` 只能接收——方向约束在编译期防止误用
- `defer wg.Done()` 保证退出必通知

### 4.3 消费者与共享计数器

```go
for item := range queue {   // channel 关闭且排空后自动退出
    mu.Lock()
    *consumed++
    mu.Unlock()
}
```

多个消费者并发更新 `totalConsumed`，用 Mutex 保护。（面试延伸：单纯计数也可以用 `atomic.Int64` 免锁。）

### 4.4 优雅关闭四步曲

```go
prodWg.Wait()   // 1. 等所有生产者写完
close(queue)    // 2. 关闭 channel（消费者可排空剩余数据）
consWg.Wait()   // 3. 等所有消费者排空退出
fmt.Printf(...) // 4. 打印统计
```

顺序不可颠倒：生产者还在写就 close，发送端 panic；不等消费者就退出，可能丢失正在处理的数据。这也是 close 的归属原则——**由发送方（且只有发送方）close**。

## 5. 关键字段与坑清单

| 字段/写法 | 含义 | 为什么重要 |
|---|---|---|
| `make(chan int, 10)` | 容量 10 的缓冲 channel | 有界队列 + 天然背压 |
| `chan<- / <-chan` | 只发送 / 只接收 | 编译期约束函数用法 |
| `prodWg / consWg` | 两组 WaitGroup | 分别跟踪生产者、消费者的完成 |
| `defer wg.Done()` | 退出时计数 -1 | 不 defer 则 panic 路径漏通知 |
| `close(queue)` | 只能发送方调用一次 | 重复 close / 接收方 close 都会 panic |

坑清单：

- **先启动消费者再启动生产者**：本程序先起 2 个消费者，保证生产者发送时有人接收，避免演示一开始就全员阻塞
- **close 不是清空**：关闭后剩余元素仍可读，`range` 会先排空再退出
- **item 编号要唯一**：`id*1000 + i` 让日志可对账，`Total consumed: 50` 才能证明不丢不重
- **并发输出有随机性**：逐条日志的顺序每次不同，不要拿某次顺序当"确定行为"

## 6. 文件结构

```text
interview/09_producer_consumer/
├── README.md                      # 本文件：原理 + 运行指引 + 面试要点
├── producer_consumer.go           # 主教材：3 生产者 → 缓冲 channel → 2 消费者 + 优雅关闭
└── images/
    ├── pc.architecture.json       # 图源：多生产者/缓冲 channel/多消费者架构（Typed JSON IR）
    ├── pc.html                    # 交互版：可缩放/高亮连线的自包含页面
    └── pc.svg                     # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：如何优雅关闭一个多生产者多消费者的 channel？**
A：等生产者完成（`prodWg.Wait()`）→ 发送方 `close(queue)` → 等消费者排空（`consWg.Wait()`）。核心是 close 必须发生在所有发送之后，否则向已关闭 channel 发送会 panic。

**Q：close 之后接收方还能读到数据吗？done channel 是什么？**
A：能。close 只承诺不再有新数据，`range` 会先排空剩余元素再退出；`v, ok := <-ch` 中 ok 变 false 表示"已关闭且取空"。`done := make(chan struct{})` + `close(done)` 是广播"该停了"的惯用法——所有 `<-done` 同时解除阻塞，且关闭后的 channel 永远可读，不会有人漏听。

**Q：背压是怎么产生的？为什么选有界队列而不是无界？**
A：缓冲 channel 满时 `ch <- v` 的 goroutine 被挂起，直到消费者取走元素——阻塞即背压，不需要额外限流组件。无界队列（slice + mutex）没有这层保护，消费跟不上生产时内存无上限增长直至 OOM。

**Q：手写这个模式最容易错在哪？**
A：① 在生产者未结束时 close（panic）；② 消费者用 `for { v := <-ch }` 而不用 `range`/`ok`，close 后永远取到零值死循环；③ `wg.Done()` 没 defer，panic 路径下 `Wait()` 永久阻塞；④ 忘了先起消费者，全场互相等待。

**Q：和 Worker Pool 是什么关系？**
A：Worker Pool 是它的特例：Submit 是（隐式的）生产者，固定 N 个 worker 是消费者，task channel 是中间队列——背压同样由带缓冲任务队列提供。

## 8. 总结

带缓冲 channel 把"线程安全队列 + 同步 + 背压"压缩进一个类型，优雅关闭只需记一个顺序：**等生产者 → close → 等消费者**。把 `Total produced: 50 / Total consumed: 50` 跑出来，比背十遍结论都可靠。

下一篇 [10_shard_map](../10_shard_map/) 看并发下的另一件武器：把一个 map 切成多个 shard，如何让写并行度等于 shard 数。
