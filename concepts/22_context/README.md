# 22 · Context：一次 cancel，整棵 goroutine 树同时退出

> `context.Context` 是 Go 处理"跨 goroutine 控制信号"的标准答案：把取消信号、截止时间、请求级数据装进一个值，沿着调用树向下传；父节点一声令下，所有子 goroutine 的 `Done()` 同时关闭。读完本篇你将掌握 WithCancel / WithTimeout / WithValue 三类派生方式的用法与边界、树形广播的传播机制，以及典型泄漏与误用的成因解法。

## Background

goroutine 好开难收：一个请求扇出十个子任务，谁负责告诉它们"不用干了"？

没有 context 时，"取消"要自己发明：给每个函数加一个退出用的 channel，层层手工传递、逐个 close。这个做法有三处撞墙：

- 信号容易漏传，某一层忘了监听，取消就断在半路
- 无法表达"最多再等 1.5 秒"这种截止语义，定时与取消要分别实现
- 无法携带请求级元数据（本次请求的 trace ID、登录用户），只能继续膨胀函数参数

Go 标准库为此提供了 `context` 包，把三件事收敛为一个参数（惯例上永远是函数的第一个参数 `ctx`），net/http、database/sql 等标准库全部基于它。

## What

`context.Context` 是一个接口类型的值，把取消信号、截止时间、请求级数据打包在一起，沿着调用树从父传给子：**取消是广播**（父 cancel，整棵子树同时收到）、**超时只是到点自动 cancel**、**值是旁路**（不占用函数参数）。

可以把取消传播想象成公司里的紧急广播：任何一层的管理者（父 ctx）按下按钮，其下所有部门同时听到警报。但和真实广播不同的是，员工可以戴上耳塞继续干活——context 只负责把信号可靠送达，退不退出仍由每个 goroutine 自己决定。

**心智模型一句话**：context 是一棵单向广播树——取消与超时只从父流向子，`Done()` 是同一时刻关闭的 channel，`Err()` 告诉你为什么退出，`Value()` 是不走参数的旁路。

根持有取消权，子/孙 goroutine 靠 select `Done()` 退出，最后用 WaitGroup（带计数器的等待原语）对账。

## When to Use

- 处理网络请求时：HTTP/RPC 服务里每个请求派生一个 ctx，客户端断开或到达截止时间，这条调用链上的数据库查询、下游调用同时收工。
- 扇出并行子任务时：一个任务拆成多个 goroutine 并行跑，任一失败或整体超时就 cancel，其余子任务立即收到信号退出。
- 跨层传请求元数据时：trace ID、登录用户这类"随请求走"的数据用 WithValue 旁路携带，中间层不需要为此改函数签名。

何时不用它：

- 短小的本地函数调用链、没有任何取消或截止需求：塞 ctx 只是仪式
- 要在 goroutine 之间传的是数据本身：那用 channel，WithValue 不是参数传递的替代品
- 后台常驻任务：ctx 表示"一次调用"的生命周期，见下文坑清单

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 手工 quit channel | 层层传递、逐个 close，无截止语义 | 一次性、无截止需求的简单取消 |
| context（WithCancel 等） | 树形广播 + 截止时间 + 元数据三合一 | 跨函数、跨 goroutine 的可取消调用链 |
| channel 传数据 | 传的是数据不是控制信号 | goroutine 间移交数据 |

## Quick Start

前置条件：已安装 Go 并可用 `go run`，无第三方依赖；程序约 2~3 秒跑完。

```bash
cd concepts/22_context
go run main.go
```

真实输出（节选，共 23 行；`1500ms` 这类耗时随机器有毫秒级波动，第 4 节子/孙两行退出顺序可能互换）：

