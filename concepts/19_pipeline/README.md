# 19 · Pipeline 模式：数据在 stage 间的 channel 之河

> 把一个大计算拆成一串 stage，每个 stage 用 channel 接到下一个 stage 上，数据就像水一样逐个流过整条管线。这样拆的好处是每个 stage 只做一件事、可以独立并发运行，而且整条流水线不需要"数个数"的收尾信号——`close` 沿着 stage 逐级传播，下游自然知道"没有更多了"。本实验用 生成 1..10 → 平方 → 求和 三级流水线把这个模式完整跑一遍。

## 1. 为什么需要它

不用流水线的写法是两层 for 循环：先算完所有平方存进切片，再遍历求和。问题是每一步都要等上一步全部做完（没有并发）、中间结果全部落盘（多余内存）、逻辑搅在一个函数里（不好测试复用）。流水线把这三个问题一起解决：stage 之间用 channel 传递，数据到哪算到哪；每个 stage 是独立函数，输入输出都是 channel，随手就能拼接、替换、测单测。这也是 Go 并发编程里最常用的组合方式之一——不要用共享内存来通信，要用通信来共享内存。

## 2. 总览：核心机制一图看懂

![三级流水线：generate → square → sum 的 channel 数据流](images/pipeline.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/19_pipeline/images/pipeline.html)
> （或本地打开 [`images/pipeline.html`](images/pipeline.html)）。

**心智模型一句话**：每个 stage 是一个"只做一件事"的 goroutine，上游 channel 是它的进水管、下游 channel 是出水管；数据形态沿着管线从 `int 1..10` 变成平方值流、最后折叠成单一结果 385。

## 3. 快速开始

```bash
cd concepts/19_pipeline
go run main.go
```

预期输出（节选，stage1/stage2 并发打印，顺序可能交错）：

```text
========== 三级流水线：generate → square → sum ==========
[stage1] generate 产出: 1
[stage2] square 收到 1 → 产出 1
[stage1] generate 产出: 2
[stage1] generate 产出: 3
[stage2] square 收到 2 → 产出 4
...
[stage2] square 收到 10 → 产出 100
[stage3] sum: 聚合完成，总和 = 385
最终结果: 1²+2²+...+10² = 385
========== 收尾：close 沿 stage 逐级传播，无需计数信号 ==========
```

注意看交错的行：stage1 还在产 `5` 的时候 stage2 已经在处理 `2` 了——三个 stage 是同时在跑的，数据逐个流过，这就是流水线。

## 4. 核心概念

### 4.1 stage 函数的统一形态

三个 stage 函数签名高度一致：接收上游 channel，返回 `out := make(chan int)` 起的 goroutine 干活，干完 `defer close(out)`。写熟了之后，流水线就是"把函数调用串起来"：`sum(square(generate(10)))`。

### 4.2 单向 channel 是职责边界

`generate` 返回 `<-chan int`（只收），调用方拿到后**想发也编译不过**；`square` 的参数 `in <-chan int`（只收）同样收不到"往里写"的能力。方向约束写在类型上，谁生产、谁消费一目了然，接反了立刻编译报错。单向的转换发生在函数边界：双向 channel 在 `return out` 时自动收窄为 `<-chan`。

### 4.3 close 沿流水线传播

`generate` 产完 10 个数后 `close(out)`；`square` 的 `for range in` 检测到通道关闭且排空，循环退出，随即 `close(out)` 通知下一级；`sum` 同理。整条管线不需要 WaitGroup、不需要约定个数——"上一级关闭"本身就是结束信号。唯一的顺序纪律：**先发结果，再 close**，反过来接收方会先读到零值。

### 4.4 末端接收就是同步点

