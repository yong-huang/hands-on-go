# 08 · Goroutine 泄漏检测：让泄漏在 NumGoroutine 里现形

> goroutine 没有父对象等它结束——你 `go` 出去之后，谁也不负责回收它。泄漏的本质只有一句话：goroutine 永久阻塞在一个"再也不会就绪"的操作上，或跑在一个永不返回的循环里。本实验用 `runtime.NumGoroutine()` 当仪表盘，把三种典型泄漏态和各自的修复对照逐段跑给你看。

## 1. 为什么需要它

goroutine 泄漏不会崩溃、不会报错，只是让计数悄悄上涨、内存慢慢变多——直到服务长跑几天后 OOM。没有观测手段时，你甚至无法确认"是不是漏了"。本实验给出的方法论是：**先记基线 → 制造嫌疑操作 → 等一个观测窗口再读数 → +N 不回落即泄漏**。每段泄漏立刻跟一段修复版，数字回落就是修复生效的证据。

## 2. 总览：核心机制一图看懂

![Goroutine 生命周期：正常退出 vs 三种泄漏态](images/leak.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/08_leak_detect/images/leak.html)
> （或本地打开 [`images/leak.html`](images/leak.html)）。

**心智模型一句话**：goroutine 只有"返回了才算结束"一条正常出口；发送阻塞、接收阻塞、死循环三种泄漏态都卡在出口之前，而观测的手段就是泄漏前后 `runtime.NumGoroutine()` 的差值不回落。

## 3. 快速开始

```bash
cd concepts/08_leak_detect
go run main.go
```

预期输出（节选；基线数值随机器/调度逐次不同，但每段增量 +1/+0 的形态稳定）：

```text
========== 1. 发送阻塞：ch <- v 而无人接收 ==========
  基线 goroutine 数: 1
  泄漏版 +100ms: 2 个 (+1 不回落，goroutine 永久阻塞在发送上)
  修复版 +100ms: 2 个 (+0，缓冲吸收了那次发送，goroutine 已退出)

========== 2. 接收阻塞：<-ch 而无人发送 ==========
  泄漏版 +100ms: 3 个 (+1 不回落，goroutine 永久阻塞在接收上)
  修复版 +100ms: 3 个 (+0，发送方 close 解除阻塞，数量回落)

========== 3. 死循环：for {} 缺退出条件 ==========
  泄漏版 +100ms: 4 个 (+1 不回落，死循环永不返回)
  cancel +100ms: 4 个 (+0，ctx 取消让 goroutine 主动退出)

========== 总结 ==========
3. 三段净残留合计 = 3（每段泄漏态 +1、修复后归零；服务长跑时这是线性累积的）
```

注意：绝对数值（基线 1/2/3……）随运行环境有毫秒级波动，请以**增量**（+1 不回落 / +0 回落）为准。

## 4. 核心概念

### 4.1 基线 → 观测窗口 → 差值

`runtime.NumGoroutine()` 返回全进程存活 goroutine 数。单次读数没有意义，有意义的是**差值**：操作前记基线，`time.Sleep(100*time.Millisecond)` 给调度器留出窗口后再读，若增量 >0 且不再回落，就是泄漏的最小可复现证据。100ms 是演示用的经验窗口，生产里通常在压测前后各读一次做对比。

### 4.2 泄漏态一：发送阻塞

`ch := make(chan int); ch <- 1`——无缓冲 channel 的发送会一直挂起，直到有接收者。接收者永远不来，goroutine 就永久停在 `Gwaiting`。修复：给缓冲 `make(chan int, 1)`（发送即完成），或保证对端存在（如 `select` + 超时/退出信号）。

### 4.3 泄漏态二：接收阻塞

`<-make(chan int)`——无缓冲 channel 的接收同样挂起等待发送者。修复：由**发送方**负责 `close(ch)`，接收方读到零值（`v := <-ch` 得 `0`）后自然继续/退出；这也是 `for range ch` 能随 close 退出的前提。

### 4.4 泄漏态三：死循环

`for {}` 没有退出条件，goroutine 永远处于运行态，连阻塞都算不上——它一直消耗调度时间片。修复：`select` 监听 `ctx.Done()`，`cancel()` 一调用就从 `Done` 分支 `return`，计数回落。这是 `context` 取消传播的标准用法。

