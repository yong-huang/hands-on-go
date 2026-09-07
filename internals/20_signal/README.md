# 20 · 信号处理与 panic 恢复：栈是怎么"一层层收回来"的

> 两个日用却少人拆开的机制：**os/signal** 把硬中断变成 channel 消息；**defer + recover** 在 panic 时把调用栈一层层收回来。本实验的程序自己给自己发一个 SIGINT 走完整信号管，再让一个 6 层深递归在底层 panic、由 guard 的 defer recover 兜住，把真实展开路径一步步打印出来。理解了这两条管线，优雅退出与崩溃排查才不是背口诀。

## 1. 为什么需要它

线上服务总有两类"退出"：一类是收到 `SIGTERM`（kill、K8s 缩容）后的优雅退出——要关连接、冲日志、留退出码；另一类是代码 panic 后的崩溃——runtime 沿 defer 链收栈，有人 recover 则恢复正常，无人接住则进程终止。这两个机制都藏在 runtime 里，行为靠猜不如跑一遍看输出。

本实验（`20_signal/`）演示两件事：`signal.Notify` 把 SIGINT/SIGTERM 转成 channel 消息的完整链路；panic 从最深一层爆发、沿 defer 链 LIFO（后注册先执行）展开、到 guard 层被 recover 接住的全过程。

## 2. 总览：核心机制一图看懂

![panic 栈展开：defer 链 LIFO 执行，recover 兜底](images/panic.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/20_signal/images/panic.html)
> （或本地打开 [`images/panic.html`](images/panic.html)）。

**心智模型一句话**：panic 不是"扔异常"，是"交还控制权"——runtime.gopanic 接手后，逆序执行已注册的 defer（栈展开），任何一层的 defer 里 recover 命中就终止展开、恢复正常返回；一路没人接就崩到 main.main 后进程终止。图中 4 个参与者对应本实验真实调用链，展开路径 1–7 与 `go run main.go` 输出一致。

## 3. 快速开始

```bash
cd internals/20_signal
go run main.go          # 文本演示：信号闭环 → panic 栈展开 → 结论
go run main.go -json    # 输出 JSON 快照（sigCount/frames/path/trace）
```

预期输出（节选，PID 每次运行不同）：

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

## 4. 核心概念

### 4.1 panic 的三句话原理

1. **panic 是交还控制权**：`panic(v)` 之后当前函数剩下的代码不跑了，runtime 转而逆序执行本函数已注册的所有 defer（LIFO = 栈展开）
2. **recover 只在 defer 里有效**：它看的是"当前是否在展开途中"，在普通位置调 `recover()` 返回 nil 纯属白干
3. **没人接住就一直展开到 main**：最终打印 panic 值与 goroutine 栈、进程终止（exit status 2）；生产环境的 crash dump 就是这么来的

### 4.2 本实验的展开路径

```go
func evil(depth int, log *[]string) {
	defer register(log)()          // 注册瞬间用 runtime.Caller(1) 抓函数名
	if depth == 0 {
		panic("boom: 最深一层 panic")
	}
	evil(depth-1, log)             // guard(5) 递归 6 层到 depth=0
}

func guard(depth int, log *[]string) (stack string, frames int, err interface{}) {
	defer func() {
		if r := recover(); r != nil { /* 抓栈、记异常 */ }
	}()
	defer register(log)()
	evil(depth, log)
	return
}
```

实测（go1.26）：`guard(5)` 递归到 `evil(0)` 爆 panic，展开路径 7 步——`evil` 的 defer 逆序执行 6 次，第 7 步轮到 guard：先跑 `register` 的 defer 写入 "guard"，再跑 recover 的 defer 把展开拦下，`recovered=true`、recover 时刻调用栈 13 帧（含 runtime 内部）。注意源码头注释写的"第 2 层 recover"是历史残留，实际接住点就是上面输出的第 7 步。

### 4.3 信号：硬中断变 channel 消息

![SIGTERM 优雅退出：内核信号 → Notify 的 channel → 业务 goroutine](images/signal.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/20_signal/images/signal.html)
> （或本地打开 [`images/signal.html`](images/signal.html)）。

`signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)` 之后，信号由 runtime 内部循环查注册表转发到 channel，业务 goroutine 只消费消息：收到后关连接、冲日志、正常退出。未注册 Notify 时，SIGINT/SIGTERM 的默认处置就是直接终止进程——restart 型服务会丢状态，优雅退出都要走图中主路径。本实验为可重复演示，用 `syscall.Kill` 在 300ms 后给自己发 SIGINT，实测 handler goroutine 收到 1 次，闭环完成。

