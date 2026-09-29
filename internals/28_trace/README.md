# 28 · 调度器 Trace 分析：用 runtime/trace 看清三类 goroutine 到底卡在哪

> `top` 和 CPU profile 只能告诉你"谁在烧 CPU"，回答不了"谁在等、等多久、被谁卡住"。读完本篇，你能用 `runtime/trace` 录一段混合工单的完整调度轨迹（4 个 CPU 密集 + 4 个模拟 I/O + 4 对 producer/consumer），并用 `go tool trace` 出调度延迟画像，再用自建埋点补上它的盲区。

## Background

定位性能问题时，最常见的工具是 `top` 和 CPU profile（pprof，按采样统计函数热点的剖析器）。它们对"等待"几乎是盲的：一个 goroutine（Go 的轻量级协程）阻塞在 channel 上时，它根本不在采样栈里，profile 只显示"没人用 CPU"。

要回答"这 40ms 到底花在哪"，需要调度器视角的事件流——谁何时就绪、何时真正上 P（调度器的逻辑处理器，goroutine 必须被放到某个 P 上才能执行）、阻塞原因是什么。

`runtime/trace` 就是这份数据的官方出口：`trace.Start` 后每个调度事件都带（时间戳, goroutine, P, 原因）记进 buffer（内存缓冲区），`trace.Stop` 落盘成 trace 文件，供事后分析。

## What

**定义**：runtime/trace 是标准库里的调度事件录制器——在指定区间内记录所有 goroutine 的创建、就绪、阻塞、运行、退出事件，落盘后可用 `go tool trace` 回放成时间线、GC、阻塞等视图。

**心智模型**：可以把 `trace.Start(f)` 到 `trace.Stop` 之间的区间想象成行车记录仪——中间所有 goroutine（CPU 忙循环、I/O 阻塞、chan 交接）的调度事件全部录进 buffer，事后离线回放。

但和真实行车记录仪不同的是：它不实时显示画面，必须 Stop 落盘后才能回放；录制本身有开销、文件大小与事件量成正比，只适合诊断窗口期，不适合长期常开。

## When to Use

### 典型场景

- 延迟归因：接口 p99（99 分位延迟，即 99% 请求耗时低于该值）出现毛刺、profile 却显示 CPU 空闲——用 trace 查"谁在等、等多久、被谁阻塞"。
- 调度调优验证：调整批量大小、channel 缓冲、goroutine 数量前后，用 `-pprof=sched` 对比调度延迟变化。
- 交接密集服务的瓶颈定位：确认调度延迟大头是 channel 交接、锁竞争还是调度器本身。

### 何时不用

- 纯 CPU 热点：pprof 更轻量，trace 属于杀鸡用牛刀。
- 生产环境长期开启：录制开销与文件体积都不可忽视，只在诊断窗口采样。

### 同类方案对比

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| pprof CPU profile | 采样"谁在占 CPU"，等待中的 goroutine 不在样本里 | CPU 热点定位 |
| runtime/trace | 记录全部调度事件，能回答等待与阻塞归因，开销较大 | 延迟、卡顿、调度问题 |
| 自建时间戳埋点 | 只量自己关心的区间墙钟，零依赖 | trace 的盲区（如系统调用睡眠时长） |

## Quick Start

前置条件：无第三方依赖。运行：

```bash
cd internals/28_trace
go run main.go          # 录制 trace 并打印埋点统计（trace 文件写入系统临时目录）
go run main.go -json    # 输出 JSON 快照（埋点事件 + trace 文件路径）
go tool trace <打印出的 trace 路径>   # 浏览器交互式时间线/GC/阻塞视图
```

预期输出（节选，路径每次运行不同）：

```text
trace 文件：/var/folders/.../trace3411224687.out
goroutine 事件数：32（4 类 × 8 个 goroutine × 2 个相位）
提示：go tool trace /var/folders/.../trace3411224687.out（浏览器交互式）
```

诚实预期：输出里"4 类 × 8 个 goroutine"是程序打印的笔误，实际是 3 类共 16 个 goroutine、32 个相位事件（cpu×4 + io×4 + producer/consumer×8）。

`-json` 快照里的自建埋点（相对时间）：io 任务单次历时 ~40.8ms（读文件 + Sleep 40ms），cpu/chan 任务的埋点历时都接近 0——因为埋点只量"墙钟在任务里"的时间，chan 交接的等待发生在 `<-ch`/`ch <-` 阻塞点内部，这正是要用 trace 才能拆开的部分。

## How It Works

### 录制边界：Start/Stop 之间的才是你的

`trace.Start` 后所有调度事件都进 buffer，包括不相关的；`trace.Stop` 落盘。所以要圈定业务区间：Start → `wg.Wait` → Sleep(10ms) 让尾部事件落袋 → Stop。Stop 必须排在 `wg.Wait` 之后，否则任务没跑完文件就不完整。

写文件用 `os.CreateTemp` 拿可写路径（直接写 `/tmp` 固定路径在 macOS 沙箱会 EACCES，即权限拒绝错误）。

