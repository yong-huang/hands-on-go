# 09 · Goroutine 栈与 runtime 信息：让程序看见自己

> goroutine 没有名字、没有 id、没有"我是谁"的 API——这是 Go 刻意的设计。但调试泄漏、排查调度问题时，你又确实需要"看一眼"运行时：当前调用栈长什么样、goroutine 怎么在不经 return 的情况下体面退场、栈什么时候被回收。本实验用一份可运行的 demo 跑通 `runtime.Stack`、`runtime.Goexit` 与 goid 解析这三件事。

## 1. 为什么需要它

出了并发 bug，你最想问的是"现在到底有哪些 goroutine、各自卡在哪一行？"——`runtime.Stack(buf, true)` 一次 dump 全部，就是答案。想从一个"陷在半路"的 goroutine 里退出来，又不想 panic、不想层层 return——`runtime.Goexit()` 就是为此而生，而且 defer 照常执行。至于"给 goroutine 编个号"的冲动，本实验会演示黑客做法并解释为什么 runtime 刻意不给你公开 API。

## 2. 总览：核心机制一图看懂

![栈与 runtime 信息：Stack 打印 / Goexit 触发 defer / goid 解析](images/rtinfo.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/09_runtime_info/images/rtinfo.html)
> （或本地打开 [`images/rtinfo.html`](images/rtinfo.html)）。

**心智模型一句话**：runtime 是程序观测自身的镜子——`Stack` 把当前 goroutine 的调用帧"印"出来，`Goexit` 走与 return 相同的 defer 清理路径但只结束当前 goroutine，goid 则只能从 `Stack` 输出的首行"偷"出来。

## 3. 快速开始

```bash
cd concepts/09_runtime_info
go run main.go
```

预期输出（节选；栈帧路径、行号、goid 与字节数每次运行可能不同）：

```text
========== 1. runtime.Stack：打印当前 goroutine 调用栈 ==========
goroutine 1 [running]:
main.main()
	/path/to/concepts/09_runtime_info/main.go:54 +0x7c
...(共 130 字节，栈帧地址每次运行不同)

========== 2. runtime.Goexit：提前退出，defer 照常执行 ==========
  [worker] defer 执行了——Goexit 不跳过 defer
  [main] worker 已被 Goexit 结束：defer 执行了，后面的代码没执行

========== 3. goid：没有公开 API，只能从栈首行解析 ==========
  解析到当前 goroutine id = 1（每次运行可能不同）
  runtime 刻意不暴露 goid：id 会复用、语义不稳定；
  生产代码给 goroutine 带"身份"请用 context.WithValue 传递。
```

## 4. 核心概念

### 4.1 runtime.Stack：把调用栈"印"进缓冲区

`n := runtime.Stack(buf, all bool)` 把栈文本写入 `buf` 并返回字节数。`all=false` 只打当前 goroutine，`all=true` 打印**全部** goroutine 的栈——这是排查泄漏/死锁最常用的一招（pprof 的 goroutine profile 本质上就是它的增强版）。首行格式固定为 `goroutine <id> [状态]:`，后跟一行一个栈帧。

### 4.2 runtime.Goexit：体面地提前退场

`runtime.Goexit()` 终止**当前** goroutine：已注册的 defer 全部执行（与函数 return 走同一条清理路径），然后 goroutine 正常消失、栈被回收。三种"退出"的边界：

| 方式 | 影响范围 | defer 执行 | 可 recover |
|---|---|---|---|
| `return` | 当前函数 | 执行（ defer 本就是 return 的一部分） | — |
| `os.Exit` | **整个进程** | 全部不执行 | 否 |
| `panic` | 向上传播 | 沿途逐层执行 | 是 |
| `Goexit` | 当前 goroutine | 执行 | **否** |

### 4.3 goid：能"偷"到，但别当身份用

