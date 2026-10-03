# 13 · netpoller 与 C10K：一万条连接，线程只有 27 条

> 本篇写一个 goroutine-per-connection 的 echo 服务并实测 C10K（单机一万并发连接）：10000 条空闲连接下 RSS 63MB、goroutines=10001、线程仅 27 条、echo p99=32µs。读完本篇能画出一条连接从 accept 到 data-ready 的 park/unpark 时序，并算清每连接的内存账。

## Background

"一连接一线程"是网络服务最直白的设计：一个线程守一条连接，阻塞读写，代码顺序好写。但一万条连接就是一万个线程——按每线程 1MB 栈起步，光栈就是 10GB，内核上下文切换与调度负担随之爆炸。

手工多路复用是第一条出路：select/poll/epoll 把成千上万个 fd（文件描述符，每条连接一个）交给一次系统调用盯着。但事件驱动把顺序代码拆成回调，状态机写法陡增。

Go 的 netpoller（网络轮询器：运行时对 epoll/kqueue 的封装）把两条路合并：API 保持阻塞式写法，实现上 fd 注册进内核多路复用、阻塞的 goroutine 挂起让路。本篇验证它撑不撑得起一万条连接。

## What

goroutine-per-connection：accept 到的每条连接配一个 goroutine，业务代码写成顺序阻塞风格；netpoller 负责在"无数据可读"时挂起对应 G，数据到达时唤醒它。

每条空闲连接的真实成本（本篇实测配置）：

| 部件 | 大小 | 说明 |
|---|---|---|
| goroutine 栈 | 2KB 起步 | 空闲连接几乎不增长 |
| bufio 读缓冲 | 256B | 按行协议可再省 |
| netFD 与 fd 表项 | 数百 B | 运行时与内核各一份 |

可以把 netpoller 想象成餐厅的叫号器：顾客（G）下单后去休息区（_Gwaiting），
服务员（线程）不守着任何一桌，出餐叫号（kqueue 事件）才有人被唤醒。
但和叫号器不同的是，休息区里的人不占座位也不占服务员，一万桌也只要几个服务员。

## When to Use

- **长连接网关与推送**：设备、App 保持大量空闲连接，偶尔来一条消息——空闲成本决定容量。
- **代理与转发层**：连接多、单连接逻辑简单，goroutine-per-connection 的顺序写法最不容易错。
- **何时不用**：单连接要做重计算或大事务时，连接数往往不大，直接阻塞模型即可；公网高扇出场景还需在应用层再加并发上限。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 线程-per-connection | 栈大、切换贵 | 连接数十级、逻辑极简 |
| 手工 reactor（epoll+回调） | 省内存但状态机难维护 | 资源预算严苛的 C/C++ 场景 |
| goroutine-per-connection | 阻塞写法 + 运行时多路复用 | Go 里的默认选择 |

## Quick Start

需要 go 1.26，无第三方依赖。macOS 注意文件描述符上限：本机实测 `ulimit -n` 已是
1048575，无需调整；默认 256 的机器先执行 `ulimit -n 12288`。

两个终端分别运行：

```bash
go run ./13_netpoller_c10k/ -role=serve -addr=127.0.0.1:9007
go run ./13_netpoller_c10k/ -role=connect -addr=127.0.0.1:9007 -n=10000 -msgs=2000
```

真实输出（建连耗时与 RTT 每次运行不同，µs 级为主）：

```text
== 阶段 1：建连 10000 个，失败 0，耗时 191ms ==
== 阶段 2：空闲态账本 ==
  conns=10000 goroutines=10001
  goroutines-conns=1（诚实预期：个位数常数）
== 阶段 3：2000 次 echo 的 RTT 分位数 ==
  p50=22µs p95=28µs p99=32µs max=1.202ms（样本=2000）
RSS 请在服务端进程上另测：ps -o rss= -p <serve 的 pid>（单位 KB）
```

连接保持期间在服务端进程上采集（命令与真实结果）：

```bash
ps -o rss= -p 69510 | awk '{printf "RSS = %.1f MB\n", $1/1024}'   # RSS = 63.0 MB
ps -M 69510 | tail -n +2 | wc -l                                   # 27
```

## How It Works

### 一条连接的 park/unpark 时序

```text
accept() 返回 conn
  └─ go handle(conn)：新 G 进入 _Grunnable，排队等 P
       └─ G 上 P 转 _Grunning，执行到 conn.Read
            └─ fd 首次注册进 kqueue；gopark 把 G 置为 _Gwaiting
                 └─ M 与该 G 解绑，回调度器跑别的 G   ← 空闲期：线程不被占用
（数据到达）
kqueue 报告 fd 可读 → netpoll 返回就绪的 G → goready 置回 _Grunnable
  └─ G 重新上 P：读出数据、回写 echo，循环回到 conn.Read
```

你在 Quick Start 看到的 goroutines-conns=1，就是这条链路的直接后果：每连接
恰好一个 G，唯一的例外是 accept 循环自己的 G。同样地，"线程 27 条"说明
一万个挂起的 G 只需要 GOMAXPROCS（18）个常驻执行线程加少量运行时线程。

### 内存账对表

RSS 63MB = 运行时与堆的底数（约 30MB）+ 一万条连接的增量（约 33MB），折合每连接
约 3.3KB：2KB 栈 + 256B 缓冲 + netFD 与 fd 表。

验收线 RSS<100MB 对应 1 万连接规模；若受 ulimit 限制只能开更少连接，3.3KB 的
每连接系数不变，预算按 `连接数 × 3.3KB + 底数` 换算即可。

### p99=32µs 怎么读

回环地址 + 空闲连接，一次 RTT 只含两次协议栈穿越和两次唤醒，微秒级合理。
max 1.202ms 是尾部的单次尖峰，来源在 GC 或调度排队
（量化方法见 [14_sched_latency](../14_sched_latency/)）；p95 到 p99 只涨了 4µs，
说明负载下没有系统性排队。

## Pitfalls & Q&A

**too many open files**。macOS 默认软上限 256 条 fd，建连几百条就报错。
先 `ulimit -n` 检查，再提到 12288 以上（本机实测已是 1048575，可直接跑 1 万）。

**每连接缓冲给太大**。4KB 缓冲 × 一万连接 = 40MB 起步，纯粹浪费。
行协议用 256B 足够；确需大缓冲，读到长度后再按需扩。

**连接全关了，RSS 不回落**。实测一万连接关闭后 RSS 仍为 63.6MB：goroutine 栈
归还给运行时的内存池，是否还给操作系统由平台策略决定。观察泄漏要看
goroutine 数（STATS 输出），不能只看 RSS。

**Q：为什么一万条连接不用一万个线程？** 阻塞在 netpoller 上的 G 不占线程：
gopark 之后 M 立即回调度器。线程只在 G 真正运行或陷入内核 syscall 时被占用。

**Q：再往上一万变十万（C10M）还成立吗？** 内核 fd 与协议栈先成为瓶颈，
通常需要 io_uring/DPDK 一类绕过内核的手段；netpoller 的每连接 3.3KB 账本
只覆盖 Go 层，不含内核侧成本。

**Q：netpoller 只管网络吗？** 常规文件不走它（普通文件读写是真 syscall，
会占住线程），管道、tty 与 socket 才注册进 netpoller。阻塞视角与调度视角的
这条边界，与 [12_preemption](../12_preemption/) 里 syscall 占 M 的讨论同源。
