# 25 · panic 与恢复：panic 不是异常处理，是"不可绕过的失败"

> panic/recover 是 Go 处理"程序已无法继续"这类失败的机制：panic 沿调用栈向上传播、逐层执行 defer，无人接住就终止整个进程；recover 是唯一的拦截手段，且只在 defer 函数里、只在本 goroutine 内生效。读完本篇你能写出 panic 转 error 的惯用法，并说清"主 goroutine 接不住子 goroutine 的 panic"这一经典陷阱的成因与解法。

## Background

Go 的常规错误处理靠 `error`（内建的错误接口类型，函数用它把失败当普通返回值上交）逐层传递：文件不存在、参数非法这类可预期失败，调用方拿到 error 自行处理。这条路的前提是"每一层都愿意配合返回"。

但有一类失败无法用返回值表达：程序自身 bug 破坏了不变量（程序任何时刻都必须成立的条件），比如切片越界、并发误用 map。runtime（Go 程序背后的支撑层，负责栈管理、调度与垃圾回收）对此的反应是触发 panic——立即中断当前函数，沿调用栈一路向上终止整个进程。

痛处在并发服务里最明显：一条"无关紧要"的请求 goroutine（Go 的轻量并发执行单元）触发 panic，整个 HTTP 服务跟着下线——哪怕其他请求毫无问题。没有恢复手段时，任何一条协程出错都等于全站事故。

Go 的答案是给这条崩溃路径开一个受控的口子：传播途中允许 defer（注册为"函数返回前必执行"的延迟调用）里调用 recover 把 panic 拦下来，拦不住的仍然崩。这也是 Go 社区"能 error 不 panic"纪律的由来。

## What

panic 是 Go 表示"不可恢复失败"的内建信号（只能向上传播、不能当返回值传递的异常状态），recover 是唯一能在传播途中把它拦下并转回正常控制流的内建函数。二者合起来构成 Go 唯一跨函数的"非常规控制流"。

**心智模型一句话**：panic 是沿当前 goroutine 调用栈向上传播的"紧急出口"，defer 是出口上的必经检查站，recover 只在检查站里有效；跨 goroutine 没有出口——子 goroutine 的 panic 必须由子 goroutine 自己的 defer recover 拦截。

可以把这套机制想象成大楼的消防通道：每层（每个函数）撤离时都要经过本层的检查站（defer），只有检查站里的值班员（recover）能宣布"虚惊一场，回去上班"。但和真实消防通道不同的是，通道只存在于发起警报的那栋楼（goroutine）里，隔壁楼的检查站帮不上忙。

## When to Use

- 写 HTTP/RPC 服务时：在请求处理边界用 recover 把 handler 的 panic 转成 500 error，让一条坏请求不至于带崩整个进程。
- 启动并发 worker 时：谁启动 goroutine，谁就给它套一层 safeCall 式兜底，任务失败以 error 形式送回主流程，不影响其他 worker。
- 写库的内部断言时：检测到调用方误用（不变量被破坏）就 panic 快速失败，同时在库的公开入口 recover 转 error。

何时不用：

- 失败可预期（文件不存在、输入非法、网络超时）：返回 error，用 panic 会掩盖本该处理的分支。
- 日常控制流：recover 不是 try/catch（其他语言"捕获异常后继续"的惯用结构），把 panic 当异常处理用，等于把 bug 藏进日志。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 返回 `error` | 显式、可组合，能被 `errors.Is/As` 判定 | 可预期的操作失败一律选它 |
| `panic`/`recover` | 立即中断、逐层执行 defer，只在本 goroutine 可拦 | 不变量被破坏的 bug、库边界兜底 |
| `log.Fatal`/`os.Exit` | 直接终止进程，defer 不再执行 | 初始化失败等无恢复意义的场合 |

## Quick Start

前置条件：本机已安装 Go 工具链（`go run` 可用），无第三方依赖。

```bash
cd concepts/25_panic_recover
go run main.go
```

真实输出（节选）：

```text
========== 1. safeCall：panic → error 惯用法 ==========
risky(): recovered: boom | 已转成 error: true
calm(): <nil> (正常返回，err 为 nil)
main 仍在继续执行 —— 捕获后程序不崩
========== 3. 无 panic 时 recover() 返回 nil ==========
noPanic 正常运行，没人 panic
defer 中 recover(): <nil> | 是 nil: true
========== 4. 跨 goroutine：主 goroutine 接不住子 goroutine 的 panic ==========
子 goroutine 内部捕获: child died
程序正常收尾 —— 子 goroutine 内部 recover 后主流程安然无恙
```

输出从第 1 段直接跳到第 3 段，是节选所致：第 2 段只打印一行提示。诚实预期是程序以退出码 0 正常结束——未捕获 panic 的场景被注释保护，取消 main.go 第 2 节的注释重跑，才会看到 `panic: boom`、栈回溯和 `exit status 2`。

核心实现只有 safeCall 一个函数。这段在做什么：defer 里 recover 捕获 panic 值，用命名返回值 err 把它格式化成普通 error 带回给调用方：

```go
// safeCall 把任意函数包起来：panic 被拦截并转成 error（Go 社区惯用法）。
func safeCall(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	fn()
	return nil
}
```

## How It Works

### panic 的传播路径

`panic(v)` 立即中断当前函数，然后沿调用栈向上传播：每上一层，先执行该层已注册的 defer，再继续上抛。panic 路径上只有 defer 会执行——这就是 recover 必须写在 defer 里的根本原因。

