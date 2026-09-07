# 12 · sync.Once：done 原子位快路径 + Mutex 慢路径

> `sync.Once` 是标准库里最小而精的同步原语：一个 1 字节的 `atomic.Bool` 加一个 `Mutex`。为什么并发 100 个调用 f 只跑一次？为什么抢不到锁的人要"傻等"f 跑完，而不是各自判断"反正马上就好了"？为什么 `done.Store(true)` 必须放在 f 返回之后？本实验用 unsafe 读出 done 字段、用毫秒级延时对比把语义拍在台面上（`12_once/main.go`，Go 1.26.5 实测）。

## 1. 为什么需要它

初始化一次的代码到处都是：单例、配置加载、连接建立。手写双重检查锁几乎必错——要么忘了一次二次检查，要么把 `Store` 放错位置，要么用 CAS 写出"只保证跑一次、不保证别人等它跑完"的残缺版本。`sync.Once` 用两个小小的字段把这一切做对了，理解它等于理解 Go 并发最讲究的形态：**双重检查 + release 语义**。

## 2. 总览：核心机制一图看懂

![sync.Once：done 原子位快路径 + Mutex 慢路径](images/once.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/12_once/images/once.html)
> （或本地打开 [`images/once.html`](images/once.html)）。

**心智模型一句话**：`Do` 每次都先做一次原子 `done.Load()`（快路径，无锁无等待）；只有读到 0 才进慢路径抢 mutex，抢到的人二次检查后执行 f，**f 返回之后**才 `done.Store(true)`——置位顺序就是语义本身。

## 3. 快速开始

```bash
cd internals/12_once
go run main.go          # 文本演示：100 并发 → 快慢路径 → 内存屏障
go run main.go -json    # 输出 JSON 快照
```

预期输出（节选，延时数值随调度漂移）：

```text
========== 1. 100 个 goroutine 并发 Do：f 只执行一次 ==========
   并发 100 个 Do 之后：f 实际执行次数 = 1

========== 2. 快慢路径：done 在 f 返回后才置位 ==========
   [    0ms] f 开始（done 此刻 = false）
   [   31ms] f 结束，此刻才 done.Store(true)
   done 最终值 = true
   exp2 实测：4 个慢路径调用者平均等待 28.9 ms（f 睡 30ms）
   之后的快路径调用只花 0.00 ms

========== 3. 内存屏障：done 置位 = f 副作用已发布 ==========
   16 个 goroutine 在 Do 返回后读 result：看到 42 的有 16/16
```

## 4. 核心概念

### 4.1 结构：done 刻意放在最前面

```go
type Once struct {
    _    noCopy      // 零宽占位，禁止拷贝
    done atomic.Bool // 首个有效字段，1 字节，热路径只读它
    m    Mutex       // 慢路径串行化
}
```

`done` 放最前是性能工程：快路径 `if !o.done.Load()` 会被内联到每个调用点，首字段意味着更紧凑的指令。实验里 `readDone` 直接把 `*Once` 按 `*atomic.Bool` 解读——正因为 done 位于偏移 0（noCopy 是零宽字段），且只读保持原子性。

### 4.2 并发首次调用的时间线

![并发首次调用：1 个执行 f，其余持锁等待，之后直达快路径](images/once_timeline.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/12_once/images/once_timeline.html)
> （或本地打开 [`images/once_timeline.html`](images/once_timeline.html)）。

慢路径 `doSlow` 三步：`m.Lock()` → 再查一次 `done`（双重检查，锁外的赢家可能已经跑完）→ `f()` 返回后才 `Store(true)`。实测 f 睡 30ms 时，4 个慢路径调用者平均等 28.9 ms——**等待的终点是"f 已结束"，而不是"done 已置位"**，这两个时刻差一次 `Store`。之后每次 Do 只花 0.00 ms。

### 4.3 内存屏障：看到 done=1 就看到 f 的全部副作用

`done.Store(true)` 是 release-store：它之前的所有写操作（f 里写的 `result=42`）对后续 acquire-load 的读者全部可见。exp3 实测：16 个 goroutine 在 Do 返回后读 result，16/16 看到 42。反过来，f 内部读 done 必然是 false——置位还没发生，这正是"release 在 f 之后"的活证据。

