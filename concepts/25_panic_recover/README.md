# 25 · panic 与恢复：panic 不是异常处理，是"不可绕过的失败"

> Go 的错误处理靠 `error` 返回值，而 `panic` 留给"程序已经无法继续"的场景：沿调用栈向上传播、逐层执行 defer、无人接住就终止整个进程。`recover` 是唯一的刹车，但刹车有严格的生效条件——只在 defer 函数里、只在本 goroutine 内。本实验把这套规则逐条跑给你看，包括最经典的坑：主 goroutine 接不住子 goroutine 的 panic。

## 1. 为什么需要它

没有 recover 时，一个 panic 就能带走整个进程——哪怕崩的是一条"无关紧要"的请求 goroutine，HTTP 服务会整体下线。理解 panic 的传播路径和 recover 的生效条件，才能正确地做两件事：在库的边界把 panic 转成 error（如 `encoding/json` 内部就是这么干的），以及在并发程序里让每个 goroutine 自己兜底。反过来说，把 recover 当 try/catch 日常用，会掩盖真正的 bug——这也是 Go 社区"能 error 不 panic"的原因。

## 2. 总览：核心机制一图看懂

![panic 抛出、recover 拦截与恢复后继续的完整时序](images/recover.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/25_panic_recover/images/recover.html)
> （或本地打开 [`images/recover.html`](images/recover.html)）。

**心智模型一句话**：panic 是沿当前 goroutine 调用栈向上传播的"紧急出口"，defer 是出口上的必经检查站，recover 只在检查站里有效；跨 goroutine 没有出口——子 goroutine 的 panic 必须由子 goroutine 自己的 defer recover 拦截。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 panic 的传播路径

`panic(v)` 立即中断当前函数，然后沿调用栈向上传播：每上一层，先执行该层已注册的 defer，再继续上抛。如果一路传到 goroutine 入口都没人 recover，runtime 打印 `panic: ...` 加栈回溯，进程以非零码退出（`exit status 2`）。main.go 第 2 节用注释演示了这个场景——取消注释即可复现崩溃。

### 4.2 recover 的生效条件

`recover()` 只有在**本 goroutine 的 defer 函数中直接调用**才生效，三个条件缺一不可：

- 在 defer 的函数里调用（不在 defer 里调用恒返回 nil）；
- 与 panic 在同一个 goroutine；
- recover 一旦返回非 nil，panic 即被"消费"，控制流从 defer 所在函数正常返回。

没有 panic 时调用 `recover()` 完全无害，只返回 nil——这就是 main.go 第 3 节验证的行为，也是 safeCall 里 `if r := recover(); r != nil` 写法的由来。

### 4.3 safeCall：panic 转 error 惯用法

把可能 panic 的调用包进 `safeCall(fn)`：defer 里 recover，把 panic 值格式化成 error 用命名返回值带回。调用方拿到的仍是普通的 `error`，可以继续用 `errors.Is/As` 处理——panic 被压平进了 Go 的常规错误通道。标准库里 `encoding/json`、`net/http` 的连接处理都在边界用了这一招。

### 4.4 跨 goroutine：谁的孩子谁抱走

panic 只沿**当前 goroutine**的栈传播。main.go 第 4 节里，如果去掉子 goroutine 内部的 defer recover，进程会整体崩溃——main 里注册再多 recover 也拦不住。正确姿势是"谁启动 goroutine，谁就给它套一层 safeCall 式兜底"，把 error 通过 channel 送回主流程。

## 5. 关键字段与坑清单

语法要点：

| 写法 | 语义 |
| --- | --- |
| `panic(v)` | 抛出，v 可以是任意类型（惯用 `error` 或字符串） |
| `recover()` | 仅在 defer 函数中生效，返回 panic 值；无 panic 返回 nil |
| `func f() (err error)` | 命名返回值，defer 里 recover 后可直接改写 err |
| `defer func(){...}()` | defer 必须是函数值，panic 时依然执行（LIFO） |

