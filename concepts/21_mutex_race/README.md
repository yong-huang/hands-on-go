# 21 · 竞态与锁：`counter++` 丢的不是一次加法，是整个更新

> 100 个 goroutine 给同一个计数器各加 1，答案"显然"是 100——但 `counter++` 不是原子操作，无锁并发跑出来的结果往往小于 100，而且每次运行都不同。这就是数据竞态（data race）导致的丢失更新（两次加法只剩一次生效的并发错误）。读完本篇你将看到这个 bug 的真实复现与 `-race` 检测报告，掌握 Mutex 与 atomic 两条修复路径的选择依据，以及典型坑的成因与解法。

## Background

单线程世界里，`counter++` 从不出错。一旦多个 goroutine 并发修改同一个变量，这行代码就会丢更新：两个 goroutine 都执行了加法，计数器却只多 1。

这类 bug 的阴险之处在于：本地小并发测试经常"碰巧通过"，上了生产高并发才偶发爆雷，且无法稳定复现。"我这没复现"不能作为没有并发 bug 的证据。

为此 Go 提供了三件武器：一条运行命令把竞态暴露出来，一种锁把冲突的代码段串行化，一组硬件级原子操作消灭中间状态。本实验把三者全部实测：无锁 / `sync.Mutex` / `sync/atomic` 三方对比。

## What

数据竞态（data race）是一种并发访问错误：两个 goroutine 同时访问同一变量、至少一个是写、且二者之间没有同步措施。一旦三条同时成立就是竞态，与结果"看起来对不对"无关。

可以把 `sync.Mutex`（互斥锁）想象成卫生间门上的锁：一次只放一个人进去，后来者在门外排队。但和卫生间门锁不同的是，Mutex 不强制任何人先锁门再进入——哪段代码要进临界区（同一时刻只允许一个 goroutine 执行的代码区间）全靠约定，忘记 Lock 的代码照样能改共享变量。

**心智模型一句话**：无锁竞争时，多个 goroutine 的"读改写"交错执行、写回互相覆盖，计数器停在 96；加锁串行化后，Mutex 一次只放一个 goroutine 进临界区，结果是精确的 100；而 atomic（`sync/atomic` 包的硬件原子指令）用单条 CPU 指令做到同样的事且更轻。

## When to Use

- 写共享计数器、统计指标时：多个 goroutine 更新同一个整数（请求计数、命中数），用 atomic 最轻；用 Mutex 也正确。
- 保护一组相关数据时：多个字段或 map 需要整体保持一致（比如"转账要同时改两个余额"），各字段分别原子并不能保证整体一致，这时用 Mutex 把整段操作包进临界区。
- 上线任何并发程序前：把测试跑在 `-race` 下做一次竞态检测，是成本最低的保险。

何时不用锁：

- 能不共享就不共享：把数据通过 channel 交给唯一的持有者处理，是更符合 Go 哲学的方式——不共享内存，用通信传递数据
- 只读的并发访问没有竞态，不需要任何同步
- 单个 goroutine 内部的局部变量天然隔离，加锁纯属开销

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `sync.Mutex` | 临界区串行化，通用但排队有开销 | 保护一组变量的整体一致性 |
| `sync/atomic` | 单条硬件原子指令，无锁更轻 | 单个整数/标志位的简单运算 |
| channel 传数据 | 不共享内存，数据只有唯一持有者 | 数据可以在 goroutine 间移交时 |
| `-race` 检测器 | 只检测不修复，运行时开销约 5~10 倍 | 测试与排查阶段始终开启 |

## Quick Start

前置条件：已安装 Go 并可用 `go run`，无第三方依赖。

```bash
cd concepts/21_mutex_race
go run main.go          # 普通运行：三方对比
go run -race main.go    # 竞态检测器：对无锁版本报 DATA RACE
go run main.go -json    # 输出三方计数的 JSON（供 viz.py 画图）
```

真实输出节选（**注意：无锁结果每次运行都不同**，下面 96 与"丢失 4 次"只是本机某一次的实测，你跑出来大概率是另一个数）：

```text
========== 100 个 goroutine 并发加 1，期望值 100 ==========
① 无锁 counter++      结果 =  96  丢失更新 4 次（每次运行都不同！）
② sync.Mutex 加锁     结果 = 100  精确
③ atomic.AddInt64     结果 = 100  精确
```

`go run -race main.go` 的真实运行节选（检测器对 main.go:34 的 `counter++` 报出读-写冲突，一次运行报告了多组竞态，节选其一）：

```text
==================
WARNING: DATA RACE
Read at 0x00c000012168 by goroutine 7:
  main.incUnlocked.func1()
      .../concepts/21_mutex_race/main.go:34 +0x68

Previous write at 0x00c000012168 by goroutine 8:
  main.incUnlocked.func1()
      .../concepts/21_mutex_race/main.go:34 +0x78
==================
```

无锁版本的最小实现，这段在做什么：起 100 个 goroutine 各做一次 `counter++`，不施加任何同步：

```go
// incUnlocked：无锁并发 counter++ —— 演示丢失更新
func incUnlocked(wg *sync.WaitGroup) (final int64) {
	var counter int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter++ // 编译后是 读→加1→写回 三条指令，交错执行会覆盖彼此的写入
		}()
	}
	wg.Wait()
	return counter
}
```

race 报告里 `main.go:34` 的 Read 与 Previous write，指向的正是上面那行 `counter++`——同一行代码既被读又被写地并发执行。

## How It Works

### 竞态：三步指令被调度打断

`counter++` 编译后是三条指令：读内存到寄存器、寄存器加 1、写回内存。调度器可以在任意两条指令之间切走 goroutine。

