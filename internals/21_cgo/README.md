# 21 · CGO 内存归属：C.malloc 出去的内存，GC 管不着

> 只要 `import "C"`，一个进程里就同时住着两套内存管理：Go 的 GC（垃圾回收，自动回收不可达对象）世界和 C 的 malloc/free（手动分配与释放）世界。最常见的误会是"C 泄漏了 GC 会兜底"——不会，GC 的考勤表里根本没有 C 的内存。
>
> 本实验用 64MB 内存做 leak / free / go 三种对照，各自独立进程采样 RSS（进程常驻内存），把"GC 看不见 C 内存"变成看得见的数字。读完你将能判断内存归属、选对观测指标。

## Background

Go 程序接了 cgo（Go 调用 C 代码的桥接机制）库（图片解码、加密、数据库驱动）之后内存一直涨，`pprof` heap profile（Go 堆剖析数据）却一切正常——因为 heap profile 只统计 Go 堆，而泄漏发生在 C 堆。

要诊断这类问题，先建立三条基本认知：

1. **GC 只遍历 Go 指针**（扫描栈/寄存器/全局变量 + Go 堆内指针），`C.malloc` 的内存从不登记为 GC 对象，GC 跑三遍也看不见它
2. **进程内存 ≠ Go 堆**：RSS = Go 堆 + C 堆 + 栈 + runtime 元数据，C 泄漏只会在 RSS 上显形
3. 跨语言的 free 是纯手工活：malloc/free 是对偶操作，GC 没有免费午餐可以替 C 兜底

程序用三种模式做对照实验：`leak`（64 次 `C.malloc` 永不释放）、`free`（用完 `C.free`）、`go`（64 次 `make([]byte, 1MB)` 交给 GC）。

## What

**定义**：CGO 内存归属规则一句话——**内存归谁分配就归谁回收**。跨过 cgo 边界的只有指针值，不是内存本身：`C.malloc` 出的块登记在 C 的分配器（allocator，管理内存块簿记与出借的组件）名下，Go GC 的考勤表里没有它，唯一回收出路是 `C.free(p)`。

**心智模型**：可以把一个进程想象成两本账本——Go 堆一本、C 堆一本。GC 是只翻 Go 账本的会计：扫栈、寄存器、全局变量，沿 Go 指针清点可达对象；C 账本上的条目它连看都不看。

但和"两个独立进程"不同的是：两个世界共享同一地址空间，C 堆泄漏会推高整个进程的 RSS——账本分开，房子是同一间。

## When to Use

这套知识在三类时刻派上用场：

- 评估或引入 cgo 依赖（图片解码、加密、数据库驱动）时：为"C 堆泄漏只有 RSS 能看见"建立监控口径，不能只挂 heap profile
- 排查"RSS 持续上涨而 pprof heap 正常"时：优先怀疑 C 侧漏了配对 `C.free`，先看 RSS 与 `HeapInuse` 的差值
- 设计跨界接口时：决定参数是只读借用还是拷贝交接，规则见 How It Works 的边界规则一节

什么时候不用：

- 纯 Go 项目没有 C 堆，`runtime.MemStats` 与 heap profile 已覆盖观测需求
- 有纯 Go 替代实现且性能可接受时，优先避免 cgo——它会拉起 C 编译器与动态链接，拖慢构建、增加交叉编译负担
- 一次性分配且进程随即退出时，泄漏不会累积成事故，不必为此加监控

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 纯 Go 数据结构 + GC | 分配回收全自动，`HeapInuse` 可观测 | 无硬性 C 依赖时首选 |
| C 指针只读借用 | 零拷贝，但所有权留在 C，必须配对 `C.free` | 大块数据仅在调用期间读 |
| `C.CBytes` / `C.GoBytes` 拷贝交接 | 拷贝一次，所有权随之切换 | 跨界后需长期持有 |

## Quick Start

前置条件：装有 Go 与本机 C 编译器（cgo 构建需要）；重画数据图另需 Python 3 与 matplotlib。

```bash
cd internals/21_cgo
go run main.go                     # 三种模式各起独立子进程，汇总对比
go run main.go -mode leak -json    # 单模式 JSON 快照（rssBefore/rssAfter/goHeapMax）
python3 viz.py                     # 重新实测三模式 RSS 数据
```

预期输出（节选，RSS 每次运行有 ±1MB 左右波动，"新分配数"也随之变化）：

```text
 模式   分配后RSS   GC后RSS   进程堆峰值   新分配数
 leak           73        74            0        82
 free           10        10            0        86
 go             11        11           64       219

单位 MB。RSS = 进程常驻内存（mach task_info）；三模式各自独立进程跑。

1. leak：分配后 RSS 73 MB，GC 三遍后仍 74 MB —— C 内存 GC 看不见
2. free：同样 64MB，正确 C.free 后 RSS 10 MB ≈ 基线 —— 泄漏消失
3. go：GC 管的堆峰值 64 MB，回收后回落；RSS 平稳 — 分配活由 GC 干
```

`goHeapMax` 打印 0 表示堆峰值不足 1MB（取整），与 RSS 73MB 对照就是"进程内存 ≠ Go 堆"的铁证。

## How It Works

### 两个世界，两本账

