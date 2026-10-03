# 11 · work-stealing 实证：runnext、本地队列与全局队列的分工

> 本篇用三个可重复的实验把 Go 调度器的三层队列行为拆开验证：runnext 插队、本地队列 256 上限溢出、全局队列与偷任务（work-stealing）。每个现象都有 GOMAXPROCS 三档对比与 GODEBUG=schedtrace 的实测输出对照。读完本篇，你能预测"谁先执行"并解释为什么。

## Background

如果所有 goroutine 排一条全局队列，每次取任务都要抢同一把锁：核数越多，锁争用越凶，扩展性越差。Go 的解法是给每个 P（processor，运行时逻辑处理器）配一条无锁本地队列，取放都不加锁。

本地队列解决了锁，制造了新问题：负载不均时，一个 P 排长队、另一个 P 闲着。让闲的 P 去忙的 P 手里匀任务，就是 work-stealing（工作窃取，闲 P 从忙 P 的本地队列里拿走一半任务）。

三片拼图由此凑齐：插队位 runnext 保"刚创建的 G 先跑"，本地队列保快路径，全局队列与偷任务保均衡。本篇逐片实证。

## What

P 侧的任务队列分三层，各有一句话职责：

| 部件 | 容量/结构 | 职责 |
|---|---|---|
| runnext | 每个 P 一格 | 插队位：`go` 出的新 G 直接进这里，挤下一位进本地队列尾 |
| 本地队列 | 每 P 一条，256 容量环形 | 快路径：无锁 FIFO，满则触发溢出移送 |
| 全局队列 | 带锁链表 | 兜底：接溢出，也接抢占下来的 G |

配套两条规则：调度器每 61 次调度会先看一眼全局队列（防饿死）；P 空闲时从最忙邻居偷一半。

可以把三层队列想象成食堂打饭：手里端着的一盘是 runnext（必须先吃完），自己窗口的队伍是本地队列，中央排号机是全局队列。但和食堂不同的是，空闲窗口的师傅会直接从隔壁队伍"匀走一半人"，且中央排号机每 61 号就被强制叫一次。

## When to Use

- **预测与解释执行顺序**：写并发演示或基准时，能预判哪段代码先跑，避免把顺序巧合当结论。
- **解释队列相关延迟**：本地堆 256、全局堆积、偷任务失败，都能对应到 schedtrace 里的具体字段。
- **何时不用**：业务代码几乎不需要干预这套机制；调 GOMAXPROCS 或改任务粒度前想清楚即可。

| 观察工具 | 差异 | 什么时候选它 |
|---|---|---|
| 完成顺序实验 | 零依赖，直接可复现 | 教学与回归验证 |
| GODEBUG=schedtrace | 周期快照队列长度 | 看负载分布与分工 |
| runtime/trace | 全事件流 | 需要逐 G 归因时，见 [10_g_trace](../10_g_trace/) |

## Quick Start

需要 go 1.26，无第三方依赖。三个实验一套跑齐：

```bash
go run ./11_work_stealing/
```

真实输出（实验 1 的 2/8 档顺序与实验 3 的跳变位置每次运行不同，结构不变）：

```text
== 实验 1：12 个短任务在 GOMAXPROCS 三档下的完成顺序 ==
  GOMAXPROCS=1  完成顺序=[11 0 1 2 3 4 5 6 7 8 9 10]
  GOMAXPROCS=2  完成顺序=[11 5 0 6 7 8 9 10 2 1 3 4]
  GOMAXPROCS=8  完成顺序=[4 3 11 5 6 7 8 9 10 1 0 2]

== 实验 2：runnext 插队（GOMAXPROCS=1，A 先 spawn、B 后 spawn）==
  10 次实验，B（后 spawn）先完成 9 次；序列=[AB BA BA BA BA BA BA BA BA BA]

== 实验 3：本地队列 256 上限，600 个任务溢出到全局队列 ==
  完成 600 个任务；顺序跳变点位置=[1 29 30 90 91 131 151 152 212 213 217 273 274 334 335 343 344 347 395 396 456 457 472 473 477 599]
  前 8 个完成=[599 386 387 388 389 390 391 392]，最后 4 个完成=[382 383 384 514]
```

schedtrace 采集（任务粒度要够粗，100ms 打点才追得上队列变化）：

```bash
GODEBUG=schedtrace=100 go run ./11_work_stealing/ -mode=queue
```

真实输出节选（每 100ms 一行，快照时机的数字每次不同）：

