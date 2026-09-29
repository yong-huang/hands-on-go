# 16 · Channel 关闭语义：close 是"没有了"，不是"销毁"

> channel 的关闭是 Go 并发里最容易被误解的动作：它不清空缓冲、不销毁通道，只承诺一件事——不会再有新值了。接收方靠这个承诺获得零值兜底、`ok=false` 探测、`for range` 自动退出，还能把它当广播信号用。本篇把 close 之后的每一条读取行为逐行演示；读完你能掌握关闭后读取的三段行为、range 的退出条件，以及"一次 close 唤醒所有等待者"的广播用法。

## Background

Go 里 channel（goroutine 之间传递数据的管道）创建后没有"结束"这个状态。没有 close 时，接收方只能靠超时或约定数量来猜"还有没有值"——猜错就永久阻塞，goroutine（语言内置的轻量级线程）泄漏。

猜的代价具体可见：消费者 `for v := range ch` 等一个永不关闭的 channel，循环永不结束；调用方想知道"处理完了没"，只能另设标志位轮询。

Go 把"结束"做成了 channel 自带的状态：发送方一个 `close` 调用，接收方的单值读取、双值探测、`for range`、多路等待全部自动获得收尾语义。

## What

一句话定义：close 是发送方对 channel 的一种宣告，用来承诺"不会再有新值"；它不清空缓冲、不销毁通道，只改变通道状态——余值照常读出，排空后读取立即返回零值。

可以把 close 想象成商店门口挂出的"今日售罄，不再进货"牌子：货架（缓冲）上的存货照常卖，卖完之后顾客再来，自动得到"没有了"的答复。但和"把店拆了"不同的是，通道本身还在——重复挂牌（二次 close）会 panic，而一次挂牌能同时通知所有在等的顾客。

close 的承诺给接收方带来四种能力：

- 排空后读取立即返回零值（Go 里变量类型的默认值，`int` 为 0），不阻塞
- `v, ok := <-ch` 用 `ok=false` 区分"真收到了零值"和"没值了"
- `for range` 在关闭且排空后自动退出
- 一次 close 广播唤醒全部等待的接收者

## When to Use

在做什么事的时候用它：

- 在做流式消费的收尾时：发送方发完最后一批数据就 `close`，消费方 `for range` 读完余值自然退出，不需要额外的"结束标志"消息。
- 在做"通知所有人退出"的取消广播时：`chan struct{}`（不携带数据的信号通道）+ close，N 个等待者同时被唤醒。
- 在做一次性结果传递时：worker 算完把结果发进带缓冲 channel 再 close，调用方稍后读取仍能拿到结果，不必同步等待。

何时不用：双方用 `sync.WaitGroup` 就能会合的一次性交互，不需要 close；跨多层的取消信号用 `context` 更合适（见 [22_context](../22_context/)）。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `close(ch)` | 永久性的"没有了"宣告，可广播 | 数据流有明确终点、要通知多个等待者 |
| 发哨兵值（约定表示"结束"的特殊值） | 只能唤醒一个接收者，要占用一个保留值 | 不拥有 channel、只有发送权的场景 |
| `context` 取消 | 携带取消原因、可级联传播 | 需要跨多层传递取消信号（见 [22_context](../22_context/)） |

## Quick Start

前置条件：本机装有 Go，无第三方依赖。

```bash
cd concepts/16_chan_close
go run main.go
```

预期输出（节选，程序末尾还会打印"========== 总结 =========="共 4 条收尾规则；worker 编号顺序可能漂移）：

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

第 1 段演示的读取序列（关闭后缓冲里还有 1 个值，读取按"余值 → 零值"演进）：

```go
	ch := make(chan int, 2)
	ch <- 42
	close(ch)     // 缓冲里还有 1 个值，close 不清空缓冲
	v, ok := <-ch // 先读到缓冲里的真值
	fmt.Println("排空前 v, ok =", v, ok, "（缓冲余值照常读出）")
	<-ch            // 把最后一个值读走，channel 彻底排空
	v0 := <-ch      // 关闭且已排空：立即返回，不阻塞
	v1, ok1 := <-ch // 双值形式能区分"真收到了零值"和"没值了"
	fmt.Println("排空后 v0 =", v0, "| v1, ok1 =", v1, ok1, "（ok=false 表示已关闭且排空）")
```

第 4 段广播的写法（3 个 worker 等 `struct{}` 通道，一次 close 全部唤醒）：

```go
	broadcast := make(chan struct{})
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-broadcast // channel 关闭时，所有等待的接收者都被唤醒（零值 struct{}）
			fmt.Printf("worker %d 收到退出信号\n", id)
		}(i)
	}
	close(broadcast) // 一次 close，广播给全部 3 个接收者
	wg.Wait()
	fmt.Println("3 个 worker 全部退出（wg.Wait 无阻塞直接返回）")
```

