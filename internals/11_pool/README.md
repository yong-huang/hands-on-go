# 11 · sync.Pool：per-P 分片、victim 缓存与 GC 的两拍清理

> `sync.Pool` 是 Go 提供的"对象复用器"，但它不是普通对象池：生命周期和 GC 深度绑定。为什么它快？为什么 GC 后对象不是立刻消失，而是"慢一拍"才被清理？为什么拿它当持久缓存一定会翻车？本实验用三组可复现的实测把这些问题拍在台面上（`11_pool/main.go`，Go 1.26.5）。

## 1. 为什么需要它

高频路径上反复 `new` 大对象，会让 GC 次数和堆分配量随请求量线性上涨。直觉解法是自己写个 `map` 当对象池，但那要自己处理锁竞争和内存回收。`sync.Pool` 把这两个问题都替你想好了：**性能靠 per-P 无锁分片，清理靠 GC 两拍淘汰**。理解这两层机制，才能解释它"有时快得飞起、有时对象莫名丢失"的行为。

## 2. 总览：核心机制一图看懂

![sync.Pool 结构：per-P 分片 + victim 缓存 + New 兜底](images/pool.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/11_pool/images/pool.html)
> （或本地打开 [`images/pool.html`](images/pool.html)）。

**心智模型一句话**：Pool = 一撮 per-P 无锁分片（`local[]`，每 P 一份 `poolLocal`：private 槽 + shared 链）+ victim 缓存 + `New()` 兜底；Get 沿着"本 P private → 偷他人 shared → victim → New()"逐级下探，越靠前越快。

## 3. 快速开始

```bash
cd internals/11_pool
go run main.go          # 文本演示：同轮复用 → GC 两拍 → GC 压力对比
go run main.go -json    # 输出 JSON 快照
```

预期输出（节选，Go 1.26.5 实测）：

```text
========== 1. 同一轮：Put/Get 复用同一个对象 ==========
   Get→Put→Get 拿到同一指针: true（New 只被调了 2 次）
   连续 3 轮 Get/Put 拿到的都是同一对象 = true

========== 2. GC 的两拍：victim 缓存延迟清理 ==========
   GC×1 后 Get → 捞出旧对象（blob.a=100, 是 seed=true）
   GC×2 后 Get → 触发 New（blob.a=7, 是 seed=false）

========== 3. 复用 vs 新建：GC 次数与总分配量 ==========
   Pool 复用 3000000 轮：GC 增量 0 次
   裸 new  3000000 轮：GC 增量 19 次，期间分配 72.1 MB
```

exp3 的分配量（72.1 MB / GC 19 次）随机器与调度会小幅漂移，量级稳定。

## 4. 核心概念

### 4.1 三层结构与 Get/Put 路径

- **Get**：先摸本 P 的 `private`（无锁）；没有就去别的 P 的 `shared` 尾部"偷"；再没有就翻 victim 缓存；还空才调 `New()`。
- **Put**：本 P 的 `private` 空就放进去，否则推入本 P 的 `shared` 链头。
- 同一轮 GC 周期内、同一个 P 上，Get/Put 就是两次内存读写——exp1 实测连续 3 轮 `Get→Put` 拿到的都是同一对象。exp1 里 `New` 被调用 2 次的原因：前一次 `Get` 把对象取走后没有还回，下一次 `Get` 只能触发 `New`。

### 4.2 GC 的两拍清理：victim 缓存延迟一拍

![池对象的两拍 GC 生命周期](images/gc_lifecycle.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/11_pool/images/gc_lifecycle.html)
> （或本地打开 [`images/gc_lifecycle.html`](images/gc_lifecycle.html)）。

标准库在每次 GC 开始时（`poolCleanup`，世界已停）做两件事：先丢掉上一轮留下的 victim，再把当前 local 整体平移成 victim。所以一个池对象的完整生命周期是：Put 进 local → **第一次 GC 后还能从 victim 捞回** → **第二次 GC 才彻底消失**。exp2 单 P 实测序列：GC×1 后 Get 捞回 seed（`是 seed=true`），GC×2 后 Get 触发 New（`是 seed=false`）。

### 4.3 复用收益：GC 次数归零

exp3 同样 300 万次操作：Pool 复用全程 **0 次 GC**；裸 `new` 用 `interface{}` 装箱强制逃逸到堆后触发 **19 次 GC**、累计分配 **72.1 MB**（72045624 B）垃圾。复用把 GC 的工作量几乎清零——这就是"大对象循环复用"在吞吐上的意义。

