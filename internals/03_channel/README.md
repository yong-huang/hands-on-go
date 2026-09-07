# 03 · Channel 底层实现：带锁环形队列 hchan

> channel 是 Go 协程通信的核心原语，但它的底层 hchan 不在公开 API 里，字段布局随版本
> 演进（Go 1.24 起运行时大规模重构），`unsafe` 硬读极易读到垃圾字段。好在 hchan 的核心
> 设计十几年没变过：一个带锁的环形队列。本实验用纯 Go 结构复刻它，把 sendx/recvx 两个
> 游标的每一步推进、阻塞与回绕都打印出来，最后用真实 channel 对照验证。

## 1. 为什么需要它

"向已关闭的 channel 发送会怎样"、"无缓冲 channel 和带缓冲差在哪"——这些问题的答案，
都能从"环形队列 + 两个游标"推出来。外部观察 channel 的官方入口只有 `len()` 和
`cap()`，分别对应 hchan 的 `qcount` / `dataqsiz`；更深的内部状态读不到，所以本实验
按简化后的 hchan 结构用纯 Go 模拟，再在第 6 节用真实 channel 交叉验证行为一致。

## 2. 总览：核心机制一图看懂

![hchan 环形队列状态机：sendx 填、recvx 取、满了阻塞、回绕复用](images/channel_ring.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/03_channel/images/channel_ring.html)
> （或本地打开 [`images/channel_ring.html`](images/channel_ring.html)）。

**心智模型一句话**：channel 就是一个环形缓冲区加两个只增不回绕的游标——发送写
`sendx & (cap-1)` 槽后 sendx++，接收读 `recvx & (cap-1)` 槽后 recvx++；写满了发送方
阻塞，读空了接收方阻塞，cap=0 则干脆没有环、收发直接交接。状态机每条边都标注了实测
的 qcount/sendx/recvx 变化。

## 3. 快速开始

```bash
cd internals/03_channel
go run main.go          # 文本演示：环形队列 → 阻塞 → 回绕 → close → 无缓冲 → 真实对照
go run main.go -json    # 输出 7 个时刻的环状态快照（JSON，供程序化读取）
```

预期输出（节选）：

```text
新建 cap=4 的空 channel：qcount=0 sendx=0 recvx=0
  发送 40   → 写入环槽3，qcount=4  sendx=4↗
  发送 50   → 环满，发送方阻塞（等待接收方腾出槽位）
  接收  → 读环槽0 得到 10，qcount=3  recvx=1↗
  发送 500  → 写入环槽0，qcount=3  sendx=5↗
  关闭 channel → closed=true；buf 里残留的数据仍可读出
  真实 buffered chan: len=2 cap=2（缓冲区已满）
```

每一步"在看什么"都写在 `main.go` 的注释里；最后用真实的 `make(chan int, 2)` 和无缓冲
channel 做对照实验，验证模拟与 runtime 行为一致。

## 4. 核心概念

### 4.1 hchan 结构与环形队列

```go
type hchan struct {
    qcount   uint  // 队列中元素个数（对应 len(ch)）
    dataqsiz uint  // 环形队列容量（对应 cap(ch)）
    buf      []int // 环形队列底层数组（简化：元素为 int）
    sendx    uint  // 发送游标：下一个写入的位置（只增不回绕）
    recvx    uint  // 接收游标：下一个读取的位置（只增不回绕）
    closed   bool  // 是否已 close
    blocked  uint  // 被阻塞方数量（简化"等待队列"）
}
```

真实 runtime 还有 `recvq`/`sendq` 两个等待队列和一把 `lock`，模拟版用 `blocked` 计数
简化；语义不变：阻塞的 goroutine 挂进队列，被对端唤醒。

### 4.2 sendx/recvx 推进与掩码回绕

```go
func (ch *hchan) mask(n uint) uint { return n & (ch.dataqsiz - 1) }
```

游标只增不回绕，落槽用 `&(size-1)` 掩码。演示对 cap=4 的环发送 {10,20,30,40} 填满
（sendx=4），接收 2 个腾出槽 0/1 后再发 500/600：sendx 4→5→6，掩码后正好落回槽 0/槽 1
——槽位被循环复用，qcount 回到 4。