### 三类任务的调度形态

自建埋点统计的墙钟（wall-clock，实际流逝的时间）：io 条带长约 40ms 且错峰（睡 40ms 让出 P）；cpu 与 chan 的条带短到几乎看不见——不是它们快，而是它们的"卡"发生在埋点看不见的调度事件里。

trace 时间线视图里三者形态完全不同：CPU 任务是连成片的绿色 Running 块；I/O 是"拉起-就绪-睡下"的锯齿；chan 任务频繁在 Runnable（就绪）/阻塞间切换。

### 调度延迟画像：-pprof=sched

`go tool trace -pprof=sched` 把 trace 里 goroutine 从"就绪到被调度"的等待时间按栈聚合成二进制 pprof，再喂 `go tool pprof -top` 解析。它衡量调度系统的认领延迟，不是任务本身的耗时。本次实测（总量 ~110μs，每次运行波动）：

```text
      flat  flat%   sum%        cum   cum%
   50.75us 45.97%        runtime.chanrecv1     <- 消费者接收侧，最大单项
   38.02us 34.43%        sync.(*Mutex).Unlock  <- hchan 锁
    6.47us  5.86%        runtime.chansend1     <- 生产者发送侧
```

channel 相关合计 ~86%：send/recv 都要先抢 `hchan` 锁（channel 的运行时头结构，内含锁与等待队列）再入队/出队，无缓冲交接还必须唤醒对端才能继续——所以 chan 是调度延迟一号位。

但这通常是"正确的慢"（交接语义的固有代价），想降就减少交接次数（批量/缓冲），而不是换掉 chan。

CPU 密集任务全程 `Runnable→Running` 自占 P，在 sched profile 里近乎为 0。

真正能逼停它的是 sysmon（运行时后台监控线程）约 10ms 的抢占——"没有调度延迟"不等于"没有卡顿"，还要看它是不是把 P 占死了。

### 自建埋点补 trace 的盲区

trace 工具告诉你"调了谁、等了谁"，自建时间戳告诉你"每件事墙钟花了多久"。两者互补。

sched profile 对 I/O 睡眠近乎盲视（它不产生"就绪等待"，直接让出 P），40ms 的墙钟只有埋点能量出来；反过来 chan 内部的等待分布只有 trace 能拆。

### 关键命令与字段速查

| 命令/字段 | 含义 | 为什么重要 |
|---|---|---|
| `trace.Start(f)/Stop` | 录制边界 | Stop 必须在 `wg.Wait` 之后，否则文件不完整 |
| `go tool trace <file>` | 浏览器交互式 UI | 直接跑会弹浏览器；纯脚本要走 `-pprof=` 离线模式 |
| `go tool trace -pprof=sched` | 调度延迟 profile（二进制） | 输出不是文本表，要再喂 `go tool pprof -top` |
| `os.CreateTemp` | 临时文件可写路径 | 写 `/tmp` 固定路径在 macOS 沙箱会 EACCES |
| 埋点 `{kind,id,phase,tsMs}` | 自建生命周期事件 | `UnixNano()/1e6` 得 ms；锁保护 append 防并发写乱序 |

## Pitfalls & Q&A

**坑 1：pprof `-top` 的 flat 列直接转浮点。** 现象：`float("28.54us")` 报 ValueError。原因：flat 列带单位 `us`（flat 是函数自身耗时，cum 是含被调的累计耗时）。解法：先去掉单位再转（本实验 viz.py 已处理）。

**坑 2：Stop 之前进程退出。** 现象：trace 文件打不开或缺尾部事件。原因：`trace.Start` 之后所有调度都被记录，缓冲未落盘就退出等于丢数据。解法：`go run` 子进程也要等 Stop 落盘才退出。

**坑 3：把埋点显示 0 当成"没等"。** 现象：chan 任务的埋点历时接近 0。原因：埋点墙钟只量"在任务函数里"的时间，chan 交接的等待在 `<-ch` 内部。解法：chan 等待归因交给 trace，埋点只看墙钟区间。

**坑 4：轻信程序打印的统计口径。** 现象：输出写"4 类 × 8 个 goroutine"。原因：程序打印笔误，实际 3 类共 16 个 goroutine、32 个相位事件。解法：以代码里的任务定义为准（cpu×4 + io×4 + producer/consumer×8）。

**坑 5：抠单次运行的数字。** 现象：地址/路径/延迟数值每次运行都变。原因：临时路径随机、调度时序有波动。解法：对比实验用同一台机器多次取趋势。

**Q：runtime/trace 和 pprof 有什么区别？什么时候用哪个？**
pprof 采样"谁在占 CPU"（等待中的 goroutine 不在样本里）；trace 记录调度事件流，能回答"谁在等、等多久、被谁阻塞"。CPU 热点用 pprof，延迟/卡顿归因用 trace，两者互补。

`-pprof=sched` 输出的含义、channel 占 ~86% 是否是坏事、CPU 任务为何零调度延迟，均已在 How It Works 的"调度延迟画像"一节展开，不再重复。
