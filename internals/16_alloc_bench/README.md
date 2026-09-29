# 16 · 对象分配性能调优：ns/op 与 allocs/op 的实测账本

> "少分配"是 Go 性能优化的第一口诀，但差别到底有多大？本实验用三组对照实测出数字：`new(T)` vs `&T{}`、切片 append 扩容 vs 预分配、`sync.Pool` 复用 vs 每次新建。同时借一次分配在 runtime 里的"逐层下探"，解释为什么省下的每一次分配都值钱——省掉的不只是几十纳秒，而是可能在 mheap（全局页堆）/OS 层走完的整条慢路径。

## Background

性能优化最怕凭感觉。`new` 换成 `&T{}`、切片加个 `make` 预分配、热点对象丢进 `sync.Pool`——这些口诀人人会背，但每条到底值多少，没人说得清。

凭口诀优化的痛点很具体：可能白改一通（写法层面的"优化"常常是心理安慰），也可能漏掉真正值钱的改动（结构性减少分配）。优化决策需要可复核的数字，这正是基准测试（benchmark，固定工作负载下反复计时与计数的方法）要解决的问题。

本实验的 `main.go` 内建计时器（等价于 `go test -bench=. -benchmem` 的 ns/op 与 allocs/op），同目录的 `bench_test.go` 可以亲手跑官方基准复核。

实测同时回答两个问题：**哪类优化有真实收益**（预分配、Pool），**哪类只是心理安慰**（`new` vs `&T{}` 写法差异）。

## What

对象分配性能调优是一种用基准指标量化每次堆分配成本、再按"减少分配次数"方向改造代码的实践。两个核心指标：ns/op（每次操作的平均纳秒数）与 allocs/op（每次操作的堆分配次数）。

心智模型一句话：每次堆分配先由 `mallocgc`（runtime 里所有堆分配的统一入口函数）把对象大小向上对齐到 size class（固定尺寸档位），然后从 mcache（每 P 私有、无锁，P 是 Go 运行时的逻辑处理器）取货。

只有缺货才逐层下探 mcentral（按 class 全局加锁）→ mheap（8KB 页堆）→ OS 缺页，拿到货后逐层回填。三组对照的差距，本质都是"少走下探路 + 少喂 GC"。

可以把一次堆分配想象成去仓库领货：前台按货物规格查货位表（size class），工位药箱（mcache）随手取；缺货才跑部门库房（mcentral）、总部仓库（mheap），最后向厂家下单（OS 缺页）。

但和领货不同的是：绝大多数分配在第一层就结束了，且领货频次本身就是持续税——每次分配都是 GC（垃圾回收）的标记与回收负担，不只看单次耗时。

## When to Use

这套量化方法在"优化决策需要证据"的时候派上用场。三个典型场景：

- **GC 压力高的服务做降本**：GC 占比偏高时，先用 allocs/op 找出分配大户，再决定预分配还是池化
- **代码评审中的写法之争**：`new` 还是 `&T{}`、要不要预分配，跑一组对照用数字裁决，不靠权威
- **建立性能回归防线**：把关键路径的 allocs/op 纳入基准监控，分配次数回涨即刻可见

何时不用：冷路径代码、IO 密集型服务（瓶颈在网络与磁盘）不值得抠纳秒；优化前先剖析证明分配确实是热点。

三种测量方式对比：

| 方案 | 差异 | 什么时候选它 |
|:---|:---|:---|
| `go test -bench=. -benchmem` | 官方基准框架，自动控迭代数 | 正式、可入库复核的基准 |
| `MemStats.Mallocs` 前后差值自测 | 轻量，嵌在普通程序里 | 快速验证单个热点 |
| pprof 采样剖析 | 定位热点，不精确定账 | 先找"哪里"再定"多少" |

## Quick Start

前置条件：安装 Go（本实验在 go1.26.5 下实测），进入实验目录即可运行，无第三方依赖。

```bash
cd internals/16_alloc_bench
go run main.go          # 文本演示：三组对照的 ns/op 与 allocs/op
go run main.go -json    # 输出 JSON 快照（viz.py 画图用）
GO111MODULE=off go test -bench=. -benchmem -run='^$' main.go bench_test.go   # 官方基准
```

`go run main.go` 预期输出（go1.26.5 实测，**ns 每次运行都不同，allocs/op 稳定**）：

```text
对照 1: new(T) vs &T{}（语义完全相同，耗时/分配一致）
对照 2: 预分配避免扩容搬移，更省分配
对照 3: Pool 复用基本零分配
   new vs &struct{} new                 19.65 ns/op    1.00 allocs/op
   new vs &struct{} &struct{}           16.40 ns/op    1.00 allocs/op
   append vs 预分配  append 扩容          488.39 ns/op   10.00 allocs/op
   append vs 预分配  make 预分配           239.78 ns/op    2.00 allocs/op
   Pool vs fresh  Pool 复用              6.36 ns/op    0.00 allocs/op
   Pool vs fresh  每次 fresh             9.56 ns/op    1.00 allocs/op
```

官方基准（同机同轮，注意 `B/op` 与 `allocs/op` 和上表完全一致）：

```text
BenchmarkNew-18           22182136    49.97 ns/op    32 B/op   1 allocs/op
BenchmarkPrealloc-18        946934  1476   ns/op  2072 B/op   2 allocs/op
BenchmarkAppendGrow-18       399171  2775 ns/op  4112 B/op  10 allocs/op
```

