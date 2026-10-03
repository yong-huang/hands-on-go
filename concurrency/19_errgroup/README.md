# 19 · errgroup：首错取消、并发上限与结构化并发的手写复刻

> 手写 x/sync/errgroup 的核心：WaitGroup 联结、sync.Once 首错独占、令牌通道限流、cancel 联动取消。读完能说清"一个任务失败整组如何 <10ms 停机"、"SetLimit 为什么能精确卡住峰值并发"，以及裸 goroutine 在同一场景下如何失控。

## Background

管理一组子任务时，早期做法是 `sync.WaitGroup` 加一个带锁的错误切片：每个
goroutine 把自己的错误追加进去，Wait 之后调用方逐条翻看。这套写法能收回
结果，却回答不了两个问题。

第一个问题是失败联动：50 个子任务里第 3 个已经失败，剩下 47 个还在全速
跑完。第二个问题是句柄缺失：错误只能通过预先布置的容器传回，容器没布置
到位（比如临时起的一个 goroutine），错误和 goroutine 就一起失联。

errgroup（golang.org/x/sync 在 2016 年收编的包）把这两个答案标准化：
错误取第一个，失败立即广播取消；goroutine 的生命周期被 Wait 圈死。
本篇徒手复刻它的核心协议，并把行为钉进 -race 测试。

## What

**定义**：errgroup 是"会传错误、会联动取消、会限流"的任务组管理器，
Group 结构体只有五个字段，是三个标准件加一条取消通道的组合。

| 部件 | 职责 |
|---|---|
| `wg sync.WaitGroup` | 联结（join，等全部任务返回再放行）：生命周期的圈 |
| `sem chan struct{}` | 令牌通道：容量即并发上限，nil 表示不限 |
| `errOnce + err` | 首错独占：只有第一个非 nil 错误生效 |
| `cancel func()` | WithContext 注入的取消函数，首错或 Wait 时触发 |
| `ctx` | 派生给任务的停机令，`ctx.Done()` 即"全组收工"信号 |

可以把 Group 想象成登山队的结组绳：一人坠落（返回错误），绳子绷紧，
全组立刻制动（收到取消）。但和真绳子不同的是，制动信号是广播不是拽拽
——每个成员各自从 `ctx.Done()` 读到信号，互不阻塞，先退出的先走。

## When to Use

- 一批子任务"要么全成、要么尽快一起撤"：批量抓取、扇出探测、并行回源
- 需要"最多 N 个同时在跑"的扇出：SetLimit 给提交方背压（backpressure，
  上游被迫等待而不是无限派生，从而保护下游）
- 想给 goroutine 泄漏上一道结构保险：Wait 返回即组内 goroutine 清零

何时不用：没有失败联动需求、只要"等全部做完"，WaitGroup 更轻；任务
结果需要按序收集，直接给每个任务分下标写切片更直白。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 裸 `go` + channel | 无联结、无取消，句柄全靠手工布置 | 只跑不回首的火后不理任务 |
| `sync.WaitGroup` | 只联结，无错误无取消 | 无失败语义的纯汇合点 |
| x/sync errgroup | 生产级，本文的对照物 | 默认选择 |
| 本篇手写版 | 核心协议同形，panic 转错误 | 学习与实验 |

## Quick Start

前置条件：go 1.26，另需 golang.org/x/sync（对照测试用）。运行分步演示：

```bash
go run ./19_errgroup/
```

真实输出（第 2 步耗时每次不同，量级为几十微秒；基线 goroutine 数每次
不同，泄漏差值固定为 2）：

```text
== 第 1 步：三合一心智模型 ==
Group = WaitGroup(联结) + sync.Once(首错独占) + 令牌通道(限流) + cancel(联动)。

== 第 2 步：一失败全组取消（验收线 < 10ms）==
Wait 返回首错：task 0 failed
7 个幸存者最慢收到取消耗时：59µs（每次不同，验收线 < 10ms）

== 第 3 步：SetLimit 峰值并发 = limit ==
20 个任务、limit=3：实测峰值并发 = 3（固定）

== 第 4 步：panic 转错误 ==
Wait 返回：task panicked: boom（固定）

== 第 5 步：裸 goroutine 失控对照（失败无人管、泄漏无人收）==
A 失败后错误发往无人接收的通道，B 永远等 A 的结果：
调用方既拿不到错误，也收不回 goroutine。errgroup 版两条都有解——
错误经 Wait 返回，goroutine 经 Wait 清零。
goroutine 数：基线 1 → 泄漏后 3（基线每次不同，差值固定为 2）
```

