# 15 · Select 多路复用：一个入口，随时待命的多个 case

> 一个 goroutine 怎么同时等多个 channel？for 循环轮询既丑又浪费。`select` 是 Go 为这件事定制的语法：哪个 case 就绪走哪个，都不就绪就等，等太久可以放弃，压根不想等也能立刻返回。本实验用三个场景把 select 的随机公平、超时控制与非阻塞探测全部跑出来。

## 1. 为什么需要它

并发程序最常见的形状是"谁先来处理谁"：两个上游 channel、一个超时兜底。手写多个 goroutine 各自转发再合并，代码量翻倍还容易漏掉超时。`select` 把这件事压缩成一个语法结构——它是 channel 世界的 `switch`，但每个 case 都是一个"是否就绪"的运行时询问。理解它的三条铁律（随机、阻塞、default 非阻塞），Go 并发的一半惯用法就通了。

## 2. 总览：核心机制一图看懂

![select 多路复用：一个入口，四个分支](images/select.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/15_select/images/select.html)
> （或本地打开 [`images/select.html`](images/select.html)）。

**心智模型一句话**：select 是一个裁判——数据源把就绪状态报给它，它从就绪的 case 里**随机**挑一个走；都没就绪就按你是否写了 `default` 决定"等"还是"立刻走"。图中裁判左接两个数据源，右通"收到数据"与"放弃等待"两个出口。

## 3. 快速开始

```bash
cd concepts/15_select
go run main.go
```

预期输出（场景一的分支顺序每次运行不同，统计趋近均衡）：

```text
== 场景一：select 监听 ch1/ch2，分支随机 ==
  第 1 次 select → 走 ch2 分支，收到 "ch2 消息 #1"
  第 2 次 select → 走 ch2 分支，收到 "ch2 消息 #2"
  第 3 次 select → 走 ch1 分支，收到 "ch1 消息 #1"
  第 4 次 select → 走 ch1 分支，收到 "ch1 消息 #2"
  分支命中统计：ch1=2 次，ch2=2 次
== 场景二：select + time.After 超时 ==
等待 slow channel（最多 500ms）……
  500ms 超时！放弃等待，走超时分支
== 场景三：default 非阻塞探测 ==
  没数据，干别的去了
```

## 4. 核心概念

### 4.1 随机公平：就绪 case 的选择不看出场顺序

多个 case 同时就绪时，Go 运行时用**伪随机**挑一个——这不是缺陷，是刻意的公平设计，防止"写在前面 case 永远优先"导致的饥饿。demo 场景一里 ch1/ch2 各命中 2 次，多跑几次统计会更均匀。

### 4.2 超时控制：select 与 time.After 组合

`select` 本身没有超时参数，惯用法是往 case 列表里加一个 `time.After(d)` 返回的 channel：其余 case 都没就绪且计时器到了，就走超时分支放弃等待。demo 场景二等一个永远不发数据的 slow channel，500ms 后准时止损。

### 4.3 default：让 select 变成非阻塞探测

有 `default` 分支时 select 永不阻塞：没有任何 case 就绪就立即走 default。典型用途是"有数据就处理，没数据就继续干别的"——比如非阻塞地检查取消信号。注意 default 里如果 `continue` 一个空循环，会把 CPU 打满。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `select {}` | channel 专用多路复用 | case 必须是收/发 channel，不能是普通 IO |
| `case v := <-ch` | 接收分支 | 就绪=缓冲有数据或有发送方在等 |
| `case ch <- v` | 发送分支 | 就绪=缓冲有空间或接收方在等 |
| `time.After(d)` | 延时 channel | 与 select 组合成超时 |
| `default` | 非阻塞分支 | 存在时 select 永不阻塞 |

坑清单：

- `for { select { case <-ch: ... } }` 里写 `time.After(d)`：每次循环重新开始计时，超时永远到不了——长循环用 `time.NewTimer` + `Reset`
- 多个 goroutine 同时 select 同一个 channel，是合法且常见的（运行时保证一条消息只给一个人）
- `default` + 空转循环 = CPU 100%；探测要配合 `time.Sleep` 或做正事
- select 的 case 表达式每次执行都会被求值（包括 channel 本身），别在里面写有副作用的代码

## 6. 文件结构

```text
concepts/15_select/
├── README.md                  # 本文件：原理 + 运行指引 + 深入要点
└── images/
    ├── select.workflow.json   # 图源：select 判定流（Typed JSON IR）
    ├── select.html            # 交互版：自包含单文件
    └── select.svg             # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么多个就绪 case 要随机选？**
A：防止程序员靠排列顺序获得优先级、导致其他 channel 长期饥饿；随机让语言语义与书写顺序解耦。真需要优先级就循环 select 或分层处理。

**Q：select 能监听普通函数调用或文件 IO 吗？**
A：不能。case 只接受 channel 收发。文件/网络 IO 的多路复用用 goroutine 包成 channel，或用 syscall 层的 epoll/kqueue 封装库。

**Q：`for range ch` 和 select 怎么选？**
A：只等一个 channel 且要读到关闭——`for range`；要等多个、要超时、要不阻塞——`select`。两者常组合：外层 select 处理多路与超时，内层 for range 消费。

**Q：time.After 的 timer 会被回收吗？**
A：会，超时触发后由运行时回收；但在长循环里反复调用会产生大量待回收 timer，性能敏感场景用 `time.NewTimer` 复用。

## 8. 总结

select 的全部行为都从三条铁律推出：就绪随机走、无 default 就阻塞、有 default 立即走。用三个场景各触发一条，加上 `time.After` 超时惯用法，channel 的"多选题"就再也难不倒你了。

下一篇 [16_chan_close](../16_chan_close/) 把 channel 的关闭语义单独讲透：零值、ok 探测、for range 退出与重复 close 的 panic。
