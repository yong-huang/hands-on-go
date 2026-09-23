# 06 · Ring Buffer：固定数组 + head/tail 取模回绕

> 环形缓冲区（Ring Buffer / 循环队列）是最经典的数据结构之一：用**固定大小数组**和 **head/tail 两个指针**实现 FIFO 队列，指针走到数组末尾就取模回绕到开头，复用已读出的空间。核心优势是**无需动态分配、无数据搬移、读写均 O(1)**——Go channel 的底层缓冲正是它。本实验用一个可运行的 Go 实现把回绕、判满、判空变成看得见的指针数值。

## 1. 为什么需要它

用普通切片做队列，读出头部元素后要么 `s = s[1:]`（底层数组越切越长，前面的内存浪费掉），要么搬移剩余元素（O(n)）。环形缓冲区的答案：指针前进改为**取模回绕** `tail = (tail + 1) % cap`，读写都在同一个固定数组里转圈，两个操作都严格 O(1)。

它广泛存在于：操作系统内核缓冲区、音视频流处理、日志缓冲、生产者-消费者模型，以及 Go `runtime.hchan` 的 `buf` 字段。实现难点集中在三处：**满/空判别、回绕取模、手写时的边界错误**。

## 2. 总览：核心机制一图看懂

![Ring Buffer 架构：固定数组 + head/tail 取模回绕](images/ring.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/06_ring_buffer/images/ring.html)
> （或本地打开 [`images/ring.html`](images/ring.html)）。

**心智模型一句话**：head 指向下一个**读出**的位置，tail 指向下一个**写入**的位置，两者都用 `% cap` 回绕；`count` 字段负责区分"满"和"空"——否则 `head == tail` 时两种状态无法区分。图中满状态（cap=5）下 `head=2 · tail=2 · len=5`，物理序 `[60 70 30 40 50]` 正是回绕后逻辑序 `30 40 50 60 70`。

## 3. 快速开始

```bash
cd patterns/06_ring_buffer
go run ring_buffer.go
```

预期输出（节选，与实际运行一致）：

```text
创建 RingBuffer(cap=5)

1. 写入 3 个元素:
  Write(10) → true  buf=[ 10 _ _ _ _ ]  head=0  tail=1  len=1  cap=5
  Write(30) → true  buf=[ 10 20 30 _ _ ]  head=0  tail=3  len=3  cap=5
  ...

3. 继续写入（测试 head/tail 回绕）:
  Write(40) → true  buf=[ 30 40 _ _ _ ]  head=2  tail=4  len=2  cap=5
  Write(50) → true  buf=[ 30 40 50 _ _ ]  head=2  tail=0  len=3  cap=5
  ...
  Write(70) → true  buf=[ 30 40 50 60 70 ]  head=2  tail=2  len=5  cap=5

4. 溢出处理:
  Write(99) → err: ring buffer is full

6. FIFO 顺序验证:
  写入: 100, 200, 300
  读取: 100 200 300
  FIFO 顺序正确!
```

三个关键观察：`Write(50)` 后 **tail 从 4 回绕到 0**；写满时 `head == tail == 2` 却是"满"；满后再写指针**一动不动**，返回 `ErrFull`。

## 4. 核心概念

### 4.1 结构定义

```go
type RingBuffer struct {
    buf   []int // 底层数组（固定大小，创建后不变）
    head  int   // 下一个读出的位置
    tail  int   // 下一个写入的位置
    count int   // 当前元素数量
    cap   int   // 最大容量
}
```

`count` 是本实现的关键设计：只用 head/tail 无法区分满和空，加一个计数器是最直接的方案。

### 4.2 Write：先判满，写 tail，取模回绕

```go
func (rb *RingBuffer) Write(val int) error {
    if rb.IsFull() { // count == cap
        return ErrFull
    }
    rb.buf[rb.tail] = val
    rb.tail = (rb.tail + 1) % rb.cap // 回绕
    rb.count++
    return nil
}
```

本实现是**拒绝语义**：满时返回 `ErrFull`，不覆盖旧数据。另一种常见的**覆盖语义**变体：满时仍写 `buf[tail]` 并同步前移 `head`，旧数据被挤掉——适合日志环、指标采样等"只关心最近 N 条"的场景。

### 4.3 Read：先判空，读 head，取模回绕

```go
func (rb *RingBuffer) Read() (int, bool) {
    if rb.IsEmpty() { // count == 0
        return 0, false
    }
    val := rb.buf[rb.head]
    rb.head = (rb.head + 1) % rb.cap // 回绕
    rb.count--
    return val, true
}
```

