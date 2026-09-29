# 11 · Pub/Sub 发布订阅：topic 扇出与慢消费者隔离

> 发布订阅（Publish-Subscribe）是消息系统的核心抽象：发布者不直接调用订阅者，而是把消息发到主题（Topic），由中间的 Broker 负责分发——发布者不知道有多少订阅者，订阅者也不关心消息来自谁。本篇用约 100 行 Go（`map[topic][]*subscription` + buffered channel + `sync.RWMutex`）实现一个并发安全的 Pub/Sub，把扇出语义、慢消费者处理、Unsubscribe 竞态三个核心机制写成看得见、跑得着的代码。

## Background

系统里常见"一个事件、多方关心"的通知需求：价格更新了，行情页、风控、报表都要知道。最直接的做法是 Observer 模式——发布者直接逐个调用订阅者的回调函数。

它有两个问题：发布者必须持有所有订阅者的引用，增删订阅方都要改发布者的代码；调用是同步的，任何一个订阅者卡住都会拖垮发布者。

需要的是：发布者只管把消息发出去、不认识任何订阅者；每个订阅者有自己的收件箱，收得快慢互不影响。中间负责登记与分发的角色，叫 Broker（消息代理）。

## What

**发布订阅（Publish-Subscribe，Pub/Sub）**是一种消息传递模式：发布者把消息发到主题（Topic，消息的逻辑分类），Broker 分发给订阅了该主题的所有订阅者，发布者与订阅者互相不认识、互不阻塞。

心智模型一句话：Broker 只是一张 `map[string][]*subscription`，发布 = 在读锁下拷贝该 topic 的订阅列表快照，再对快照逐个做 `select + default` 非阻塞发送（通道满就走 default 分支、不等待）。

消息随即扇出（一份消息分发给多个接收者）到每个订阅者各自的 channel，慢订阅者的 channel 满了就丢消息，绝不阻塞发布者。

可以把 Broker 想象成杂志社：订户登记地址（订阅），杂志社按品类把新刊寄给所有订户（扇出）。但和杂志社不同的是：这里的"信箱"容量有限（cap 64），信箱塞满时杂志社直接放弃这一份就走，绝不在门口等——丢弃正是慢消费者隔离的手段。

## When to Use

这节回答"什么通知需求适合进程内 Pub/Sub"。

典型场景：

- **进程内事件广播**：配置变更、缓存失效通知——一个组件状态变了，多个组件各自响应，发布者不必认识它们。
- **可丢弃的高频消息**：指标、日志、行情 tick——下游处理不过来时丢几条可接受，但绝不能拖住生产方。
- **订阅关系动态变化**：订阅者随时加入退出，需要并发安全的登记与退订。

何时不用：

- 消息绝对不能丢：本实现是尽力而为语义，需要 ack + 持久化的方案（如 Kafka）。
- 只有一对一通知、同步即可：直接调用比引入 Broker 更简单。
- 需要跨进程、跨机器分发：进程内 channel 方案不适用，应选消息中间件。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| Observer 直接回调 | 同步逐个调用；一个订阅者卡住拖垮发布者 | 订阅者固定、处理极快、同步语义可接受 |
| **本篇 Pub/Sub** | 每订阅独立 buffered channel + 非阻塞扇出；满即丢弃 | 进程内事件广播，可容忍少量丢弃 |
| 消息中间件（Kafka 等） | ack + 持久化，不丢消息；需独立部署 | 跨进程、必须送达的场景 |

## Quick Start

前置条件：只需安装 Go，纯标准库实现。