## 5. 关键字段与坑清单

| 字段/概念 | 含义 | 为什么重要 |
|---|---|---|
| `local[] poolLocal` | 每 P 一份分片：private 槽 + shared 无锁链 | 热路径零锁、零 CAS，Pool 快的全部秘密 |
| `private` | 本 P 独占的槽位 | Get 第一优先级，Put 优先落点 |
| `shared` | 本 P 的无锁链（其他 P 可偷尾部） | 跨 P 平衡负载，仍是有限竞争 |
| `victim` | 上一轮 local 的整体备份 | GC 后第一拍还能捞回，两拍才清空 |
| `New func() interface{}` | 池空兜底工厂 | 对象所有权是"尽力而为"，必须设 |

坑清单：

- **victim 按 P 分块，多 P 下结果是概率性的**：GC 后 goroutine 可能漂到别的 P，第一轮也捞不回。exp2 锁定 `runtime.GOMAXPROCS(1)` 才能稳定复现"第一拍捞回 seed"
- **`runtime.NumGC()` 不存在**：GC 计数在 `runtime.MemStats.NumGC` 字段里，要 `ReadMemStats` 前后各读一次取差值
- **编译器会把裸分配优化没了**：`b := &blob{...}` 不外泄会被栈上化，300 万轮只分配 28 KB。必须用 `interface{}` 装箱（`sink = b`）强制逃逸，才能制造真实垃圾
- **别拿它当持久缓存**：对象最多跨一个 GC 周期，跨两轮一定丢；连接池、缓存请用带锁的真实池

## 6. 文件结构

```text
internals/11_pool/
├── README.md                          # 本文件：原理 + 运行指引 + 面试要点
├── main.go                            # 主教材：同轮复用 → GC 两拍 → GC 压力对比（-json 输出快照）
└── images/
    ├── pool.architecture.json         # 图源：Pool 的 per-P 分片 + victim + New 结构
    ├── pool.html                      # 交互版：可缩放/高亮连线的自包含页面
    ├── pool.svg                       # README 内嵌双主题矢量图
    ├── gc_lifecycle.lifecycle.json    # 图源：池对象两拍 GC 生命周期状态机
    ├── gc_lifecycle.html              # 交互版
    └── gc_lifecycle.svg               # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：sync.Pool 为什么快？**
A：性能靠 per-P 分片——每个 P 一份 `poolLocal`（private 槽 + shared 链），同 P 的 Get/Put 无锁；只有本地空了才去偷别的 P 的 shared 或翻 victim，代价逐级上升。

**Q：GC 之后 Pool 里的对象还在吗？**
A：两拍清理。每次 GC 开始时 `poolCleanup` 先丢旧 victim、再把 local 平移成 victim：第一次 GC 后对象还在 victim 里，Get 仍能捞回；第二次 GC 才真正清空（exp2 实测：GC×1 捞回 seed，GC×2 触发 New）。

**Q：sync.Pool 能当连接池/缓存用吗？**
A：不能。对象生命周期与 GC 绑定，最多存活一个 GC 周期；且 Get 不保证归还顺序、可能随时被清。要持久语义请用带锁的真实池或 `chan` 池。

**Q：为什么 exp1 里 New 被调用了 2 次？**
A：`Get` 取走对象后如果没 `Put` 回去，下一次 `Get` 就只能走 `New`。Pool 的所有权是"尽力而为"——这也是 `New` 兜底必须存在的原因。

**Q：怎么量化 Pool 的收益？**
A：同一工作量对比 `MemStats.NumGC` 增量与 `TotalAlloc`：exp3 里 300 万轮复用 GC 增量 0 次，裸 new（强制逃逸）19 次、72.1 MB 分配。

## 8. 总结

sync.Pool 的全部行为都能从"per-P 无锁分片 + 两拍 GC 清理"推出来：快是分片带来的，"对象丢失"是两拍淘汰的必然代价。记住验证方法——`go run main.go` 亲手跑一遍两拍序列，比背结论可靠得多。

下一篇 [12_once](../12_once/) 看最小的同步原语 `sync.Once`：一个 1 字节的 atomic.Bool 如何同时做到"只执行一次"和"所有调用者都等 f 跑完"。
