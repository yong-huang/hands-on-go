# 23 · error 基础：错误就是值，哨兵 + 包装 + errors.Is

> Go 没有 try/catch：错误是一个普通的内置接口 `error`，随返回值一路往上传，由调用方决定处理还是忽略。这个模型简单，但有一套必须掌握的配合技——哨兵错误定义锚点、`%w` 包装保留上下文与错误链、`errors.Is` 穿透包装做判定。本实验用一个安全除法函数把这套流程完整跑一遍。

## 1. 为什么需要它

从异常语言转来的人最常问：`if err != nil` 写得到处都是，不累吗？累的背后是 Go 的取舍——**错误是值**，被强制出现在每个调用点的视线里，不能像异常一样被无声吞掉。但"返回错误"只是第一步：上层调用者还想加上下文（"在哪一层错的"），同时又想让底层能识别错误类别（"是不是除零"），这就需要 `%w` 包装与 `errors.Is` 判定的组合。不掌握它，要么裸比较 `err ==` 在包装后失效，要么用 `%v` 格式化把错误链生生折断。

## 2. 总览：核心机制一图看懂

![错误处理流：哨兵 error → %w 包装 → errors.Is 判定](images/errflow.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/23_error_basics/images/errflow.html)
> （或本地打开 [`images/errflow.html`](images/errflow.html)）。

**心智模型一句话**：哨兵错误是调用双方约定的"判定锚点"，`%w` 包装是"加上下文但保留链条"，`errors.Is` 沿链条逐层 `Unwrap` 找锚点——三者配合，错误才能既可读又可判。

## 3. 快速开始

```bash
cd concepts/23_error_basics
go run main.go
```

真实输出（共 19 行，节选）：

```text
========== 1. 正常路径：err == nil ==========
10 / 4 = 2.5（err == nil，走正常分支）

========== 2. 哨兵错误 + %w 包装 + errors.Is 判定 ==========
err 本体: safeDivide(1, 0): division by zero
== 裸比对（已失效）: false
errors.Is 穿透包装（推荐）: true
→ 判定为除零：请把分母换成非零值后重试

========== 3. error 是接口：自定义类型也是 error ==========
err2 = query failed (code 404)（动态类型 *main.OpsError，实现了 Error() string）

========== 4. 错误必须被处理或显式忽略 ==========
v, _ := safeDivide(9, 3) → v = 3（err 被 _ 显式丢弃）
```

## 4. 核心概念

### 4.1 哨兵错误：包级错误值做锚点

`var ErrDivisionByZero = errors.New("division by zero")` 定义在包级，是调用双方都认识的固定值。库函数返回它（或它的包装），调用方拿它做判定。命名惯例是 `Err` 前缀。

### 4.2 `%w` 包装：上下文与链条兼得

`fmt.Errorf("safeDivide(%g, %g): %w", a, b, ErrDivisionByZero)` 返回一个**新错误**：消息带上了参数上下文，内部通过 `Unwrap()` 链接着原哨兵。注意两个反面：`%v` 只拼字符串不建链，`errors.Is` 从此判不中；包装后的错误 `==` 裸哨兵也判不中（输出里的 `false` 就是证据）。

### 4.3 `errors.Is`：沿链穿透比对

`errors.Is(err, ErrDivisionByZero)` 先比 `err` 本身，不等就调 `err.Unwrap()` 取下一层继续比，直到命中或链尽。所以它对"包了多少层"无感——这是错误链体系里最常用的判定入口。

### 4.4 error 只是接口

`type error interface { Error() string }`——任何类型实现这个方法就是 error。demo 里的 `OpsError{Op, Code}` 用指针接收者实现 `Error() string`，`var err2 error = &OpsError{...}` 直接成立，`%T` 打出 `*main.OpsError`。这也是标准库 `*errors.errorString`、`*fmt.wrapError` 的共同底座。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `errors.New("...")` | 创建消息式错误 | 定义哨兵 / 一次性错误 |
| `fmt.Errorf("...: %w", err)` | 包装并保留错误链 | 上层加上下文的标准姿势 |
| `errors.Is(err, target)` | 沿 Unwrap 链比对 | 包装后判定的唯一可靠入口 |
| `err == ErrXxx` | 指针/值直比 | 只对未包装错误有效，包装后失效 |
| `v, _ := f()` | 显式忽略错误 | 唯一合法的"不处理"写法 |
| `Error() string` | error 接口唯一方法 | 任何类型实现即可当错误用 |

坑清单：

- 包装后 `err == 哨兵` 是 `false`：`fmt.Errorf` 返回新值，直接比指针必失手，demo 第 2 节可复现
- `fmt.Errorf` 手滑写成 `%v`：错误链断在那一层，下游 `errors.Is` 全部判不中
- `err, err2 := f(); g(err)` 之后忘了用 `err2` → `declared and not used` 编译错；不处理必须写 `_`
- 哨兵错误变量被重新赋值会让判定错乱：把它当常量对待，不要改写
- 自定义 error 类型若用值接收者而返回时取了地址/没取地址不一致，容易造成类型断言（`errors.As`）判不中

## 6. 文件结构

```text
concepts/23_error_basics/
├── README.md                  # 本文件：原理 + 运行指引 + 深入要点
├── main.go                    # 演示程序：哨兵/%w 包装/errors.Is/自定义 error 类型
└── images/
    ├── errflow.workflow.json  # 图源：除法 → 哨兵 → 包装 → 判定 管线（Typed JSON IR）
    ├── errflow.html           # 交互版：自包含单文件，可缩放/切换主题
    └── errflow.svg            # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 Go 用返回错误而不是异常？**
A：错误走返回值，控制流显式可见，`if err != nil` 让每个可能失败的调用点都无法被跳过；异常则允许错误无声穿透多层调用。代价是代码略啰嗦，收益是不会"忘了处理"——编译器还会强制你处理或显式 `_`。

**Q：`errors.Is` 和 `==` 到底差在哪？**
A：`==` 只比较当前值；`errors.Is` 沿 `Unwrap()` 链逐层比较，且对实现 `Is(error) bool` 的错误类型调用其自定义判定。未包装时两者等价，一旦中间有人 `%w` 包装，`==` 即失效。

**Q：哨兵错误和自定义错误类型怎么选？**
A：错误类别少、无需携带数据 → 哨兵（如 `io.EOF`）；要带结构化信息（错误码、操作名、时间）→ 自定义类型，配合 `errors.As` 取出字段。两者可叠加：类型内部也可以 `Unwrap` 到哨兵。

**Q：`%w` 可以包装多个错误吗？**
A：可以。1.20 起 `fmt.Errorf` 支持多个 `%w`，错误形成树而非链，`errors.Is` 会遍历整棵树逐一匹配；更早版本一层只能一个 `%w`。

## 8. 总结

error 基础三件套：`errors.New` 立锚点、`%w` 包装留链、`errors.Is` 穿透判定——再记住"错误要么处理、要么 `_` 显式忽略"，Go 错误处理的日常就齐了。跑一遍 §3，对照 §5 坑清单逐条验证，比背规则有效得多。

下一篇 [24_error_chain](../24_error_chain/) 把包装链拉长到三层，看 `Unwrap`/`errors.As` 如何溯源定位最初的错误；底层原理深入见 internals 线的 error 接口实现篇。