```bash
cd patterns/11_pubsub
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

Broker 的全部状态就两个结构体：

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

这段在做什么：两级设计——`PubSub.mu` 保护 topic→订阅者列表这张 map；每个 `subscription` 自带一把小锁保护 `closed` 标志。每个订阅是独立的 buffered channel（cap 64），这是"慢消费者隔离"的物理基础。

## How It Works

按生命周期走：Subscribe 登记 → Publish 扇出 → Unsubscribe 退出。

### Subscribe：channel + cleanup 闭包

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

这段在做什么：返回**只读接收 channel** + **取消订阅闭包**——调用方拿到 unsub 即可随时退出，这是 Go 惯用的 "channel + cleanup func" API 设计。

### Publish：RLock → copy → 非阻塞扇出

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

这段在做什么：三个关键点——

- **RLock 读锁**：发布只读订阅列表，多个 Publish 可并行（读多写少场景）；
- **先 copy 再发送**：锁内做 channel 发送是大忌——发送若阻塞会卡死所有 Subscribe/Unsubscribe/Publish；
- **select + default**：channel 满即丢弃，慢消费者不拖垮发布者。

你在输出里看到的 `sent to 2 subs`、`sent to 1 subs`，就来自 `sent++` 这个计数——每次发布实际送达几个订阅者一目了然。

### Unsubscribe：closed 标志防重复 close

```go
s.mu.Lock()
if s.closed { s.mu.Unlock(); return } // 防 close of closed channel panic
s.closed = true
close(s.ch)
s.mu.Unlock()

ps.mu.Lock() // 再从 ps.subs[topic] 按 id 移除；列表空则 delete(topic)
ps.mu.Unlock()
```

这段在做什么：订阅方的 `for range ch` 在 channel 关闭后自然退出——close(ch) 是 Go 中广播退出的标准手段。注意锁的顺序：先锁 subscription 自己的锁完成 close，再锁 Broker 的锁做列表移除，两段临界区各自最小化、不嵌套持有。

输出里 unsub 后 `Publish -> sent to 1 subs`，就来自按 id 从列表移除这一步。

### 关键字段一览

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `ch chan interface{}`（cap 64） | 每订阅独立缓冲通道 | 扇出互不干扰；缓冲吸收瞬时突发 |
| `closed bool` + `mu sync.Mutex` | 每订阅的关闭标志 | 防止重复调用 unsub 时 double-close panic |
| `subs map[string][]*subscription` | topic→订阅者列表 | Broker 的全部状态；读写锁保护 |
| `Publish` 返回 `sent int` | 实际送达的订阅者数 | 让"丢了几个消息"可观测 |
| `select { case ch<-msg: default: }` | 非阻塞发送 | 慢消费者隔离的核心一行 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **持锁发送**：现象是某订阅者 channel 满时全 Broker 卡死。原因：`RLock` 期间直接 `ch <- msg`，发送阻塞会卡死所有 Subscribe/Unsubscribe/Publish。解法：坚持 "lock, copy, unlock, then send"。
- **重复 Unsubscribe**：现象是 `close of closed channel` panic。原因：unsub 被调两次。解法：用每订阅的 `closed` 标志在锁内"检查→置位→close"。
- **向已关 channel 发送**：现象是 send on closed channel panic。原因：unsub 后订阅对象可能仍在当次拷贝的快照里。解法：Publish 发送前检查 `closed` 并跳过。
- **把丢弃当事故**：现象是高压下订阅者少收消息。原因：本实现是"尽力而为"语义，满即丢是设计行为。解法：要保证不丢得改阻塞发送（背压）、无界队列（OOM 风险）或 ack+持久化（Kafka 的做法）。
- **锁顺序嵌套**：现象是死锁面扩大。原因：同时持有订阅者锁与 Broker 锁。解法：先订阅者的锁后 Broker 的锁，且两段临界区不嵌套。

**Q：Publish 的扇出语义是什么？消息会丢吗？**
A：机制见上文 Publish 小节与坑清单第 4 条；决策准则一句话——取决于业务能容忍丢消息，还是容忍发布者被背压变慢。

**Q：慢消费者怎么处理？**
A：三种策略：① 丢弃（本实现，适合指标/日志类可丢消息）；② 阻塞发布者（背压传导，适合必须送达的场景）；③ 每订阅者独立 goroutine + 无界队列（隔离但耗内存）。答案取决于消息的业务语义，没有银弹。

**Q：为什么 Publish 用 RLock 而不是 Lock？发送前为什么要 copy 列表？**
A：一句话——Publish 只读 `subs` map，读锁允许多个发布并行；copy 是为了 "lock, copy, unlock, then send"，锁粒度必须最小化（细节见上文 Publish 小节）。

**Q：和 Observer 模式的区别？**
A：Observer 是同步回调（逐个调 handler，一个卡全卡）；Pub/Sub 异步解耦（消息进 channel，各订阅者按自己的节奏消费），且天然可扩展到跨进程（Kafka、NSQ）。
