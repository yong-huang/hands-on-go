# 09 · Goroutine 栈与 runtime 信息：让程序看见自己

> goroutine（Go 的并发执行单元）没有名字、没有 id、没有"我是谁"的 API——这是 Go 刻意的设计。但调试泄漏、排查调度问题时，确实需要"看一眼"运行时：当前调用栈长什么样、goroutine 怎么在不经 return 的情况下体面退场、栈什么时候被回收。读完本篇，你能跑通 `runtime.Stack`、`runtime.Goexit` 与 goid 解析这三件事，并知道各自的能力边界。

## Background

出了并发 bug，排查者的第一问是"现在到底有哪些执行流、各自卡在哪一行"。日志给不出答案——业务字段里没有调用栈（调用栈：函数调用链的记录，一行一个栈帧）。

在标准库提供便利入口之前，常用的观测手段有两条：本地接调试器单步跟踪，生产环境基本不现实；接入 pprof（Go 自带的运行时剖析工具）抓 profile（运行时采样的快照数据），能力强但要预先挂好 HTTP 端点，适合事后剖析而不是随手 dump（一次性导出全部内容）看一眼。

真正没有被既有手段覆盖的痛点有三个：

- 观测：想零依赖地打印一次全部执行流的调用栈，确认"谁卡在哪"
- 退场：执行流陷在半路想让它结束——panic（向上传播的运行时错误）动静太大，层层 return 要改动整条调用链
- 身份：想给每个执行流编号来串日志，运行时却没有公开的编号入口

标准库 runtime 包（Go 运行时能力的入口包）里躺着几个零依赖的自我观测入口，恰好对准这三类需求。它们能力各异、边界也不同，本篇逐一跑通。

## What

**定义**：runtime 自我观测是一组标准库内置的零依赖诊断能力，用来在不加依赖、不重启服务的前提下回答"现在有哪些 goroutine、各自在哪、怎么退场"。defer（延迟调用：函数退出前逆序执行的清理机制）是其中退场能力的关键配角。

它由三件具体的事构成：

- `runtime.Stack(buf, all)`：把调用栈文本写进缓冲区 buf，并返回写入的字节数
- `runtime.Goexit()`：让当前 goroutine 立即退场，但 defer 照常执行
- goid：goroutine 的内部编号，没有公开 API，只能从 Stack 输出首行解析

**心智模型一句话**：runtime 是程序观测自身的镜子——`Stack` 把当前 goroutine 的调用帧"印"出来，`Goexit` 走与 return 相同的 defer 清理路径但只结束当前 goroutine，goid 则只能从 `Stack` 输出的首行"偷"出来。

可以把 runtime 包想象成汽车的后视镜：行驶中随时看一眼自身状态，不用停车（进程不中断、不需要额外接线）。但和后视镜不同的是，照全量（dump 所有栈）有短暂的全局暂停成本，而且它只读不改——不能用来控制执行逻辑，只能观测和退场。

## When to Use

这类能力在三类场景用得上：

- 排查泄漏或死锁时：在 HTTP debug 端点或超时分支里 dump 一次全部调用栈，确认"谁卡在哪"；与计数差值法配套使用，见 [08_leak_detect](../08_leak_detect/)
- 写后台 worker 时：任务需要"陷在半路也能退场"，且已注册的 defer 清理必须照常执行
- 串日志时：想把多个执行流的日志区分开，又不想改所有函数签名去显式传 id

何时不用：

- 热路径轮询：dump 全部栈有短暂的全局暂停成本，goroutine 多、栈大时开销可观（机制见 How It Works）
- 业务身份传递：编号会复用、格式无稳定性承诺，给 goroutine 带身份应显式传值

同类手段对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `runtime.Stack(buf, true)` | 零依赖，一次打印全部 goroutine 栈文本 | 现场排查泄漏/死锁、随手看一眼 |
| pprof goroutine profile | Stack 的增强版，带采样聚合与 HTTP 端点 | 需要持续剖析与统计分布时 |
| `context.WithValue`（沿调用链显式携带键值对） | 身份由调用方控制、语义稳定 | 需要 goroutine 级"身份"追踪日志时 |

## Quick Start

前置条件：安装 Go 即可，demo 纯标准库。

```bash
cd concepts/09_runtime_info
go run main.go
```

预期输出（节选；栈帧路径、行号、goid 与字节数每次运行可能不同）：

```text
========== 1. runtime.Stack：打印当前 goroutine 调用栈 ==========
goroutine 1 [running]:
main.main()
	/path/to/concepts/09_runtime_info/main.go:54 +0x7c
...(共 130 字节，栈帧地址每次运行不同)

========== 2. runtime.Goexit：提前退出，defer 照常执行 ==========
  [worker] defer 执行了——Goexit 不跳过 defer
  [main] worker 已被 Goexit 结束：defer 执行了，后面的代码没执行

========== 3. goid：没有公开 API，只能从栈首行解析 ==========
  解析到当前 goroutine id = 1（每次运行可能不同）
  runtime 刻意不暴露 goid：id 会复用、语义不稳定；
  生产代码给 goroutine 带"身份"请用 context.WithValue 传递。
```

三段输出对应 main.go 的三个部分：第 1 段打印当前 goroutine 的调用栈，第 2 段验证提前退场时 defer 照常执行，第 3 段从栈首行解析编号。main.go 约 80 行，仅用到 `fmt`、`runtime`、`strconv`、`strings`。

## How It Works

### runtime.Stack：把调用栈"印"进缓冲区