如果一路传到 goroutine 入口都没人 recover，runtime 打印 `panic: ...` 加栈回溯（stack trace，崩溃时打印的函数调用链），进程以非零码退出（`exit status 2`）。

main.go 第 2 节用注释演示了这个场景，取消注释即可复现。进程收到信号（SIGSEGV/SIGINT）时 runtime 另有一套处理，见 [../../internals/20_signal](../../internals/20_signal)。

### recover 的生效条件

`recover()` 只有在本 goroutine 的 defer 函数中直接调用才生效，三个条件缺一不可：

- 在 defer 的函数里调用（不在 defer 里调用恒返回 nil）；
- 与 panic 在同一个 goroutine；
- recover 一旦返回非 nil，panic 即被"消费"，控制流从 defer 所在函数正常返回。

没有 panic 时调用 `recover()` 完全无害，只返回 nil。你在输出第 3 段看到的 `defer 中 recover(): <nil> | 是 nil: true` 验证的就是这一点，也是 safeCall 里 `if r := recover(); r != nil` 写法的由来。

### safeCall：panic 转 error 惯用法

Quick Start 里的 safeCall 是标准库反复使用的模式：defer 里 recover，把 panic 值格式化成 error，用命名返回值带回。

调用方拿到的仍是普通的 `error`，可以继续用 `errors.Is/As` 处理——panic 被压平进了 Go 的常规错误通道。`encoding/json`、`net/http` 的连接处理都在边界用了这一招。

输出第 1 段的 `risky(): recovered: boom | 已转成 error: true` 正是它工作的痕迹：panic 值 `boom` 成了 err 的内容。

命名返回值在这里不可省略——普通返回值在 defer 里改不到。defer 与命名返回值的更多配合见 [../04_funcs_defer](../04_funcs_defer)。

### 跨 goroutine：panic 不跨栈，谁启动谁兜底

panic 只沿当前 goroutine 的栈传播。每条 goroutine 栈独立展开，主 goroutine 的 defer 挂在另一条栈上，panic 到不了那里；某条栈的 panic 无人接时，runtime 打印栈回溯并 `exit(2)`，整个进程所有 goroutine 一起终止。

main.go 第 4 节的演示：去掉子 goroutine 内部的 defer recover，进程会整体崩溃，main 里注册再多 recover 也拦不住。

输出第 4 段的 `子 goroutine 内部捕获: child died` 来自子 goroutine 自己的兜底——它 recover 后把 error 通过 channel 送回主流程。

正确纪律是"谁启动 goroutine，谁就给它套一层 safeCall 式兜底"。

### 关键写法速查

| 写法 | 语义 |
| --- | --- |
| `panic(v)` | 抛出，v 可以是任意类型（惯用 `error` 或字符串） |
| `recover()` | 仅在 defer 函数中生效，返回 panic 值；无 panic 返回 nil |
| `func f() (err error)` | 命名返回值，defer 里 recover 后可直接改写 err |
| `defer func(){...}()` | defer 必须是函数值，panic 时依然执行（LIFO，后进先出） |

## Pitfalls & Q&A

踩坑清单（每条均可复现）：

- **在函数体里直接调 recover**。现象：`recover()` 永远返回 nil，拦不住任何 panic。原因：panic 传播路径上只有 defer 会执行，普通代码不在路径上。解法：把 recover 放进 `defer func(){...}()`。
- **panic 裸值难定位**。现象：`panic("boom")` 上抛后栈回溯里只有一个字符串，没有上下文。解法：惯用 `panic(fmt.Errorf(...))` 携带上下文，或干脆返回 error。
- **recover 后吞掉现场**。现象：捕获后程序继续跑，bug 现场却丢了。原因：recover 不保留栈信息。解法：基础设施代码 recover 后记日志（可用 `runtime.Stack`）再转 500，或重新上报。
- **以为主 goroutine 能兜底子 goroutine**。现象：main 里注册了 recover，子 goroutine panic 仍崩掉整个进程。原因：panic 只在发起它的那条栈上展开。解法：谁启动 goroutine，谁就在子 goroutine 内部套 safeCall 式兜底。
- **recover 当 try/catch 用**。现象：能预判的失败（文件不存在、参数非法）被吞进 recover，错误分支失去类型信息。解法：可预见的失败一律返回 error，panic 留给不变量被破坏的场合。

**Q：recover 之后，panic 点下面的栈怎么处理？**

不回去了。recover 不恢复现场，控制流从注册 defer 的那个函数正常返回（命名返回值可携带结果），panic 点以下的帧已经展开完毕。所以 recover 后的代码只能活在 defer 函数里，`fn()` 之后的语句不会执行。

**Q：recover() 返回 nil 一定是"没有 panic"吗？**

还有一种情况：panic 的值本身就是 nil（`panic(nil)`）。

Go 1.21 起会把 `panic(nil)` 换成 `*runtime.PanicNilError`，recover 拿到非 nil，避免"recover 到 nil 却处于恢复态"的歧义；旧版本里 `recover() != nil` 判断会漏掉这种情况。

**Q：什么时候该 panic，什么时候该返回 error？**

可预见的操作失败（网络、输入、资源缺失）永远返回 error；panic 留给"不变量被破坏、继续跑就是 bug"的场合，如切片越界、并发误用 map。库代码的纪律是"内部可以 panic，边界必须 recover 转 error"，参考 `encoding/json` 的实现。