```text
========== 1. WithCancel：父取消，子收到 Done 退出 ==========
  子: 收到取消信号 context canceled，提前退出
  父: 子已退出，WaitGroup 对账完毕

========== 2. WithTimeout：1.5s 截止，3s 的任务被打断 ==========
  任务: 被超时打断，耗时 1500ms，ctx.Err() = context deadline exceeded

========== 3. WithValue：塞 user 值，子层取出 ==========
  handler: 为用户 "alice" 服务（值经 ctx 旁路传入，不走参数）

========== 4. 树形传播：一次 cancel，三代全退 ==========
  孙: 等待取消...
  子: 携带的 user = "alice"，等待取消...
  根: 调用 cancel()，广播给整棵树
  子: Done 关闭（context canceled），退出
  孙: Done 关闭（context canceled），退出
  根: WaitGroup 归零，子与孙全部退出
```

响应取消的标准姿势，这段在做什么：子 goroutine 在 select 里同时等"任务完成"与"ctx 取消"两个分支：

```go
	go func() {
		defer wg1.Done()
		select {
		case <-time.After(10 * time.Second): // 假装一个长任务
			fmt.Println("  子: 任务正常完成（不该发生）")
		case <-ctx1.Done():
			fmt.Printf("  子: 收到取消信号 %v，提前退出\n", ctx1.Err())
		}
	}()
	time.Sleep(2 * time.Second) // 让子任务跑一会儿
	cancel1()                   // 父调用 cancel：向子 goroutine 广播取消
	wg1.Wait()
```

## How It Works

### WithCancel：手动取消

`ctx, cancel := context.WithCancel(parent)` 返回派生 ctx 与取消函数。

子 goroutine 用 `select { case <-ctx.Done(): ... }` 阻塞等待；父调用 `cancel()` 后 Done 关闭，`ctx.Err()` 返回 `context.Canceled`。

`cancel` 可以并发调用、可以重复调用（幂等，多次调用效果相同），惯例写 `defer cancel()` 防泄漏。

### WithTimeout / WithDeadline：到点自动取消

`ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)` 等价于"1.5 秒后帮你调 cancel"。

任务要跑 3 秒，但 select 先等到 Done 关闭，`Err()` 是 `context.DeadlineExceeded`——与手动取消可区分，任务据此决定是"被叫停"还是"超时报错"。

你在 Quick Start 输出第 2 节看到的"耗时 1500ms"，就来自这个定时器。`defer cancel()` 仍然要写：提前完成时释放定时器资源。

### WithValue：请求级数据旁路

`ctx = context.WithValue(ctx, key, "alice")`，子层用 `ctx.Value(key)` 取回。取值方做类型断言（把接口值转回具体类型的操作）并检查 ok：

```go
// handleRequest 模拟一层服务：从 ctx 里取出请求级数据
func handleRequest(ctx context.Context) {
	user, ok := ctx.Value(ctxKey("user")).(string) // 类型断言取值，ok 判缺失
	if !ok {
		fmt.Println("  handler: 未登录")
		return
	}
	fmt.Printf("  handler: 为用户 %q 服务（值经 ctx 旁路传入，不走参数）\n", user)
}
```

键必须是自定义类型（本实验用 `type ctxKey string`），避免不同包的字符串键互相冲突。它只放请求级元数据，不是函数参数的替代品——编译器不检查 Value，拿错键只会静默得到 nil。

### 树形传播与 WaitGroup 对账

从同一个父派生的 ctx 共享取消命运：根 `cancel()` 一次，子与孙的 Done 在同一时刻关闭，不需要父逐个通知。这段在做什么：子与孙共享同一个根 ctx，各自阻塞在 `<-ctx.Done()` 上等广播：

```go
// child 子 goroutine：监听 ctx.Done()，等待期间顺便看一眼 value
func child(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	u, _ := ctx.Value(ctxKey("user")).(string)
	fmt.Printf("  子: 携带的 user = %q，等待取消...\n", u)
	<-ctx.Done()
	fmt.Printf("  子: Done 关闭（%v），退出\n", ctx.Err())
}

// grandchild 孙 goroutine：与子共享根 ctx 的 Done，树形传播的意义所在
func grandchild(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	fmt.Println("  孙: 等待取消...")
	<-ctx.Done()
	fmt.Printf("  孙: Done 关闭（%v），退出\n", ctx.Err())
}
```

