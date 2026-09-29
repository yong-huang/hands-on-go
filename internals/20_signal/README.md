# 20 · 信号处理与 panic 恢复：栈是怎么"一层层收回来"的

> 两个日用机制：**os/signal** 把硬中断（内核发给进程的异步通知）变成 channel 消息；**defer + recover** 在 panic（程序自行中断当前流程的运行时错误）时把调用栈一层层收回来。
>
> 本实验给自己发一个 SIGINT 走完信号管线，再让 6 层深递归在底层 panic、由 guard 层 recover 兜住，打印真实展开路径。读完你将能独立实现优雅退出与 panic 兜底。

## Background

线上服务总有两类"退出"。一类是收到 SIGTERM（kill 命令与 K8s（Kubernetes，容器编排系统）缩容发出的终止信号）后的优雅退出：要关连接、冲日志、留退出码。另一类是代码 panic 后的崩溃：runtime 沿 defer 链收栈，有人接住就恢复，无人接住进程终止。

这两类行为都藏在 runtime（Go 运行时）里。没有可复现的演示时，工程师只能靠文档记忆判断"recover 写在这里到底接不接得住"，判断成本高且容易出错。

本实验把两条链路做成可重复的演示：`signal.Notify` 把 SIGINT/SIGTERM 转成 channel 消息的完整链路；panic 从最深一层爆发、沿 defer 链 LIFO（后注册先执行）展开、到 guard 层被 recover 接住的全过程。

## What

**定义**：本篇实测两套机制——os/signal（把进程信号转成 channel 消息的标准库包）与 panic/recover（沿 defer 链逐层展开调用栈的错误处理机制）。前者解决"内核异步事件如何有序送达业务代码"，后者解决"深处的错误如何带回控制权与清理机会"。

**心智模型**：panic 不是"扔异常"，而是"逐层交还控制权"。runtime.gopanic（runtime 里驱动 panic 的内部函数）接管后，逆序执行每层已注册的 defer——这一过程叫栈展开（调用链从最深一层逐层向上收尾）。

任何一层 defer 里的 recover（拦截 panic 的内置函数）命中，展开立即终止、函数恢复正常返回；一路没人接，就崩出 main.main 后进程终止。可以把展开想象成反向接力：panic 这根棒从最深层往上传，任何一棒都能喊停。

但和常见语言的异常机制不同的是：Go 的拦截机会只存在于 defer 里，普通位置调用 `recover()` 恒返回 nil。信号侧的心智模型则是"门铃接线"：`signal.Notify` 把内核铃声接到手机（channel）上；不接线，默认处置就是直接终止进程。

## When to Use

这套知识在三类时刻派上用场：

- 做容器服务的优雅退出时：收到 SIGTERM 后停接新流量、等活跃请求完成、冲日志再退出（完整步骤见 Pitfalls & Q&A 最后一问）
- 在框架或任务入口兜底时：不希望局部的 panic 打挂整个进程，用 defer + recover 在边界层收口、记现场
- 排查线上崩溃时：无人 recover 的进程以 exit status 2 终止；需要现场可在 recover 里抓栈，或用 SIGQUIT 让 runtime 打印全部栈

什么时候不用：

- 可预见的业务错误（文件不存在、解析失败）用 error 返回值传递，panic 不做流程控制
- 进程内部的事件协调用 channel 或 context，不必绕道 OS 信号
- recover 不是"撤销"：展开途中各层已执行的 defer 不可回退，接住只代表流程恢复

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| error 返回值 | 显式、调用方可检查，但需逐层传递 | 可预见的业务错误一律选它 |
| panic/recover | 自动逐层展开并执行各层 defer 清理 | 程序无法继续的异常状态、边界兜底 |
| os/signal | 进程级异步事件送进 channel | 优雅退出、外部终止通知 |

## Quick Start

前置条件：仓库根目录下装有 Go 即可，无其他依赖。

```bash
cd internals/20_signal
go run main.go          # 文本演示：信号闭环 → panic 栈展开 → 结论
go run main.go -json    # 输出 JSON 快照（sigCount/frames/path/trace）
```

预期输出（节选，PID（进程 ID）每次运行不同）：

```text
== 信号 =====================
PID=13075 自我 SIGINT → 收到 1 次，channel 化处理

== panic 栈展开 =============
recover 时异常: boom: 最深一层 panic
展开路径（defer 逆序收拢）:
  1. evil
  ...
  7. guard

recover 时刻调用栈共 13 帧：exp -> main -> main -> goexit
```

JSON 快照（节选，`path` 省略了中间 5 个 evil）：

```json
{ "frames": 13, "path": ["evil", "...", "guard"], "recovered": true,
  "sigCount": 1, "trace": ["exp", "main", "main", "goexit"] }
```

`trace` 里出现两个 `main` 不是 bug：程序用 `short()` 剥掉包名，`main.main` 和 `runtime.main` 打出来同名。

实验主体是下面两个函数。这段在做什么：`evil` 递归到 `depth=0` 时 panic，每层在注册 defer 的瞬间抓下自己的函数名；`guard` 的第一个 defer 用 recover 拦截展开。

