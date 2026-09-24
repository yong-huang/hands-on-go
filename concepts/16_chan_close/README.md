# 16 · Channel 关闭语义：close 是"没有了"，不是"销毁"

> channel 的关闭是 Go 并发里最容易被误解的动作：它不清空缓冲、不销毁通道，只承诺一件事——**不会再有新值了**。接收方靠这个承诺获得零值兜底、`ok=false` 探测、`for range` 自动退出，甚至把它当广播信号用。本实验把 close 之后的每一条读取行为逐行跑给你看。

## 1. 为什么需要它

没有 close，接收方只能靠超时或约定数量来猜"还有没有值"——猜错就永久阻塞泄漏 goroutine。有了 close：发送方发完即宣告，接收方用 `v, ok` 一条语句就能区分"收到了零值"和"没值了"；`for range` 能自然收尾；多个等待者能被一次 close 同时唤醒。不理解关闭语义，就写不出能优雅退出的并发程序。

## 2. 总览：核心机制一图看懂

![Channel 关闭语义生命周期](images/close.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/16_chan_close/images/close.html)
> （或本地打开 [`images/close.html`](images/close.html)）。

**心智模型一句话**：close 是发送方说"没有了"，不是销毁通道——close 不清空缓冲，余值照常读出，排空后读取立即返回零值；重复 close 会 panic，而一次 close 能广播唤醒全部等待的接收者。

## 3. 快速开始

```bash
cd concepts/16_chan_close
go run main.go
```

预期输出（worker 编号顺序可能漂移）：

```text
========== 1. 关闭后读取：零值 + ok=false ==========
排空前 v, ok = 42 true （缓冲余值照常读出）
排空后 v0 = 0 | v1, ok1 = 0 false （ok=false 表示已关闭且排空）

========== 2. for range：close + 排空后自动退出 ==========
range 收到 1..5 的平方，sum = 55 （1+4+9+16+25）

========== 3. 重复 close：panic，但可以被 recover ==========
recover 到: close of closed channel
程序没崩，继续执行

========== 4. close 广播：3 个接收者同时退出 ==========
worker 3 收到退出信号
worker 2 收到退出信号
worker 1 收到退出信号
3 个 worker 全部退出（wg.Wait 无阻塞直接返回）
```

## 4. 核心概念

### 4.1 关闭后读取的三段行为

close 不清空缓冲。关闭后的读取按顺序呈现三种行为：缓冲里还有余值 → 照常读出（`v, ok = 42, true`）；余值读完 → 立即返回零值，**不阻塞**；用双值形式能区分"真收到了零值"和"没值了"（`ok=false`）。

### 4.2 for range 的退出条件

`for v := range ch` 循环退出的充要条件是：**已 close 且已排空**。只 close 不排空，range 会先读完缓冲里的余值再退出——这正是"先收 8/9/10 后退出"的原因。没有 close 的 channel，range 永远不会结束。

### 4.3 重复 close 与向已关闭通道发送

对同一个 channel 第二次 `close` 会 panic（`close of closed channel`）；向已关闭的 channel **发送**数据同样 panic。所以惯例是**谁发送谁 close，接收方永远不 close**；多个发送方需要 `sync.Once` 或额外的协调通道保证只关一次。

### 4.4 close 是广播原语

`<-ch` 在 channel 关闭时对所有等待的接收者**同时**解除阻塞——一次 close，N 个 goroutine 全部收到零值退出信号。用 `chan struct{}`（不携带数据的信号通道）+ `close` 是 Go 里最惯用的"通知所有人退出"模式。

## 5. 关键语法与坑清单

| 语法 | 行为 | 备注 |
|---|---|---|
| `close(ch)` | 宣告不再有新值 | 只有发送方调用；不清空缓冲 |
| `v := <-ch`（已排空） | 立即返回零值 | 不阻塞，但无法区分真假零值 |
| `v, ok := <-ch` | `ok=false` 表示已关闭且排空 | 探测关闭的标准姿势 |
| `for v := range ch` | 关闭且排空后自动退出 | 未关闭则永不结束 |
| 二次 `close(ch)` | panic | `defer recover` 可捕获 |
| 向已关闭通道发送 | panic | 接收不会 panic |

坑清单：

- 关闭不等于排空：close 后缓冲里的余值**必须先读完**，直接当"零值流"用会丢数据
- 循环里用 `v := <-ch` 判断零值来探测关闭——当零值本身是合法数据时会误判，应使用 `v, ok`
- 对 `nil` 通道 close 会 panic，收发则永久阻塞（见 [08_leak_detect](../08_leak_detect/)）
- 多个发送方各自 close：二次 close panic——用 `sync.Once` 收敛为恰好一次

## 6. 文件结构

```text
concepts/16_chan_close/
├── README.md                    # 本文件：原理 + 运行指引 + 深入要点
├── main.go                      # 四段演示：零值/ok、range 退出、重复 close、广播
└── images/
    ├── close.lifecycle.json     # 图源：channel 打开→关闭→排空/广播的状态机（图源 JSON）
    ├── close.html               # 交互版：自包含单文件，可缩放/切换主题
    └── close.svg                # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么关闭后要返回零值而不是阻塞或报错？**
A：`<-ch` 是表达式，必须产出一个值；"关闭且无值"是一种**终态**而非错误，返回零值让表达式语义完整。需要区分时用双值形式，`ok` 才是关闭信号，零值只是填充。

**Q：close 之后缓冲里的数据去哪了？**
A：还在。close 只改通道状态位，不搬运不丢弃数据；接收方按 FIFO 先读完余值，之后才进入"零值 + ok=false"阶段。`len(ch)` 在排空前仍大于 0。

**Q：接收方 close 会怎样？发送方怎么知道接收方不要了？**
A：接收方 close 同样合法但语义颠倒——它不通知发送方。反向取消要用额外机制：`context` 取消、`select` + 退出通道，或带缓冲的信号通道（见 [22_context](../22_context/)）。

**Q：广播为什么用 `chan struct{}`？**
A：`struct{}` 零字节、不携带数据，读它只关心"事件发生了"这一事实；close 让事件永久成立（后来者读取也立即返回），比发一个值只能唤醒一个接收者高效且语义准确。

## 8. 总结

close 把"结束"变成一个所有 goroutine 都能观测到的事件：读取端得到零值与 `ok=false`，range 得到自然退出，等待者得到广播唤醒。记住三条铁律——谁发送谁 close、关闭不排空、重复 close 必 panic——channel 的收尾问题就都有了标准答案。

下一篇 [17_chan_semaphore](../17_chan_semaphore/) 看信号量限流：用带缓冲 channel 当令牌桶，控制并发度。