Quick Start 输出第 4 节的两行"Done 关闭（context canceled）"，就来自这两个函数：一次 `rootCancel()` 让两代 goroutine 同时退出。这正是"取消广播"的价值——新增一层调用只要继续传 ctx，取消语义自动继承。

`sync.WaitGroup` 用来对账：Add 了多少个，Wait 就必须等到 Done 多少次，程序才不会悬挂或 panic（Done 多调会触发 negative counter panic，demo 修复过这一版）。

### API 速查

| API | 作用 | 备注 |
| --- | --- | --- |
| `context.Background()` | 根 ctx，main/请求入口用 | 永不取消 |
| `WithCancel(parent)` | 手动取消 | 返回的 `cancel` 必须调用 |
| `WithTimeout(parent, d)` | d 后自动 cancel | `Err()==DeadlineExceeded` |
| `WithDeadline(parent, t)` | 到指定时刻自动 cancel | 同上 |
| `WithValue(parent, k, v)` | 旁路携带值 | 键用自定义类型 |
| `ctx.Done()` | `<-chan struct{}`，取消即关闭 | select 监听它 |
| `ctx.Err()` | `Canceled` / `DeadlineExceeded` | Done 关闭后非 nil |

## Pitfalls & Q&A

踩坑清单（每条可在 demo 里复现或注释复现）：

- **忘记调 cancel**。现象：派生 ctx 及其定时器永远不释放（ctx 泄漏），子 goroutine 永远阻塞在 Done。原因：派生时注册的资源只有 cancel 才释放。解法：惯例 `defer cancel()`。
- **goroutine 里直接干活不监听 Done**。现象：父取消了它还在跑。原因：取消传播对不 select Done 的代码无效。解法：每个子任务都把 `<-ctx.Done()` 写进 select。
- **用 ctx.Value 传业务参数**。现象：拿错键静默得到 nil，编译器与 IDE 都不报错。原因：Value 是接口值存取，类型不安全、对调用方不可见。解法：只放请求级元数据，业务参数走函数签名。
- **WaitGroup Done 与 Add 不对账**。现象：多 Done 一次立刻 panic（`sync: negative WaitGroup counter`）；少 Done 一次 Wait 永久阻塞。解法：每个 goroutine 恰好一次 `defer wg.Done()`。
- **把 ctx 存进 struct 长期持有**。现象：跨请求复用后"取消错人"，无关请求被牵连取消。原因：ctx 表示一次调用的生命周期。解法：ctx 作为参数逐层传递，不作为字段长期保存。

**Q：cancel() 之后 Done 为什么是"关闭"而不是"发一条消息"？**

A：channel 关闭是粘性的：关闭后所有现在和将来的接收者都立即返回，后来的 goroutine 也能感知取消；发一条消息只能唤醒一个接收者，还得处理"消息被消费掉"的竞态。关闭语义天然匹配"状态"而非"事件"。

**Q：WithTimeout 的定时器是谁在计时？会不会泄漏？**

A：到点前由 runtime 定时器持有取消回调；提前调用 `cancel()`（哪怕只是 defer）会释放定时器。海量短请求里不 defer cancel，定时器会堆积——这是 ctx 泄漏最常见的形态，`goleak`/goroutine 数监控能抓到。

**Q：父 ctx 没取消、子 ctx 超时，父会受影响吗？**

A：不会。传播是单向向下的：父取消波及所有后代，后代到点只结束自己这条分支。这正是一棵树的意义——每个请求、每个子操作有独立的截止时间，互不牵连。

**Q：取消是强制的吗？子 goroutine 能无视吗？**

A：能无视，context 只是通知机制不是抢占。Done 关闭后 goroutine 若不 select 它，照样跑到自然结束——所以 demo 里每个子任务都把 `<-ctx.Done()` 写进 select，且用 WaitGroup 对账"确实退出了"。

终止权永远在 goroutine 自己手里，context 只负责把信号可靠送达；取消送达后，上层接下来要面对的是失败处理，错误的基础语义见 [23_error_basics](../23_error_basics/)。
