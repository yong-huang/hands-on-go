# 19 · Task Scheduler：小顶堆驱动的定时任务调度

> 任务调度器是后台系统的核心组件：cron job（按计划执行的任务）、延迟队列、定时重试都依赖它。本实验用约 160 行代码实现一个支持**延时、优先级、取消、优雅停止**的线程安全调度器，核心是一个"看堆顶睡觉"的调度循环——如何按时间排序、同刻任务如何按优先级决胜、如何取消已调度任务而不阻塞、Stop 时正在执行的任务怎么办，四个问题都能在运行输出里观察到答案。

## Background

定时任务最直觉的两种做法各有天花板：`time.Sleep` 是"一个 goroutine 睡一段时间"，任务一多 goroutine 就泛滥；`time.AfterFunc` 给每个任务挂一个 timer，百万任务时 timer 对象与 runtime 内部的 timer 堆都是负担。

更好的模型是**少量 goroutine + 一张按到期时间排序的优先队列**：谁最早到期谁在堆顶，调度循环只看堆顶、睡到那个时刻、批量执行。把 goroutine 数量从"每任务一个"压到"每调度器一个"。

这个模型要答对四个子问题，正是本实验逐一演示的：

- 堆的排序键是什么？（`executeAt` 早者优先，同刻 `priority` 小者优先）
- 取消怎么做到不动堆？（懒删除：只打标记，堆顶弹出时跳过）
- 睡眠期间如何兼顾"到期"与"Stop"？（`select` + `time.After` + `done` channel）
- 停止语义是什么？（`close(done)` 唤醒、`wg.Wait()` 等正在执行的 `fn` 跑完）

## What

**任务调度器**是一种把"延时执行一次任务"集中管理的组件：任务带延时与优先级提交，到期的按序执行，并支持随时取消单个任务与整体优雅停止；整个结构线程安全，可被多个 goroutine 并发使用。

心智模型一句话：调度器是一个"看堆顶睡觉的 goroutine"——`Schedule` 把任务压进小顶堆（min-heap，堆顶永远是键值最小元素的树结构），循环取堆顶睡到它的 `executeAt`，醒来后批量弹出到期任务执行。

`Cancel` 只是打标记（懒删除），`Stop` 用 `close(done)` 把任何睡眠中的等待立刻唤醒。

可以把调度器想象成一位只盯最近一班闹钟的值班员：任务单按最早触发时间摞成一摞（小顶堆），值班员只看最上面一张，睡到那一刻，把到点的单子批量处理掉；临时作废的单子不做现场处理，轮到它浮到最上面时直接扔掉。

但和值班员不同的是：多个提交方可以同时往摞里塞单子（并发），翻动这摞单子必须持锁；而且值班员下班（Stop）不会把手头未到期的单子处理完——它们被静默丢弃。

注意本实现是**单调度 goroutine 顺序执行 `fn`**，没有 worker 池、没有失败重试——这是它与生产级调度器（如 `robfig/cron`）的关键差距。

## When to Use

这节回答"哪些定时任务需求适合这个小调度器，哪些该换方案"。

典型场景：

- **延迟任务**：订单 30 分钟未支付自动关闭这类"提交后延时执行一次"的需求。
- **延时重试**：失败任务按退避延时重新提交自身，复用同一套堆机制。
- **同刻任务分优先级**：同一时刻到期的多个任务按 `priority` 决胜，重要的先执行。

何时不用：

- 周期性 cron 任务：本实现只有一次性延时语义，没有周期调度，直接用 `robfig/cron` 这类库。
- 任务可能卡死或执行耗时长的场景：单调度 goroutine 顺序执行，一个任务卡住会拖住整个调度器。
- 海量短延时任务：插入/弹出的 O(log n) 与 timer 分配成为负担时，时间轮更合适。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 每任务一个 `time.AfterFunc` | 各自独占 goroutine 触发，海量任务时 timer 对象与 runtime 堆成负担 | 任务量很小、实现成本优先 |
| 小顶堆 + 单调度 goroutine（本篇） | 插入/弹出 O(log n)，触发与执行集中，支持优先级与取消 | 延时任务量中等、需要取消与优先级 |
| 时间轮（timing wheel） | 任务放刻度桶，插入与到期触发 O(1)，精度受刻度限制 | 海量短延时任务（Kafka、Netty 的做法） |
| `robfig/cron` 等生产库 | 周期调度、worker 池、失败处理完备 | 直接要生产语义时 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/19_task_scheduler
go run task_scheduler.go
```

真实输出（时间戳每次运行会有毫秒级抖动，执行顺序确定）：

```text
=== Task Scheduler Demo ===

