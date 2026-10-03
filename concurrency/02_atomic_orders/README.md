# 02 · atomic 语义层级：SeqCst 与 Acquire/Release 的分界

> Go 的 atomic 没有内存序参数，全部是顺序一致（SeqCst）语义。本篇把 Release/Acquire 作为"推理层级"用起来：发布对象、消息传递、CAS 自旋锁三个场景逐个验证配对语义，并用 store buffer litmus 划出"哪里必须全序"的分界线。

## Background

多个核心各改各的缓存时，"谁先写"在不同硬件上答案不同：x86 的写不乱序，
ARM 的写可以晚于更晚的指令生效。早期给并发代码挑 CPU 指令的做法是直接查
芯片手册数屏障（barrier，强制前后访存按序生效的指令），换一个架构重查一遍。

痛点在于：程序员真正想要的从来不是"这条指令前面加个屏障"，而是"这批写作为
整体对别人可见"。逐条对指令层做正确性论证既难写又难审，一行改动就能失效。

C++11 与 Java 先后把这个问题收进语言标准：不再描述指令，而是描述"操作之间
允许的顺序"，即内存序（memory order）层级。Go 走得更窄——公开 API 只提供
顺序一致一档，把推理负担降到最低。

## What

**定义**：Release 写（出闸）保证"本次写之前的所有写，对之后读到本次写效果的读者可见"；Acquire 读（进闸）保证"读到出闸写的效果后，出闸前的写全部可见"。

两者配对即构成一条 happens-before 边。SeqCst（顺序一致）在 Release/Acquire
之上再加一条：所有原子操作存在全局唯一的时间线，任何 goroutine 看到的先后
顺序一致。

Go 的 `sync/atomic` 全部操作都是 SeqCst，因此 Release/Acquire 是
它必然满足的推理子集。

**心智模型**：可以把 Release 写想象成集装箱封箱、Acquire 读想象成拆箱验货——
封箱前的货物必然在箱内。但和集装箱不同的是，SeqCst 还额外要求所有集装箱
过同一条传送带：出闸/进闸语义本身并不提供这条传送带。

## When to Use

什么时候按 Release/Acquire 口径推理：

- 发布不可变对象：先填字段再 `atomic.Pointer.Store`，读者 `Load` 后直接解引用
- 实现锁：CAS 加锁即 acquire，Store 解锁即 release，语义刚好闭环
- 读标准库与 runtime 源码：注释里的 release/acquire 对应这套推理

什么时候不够：

- 多个标志位需要"要么全看见、要么全看不见"的组合判断，或无锁队列这类多指针联动的结构——A/R 不提供全序，推理极易漏掉一个方向

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| atomic（Go API） | 全 SeqCst，无参数可选 | 绝大多数场景；语义只会更强 |
| channel | 附带调度与队列语义 | 需要阻塞、传递所有权时 |
| 互斥锁 | 保护一段临界区而非单个值 | 不变量跨多个变量时 |

## Quick Start

前置条件：Go 1.19 及以上（用到 `atomic.Pointer[T]`），纯标准库。

```bash
go run main.go
```

一次真实输出（节选）：

```text
== 第 2 步：发布对象——Release 写 + Acquire 读（推理口径） ==
发布前 nil 观测：13244 次（正常：指针尚未发布）
字段签名不一致：0 次（固定，0 才是验收线）

== 第 3 步：flag 消息传递——Release/Acquire 配对 ==
异常（flag 新 data 旧）：0（固定，0 才是验收线）

== 第 4 步：store buffer litmus——只有 SeqCst 能封住 ==
双重旧值轮数：0 / 100000 轮（固定，0；A/R 语义下本允许失败，见 README）

== 第 5 步：CAS 自旋锁——锁的最小实现 ==
4 goroutine 各加 10000：n = 40000（固定，必须恰为 40000）

== 第 6 步：无争用粗测——自旋锁 vs sync.Mutex（每次不同） ==
SpinLock 无争用 200000 次：1.013ms
sync.Mutex 无争用 200000 次：955µs（每次不同，只看量级）
```