## 5. 关键字段与坑清单

| 字段/语句 | 含义 | 为什么重要 |
|---|---|---|
| `done atomic.Bool` | "f 已跑完"标志 | 快路径判据；1 字节就有屏障效果 |
| `m Mutex` | 慢路径互斥锁 | 保证"所有 Do 返回时 f 已结束" |
| `if !o.done.Load()` | 快路径 | 内联在每个调用点，零锁零等待 |
| `f(); o.done.Store(true)` | 顺序不可换 | 换了就丢失 release 语义 + 完成语义 |

坑清单：

- **CAS 是错误实现**：`if o.done.CompareAndSwap(false, true) { f() }` 只保证"执行一次"，第二个并发调用者会在 f 没跑完时直接返回——mutex 不可省
- **读 done 别用反射**：直接 `(*atomic.Bool)(unsafe.Pointer(o)).Load()` 最贴近标准库且原子（依赖 done 在偏移 0，仅限学习用途）
- **并发测量要看观测窗口**：慢路径等待 ~29ms 的前提是"f 已在跑"；实验用 `time.Sleep(2ms)` 先让 f 进临界区，否则并发者抢在 f 前面会虚低
- **`Once` 不要按值拷贝**：结构体含 `noCopy` 标记，`go vet` 会报拷贝告警；传参一律用 `*sync.Once`

## 6. 文件结构

```text
internals/12_once/
├── README.md                         # 本文件：原理 + 运行指引 + 面试要点
├── main.go                           # 主教材：100 并发 → 快慢路径 → 内存屏障（-json 输出快照）
└── images/
    ├── once.architecture.json        # 图源：Once 字段与快/慢路径结构
    ├── once.html                     # 交互版：可缩放/高亮连线的自包含页面
    ├── once.svg                      # README 内嵌双主题矢量图
    ├── once_timeline.sequence.json   # 图源：并发首次调用时间线
    ├── once_timeline.html            # 交互版
    └── once_timeline.svg             # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：sync.Once 为什么快？每次调用都要加锁吗？**
A：不加。f 结束后所有调用走快路径——一次原子 `done.Load()` 直接返回，无锁无等待（实测 0.00 ms）；只有首次初始化才碰 mutex。

**Q：为什么不用 `CAS(false,true)` 决定谁执行 f？**
A：CAS 只保证恰好一个人执行，不保证其他调用者等它执行完。第二个调用者会立刻返回，拿到的是"未初始化完成"的状态。Once 的语义是"所有 Do 返回时 f 已结束"，必须靠 mutex 排队。

**Q：done.Store(true) 放在 f() 前后会怎样？**
A：两个层面都坏：语义上"Do 返回时 f 未结束"；内存模型上丢失 release——其他 goroutine 看到 done=1 时，f 写入的变量可能还没对它们可见（16/16 看到副作用的保证消失）。

**Q：双重检查（抢到锁后再查 done）可以省吗？**
A：不能。抢到锁之前可能有别的 goroutine 已经在锁内跑完 f 并置位；不二次检查会跑两次 f。

**Q：100 个 goroutine 同时 Do，时间线上发生了什么？**
A：99 个在 mutex 上排队等 1 个执行 f；exp2 单独实测过慢路径等待（4 个调用者平均 28.9 ms，f 睡 30ms）；f 返回、done 置位后，队里的人二次检查直接返回；之后再来的走快路径零等待。

## 8. 总结

sync.Once 把"只执行一次"和"所有调用者都看到执行完成"两件事拆给两个字段：done 的原子读做快路径判据，mutex 做慢路径串行化；`f 返回之后才 Store` 一个顺序同时给出了完成语义和 release 语义。验证方法照旧——`go run main.go` 亲手看 done 的置位时刻。

下一篇 [13_escape](../13_escape/) 回到内存主题：编译器的逃逸分析到底把变量放栈还是放堆，为什么 `-gcflags=-m` 的结论有时和运行时实测"打架"。
