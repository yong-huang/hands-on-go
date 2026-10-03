# 01 · happens-before 探针：用 litmus 测试实证重排

> 不加任何同步时，另一个 goroutine 看到的写入顺序没有任何承诺。本篇用两个最小的双 goroutine 程序（litmus 测试）实证"重排可以被观测"，再分别用 channel 与 atomic 修复，把 Go 内存模型承诺的五类 happens-before 边逐条对号入座。

## Background

在内存模型成为共识之前，并发代码的正确性靠"在我机器上没出过问题"背书。

常见的土办法是给共享变量加锁，或在两个 goroutine 之间插一个 `time.Sleep`，
"睡一毫秒总够了吧"。这类做法在低负载机器上长期有效，直到换 CPU 架构、
赶上一次激进的编译优化，写入顺序的幻觉才会碎掉。

痛点在于：源代码里的两行写入，既不等于编译器生成的指令顺序，也不等于其他
核心观察到这两条写生效的顺序。CPU 会把写先攒进写缓冲（store buffer，核心
在写真正落到缓存前暂存写的硬件队列），编译器也会交换没有依赖的访存指令。

Go 内存模型（go.dev/ref/mem）因此给出可推理的承诺：哪些操作组合保证"A 的
效果对 B 可见"。承诺的载体就是 happens-before 边。

## What

**定义**：happens-before（先于发生）是并发事件间的一种偏序关系。若 A
happens-before B，则 A 的写入对 B 可见，且编译器和 CPU 都不允许把 A 的
效果排到 B 之后生效。

litmus 测试（判定内存模型是否允许某个交错结果的最小多线程程序）是验证
承诺的最小实验装置：两三个 goroutine、两三个变量，结果只分"允许/禁止"。

**心智模型**：可以把 happens-before 想象成快递签收单——寄件方（写方）交出
包裹后，签收记录保证收件方（读方）拿到的至少是这一件。但和快递不同的是，
没有签收单时读方拿到的可能是货架上的旧包裹，并且系统不会给出任何提醒，
错误观测不会报错，只会让结果悄悄不对。

## When to Use

什么时候需要 happens-before 推理：

- 判断一段"没加锁但看起来没问题"的并发代码是真没问题，还是竞态恰好没发作
- 在性能敏感路径上用 atomic 或 channel 建立最小同步，而不是整个临界区加锁
- 读写 runtime 与 sync 标准库源码时，确认某个无锁数据结构的可见性论证

什么时候不用：

- 单 goroutine 内的代码不涉及任何边，顺序就是源码顺序
- 已有明确锁保护的临界区内部，锁本身已提供全部需要的边

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 推理 happens-before | 把可见性变成可判定的偏序推理 | 无锁/细粒度同步的正确性论证 |
| 全部加锁 | 正确但掩盖了同步的真实粒度 | 逻辑复杂、争用低的常规代码 |
| 睡眠等待 | 依赖调度时机，无任何承诺 | 永远不应用于正确性 |

## Quick Start

前置条件：本机安装 Go 1.19 及以上即可，纯标准库。

```bash
go run .       # 观察重排被看见的异常计数
go run -race . # 第 2、3 步会打印 WARNING: DATA RACE（预期行为）
```

命令用包模式（`go run .`）而非 `go run main.go`：raceEnabled 常量按 build tag
拆在 race_on.go / race_off.go 里，单文件运行会报 undefined。

`go run main.go` 的一次真实输出：

```text
== 第 1 步：三个 litmus 测试在测什么 ==
MP（message passing，消息传递）：T1 写 data 再写 flag；T2 读到 flag 新值后读 data。
SB（store buffer，写缓冲）    ：两个 goroutine 各写各的 flag，再读对方的 flag。
问的问题只有一个：这两段程序允许出现"违背写入顺序的观测"吗？

== 第 2 步：MP 无同步版——不建立任何 happens-before 边 ==
异常（看到 flag 新值但 data 旧值）：27 次（每次不同）

== 第 3 步：SB 无同步版——双方各写各的 flag，再读对方的 ==
双重旧值轮数：0 / 300000 轮（每次不同，可能为 0）

== 第 4 步：MP channel 修复版——发送/接收建立 happens-before 边 ==
异常：0 次（固定，0 才是验收线）

== 第 5 步：MP atomic 修复版——atomic 写/读配对建立边 ==
异常：0 次（固定，0 才是验收线）

（本机 18 个逻辑核；go run -race . 时第 2、3 步会报 DATA RACE）
```

诚实预期：第 2 步的异常数每次运行不同（实测个位数到几十，偶尔为 0；
为 0 不代表没有重排，只是没被采样到）。第 3 步在本机多次运行均为 0，
原因见 Pitfalls。第 4、5 步的 0 是固定结果，`go test -race -count=100
./01_hb_litmus/` 全绿是验收线。

## How It Works

修复版的核心代码只有几行。channel 版用发送/接收托付写：

```go
data = v  // 写 data
flag <- v // 发送：把它之前的写"托付"给接收方
// 接收方：
v := <-flag // 接收完成 => data=v 的写先于接下来对 data 的读
```

atomic 版把"托付"换成读写配对：reader 先 atomic 读 flag、后 atomic 读
data，当 flag 的读观测到 flag 的写时，那次写同步先于这次读。

Go 内存模型承诺的五类 happens-before 边，与本项目各步的对应关系：

| 边 | 承诺内容 | 救了哪个 litmus |
|---|---|---|
| 1. 程序顺序 | 单 goroutine 内按源码顺序对自身可见 | 一切前提：writer 内 data 先于 flag |
| 2. go 语句 | go 语句同步先于新 goroutine 开始 | TestGoStatementEdge：语句前写直接可见 |
| 3. channel | 发送（或 close）同步先于对应接收完成 | 第 4 步 channel 修复版 |
| 4. 锁与 Once | Unlock 同步先于后续 Lock 的返回 | 本项目未用到；复刻其实现见 05_mini_mutex |
| 5. atomic | atomic 读观测到 atomic 写的效果时，写同步先于读 | 第 5 步 atomic 修复版 |

无同步版之所以失败：writer 的两次写之间只有边 1（对 writer 自己成立），
reader 与 writer 之间没有任何边，于是边 1 的承诺不被传递，重排被看见。

## Pitfalls & Q&A

**坑 1：channel 修复版第一稿被 -race 抓了真 bug。** writer 写完第 i 轮不等
读者 ack 就写第 i+1 轮的 data，"写 data(i+1)"与"读 data(i)"之间没有任何边。
解法是加 ack 通道，让每轮读写严格交替。可见：修 litmus 不等于消灭全部竞态。

**坑 2：SB litmus 双重旧值在本机恒为 0。** Apple Silicon 的性能核行为接近
TSO（Total Store Order，x86 采用的"写不乱序、读不乱序"的弱化模型），
300000 轮采样不到重排。

这不是 SB 被封住了——它依赖的是顺序一致语义，
由 TestStoreBufferAtomicSeqCstFixed 在语义层验证。

**坑 3：无同步版本不能用 -race 验证"重排可观测"。** -race 给每次内存访问
插桩并串行化判定，插桩后的访存不再体现硬件重排。所以重排证据用普通构建
采集（reorder_norace_test.go 只在非 -race 下编译），竞态证据才交给 -race。

**Q：goroutine 结束是不是一条边？** 不是。内存模型明确写了"goroutine 的
退出不与任何事件同步"。等一个 goroutine 结束必须用 WaitGroup、channel 等
显式同步，`time.Sleep` 不构成任何边。
