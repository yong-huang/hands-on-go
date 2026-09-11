# 19 · Task Scheduler：小顶堆驱动的定时任务调度

> 任务调度器是后台系统的核心组件：cron job、延迟队列、定时重试都依赖它。面试常追问：如何按时间排序？同刻任务如何按优先级决胜？如何取消已调度任务而不阻塞？Stop 时正在执行的任务怎么办？本实验用约 160 行代码实现一个支持**延时、优先级、取消、优雅停止**的线程安全调度器，并把整条「提交 → 小顶堆 → 调度循环 → 到期执行」链路画成一张图。

## 1. 为什么需要它

`time.Sleep` 只能"一个 goroutine 睡一段时间"，任务一多 goroutine 就泛滥；`time.AfterFunc` 每个任务一个 timer，百万任务时内存与 runtime timer 堆都是负担。更好的模型是**少量 goroutine + 一张按到期时间排序的优先队列**：谁最早到期谁在堆顶，调度循环只看堆顶、睡到那个时刻、批量执行。

这个模型要答对四个子问题，正是本实验逐一演示的：

- 堆的排序键是什么？（`executeAt` 早者优先，同刻 `priority` 小者优先）
- 取消怎么做到 O(1)？（懒删除：只打标记，堆顶弹出时跳过）
- 睡眠期间如何兼顾"到期"与"Stop"？（`select` + `time.After` + `done` channel）
- 停止语义是什么？（`close(done)` 唤醒、`wg.Wait()` 等正在执行的 `fn` 跑完）

## 2. 总览：核心机制一图看懂

![Task Scheduler 全链路：提交入堆、堆顶触发、到期批量执行、Stop 唤醒](images/scheduler.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/19_task_scheduler/images/scheduler.html)
> （或本地打开 [`images/scheduler.html`](images/scheduler.html)）。

**心智模型一句话**：调度器是一个"看堆顶睡觉的 goroutine"——`Schedule` 把任务压进小顶堆，循环取堆顶睡到它的 `executeAt`，醒来后批量弹出到期任务执行；`Cancel` 只是打标记（懒删除），`Stop` 用 `close(done)` 把任何睡眠中的等待立刻唤醒。注意本实现是**单调度 goroutine 顺序执行 `fn`**，没有 worker 池、没有失败重试——这是它与生产级调度器（如 `robfig/cron`）的关键差距。

## 3. 快速开始

```bash
cd interview/19_task_scheduler
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

三个观察点：T3 最早到期先跑，所以 120ms 后 `Cancel(3)` 返回 `false`（已执行即取消失败）；T6/T2/T1 按到期时间再按优先级严格有序；被取消的 T4 从未出现。

## 4. 核心概念

### 4.1 优先队列：container/heap 五方法

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

### 4.2 调度循环：看堆顶睡觉

后台 goroutine（`Start` 里启动）每轮做三件事：

```text
peekNext() 取堆顶 → 没到期就 select{ done, time.After(waitDuration) } → 到期后批量 Pop + 执行
```

- 堆空时以 50ms 间隔轮询（同样可被 `done` 打断），避免忙等
- `time.After` 每次分配一个临时 timer；高吞吐场景可换成单个 `time.Timer` + `Reset` 减少分配
- 醒来后**一次锁内循环弹出所有已到期任务**（弹出时解锁执行 `fn`、再重新加锁），避免同刻任务被迫等下一轮睡眠

### 4.3 懒删除：Cancel 只打标记

从堆里按值删除需要 O(n) 定位 + 重整堆；本实现 `Cancel(id)` 只线性扫描并置 `cancelled = true`。被标记的任务留在堆里，等它浮到堆顶时由 `peekNext()` 顺手 `heap.Pop` 丢弃——取消成本 O(n)，删除成本摊到堆顶弹出。已执行的任务查不到（不在堆里），所以 `Cancel` 返回 `false`，这本身就是"任务是否已跑"的一次查询。

### 4.4 并发安全与优雅停止

`sync.Mutex` 保护堆的全部读写；`done chan struct{}` + `sync.WaitGroup` 组成停止协议：`Stop()` 先置 `running=false`、`close(done)`（让所有 `select` 的 `<-s.done` 立即命中），再 `wg.Wait()` 等 goroutine 退出。由于 goroutine 在解锁后才执行 `t.fn()`，`Stop` 返回时正在执行的任务函数必然已经完成——但**Stop 不会等待"已调度未到期"的任务**，它们被静默丢弃，这是必须向调用方讲清的语义。

## 5. 关键字段与坑清单

| 字段/组件 | 含义 | 为什么重要 |
|---|---|---|
| `executeAt` | 任务到期时刻 | 堆的第一排序键，`Schedule` = `now + delay` |
| `priority` | 同刻决胜键，小者先 | 只在 `executeAt` 相等时参与 `Less` |
| `cancelled` | 懒删除标记 | 不立即出堆，堆顶弹出时跳过 |
| `done chan struct{}` | 停止信号 | `close` 唤醒一切 `select` 等待，包括睡眠中 |
| `wg sync.WaitGroup` | 跟踪调度 goroutine | 保证 `Stop` 语义：返回时 goroutine 已退出 |
| `running bool` | 防重复 Start/Stop | `Stop` 后未 `Start` 不可复活（done 已关闭） |

坑清单：

- **睡在锁外**：等 `executeAt` 时必须先 `Unlock`，否则 `Schedule` 无法入堆，也收不到新堆顶更早的情况
- **执行在锁外**：`t.fn()` 在解锁后调用——如果 `fn` 里调用 `Schedule`/`Cancel`，不会死锁；但同一时刻任务与提交方并发跑
- **`time.After` 泄漏直觉**：这里 timer 都会被 select 消费或自然到期，不泄漏；但如果每次循环创建大量超长 timer，考虑复用 `time.Timer`
- **Cancel 是 O(n) 扫描**：任务量极大时加 `map[int]*scheduledTask` 索引，配合 `heap.Remove` 做真删除
- **堆空轮询 50ms**：Stop 依赖这个轮询窗口或睡眠点响应 `done`；改睡眠策略时别把 `case <-s.done` 删掉，否则 Stop 会永久挂起

## 6. 文件结构

```text
interview/19_task_scheduler/
├── README.md                        # 本文件：原理 + 运行指引 + 深入要点
├── task_scheduler.go                # 主教材：heap 接口 → Scheduler → Start/Stop → demo
└── images/
    ├── scheduler.architecture.json  # 图源：提交→堆→调度循环→到期执行（Typed JSON IR）
    ├── scheduler.html               # 交互版：可缩放/高亮连线的自包含页面
    └── scheduler.svg                # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么用堆而不是每个任务一个 `time.AfterFunc`？和时间轮比呢？**
