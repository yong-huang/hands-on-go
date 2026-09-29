# 19 · 系统调用与阻塞 I/O：M 是怎么涨起来的

> `GOMAXPROCS` 定住的是 P（逻辑处理器）的数量，但 OS 线程 M 可以比 P 多——只要 goroutine 阻塞在真实系统调用（陷入内核的服务请求）上。
>
> 本实验用 8 个 goroutine 抢一把 `flock` 文件锁，实测 M 从 5 涨到 11、锁释放后不回落；读完你将能解释并复现"文件 I/O 费线程、网络 I/O 不费线程"这条分界线。

## Background

运维 Go 服务时，`GOMAXPROCS`（控制并行度的 Go 环境变量）常被当成"线程数旋钮"：设成 CPU 核数或容器配额，然后默认线程数也约等于它。多数时候这个近似成立，监控面板上的线程数长期贴着它不动。

撞墙发生在 I/O 密集路径上。某个 goroutine 卡进一次真正阻塞的文件读写、`flock` 或数据库驱动原生调用时，执行它的 OS 线程会被扣在内核里回不来。

调度器（Go 运行时里给 goroutine 分配线程的组件）只能再找或新建线程来跑其余 goroutine——线程数就此冲出 `GOMAXPROCS`。

更迷惑的是反例：网络连接数上百万，线程数却几乎不动。分清哪类 I/O 费线程、哪类不费，是排查线程数失控的前提。本实验把这条分界线压缩成最小场景——8 个 worker（干活的 goroutine）抢一把文件锁，全程打印计数。

## What

**定义**：本篇主题是 Go 调度器对阻塞系统调用的处理机制：G（goroutine，下文简称 G）陷入内核时，runtime 把它占着的 P 收走交给别人用，M 留在内核里等系统调用返回。G/M/P 是 Go 调度模型的三个角色——G 是执行体，M 是 OS 线程，P 是调度槽位。

**心智模型**：可以把 M 和 P 想象成"人与入场券"：M 是干活的人，P 是进场资格。人钻进后台干长活（阻塞在内核里），资格会被收走转给候补的人；干完活回来的人先试着领回一张票，领不到就去休息室睡觉。

但和真实抢票不同的是：休息室里的人不会被清退——M 不会被销毁，只会休眠待复用，线程数因此呈"峰值保留"。这正是后文输出里 after-release 阶段线程停在 11 的原因。

## When to Use

这套知识在三类时刻派上用场：

- 排查线程数告警时：容器线程数持续上涨而 `GOMAXPROCS` 没变，先怀疑有 goroutine 卡在真实阻塞调用上（文件 I/O、`flock`、部分 DB 驱动的原生调用、`syscall.Exec`）
- 给文件处理服务做容量评估时：每个阻塞在文件锁或磁盘调用上的 worker 实际占掉一个 OS 线程，预算要按阻塞并发数估，不能按 `GOMAXPROCS` 估
- 评审高并发网络服务的资源模型时：百万级长连接几乎不吃线程，依据是网络 I/O 走非阻塞 fd（文件描述符，进程持有连接的句柄）+ netpoller（网络轮询器，基于 kqueue/epoll 的事件机制）

什么时候不用：

- 纯网络服务没有这个问题：socket（网络连接端点）读写不占住线程，无需按本篇扩线程预算
- `time.Sleep`、channel 等待这类"暂停"也不属于此类——它们挂起 G 却归还 M
- 阻塞调用偶发且极短时，线程数只会小幅波动，不必为此改造代码

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 网络 I/O（netpoller） | 非阻塞等待，G 睡在 poller 里，M 不被占用 | 高并发连接场景的默认选择 |
| `time.Sleep` / timer | G 被 timer 挂起，M 归还空闲池 | 定时等待，但复现不了线程暴涨 |
| 真实阻塞 syscall（`flock`、文件读写） | M 被扣在内核里，P 被移交，线程数可超 `GOMAXPROCS` | 确需文件锁或原生阻塞调用时；本实验用它复现 |

## Quick Start

前置条件：仓库根目录下装有 Go 即可，无其他依赖。

```bash
cd internals/19_syscall
go run main.go          # 文本演示：五个阶段的 G/M/卡锁 worker 计数
go run main.go -json    # 输出 JSON 快照（供程序化读取）
```

程序依次经过五个阶段：baseline（基线）→ start → blocked-8（8 个 worker 卡上文件锁）→ blocked+（持续阻塞）→ after-release（释放锁）。

预期输出（go1.26.5 实测）：

```text
 阶段            G数   M(累计线程)  新增线程  卡锁worker
 baseline          1       5          0         0
 start             1       5          0         0
 blocked-8         9      11          6         8
 blocked+          9      11          6         8
 after-release     1      11          6         0

GOMAXPROCS=2，但 8 个 worker 同时阻塞在 flock → M 撑到 11
```

`M` 列来自 `threadcreate` profile（runtime 记录线程创建事件的采样数据），是**累计创建的线程数**。基线 5 与增量 +6 是一次实测值，随机器、平台与 Go 版本浮动（基线可能是 4 或 6）。

但"阻塞期线程远超 `GOMAXPROCS`、释放锁后不回落"这两个定性结论，在任何平台都稳定复现。

读表先看三格：blocked-8 行 M 列 11（新增 6）、卡锁worker 列 8（JSON 字段名 waiting）；after-release 行 G 回落 1，M 仍停 11。这三格就是下文机制小节要解释的全部现象。

## How It Works

### handoff：P 为什么必须离开 M

