# 18 · 生产者-消费者：多生产多消费与两级 WaitGroup 优雅关闭

> 生产者-消费者是并发程序最基本的骨架：生产者只管发，消费者只管收，channel 是两者之间唯一的交接点。人多之后真正的难点不是收发，而是**收尾**——什么时候关 channel、谁来关、怎么让所有 goroutine 都体面退出。本实验用 3 生产者、2 消费者的可运行 demo 把"两级 WaitGroup + 第三方 close"的标准姿势跑给你看。

## 1. 为什么需要它

单个 goroutine 发、单个收的版本随手就能写；一旦生产者变成 3 个、消费者变成 2 个，新问题立刻出现：某个生产者发完了，怎么知道**别的生产者**也发完了？谁有资格 close channel？main 怎么确认 12 个任务一个不少地被处理了？答案是一套关闭顺序的纪律：先等生产者全部 Done，再由"第三方"close，最后等消费者退出。顺序错一步就是 panic 或死锁。

## 2. 总览：核心机制一图看懂

![多生产多消费：两级 WaitGroup 的优雅关闭](images/pc.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/18_producer_consumer/images/pc.html)
> （或本地打开 [`images/pc.html`](images/pc.html)）。

**心智模型一句话**：两级 WaitGroup 各管一边——wgProducers 管发送、wgConsumers 管接收；close 是发送方的"再无发送"宣言，必须先由收尾 goroutine 用 `wgProducers.Wait()` 证明前提成立，再交给消费者的 `for range` 自然终结。

## 3. 快速开始

```bash
cd concepts/18_producer_consumer
go run main.go
```

预期输出（节选。**发送与处理的行序每次运行都不同**——调度器决定谁先跑，下面只是其中一次的真实快照）：

```text
  [C1] 处理任务 100（来自 P1）
[P3] 发送任务 300
  [C1] 处理任务 301（来自 P3）
[P2] 发送任务 201
[P1] 全部发送完毕，退出
--- tasks channel 已关闭（所有生产者都完成了）---
  [C2] channel 已关闭，退出
  [C1] channel 已关闭，退出

对账：发送 12 = 接收 12？true
  P1 发了 4、被收了 4
  P2 发了 4、被收了 4
  P3 发了 4、被收了 4
```

不变量是：12 条发送对应 12 条处理、对账行恒为 `true`、每个生产者的 4 个任务都被收齐、两个消费者都以"channel 已关闭"退出。行序随机恰恰说明了并发的本质。

## 4. 核心概念

### 4.1 两级 WaitGroup：各管一边

`wgProducers.Add(1)` 在启动生产者前调用，生产者 `defer wgProducers.Done()`；`wgConsumers` 同理管理两个消费者。两级分开的意义是**关闭顺序可以被精确表达**：先等生产归零，再关 channel，最后等消费归零。如果混用一个 WaitGroup，你将无法说清"该等谁"。

### 4.2 为什么 close 必须由"第三方"来做

`close(tasks)` 的语义是"**从此再也不会有人向它发送**"。三个生产者各自只知道自己的进度，任何一个都无权替别人宣布——P1 发完就 close，P2/P3 的下一次发送就是 panic（send on closed channel）。所以标准做法是起一个收尾 goroutine：

```go
go func() {
    wgProducers.Wait() // 等到"所有生产者都完成"这一事实成立
    close(tasks)       // 此时才满足 close 的前提
}()
```

`Wait()` 返回的那一刻，"再无发送"被证明，close 才是安全的。

### 4.3 为什么消费者侧绝不能 close

从语言语义看有三层：**权力**——close 是发送侧的操作，消费者根本无法知道"还有没有生产者要发"，它没有资格替未来做保证；**时机**——消费者 early close 会让还在发送的生产者当场 panic，而消费者对此毫不知情；**无益**——消费者想退出去等关闭信号就行（`for range` 在 close 后自动结束），主动 close 不解决任何它面对的问题，只制造竞态。Go 干脆规定"仅发送方应 close"，编译器还用单向 channel（`chan<-`/`<-chan`）把这种权力分配固化到类型上。

