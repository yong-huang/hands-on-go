# 15 · Select 多路复用：一个入口，随时待命的多个 case

> `select` 是 Go 为"一个 goroutine 同时等多个 channel"定制的语法：哪个 case 就绪走哪个，都不就绪就等，等太久可以放弃，压根不想等也能立刻返回。本篇用三个场景跑出它的随机公平、超时控制与非阻塞探测；读完你能掌握三条行为规则——就绪随机走、无 `default` 就阻塞、有 `default` 立即走。

## Background

并发程序最常见的形状是"谁先来处理谁"：两个上游数据源、一个超时兜底。

没有 select 时，一个 goroutine 无法直接同时等待多个 channel——写两个 `<-ch` 是先后等待，不是同时等待。要实现"谁先来处理谁"，得手写多个 goroutine 各自接收再转发到一条合并 channel，代码量翻倍还容易漏掉超时。

轮询是另一种替代：反复探测"有数据吗"。它既浪费 CPU，又要在"问得太勤"和"反应太慢"之间两难。

Go 为此内置了 `select` 语句，把"等多个数据源 + 超时兜底"压缩成一个语法结构。

## What

一句话定义：select 是一种专门同时等待多个 channel 收发操作的语句（即多路复用，多个数据流共用一个等待点），用来在就绪的分支里挑一个执行；都不就绪时，按有无 `default` 决定阻塞等待还是立即返回。

可以把 select 想象成一个裁判：每个数据源把自己的就绪状态报给裁判，裁判从就绪者里随机点名一位。但和普通 `switch` 不同的是，select 匹配的不是"值相等"，而是"就绪与否"这个运行时状态——case 的书写顺序不影响结果。

三条行为规则：

- 随机公平：多个 case 同时就绪时，用伪随机挑一个，不看出场顺序
- 无 `default` 阻塞：都不就绪时整个 select 挂起，直到某个 case 就绪
- 有 `default` 立即走：都不就绪时执行 `default` 分支，永不阻塞

## When to Use

在做什么事的时候选它：

- 在合并多个上游数据源时（比如同时监听任务队列与取消信号），一个 select 循环处理两路输入，谁先来先处理谁。
- 在给可能迟到的等待加时限时（比如等下游响应最多 500ms），用 select + `time.After` 组成超时兜底。
- 在做"有活就干、没活干别的"的调度循环时，用 select + `default` 做非阻塞探测，不卡在某一处。

何时不用：只等一个 channel 且要读到关闭时，`for range ch` 更直接；严格顺序的等待（B 必须等 A 完成）也用不上 select。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `for range ch` | 只盯一个 channel，读到关闭退出 | 单一数据源的消费循环 |
| `select`（本篇） | 同时等多个 case，随机/超时/default 可组合 | 多数据源、需要超时或非阻塞 |
| 手写多个 goroutine 转发合并 | 灵活但代码翻倍，超时要自己做 | case 之间有复杂变换、select 表达不下时 |

## Quick Start

前置条件：本机装有 Go，无第三方依赖。

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

场景一的核心循环（两个 goroutine 各发 2 条；select 每轮从就绪分支里随机挑一个）：

```go
	for i := 0; i < 4; i++ {
		// 两个 channel 就绪与否由调度决定；
		// 若同时就绪，select 均匀随机选择，不偏向书写顺序。
		select {
		case msg := <-ch1:
			received["ch1"]++
			fmt.Printf("  第 %d 次 select → 走 ch1 分支，收到 %q\n", i+1, msg)
		case msg := <-ch2:
			received["ch2"]++
			fmt.Printf("  第 %d 次 select → 走 ch2 分支，收到 %q\n", i+1, msg)
		}
	}
```

超时与非阻塞的写法（前一段加 `time.After` 分支组成 500ms 兜底；后一段加 `default` 让 select 永不阻塞）：

```go
	select {
	case msg := <-slow:
		fmt.Println("  收到：", msg)
	case <-time.After(500 * time.Millisecond):
		fmt.Println("  500ms 超时！放弃等待，走超时分支")
	}
```