验收命令（八条测试见 errgroup_test.go，实测记录）：

```bash
go test -race -count=10 ./19_errgroup/   # ok  1.885s
```

## How It Works

Go 的三步：先取令牌（`sem <- struct{}{}`，令牌满则提交方阻塞，这就是
限流的位置）；再 `wg.Add(1)` 登记；最后派生 goroutine 跑任务。

任务收尾
走 done()：先还令牌、再 `wg.Done()`。顺序不能反——Wait 只看 wg，若令牌
还没归还 wg 就清零，Wait 返回后信号量仍处满态。

首错的传播是一条 happens-before 链（两个事件间的全序保证，后者能看到
前者的全部写入）。

任务 0 里 `failAt.Store` → 返回错误 → collect 里
`errOnce.Do` 执行 `cancel()` → context 内部 `close(done)` → 其余任务的
`<-ctx.Done()` 解除阻塞。

所以幸存者醒来时读 failAt 必然读到失败时刻。
实测这条链走完只要几十微秒，10ms 验收线留足了调度抖动余量。

峰值恰等于 limit 靠一个不变式：任务体先 `active.Add(-1)` 再由 done() 还
令牌，所以"活跃数"永远不超过"持令牌数"，而令牌只有 limit 个。测试用
屏障把前 limit 个任务堵在峰值点上：第 limit 个进入时关闭放行通道，
`peak == limit` 在任意调度下都可观测。

**与 x/sync/errgroup 的差异表**（对照 v0.23.0 源码逐行核对）：

| # | 差异点 | x/sync 做法 | 后果 |
|---|---|---|---|
| 1 | panic 处理 | 刻意不 recover（#53757 等议题）：panic 原样崩进程，保住原始栈 | 手写版 panic 转为普通错误，可重试可降级；代价是崩溃监控看不到原始栈 |
| 2 | 取消原因 | `context.WithCancelCause`，cancel(err) 携带首错 | 手写版只有 close，`ctx.Err()` 只剩 context.Canceled |
| 3 | TryGo 语义 | 满载非阻塞返回 false，令牌抢占允许 barging | 行为一致（对照测试 TestParityWithXSyncErrgroup 实证） |
| 4 | SetLimit 活跃期修改 | panic 并报出活跃数 | 行为一致，均视为编程错误 |

## Pitfalls & Q&A

**坑 1：Group 复用。** 文档写明一组不该用于多批任务：Wait 返回后
errOnce 已耗尽，第二批的错误会被静默丢弃。每批任务新建一个 Group。

**坑 2：活跃期调 SetLimit。** x/sync 与手写版都会 panic；先 Wait 清场
再改，或者一开始就设好。

**坑 3：panic 转错误抹平了崩溃栈。** 手写版把 panic 变成
`task panicked: boom` 一行字，APM（应用性能监控）与 crash 上报从此看
不到原始调用栈。生产代码沿用 x/sync 的取舍：让 panic 崩，宁可崩溃
可见，不要错误静默。

**Q：done() 为什么先还令牌再 wg.Done？** Wait 只等 wg。若先 wg.Done，
Wait 可能在最后一个令牌归还前返回，组看起来结束了，信号量却还满着，
下次 SetLimit 检查 `len(g.sem)` 会误报"仍有活跃任务"。

**Q：和 09_singleflight 的合并是一回事吗？** 不是。singleflight 把并发
相同 key 的请求合并成一次执行，省的是重复工作；errgroup 不去重，管的是
编组纪律——联结、首错、限流、取消。一个省执行，一个管生死。

**Q：Wait 返回后组内 goroutine 一定清零吗？** 一定，wg 清零即全部任务
返回。这正是结构化并发（任务的生灭被限制在一个词法范围内）的核心承诺，
也是 [20_goleak_gate](../20_goleak_gate/) 敢把 goroutine 快照当门禁的
前提。
