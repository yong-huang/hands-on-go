# 14 · 单向 Channel：把方向写进函数签名

> channel 天生双向，但好的 API 只暴露需要的那一个方向。Go 用类型系统做这件事：`chan<- int` 只能发、`<-chan int` 只能收，约束发生在编译期——方向用错直接编译错，零运行时开销。本篇用一个生产者/消费者的最小完整实现，展示双向 channel 传参时的隐式转换单向；读完你能把"谁发、谁收、谁关闭"从口头约定升级为类型约束。

## Background

Go 里 channel（goroutine 之间传递数据的管道）创建出来是双向的。没有方向约束时，任何拿到 channel 的函数都能读也能写——谁负责发送、谁负责关闭，全靠口头约定或注释。

约定的脆弱在多人协作时暴露：消费者"顺手发一条"通知、维护者把 `close` 挪了个位置，编译器都无话可说，问题拖到运行时才炸。

Go 的答案是把方向写进类型：参数声明成只发或只收，越权操作直接编译失败。约束从文档搬进了类型，这是 Go 标准库随处可见的设计（如 `time.Tick` 返回 `<-chan time.Time`）。

## What

一句话定义：单向 channel 是只保留发送或接收一侧能力的 channel 类型，用来在函数签名层面声明"这个函数只会发"或"这个函数只会收"；双向 channel 传参时自动转成对应方向，无需显式代码。

箭头记法本身就是语义：`chan<- int` 箭头朝 chan 里，"数据往里流"——只写；`<-chan int` 箭头朝外，"数据往外流"——只读。

可以把单向 channel 想象成双向通道的两个"权限视图"：同一个门禁系统，发给仓库管理员的卡只能进货，发给店员的卡只能取货。但和"另开一条新通道"不同的是，视图背后是同一条 channel——转换只发生在类型层面，不拷贝任何数据。

## When to Use

在做什么事的时候选它：

- 在拆分生产者/消费者流水线的函数时：生产函数收 `chan<-`，消费函数收 `<-chan`，main 持有双向并负责协调。
- 在设计库 API 时：返回值用 `<-chan` 暴露事件流（如定时器、完成通知），调用方从拿到返回值那一刻起就没有写权限。
- 在做结构体封装时：channel 字段私有，方法对外返回 `<-chan` 视图，是标准的封装手法。

何时不用：一次性脚本或函数内部自用的 channel，方向显而易见时，直接用双向 `chan int` 更省事；类型约束的价值随 API 边界扩大而显现。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 双向 `chan int` | 收、发、close 全能 | 创建者/main 持有，或函数内自用 |
| 单向参数（本篇） | 编译期禁止越权方向 | 跨函数、跨包传递，权限需要显式声明 |
| 私有字段 + `<-chan` 方法 | 连持有权一起隐藏 | 库对外暴露事件流时 |

## Quick Start

前置条件：本机装有 Go，无第三方依赖。

```bash
cd concepts/14_directional_chan
go run main.go
```

预期输出：

```text
收到: 10
收到: 20
收到: 30
收到: 40
收到: 50
channel 已关闭，消费者退出
```

main.go 的注释里埋了两行"取消注释就编译错"的代码（在只读参数上发送、在只写参数上接收），删掉注释前缀即可亲眼看到编译器的方向报错。

生产与消费的核心骨架（producer 拿只写端发 5 条并关闭；consumer 拿只读端 range 到排空）：

```go
// producer 只写端：参数类型 chan<- int，箭头指向 chan 表示"只能往里发"。
// ch <- 1 合法；试图 <-ch 接收则编译报错（见函数末尾注释）。
func producer(ch chan<- int) {
	for i := 1; i <= 5; i++ {
		ch <- i * 10 // 只允许发送
	}
	close(ch) // 发送方负责关闭，是 chan<- 的特权也是职责
	// v := <-ch // 编译错：invalid operation: cannot receive from send-only channel ch (variable of type chan<- int)
}

// consumer 只读端：参数类型 <-chan int，箭头在 chan 左侧表示"只能从里收"。
// <-ch 合法；试图 ch <- v 发送则编译报错（见循环后注释）。
func consumer(ch <-chan int) {
	for v := range ch { // channel 被 close 且排空后循环自动退出
		fmt.Printf("收到: %d\n", v)
	}
	// ch <- 1 // 编译错：invalid operation: cannot send to receive-only channel ch (variable of type <-chan int)
}
```

## How It Works

### 隐式转换与单向性

双向 `chan int` 传给 `chan<- int` 或 `<-chan int` 参数时自动转换，无需任何显式代码；反向转换不存在——单向丢掉了另一侧的能力，类型系统不还给你。

main.go 末尾的注释给了正反两例：`var ro <-chan int = ch` 合法，`var both chan int = ro` 编译错（类型不匹配）。Quick Start 输出里的 5 条数据与"消费者退出"，走的正是 main 里两次隐式转换背后的同一条 channel。

### close 是发送方的特权

`close` 只对 `chan<-` 合法。这把"谁负责关闭"编码进了类型：消费者手里根本没有关闭的能力，重复 close 之类的事故在签名层面就被隔离了。

Quick Start 输出最后一行"channel 已关闭，消费者退出"的前提，正是 producer 里的 `close(ch)`——`for range` 等到关闭且排空才退出循环。

### 类型速查

| 类型 | 合法操作 | 编译报错示例 |
|---|---|---|
| `chan int` | 收、发、close | 全能持有者 |
| `chan<- int` | 发、close | `<-ch` → cannot receive from send-only |
| `<-chan int` | 收 | `ch<-` → cannot send to receive-only |

## Pitfalls & Q&A

坑清单（每条按"现象 → 原因 → 解法"读）：

- **现象**：把 `<-chan int` 赋回 `chan int` 变量，编译失败。**原因**：方向转换单向，单向类型丢掉了另一侧能力，类型系统不归还。**解法**：双向 channel 只在创建处（如 main）持有，需要双向权限就从创建处重新传参。
- **现象**：担心函数收到单向参数后操作的是"另一条 channel"，数据不同步。**原因**：误把视图当成新通道——单向是同一 channel 的受限视图，底层还是同一条 chan，转换不拷贝任何东西。**解法**：放心传递；分析同步行为时只看底层数据结构。
- **现象**：以为 `<-chan` 参数只能读一次、写不了循环。**原因**：混淆了"只读方向"与"读取次数"。**解法**：`for range ch` 与 `v, ok := <-ch` 都对 `<-chan` 可用——只读不等于只能读一次。
- **现象**：外部代码绕过封装直接往事件流里塞数据。**原因**：结构体把双向 channel 字段或 getter 直接暴露了出去。**解法**：字段私有，方法只返回 `<-chan` 视图（私有字段 + 方法返回 `<-chan` 是标准封装手法）。

**Q：单向 channel 有运行时开销吗？**
A：零。它只是编译期的类型视图，运行时全是同一个 `hchan`（channel 的运行时结构体）指针。

**Q：为什么 close 只允许发送方？**
A：只有发送方知道"数据是否发完"；若允许接收方关闭，多发送方场景会互相干扰。类型系统把这条纪律固化了。

**Q：双向 chan 传给单向参数是拷贝吗？**
A：是 channel 头的值拷贝（和普通传参一样），指向同一底层数据；"转换"只发生在类型层面。

**Q：什么时候该用单向参数？**
A：只要函数对 channel 的使用是单向的——这几乎是所有情况。生产函数收 `chan<-`，消费函数收 `<-chan`，main 才持有双向。
