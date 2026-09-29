# 10 · sync.WaitGroup：一个 64 位 state 字 + 一个 sema 的等待艺术

> `main` 精确等到一组 goroutine 全部收工的官方答案是极简的：没有锁、没有队列，只有一个 64 位原子 state 字（高 32 位 counter、低 32 位 waiters）加一个 4 字节 sema 信号量。本篇用 `atomic.Uint64.Load` 直接读出每一步操作后的真实计数，看清 Add/Done/Wait 各自碰哪个半边、归零那一瞬如何唤醒所有等待者。

## Background

等一组并发任务收工，没有现成原语时的手写方案两条路都不理想：忙轮询（循环检查计数再 sleep）空烧 CPU；用 channel 计数收尾，每个任务发一个值、接收方收满 N 个，多写少写一处就漏。

更麻烦的是这类错误的表现形态：一旦计数对不上，等待方不是报错退出，而是永久卡住——问题被静默放大。手写方案里"计数、等待、唤醒"三件事散落各处，边界全靠调用方自己守。

`sync.WaitGroup` 把这三件事封装成三个动作：`Add` 报数、`Done` 销账、`Wait` 睡到清零。理解它内部那个 64 位 state 字的位布局，就理解了哪些用法安全、哪些是未定义行为。

## What

`sync.WaitGroup` 是一个基于"单个 64 位原子状态字 + 单个信号量"实现的等待原语：高 32 位 counter 记录还有多少个 Add 增量未 Done，低 32 位 waiters 记录阻塞在 `Wait` 上的人数，sema 负责让等待者入睡与唤醒。

可以把 WaitGroup 想象成工地的任务白板：开工前把任务数写上（`Add`），每完成一项擦一格（`Done`），验收员盯着白板清零就放行（`Wait`）；但和真实白板不同的是，验收员不是轮询盯梢，而是登记后直接睡着，清零那一瞬被统一叫醒。

心智模型一句话：两个计数打包进一个 64 位字——Add/Done 只改高半边 counter，Wait 只改低半边 waiters，两边互不冲突，任何读都是一次原子 Load，锁都不需要；counter 归零那刻由 sema 逐个唤醒睡眠的等待者。

## When to Use

**在 fan-out 一批并发任务、只关心全部完成时**。启动 N 个 goroutine 前先 `Add(N)`，每个任务 `defer Done()`，主流程 `Wait`——不要结果时这是最省的写法。

**在批量等待多个独立阶段的收尾时**。counter 中途还能再涨（实测 Add(2) 后又 Add(2)），一个 WaitGroup 可以覆盖"边跑边加"的批次。

何时不用：

- 需要每个任务的结果时，用 channel 收集更直接。
- 需要超时或取消语义时，选 context 配合 channel。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `sync.WaitGroup` | 无锁无队列，只报完成 | 只等完成、不要结果 |
| channel | 能传值、能关闭广播 | 要收集结果或做流式处理 |
| context + channel | 带超时/取消传播 | 等待本身可能要中断 |

## Quick Start

前置条件：已安装 Go；观测内部字段用 `unsafe`，仅用于学习。

```bash
cd internals/10_waitgroup
go run main.go          # 文本演示：Add/Done 步进 → Wait 阻塞 → 时间线采样
go run main.go -json    # 输出 JSON 快照（steps 与完整 timeline 数组）
```

预期输出（节选）：

```text
New()        counter=0  waiters=0   (新建：两个计数都是 0)
Add(2)       counter=2  waiters=0   (要等 2 个任务)
Add(1)       counter=3  waiters=0   (再加 1 个，counter=3)
Done()       counter=2  waiters=0   (-1，还剩 2 个)
Add(2)       counter=4  waiters=0   (中途还能再涨，counter=4)
Done()×4     counter=0  waiters=0   (全部清完，counter=0)
任务进行中: counter=1 waiters=1     ← Wait 期间 waiters +1
Done() 后  : counter=0 waiters=0   ← 清零那刻 sema 唤醒
t=  3786µs  counter=3 waiters=2
t= 15070µs  counter=2 waiters=3   ← waiters 顶满不动，等归零
```

每一步"在看什么"都写在 `main.go` 的注释里；`-json` 快照给出与文本演示一致的机器可读数值。

读出真实计数的解码代码（这段在做什么：把 WaitGroup 的首 8 字节重解释为 `atomic.Uint64` 并做一次原子 Load，再按高低 32 位拆出 counter 与 waiters——这就是标准库自己的读法）：

