# 04 · 函数多返回值与 defer：结果和出错一起回来，清理动作钉在出口

> Go 的函数没有异常，也没有默认参数：成功值与错误捆在一起作为返回值交接；资源清理不靠 finally 块，靠 `defer` 在函数出口（含 panic 路径）必然执行。本篇用一个可运行 demo 把多返回值、命名返回值、defer 的 LIFO 执行序、recover 捕获 panic 逐行跑出来，读完你能解释每条 defer 何时执行、为什么能改写返回值。

## Background

C 程序员要手工记得在每个 `return` 前释放资源：`fopen` 配对 `fclose`，一旦某个提前 return 忘了关，句柄就漏了。Java 用 try/finally 把清理钉在块出口，但异常本身是隐藏的控制流——调用点看不出哪个调用会抛。

Go 把"结果传递"与"清理时机"分给两个机制：结果用多返回值（函数一次返回多个值的能力）交接，`value, err` 成对出现在签名里；清理用 defer（注册一次、函数所有出口都执行的延迟调用）覆盖。两者拼出了 Go 错误处理与资源管理的骨架。

命名返回值（签名里给返回值起名字，函数体内可当变量用）还给这套骨架补了一个独门特性：defer 在 return 之后仍能改写返回值。

## What

一句话定义：多返回值让函数同时交回结果与错误；defer 把清理动作的"声明"与"执行"解耦——注册处写下，函数出口（LIFO，含 panic 路径）必然执行；recover 在 defer 函数体内把 panic 变回一次正常返回。

心智模型一句话：返回值从被调函数"流回" main（值 + error 捆绑），而 defer 调用栈在函数出口反向弹空（LIFO）——注册顺序和执行顺序恰好相反，panic 也要先过 defer 这一关。

可以把 defer 想象成停车取卡：进场时拿卡（注册），不管从哪个出口离场都得先还卡（执行）。但和真实停车场不同的是，卡的顺序是后拿的先还——最后注册的清理动作最先执行。

## When to Use

先说在做什么事的时候靠这两个机制：

- 在写任何可能失败的调用（解析、IO、查库）：函数签名返回 `value, err`，调用方先判 `err != nil` 再用值。
- 在打开资源（文件、网络连接、事务）时：紧跟打开语句写 `defer f.Close()`，多个出口不必逐个记得关。
- 在做统一的错误收口（中间件、数据库事务）时：命名返回值 + defer 里检查 err 并补上下文，函数体内只需裸 `return`。

何时不用 recover 兜底：任意吞 panic 会掩盖程序不变量被破坏的事实；预期内可转错误的 panic（如解析、边界）才值得捕获。

同类方案对比：

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 多返回值 `v, err` | 错误进签名，控制流显式 | 所有可能失败的调用（Go 惯用法） |
| 异常/try-catch | 隐藏的跨函数跳转 | Go 不提供；跨层传播用错误包装 |
| defer 清理 | 注册一次覆盖所有出口 | 资源申请后立即注册 |
| defer + recover | panic 转回正常返回 | 可预期、可转化为错误的 panic |

## Quick Start

前置条件：本机安装 Go 工具链即可，demo 只用标准库。

```bash
cd concepts/04_funcs_defer
go run main.go
```

真实输出（节选）：

```text
========== 1. 多返回值 + error 惯用法 ==========
17 ÷ 5 → 商 3 余 2, err = <nil>
17 ÷ 0 → 先判错: err = "除数不能为 0"（值已不可信，永远是零值）

========== 2. 命名返回值：defer 可改写 ==========
status(true)  = ok
status(false) = degraded （return 的 warn 被 defer 改写为 degraded）

========== 3. 多个 defer 的 LIFO 执行序 ==========
  三个 defer 已注册，main 即将结束

========== 4. defer + recover 捕获 panic ==========
  [recover] 捕获 panic: runtime error: integer divide by zero
divideChecked(10, 0) 返回: -1
main 继续执行——panic 被捕获后程序没有中断

========== 总结 ==========
...
  defer 执行: 3
  defer 执行: 2
  defer 执行: 1
```

注意最后一段是程序自己打印的分节标题与 LIFO 弹栈顺序：注册顺序 1/2/3，打印顺序 3/2/1。四段输出对应四个知识点：error 惯用法、命名返回值改写、LIFO 执行序、recover。

## How It Works

### 多返回值与 error 惯用法

`q, r, err := divide(17, 5)` 一次拿到多个结果。约定：出错时值恒为零值、err 非 nil——调用方先判 `err != nil` 再用值，值与错误永远不会同时"半真半假"。不需要的结果用 `_` 丢弃。

输出第 1 段 `17 ÷ 0 → 先判错: err = "除数不能为 0"`：出错路径里商和余数都是零值，括号里的说明就是这条约定。