首行 `goroutine 1 [running]:` 的第二个字段就是 goid，`strings.Fields` 一拆就出来。但这是**黑客手段**：runtime 没有公开 API 是刻意设计——goid 会被复用、没有稳定性承诺、解析依赖栈输出的文本格式。要在 goroutine 之间传递"身份"，标准答案是 `context.WithValue`；要区分日志归属，传一个业务层的 traceID。

## 5. 关键字段与坑清单

| API / 写法 | 含义 | 为什么重要 |
|---|---|---|
| `runtime.Stack(buf, false)` | 当前 goroutine 栈写入 buf | 自我观测的最小入口 |
| `runtime.Stack(buf, true)` | 全部 goroutine 栈 | 泄漏/死锁排查首选 |
| `goroutine N [running]:` | 栈输出首行 | goid 唯一可见的来源 |
| `runtime.Goexit()` | 结束当前 goroutine | defer 照跑，不可 recover |
| `runtime.NumGoroutine()` | 当前 goroutine 数量 | 配合 Stack 做泄漏观测（见 [../08_leak_detect/](../08_leak_detect/)） |

坑清单：

- `os.Exit` 会跳过**所有** defer（包括 main 的）——进程级"消失术"，清理逻辑别只挂在 defer 上
- `recover()` 捕不到 `Goexit`：Goexit 不是 panic，进不了 recover 的作用域
- 在 main goroutine 里调 `Goexit` = main 提前结束，但**其他** goroutine 不会被等待
- 解析 goid 的代码在栈格式变化时可能失效——它是调试工具，不是业务依赖
- demo 里用 `close(exited)` 而不是发送值来通知 main：Goexit 之后"Goexit 下面那行"永远不会执行

## 6. 文件结构

```text
concepts/09_runtime_info/
├── README.md                 # 本文件：原理 + 运行指引 + 深入要点
├── main.go                   # demo：Stack 打印 / Goexit 与 defer / goid 解析
└── images/
    ├── rtinfo.sequence.json  # 图源：三层调用的时序图（Typed JSON IR）
    ├── rtinfo.html           # 交互版：自包含单文件，可缩放/切换主题
    └── rtinfo.svg            # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 runtime 不直接提供 `runtime.GoID()`？**
A：goid 会复用（goroutine 退出后编号可被新 goroutine 拿走）、没有跨平台稳定性承诺，一旦公开就会被当成协程身份滥用（做 goroutine-local storage 之类），破坏"显式传值"的并发模型。官方立场：需要身份就用 context 传。

**Q：`Goexit` 之后 goroutine 的栈去哪了？**
A：与正常 return 一样，g 对象被复用、栈内存归还或降级回收——不存在"Goexit 泄漏栈"的问题。泄漏发生在 goroutine **永远退不出去**的时候（见 [../08_leak_detect/](../08_leak_detect/)）。

**Q：`runtime.Stack(buf, true)` 会停住世界吗？**
A：dump 全部栈需要短暂 stop-the-world 并逐个回收各 goroutine 的帧；栈大、goroutine 多时开销可观，所以它适合诊断路径（HTTP debug endpoint、超时 dump），不适合热路径轮询。

**Q：demo 里 `parseGoid` 解析失败会怎样？**
A：返回 0 兜底。真实的调试工具（如社区惯用的 go-routine-id 库）会同时用 `reflect`/链接符号等手段交叉验证；本 demo 固定从 `runtime.Stack` 首行取，格式由 runtime 保证，目前非常稳定。

## 8. 总结

`runtime.Stack` 让你看见"现在谁在哪"，`Goexit` 让 goroutine 走得了体面的退场流程，而 goid 的"藏起来"本身就是设计——身份靠 context 显式传递，而不是靠运行时编号。把 §3 的输出与 §5 的坑清单对一遍，这组运行时工具就归你支配了。

想继续向下挖：goroutine 的栈是怎么按需生长的（2KB 初始、分段/连续栈）见 [internals/07_goroutine](../../internals/07_goroutine/)；下一篇 [10_gomaxprocs](../10_gomaxprocs/) 用实测数据看 GOMAXPROCS 如何控制真正的并行度。