## How It Works

### 关闭后读取的三段行为

close 不清空缓冲。关闭后的读取按顺序呈现三种行为：缓冲里还有余值 → 照常读出（`v, ok = 42, true`）；余值读完 → 立即返回零值，**不阻塞**；用双值形式能区分"真收到了零值"和"没值了"（`ok=false`）。

Quick Start 输出第 1 段的两行正对应：`排空前 v, ok = 42 true` 是余值阶段，`排空后 v0 = 0 | v1, ok1 = 0 false` 是终态阶段。

### for range 的退出条件

`for v := range ch` 循环退出的充要条件是：**已 close 且已排空**。只 close 不排空，range 会先读完缓冲里的余值再退出。没有 close 的 channel，range 永远不会结束。

demo 第 2 段先灌入 5 个平方值再 close，range 收完 1/4/9/16/25、sum=55 后退出——输出里"range 收到 1..5 的平方，sum = 55"就是这条规则的印证。

### 重复 close 与向已关闭通道发送

对同一个 channel 第二次 `close` 会 panic（`close of closed channel`）；向已关闭的 channel **发送**数据同样 panic。

demo 第 3 段用 `defer recover` 捕获了这次 panic，所以输出里能看到"recover 到: close of closed channel"和"程序没崩，继续执行"。

惯例是**谁发送谁 close，接收方永远不 close**；多个发送方需要 `sync.Once`（保证函数体只执行一次的同步原语）或额外的协调通道，把关闭收敛为恰好一次。

### close 是广播原语

`<-ch` 在 channel 关闭时对所有等待的接收者**同时**解除阻塞——一次 close，N 个 goroutine 全部收到零值退出信号。用 `chan struct{}` + `close` 是 Go 里最惯用的"通知所有人退出"模式。

demo 第 4 段输出里 3 个 worker 都打印"收到退出信号"，顺序每次可能漂移——被同时唤醒后，先打印谁由调度决定。

关键语法速查：

| 语法 | 行为 | 备注 |
|---|---|---|
| `close(ch)` | 宣告不再有新值 | 只有发送方调用；不清空缓冲 |
| `v := <-ch`（已排空） | 立即返回零值 | 不阻塞，但无法区分真假零值 |
| `v, ok := <-ch` | `ok=false` 表示已关闭且排空 | 探测关闭的标准姿势 |
| `for v := range ch` | 关闭且排空后自动退出 | 未关闭则永不结束 |
| 二次 `close(ch)` | panic | `defer recover` 可捕获 |
| 向已关闭通道发送 | panic | 接收不会 panic |

## Pitfalls & Q&A

坑清单（每条按"现象 → 原因 → 解法"读）：

- **现象**：close 后按"零值流"处理读取，缓冲里的余值被跳过丢数据。**原因**：关闭不等于排空，close 后缓冲里的余值必须先读完。**解法**：消费端用 `for range`（它先排空再退出），别假设 close 后读到的都是零值。
- **现象**：循环里用 `v := <-ch` 是否为零值来判断关闭，当零值本身是合法数据时误判。**原因**：单值形式无法区分"真收到了零值"和"没值了"。**解法**：改用双值形式 `v, ok := <-ch`，`ok` 才是关闭信号。
- **现象**：对 nil 通道（未初始化、值为 nil 的 channel 变量）close 直接 panic，收发则永久阻塞。**原因**：nil 通道的三类操作都是特殊语义。**解法**：确保 channel 已经 `make` 再使用（见 [08_leak_detect](../08_leak_detect/)）。
- **现象**：多个发送方各自 close，程序随机 panic。**原因**：二次 close 必定 panic。**解法**：用 `sync.Once` 把关闭收敛为恰好一次。

**Q：为什么关闭后要返回零值而不是阻塞或报错？**
A：`<-ch` 是表达式，必须产出一个值；"关闭且无值"是一种**终态**而非错误，返回零值让表达式语义完整。需要区分时用双值形式，`ok` 才是关闭信号，零值只是填充。

**Q：close 之后缓冲里的数据去哪了？**
A：还在。close 只改通道状态位，不搬运不丢弃数据；接收方按 FIFO（先进先出）先读完余值，之后才进入"零值 + ok=false"阶段。`len(ch)` 在排空前仍大于 0。

**Q：接收方 close 会怎样？发送方怎么知道接收方不要了？**
A：接收方 close 同样合法但语义颠倒——它不通知发送方。反向取消要用额外机制：`context` 取消、`select` + 退出通道，或带缓冲的信号通道（见 [22_context](../22_context/)）。

**Q：广播为什么用 `chan struct{}`？**
A：`struct{}` 零字节、不携带数据，读它只关心"事件发生了"这一事实；close 让事件永久成立（后来者读取也立即返回），比发一个值只能唤醒一个接收者高效且语义准确。
