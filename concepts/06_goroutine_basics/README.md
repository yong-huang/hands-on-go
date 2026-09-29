# 06 · Goroutine 基础：go 只管启动，WaitGroup 负责等待

> `go f()` 五个字母就能并发一个函数调用，但它只是"启动"，不是"等待"——main 一退出，所有还没跑完的 goroutine 直接被带走，一行输出都可能来不及打印。`sync.WaitGroup` 是标准库里"等一批 goroutine 干完活"的计数器。本篇用 10 个 goroutine 各打印编号的 demo，把 Add/Done/Wait 的配对规则跑出来，读完你能解释为什么 goroutine"一条都没打印"、两种 Add 写法各用在什么时候。

## Background

`go worker(i)` 执行完立即返回，main 继续往下走；如果这行是 main 函数的最后一行逻辑，函数一返回整个进程退出，还没被调度的 goroutine 全部消失。初学者最常见的现象就是"goroutine 怎么一条都没打印"——不是没启动，是没人等它。

在没有等待机制时，人们用过两类土办法：`time.Sleep(时间)` 凑时长——睡短了丢输出、睡长了白等；或者用 channel 手工传完成信号——能用，但"等一批全部完成"这种纯同步场景写起来啰嗦。

标准库的 `sync.WaitGroup` 用一个原子计数器解决这个问题：启动前 Add，完成时 Done，main 在 Wait 上阻塞到计数归零。原子（多 goroutine 并发读写时仍然保证整体一致的操作）保证了计数在并发减一时不出错。

## What

一句话定义：goroutine 是 Go 运行时调度的轻量执行单元，`go` 语句只负责启动它；WaitGroup 是标准库提供的等待计数器，负责"等一批 goroutine 全部完成"。

心智模型一句话：WaitGroup 是 main 与 10 个 goroutine 之间的计数器——main 先加满（Add）、再放人（go）、然后在 Wait 上等；每个 goroutine 完成时减一（Done），减到零，main 解除阻塞。10 个 goroutine 的完成顺序没有任何保证。

可以把 WaitGroup 想象成餐厅的等位叫号屏：一桌客人走一位，屏幕减一，减到零店员才收工。但和真实叫号屏不同的是，屏幕本身不会替你登记——先按下号码（Add）再放客入座（go），顺序反了店员可能提前收工。

## When to Use

先说在做什么事的时候用这套机制：

- 在并发处理一批互相独立的任务（并发抓 10 个 URL、并发校验一批文件）：每个任务一个 goroutine，main 用 Wait 收口。
- 在启动数量事先已知的场景：循环外一次性 `Add(n)` 最省事。
- 在边生产边启动的场景（任务从 channel 持续到达）：每个 `go` 之前紧挨着 `Add(1)`。

何时不用 WaitGroup：goroutine 之间要传结果数据时，channel 一并承担同步职责，不必再叠一层计数器；单个后台任务且不在乎是否跑完时，连等待都可以省。

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `sync.WaitGroup` | 纯计数、等"全部完成" | 一批独立任务收口 |
| channel 关闭广播 | 还能携带数据流动 | goroutine 间传结果 + 顺便同步 |
| `time.Sleep` 猜时长 | 无同步语义 | 都不该用于等待（调试凑合） |
| `errgroup.Group` | WaitGroup + 首个错误收集 | 需要知道"谁失败了" |

## Quick Start

前置条件：本机安装 Go 工具链即可，demo 只用标准库。

```bash
cd concepts/06_goroutine_basics
go run main.go
```

输出节选（完成顺序每次运行都可能不同，以下仅为一次真实运行的快照）：

```text
========== 1. 循环外一次性 Add(10) ==========
  goroutine 10 完成
  goroutine 6 完成
  goroutine 7 完成
  goroutine 8 完成
  goroutine 9 完成
  goroutine 2 完成
  ...（略）
main: 10 个 goroutine 全部完成（完成顺序每次运行可能不同）

========== 2. 循环内逐个 Add(1) ==========
  goroutine 10 完成
  goroutine 5 完成
  ...（略）
main: 第二批 10 个也全部完成

========== 总结 ==========
1. go f() 只是启动并发，main 不等它；不等待 = goroutine 随 main 一起消失
2. WaitGroup 三件套配对：Add(n) 在 go 之前，Done 用 defer，Wait 阻塞到归零
3. Add(10) 一次到位 vs 循环内 Add(1)，两种姿势都对，关键是先 Add 后 go
4. goroutine 的调度顺序没有保证，完成顺序每次运行都可能不同
```

第三段是程序自己打印的要点清单。建议把 demo 多跑几次，亲眼确认两批的完成编号顺序都会变化。

## How It Works

### go 关键字：启动不等于等待