A：`time.AfterFunc` 是每任务一个 runtime timer，海量任务时 timer 对象与 runtime 内部最小堆都成为负担，且各自独占 goroutine 触发。小顶堆方案只有一个调度 goroutine，插入/弹出 O(log n)。时间轮（timing wheel）把任务放进刻度桶，插入与到期触发都是 O(1)，适合海量短延时任务（Kafka、Netty 的做法）；但精度受刻度限制，跨轮任务需要层级时间轮。任务量小、精度高选堆；任务量巨大、延时范围集中选时间轮。

**Q：`Stop()` 之后，还没到期的任务去哪了？正在执行的任务呢？**
A：未到期任务被静默丢弃——`Stop` 只保证调度 goroutine 退出（`wg.Wait`）和正在执行的 `fn` 跑完，不 drain 堆。若要"清空并回调"，`Stop` 里需要遍历剩余堆逐个通知。另外本实现 `Stop` 后不能 `Start` 复活：`done` 已关闭，goroutine 会立刻返回。

**Q：懒删除和真删除怎么取舍？**
A：懒删除 `Cancel` O(n)（找 id）+ 出堆时顺手丢，实现最简单，适合取消低频场景；真删除需 `map[id]*task` 索引 + `heap.Remove(h, index)`（依赖 `Swap` 维护的 `index` 字段），`Cancel` 变 O(log n)，代价是每次 `Push/Swap` 都要同步 map。堆里残留的 cancelled 任务还会白占内存，长期运行必须配合堆顶懒清理。

**Q：手写这个调度器最容易错的三处？**
A：① 睡觉时持锁——导致提交阻塞且收不到新堆顶；② 只执行堆顶一个任务就回去睡——同刻任务被推迟一轮，正确做法是醒来后循环弹出所有 `executeAt <= now` 的任务；③ 停止协议不完整——只用 bool 标志不用 channel，睡眠中的 `time.After` 等不到头，`Stop` 就得等最长的那个延时。

**Q：如何扩展出失败重试和 worker 池？**
A：当前 `fn func()` 无返回值、单 goroutine 顺序执行，任务卡住会拖住整个调度器。生产化方向：`fn` 改 `func() error` 并 `recover` panic；失败后按指数退避重新 `ScheduleAt` 自身（复用同一套堆机制）；执行侧引入固定 worker 池（任务 chan + N worker）把"触发"与"执行"解耦，代价是失去执行顺序保证。

## 8. 总结

一个小顶堆加一个看堆顶睡觉的 goroutine，就构成了定时任务调度的全部骨架：`Less` 定序（时间优先、优先级决胜）、懒删除化解取消难题、`close(done)` 打断一切睡眠实现毫秒级停止。验证方式是把 6 个不同延时/优先级/取消组合的任务跑一遍，对着输出的执行顺序逐条核对——比背结论可靠得多。

下一篇 [20_concurrent_lru](../20_concurrent_lru/) 看并发容器怎么做分片：把一把大锁拆成每 shard 一把，如何让 LRU 在多核下也保持 O(1)。