```go
st := (*atomic.Uint64)(unsafe.Pointer(wg))
w := st.Load()
Counter = int32(w >> 32)    // 高 32 位
Waiters = int32(uint32(w))  // 低 32 位
```

## How It Works

### 内存布局：两个计数挤进一个字

`WaitGroup = { noCopy 0B, state atomic.Uint64 8B, sema uint32 4B }`。noCopy 是一个空字段，唯一作用是让 `go vet` 能检测出"WaitGroup 被拷贝"这种误用。

state 高 32 位是 counter（还有多少 Add 增量未 Done），低 32 位是 waiters（阻塞在 Wait 上的 goroutine 数）。

Quick Start 输出前七行的步进——New 0/0 → Add(2) 2/0 → Add(1) 3/0 → Done 2/0 → Add(2) 4/0（中途还能涨）→ Done×4 0/0——counter 全程住在高 32 位。

分工带来一个关键性质：Add/Done 与 Wait 碰的是同一个字的不同半边，各自用一次原子操作完成，互不加锁也不互相冲突。

### Wait：把等待者挂到低 32 位并睡在 sema 上

有人 `Wait()` 且 counter 未归零时，waiters 低 32 位 +1，然后这个 goroutine 通过 `runtime_Semacquire`（运行时内部在信号量上休眠的函数）睡在 sema 上——不烧 CPU。

输出里"任务进行中: counter=1 waiters=1"这一行，就是入睡后的快照。

### 时间线：递减到 0 的唤醒瞬间

实验让 3 个 waiter 错峰（每 2ms 一个）涌入，主 goroutine 每 8ms 调一次 Done，每 500µs 采样一次。waiters 错峰爬到 3 后一直顶着不动（没到唤醒条件大家都睡着）；counter 阶梯 3→2→1→0。

counter=0 与 waiters=3 同框的那一瞬，`runtime_Semrelease` 把 3 个睡眠者全部叫醒，waiters 瞬间归零。

输出里 t=3786µs（counter=3, waiters=2）与 t=15070µs（counter=2, waiters=3）就是这条时间线上的两个采样点——"全部完成后一起放行"的底层来源。

### 归零即质变

唤醒条件是 counter **等于 0** 而非"小于等于 0"：从 1 到 0 的那次 Done 触发对每位等待者的 `runtime_Semrelease`。这也是为什么 Done 必须与 Add 严格配对——减成负数直接 panic。

### 关键字段与操作

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `state 高 32 位` | counter：未完成任务的 Add 增量 | Add/Done 的工作区，归零即放行 |
| `state 低 32 位` | waiters：阻塞在 Wait 上的人数 | 归零唤醒后立即回 0 |
| `sema uint32` | 睡眠/唤醒信号量 | 绝对计数在 state，入睡叫醒在 sema |
| `Add(n)` / `Done()` | counter ±n（Done 即 Add(-1)） | 只碰高半边，乱序安全 |
| `Wait()` | waiters+1 并睡到归零 | 只碰低半边，可跨 goroutine 调用 |

## Pitfalls & Q&A

坑清单：

- **Add 与 Wait 并发是未定义行为**。现象：偶发 panic 或卡死。原因：官方语义要求 Add 的额度在 Wait 前确定，标准库对"Wait 之后再加"有 panic 检查。解法：在启动 goroutine 之前完成 Add。
- **计数不清零 = 永久泄漏**。现象：所有 Wait 永远睡着，goroutine 泄漏（goroutine 永久阻塞无法退出）。原因：少一次 Done。解法：把 Done 写成 `defer wg.Done()` 是肌肉记忆级防御。
- **counter 减成负数直接 panic**（negative WaitGroup counter）。原因：Done 次数多于 Add。解法：Done 与 Add 严格配对，不多调。

**Q：为什么 Done 可以在任意 goroutine 里调用？**
A：Done 就是 `Add(-1)`，只对高 32 位做一次原子减，与谁执行无关；归零的唤醒由那次 Done 顺带触发（发现 waiters>0 就逐个 semrelease）。

**Q：直接读 state 靠得住吗？**
A：readWG 只是瞬时快照，任何一次 Load 都是"某一瞬间"。跨操作的因果要靠实验的时间设计（错峰、定时 Done）保证。

**Q：为什么实验要给 waiter 加 2ms 错峰？**
A：并发涌入时第 5µs waiters 就满 3，采样拍不到 0→1→2→3 的爬升过程；加 2ms 错峰才能拍到完整阶梯。
