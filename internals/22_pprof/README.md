# 22 · pprof 性能分析：热点在哪，优化就从哪下手

> 优化不靠感觉，靠数据。凭直觉"优化"常常花一下午改了个不热的地方，性能纹丝不动。本实验用 `runtime/pprof` 落盘 CPU/heap profile（性能剖析数据文件），再用 `go tool pprof -top/-list` 把热点钉到具体的函数和代码行。
>
> 最后完成一次完整闭环：找到热点 → 只改它 → 复测对比——1MB 校验和从逐字节改按字折叠，实测加速 8.1 倍。读完你将掌握"测量 → 定位 → 只改热点 → 复测"的完整流程。

## Background

程序慢了怎么办？新手靠猜：换 map、加缓存、上 goroutine……改一圈 profile 一拉，90% 的 CPU 还烧在原来那个函数里。性能优化的第一原则是先测量：CPU profile 告诉你时间烧在哪，heap profile 告诉你内存被谁占着。

pprof 就是 Go 官方自带的测量工具链——标准库直接 `import`，零第三方依赖。它把"哪里慢"从猜测变成读数：采样、落盘、按函数和行聚合耗时。

程序演示四件事：造两个热点函数（逐字节 vs 按字折叠的校验和）并用 `runtime/pprof` 落盘 profile；造 30 万个对象（一半立刻变垃圾）拍 heap 快照；起 `net/http/pprof` 在线端点自检；最后用 `go tool pprof` 完成定位与优化复测。

## What

**定义**：pprof 是 Go 官方的性能剖析（profiling，按固定频率采样程序状态并聚合统计）工具链——CPU profile 用采样统计时间分布，heap profile 抓堆内存驻留快照，`go tool pprof` 负责聚合展示。它回答两个问题：时间烧在哪、内存被谁占着。

**心智模型**：性能优化是闭环不是灵感。流程固定五步：埋点采集（本地落盘或线上端点）→ `-top` 看 flat（函数自身耗时的直接占比，下文详述）谁最大 → `-list` 钉到行 → 只改这一个函数 → 再 profile 复测对比。

可以把这套闭环想象成体检：先抽血化验（采集），看哪项指标异常（`-top`），进一步检查定位到具体部位（`-list`），治疗后复查（复测）。但和体检报告不同的是：pprof 的每次"复查"都要拿出前后两份 profile 的数字对比，而不是凭感觉说"好多了"。

## When to Use

这套流程在三类时刻派上用场：

- 服务 CPU 或内存指标异常、需要落到具体函数时：先拉 CPU/heap profile 看 flat 分布
- 做优化改造前：先 profile 确认改动对象是 flat 最大者，避免改完不热的地方白费功夫
- 生产环境排查疑案时：`net/http/pprof` 端点在线采 30 秒快照，不用改任何业务代码

什么时候不用：

- 瓶颈已经明确（如显而易见的算法复杂度问题）时，直接改直接测，无需完整流程
- 优化已锁定并复测完成后，反复采样没有增量信息
- flat 占比很低的函数不值得动：优化收益上限就是它的占比

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `go test -bench` | 基准测试，测已知函数的相对性能 | 验证单个函数改动前后的效果 |
| `runtime/pprof` 本地落盘 | 程序自己写 profile 文件，完全可复现 | 本地实验与回归对比（本实验方式） |
| `net/http/pprof` 端点 | HTTP 在线采集，零业务代码侵入 | 生产与长跑服务的线上采样 |

## Quick Start

前置条件：仓库根目录下装有 Go 即可，无其他依赖。

```bash
cd internals/22_pprof
go run main.go          # 文本演示：CPU 热点 → 内存热点 → 端点自检
go run main.go -json    # 输出 JSON 快照（供程序化读取）
```

预期输出（节选，ns/op 与对象数随机器/负载波动，每次运行不同）：

```text
== CPU 热点（1MB 校验和 ×300 轮，ns/op） ====
  hotCPU(逐字节)   : 768208
  hotCPUfast(按字) : 94695
  加速比：8.1x

== 内存热点（30 万对象，一半保活） ====
  heap profile 写入时：213015 对象 / 12 MB（含待回收垃圾）
  GC 后真正存活   ：150498 对象 / 10 MB

== net/http/pprof =====
  端点 :6160/debug/pprof/ 自检 → true
```

`go run main.go` 会在当前目录写 `cpu.pprof` / `heap.pprof` 两个 profile 文件，属分析产物，看完即删，不入库。用 `go tool pprof` 分析：

```bash
go tool pprof -top cpu.pprof                        # 谁烧的 CPU 最多？
go tool pprof -list hotCPU cpu.pprof                # 热到哪一行？
go tool pprof -top -inuse_space heap.pprof          # 谁占着内存？
```

`-top` 真实输出（节选，采样百分比每次运行不同）：

```text
      flat  flat%   sum%        cum   cum%
     210ms 87.50% 87.50%      220ms 91.67%  main.hotCPU      ← 热点
      20ms  8.33% 95.83%       20ms  8.33%  main.hotCPUfast
      10ms  4.17%   100%       10ms  4.17%  runtime.asyncPreempt
```

