# 11 · Pub/Sub 发布订阅：topic 扇出与慢消费者隔离

> 发布订阅（Publish-Subscribe）是消息系统的核心抽象：发布者不直接调用订阅者，而是把消息发到主题（Topic），由中间的 Broker 负责分发——发布者不知道有多少订阅者，订阅者也不关心消息来自谁。本实验用约 100 行 Go（`map[topic][]*subscription` + buffered channel + `sync.RWMutex`）实现一个并发安全的 Pub/Sub，把"扇出语义、慢消费者处理、Unsubscribe 竞态"这三个面试高频考点变成看得见、跑得着的代码。

## 1. 为什么需要它

直接调用式的通知（Observer 模式）有两个问题：发布者必须持有所有订阅者的引用，且任一订阅者卡住都会拖垮发布者。Pub/Sub 用一个中间 Broker 把两边解耦，并让**每个订阅者拥有自己独立的 buffered channel**——消息扇出后各走各的管道，一个慢消费者最多丢自己的消息，不影响别人。这也是面试中检验 channel、锁、map 组合运用能力的经典题：怎么设计订阅结构？发布时怎么持锁？退订时怎么避免 double-close panic？

## 2. 总览：核心机制一图看懂

![Pub/Sub Broker：topic → 订阅者列表，发布扇出到各自 channel](images/pubsub.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/11_pubsub/images/pubsub.html)
> （或本地打开 [`images/pubsub.html`](images/pubsub.html)）。

**心智模型一句话**：Broker 只是一张 `map[string][]*subscription`，发布 = 在读锁下拷贝该 topic 的订阅列表快照，再对快照逐个做 `select + default` 非阻塞发送——扇出到每个订阅者各自的 channel，慢订阅者的 channel 满了就丢消息，绝不阻塞发布者。

## 3. 快速开始

```bash
cd interview/11_pubsub
go run pubsub.go    # 演示：订阅 → 发布扇出 → 取消订阅 → 未知 topic
```

预期输出（节选，订阅者 goroutine 的打印顺序每次运行可能不同）：

```text
[4] Publishing messages:
  subscribers('news'): 2  ·  subscribers('alerts'): 1
  Publish('news', 'Breaking...') -> sent to 2 subs
  Publish('alerts', 'CPU > 90%') -> sent to 1 subs
  [alerts subscriber] got: CPU usage > 90%
  [news subscriber #2] got: Breaking: Go 1.24 released!
  [news subscriber] got: Breaking: Go 1.24 released!

[5] Unsubscribe 'news subscriber #2':
  subscribers('news'): 1
  Publish -> sent to 1 subs

[6] Publish to non-existent topic:
  Publish('unknown', 'hello') -> sent to 0 subs
```

看点：同一消息 `sent to 2 subs` 表示扇出到 news 的两个订阅者；unsub 后同 topic 只 `sent to 1 subs`；发到不存在的 topic 返回 0 而不是报错。

## 4. 核心概念

### 4.1 核心数据结构

```go
type subscription struct {
    id     uint64
    topic  string
    ch     chan interface{} // buffered, cap 64
    closed bool
    mu     sync.Mutex
}

type PubSub struct {
    mu     sync.RWMutex
    subs   map[string][]*subscription
    nextID uint64
}
```

两级设计：`PubSub.mu` 保护 topic→订阅者列表这张 map；每个 `subscription` 自带一把小锁保护 `closed` 标志。每个订阅是独立的 buffered channel（cap 64），这是"慢消费者隔离"的物理基础。

### 4.2 Subscribe：channel + cleanup 闭包

```go
func (ps *PubSub) Subscribe(topic string) (<-chan interface{}, func()) {
    s := &subscription{
        id: atomic.AddUint64(&ps.nextID, 1),
        ch: make(chan interface{}, 64),
    }
    ps.mu.Lock()
    ps.subs[topic] = append(ps.subs[topic], s)
    ps.mu.Unlock()
    unsub := func() { /* close + 从列表移除，见 4.4 */ }
    return s.ch, unsub
}
```

返回**只读接收 channel** + **取消订阅闭包**——调用方拿到 unsub 即可随时退出，这是 Go 惯用的 "channel + cleanup func" API 设计。

### 4.3 Publish：RLock → copy → 非阻塞扇出

```go
ps.mu.RLock()
copied := make([]*subscription, len(list))
copy(copied, list)
ps.mu.RUnlock() // lock, copy, unlock, then send

for _, s := range copied {
    s.mu.Lock()
    if s.closed { s.mu.Unlock(); continue } // 快照里可能已有已退订者
    select {
    case s.ch <- msg:
        sent++
    default: // skip slow consumer
    }
    s.mu.Unlock()
}
```

三个关键点：

- **RLock 读锁**：发布只读订阅列表，多个 Publish 可并行（读多写少场景）
- **先 copy 再发送**：锁内做 channel 发送是大忌——发送若阻塞会卡死所有 Subscribe/Unsubscribe/Publish
- **select + default**：channel 满即丢弃，慢消费者不拖垮发布者；`sent` 报告实际送达数

