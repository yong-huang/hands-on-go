# 10 · G 状态机追踪：从 trace 时间线读出一个 G 的一生

> 本篇用 runtime/trace 录制一段混合负载（CPU 自旋、channel 交接、真实 HTTP、手动 GC），再用 go tool trace 与它的 JSON 导出，把一个 G（goroutine，Go 的轻量执行单元）在 _Grunnable、_Grunning、_Gwaiting、_Gsyscall 四个状态间的流转读出来。读完本篇，你能对着时间线说出每段颜色是什么状态、为什么切换。

## Background

没有 trace 之前，定位"程序为什么慢"主要靠打日志和 CPU profile。日志只记录代码执行到了哪一行，记录不了这一行卡了多久、卡在什么上。

CPU profile（按采样统计函数热点的剖析器）对等待几乎失明：一个 goroutine 阻塞在网络上时，它不在任何采样栈里，profile 只会显示 CPU 空闲。

runtime/trace 给出的是另一份数据：调度事件流。trace.Start 之后，每个 G 的创建、就绪、运行、阻塞、退出都带时间戳记进缓冲区，trace.Stop 落盘成文件，交给 go tool trace 回放成时间线。

## What

G 的生命周期由运行时状态机管理，本篇关心其中四个状态：

| 状态 | 含义 | 典型触发 |
|---|---|---|
| _Grunnable | 就绪：等着被放上 P | `go` 语句刚创建、被唤醒 |
| _Grunning | 运行：占着一个 P 在执行 | 被调度器选中执行 |
| _Gwaiting | 等待：不进运行队列 | channel 收发、锁、等网络 |
| _Gsyscall | 系统调用：卡在内核里 | accept、read、write |

P（processor）是运行时的逻辑处理器，G 必须占有一个 P 才能执行，P 的数量即 GOMAXPROCS。

runtime/trace 是标准库自带的调度事件录制器。可以把 trace.Start 到 trace.Stop 的区间想象成航班动态屏：每个 G 像一趟航班，就绪、起飞、盘旋、落地全部带时间戳。但和航班屏不同的是，它不显示"当前状态"，而是把全程录下来事后回放，还能导出成 JSON 逐条精读。

## When to Use

- **解释延迟毛刺**：p99（99 分位延迟，99% 请求耗时低于该值）出现尖刺、CPU profile 却很空闲，用 trace 看 G 在等谁、等了多久。
- **验证调度假设**："满载时新 G 要排队多久""GC 会不会卡住业务 G"这类问题，时间线直接给答案。
- **何时不用**：找 CPU 热点用 pprof 更轻；生产服务不要长期常开，录制开销与文件大小都随事件量增长。

| 工具 | 差异 | 什么时候选它 |
|---|---|---|
| pprof | 只采样占用 CPU 的栈 | CPU 热点定位 |
| runtime/trace | 全量调度事件，含等待与阻塞 | 延迟归因、调度问题 |
| GODEBUG=schedtrace | 周期打印队列长度，零录制成本 | 粗看负载分布 |

## Quick Start

需要 go 1.26，无第三方依赖。运行：

```bash
go run ./10_g_trace/
```

真实输出（墙钟耗时、文件字节数、sink 数值每次运行不同）：

```text
== 混合负载录制完成，墙钟耗时 62ms ==
trace 文件: g_states.out（38405 字节）
sink = 1947382 （自旋结果，证明循环没被优化掉）

解读步骤：
  1. go tool trace g_states.out
  2. 浏览器打开后截取这些视图：
     - view trace：主时间线，按 P 分行，看 G 的状态色条与就绪间隔
     - goroutine analysis：按函数分组，点开单个 G 看运行/等待时长与原因
     - user-defined tasks：应有 spin / pingpong / http 三个任务
     - GC 视图：确认 runtime.GC() 对应的标记阶段与 STW（stop-the-world）
  3. 在 view trace 里找一个跑 runOneHTTP 的 G：
     就绪→运行→阻塞读网络→就绪→运行到结束，构成四次状态变化
```

建议截取四个视图：view trace 主时间线、goroutine analysis 里 main.main 的详情、
user-defined tasks 列表、GC 视图。