`-list` 直接把热点钉到行：`main.go:42` 的 `h = h*31 + uint64(buf[i])` 独占 210ms——优化对象一目了然。

## How It Works

### CPU profile 是"采样"，不是计时

`pprof.StartCPUProfile` 按默认 100Hz 频率中断程序、记录当时栈顶在哪个函数——谁在栈顶的时间多，谁的 flat 就大。本次运行采样 407ms、总样本 240ms，87.5% 的样本落在 `hotCPU`：CPU 时间几乎全烧在这。

你在 `-top` 输出里看到的 87.50%，就来自这批样本的归属统计。采样式 profile 的好处是开销低（默认挡位 <1%），可以一直开着。

### flat 与 cum：出手 vs 流程

- **flat**：函数自己（不含调用子孙）烧掉的 CPU/内存。flat 87.5% 在 `hotCPU` = 必须动它本身
- **cum**：含它调用的全部子孙。`main.bench`/`main.main` flat 为 0、cum 100%——它们只是流程壳

优化对象只选 **flat 最大**的那一个。`-list` 再把 flat 归属到具体行，`main.go:42` 一行独占 210ms，热点从"函数级"细化到"行级"。

### heap profile 是"快门"

`pprof.Lookup("heap").WriteTo` 抓"此刻谁占着堆内存"的快照：`-inuse_space` 看驻留（泄漏/占用），`-alloc_space` 看分配吞吐（GC 压力）。

本次实测驻留的 91.65%（5632kB）来自 `hotMemAlloc (inline)`——一半保活对象 + 尚未回收的垃圾；GC 后存活降到 150498 对象 / 10MB。

Quick Start 输出里"写入时 213015 → GC 后 150498"两行，对应"先拍含垃圾的现场，再 GC 对比存活"这个顺序；标注里的 inline 是编译器内联的结果，机制见 [23_inline](../23_inline/)。

### 在线端点与优化闭环

`_ "net/http/pprof"` 把处理器注册到 `DefaultServeMux`（net/http 的默认路由表），绑定 `127.0.0.1:6160` 自检 GET 200。

生产环境 `curl http://127.0.0.1:6160/debug/pprof/profile?seconds=30` 拿 30 秒线上 CPU 快照，不需要改任何业务代码。

闭环走一遍：`-top` 显示 flat 87.5% 在 `hotCPU` → `-list` 钉到逐字节循环 → 只改这一个函数（按字折叠，循环次数降到 1/8）。

复测：768208 → 94695 ns/op，**8.1x**，`hotCPUfast` 在 profile 里只剩 8.33%。

### 关键字段速查

| 命令/字段 | 含义 | 为什么重要 |
|---|---|---|
| `flat` / `flat%` | 函数自身的耗时/内存 | 决定"优化谁"，只挑最大的下手 |
| `cum` / `cum%` | 含调用子孙的累计值 | 定位"流程壳"，flat≈0 而 cum 大的别直接优化 |
| `-inuse_space` | 此刻驻留内存 | 查泄漏、查占用 |
| `-alloc_space` | 累计分配量 | 查 GC 压力、分配热点 |
| `?seconds=30` | 线上采集时长 | 太短样本不够，太长错失现场 |
| `-nodecount=N` | 限制 top 输出行数 | 解析输出时先跳过 `Showing nodes…` 行 |

## Pitfalls & Q&A

### 踩坑清单

- **heap profile 要赶在 GC 前拍**：GC 后拍的快照里看不到"待回收垃圾"
  - 原因：GC 已把垃圾对象回收，现场消失
  - 解法：先落盘含垃圾的现场，再 `runtime.GC()`，再对比存活数
- **`runtime.KeepAlive(head)` 放错位置等于没放**：对象仍被提前回收
  - 原因：它保证的是"执行到那一刻必活"；放在 GC 前，GC 时变量活期已过、保活失效
  - 解法：放在 GC **之后**，作为 `head` 的最后一次使用
- **解析 `-top` 输出拿错列**：脚本解析结果混乱
  - 原因：`-nodecount` 行之后才是数据行，函数名永远在行尾
  - 解法：先跳过 `Showing nodes…` 行，`main.`/`runtime.` 前缀剥掉再配标签
- **`net/http/pprof` 别手写 handler、别暴露公网**：端点泄露内部结构
  - 原因：空白导入已自动注册到 `DefaultServeMux`，手写多余；公网可达的 profile 泄露程序内部结构
  - 解法：端口绑定 `127.0.0.1`，只在内网/本机开放
- **自计时别和 profile 抢 CPU**：计时时长不稳定
  - 原因：`StartCPUProfile` 本身有开销，同一进程内 profile 不是零成本
  - 解法：先各跑 300 轮到稳态再计时

### 深入问答

**Q：flat 和 cum 有什么区别？优化该看哪个？**
A：只挑 flat 最大的函数下手；cum 大说明问题在下游，顺着 cum 找到 flat 集中处即可（机制见上文"flat 与 cum"一节）。

**Q：profile 显示热点在 `runtime.mallocgc`，怎么办？**
A：mallocgc 只是"现场"不是"元凶"——看它的 cum 调用栈找到分配来源的业务函数，减少分配（对象复用、`sync.Pool`、预分配容量）而不是去"优化"runtime。