坑清单（每条可复现）：

- **只在 defer 里 recover**：函数体里直接调 `recover()` 永远返回 nil，拦不住任何 panic。
- **直接 panic 裸值难定位**：`panic("boom")` 上抛后只有字符串；惯用 `panic(fmt.Errorf(...))` 或干脆返回 error。
- **recover 后吞掉现场**：捕获后若不打印栈（`runtime.Stack`）或重新上报，bug 现场就丢了；基础设施代码常 recover 后记日志再转 500。
- **以为主 goroutine 能兜底子 goroutine**：接不住。子 goroutine 的 panic 未被自己 recover = 整个进程崩。
- **recover 当 try/catch 用**：能预判的失败（文件不存在、参数非法）应返回 error；panic 留给不变量被破坏的场合。

## 6. 文件结构

```text
concepts/25_panic_recover/
├── README.md                  # 本文件：八节讲解
├── main.go                    # 演示程序：safeCall 惯用法 / 未捕获 panic（注释）/ recover 返回 nil / 跨 goroutine 兜底
└── images/
    ├── recover.sequence.json  # 图源：panic 抛出 → recover 拦截 → 继续执行 时序
    ├── recover.html           # 交互版时序图（浏览器打开可探索）
    └── recover.svg            # 静态版时序图（README 内嵌）
```

## 7. 深入要点

**Q1：为什么 recover 必须放在 defer 里？**
因为 panic 传播的每一站只有 defer 会执行。runtime 在展开栈帧时逐层运行 defer 链，recover 的语义就是"我在 defer 里声明愿意接住正在上抛的 panic"——除此之外的任何代码在 panic 路径上根本不会运行，自然无处可接。

**Q2：recover 之后，panic 点下面的栈怎么处理？**
不回去了。recover 不恢复现场，控制流从**注册 defer 的那个函数**正常返回（命名返回值可携带结果），panic 点以下的帧已经展开完毕。所以 recover 后的代码只能活在 defer 函数里，`fn()` 之后的语句不会执行。

**Q3：recover() 返回 nil 一定是"没有 panic"吗？**
还有一种情况：panic 的值本身就是 `nil`（`panic(nil)`）。Go 1.21 起会把 `panic(nil)` 换成 `*runtime.PanicNilError`，recover 拿到非 nil，避免"recover 到 nil 却处于恢复态"的歧义；旧版本里 `recover() != nil` 判断会漏掉这种情况。

**Q4：子 goroutine panic 为什么主 goroutine 接不住？**
每个 goroutine 有独立的调用栈，panic 只在发起它的栈上展开；主 goroutine 的 defer 挂在另一条栈上，panic 到不了那里。runtime 的处理很干脆：某条栈的 panic 无人接 → 打印栈回溯 → `exit(2)`，整个进程所有 goroutine 一起终止。

**Q5：什么时候该 panic，什么时候该返回 error？**
可预见的操作失败（网络、输入、资源缺失）永远返回 error；panic 留给"不变量被破坏、继续跑就是 bug"的场合，如切片越界、并发误用 map。库代码的纪律是"内部可以 panic，边界必须 recover 转 error"，参考 `encoding/json` 的实现。

## 8. 总结

panic/recover 是 Go 里唯一跨函数的"非常规控制流"：panic 沿本 goroutine 栈上抛、defer 必经、recover 只在 defer 里生效、跨 goroutine 不可恢复。记住两条工程纪律——能 error 不 panic，谁开 goroutine 谁兜底。想看进程收到信号（SIGSEGV/SIGINT）时 runtime 的另一套处理，见 [../../internals/20_signal](../../internals/20_signal)；想学 defer 与命名返回值的更多配合，见 [../04_funcs_defer](../04_funcs_defer)。下一篇我们看接口的另一面——类型断言与类型 switch，见 [../26_iface_assert](../26_iface_assert)。