两轮 ns 差异很大不是误差：`go test` 把迭代数推到千万级，累计垃圾触发 GC 并发参与，而 GC 并发会拉扯 ns/op、方向并不稳定——这恰好现场演示了"分配速率 = GC 压力"。这也是为什么跨机器、跨负载比较性能要看 allocs/op，不要背 ns。

读输出的三个锚点：`new` 与 `&struct{}` 的两行数字几乎相同；扩容 10 allocs/op 对预分配 2；Pool 是 0。三个对照分别对应下一节的三小节。

## How It Works

按"分配走哪条路 → 三个对照分别省在哪"的顺序展开。

### size class 与分配下探路径

`mallocgc` 拿到字节数后查 size class 表：本实验的 `Payload` 是 32B（`data [32]byte`），正好命中 class 32，**无 padding**（基准里 32 B/op 佐证）。

padding 即编译器插入的填充字节，机制见 [17_align](../17_align/)。

取货顺序就是 What 节心智模型里那条链：mcache 是每 P 私有的无锁缓存，绝大多数分配在这里结束（实测 `new` 恰好 1 allocs/op、Pool 0 allocs/op）。

span 缺了才找按 class 全局加锁的 mcentral，再缺找按 8KB 页管理的 mheap，最后缺页向 OS 要内存（darwin/arm64 物理页实测 16KB，`os.Getpagesize()`）。

- **> 32KB 的大对象**绕过 mcache/mcentral 直达 mheap
- **< 16B 的无指针对象**走 tiny 合并（<16B 无指针对象的微对象合并路径，见 [14_allocator](../14_allocator/)），共享 16B 块
- **没逃逸的对象**在栈上分配，根本不进这条链（见 [13_escape](../13_escape/) 与 [14_allocator](../14_allocator/)）

### 对照 1：`new(T)` 和 `&T{}` 是同一个操作

编译器把它俩编成同一段代码。实测两者的 ns/op 与 allocs/op（1.00）都相同——写哪个都行，别在写法上做性能文章。真正影响分配的是**结构**：数据是否逃逸、是否频繁扩容。

### 对照 2：预分配让扩容搬移归零

从 `nil` 开始 append 256 个 int，cap（切片当前容量）按"cap < 256 翻倍"规则走 **1→2→4→…→128→256**，共 9 次数组重分配（每次都要分配新数组 + 拷贝旧数据）。

外加 1 次 slice 头（切片的指针+长度+容量三元组）逃逸包装 = **10 allocs/op**——就是你在输出里看到的扩容那行。

字节数 4112 B/op = 8×(1+2+…+256) + 24。`make([]int, 0, 256)` 一次性拿到 2048B（命中 class 2048）加 24B 头 = **2072 B/op、2 allocs/op**，耗时省一半（488→240 ns）。

一行 `make(cap)` 换 5 倍分配次数下降，无并发风险，是最省事的优化。

### 对照 3：`sync.Pool` 把分配压到 0

`Get` 优先从本 P 的 private 槽取，再试 shared/victim（两级共享缓存）缓存，只有池空才调 `New`——实测 0.00 allocs/op，~6 ns vs 每次 `new` 的 ~10 ns。

复用 vs 再造的差是小头，大头是两个数量级之外的 GC 压力。注意 Pool 的对象跨两轮 GC 会被清掉，只能当短期缓存用（细节见 [11_pool](../11_pool/)）。

### 指标速查表

| 指标/字段 | 含义 | 为什么重要 |
|:---|:---|:---|
| `ns/op` | 每次操作耗时 | 依赖机器与负载，同机两轮都能差 2 倍以上，只做参考 |
| `allocs/op` | 每次操作堆分配次数 | 完全稳定、直接反映 GC 负担，跨平台比较靠它 |
| `B/op` | 每次操作分配字节数 | 佐证 size class：32 B/op 说明无 padding，2072 = 2048+24 |
| `MemStats.Mallocs` | 累计分配次数 | `main.go` 用前后两次 `ReadMemStats` 的差值算 allocs/op |
| `sink` | 防优化引用 | 结果若无人引用，编译器直接删掉分配，测量全失真 |

## Pitfalls & Q&A

踩坑清单，每条按"现象 → 原因 → 解法"展开：

- **`go test` 报 "no main module"**：现象是无 go.mod 的单文件目录跑不了基准；原因是 go test 默认找模块；解法是加 `GO111MODULE=off` 并显式传入文件：`go test -bench=. -benchmem -run='^$' main.go bench_test.go`
- **拿字节数当次数**：现象是算出的 allocs/op 全错；原因是误用 `TotalAlloc`（累计字节数）当计数器；解法是统计分配次数用 `MemStats.Mallocs`
- **测出 0 分配的假象**：现象是 allocs 全 0、耗时异常低；原因是结果无人引用，编译器把分配整个删掉；解法是结果赋给包级 `sink` 保引用，这是底线
- **"相同"被证成"不同"**：现象是 `new` 与 `&T{}` 的数字对不上；原因是两种写法里有一个漏了 `sink` 赋值；解法是每种写法各保一次引用
- **计时窗口被污染**：现象是首轮数字异常大；原因是初代分配与页面映射混进测量；解法是计时前先 `runtime.GC()` 清场、再读 `MemStats`

深入问答：写法差异、allocs/op 为什么比 ns/op 可信、10 次分配的对账、Pool 的坑、测量防失真——这些常见疑问已分别在 How It Works 的三个对照、指标速查表与上面的坑清单中展开，不再重复。
