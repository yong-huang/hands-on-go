# 08 · sync.Mutex 底层：一个 int32 装下 locked、woken、starving 与 waiters

> `sync.Mutex` 的底层只有 `{ state int32, sema uint32 }` 共 8 字节，布局十余年未变，可以放心用 `unsafe` 读出真实的 state 字并逐位解码。本篇把锁内部的"位运算状态机"变成可验证的数据：state=25 怎么读出"持锁 + 3 人排队"、自旋窗口为什么只有几微秒、饥饿模式如何在 1ms 阈值后接管调度。

## Background

日常写 Go 时，互斥锁的标准用法是把临界区（一段同时只允许一个 goroutine 进入的代码）用 `Lock()`/`Unlock()` 包起来，锁内部完全是个黑盒。这种理解在低竞争下够用，问题被藏住了。

一旦竞争上来了，黑盒就开始让人无从下手：为什么高竞争下性能骤降？为什么有的等待者一直拿不到锁、有的新来者却能插队？这些问题的答案不在 API 层，而在锁的内部状态里。

`sync.Mutex` 的全部行为其实由一个 int32 状态字驱动：bit0 locked、bit1 woken、bit2 starving、bits3+ 是 waiters 计数，读写全靠位运算，一次 CAS 就能原子流转。

布局十余年未变且无指针，这让它可以用影子结构安全地观测——读懂这个字，自旋、饥饿、公平性都成了可推导的结论，而不是背下来的口诀。

## What

`sync.Mutex` 的底层实现是两个机器字：`state int32`（把"锁被谁拿着、有没有人被唤醒、是否转入饥饿、多少人在排队"四个信息压进 32 个 bit 的状态字）和 `sema uint32`（信号量——用于挂起与唤醒等待者的同步原语，等待者 park 在上面，被唤醒后再回来竞争 bit0）。

可以把 state 想象成电话亭门口的一块登记板：bit0 是门闩、bit1 是"已按铃"、bit2 是"改为排队叫号"、高 29 位是排队人数；但和真实登记板不同的是，任何涂改都必须一次 CAS（compare-and-swap，比较旧值符合预期才写入新值的原子指令）原子完成，不存在改了一半的中间状态。

心智模型一句话：state 是一个位运算状态机，`sema` 是配套的停车场——抢不到锁的 goroutine 挂进去睡觉，被唤醒后回到 state 上继续竞争。

## When to Use

**在诊断高竞争下的性能骤降时**。读 waiters 计数能观测排队深度，确认瓶颈是不是锁竞争，而不是靠猜。

**在排查"某个 goroutine 长期拿不到锁"或延迟毛刺时**。知道饥饿模式在等待超 1ms 后接管、改为 FIFO 交接，才能把现象对上机制。

**在选择同步方案的粒度时**。理解自旋只在极短临界区有收益，会影响临界区拆多细、锁加在哪的决策。

何时不用：

- 低竞争、共享数据简单时，直接用 `Lock()`/`Unlock()` 即可，无需关心内部状态。
- 生产代码不要依赖内部布局——`unsafe` 观测只适合学习与诊断工具。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `sync.Mutex` | 一个 state 字实现互斥，通用 | 默认的临界区保护 |
| `sync.RWMutex` | 读锁共享、写锁独占 | 读远多于写、读临界区不太短时 |
| atomic 操作 | 单个变量的无锁 CAS/读写 | 只保护一个机器字时，见 [09_atomic](../09_atomic/) |
| channel | 传递数据所有权而非保护数据 | 生产者-消费者、需要关闭/超时语义时 |

## Quick Start

前置条件：已安装 Go；`python3 viz.py` 需 Python 3 与 matplotlib（可选，仅影响画图）。`unsafe` 读内部字段的写法仅用于观测，不影响程序其他部分。

```bash
cd internals/08_mutex
go run main.go          # 文本演示：state 位解码 → 自旋窗口 → 饥饿模式
go run main.go -json    # 输出 JSON 状态快照（spin/starvation 采样）
python3 viz.py          # 读 -json 快照绘制数据图
```

预期输出（节选，采样数值每次运行略有波动）：

```text
新建未锁: state=0 → locked=false woken=false starving=false waiters=0
加锁后  : state=1 → locked=true woken=false starving=false waiters=0
持锁时有 3 人等待: state=25 → locked=true woken=false starving=false waiters=3
            waiters=3（bits3+ 计数），waiters = state >> 3
持锁后 t≈ 2µs  waiters=2
持锁后 t≈ 4µs  waiters=8    ← 几微秒内爬满
t=   6093µs  locked=true  starving=true  waiters=6  ← 首次进入饥饿模式
t=  10125µs  locked=true  starving=false waiters=6  ← 被服务后复位
```