`go worker(i, &wg)` 在当前 goroutine 里对参数求值并拷贝，然后把它交给运行时调度，语句本身立即返回。它不产生返回值、不阻塞、不报错——goroutine 没跑完 main 就退出，它们就静默消失。

输出里每行"goroutine x 完成"能稳定出现，靠的不是 go 语句本身，而是后面的 `wg.Wait()` 把 main 拦到了计数归零。

### WaitGroup 的计数器模型

`Add(n)` 把计数加 n，`Done()` 等价于 `Add(-1)`，`Wait()` 阻塞到计数归零。

三条纪律：Add 必须发生在对应 go 语句之前（否则 Wait 可能在 Add 之前就看到 0 而提前放行）；Done 用 defer（保证哪怕函数中途 return 也计数必达）；计数不能为负（多 Done 一次直接 panic：`sync: negative WaitGroup counter`）。

### 两种 Add 姿势

启动数量已知时，循环外一次性 `wg.Add(10)`；边生产边启动（比如从 channel 收任务）时，循环内每个 go 之前 `wg.Add(1)`。两种都对，共同点是"先 Add 后 go"。

输出第 1、2 段分别演示这两种姿势，两批都正确等到了全部完成——差异只在 Add 的时机，不在结果。

### 编号经参数传递：避开闭包陷阱

`go worker(i, &wg)` 里 `i` 在 go 语句执行时就被求值拷贝。

若改成在函数体里直接引用外层循环变量 `i`，所有 goroutine 可能共享同一个变量、全部打出同一个值——这是经典的闭包陷阱（函数捕获外层变量引用而非值导致的行为），成因与三种修法见 [07_closure_trap](../07_closure_trap/)。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `go worker(i, &wg)` | 启动并发，立即返回 | 参数当场拷贝；不等待完成 |
| `wg.Add(10)` | 计数 +10 | 必须在 go 语句之前 |
| `defer wg.Done()` | 计数 -1 | defer 保证必达，等于 `Add(-1)` |
| `wg.Wait()` | 阻塞到计数归零 | main 不 Wait = goroutine 随退出消失 |
| `&wg` 传指针 | WaitGroup 不要拷贝 | 拷贝后 Done 减的是副本，Wait 永远等不到 |

## Pitfalls & Q&A

踩坑清单（每条都可在 demo 里复现，按"现象 → 原因 → 解法"展开）：

- 现象：删掉 `wg.Wait()` 后，程序大概率一行 "goroutine x 完成" 都不打印就退出。原因：go 只管启动，main 退出带走全部 goroutine。解法：在需要收口的位置调 `wg.Wait()`。
- 现象：`Add` 放到 goroutine 内部第一行，Wait 提前放行。原因：Wait 只看计数器当前值，Add 晚于 Wait 观察就是时序竞态。解法：坚持"先 Add 后 go"。
- 现象：`Done` 次数多于 `Add`，程序 panic `sync: negative WaitGroup counter`。原因：计数不能为负。解法：核对 Add 与 Done 的配对次数。
- 现象：WaitGroup 按值传给 goroutine，main 的 Wait 死等。原因：计数减在副本上，原计数不动。解法：始终传 `&wg` 指针。
- 现象：以为输出顺序等于启动顺序。原因：goroutine 的调度顺序没有保证，完成顺序每次运行都可能不同。解法：需要顺序就收集结果后显式排序，不依赖调度。

深入问答：

**Q：为什么 Add 必须在 go 语句之前？**
A：Wait 只看计数器当前值。如果 Add 在 goroutine 内部才执行，main 可能在计数还是 0 时就通过了 Wait——"先 Add 后 go"保证 Wait 开始前计数已就位。循环内 Add(1) 同理：Add 与 go 紧挨着，且 Add 在前。

**Q：WaitGroup 和 channel 等待怎么选？**
A：只关心"全部完成"用 WaitGroup 最直白；如果要在 goroutine 之间传结果数据，channel 一并承担同步职责（`for range` 到关闭为止也是等待）。

两者不冲突，`11_worker_pool`、`18_producer_consumer` 里是组合用法。

**Q：Done 能不用 defer 吗？**
A：能，但不该。函数路径一多（提前 return、panic），手动 Done 极易漏调，结果就是 Wait 死等。defer 把"必达"交给语言机制，代价几乎为零。

**Q：WaitGroup 能复用吗？**
A：能，但有纪律：一轮 Wait 返回之后才能开始下一轮 Add。Wait 还没返回就 Add 会与减计数竞争，典型替代是每轮任务用一个新实例。

WaitGroup 的底层原理（计数器的原子实现、Wait 的挂起与唤醒）见 [../../internals/10_waitgroup/](../../internals/10_waitgroup/)。