### 4.4 Unsubscribe：closed 标志防重复 close

```go
s.mu.Lock()
if s.closed { s.mu.Unlock(); return } // 防 close of closed channel panic
s.closed = true
close(s.ch)
s.mu.Unlock()

ps.mu.Lock() // 再从 ps.subs[topic] 按 id 移除；列表空则 delete(topic)
ps.mu.Unlock()
```

订阅方的 `for range ch` 在 channel 关闭后自然退出——close(ch) 是 Go 中广播退出的标准手段。注意锁的顺序：先锁 subscription 自己的锁完成 close，再锁 Broker 的锁做列表移除，两段临界区各自最小化、不嵌套持有。

## 5. 关键字段与坑清单

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `ch chan interface{}`（cap 64） | 每订阅独立缓冲通道 | 扇出互不干扰；缓冲吸收瞬时突发 |
| `closed bool` + `mu sync.Mutex` | 每订阅的关闭标志 | 防止重复调用 unsub 时 double-close panic |
| `subs map[string][]*subscription` | topic→订阅者列表 | Broker 的全部状态；读写锁保护 |
| `Publish` 返回 `sent int` | 实际送达的订阅者数 | 让"丢了几个消息"可观测 |
| `select { case ch<-msg: default: }` | 非阻塞发送 | 慢消费者隔离的核心一行 |

坑清单：

- **持锁发送**：`RLock` 期间直接 `ch <- msg`，一旦某订阅者 channel 满且无 default，全 Broker 卡死——必须 "lock, copy, unlock, then send"
- **重复 Unsubscribe**：unsub 被调两次会 `close of closed channel` panic，必须用 `closed` 标志在锁内先检查再 close
- **向已关 channel 发送**：unsub 后订阅对象可能仍在当次拷贝的快照里，Publish 发送前要检查 `closed` 并跳过，否则 send on closed channel panic
- **消息丢弃是特性也是陷阱**：本实现是"尽力而为"语义；要保证不丢得改阻塞发送（背压）、无界队列（OOM 风险）或 ack+持久化（Kafka 的做法）
- **锁顺序**：先订阅者的锁后 Broker 的锁，且两段临界区不嵌套——嵌套持有会扩大死锁面

## 6. 文件结构

```text
interview/11_pubsub/
├── README.md                      # 本文件：原理 + 运行指引 + 面试要点
├── pubsub.go                      # 主教材：订阅/发布/退订/慢消费者 + demo
└── images/
    ├── pubsub.architecture.json   # 图源：Broker 扇出架构（Typed JSON IR）
    ├── pubsub.html                # 交互版：可缩放/高亮连线的自包含页面
    └── pubsub.svg                 # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：Publish 的扇出语义是什么？消息会丢吗？**
A：`select + default` 非阻塞发送，订阅者缓冲满即丢弃（尽力而为语义），返回值 `sent` 报告送达数。要保证不丢：改阻塞发送让发布者被背压、或引入 ack + 持久化——取决于业务能容忍丢消息还是容忍发布者变慢。

**Q：慢消费者怎么处理？**
A：三种策略：① 丢弃（本实现，适合指标/日志类可丢消息）；② 阻塞发布者（背压传导，适合必须送达的场景）；③ 每订阅者独立 goroutine + 无界队列（隔离但耗内存）。答案取决于消息的业务语义，没有银弹。

**Q：Unsubscribe 有什么竞态？怎么防？**
A：两个：① unsub 调两次会 double-close panic——用每订阅的 `closed` 标志在锁内"检查→置位→close"；② Publish 正在遍历快照时某订阅被关——Publish 对快照里 `closed==true` 的订阅跳过发送，避免向已关闭 channel 发送 panic。

**Q：为什么 Publish 用 RLock 而不是 Lock？发送前为什么要 copy 列表？**
A：Publish 只读 `subs` map，读锁允许多个发布并行，是典型读多写少优化。copy 是为了 "lock, copy, unlock, then send"——若持锁期间做 channel 发送，发送阻塞会卡死所有 Subscribe/Unsubscribe/Publish，锁粒度必须最小化。

**Q：和 Observer 模式的区别？**
A：Observer 是同步回调（逐个调 handler，一个卡全卡）；Pub/Sub 异步解耦（消息进 channel，各订阅者按自己的节奏消费），且天然可扩展到跨进程（Kafka、NSQ）。手写易错点：锁内发送、double-close、向已关 channel 发送。

## 8. 总结

Pub/Sub 的全部行为都能从"一张 map + 每订阅一个 buffered channel"推出来：扇出是遍历快照的自然结果，慢消费者隔离是 select-default 的必然代价，退出广播靠 close(ch)。记住验证方法——`go run pubsub.go` 看每次发布的 `sent` 数和订阅者收到的顺序，比背结论可靠得多。

下一篇 [12_conn_pool](../12_conn_pool/) 继续看并发资源管理：连接池如何用空闲队列 + 信号量做限流，Get 不到连接时是创建还是等待。
