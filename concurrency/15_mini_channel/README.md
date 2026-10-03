# 15 · mini channel：锁 + 环形缓冲 + 等待队列的运行时复刻

> 不看 runtime/chan.go，按自己的理解复刻一个泛型 Chan[T]：一把互斥锁、一个环形缓冲、发送/接收两条等待队列，覆盖阻塞收发、缓冲回绕、close 广播三语义。读完能对着自己的代码指出 runtime 对应路径，并说清与真实 hchan 的五处差异。

## Background

多个 goroutine 之间传数据，最直接的做法是共享切片加互斥锁加轮询：
发送方加锁追加，接收方循环查有没有新元素。互斥锁（mutex）指同一
时刻只允许一个持有者进入临界区的锁。方案能跑，但轮询白烧 CPU，换成
条件变量又要自己防丢失唤醒（见 [07_mini_cond](../07_mini_cond/)）。

channel 把这组问题收进一个原语：有界缓冲、满则阻塞、空则阻塞、关闭
广播，语义由 runtime 统一担保。

对实现者，channel 是一张明确的内部结构图——缓冲、两条游标、两条
等待队列，全部读写在一把锁内完成。本篇按这张图徒手复刻（先写后
对照源码，同 [05_mini_mutex](../05_mini_mutex/) 的盲写路线），
差异就是理解与实现之间的缝。

## What

**定义**：Chan[T] 是一个复刻 hchan（channel 在 runtime 里的内部结构）
的泛型值管道，三个方法对齐内建 channel 的阻塞收发与关闭语义。

| 部件 | 职责 |
|---|---|
| `buf + sendx/recvx` | 环形缓冲与读写游标，下标对容量取模回绕 |
| `recvq / sendq` | 排队中的接收者/发送者，FIFO（先进先出）链表 |
| `waiter.ready` | 每个等待者一个私有通道，唤醒即 close 它 |
| `closed` | 关闭标记；Close 在锁内一次性广播两条队列 |

可以把 Chan 想成带候客区的传送带：传送带是环形缓冲，候客区是等待
队列。但和传送带不同的是，空手取货的人也会被登记叫号，货一到直接
塞进他手里、不经过传送带——这条"直接交接"路径是无缓冲 channel 快的原因。

## When to Use

什么时候值得手写 channel：

- 想回答"向已关闭发送为什么 panic、接收却只拿零值"这类问题时，实现一遍胜过背结论
- 阅读 runtime/chan.go 前的预热：盲写后对照差异表，理解的缝一眼可见

什么时候必须用内建 channel：

- 一切生产代码：内建 channel 与 select、runtime 调度深度耦合
- 需要 select 多路等待时本实现没有登记接口（差异表第 1 条）

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 内建 channel | runtime 信号量 + sudog，支持 select | 默认选择 |
| Chan[T]（本篇） | sync.Mutex + 私有通道停车，无 select | 学习与语义实验 |
| 锁 + 切片手搓队列 | 无"直接交接"与关闭广播协议 | 不建议，丢唤醒风险高 |

## Quick Start

前置条件：go 1.26，纯标准库。运行分步演示：

```bash
go run ./15_mini_channel/
```

真实输出（第 5 步接收者的编号顺序每次可能不同：谁先停进队列由调度决定）：

```text
== 第 1 步：结构总览 ==
Chan[T] = 锁 + 环形缓冲(sendx/recvx) + 发送/接收两条 FIFO 等待队列。
发送：交接空闲接收者 → 写缓冲 → 排队停车，三选一；接收对称。

== 第 2 步：无缓冲直接交接（rendezvous） ==
发送方：ping 已递到接收者手里（固定）
接收方：收到 "ping"（固定）

== 第 3 步：环形缓冲回绕（cap=3） ==
发满 3 个：           qcount=3 sendx=0 recvx=0 closed=false 排队[发0 收0]
收走 1 个：           qcount=2 sendx=0 recvx=1 closed=false 排队[发0 收0]
再发 4（sendx 回绕）：qcount=3 sendx=1 recvx=1 closed=false 排队[发0 收0]

== 第 4 步：缓冲满发送阻塞 ==
50ms 后发送 2 仍未返回：qcount=1 sendx=0 recvx=0 closed=false 排队[发1 收0]（固定）
接收方取到 1，发送 2 随即完成（固定）
发送方 2：接收者取走 1 之后才发出（固定）

== 第 5 步：close 广播唤醒全部排队接收者 ==
接收者 2：醒来 ok=false（固定）
接收者 3：醒来 ok=false（固定）
接收者 1：醒来 ok=false（固定）

== 第 6 步：向已关闭 channel 发送 panic ==
recover 到：send on closed channel（固定）
```