两个 goroutine 同时读到 42、各自加 1、先后写回 43——两次加法只剩一次的效果。Quick Start 输出里的"结果 = 96"，就是这类覆盖在一百次加法中发生了 4 次的累计结果。

### 修复一：sync.Mutex 把临界区串行化

这段在做什么：把"读改写"包进 Lock/Unlock 之间，同一时刻只有一个 goroutine 能进入：

```go
// incMutex：sync.Mutex 把"读-改-写"包成临界区，串行化后不再丢更新
func incMutex(wg *sync.WaitGroup) (final int64) {
	var (
		mu      sync.Mutex
		counter int64
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			counter++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return counter
}
```

`mu.Lock()` 到 `mu.Unlock()` 之间的代码是临界区，其余 goroutine 在 Lock 上排队。"读改写"不再交错，结果精确为 100。代价有二：排队带来的串行化开销；以及锁靠约定不靠强制——某条路径忘记加锁，那条路径等于没受保护。

### 修复二：atomic 用硬件原子指令

这段在做什么：用一条原子加指令完成读改写，不需要锁：

```go
// incAtomic：atomic.AddInt64 用一条硬件原子指令完成读改写，无需加锁
func incAtomic(wg *sync.WaitGroup) (final int64) {
	var counter int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt64(&counter, 1)
		}()
	}
	wg.Wait()
	return counter
}
```

`atomic.AddInt64(&counter, 1)` 把"读改写"折叠成一条 CPU 原子指令（arm64/x64 的 LOCK 前缀或 LL/SC），中间态根本不存在，不需要排队。对单个整数的计数场景，atomic 比 Mutex 更轻；但它只适合孤立变量的简单运算，多个变量要保持一致还得靠锁。

### 50 轮实测：丢失是概率性的

viz.py 真实运行 `go run main.go -json` 50 轮，取三方各自的最小值作图。无锁版本的丢失是概率性的——单次运行可能碰巧得到 100，50 轮里实测最低 92（丢失 8 次）、最高 100（碰巧没丢）；Mutex 与 atomic 在全部 50 轮中始终精确为 100。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `counter++` | 读→加1→写回，三条指令 | 不是原子操作，并发写就是数据竞态 |
| `var mu sync.Mutex` | 互斥锁（值类型，勿拷贝） | `Lock/Unlock` 之间的代码串行执行 |
| `mu.Lock(); defer mu.Unlock()` | 加锁 + 延迟解锁 | 防止临界区提前 return / panic 忘解锁 |
| `atomic.AddInt64(&c, 1)` | 原子加，返回新值 | 单条指令完成读改写，无锁 |
| `go run -race` | 竞态检测器 | 发现"同时访问、一写、无同步"，运行时开销约 5~10 倍 |

## Pitfalls & Q&A

踩坑清单，每条按"现象 → 原因 → 解法"展开：

- **无锁结果每次运行都不同**。现象：本地跑出 100，测试环境跑出 97。原因：丢失更新是概率性的，取决于调度时机。解法："没复现"不等于没有竞态，用 `-race` 说话。
- **`-race` 只报"被真正触发的竞态"**。现象：加了 race 检测却没报警，上线后仍出事。原因：没跑到的代码路径不会报。解法：测试里要用真并发把可疑路径跑到。
- **锁的粒度靠约定**。现象：某条路径漏加锁，数据照样被写坏。原因：锁住的是"代码区间"不是"变量"，保护哪些全靠团队约定。解法：把共享变量的访问收敛到少数几个带锁的方法。
- **`sync.Mutex` 是值类型，拷贝一个已使用的锁是 bug**。现象：`go vet` 报 copylocks。原因：锁的内部状态（是否有等待者）随拷贝被复制。解法：锁随结构体使用时传指针，用 `go vet` 把关。
- **atomic 不能保护"一组变量"**。现象：各字段分别原子，整体仍不一致。原因：atomic 只保证单个操作的原子性，不保证多个变量之间的顺序。解法：需要一致性时用锁或 channel。

**Q：为什么本地测试经常"碰巧通过"，上了生产才出事？**

A：竞态是否表现取决于调度时机。goroutine 少、核少、执行快时，交错往往来不及发生；生产环境高并发、核多、持锁时间长，交错概率骤增。所以竞态是"概率性 bug"，复现不能靠多跑几次，要靠 `-race` 的 happens-before 分析。

**Q：`-race` 是怎么发现竞态的？它靠运行结果判断吗？**

A：不靠结果。它基于 happens-before（"某操作必然先于另一操作"的偏序关系）：记录每次内存访问的时钟向量，发现两次访问没有先后顺序约束、且一读一写就报 WARNING——哪怕最终数值恰好是 100。所以 `-race` 报了就是真竞态，与本次运行结果无关。

**Q：Mutex 和 atomic 怎么选？**

A：保护单个计数器/标志位 → atomic（`atomic.Int64`、`CompareAndSwap`）；保护"一组变量的一致性"或复杂不变量 → Mutex。Mutex 更通用但有排队开销与忘加锁风险；atomic 更快但表达能力有限。拿不准时用锁，profiling 证明热点后再换 atomic。

**Q：`go run -race` 能放进 `go run main.go -json` 的数据里吗？**

A：不能，也无需。race 检测器的输出走 stderr、且开销大，会把时序搅乱；-json 数据只采集普通运行的三方计数。本实验里 race 输出只用于如实记录与演示，两者分离。

**Q：除了加锁和原子指令，还有第三条路吗？**

A：有——不共享内存。把共享变量收归唯一一个 goroutine 所有，其他 goroutine 通过 channel 把"修改请求"发给它。数据只有一个持有者，竞态无从谈起，这也是 Go 并发哲学更倾向的做法。