## 5. 关键字段与坑清单

| API / 语法 | 含义 | 为什么重要 |
|---|---|---|
| `runtime.NumGoroutine()` | 当前存活 goroutine 总数 | 零依赖的泄漏仪表盘 |
| `ch <- v`（无缓冲） | 阻塞直到接收者就绪 | 无人收 = 永久挂起 |
| `<-ch`（无缓冲） | 阻塞直到发送者就绪 | 无人发且不 close = 永久挂起 |
| `close(ch)` | 广播"不会再发了" | 接收方据此退出循环/继续执行 |
| `ctx.Done()` | 取消信号 channel | 死循环/长任务的唯一体面出口 |
| `select { case <-ctx.Done(): return ... }` | 可取消的循环骨架 | 把退出条件显式化 |

坑清单（均可在 demo 中复现）：

- 泄漏的 goroutine 不会被 GC 回收——它阻塞在活跃的 channel 引用上，channel 也随之无法回收（连带泄漏）
- `NumGoroutine` 读的是**全进程**计数；并发段多时差值会互相污染，观测窗口内别混入其他 goroutine
- 修发送阻塞用缓冲只是 demo 手段：缓冲大小 ≠ 接收能力，容量满后照样阻塞
- `for range ch` 只在 **close** 后退出，仅"不再发送"不够——这是接收阻塞泄漏最常见的来源
- 死循环里的 `default` 分支如果不检查 `ctx.Done()`，取消信号永远没机会被处理

## 6. 文件结构

```text
concepts/08_leak_detect/
├── README.md                 # 本文件：原理 + 运行指引 + 深入要点
├── main.go                   # 三段泄漏/修复对照 demo，runtime.NumGoroutine 观测
└── images/
    ├── leak.lifecycle.json   # 图源：正常退出主轴 vs 三种泄漏态（生命周期图）
    ├── leak.html             # 交互版：自包含单文件，可缩放/切换主题
    └── leak.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么泄漏的 goroutine 不能被 GC 回收？**
A：GC 判断"不可达"才回收，而阻塞中的 goroutine 栈上还引用着 channel，channel 又被彼此引用——整条引用链从"正在等待"的 goroutine 出发全是可达的。只要它没返回，这套对象就一个都释放不掉。

**Q：`NumGoroutine` 不涨就一定没泄漏吗？**
A：不一定。若泄漏的 goroutine 先崩溃退出、或泄漏与退出速率相抵，计数会持平。计数稳定增长是最强信号；定位具体漏在哪要用 pprof 的 goroutine profile（`/debug/pprof/goroutine`）看阻塞栈分布。

**Q：给 channel 加缓冲算不算根治发送泄漏？**
A：不算根治，只是消化了这一次发送。生产代码的正确姿势是 `select { case ch <- v: case <-ctx.Done(): }`——发送与退出信号同时待命，对端消失也能走人。缓冲适合"确定有人消费、只是节奏不同步"的场景。

**Q：`select{}` 空选择和 `for{}` 有什么区别？**
A：`select{}` 没有任何 case 会永久阻塞（Gwaiting，不占 CPU）；`for{}` 是忙等（一直 G-running，烧时间片）。两者都是泄漏，但死循环危害更大——它直接偷走本可用于其他 goroutine 的调度配额。

**Q：main 退出后泄漏的 goroutine 去哪了？**
A：main 返回即进程退出，所有 goroutine 被整体丢弃，没有等待、没有优雅关闭。所以 demo 里泄漏不会累积到崩溃——但在常驻服务里没有"main 退出"兜底，泄漏就是纯粹的慢性死亡。

## 8. 总结

泄漏检测的最小闭环是：**基线 → 制造嫌疑 → 观测窗口 → 差值不回落**。三种泄漏态（发送阻塞、接收阻塞、死循环）对应三种修复姿势（缓冲/对端 close、发送方 close、ctx 取消），共同原则只有一条：**每个 `go` 出去的函数都必须有一条保证可达的返回路径**。

想看泄漏在运行时内部的完整成因与 pprof 实测定位，深入篇见 [internals/29_leak](../../../internals/29_leak/)；下一篇 [09_runtime_info](../09_runtime_info/) 继续玩 runtime 包：`runtime.Stack` 打印调用栈、`Goexit` 触发 defer、以及 goid 的获取姿势。