```text
SCHED 107ms: gomaxprocs=1 idleprocs=0 threads=5 spinningthreads=0 needspinning=1 idlethreads=3 runqueue=386 [ 107 ] schedticks=[ 108 ]
SCHED 212ms: gomaxprocs=1 idleprocs=0 threads=5 spinningthreads=0 needspinning=1 idlethreads=3 runqueue=384 [ 4 ] schedticks=[ 213 ]
SCHED 317ms: gomaxprocs=1 idleprocs=0 threads=5 spinningthreads=0 needspinning=1 idlethreads=3 runqueue=254 [ 29 ] schedticks=[ 318 ]
SCHED 422ms: gomaxprocs=1 idleprocs=0 threads=5 spinningthreads=0 needspinning=1 idlethreads=3 runqueue=125 [ 53 ] schedticks=[ 423 ]
SCHED 527ms: gomaxprocs=1 idleprocs=0 threads=5 spinningthreads=0 needspinning=1 idlethreads=3 runqueue=0 [ 73 ] schedticks=[ 528 ]
```

## How It Works

### 三档顺序对照预测

| 档位 | 预测 | 实测 | 结论 |
|---|---|---|---|
| 1 | 最后 spawn 的 11 先跑，其余 0~10 升序 | 完全一致 | runnext 插队 + 本地 FIFO |
| 2 | 11 先，随后乱序并混入被偷的连续段 | `[11 5 0 ...]`，5 第 2 位 | 邻居 P 偷走一段 |
| 8 | spawn 期间就被偷，开局即乱 | `[4 3 11 ...]` | 空闲 P 连 runnext 都能抢 |

8 档里 4、3 比 11 还早：偷任务方会带一小段延迟后抢受害者的 runnext 槽位，
所以"最后创建的最先执行"在多 P 下不成立。

### 溢出的实测划分

600 个任务 spawn 完、排空开始前，三个队列的存货从输出可精确读出：全局 386 + 本地 213
+ runnext 1 = 恰好 600。"386"来自完成序列：599 之后从 386 起连续升序，说明本地队列
当时正是从 386 开始排空。

本地队列每次装满 256，就把新来的 G 连同本地队列里最老的一半一起移送全局队列，
来回倒手后剩 386/213 的划分。

schedtrace 印证分工：`runqueue` 是全局队列长度，方括号 `[ n ]` 是各 P 本地队列。
五条快照里全局从 386 一路降到 0，本地却从 107 回升到 73——本地见底时运行时会
把全局队列整批搬回本地（384→254→125 的三次跳降就是三批搬运）。

### runnext 插队与它的失效条件

`go` 语句把新 G 放进当前 P 的 runnext，原占位者被挤进本地队列尾；P 每次调度
都先取 runnext。

安静运行下 B 稳定插队，但实测 10 次里仍出现 1 次 AB：spawner 阻塞前若有任何新 G
就绪（GC 协程、计时器唤醒都会 ready 一个 G 并占据 runnext），B 就被挤回本地
队列。加 `-race` 后 GC 频繁，插队成功率掉到一半左右——机制没变，是窗口被占。

## Pitfalls & Q&A

**用 -race 验证执行顺序，结论漂移**。TSan（竞态检测器）带来额外分配与 GC，
GC 唤醒的 G 会抢走 runnext 槽位。顺序类断言要在安静运行下做，-race 只用于
数据竞争检测，两个用途分开跑。

**schedtrace 采样对不上实验**。任务太细时 100ms 打点之间队列已清空，看不到
非零快照。本篇实验 3 给每个任务 1ms 自旋，把排空过程拉长到秒级，快照才有内容。

**把"升序完成"当默认预期**。本地队列是 FIFO，但 runnext 插队、偷半数、全局
批次搬运都会破坏升序。上面的三档对照表就是反例集，顺序必须实测。

**Q：为什么偷一半而不是偷一个？** 偷一个马上又会闲，通信成本摊不薄；偷一半
让负载梯度一次性减半，收敛更快。代价是被偷方等待时间翻倍，所以只偷不催。

**Q：溢出为什么移送一半而不是全部？** 全部移送会让本地队列瞬间清空，偷任务方
失去目标、全局锁压力集中。留一半保住快路径，送一半解掉溢出，是对冲后的折中。

**Q：G 被抢占后去哪了？** 进全局队列而不是回本地——这是全局队列的另一条进路，
与溢出无关。抢占的演进过程见 [12_preemption](../12_preemption/)。