```go
func evil(depth int, log *[]string) {
	defer register(log)()          // 注册瞬间用 runtime.Caller(1) 抓函数名
	if depth == 0 {
		panic("boom: 最深一层 panic")
	}
	evil(depth-1, log)             // guard(5) 递归 6 层到 depth=0
}

func guard(depth int, log *[]string) (stack string, frameCount int, err interface{}) {
	defer func() {
		if r := recover(); r != nil { /* 抓栈、记异常 */ }
	}()
	defer register(log)()
	evil(depth, log)
	return
}
```

实测（go1.26）：`guard(5)` 递归到 `evil(0)` 爆 panic，展开路径 7 步，`recovered=true`、recover 时刻调用栈 13 帧（含 runtime 内部）。

信号侧为可重复演示，程序用 `syscall.Kill` 在 300ms 后给自己发 SIGINT（Ctrl-C 对应的中断信号），实测收到 1 次，闭环完成。

## How It Works

### panic 的三句话原理

1. **panic 是交还控制权**：`panic(v)` 之后当前函数剩下的代码不跑了，runtime 转而逆序执行本函数已注册的所有 defer（LIFO，即栈展开）
2. **recover 只在 defer 里有效**：它看的是"当前是否在展开途中"，在普通位置调 `recover()` 只会返回 nil，起不到拦截作用
3. **没人接住就一直展开到 main**：最终打印 panic 值与 goroutine 栈、进程以 exit status 2 终止；生产环境的 crash dump（崩溃现场转储）就是这么来的

### 本实验的展开路径

输出里的"展开路径 1–7"来自 Quick Start 的调用链：`evil` 的 defer 逆序执行 6 次，第 7 步轮到 guard——先跑 `register` 的 defer 写入 "guard"，再跑 recover 的 defer 拦下展开，LIFO 与注册顺序严格相反。

注意源码头注释写的"第 2 层 recover"是历史残留，实际接住点就是输出的第 7 步。

### 信号：硬中断变 channel 消息

`signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)` 之后，信号由 runtime 内部循环查注册表转发到 channel，业务 goroutine 只消费消息：收到后关连接、冲日志、正常退出。

未注册 Notify 时，SIGINT/SIGTERM 的默认处置就是直接终止进程——restart 型服务会丢状态，优雅退出都必须走这条主路径：硬中断 → runtime → channel → 业务清理。

### 关键调用速查

输出与实现涉及的五个关键调用：

| 字段/调用 | 含义 | 为什么重要 |
|---|---|---|
| `signal.Notify(ch, sig...)` | 注册要接收的信号 | 已注册信号不再触发默认处置（不再直接杀进程） |
| `make(chan os.Signal, 1)` | 承接信号的 channel | 信号发送是非阻塞的：没人消费就丢，缓冲 1 兜住窗口期 |
| `recover()` | 只在 defer 里拦截 panic | 返回非 nil 说明拦到；普通位置调用返回 nil |
| `runtime.Stack(buf, false)` | 抓当前 goroutine 栈到 buf | 双参数签名自 Go 1 起如此；传 `true` 抓全进程所有栈 |
| `runtime.Caller(1)` | 取调用方 PC/函数名 | 在 defer 注册那一刻抓名字，闭包只当搬运工 |

## Pitfalls & Q&A

### 踩坑清单

- **defer 执行期拿不到自己的函数名**：闭包里 `runtime.Caller(1)` 取到的是 gopanic 的名字
  - 原因：deferred 闭包是被 runtime.gopanic 调用的，栈帧属于调用方
  - 解法：名字必须在注册那一刻先抓（即 Quick Start 代码里的 `register(log)()`）
- **Notify 的 channel 不带缓冲会随机丢信号**：演示时好时坏、难以复现
  - 原因：信号发送是非阻塞的，"信号已到、消费 goroutine 还没 select 到"的窗口期内发送即丢弃
  - 解法：`make(chan os.Signal, 1)` 是标准写法，缓冲 1 兜住窗口期
- **`runtime.Stack` 一直是双参数**：按无参习惯调用，编译不过
  - 原因：它的签名自 Go 1 起就是 `(buf []byte, all bool)`，传 `true` 抓全进程所有栈
  - 解法：省事的等价物是 `debug.Stack()`（无参、返回当前 goroutine 栈字符串），别与它混淆
- **自测信号别靠手敲**：手动 Ctrl-C 让演示无法脚本化重复
  - 原因：演示需要每次运行行为一致
  - 解法：`syscall.Kill(os.Getpid(), syscall.SIGINT)` 给自己发信号

### 深入问答

**Q：recover 为什么必须写在 defer 里？**
A：recover 检查"当前 goroutine 是否处于 panic 展开途中"，这个状态只在 defer 执行期存在；命中后函数随后正常返回。机制细节见 What 节与上文三句话原理。

**Q：panic 没人 recover 会怎样？**
A：一路展开到 main.main，runtime 打印 panic 值和 goroutine 栈后以 exit status 2 终止进程。

线上抓崩溃现场：在 recover 里用 `runtime.Stack` 记日志，或发 SIGQUIT（触发 runtime 打印全进程栈的信号）。

**Q：收到 SIGTERM 后怎么优雅退出一个 HTTP 服务？**

A：启动时 `signal.Notify` 监听 SIGTERM/SIGINT，业务 goroutine select 到信号后先调 `http.Server.Shutdown(ctx)` 停接新连接并等活跃请求完成（可加超时），再冲日志、关依赖、以 0 退出。

核心就是这条主路径：硬中断 → channel → 业务清理。