诚实预期：第 2 步的 nil 计数每次运行不同（读者起跑早于发布方属正常）；第 6 步
两个数字随机器负载浮动。`go test -race -count=100 ./02_atomic_orders/` 零报告
才是验收线。

`go doc sync/atomic` 列出的原子类型清单（1.26，节选）：

```text
type Bool struct{ ... }
type Int32 struct{ ... }
type Int64 struct{ ... }
type Pointer[T any] struct{ ... }
type Uint32 struct{ ... }
type Uint64 struct{ ... }
type Uintptr struct{ ... }
type Value struct{ ... }
```

这些类型是 1.19 起提供的强类型封装，内部对应同名的包级函数（`LoadInt32`
等一族）。全部类型与函数共享同一条规则：无内存序参数，SeqCst。

## How It Works

发布对象的代码是 Release/Acquire 配对最典型的样子：

```go
p.Store(&[2]int32{v, ^v}) // release：两个字段的写随指针一起发布
ps := p.Load()            // acquire：拿到非 nil 指针则字段写入全部可见
if ps[0]^ps[1] != ^int32(0) { ... } // 签名校验：被破坏即语义失效
```

CAS 自旋锁把这层配对收敛成两条语句，这就是"Release/Acquire 足够建锁"：

```go
func (l *SpinLock) Lock() {
	for !l.v.CompareAndSwap(0, 1) { runtime.Gosched() } // CAS 成功即 acquire
}
func (l *SpinLock) Unlock() { l.v.Store(0) } // Store(0) 即 release
```

**为什么 Store/Load 默认语义也能过**：Go 的 atomic 全部是 SeqCst，它包含
Release/Acquire 配对语义，再附加全局时间线，按 A/R 推理成立的结论只会更严。

两个层级的分界由第 4 步划出：SB litmus 的"同一轮双方都读到对方旧值"在
纯 A/R 语义下被允许（不存在全局时间线去否定它），SeqCst 则把它封死。

基准实测（`go test -bench . -run '^$' -benchtime=1s -count=3` 取中位数）：

| 基准 | ns/op | 说明 |
|---|---|---|
| SpinLock 无争用 | 4.60 | CAS + Store 各一次 |
| Mutex 无争用 | 4.19 | 快路径两条原子指令，略胜 |
| SpinLock 有争用 | 14.66 | 18 P 争用下无睡眠开销 |
| Mutex 有争用 | 57.25 | 饥饿模式交接 sema，单次更贵 |

## Pitfalls & Q&A

**坑 1：把"发布前读到 nil"当成违例。** 订阅方起跑早于发布方时 `Load` 返回
nil 属正常现象，它不是语义失败。判定违例的只能是"非 nil 但字段不完整"，
所以载荷要带 `{v, ^v}` 这类自校验签名，单纯 `a==b` 检查发现不了陈旧字段。

**坑 2：以为 SeqCst 能让普通写也变有序。** SeqCst 只约束原子操作之间的顺序；
`data` 若是普通变量，它的可见性来自"先于 release 写"这条推理链，不能指望
"后面的原子操作"把普通写托付出去。

**坑 3：自旋锁无争用输给 Mutex。** 实测 4.60 对 4.19 ns/op——CAS 循环
（arm64 上的 LDAXR/STLXR 重试）单次成本高于 Mutex 快路径的两条普通原子
指令。自旋锁的价值在有争用且临界区极短的场合，无争用它没有优势。

**Q：Go 为什么不提供弱内存序参数？** 官方在内存模型文档中的立场是：弱序
API 的正确使用率太低，误用成本（只在某个架构上偶发的错误）远高于收益。
需要更细粒度控制时，正确路径是换算法而不是换内存序。
