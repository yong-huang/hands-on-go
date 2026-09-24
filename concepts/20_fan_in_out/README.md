# 20 · 扇出扇入：一个 channel 喂多个 worker，结果合流回一个

> 上一实验的流水线是"一条道走到黑"：一个 stage 慢，整条线就慢。扇出（fan-out）让多个 worker 共享消费同一个源 channel，把工作分摊出去；扇入（fan-in）再把多路结果合并回一个 channel 统一处理。难点不在分发，而在**收尾**：合并 channel 该由谁、在什么时机 close？本实验用 `sync.WaitGroup` 给出标准答案。

## 1. 为什么需要它

单个消费者跟不上生产速度时，你会本能地"多开几个消费者"。但 channel 的语义保证了一件事恰好帮到你：**一条消息只会被一个接收者拿到**——多个 goroutine 读同一个 channel，元素不会重复、不会丢失。于是 fan-out 几乎是免费的。真正需要设计的是 fan-in：每个 worker 各有一个结果 channel，下游想用 `for range` 只读一个 channel，就必须有人把多路合成一路，并在**所有输入都结束后**关闭合并 channel——关早了丢数据，不关则 range 泄漏。

## 2. 总览：核心机制一图看懂

![扇出扇入：源 channel 分发 3 个 worker，merge 合流回一个 channel](images/fan.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/20_fan_in_out/images/fan.html)
> （或本地打开 [`images/fan.html`](images/fan.html)）。

**心智模型一句话**：fan-out 靠"一条消息只被一个 worker 收到"的天然语义分发；fan-in 靠 `sync.WaitGroup` 等齐所有输入 channel 的 close，再由 merge（合并 channel 的唯一生产者）执行 `close(merged)`。

## 3. 快速开始

```bash
cd concepts/20_fan_in_out
go run main.go
```

真实输出节选（**worker 完成顺序每次运行不同**，属于正常调度抖动；末行恒定）：

```text
worker 2 处理 1 -> 1
worker 2 处理 4 -> 16
worker 2 处理 5 -> 25
worker 3 处理 2 -> 4
worker 1 处理 3 -> 9
worker 3 处理 9 -> 81
worker 2 处理 6 -> 36
worker 1 处理 7 -> 49
worker 3 处理 8 -> 64
共收到 9 个平方值，总和 = 285（= 1²+2²+…+9²）
```

9 个元素一条不漏，平方和恒为 285——谁处理了哪几个元素每次都不同，但结果确定。

## 4. 核心概念

### 4.1 fan-out：共享消费即分发

3 个 worker goroutine 拿到的是**同一个** source channel。Go 运行时对 channel 接收的串行化保证：每个元素只投递给其中一个等待中的接收者。不需要任何分发器代码，`worker(1, src)`、`worker(2, src)`、`worker(3, src)` 就是 fan-out 全部。

### 4.2 fan-in：多路转发 + WaitGroup 把关

`merge` 为每个输入 channel 起一个转发 goroutine：`for v := range c { merged <- v }`，channel 被 close 后 range 退出、`wg.Done()`。最后另一个 goroutine 执行 `wg.Wait(); close(merged)`——`Wait` 返回意味着 3 路输入都已排空，此刻 close 才不丢数据。

### 4.3 谁生产谁 close

`merged` 只有一个生产者（merge 内部的转发 goroutine 们），所以 close 权归 merge 所有。worker 各自 close 自己的结果 channel（`defer close(out)`），source close 源 channel——每个 channel 都由它唯一的生产者关闭，这是关闭语义的铁律。

### 4.4 与 worker pool 的分工

