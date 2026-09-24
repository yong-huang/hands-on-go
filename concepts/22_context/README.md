# 22 · Context：一次 cancel，整棵 goroutine 树同时退出

> goroutine 好开难收：一个请求扇出十个子任务，谁负责告诉它们"不用干了"？往每个函数里塞一个 `chan struct{}` 越传越乱。`context.Context` 是 Go 的标准答案——把取消信号、截止时间、请求级数据装进一个值，沿着调用树向下传，父节点一声令下，所有子 goroutine 的 `Done()` 同时关闭。本实验用可运行的 demo 把 WithCancel、WithTimeout、WithValue 与树形传播逐个跑给你看。

## 1. 为什么需要它

没有 context 时，"取消"要自己发明：给每个函数加一个 quit channel，层层手工传递、逐个 close——容易漏、无法表达"最多再等 1.5 秒"这种截止语义，也无法携带请求级元数据。context 把这三件事收敛为一个参数（惯例上永远是函数的第一个参数 `ctx`）：**取消是广播**（父 cancel，整棵子树同时收到）、**超时只是到点自动 cancel**、**值是旁路**（不占用函数参数）。标准库的 net/http、database/sql 全部基于它。

## 2. 总览：核心机制一图看懂

![context 取消传播：一次 cancel，整棵树同时收到 Done](images/ctx.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/22_context/images/ctx.html)
> （或本地打开 [`images/ctx.html`](images/ctx.html)）。

**心智模型一句话**：context 是一棵单向广播树——取消与超时只从父流向子，`Done()` 是同一时刻关闭的 channel，`Err()` 告诉你为什么退出，`Value()` 是不走参数的旁路。左起 parent 持有取消权，向下派生 ctx，子/孙 goroutine 靠 select `Done()` 退出，最后 WaitGroup 对账。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 WithCancel：手动取消

`ctx, cancel := context.WithCancel(parent)` 返回派生 ctx 与取消函数。子 goroutine 用 `select { case <-ctx.Done(): ... }` 阻塞等待；父调用 `cancel()` 后 Done 关闭，`ctx.Err()` 返回 `context.Canceled`。`cancel` 可以并发调用、可以重复调用（幂等），惯例写 `defer cancel()` 防泄漏。

### 4.2 WithTimeout / WithDeadline：到点自动取消

`ctx, cancel := context.WithTimeout(parent, 1500*time.Millisecond)` 等价于"1.5 秒后帮你调 cancel"。任务要跑 3 秒，但 select 先等到 Done 关闭，`Err()` 是 `context.DeadlineExceeded`——与手动取消可区分，任务据此决定是"被叫停"还是"超时报错"。`defer cancel()` 仍然要写：提前完成时释放定时器资源。

### 4.3 WithValue：请求级数据旁路

`ctx = context.WithValue(ctx, key, "alice")`，子层用 `ctx.Value(key)` 取回。键必须是自定义类型（本实验用 `type ctxKey string`），避免不同包的字符串键互相冲突。它只放**请求级元数据**（trace ID、登录用户），不是函数参数的替代品——编译器不检查 Value，拿错键只会静默得到 nil。

### 4.4 树形传播与 WaitGroup 对账

从同一个父派生的 ctx 共享取消 fate：根 `cancel()` 一次，子与孙的 Done 在同一时刻关闭，不需要父逐个通知。这正是"取消广播"的价值——新增一层调用只要继续传 ctx，取消语义自动继承。是否退出仍由 goroutine 自己决定（监听 Done），`sync.WaitGroup` 用来**对账**：Add 了多少个，Wait 就必须等到 Done 多少次，程序才不会悬挂或 panic（Done 多调会 negative counter panic，demo 修复过这一版）。

## 5. 关键字段与坑清单

| API | 作用 | 备注 |
| --- | --- | --- |
| `context.Background()` | 根 ctx，main/请求入口用 | 永不取消 |
| `WithCancel(parent)` | 手动取消 | 返回的 `cancel` 必须调用 |
| `WithTimeout(parent, d)` | d 后自动 cancel | `Err()==DeadlineExceeded` |
| `WithDeadline(parent, t)` | 到指定时刻自动 cancel | 同上 |
| `WithValue(parent, k, v)` | 旁路携带值 | 键用自定义类型 |
| `ctx.Done()` | `<-chan struct{}`，取消即关闭 | select 监听它 |
| `ctx.Err()` | `Canceled` / `DeadlineExceeded` | Done 关闭后非 nil |

坑清单（每条可在 demo 里复现或注释复现）：

1. **忘记调 cancel**：派生 ctx 及其定时器永远不释放（ctx 泄漏），子 goroutine 永远阻塞在 Done——所以惯例 `defer cancel()`。
2. **goroutine 里直接干活不监听 Done**：父取消了它还在跑，"取消传播"对它无效；必须 `select ctx.Done()`。
3. **用 ctx.Value 传业务参数**：类型不安全、不可见，IDE 和编译器都帮不上忙；只放请求级元数据。
4. **WaitGroup Done 与 Add 不对账**：多 Done 一次立刻 panic（`sync: negative WaitGroup counter`），少 Done 一次 Wait 永久阻塞。
5. **把 ctx 存进 struct 长期持有**：ctx 表示一次调用的生命周期，跨请求复用会取消错人。

## 6. 文件结构

```text
concepts/22_context/
├── README.md              # 本文档：八节结构
├── main.go                # 演示程序：四段 demo，约 2~3 秒跑完，go vet 通过
└── images/
    ├── ctx.sequence.json  # 时序图图源（图管线的诊断对象）
    ├── ctx.html           # 交互式时序图（可在浏览器中打开探索）
    └── ctx.svg            # 静态导出，README §2 内嵌的就是它
```

## 7. 深入要点

**Q1：cancel() 之后 Done 为什么是"关闭"而不是"发一条消息"？**
channel 关闭是**粘性**的：关闭后所有现在和将来的接收者都立即返回，后来的 goroutine 也能感知取消；发一条消息只能唤醒一个接收者，还得处理"消息被消费掉"的竞态。关闭语义天然匹配"状态"而非"事件"。

**Q2：WithTimeout 的定时器是谁在计时？会不会泄漏？**
到点前由 runtime 定时器持有取消回调；提前调用 `cancel()`（哪怕只是 defer）会释放定时器。海量短请求里不 defer cancel，定時器会堆积——这是 ctx 泄漏最常见的形态，`goleak`/goroutine 数监控能抓到。

**Q3：父 ctx 没取消、子 ctx 超时，父会受影响吗？**
不会。传播是**单向向下**的：父取消波及所有后代，后代到点只结束自己这条分支。这正是一棵树的意义——每个请求、每个子操作有独立的截止时间，互不牵连。

**Q4：取消是强制的吗？子 goroutine 能无视吗？**
能无视，context 只是通知机制不是抢占。Done 关闭后 goroutine 若不 select 它，照样跑到自然结束——所以 demo 里每个子任务都把 `<-ctx.Done()` 写进 select，且用 WaitGroup 对账"确实退出了"。终止权永远在 goroutine 自己手里，context 只负责把信号可靠送达。

## 8. 总结

context 把"取消、截止、元数据"三件跨 goroutine 的麻烦事收敛成一个沿调用树传递的参数：`Done()` 收信号、`Err()` 辨原因、`Value()` 走旁路、WaitGroup 做对账。写并发代码时，凡是"可能要提前收工"的调用链，第一个参数就该是 `ctx`。取消之后常伴随"上层如何处理失败"——下一篇正是 error 的基础语义：[../23_error_basics/](../23_error_basics/)。