每一步"在看什么"都写在 `main.go` 的注释里；`-json` 快照给出与文本演示一致的机器可读数值。

## How It Works

### state 字的位布局与原子读取

解码规则：`locked = state&1`、`woken = state&2`、`starving = state&4`、`waiters = state>>3`（高 29 位计数）。一个字同时编码四种信息，任何状态流转只需一次 CAS。

实测三连：新建未锁 state=0；`Lock()` 后 state=1；持锁且 3 人排队时 state=25（= 1 | 3<<3）。Quick Start 输出的前三行就是这三连——你在输出里看到的 state=25，就来自这里的位编码。

读取手段是影子结构：`muShadow{state int32; sema uint32}` 与 `sync.Mutex` 前 8 字节逐字节对应，读 state 必须用 `atomic.LoadInt32` 避免数据竞争。

### 自旋：几微秒的短窗口

抢锁失败时，竞争者先做少量自旋（空转轮询 CAS，不挂起），失败立刻 park 入队。实验在 `GOMAXPROCS(2)` 下让 8 个竞争者抢锁，每 2µs 采样一次 waiters。

输出里"持锁后 t≈2µs waiters=2 → t≈4µs waiters=8"就是采样结果：几微秒内全部爬满入队。Go 宁可快速入队也不长空转浪费 CPU；自旋的价值仅在于临界区极短时（<1µs）免掉一次上下文切换。

### 饥饿模式：1ms 阈值触发 FIFO 直通

保持 2 个 P（processor，Go 调度器中执行 goroutine 的资源，数量即 GOMAXPROCS）、8 个 worker、每轮临界区 60µs，让竞争永不停止，采样 30ms。

当某等待者累计等待超过 1ms，starving 位置位，解锁直接把锁交接给排队最久的等待者（FIFO——先来先服务，不再允许插队自旋）；它被服务后立即复位。输出里 t=6093µs 的 starving=true 与 t=10125µs 的复位对应这个循环——实测首次饥饿出现在约 6ms，之后随竞争反复进出。

### 关键字段与表达式

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `state & 1`（bit0 locked） | 锁是否被持有 | Lock 即 CAS 置 bit0，Unlock 清 bit0 |
| `state & 2`（bit1 woken） | 是否已唤醒等待者 | 协调"唤醒 vs 新来者插队"的竞争 |
| `state & 4`（bit2 starving） | 是否饥饿模式 | 置位后 unlock FIFO 直通，禁插队 |
| `state >> 3`（bits3+ waiters） | 排队 goroutine 计数（29 位） | 实测 3 人排队 → state=25 |
| `sema uint32` | 信号量 | 等待者 park/唤醒的挂靠点 |

## Pitfalls & Q&A

坑清单：

- **影子结构必须一字不差**。现象：解码结果全错。原因：`muShadow{state int32; sema uint32}` 与 Mutex 前 8 字节布局错一位，读到的就不是 state。解法：严格按源码顺序声明字段。
- **读 state 用了普通解引用**。现象：`-race`（Go 内置的数据竞争检测器）直接报警。原因：普通读与写并发进行构成数据竞争。解法：一律 `atomic.LoadInt32`。
- **饥饿实验调高了 `GOMAXPROCS`**。现象：starving 位根本不置位。原因：P 多了竞争不够激烈，等待者累计不满 1ms。解法：调低到 2，结束记得 defer 恢复。
- **采样数值每次运行有波动**（首次饥饿 6ms 上下、waiters 爬升 4~8 不等）。原因：调度与时序天然抖动。解法：确定性的是位布局与状态机逻辑，不是绝对数值。

**Q：Mutex 的自旋发生在什么时候？为什么只自旋几次？**
A：锁被持有且满足条件（多核、P 在跑、本地队列不忙等）时先自旋轮询 CAS。窗口只有几微秒——实测 8 个竞争者约 4µs 内全部入队；长自旋浪费 CPU，收益只在临界区极短时免一次上下文切换。

**Q：正常模式和饥饿模式各牺牲什么、得到什么？**
A：正常模式允许新到达的 goroutine 在自旋后直接抢锁（吞吐高，但队头可能一直抢不到）；饥饿模式牺牲吞吐换公平，保证最老等待者直达。

**Q：为什么能直接读 Mutex 内部字段？生产代码该这么做吗？**
A：`{state int32, sema uint32}` 布局十余年未变且无指针，影子结构 + `atomic.LoadInt32` 可安全读取，适合学习与观测；生产代码请依赖 `sync` 公开 API，不要依赖内部布局。
