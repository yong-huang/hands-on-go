# 06 · mini RWMutex：一个负数把"读者计数"与"写者已到"装进同一个 int32

> 本篇徒手复刻 `sync.RWMutex` 的核心协议：readerCount 负数协议。读完本篇你能自己实现一个读写锁（一种允许多个读者同时进入、写者独占的锁），用 `-race` 压测验证"多读并行、写者独占、写者优先"，并精确解释递归 RLock 死锁的成因。

## Background

没有读写锁之前，保护一份读多写少的数据只有一个工具：互斥锁。哪怕 100 个 goroutine 都只是读一份几乎不变的配置，也必须一个一个排队进出临界区（同一时刻只允许一个 goroutine 执行的代码段）。

痛点在读多写少的场景撞墙：读者之间本互不干扰，排队却把并行度压成 1。某个热点缓存每秒被读 10 万次、每分钟才写一次，互斥锁让 10 万次读串行化，锁排队成了瓶颈本身。

读写锁因此出现：读者共享进入，写者独占。难点在于协调——写者必须等"已在场的读者"全部离开，又要挡住后来者，否则读者川流不息、写者永远等不到空场。Go 标准库的答案是让一个计数器走进负数区间，本篇就把这个协议徒手实现一遍。

## What

mini RWMutex 是一个读写锁：任意多个读者可同时持有，写者独占。它由五个部件协作：

| 部件 | 类型 | 职责 |
|---|---|---|
| `readerCount` | `atomic.Int32` | `>=0` 是活跃读者数；`<0` 表示有写者已到达 |
| `maxReaders` | 常量 `1<<30` | 把 int32 劈成两半的偏移量：写者减它、写者走时加它 |
| `w` | `sync.Mutex` | 写者之间的互斥，多个写者在此排队 |
| `writerSem` | channel 信号量（挂起与唤醒等待者的同步原语） | 写者等"在场读者全部离开"的交接点 |
| `readerSem` | channel 信号量 | 被写者挡住的新读者排队，写者离场时逐个唤醒 |

可以把 `readerCount` 想象成展览馆门口的翻牌计数器：进一个读者加 1，出一个减 1，数字大于零就放行。写者到场时把计数器整个拨到负数区间，相当于门口挂出"写者已预约"的告示，后来的读者看到负数就去休息室排队。

但和翻牌计数器不同的是，它的每一次增减都必须原子完成，且"负"不是真的计数为负，而是减去了 2 的 30 次方的偏移量。

## When to Use

**读远多于写的共享状态**。进程内的配置快照、路由表、功能开关：读取频繁、修改稀少，读锁让读者并行。

**读临界区有一定长度时**。比如读者要遍历一个 map、计算哈希或做几次内存访问，共享读省下的排队时间能盖过读锁自身的开销。

**何时不用**：

- 读临界区极短（比如只读一个 int）：`RLock`/`RUnlock` 两次原子加的开销可能超过数据访问本身，普通 `sync.Mutex` 或 `atomic.Load` 更划算。
- 写同样频繁：写者独占且要让在场读者走完，写延迟反而比普通互斥锁更差。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `sync.Mutex` | 读写一视同仁地串行 | 读写均衡、临界区短 |
| `sync.RWMutex` | 读共享、写独占 | 读远多于写且读临界区不短 |
| `atomic` 变量 | 无锁，只覆盖单个机器字 | 只保护一个整型或指针 |
| 分片加锁 | 按 key 拆成多个锁 | 写也不少、按 key 可 partition 时 |

## Quick Start

需要 go 1.26，无第三方依赖。运行分步演示：

```bash
go run ./06_mini_rwmutex/
```

真实输出（步骤 1 的进入顺序与瞬时计数每次运行都会不同，属正常；步骤 3、4 的负数值是固定的）：

```text
== 步骤 1：并发读：4 个读者同时进入 ==
  读者 4 进入，临界区内读者数=1，readerCount=2
  读者 1 进入，临界区内读者数=2，readerCount=2
  读者 3 进入，临界区内读者数=4，readerCount=3
  读者 2 进入，临界区内读者数=3，readerCount=2
== 步骤 2：独占写：写临界区期间读者进不来 ==
  600 次写临界区，互斥破坏次数=0（诚实预期：恒为 0，任何非 0 都是 bug）
== 步骤 3：写阻塞读：写者到达后，新读者排队 ==
  写者等待中，readerCount=-1073741822（负数=写者已注册）；新读者也未放行
  顺序确认：写者先于新读者拿到锁（写者优先）
== 步骤 4：死锁复现：持读锁 → 写者到 → 同 goroutine 再取读锁 ==
  死锁现场：readerCount=-1073741822 readerWait=1
  写者在 writerSem 等最后一个读者 RUnlock；
  内层 RLock 在 readerSem 等写者 Unlock；外层 RUnlock 永远执行不到——三方互等。
  （本步演示真实死锁，goroutine 无法恢复，进程在此退出）
```