### 命名返回值：defer 的最后一次改写机会

`func status(b bool) (ret string)` 里的 `ret` 是函数体内的具名变量：裸 `return` 等价于 `ret = ...; return`。

defer 在 `return` 把值放进 `ret` 之后、真正返回之前执行，所以 demo 里 `return "warn"` 被 defer 改写成了 `"degraded"`。这是中间件里"包一层错误"的标准手法。

输出第 2 段 `status(false) = degraded （return 的 warn 被 defer 改写为 degraded）`，逐字验证的就是这条改写时序。

### defer 的三条铁律

注册时参数即求值（执行时才使用）；执行顺序 LIFO——demo 在 main 里注册 1/2/3，main 结束时打印 3/2/1（资源申请顺序与释放顺序天然相反）；panic 发生时已注册的 defer 依然执行，这正是它做清理的兜底价值。

输出最后一段的 `defer 执行: 3 / 2 / 1`，就是 LIFO 弹栈的直接呈现。

### defer + recover：把 panic 变回函数返回

`recover()` 只在被 defer 的函数体内调用才生效。`divideChecked` 里 `10 ÷ 0` 触发 runtime panic，defer 捕获后给命名返回值 `result` 定稿 `-1`，函数正常返回，main 继续往下跑——panic 没有越过调用者。

输出第 4 段 `divideChecked(10, 0) 返回: -1` 与 `main 继续执行——panic 被捕获后程序没有中断`，验证的是捕获后的两次交接：改写返回值、恢复调用方流程。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `v, err := f()` | 多返回值 + error 惯用法 | 错误进签名，调用方被迫面对 |
| `f() (ret string)` | 命名返回值 | 裸 return；defer 可在其后改写 |
| `defer func(){...}()` | 注册闭包到 defer 栈 | 出口 LIFO 执行，panic 也执行 |
| `recover()` | 捕获当前 panic | 仅 defer 函数体内有效 |
| `_` | 丢弃不需要的返回值 | 未使用变量是编译错，不能不接 |

## Pitfalls & Q&A

踩坑清单（每条按"现象 → 原因 → 解法"展开）：

- 现象：多返回值没赋给新变量时编译错。原因：接收方必须是新声明或已存在的变量。解法：丢弃不用的返回值用 `_`，`_, _, err = divide(17, 0)` 是合法丢弃。
- 现象：`defer fmt.Println(i)` 打印的是注册那一刻的 i，不是最终值。原因：注册时参数即求值拷贝。解法：想引用最终值要传指针或用闭包捕获变量。
- 现象：recover 写在普通函数体里永远返回 nil。原因：它必须位于被 defer 的函数内才生效。解法：把 recover 放进 `defer func(){ recover() }()` 这样的闭包。
- 现象：recover 之后调用方拿到零值。原因：recover 之后函数正常返回，不给命名返回值赋值就是零值。解法：在 defer 里显式给命名返回值定稿（demo 里的 `-1`）。
- 现象：defer 在循环里注册会积压到函数出口才执行（文件句柄场景会爆 fd）。原因：defer 的执行时机是函数出口，不是块出口。解法：把循环体封装成函数，让每次迭代的 defer 及时执行。

深入问答：

**Q：为什么 Go 选择"把错误当返回值"而不是异常？**
A：错误出现在函数签名里，控制流是显式的——每个可能失败的调用点在代码里可见，没有隐藏的跨函数跳转。代价是 `if err != nil` 的重复书写，收益是"谁会失败、失败后走到哪"一目了然。

**Q：defer 注册的瞬间发生了什么？执行顺序为什么是 LIFO？**
A：defer 会把"函数值 + 已求值的参数"压进当前 goroutine 的 defer 记录链表（头插），函数返回时从链表头依次弹出执行——所以后注册的先跑。参数在注册时求值，保证了执行时拿到的是"注册那一刻"的快照。

**Q：命名返回值的 defer 改写有什么实际用途？**
A：统一的错误出口。在 defer 里检查"如果返回的 err 非 nil 就补上下文/打日志/替换成业务错误"，函数体里只需裸 `return`。数据库事务、HTTP 中间件都靠这一手收口。

**Q：recover 捕获 panic 后，程序状态可信吗？**
A：只应捕获"可转化为错误"的预期 panic（如解析、边界场景），并在捕获处立刻把上下文写进返回值或日志。

任意吞 panic 会掩盖不变量被破坏的事实；跨 goroutine 的 panic 无法被别的 goroutine recover，只能在该 goroutine 自己的 defer 里处理。

defer 在运行时里的底层实现（open-coded defer、_defer 链表），见 [../../internals/25_defer/](../../internals/25_defer/)。