`n := runtime.Stack(buf, all bool)` 把栈文本写入 `buf` 并返回字节数。

`all=false` 只打当前 goroutine，`all=true` 打印**全部** goroutine——这是排查泄漏/死锁最常用的入口（pprof 的 goroutine profile 本质上就是它的增强版）。

首行格式固定为 `goroutine <id> [状态]:`，后跟一行一个栈帧。你在输出第 1 段看到的 `goroutine 1 [running]:` 就是这条固定格式；第 3 段解析编号，取的也是首行第二个字段。

成本边界：`all=true` 需要短暂 stop-the-world（运行时暂停整个进程的 goroutine）并逐个回收各 goroutine 的帧；栈大、goroutine 多时开销可观，适合诊断路径（HTTP debug 端点、超时 dump），不适合热路径轮询。

### runtime.Goexit：体面地提前退场

`runtime.Goexit()` 终止**当前** goroutine：已注册的 defer 全部执行——与函数 return 走同一条清理路径——然后 goroutine 正常消失、栈被回收。

栈的按需生长机制（初始 2KB、分段/连续栈）见 [internals/07_goroutine](../../internals/07_goroutine/)。

你在输出第 2 段看到的 `[worker] defer 执行了——Goexit 不跳过 defer`，就是这条清理路径的实证；worker 里 Goexit 之后的代码确实没有执行。

四种"退出"方式的边界（recover：内建函数，捕获 panic 后让程序恢复正常执行）：

| 方式 | 影响范围 | defer 执行 | 可 recover |
|---|---|---|---|
| `return` | 当前函数 | 执行（ defer 本就是 return 的一部分） | — |
| `os.Exit` | **整个进程** | 全部不执行 | 否 |
| `panic` | 向上传播 | 沿途逐层执行 | 是 |
| `Goexit` | 当前 goroutine | 执行 | **否** |

### goid：能"偷"到，但别当身份用

首行 `goroutine 1 [running]:` 的第二个字段就是 goid，`strings.Fields` 一拆就出来。但这是**黑客手段**：runtime 没有公开 API 是刻意设计——goid 会被复用、没有稳定性承诺、解析依赖栈输出的文本格式。

要在 goroutine 之间传递"身份"，标准答案是 `context.WithValue`；要区分日志归属，传一个业务层的 traceID（业务自定义的追踪标识）。

API 速览：

| API / 写法 | 含义 | 为什么重要 |
|---|---|---|
| `runtime.Stack(buf, false)` | 当前 goroutine 栈写入 buf | 自我观测的最小入口 |
| `runtime.Stack(buf, true)` | 全部 goroutine 栈 | 泄漏/死锁排查首选 |
| `goroutine N [running]:` | 栈输出首行 | goid 唯一可见的来源 |
| `runtime.Goexit()` | 结束当前 goroutine | defer 照跑，不可 recover |
| `runtime.NumGoroutine()` | 当前 goroutine 数量 | 配合 Stack 做泄漏观测（见 [../08_leak_detect/](../08_leak_detect/)） |

## Pitfalls & Q&A

坑清单，每条按"现象 → 原因 → 解法"展开：

- **现象**：调用了 `os.Exit`，defer 里的清理逻辑一条都没跑。**原因**：`os.Exit` 直接终止整个进程，跳过**所有** defer（包括 main 的）。**解法**：进程级清理不要只挂在 defer 上，退出前显式执行。
- **现象**：在 Goexit 的调用链上用 recover 兜底，什么也没拦到。**原因**：Goexit 不是 panic，进不了 recover 的作用域。**解法**：提前退场交给 Goexit，防 panic 与退场是两条独立路径。
- **现象**：在 main goroutine 里调 Goexit，程序直接结束，其他 goroutine 没被等待。**原因**：Goexit 只结束当前 goroutine，而 main 结束即进程结束。**解法**：把 Goexit 当"单执行流提前退场"用；等待其他执行流需要显式的同步机制。
- **现象**：Go 版本升级后，解析 goid 的代码开始返回异常值。**原因**：解析依赖栈输出文本格式，该格式没有稳定性承诺。**解法**：只当调试工具，不做业务依赖；解析失败时兜底返回（demo 里返回 0）。
- **现象**：worker 退场后，main 等的通知迟迟不来。**原因**：写在 Goexit 调用"下面那行"的通知代码永远不会执行。**解法**：把通知放进 defer——demo 用 `close(exited)` 而不是发送值。

**Q：为什么 runtime 不直接提供 `runtime.GoID()`？**

A：goid 会复用（goroutine 退出后编号可被新 goroutine 拿走）、没有跨平台稳定性承诺，一旦公开就会被当成协程身份滥用（做 goroutine-local storage 之类），破坏"显式传值"的并发模型。官方立场：需要身份就用 context 传。

**Q：`Goexit` 之后 goroutine 的栈去哪了？**

A：与正常 return 一样，g 对象（runtime 内部表示单个 goroutine 的结构体）被复用、栈内存归还或降级回收——不存在"Goexit 泄漏栈"的问题。泄漏发生在 goroutine **永远退不出去**的时候（见 [../08_leak_detect/](../08_leak_detect/)）。

**Q：demo 里 `parseGoid` 解析失败会怎样？**

A：返回 0 兜底。真实的调试工具（如社区惯用的 go-routine-id 库）会同时用 `reflect`/链接符号等手段交叉验证；本 demo 固定从 `runtime.Stack` 首行取，格式由 runtime 保证，目前非常稳定。