### 4.3 满则阻塞、空则阻塞与直接交接

环满时发送方阻塞（快照里 blocked=1，值暂存 pending）；环空时接收方阻塞。但若队空却
已有等待方，则直接交接、不占环。无缓冲 channel（cap=0）没有环：send(42) 因无接收方
阻塞，recv 到来时数据经 pending 直接递过去（rendezvous），sendx/recvx 永远为 0。

### 4.4 close 之后的语义

close 置位后：接收先把 buf 里的残留数据读完（30、40、500、600 依次读出，recvx 追到
6），读空后再接收返回零值 `ok=false`；再发送直接 panic。演示特意按"close → 读光残留 →
空收 → send"分步执行，避免"已关闭"短路吞掉残留数据。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `qcount` | 队列中元素数 | `len(ch)` 的来源，收发各增减 1 |
| `dataqsiz` | 环容量 | `cap(ch)` 的来源；满/空判定的基准 |
| `sendx` / `recvx` | 写/读游标 | 只增不回绕，落槽靠掩码 |
| `& (dataqsiz-1)` | 掩码映射 | 游标 → 环下标；容量是 2 的幂才成立 |
| `blocked` / 等待队列 | 被阻塞方 | 阻塞语义与唤醒的载体 |

坑清单：

- hchan 不在公开 API 里且字段随版本漂移：学习内部状态只能用纯 Go 结构模拟，`unsafe`
  硬读既 Flaky 又不稳定
- sendx/recvx 是单调递增游标：直接当数组下标会越界，必须过 `&(dataqsiz-1)` 掩码
- close 的接收语义有顺序：先读光残留，再判"已关闭且空 → 零值"，写反了残留数据会被
  短路吞掉
- `len(ch)` 只是某一瞬间的 qcount：并发场景下读到的值随时可能过期，不能用它做同步

## 6. 文件结构

```text
internals/03_channel/
├── README.md                            # 本文件：原理 + 运行指引 + 面试要点
├── main.go                              # 主教材：hchan 收发/阻塞/回绕演示（-json 快照）
└── images/
    ├── channel_ring.lifecycle.json      # 图源：hchan 环形队列状态机（Typed JSON IR）
    ├── channel_ring.html                # 交互版：可缩放/高亮连线的自包含页面
    └── channel_ring.svg                 # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：channel 底层是什么数据结构？**
A：带锁的环形队列（hchan）：buf 环形数组 + qcount/dataqsiz + sendx/recvx 两个游标 +
收发等待队列和一把互斥锁。`len()`/`cap()` 分别对应 qcount/dataqsiz。

**Q：sendx/recvx 会不会回绕或溢出？**
A：它们只递增、不回绕，落槽时用 `& (cap-1)` 掩码映射到环上（要求容量是 2 的幂）。
游标本身是 uint，工程生命周期内不会溢出。

**Q：向 nil channel、已关闭的 channel 收发分别会怎样？**
A：nil channel 收发永久阻塞；已关闭的 channel 接收先读光残留数据再返回零值
`ok=false`，发送直接 panic，close 已关闭的 channel 也 panic。

**Q：无缓冲 channel 的发送何时返回？**
A：必须有接收方就绪才直接交接（rendezvous），数据不经过缓冲；它保证的是"传递完成"
这一同步语义，而带缓冲 channel 只保证"已放入队列"。

**Q：什么情况下发送方会阻塞？**
A：带缓冲环满时进 sendq 等待接收方腾位；无缓冲且无接收方就绪时同样阻塞。接收方对称：
环空且无发送方等待时进 recvq。

## 8. 总结

channel 的全部行为都能从"环形队列 + 两个只增游标 + 掩码回绕"推出来：收发是游标推进，
阻塞是等待队列，close 是先读光再零值。记住验证方法——`go run main.go` 把每次写入/
读取的槽位与游标值都打印出来，最后还有真实 channel 对照，比背结论可靠得多。

下一篇 [04_string](../04_string/) 回到最熟悉的数据类型：string 的两字段只读头，以及
它和 `[]byte` 之间每一次转换背后的那次拷贝。