本篇后面的数字来自 JSON 导出，取法：先 `go tool trace -http=127.0.0.1:8000 g_states.out`
起服务，再访问 `/jsontrace` 路径，得到 Chrome 格式的事件列表。

## How It Works

### 一次运行里的四次状态变化

下表是一次真实运行的 JSON 导出（单位微秒，相对 trace 起点）。执行 HTTP 客户端的主 G
（G1，main.main）在第一次请求上的完整流转：

| 时刻 (us) | 状态 | 发生了什么 |
|---|---|---|
| 41378 前 | _Grunning | 执行 dial、写出请求，执行片结束 |
| 41378 | _Gwaiting | 阻塞在响应读取，G 交给 netpoller（网络轮询器） |
| 约 47790 | _Grunnable | netpoller 就绪回调把 G 放回运行队列 |
| 47793 | _Grunning | 恢复执行，读走响应 |

等待 6415us：服务端 handler 睡 5ms，多出的约 1.4ms 是计时器唤醒与调度排队。
同一运行里第二、三次请求的等待为 6391us 与 6385us——同一段代码也不恒定。

### trace 里能读到的五类事件

| 事件类别 | 该次运行中的真实例子 |
|---|---|
| goroutine | G1 共 544 个执行片；channel 乒乓阶段出现一串 0.1~0.4us 短片 |
| 网络 | 上表的 6415us 网络等待；服务端 G 挂在 accept 上（blocked syscall） |
| 系统调用 | 115 个 syscall 执行片，其中 1 个 parked（阻塞进内核） |
| GC | 3 次 STW（stop-the-world，全局暂停，4.1/87.2/26.0us）+ 309.3us 并发标记 |
| 用户任务 | trace.NewTask 登记 spin、pingpong、http 三个任务，各带 plan 日志 |

### 重复三次，时序会变吗

同一程序连录三次，状态序列（就绪→运行→等网络→就绪→运行）三次一致，数字全部在漂：

| 次数 | G1 执行片数 | 最大三个等待 (ms) | STW 时长 (us) |
|---|---|---|---|
| 1 | 544 | 40.010 / 6.415 / 6.391 | 4.1 / 87.2 / 26.0 |
| 2 | 549 | 40.009 / 6.427 / 6.395 | 1.2 / 46.2 / 22.5 |
| 3 | 552 | 40.024 / 6.404 / 6.387 | 1.2 / 67.6 / 32.0 |

读 trace 要读结构，不要背单次数值：哪个 G 卡了、卡在什么上是稳定的；
执行片数量、等待毫秒、STW 微秒每次都在漂移。

## Pitfalls & Q&A

**自旋负载被整体优化掉**。空 for 循环会被编译器删除，trace 里就看不到 CPU 段。
本篇把循环次数累加进 atomic 计数并打印，输出里的 `sink = 1947382` 就是未被优化掉的证据。

**录制窗口太长，文件爆炸**。文件大小与事件量成正比，长跑服务不要全程录制。
做法是短窗口：复现问题的操作准备好后再 Start，采样到可疑区间立即 Stop。

**JSON 导出里找不到"就绪"状态**。/jsontrace 只含执行片与空隙，_Grunnable 段要回
viewer（go tool trace 的网页界面）看黄色条；要精确量化就绪时长，
见 [14_sched_latency](../14_sched_latency/)。

**Q：为什么 5ms 的 handler 睡出了 6.4ms 的等待？** 计时器唤醒有粒度，G 醒来后还要
重新排队等 P。等待 = 后端处理 + 唤醒与调度开销，满载时后者会进一步放大
（放大到什么量级，[14_sched_latency](../14_sched_latency/) 有实测分布）。

**Q：_Gsyscall 与 _Gwaiting 有什么区别？** 卡在内核里的等待是 _Gsyscall（如 accept 阻塞），
此时 M（OS 线程）也被占住，运行时可能把 P 移交给别的 M；_Gwaiting 由 Go 运行时管理
（channel、netpoller、计时器），M 立即空闲可运行别的 G。