```go
	select {
	case v := <-empty:
		fmt.Println("  意外收到", v)
	default:
		// 有 default 时 select 永不阻塞：所有分支没就绪就立刻走这里
		fmt.Println("  没数据，干别的去了")
	}
```

## How It Works

### 随机公平：就绪 case 的选择不看出场顺序

多个 case 同时就绪时，Go 运行时用**伪随机**挑一个——这不是缺陷，是刻意的公平设计，防止"写在前面 case 永远优先"导致的饥饿（某路数据因抢不到执行机会而长期得不到处理）。

Quick Start 场景一里 ch1/ch2 各命中 2 次，且四轮顺序是 ch2、ch2、ch1、ch1——多跑几次，分支顺序会变，统计趋近均衡。输出里的"分支命中统计"一行，就来自 demo 里那张 `received` 计数表。

### 超时控制：select 与 time.After 组合

`select` 本身没有超时参数，惯用法是往 case 列表里加一个 `time.After(d)` 返回的 channel：其余 case 都没就绪且计时器到了，就走超时分支放弃等待。

场景二等的是一个 1 秒后才发数据的 slow channel，500ms 后准时止损。输出里"500ms 超时！"一行，就是 `time.After` 那个分支被选中的瞬间。

### default：让 select 变成非阻塞探测

有 `default` 分支时 select 永不阻塞：没有任何 case 就绪就立即走 default。典型用途是"有数据就处理，没数据就继续干别的"——比如非阻塞地检查取消信号。

场景三的 `empty` 是无缓冲且无人发送的 channel，此刻必然没数据，于是直接走 default，输出"没数据，干别的去了"。

语法速查：

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `select {}` | channel 专用多路复用 | case 必须是收/发 channel，不能是普通 IO |
| `case v := <-ch` | 接收分支 | 就绪=缓冲有数据或有发送方在等 |
| `case ch <- v` | 发送分支 | 就绪=缓冲有空间或接收方在等 |
| `time.After(d)` | 延时 channel | 与 select 组合成超时 |
| `default` | 非阻塞分支 | 存在时 select 永不阻塞 |

## Pitfalls & Q&A

坑清单（每条按"现象 → 原因 → 解法"读）：

- **现象**：`for { select { case <-time.After(d): ... } }` 里的超时永远到不了。**原因**：`time.After(d)` 写在循环体内，每次循环都重新开始计时。**解法**：长循环改用 `time.NewTimer` + `Reset` 复用同一个计时器。
- **现象**：default 探测写成 `for { select { ... default: continue } }`，CPU 打满。**原因**：default 让 select 永不阻塞，空转循环全速旋转。**解法**：探测配合 `time.Sleep`，或在 default 分支里做正事。
- **现象**：在 case 表达式里调用带副作用的函数，行为难以解释。**原因**：select 的 case 表达式每次执行都会被求值（包括 channel 本身）。**解法**：case 里只写 channel 收发，副作用放进分支体。
- **现象**：担心多个 goroutine 同时 select 同一条 channel 会重复消费。**原因**：对运行时语义的误解——这是合法且常见的写法，运行时保证一条消息只交给一个接收者。**解法**：无需额外加锁，直接用。

**Q：为什么多个就绪 case 要随机选？**
A：防止程序员靠排列顺序获得优先级、导致其他 channel 长期饥饿；随机让语言语义与书写顺序解耦。真需要优先级就循环 select 或分层处理。

**Q：select 能监听普通函数调用或文件 IO 吗？**
A：不能。case 只接受 channel 收发。文件/网络 IO 的多路复用用 goroutine 包成 channel，或用 syscall 层的 epoll/kqueue 封装库。

**Q：`for range ch` 和 select 怎么选？**
A：只等一个 channel 且要读到关闭——`for range`；要等多个、要超时、要不阻塞——`select`。两者常组合：外层 select 处理多路与超时，内层 for range 消费。

**Q：time.After 的 timer 会被回收吗？**
A：会，超时触发后由运行时回收；但在长循环里反复调用会产生大量待回收 timer，性能敏感场景用 `time.NewTimer` 复用。