## 5. 关键字段与坑清单

| 字段/调用 | 含义 | 为什么重要 |
|---|---|---|
| `signal.Notify(ch, sig...)` | 注册要接收的信号 | 已注册信号不再触发默认处置（不再直接杀进程） |
| `make(chan os.Signal, 1)` | 承接信号的 channel | 信号发送是非阻塞的：没人消费就丢，缓冲 1 兜住窗口期 |
| `recover()` | 只在 defer 里拦截 panic | 返回非 nil 说明拦到；普通位置调用返回 nil |
| `runtime.Stack(buf, false)` | 抓当前 goroutine 栈到 buf | 双参数签名自 Go 1 起如此；传 `true` 抓全进程所有栈 |
| `runtime.Caller(1)` | 取调用方 PC/函数名 | 在 defer 注册那一刻抓名字，闭包只当搬运工 |

坑清单：

- **defer 执行期拿不到自己的函数名**：deferred 闭包是被 `runtime.gopanic` 调用的，闭包里 `runtime.Caller(1)` 取到的是 gopanic——名字必须在注册那一刻先抓
- **Notify 的 channel 不带缓冲会随机丢信号**：信号到、消费还没到的窗口期发送即丢弃，`make(chan os.Signal, 1)` 是标准写法
- **`runtime.Stack` 一直是 `(buf []byte, all bool)` 双参数**：图省事可用 `debug.Stack()`（无参、返回当前 goroutine 栈字符串），别和它混淆
- **自测信号别靠手敲**：`syscall.Kill(os.Getpid(), syscall.SIGINT)` 给自己发，演示可脚本化重复

## 6. 文件结构

```text
internals/20_signal/
├── README.md                      # 本文件：原理 + 运行指引 + 面试要点
├── main.go                        # 主教材：信号闭环 + panic 栈展开（-json 输出快照）
└── images/
    ├── panic.sequence.json        # 图源：panic 沿 defer 链 LIFO 展开（Typed JSON IR）
    ├── panic.html                 # 交互版：可缩放/高亮消息的自包含页面
    ├── panic.svg                  # README 内嵌双主题矢量图
    ├── signal.workflow.json       # 图源：SIGTERM → channel → 优雅退出流程
    ├── signal.html                # 交互版
    └── signal.svg                 # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：panic 发生后到底执行什么？顺序如何？**
A：当前函数剩余代码不再执行，runtime.gopanic 逆序执行本函数已注册的所有 defer，然后带着 panic 沿调用栈向上层重复这个过程（栈展开）。本实验实测：evil×6 → guard，LIFO 与注册顺序严格相反。

**Q：recover 为什么必须写在 defer 里？**
A：recover 检查"当前 goroutine 是否处于 panic 展开途中"，这个状态只在 defer 执行期存在。普通位置调用恒返回 nil；只有 defer 里的 recover 返回非 nil 并终止展开，函数随后正常返回。

**Q：panic 没人 recover 会怎样？**
A：一路展开到 main.main，runtime 打印 panic 值和 goroutine 栈后以 exit status 2 终止进程。线上抓崩溃现场可以靠 recover 里的 `runtime.Stack` 记日志，或发 SIGQUIT 让 runtime 打印全进程栈。

**Q：`signal.Notify` 的 channel 为什么要带缓冲？**
A：runtime 对 channel 的信号发送是非阻塞的：缓冲满或无缓冲且无消费者就丢弃。带缓冲 1 能兜住"信号已到、消费 goroutine 还没 select 到"的窗口期，否则演示会随机失败。

**Q：收到 SIGTERM 后怎么优雅退出一个 HTTP 服务？**
A：启动时 `signal.Notify` 监听 SIGTERM/SIGINT，业务 goroutine select 到信号后先调 `http.Server.Shutdown(ctx)` 停接新连接并等活跃请求完成（可加超时），再冲日志、关依赖、以 0 退出。核心就是图上那条主路径：硬中断 → channel → 业务清理。

## 8. 总结

两条管线共用一个思想：把异步事件变成有序消息。panic 展开是"逆序跑完每一层的清理逻辑，recover 处处可拦"；os/signal 是"内核中断 → runtime → channel → 业务消费"。记住验证方法——`go run main.go` 把展开路径和信号次数原样打印，比背结论可靠得多。

下一篇 [21_cgo](../21_cgo/) 看跨语言边界：CGO 的内存为什么 Go 的 GC 管不着，调用边界又如何跨线程。