Go 世界里，`make([]byte, 1MB) × 64` 落在 Go 堆，`HeapInuse`（runtime 统计的在用堆字节数）可见，GC 标记可达对象后回收；C 世界里，`C.malloc(1MB) × 64` 落在 C 堆，GC 的扫描器从不踏入。

Go 调用方跨过 cgo 边界拿到的是 `unsafe.Pointer`，指着 C 堆上的 64MB——指针跨了线，内存没跨。Quick Start 输出里 leak 行"分配后 RSS 73 / 进程堆峰值 0"，就是这条规则的直接结果。

### GC 为什么看不见 C 内存

Go GC 的根扫描范围是栈、寄存器、全局变量，对象图只沿 Go 指针走。`C.malloc` 的块既不在 Go 堆的 span（Go 堆的内存管理单元）里，也不携带 GC 位图（记录对象存活标记的位图），所以"可达性分析"对它完全失明。

leak 模式的实测就是这个结论：GC 三遍后 RSS 73→74MB 纹丝不动，而同期 `HeapInuse` 始终不足 1MB——与 Quick Start 输出的 leak 行互相印证。

### 三种模式对照实测

| 模式 | 分配后 RSS | GC 三遍后 RSS | Go 堆峰值 | 说明 |
|:---|:---:|:---:|:---:|:---|
| leak | 73 MB | **74 MB** | <1MB | C 内存 GC 摸不到，三遍 GC 纹丝不动 |
| free | ≈10 MB | ≈10 MB | <1MB | 用完 `C.free`，RSS 回到基线 ≈10MB |
| go | ≈11 MB | ≈11 MB | **64 MB** | 堆涨又落全部交给 GC，RSS 平稳 |

每次运行 RSS 有 ±1MB 左右波动（如 free 模式分配后实测出现过 9MB 与 10MB 两种取整结果），但三模式的相对关系每次都成立；`python3 viz.py` 每次重新实测这组数据。

### 边界规则：只读借用 vs 拷贝交接

- **C 指针借给 Go 用是安全的**：本文 `peek(p, 0)` 读第一个字节即是最小演示；Go 侧长期保管 C 指针也不违反 cgo 规则（GC 本来就不管它），真正的问题是忘了 `C.free` 就是永久泄漏
- **Go 指针不能让 C 长期保存**：调用期间可以传，调用返回后 C 不得继续持有；违规可被 `GODEBUG=cgocheck=2`（cgocheck2）揪出来；`-race`/`checkptr` 查的是别的
- **要跨界长期持有就用拷贝交接**：`C.CBytes`（Go→C）/`C.GoBytes`（C→Go）拷贝一次，所有权随之切换，没有后续纠缠

### 观测字段速查

输出快照与采样涉及的四个关键字段：

| 字段/函数 | 含义 | 为什么重要 |
|---|---|---|
| `sample.RSSBefore/After` | mach `resident_size`，分配后 / GC 三遍后 | C 内存是否归还，只有 RSS 能看见 |
| `sample.GoHeapMax` | 过程中 `HeapInuse` 峰值（MB 取整） | GC 管辖范围的峰值；leak/free 恒 <1MB |
| `sample.Allocs` | `ms.Mallocs` 差值 | 只数 Go 对象分配，C 的 malloc 不计入 |
| `C.cur_rss()` | `mach_task_basic_info.resident_size` | 当前常驻内存的真实快照 |

## Pitfalls & Q&A

### 踩坑清单

- **`ru_maxrss` 是历史峰值，只增不减**：用它看"free 后内存降没降"，怎么测都是"没降"
  - 原因：它是峰值计数器，不反映当前值
  - 解法：macOS 用 `mach_task_basic_info.resident_size` 抓当前值（即本实验的 `C.cur_rss`），Linux 可读 `/proc/self/statm`；采样时机放在 GC 前后各一次
- **free 掉的内存通常不还给 OS**：同进程里"先泄漏 64MB 再 free"，RSS 不回落、对比不出效果
  - 原因：`free()` 一般只把内存还给 C 的 allocator 而不还 OS
  - 解法：三模式拆成三个独立进程（`-mode X -json`），RSS 才是可横向对比的诚实数字
- **`import "C"` 是隐形的**：构建变慢、交叉编译变麻烦，却找不到显式开关
  - 原因：包里任一文件 import "C" 就自动拉起 cgo 构建 + C 编译器 + 动态链接，即使 C 前置块为空
  - 解法：引入依赖前确认是否真需要 cgo，纯 Go 替代优先
- **C 泄漏的排查入口是 RSS 不是 heap profile**：RSS 直线上涨而 heap 纹丝不动
  - 原因：`runtime.MemStats` 全是 Go 堆的数，C 泄漏在它上面不可见
  - 解法：盯 RSS 与 `HeapInuse` 的差值是否持续扩大

### 深入问答

**Q：Go 进程 RSS 一直涨，pprof heap 却显示正常，为什么？**
A：heap profile 只统计 Go 堆，C 堆不在其视野内——排查入口见上面最后一条坑。

**Q：C.malloc 的内存，Go GC 会回收吗？**
A：不会，机制见上文"GC 为什么看不见 C 内存"；唯一出路是配对的 `C.free`，没有兜底。