`Show()` 按逻辑序打印：第 i 个元素是 `buf[(head+i)%cap]`——所以满状态打印 `[30 40 50 60 70]`，而物理数组里是 `idx0=60, idx1=70, idx2=30...`。

### 4.4 与 Go channel 的关系

`chan int` 的缓冲就是 `runtime.hchan.buf` 这个 ring buffer，在此之上加了锁（并发安全）、`sendq`/`recvq` 等待队列（阻塞/唤醒语义）和 select 支持。缓冲 channel 满时发送者阻塞，等价于这里返回 `ErrFull` 的"暂停"变成了挂起。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `head` | 下一个读出的下标 | 回绕后逻辑序 ≠ 物理序 |
| `tail` | 下一个写入的下标 | 满时 `tail == head`，但含义是"写不进" |
| `count` | 当前元素个数 | 判满 `count==cap`、判空 `count==0`，消除歧义 |
| `(tail+1) % cap` | 回绕推进 | 手写时漏 `%` 会越界；漏判满会覆盖未读数据 |

坑清单：

- **满/空歧义**：`head == tail` 既可能是空也可能是满。方案 A 是加 `count` 字段（本实现，多 4/8 字节）；方案 B 是浪费一个槽位——判满 `(tail+1)%cap == head`、判空 `head == tail`，代价是有效容量只剩 cap-1
- **漏取模**：`tail++` 写成不取模，回绕后立即数组越界 panic
- **覆盖与拒绝混淆**：动手前先问清要求哪种语义，再决定满时 return error 还是前移 head
- **cap 为 0**：`NewRingBuffer(0)` 会让 `% 0` panic，工程实现应在前置校验
- **并发安全**：本实现非线程安全；SPSC 场景可用原子操作做 lock-free 变体，MPMC 需要更复杂的方案

## 6. 文件结构

```text
patterns/06_ring_buffer/
├── README.md                     # 本文件：原理 + 运行指引 + 深入要点
├── ring_buffer.go                # 主教材：Write/Read/判满判空 + 回绕/FIFO 演示
└── images/
    ├── ring.architecture.json    # 图源：固定数组 + head/tail 回绕架构（Typed JSON IR）
    ├── ring.html                 # 交互版：可缩放/高亮连线的自包含页面
    └── ring.svg                  # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：如何区分"满"和"空"？**
A：`head == tail` 时两种状态重合，必须破坏对称性。方案 A：加 `count` 字段，`count==cap` 满、`count==0` 空，简单直观；方案 B：浪费一个槽位，判满 `(tail+1)%cap == head`、判空 `head == tail`，省字段但少存一个元素。本实验用方案 A。

**Q：为什么读写都要取模？不取模会怎样？**
A：指针到数组末尾后要回到 0 才能复用已读空间，`(i+1)%cap` 就是回绕。不取模则要么越界 panic，要么被迫整体搬移数据退化成 O(n)。注意 `Show()` 里按逻辑序取元素也要取模：`buf[(head+i)%cap]`。

**Q：满时应该拒绝写入还是覆盖最旧的数据？**
A：看场景。有界队列/背压场景必须拒绝（返回 error 或阻塞），丢数据不可接受；日志环、监控采样等只关心最近 N 条的场景用覆盖语义——写 tail 的同时把 head 前移一格，本质是用 O(1) 换掉最旧元素。手写前先确认语义要求。

**Q：手写 Ring Buffer 最容易错在哪？**
A：四处——判满条件写错（漏掉 head==tail 歧义）、推进指针漏 `% cap`、覆盖语义忘了同步前移 head、cap=0 未校验导致 `% 0` panic。写完用"写满→读空→再写"的回绕用例验证，本实验 demo 第 3/5 步就是这两段。

**Q：Ring Buffer 和 Go channel 什么关系？能做 lock-free 吗？**
A：`runtime.hchan` 的 `buf` 就是 ring buffer，外面套锁 + `sendq`/`recvq` 等待队列。单生产者单消费者（SPSC）可用 CAS+atomic 去掉锁；多生产者/多消费者要保证先占位再写槽的顺序，复杂度高很多，通常直接用 channel。

## 8. 总结

Ring Buffer = 固定数组 + head/tail 取模回绕：读写都严格 O(1)，无分配无搬移；`count` 字段（或浪费一槽）解决满/空歧义；满时拒绝还是覆盖由业务语义决定。运行 `go run ring_buffer.go` 亲眼确认 tail 回绕到 0 和"满时 head==tail"这两幕，比背结论可靠得多。

下一篇 [07_token_bucket](../07_token_bucket/) 看另一个经典组件：令牌桶限流器如何用"恒速补充 + 容量上限"刻画流量整形。