本实验与 [11_worker_pool](../../11_worker_pool/) 结构相近：那边强调"固定 N 个 worker 抢固定任务队列、限制并发量"；这边强调"多路结果如何合成一路并优雅关闭"。生产中两者常组合使用。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func source() <-chan int` | 返回只读 channel | 把"只能收"写进类型，防止调用方误 close/发送 |
| `worker(id, src)` × 3 | 多 goroutine 共享同一接收 channel | fan-out 本体：一条消息只被一个 worker 拿到 |
| `defer close(out)` | worker 处理完关闭自己的结果 chan | 触发 merge 端 `for range` 退出 |
| `wg.Add(len(ins))` | 按输入路数计数 | 每路一个转发 goroutine，Done 一次 |
| `go func(){ wg.Wait(); close(merged) }()` | 等 3 路全空再 close | fan-in 的收尾关键：关早丢数据，不关 range 不退出 |

坑清单：

- **在 worker 里 close 源 channel**：多个 worker 同时消费时，谁都不该 close 共享输入——close 权属于唯一生产者 source，否则二次 close 直接 panic
- **`wg.Wait()` 后忘记 close(merged)**：main 的 `for range merged` 永远等下去，goroutine 泄漏（跑着跑着程序不退出）
- **先 close(merged) 再等 WaitGroup**：转发 goroutine 还在往已关闭的 channel 发送，panic: send on closed channel
- **for 循环变量直接传给闭包**（Go 1.22 之前）：闭包捕获同一个变量，worker 编号全是最后一个值；用参数传入（本实验 `func(id int, ...)`）或升级到 1.22+
- **以为 fan-out 会复制消息**：不会。一条消息只被一个 worker 收到；要广播得自己写（见 [15_select](../15_select/) 或多个 channel 各发一份）

## 6. 文件结构

```text
concepts/20_fan_in_out/
├── README.md                   # 本文件：原理 + 运行指引 + 深入要点
├── main.go                     # 演示程序：source → 3 worker（fan-out）→ merge（fan-in）→ 汇总
└── images/
    ├── fan.dataflow.json       # 图源：五步管线的数据流图（Typed JSON IR）
    ├── fan.html                # 交互版：自包含单文件，可缩放/切换主题
    └── fan.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 fan-out 不需要任何"分发器"？**
A：channel 接收本身是原子的串行化操作——运行时从等待接收者中挑一个交付元素。多个 goroutine 阻塞在同一个 channel 的接收上时，每个元素只交付给其中一个，天然互斥，不存在重复消费或竞态。

**Q：merge 里为什么要再起一个 goroutine 做 `wg.Wait(); close(merged)`，不直接在当前 goroutine 做？**
A：`merge` 需要立刻返回 `merged` channel 让下游接上（本实验的 pipeline 风格是"函数即阶段"）。`Wait` 是阻塞调用，若不放进 goroutine，merge 会卡到所有输入结束才返回，下游连 channel 都拿不到。

**Q：结果 channel 需要缓冲吗？加了会怎样？**
A：不需要也能正确工作。给 worker 的结果 chan 加缓冲能让 worker 在 merge 转发慢时少阻塞一点（吞吐微调），但不改变正确性；merged 不缓冲则严格回压。缓存是性能参数，不是正确性参数。

**Q：扇出的 worker 数量该怎么定？越多越快吗？**
A：CPU 密集型任务上限约是 `runtime.GOMAXPROCS(0)`（见 [10_gomaxprocs](../10_gomaxprocs/)）；IO 密集可以更多。worker 太多反而增加调度与 channel 争用开销——worker 数是并发度旋钮，不是正确性开关。

**Q：如果某个 worker panic 了会怎样？**
A：panic 会沿 goroutine 崩溃整个程序，且它来不及 `close(out)`，即便 recover 也要补 close 才不拖垮 merge。并发程序的错误处理要么在每个 worker 内 recover 兜住，要么把 error 当数据通过 channel 传回（见 [25_panic_recover](../25_panic_recover/)）。

## 8. 总结

fan-out 是"免费午餐"：channel 的一次性投递语义让多 worker 共享消费天然安全；fan-in 则需要一点工程纪律——`sync.WaitGroup` 等齐所有输入、由唯一生产者 close 合并 channel。记住两个铁律：**一条消息只被一个接收者拿到**、**谁生产谁 close**。把 §3 跑几遍，观察 worker 分工每次都不同而总和恒为 285，并发"分而不乱"的感觉就建立了。

下一篇 [21_mutex_race](../21_mutex_race/) 看竞态与锁：当共享的不是 channel 而是普通变量时，会发生什么、怎么救。想继续深挖 channel 底层实现，见 internals 线的 hchan 与调度器篇。