`total := <-results` 这一行，要等 sum 把上游全部累加完才会返回——它天然是整条流水线的汇合点。main 不用管中间两级跑没跑完，收末端结果就是等全管线。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func generate(n int) <-chan int` | 返回单向接收通道 | 把"只能收"写进类型，调用方无法破坏流向 |
| `func square(in <-chan int) <-chan int` | 收单向、产单向 | stage 职责边界的标准签名 |
| `defer close(out)` | stage 结束时关闭下游 | close 沿管线传播的源头动作 |
| `for v := range in` | 收到 close 且排空后自动退出 | 下游无需知道上游个数 |
| `out <- total; close(out)` | 先发结果再关闭 | 顺序反了接收方读到零值 |

坑清单：

- **两个消费者分一个 stage 的输出**：把同一个 channel 给两个接收方，元素会被"瓜分"，两边都拿不到完整数据——本实验若额外开 goroutine 去观察 `squares`，sum 的结果就错了；要观察请在 stage 内部打印
- **忘记 close 或 close 错层级**：只有发送方该 close；下游接收方 close 上游通道是运行时 panic
- **向已关闭的 channel 发送**会 panic：所以 close 必须放在"确定不再发送"之后（这里用 `defer` 保证）
- **`for range` 不结束导致泄漏**：上游忘了 close，下游 range 永远阻塞，goroutine 泄漏（见 [../08_leak_detect/](../08_leak_detect/)）
- 未使用的局部变量是编译错：示例里每个变量都"被用掉"了

## 6. 文件结构

```text
concepts/19_pipeline/
├── README.md                     # 本文件：原理 + 运行指引 + 深入要点
├── main.go                       # 三级流水线 demo：generate/square/sum + main 接线打印
└── images/
    ├── pipeline.dataflow.json    # 图源：三 stage 的 channel 数据流（Typed JSON IR）
    ├── pipeline.html             # 交互版：自包含单文件，可缩放/切换主题
    └── pipeline.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 stage 要用 goroutine 而不是普通函数调用？**
A：如果 `generate` 在 main 里同步跑完再返回 channel，三级就退化成串行批处理——square 必须等 10 个数全部生成完才开始。放进 goroutine 后，stage1 产一个、stage2 立刻能处理一个，各 stage 按各自节奏推进。

**Q：通道要不要加缓冲？**
A：无缓冲时上游发送会阻塞到下游接收，数据逐个"接力"，背压天然存在；加缓冲能让快 stage 领先慢 stage 若干个元素，但只是平滑抖动，瓶颈仍由最慢 stage 决定。学习阶段建议无缓冲，行为最好预测。

**Q：sum 之后为什么还要 close？**
A：末端通道没人再发了，close 是把"可以安全地 range/收完即走"这个事实告诉未来可能的接收方；不 close 的末端通道在只收一次的场景不会泄漏，但只要有人 `for range` 它就会卡死。保持"谁发送谁关闭"的习惯最稳。

**Q：流水线和 worker pool 怎么选？**
A：流水线解决"多步骤串起来"（stage 之间异构），worker pool 解决"单步骤并行加速"（同构任务分摊）。两者常组合：把某个慢 stage 内部做成扇出/扇入，就是下一篇 [../20_fan_in_out/](../20_fan_in_out/) 的内容。

**Q：想给流水线加"中途取消"怎么办？**
A：本例的关闭链是"正常跑完"的路径；若下游提前退出，上游会阻塞在发送上造成泄漏。标准解法是给每个 stage 传入 `ctx context.Context`，select 里同时监听 `ctx.Done()`——见 [../22_context/](../22_context/)。

## 8. 总结

Pipeline 模式 = 单向 channel 连接的"一件事 stage"：类型签名（`<-chan` 进、`<-chan` 出）定职责，`close` 沿关闭链传播定收尾，末端接收定同步。把 §3 的输出与 §2 的图对照走一遍，再试着把 square 换成 cube——你会发现流水线的扩展就是"换一个 stage 函数"。

下一篇 [20_fan_in_out](../20_fan_in_out/) 看扇出扇入：把流水线里最慢的 stage 摊给多个 worker，再用 merge 把结果汇回一条通道。