G 调用 `syscall.Flock` 后进入 `_Gsyscall` 状态（G 生命周期里表示"卡在 syscall 中"的状态），M 带着它一起陷入内核。

此时最贵的资源不是 M，而是 P：P 手里攥着本地 runq（待运行 G 的队列）和 mcache（每 P 一份的内存分配缓存），被一个短时间回不来的 M 握着，整个调度槽位就空转了。

runtime 的 sysmon（后台监控线程，周期巡检全局状态）发现某 P 停在 syscall 里太久，就执行 handoff（移交）：把 P 从该 M 手里收走。P 的下家按优先级找——有空闲 M 就唤醒，没有就**新建一个 OS 线程**。

Quick Start 输出里 blocked-8 阶段"新增线程 6"，就来自这里。

### syscall 返回后：取回 P，否则休眠

锁释放、`flock` 返回后，M 并不会"迷路"：`exitsyscall`（runtime 的 syscall 出口）先走快速路径，用 `pidleget` 从全局空闲 P 列表取回一个 P，取到就立刻继续跑 G。

取不到（比如别的 M 正占着全部 P）就走慢路径：把 G 放回全局队列，自己 `stopm` **休眠进空闲 M 池**。对照输出里 after-release 阶段 M 仍是 11——M 不被销毁，只是睡着待复用，与 What 节"峰值保留"的失效边界互相印证。

### 怎么读五个阶段的数字

blocked-8 起 M 从 5 跳到 11（+6），远超 `GOMAXPROCS=2`；after-release 阶段 G 回落到 1，M 停在 11 不动。阻塞期"卡锁 worker 8 / 新增线程 6"同峰，释放后一个清零、一个停涨，因果对照就在这两列之间。

为什么 8 个阻塞 worker 只新增 6 个线程？因为账面增量是**新建**的 M 数，而调度器会先复用空闲 M 池里已有的线程——基线那 5 个中的一部分被直接派去接 P。差值不恒等于 worker 数，别把它当公式背。

### 对照组：netpoller 为什么不涨线程

网络 I/O 走的是非阻塞 fd + netpoller（macOS 用 kqueue，Linux 用 epoll）：socket 永不真正阻塞线程，G 被挂在 poller 里睡，M 完全空闲、转身就能从 runq 领下一个 G。

`time.Sleep` 同理——G 被 timer 挂起，M 归还空闲池。想复现"M 暴涨"，必须用真正阻塞的系统调用，`flock` 是最顺手的一个。

### 输出列与代码字段的对应

`main.go` 的 snapshot（快照）结构产出输出表的每一列：

| 字段 | 含义 | 为什么重要 |
|---|---|---|
| `phase` | 阶段名：baseline/start/blocked-8/blocked+/after-release | 五个采样点串出完整时间线 |
| `goroutines` | `runtime.NumGoroutine()` 当前存活 G 数 | 阻塞期 1→9，释放后回落到 1 |
| `threads` | `threadcreate` profile 的 `Count()` | **累计创建**的线程数，不是当前存活数 |
| `threadDiff` | `threads - baseline` | 可观测的"因阻塞新建"信号 |
| `waiting` | 仍卡在 `flock` 上的 worker 数 | 与 threadDiff 同峰同落，构成因果对照 |

## Pitfalls & Q&A

### 踩坑清单

- **`threadcreate` 是累计值**：把 `Count()` 当"当前存活线程数"读，基线就算错了
  - 原因：它统计进程开跑以来创建过的线程总数（本机那次基线 5，含 main、sysmon、GC 等后台线程）
  - 解法：用"当前采样 − 基线"的差值说话；基线随平台浮动，别背常数
- **`time.Sleep` 和网络 I/O 复现不了**：对照组里 M 纹丝不动，实验得不到现象
  - 原因：Sleep 挂起 G 却把 M 放回池子；网络走 netpoller（机制见上文对照组一节）
  - 解法：改用真正阻塞的系统调用，`flock` 最方便
- **同一 fd 重复 `flock` 永不阻塞**：想制造争锁，却一次都没卡住
  - 原因：macOS 上同一进程两个独立 `open` 的描述符才会在同一 inode（文件的底层标识）上互斥；同一 fd 重复 `flock` 只会无害升级锁
  - 解法：主协程与 worker 各自 `open`，"主协程锁死 + 8 worker 抢"才成立
- **worker 里的 EINTR 分支不能省**：worker 提前退场，阻塞期采不满 8
  - 原因：`flock` 可能被信号打断而返回 `EINTR`（错误码，表示调用被信号中断）
  - 解法：收到 EINTR 就重试，不当错误放弃；信号机制见 [20_signal](../20_signal/)
- **Go 字符串字面量里别嵌裸英文 `"`**：字符串被劈成两半，报语法错误
  - 原因：英文双引号本身就是 Go 字符串的边界
  - 解法：中文语境用「」或转义 `\"`

### 深入问答

**Q：`GOMAXPROCS` 限制的到底是线程数吗？**
A：不是。它限制的是 P 的数量，即同时执行 Go 代码的并行度；本实验 `GOMAXPROCS=2` 却实测到 11 个线程。移交细节见上文 handoff 一节。

**Q："百万并发连接"的 Go 服务，线程数为什么不涨？**
A：网络 I/O 是非阻塞 fd + netpoller：G 睡在 poller 里，M 随时可跑别的 G。

反过来把这条当排查信号——`GOMAXPROCS` 不变而线程数持续上涨，说明有人在打真实阻塞 syscall（文件 I/O、`flock`、部分 cgo（Go 调 C 代码的机制）/DB 原生调用）。