验收命令（三语义测试在 chan_test.go，实测记录）：

```bash
go test -race -count=200 ./15_mini_channel/   # ok  27.246s
```

## How It Works

Send 只有三种出路，判断顺序固定：有等待中的接收者就把值写进对方
waiter 槽位并叫醒（不碰缓冲）；否则缓冲未满就写 sendx 槽位、推进
游标；其余情况打包成 waiter 排进 sendq，解锁后停车等待。

Recv 对称，但多一处协议——sendq 有人排队且缓冲非空时，先吃缓冲队头，
再把队首发送者的值补进缓冲队尾：

```go
if s := ch.sendq.pop(); s != nil {
    if ch.qcount > 0 { // 先吃队头，发送者的值补队尾
        v = ch.buf[ch.recvx] // 跨过缓冲边界，FIFO 仍等于发送顺序
        ch.recvx = (ch.recvx + 1) % len(ch.buf)
        ch.buf[ch.sendx] = s.elem
        ch.sendx = (ch.sendx + 1) % len(ch.buf)
    } else { v = s.elem } // 无缓冲：发送者直接递到手里
}
```

这半步保证"接收顺序 = 发送顺序"跨越缓冲边界依然成立，第 3 步看到的
sendx 回绕与 TestFIFOAcrossQueuedSenders 的断言都依赖它。

Close 在锁内置 closed、释放 sendq（发送者醒来补 panic）、释放 recvq
（接收者拿 ok=false）；缓冲已有的值取完后才进入零值阶段，见
TestCloseWithBufferedItemsDrains。

**与 runtime hchan 的差异表**（盲写后对照 Go 1.26 runtime/chan.go）：

| # | 简化点 | 标准库做法 | 后果 |
|---|---|---|---|
| 1 | 不支持 select | selectgo 对多个 channel 按地址排序后两阶段加锁，并在 sudog（runtime 的等待者描述符）上多队列登记 | 多路等待只能开 goroutine 模拟，语义不等价且更慢 |
| 2 | 停车用每等待者一个通道 | sudog 挂 runtime 信号量，gopark/goready 直接换栈 | 每次阻塞多一次堆分配与一次 channel 往返，唤醒延迟更高 |
| 3 | 锁用 sync.Mutex | runtime.mutex，靠 raceacquire/racerelease 显式登记先后关系 | mini 靠 mutex 与自身通道天然被 -race 理解；争用特性不同 |
| 4 | 元素拷贝走泛型字段赋值 | typedmemmove，写屏障与 cgocheck 挂在拷贝上 | 拷贝语义相同，少一层检查开销，也少了 cgo 指针检查 |
| 5 | 无方向类型与编译期 nil 防护 | 编译期 `<-chan`/`chan<-`，内建 nil 判定 | 方向限制要自己包接口；nil 收发已实现，但只靠运行期 |

## Pitfalls & Q&A

**坑 1：直接交接忘了前置条件。** 有等待接收者时缓冲必为空（接收者排队
说明缓冲已被吃空），此时若写缓冲，接收者会饿死在队列里。把 recvq
判断放在缓冲判断之前，就是在维护这个不变式。

**坑 2：唤醒用非阻塞发送，信号落空死锁。** 唤醒者向容量 1 通道投递时
接收者尚未停车，投递失败即丢唤醒。改用 close(ready) 后不可能丢。

**坑 3：环形缓冲不清槽位，对象不回收。** Recv 取走值后槽位仍挂着引用，
大批量收发时缓冲拖住整代对象；取走后立刻写零值（对应 runtime 清 slot）。

**Q：为什么向已关闭发送是 panic，接收却只是零值？** 接收拿零值是关闭
广播的最廉价实现，所有接收者都能立即返回。发送 panic 是防御性的：
close 之后塞进来的值永远无人接，runtime 当场报错而非静默吞值，本实现
两条 panic 路径都对齐了这一行为。