### 4.4 对账：证明没有丢也没有重

多个消费者并发写同一个统计切片需要 `sync.Mutex` 保护；main 在 `wgConsumers.Wait()` 之后读统计做对账——此时消费者全部退出，数据不再有并发写，读是安全的。"发送 12 = 接收 12"就是这个模式健康运行的收据。

## 5. 关键字段与坑清单

| 写法 | 含义 | 为什么重要 |
|---|---|---|
| `tasks := make(chan Task)` | 无缓冲 channel | 每次发送与接收都是一次握手，行为最纯粹 |
| `wgProducers.Add(1)` / `defer Done()` | 计数管理与自愈释放 | Add 必须在 `go` 之前，否则 Wait 可能提前返回 |
| `wgProducers.Wait(); close(tasks)` | 收尾 goroutine 的两步 | close 的前提由 Wait 证明 |
| `for t := range tasks` | 消费直到关闭 | 关闭且排空后循环自动结束，无需额外信号 |
| `go func(id int){...}(p)` | 循环变量作参数传入 | 避免多个 goroutine 闭包共享同一变量 |

坑清单：

- 让某个生产者自己 `close(tasks)`：其他生产者再发送就 panic——可把 demo 改成这样复现
- 消费者侧 close：同样 panic，且违反"发送方关闭"的语义约定
- 重复 close 同一 channel：panic（close of closed channel）
- `wg.Add(1)` 写进了 goroutine 内部：main 的 `Wait()` 可能在 Add 之前就通过，等了个寂寞
- 消费者并发写统计切片不加锁：`go run -race` 直接报数据竞争

## 6. 文件结构

```text
concepts/18_producer_consumer/
├── main.go                     # 演示程序：3 生产者 → tasks chan → 2 消费者，两级 WaitGroup + 第三方 close + 对账
├── README.md                   # 本文件：语义讲解 + 运行指引 + 深入要点
└── images/
    ├── pc.workflow.json        # 图源：生产→通道→消费→两级关闭的对账管线（Typed JSON IR）
    ├── pc.html                 # 交互版：自包含单文件，可缩放/切换主题
    └── pc.svg                  # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：close 之后还能读出数据吗？**
A：能。close 不是"清空"，而是"封口"：缓冲里（以及正在传递中）的数据仍可继续接收，排空之后才返回零值。`for range` 正是利用这一点把剩余任务处理完才退出。

**Q：为什么不让 main 直接 `wgProducers.Wait()` 然后 close？**
A：可以，功能等价；但 main 随后还要等消费者，把"等生产、关通道"包进一个收尾 goroutine 能让 main 的职责只剩"等消费 + 对账"，关闭逻辑也被封装成一个可复用的惯用法（很多库函数叫 `drain` 或 `closeWhenDone`）。

**Q：对账时为什么不需要锁就能读 `received`？**
A：锁的必要性来自并发访问。`wgConsumers.Wait()` 返回后所有消费者已退出，写操作全部结束，`Wait` 与后续读之间有 happens-before 关系——这正是 WaitGroup 除了"等待"之外的同步语义。

**Q：把 channel 换成有缓冲的会改变关闭顺序吗？**
A：不会。close 的前提始终是"再无发送"，与缓冲无关；有缓冲只是解耦了发送与接收的时机（生产者不被消费者阻塞），两级 WaitGroup 的收尾流程一字不改。

## 8. 总结

生产者-消费者的难点从来不在收发，而在**关闭顺序**：先 `wgProducers.Wait()` 证明"再无发送"，由第三方 close，再 `wgConsumers.Wait()` 等消费排空——顺序即语义。掌握这套两级等待，多生产多消费就可以放心地扩展到任意数量。

- 组件化对照：把这套收尾封装成可复用的泛型组件（含扇入合并）见 [../../patterns/09_producer_consumer/](../../patterns/09_producer_consumer/)。
- 下一篇 [19_pipeline](../19_pipeline/) 看三级流水线：把"生产→加工→汇总"串成 stage，用 channel 把这条纪律推广成整条数据管线。
