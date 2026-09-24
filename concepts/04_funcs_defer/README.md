# 04 · 函数多返回值与 defer：结果和出错一起回来，清理动作钉在出口

> Go 的函数没有异常和默认参数，它把"成功值"和"错误"捆在一起返回；而资源清理不靠 finally 块，靠 `defer`——在注册处写下清理动作，在函数出口（含 panic 路径）必然执行。多返回值 + 命名返回值 + defer 三件套，构成了 Go 错误处理与资源管理的全部骨架。本实验用一个可运行 demo 把执行顺序逐行跑给你看。

## 1. 为什么需要它

C 程序员要手工记得在每个 `return` 前释放资源；Java 程序员依赖 try/finally；Go 把这两件事分开：**结果传递**用多返回值（`value, err` 惯用法，错误是一等返回值，调用方无法"忘记"检查的编译期提示写进了函数签名），**清理时机**用 defer（声明与执行解耦，注册一次，所有出口都覆盖）。此外命名返回值让"文档化返回值"成为可能，还带来一个独门特性：defer 在 return 之后仍能改写返回值。

## 2. 总览：核心机制一图看懂

![main 调用 → 多返回值 → defer LIFO → recover 捕获](images/defer.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/04_funcs_defer/images/defer.html)
> （或本地打开 [`images/defer.html`](images/defer.html)）。

**心智模型一句话**：返回值从被调函数"流回" main（值 + error 捆绑），而 defer 调用栈在函数出口反向弹空（LIFO）——注册顺序和执行顺序恰好相反，panic 也要先过 defer 这一关。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 多返回值与 error 惯用法

`q, r, err := divide(17, 5)` 一次拿到多个结果。约定：出错时**值恒为零值**、err 非 nil——调用方先判 `err != nil` 再用值，值与错误永远不会同时"半真半假"。不需要的结果用 `_` 丢弃。

### 4.2 命名返回值：defer 的最后一次改写机会

`func status(b bool) (ret string)` 里的 `ret` 是函数体内的具名变量：裸 `return` 等价于 `ret = ...; return`。defer 在 `return` 把值放进 `ret` **之后**、真正返回**之前**执行，所以 demo 里 `return "warn"` 被 defer 改写成了 `"degraded"`。这是中间件里"包一层错误"的标准手法。

### 4.3 defer 的三条铁律

注册时参数即求值（执行时才使用）；执行顺序 LIFO——demo 在 main 里注册 1/2/3，main 结束时打印 3/2/1（资源申请顺序与释放顺序天然相反）；panic 发生时已注册的 defer **依然执行**，这正是它做清理的兜底价值。

### 4.4 defer + recover：把 panic 变回函数返回

`recover()` 只在**被 defer 的函数体内**调用才生效。`divideChecked` 里 `10 ÷ 0` 触发 runtime panic，defer 捕获后给命名返回值 `result` 定稿 `-1`，函数正常返回，main 继续往下跑——panic 没有越过调用者。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `v, err := f()` | 多返回值 + error 惯用法 | 错误进签名，调用方被迫面对 |
| `f() (ret string)` | 命名返回值 | 裸 return；defer 可在其后改写 |
| `defer func(){...}()` | 注册闭包到 defer 栈 | 出口 LIFO 执行，panic 也执行 |
| `recover()` | 捕获当前 panic | 仅 defer 函数体内有效 |
| `_` | 丢弃不需要的返回值 | 未使用变量是编译错，不能不接 |

坑清单：

- 没赋值给新变量的多返回值接收会编译错；`_, _, err = divide(17, 0)` 是合法丢弃
- `defer fmt.Println(i)` 注册时 i 已求值拷贝；想引用最终值要传指针或用闭包捕获变量
- recover 写在普通函数体里永远返回 nil——它必须位于被 defer 的函数内
- recover 之后函数**正常返回**：不给命名返回值赋值，调用方拿到的是零值
- defer 在循环里注册会积压到函数出口才执行（文件句柄场景会爆 fd），应封装成函数

## 6. 文件结构

```text
concepts/04_funcs_defer/
├── README.md                # 本文件：原理 + 运行指引 + 深入要点
├── main.go                  # 演示程序：多返回值 / 命名返回值 / defer LIFO / recover
└── images/
    ├── defer.sequence.json  # 图源：main 调用→多返回值→LIFO→recover 的时序（Typed JSON IR）
    ├── defer.html           # 交互版：自包含单文件，可缩放/切换主题
    └── defer.svg            # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 Go 选择"把错误当返回值"而不是异常？**
A：错误出现在函数签名里，控制流是显式的——每个可能失败的调用点在代码里可见，没有隐藏的跨函数跳转。代价是 `if err != nil` 的重复书写，收益是"谁会失败、失败后走到哪"一目了然。

**Q：defer 注册的瞬间发生了什么？执行顺序为什么是 LIFO？**
A：defer 会把"函数值 + 已求值的参数"压进当前 goroutine 的 defer 记录链表（头插），函数返回时从链表头依次弹出执行——所以后注册的先跑。参数在注册时求值，保证了执行时拿到的是"注册那一刻"的快照。

**Q：命名返回值的 defer 改写有什么实际用途？**
A：统一的错误出口。在 defer 里检查"如果返回的 err 非 nil 就补上下文/打日志/替换成业务错误"，函数体里只需裸 `return`。数据库事务、HTTP 中间件都靠这一手收口。

**Q：recover 捕获 panic 后，程序状态可信吗？**
A：只应捕获"可转化为错误"的预期 panic（如解析、边界场景），并在捕获处立刻把上下文写进返回值或日志。任意吞 panic 会掩盖不变量被破坏的事实；跨 goroutine 的 panic 无法被别的 goroutine recover，只能在该 goroutine 自己的 defer 里处理。

## 8. 总结

多返回值让"成功与失败"在同一条签名里显式交接，defer 让"注册与执行"在时间上解耦、在出口处逆序收拢，recover 则给了 panic 一次转回错误的机会。把 §3 的输出与 §4.3 的三条铁律逐行对照，这套机制就长在肌肉记忆里了。

下一篇 [05_struct_iface](../05_struct_iface/) 看结构体方法与接口：值/指针接收者的差别与接口的多态遍历。想看 defer 在运行时里的底层实现（open-coded defer、_defer 链表），见 [../../internals/25_defer/](../../internals/25_defer/)。