步骤 3 打印的 `-1073741822` 就是协议现场：在场读者 1 个、新读者 1 个，合计 2，再减去 `maxReaders`（1073741824），得到负数。

并发正确性用竞态检测器（`-race`，基于向量时钟的动态检测工具）高压验证：

```bash
go test -race -count=200 ./06_mini_rwmutex/
```

真实输出（本机 go1.26.5 darwin/arm64 实测，零 DATA RACE 报告）：

```text
ok  	concurrency/06_mini_rwmutex	94.292s
```

## How It Works

三条路径就能讲完协议。先是读者的快路径，`readerCount` 加 1 后非负，说明无写者，直接进入：

```go
func (rw *RWMutex) RLock() {
	if rw.readerCount.Add(1) < 0 {
		<-rw.readerSem // 有写者已到达：新读者在此排队，把场地让给写者
	}
}
```

写者的 `Lock` 分三步，每步一个锁点：先在 `w` 上解决写者之间的竞争；再减去 `maxReaders`，把计数压进负数区间，此后所有新读者的 `Add(1)` 结果为负而排队；最后数出减之前的活跃读者数 `r`，在 `writerSem` 上等这批人全部离场：

```go
func (rw *RWMutex) Lock() {
	rw.w.Lock()
	r := rw.readerCount.Add(-maxReaders) + maxReaders // 减偏移：挡新读者；+ 回来：得到在场读者数
	if r != 0 && rw.readerWait.Add(r) != 0 {
		<-rw.writerSem // 等在场读者走完；最后一个走的读者负责唤醒
	}
}
```

离场方向是协议闭环的关键。读者 `RUnlock` 时若发现计数为负，说明有写者在等，自己可能是"最后一个在场读者"，`readerWait` 递减到 0 就向 `writerSem` 交接；写者 `Unlock` 时加回 `maxReaders`，返回值正好是排队读者个数，逐个交接唤醒，最后放行下一个写者：

```go
func (rw *RWMutex) RUnlock() {
	if r := rw.readerCount.Add(-1); r < 0 {
		if rw.readerWait.Add(-1) == 0 {
			rw.writerSem <- struct{}{} // 最后一个在场读者唤醒写者
		}
	}
}

func (rw *RWMutex) Unlock() {
	r := rw.readerCount.Add(maxReaders) // 恢复正数区间，r = 排队读者数
	for i := int32(0); i < r; i++ {
		rw.readerSem <- struct{}{} // 与一个排队读者的 receive 交接，唤醒一个
	}
	rw.w.Unlock()
}
```

把 Quick Start 里看到的两个现象在这里对上：步骤 3 的负数来自 `Lock` 的 `Add(-maxReaders)`；步骤 4 的 `readerWait=1` 说明写者正在等唯一那个"本该离场却卡在内层 RLock"的读者。

与标准库相比，mini 版有三处简化：写者互斥直接借用 `sync.Mutex`（标准库复用其完整状态机，含自旋与饥饿模式）；没有 `TryRLock`/`TryLock`；不检查误用（标准库对未持锁的 `Unlock` 会直接 fatal）。

并发安全没有被简化——channel 收发交接自带 happens-before（内存模型中"操作 A 对操作 B 可见"的偏序关系）边，`-race` 据此验证了全部同步。

## Pitfalls & Q&A

**递归 RLock 死锁**。现象：同一 goroutine 持有读锁后，在写者等待期间再取一次读锁，永久阻塞。原因：

```text
G1 持有读锁（在场读者 = 1）
W  Lock：计数变负，readerWait=1，在 writerSem 等 G1 的 RUnlock
G1 RLock：Add(+1) < 0，在 readerSem 等 W 的 Unlock
→ G1 的 RUnlock 永远执行不到，三方互等
```

协议要求 `RLock` 与 `RUnlock` 严格一一配对，因为 `readerWait` 数的是"在场读者会各走一次"这个有限集合。重入破坏了这个假设，写者就永远数不到 0。这也是文档写明"读锁不可重入"的机制根源，而非保守的设计偏好。

**Q：为什么步骤 3 里新读者被挡住是特性而不是缺陷？** 写者优先。若放行新读者，读者川流不息，`readerWait` 永远减不到 0，写者饿死。负数协议用一次原子减同时完成"挡新"与"数旧"，这是它存在的主因。

**Q：为什么用 channel 信号量而不是自旋等待？** 排队是常态而非例外（写者等待期间所有新读者都要睡），自旋只会烧 CPU。channel 交接一次唤醒一个 goroutine，与标准库 `runtime_Semrelease` 的逐个唤醒语义一致，也让 `-race` 能直接读到同步边。