Scheduling 6 tasks with varying delays and priorities...
  Scheduled tasks: 1(pri=1,300ms), 2(pri=0,300ms), 3(pri=5,100ms), 4(pri=2,200ms), 5(pri=3,400ms), 6(pri=0,200ms)

  Cancelling task 4...
  Cancel task 4: true
  [exec] Task 3: priority=5, delay=100ms (default priority, short delay)
  Cancel task 3 (likely already executed): false

  [exec] Task 6: priority=0, delay=200ms (high priority, short delay)
  [exec] Task 2: priority=0, delay=300ms (high priority, medium delay)
  [exec] Task 1: priority=1, delay=300ms (low priority, medium delay)
  [exec] Task 5: priority=3, delay=400ms (medium priority, long delay)
  Cancel task 1 (already executed): false
  Cancel task 2 (already executed): false
  Cancel task 6 (already executed): false

Scheduler stopped.

Expected execution order: 3 (100ms) -> 6 (200ms, pri=0) -> 2 (300ms, pri=0) -> 1 (300ms, pri=1) -> 5 (400ms)
Task 4 was cancelled and should not appear.
```

三个观察点：

- T3 最早到期先跑，所以 120ms 后 `Cancel(3)` 返回 `false`（已执行即取消失败）；
- T6/T2/T1 按到期时间再按优先级严格有序；
- 被取消的 T4 从未出现。

## How It Works

按一条任务的生命周期走：提交入堆 → 堆顶触发 → 到期批量执行；取消与停止是横切在这条主路径上的两个操作。

### 优先队列：container/heap 五方法

`taskHeap` 实现 `heap.Interface`。排序规则全在 `Less` 里：

```go
func (h taskHeap) Less(i, j int) bool {
    if h[i].executeAt.Equal(h[j].executeAt) {
        return h[i].priority < h[j].priority // 数值小 = 优先级高
    }
    return h[i].executeAt.Before(h[j].executeAt)
}
```

`heap.Push`/`heap.Pop` 都是 O(log n)；`Swap` 里同步维护 `item.index` 是 `container/heap` 的约定（本实现未用 index 做按位删除，但保留它为扩展 `heap.Remove` 留了口子）。

你在输出里看到的 `2 (300ms, pri=0) -> 1 (300ms, pri=1)`，就是 `Less` 在同刻任务上按优先级决胜的结果。

### 调度循环：看堆顶睡觉

后台 goroutine（`Start` 里启动）每轮做三件事：

```text
peekNext() 取堆顶 → 没到期就 select{ done, time.After(waitDuration) } → 到期后批量 Pop + 执行
```

- 堆空时以 50ms 间隔轮询（同样可被 `done` 打断），避免忙等
- `time.After` 每次分配一个临时 timer；高吞吐场景可换成单个 `time.Timer` + `Reset` 减少分配
- 醒来后**一次锁内循环弹出所有已到期任务**（弹出时解锁执行 `fn`、再重新加锁），避免同刻任务被迫等下一轮睡眠

输出末尾的 `Expected execution order: ...` 一行，就是这条循环对 6 个任务的真实排序结果。

### 懒删除：Cancel 只打标记

从堆里按值删除需要 O(n) 定位 + 重整堆；本实现 `Cancel(id)` 只线性扫描并置 `cancelled = true`。被标记的任务留在堆里，等它浮到堆顶时由 `peekNext()` 顺手 `heap.Pop` 丢弃——取消成本 O(n)，删除成本摊到堆顶弹出。

已执行的任务查不到（不在堆里），所以 `Cancel` 返回 `false`，这本身就是"任务是否已跑"的一次查询。

输出印证：`Cancel task 4: true` 是标记成功；`Cancel task 3 (likely already executed): false` 是"任务已不在堆里"的判定结果。

### 并发安全与优雅停止

`sync.Mutex` 保护堆的全部读写。

`done chan struct{}` + `sync.WaitGroup` 组成停止协议：`Stop()` 先置 `running=false`、`close(done)`（让所有 `select` 的 `<-s.done` 立即命中），再 `wg.Wait()` 等 goroutine 退出。

由于 goroutine 在解锁后才执行 `t.fn()`，`Stop` 返回时正在执行的任务函数必然已经完成——但**Stop 不会等待"已调度未到期"的任务**，它们被静默丢弃，这是必须向调用方讲清的语义。

### 字段速查

| 字段/组件 | 含义 | 为什么重要 |
|---|---|---|
| `executeAt` | 任务到期时刻 | 堆的第一排序键，`Schedule` = `now + delay` |
| `priority` | 同刻决胜键，小者先 | 只在 `executeAt` 相等时参与 `Less` |
| `cancelled` | 懒删除标记 | 不立即出堆，堆顶弹出时跳过 |
| `done chan struct{}` | 停止信号 | `close` 唤醒一切 `select` 等待，包括睡眠中 |
| `wg sync.WaitGroup` | 跟踪调度 goroutine | 保证 `Stop` 语义：返回时 goroutine 已退出 |
| `running bool` | 防重复 Start/Stop | `Stop` 后未 `Start` 不可复活（done 已关闭） |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **睡在锁外**：现象是提交阻塞、新到更早任务收不到。原因：等 `executeAt` 时持锁。解法：先 `Unlock` 再 `select` 等待，醒来重新看堆顶。
- **执行在锁外**：现象是 `fn` 里调用 `Schedule`/`Cancel` 不死锁，但与提交方并发跑。原因：`t.fn()` 在解锁后调用。解法：明确这是并发执行语义，`fn` 内部的共享状态自行加锁。
- **`time.After` 泄漏直觉**：现象是担心每次循环新建 timer 泄漏。原因：这里 timer 都会被 select 消费或自然到期，并不泄漏。解法：高吞吐场景复用单个 `time.Timer` + `Reset` 减少分配。
- **Cancel 是 O(n) 扫描**：现象是任务量极大时取消变慢。原因：线性扫描找 id。解法：加 `map[int]*scheduledTask` 索引，配合 `heap.Remove` 做真删除。
- **堆空轮询 50ms**：现象是改了睡眠策略后 `Stop` 永久挂起。原因：Stop 依赖这个轮询窗口或睡眠点响应 `done`，删掉 `case <-s.done` 后睡眠等不到头。解法：任何等待路径都保留 `case <-s.done` 分支。

**Q：为什么用堆而不是每个任务一个 `time.AfterFunc`？和时间轮比呢？**
A：三种方案的成本与适用场景：

- `time.AfterFunc`：每任务一个 runtime timer，海量任务时 timer 对象与 runtime 内部最小堆都成为负担，且各自独占 goroutine 触发；适合任务量很小、实现成本优先的场景。
- 小顶堆方案：只有一个调度 goroutine，插入/弹出 O(log n)；任务量小、精度高时选它。
- 时间轮（timing wheel）：把任务放进刻度桶，插入与到期触发都是 O(1)，但精度受刻度限制、跨轮任务需要层级时间轮；适合任务量巨大、延时范围集中的海量短延时任务（Kafka、Netty 的做法）。

**Q：`Stop()` 之后，还没到期的任务去哪了？正在执行的任务呢？**
A：未到期任务被静默丢弃——`Stop` 只保证调度 goroutine 退出（`wg.Wait`）和正在执行的 `fn` 跑完，不 drain 堆。若要"清空并回调"，`Stop` 里需要遍历剩余堆逐个通知。

另外本实现 `Stop` 后不能 `Start` 复活：`done` 已关闭，goroutine 会立刻返回。

**Q：懒删除和真删除怎么取舍？**
A：懒删除 `Cancel` O(n)（找 id）+ 出堆时顺手丢，实现最简单，适合取消低频场景。

真删除需 `map[id]*task` 索引 + `heap.Remove(h, index)`（依赖 `Swap` 维护的 `index` 字段），`Cancel` 变 O(log n)，代价是每次 `Push/Swap` 都要同步 map。

堆里残留的 cancelled 任务还会白占内存，长期运行必须配合堆顶懒清理。

**Q：手写这个调度器还有哪处最容易错？**
A：坑清单之外的一处：只执行堆顶一个任务就回去睡——同刻任务被推迟一轮。正确做法是醒来后循环弹出所有 `executeAt <= now` 的任务（见上文调度循环一节）。

**Q：如何扩展出失败重试和 worker 池？**
A：当前 `fn func()` 无返回值、单 goroutine 顺序执行，任务卡住会拖住整个调度器。

生产化方向：`fn` 改 `func() error` 并 `recover` panic；失败后按指数退避重新 `ScheduleAt` 自身（复用同一套堆机制）；执行侧引入固定 worker 池（任务 chan + N worker）把"触发"与"执行"解耦，代价是失去执行顺序保证。
