# 12 · 无缓冲 channel：发送与接收必须在同一瞬间握手

> 无缓冲 channel 是 Go 里最纯粹的同步原语：它不存任何值，每次发送都必须等到一个接收方就绪才能完成——两个 goroutine 在那一刻"会合"（rendezvous）。没有它，你想让两个并发执行流严格交替，就只能靠 sleep 猜时间或加锁；有了它，"谁先谁后"直接由通信本身编排。本实验用 ping/pong 接力把这条规则跑给你看。

## 1. 为什么需要它

有共享就要有同步。如果两个 goroutine 只靠"猜对方跑到哪了"来交换数据，就会写出依赖时序的脆代码。无缓冲 channel 把同步做成了**语义**：`ch <- v` 这一行不返回，直到另一端恰好有人执行 `<-ch`。于是"发送方阻塞到接收方就绪"不再是你额外保证的事，而是语言保证的事——本实验的严格 ping/pong 交替，就是这条保证的直接产物。

## 2. 总览：核心机制一图看懂

![无缓冲 channel：ping/pong 交替的发送-接收握手时序](images/pingpong.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/12_unbuffered_chan/images/pingpong.html)
> （或本地打开 [`images/pingpong.html`](images/pingpong.html)）。

**心智模型一句话**：无缓冲 channel 是一次"击掌"而不是一个"信箱"——两只手（send 与 recv）必须同时伸出，值在击掌瞬间直接从一方交到另一方，channel 本身不存放任何东西。

## 3. 快速开始

```bash
cd concepts/12_unbuffered_chan
go run main.go
```

预期输出（节选，序号严格递增、绝不跳号）：

```text
第  1 次: pong 收到 → 立即回球 (t=01:41:57.095)
第  2 次: pong 收到 → 立即回球 (t=01:41:57.096)
第  3 次: pong 收到 → 立即回球 (t=01:41:57.096)
...
第 10 次: pong 收到 → 立即回球 (t=01:41:57.096)
完成 10 次往返，耗时 136µs（平均每次握手 ≈ 6.85µs）
```

耗时是真实测量值，每次运行会变；不变的是**顺序**：序号 1..10 逐次出现，从不存在"连续两次 ping 中间没有 pong"——因为每次 `serve <- i` 都被迫等到了 pong 的 `<-serve`。

## 4. 核心概念

### 4.1 会合（rendezvous）：send 与 recv 同时就绪才放行

`ch := make(chan int)`（容量 0）后，`ch <- v` 会阻塞，直到某个 goroutine 执行 `<-ch`；反之亦然。两者在时间上重合的那一刻，值直接从发送方栈复制到接收方栈，channel 中间不留副本。这就是"用通信来共享内存"的最小实现。

### 4.2 握手即同步点

本 demo 用两个无缓冲 chan 交替接力：`serve` 传递回合数（ping 发球），`ball` 传回确认（pong 回球）。`serve <- i` 不返回，pong 就没收到球；`<-ball` 不返回，下一轮发球就不会开始——**两个方向各是一次握手，合起来构成严格交替**。main goroutine 扮演 ping 侧，pong 在独立 goroutine 里。

### 4.3 结束：close + 接收的第二返回值

无缓冲 chan 不会自己"空"，要结束就得显式 `close(serve)`。pong 侧用 `n, ok := <-serve` 感知：`ok=false` 表示已关闭且再无值可收，于是退出并 `close(done)` 广播给 main。`<-done` 让 main 等 pong 完全退出，程序才干净结束。

### 4.4 对照：换成有缓冲就失去这层握手

`make(chan int, 1)` 的 `ch <- 42` 会立即返回——值滑进缓冲区，接收方此刻不必存在。"发送完成"与"接收方就绪"从此解耦，本 demo 的严格交替保证随之消失。有缓冲的适用场景（削峰、解耦生产消费速率）见下一篇。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `make(chan int)` | 无缓冲（容量 0） | send 阻塞到 recv 就绪，天然同步点 |
| `serve <- i` | 发送（阻塞） | 对端不在 `<-serve` 上等，这行就走不动 |
| `n, ok := <-serve` | 接收 + 关闭探测 | `ok=false` 表示已关闭且无值 |
| `close(serve)` | 关闭 channel | 只应由发送方关闭；关闭后可收尽剩余值 |
| `<-done` | 等待结束信号 | 配合 `close(done)` 实现 goroutine 汇合 |

坑清单：

- 无缓冲 chan 在**双方都到齐之前谁也不放行**：main 里先 `ch <-` 而接收方还没 `go` 出去，一样是死锁（`fatal error: all goroutines are asleep`）
- 向已关闭的 chan 发送会 panic；重复 close 同样 panic——关闭是发送方的专属动作
- 从已关闭的无缓冲 chan 接收立即返回零值 + `ok=false`，不会阻塞
- 无缓冲 ≠ 队列长度 1：`make(chan int, 1)` 是有缓冲，`ch <- 1` 能立即返回
- 未使用的变量是编译错——demo 里每个 chan（serve/ball/done）都被真实收发过

## 6. 文件结构

```text
concepts/12_unbuffered_chan/
├── README.md                       # 本文件：原理 + 运行指引 + 深入要点
└── images/
    ├── pingpong.sequence.json      # 图源：ping/pong 交替与两次握手的时序（Typed JSON IR）
    ├── pingpong.html               # 交互版：自包含单文件，可缩放/切换主题
    └── pingpong.svg                # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么无缓冲 channel 被称为"同步"channel？**
A：因为它把"数据到达"与"双方都就绪"绑成同一时刻：send 返回时，值不仅已发出，而且**已被接收**。你可以把它当作带数据的同步屏障用——本实验的交替控制正是这种用法。

**Q：值在 channel 里"存"在哪儿？**
A：不存。运行时把发送方的值直接复制到接收方（必要时经 sudog 队列临时持有），无缓冲 chan 只是会合的媒介。对比有缓冲 chan 的环形缓冲区，这是两者最本质的实现差异。

**Q：怎么证明 send 确实等到了 recv？**
A：把 demo 的序号打印换成发送前/发送后各打一个时间戳，会发现 `serve <- i` 完成时刻总与 pong 的收球动作衔接；若把任一端注释掉，程序直接死锁而不是"先跑一段再停"。

**Q：什么时候该选无缓冲，什么时候选有缓冲？**
A：要"交接确认"（背压、步骤编排、握手）选无缓冲；要"解耦速率、容忍突发"选有缓冲。常见误用是给无缓冲 chan 找不到对端就发送——那是死锁，不是排队。

## 8. 总结

无缓冲 channel 的全部语义浓缩成一句话：**发送完成的瞬间，就是接收发生的瞬间**。把 §3 的输出跑一遍、对着 §2 的握手时序数一数两个同步点，"用通信同步执行流"就不再是口号。

下一篇 [../13_buffered_chan/](../13_buffered_chan/) 看有缓冲 channel：容量如何改变阻塞时机、满/空/关闭三个状态怎样切换——正是本实验结尾"对照版本"的完整展开。
