# 30 · 自定义序列化：让 json 包按你的格式编解码

> `encoding/json` 默认把 `time.Time`（标准库时间类型）序列化成 RFC3339 格式（`2026-09-24T10:30:00+08:00`）；想要 `2026-09-24 10:30:00` 时，答案不是在业务代码里到处手动转换，而是实现 `json.Marshaler` / `json.Unmarshaler` 两个接口接管编解码。读完你能写出双向自定义格式，并守住"合法 JSON、错误明确、null 特判"三条纪律。

## Background

默认规则下，`encoding/json` 对每个值按固定格式编码：`time.Time` 输出 RFC3339（带时区的 ISO 8601 时间格式）。想换格式，只能在业务代码里对每个字段手动转换，或序列化后再做字符串替换——改一处格式，动一遍调用点。

痛点在"值的形态"：struct tag（见 [28_struct_tags](../28_struct_tags/)）只能改键名，改不了值本身。时间格式不合口味、金额以"分"存储却要输出"元"、枚举要输出字符串名——这些需求 tag 无能为力。

Go 的方案是给类型留一个接管口：`json` 包在编解码每个值前都会问一句"你实现了 Marshaler/Unmarshaler 吗"，实现了就交出编码权或解码权。time.Time 之外，big.Int（标准库的任意精度整数类型）与所有第三方类型的 JSON 行为，都由这条路径决定。

## What

自定义序列化是给类型实现 `json.Marshaler`（`MarshalJSON() ([]byte, error)`）与 `json.Unmarshaler`（`UnmarshalJSON(data []byte) error`）两个接口，接管该类型在 JSON 编解码中的行为——签名固定，语义全由实现者决定。

心智模型一句话：`json.Marshal` 每编码一个值都先做接口检测——发现 `MarshalJSON() ([]byte, error)` 就交出编码权；反向解码时发现 `UnmarshalJSON([]byte) error` 就交出解码权，指针接收者保证结果能写回你的变量。

可以把 json 包想象成印刷厂：拿到稿件（值）默认按厂里模板排版；稿件自带一份排版说明（实现的方法）时，厂里照办。但和普通外包不同的是，交回来的必须是成品 JSON 字节——交回散装文本（不带引号的裸字符串），整批货都会被退回。

## When to Use

- 对接前端或第三方 API 时：对方要求 `2026-09-24 10:30:00` 这类非默认时间格式，在类型上实现一次，所有输出自动生效。
- 金额与枚举的形态转换时：库里以"分"存金额、对外输出"元"；枚举存储为整数、对外输出字符串名——编解码边界统一处理。
- 兼容历史数据时：旧系统的字段格式与新代码不一致，自定义解码可以在读取时归一化。

何时不用：

- 默认格式可用：不为偶尔的输出差异增加两个方法的维护面。
- 只改键名：struct tag 一行就够，不需要接管整个编解码过程。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| struct tag | 只改键名与选项，不改值的形态 | 键名映射类需求 |
| 实现两个接口 | 完全接管该字段的编解码 | 值的默认 JSON 形态不合需求 |
| 嵌入 `time.Time` 再覆盖 | 借嵌入继承方法集，改动量小 | 只想微调默认行为 |
| 第三方格式库 | 现成的布局方案，零手写解析 | 不想自己处理格式细节 |

## Quick Start

前置条件：本机已安装 Go 工具链。本目录自带测试文件；仓库根目录没有 go.mod，因此用"单文件模式"直接运行：

```bash
cd concepts/30_custom_marshal
go run main.go                 # 跑演示
go test main_test.go main.go   # 跑测试（单文件模式，列出全部 .go 文件）
```

> 若想用 `go test ./...` 的常规方式，可在本目录建一个 `go.mod`（如 `module custom_marshal` + `go 1.26`），或先 `go mod init`——代码无需任何改动，之后 `go test ./...` 与 `go test -v` 均可用。

预期输出（`go run main.go`，节选）：

```text
========== 1. MarshalJSON：struct -> 自定义格式 ==========
原始时间: 2026-09-24 10:30:00
序列化结果: {"name":"发布 v1.0","at":"2026-09-24 10:30:00"}

========== 2. UnmarshalJSON：自定义格式 -> struct ==========
还原结果: name=发布 v1.0 at=2026-09-24 10:30:00 | round-trip 一致: true

========== 3. 格式错误的处理 ==========
解析错误: CustomTime 只接受 "2006-01-02 15:04:05" 格式，收到 "24/09/2026 10:30": ...
错误对象未改动: name="坏数据" at="0001-01-01 00:00:00"

========== 4. 零值与默认格式对照 ==========
默认 time.Time（未自定义）: {"std":"2026-09-24T10:30:00+08:00"}
```

`go test main_test.go main.go` 输出：

```text
ok  	command-line-arguments	0.4s
```

三个用例分别覆盖：正常 round-trip（编码再解码结果一致）、坏格式明确报错、零值稳定往返。

整套机制的入口只有两个固定签名。这段在做什么：定义 json 包用来"询问"类型的两个接口——实现了就接管对应方向的编解码，签名一个字符都不能改：

```go
type Marshaler   interface { MarshalJSON() ([]byte, error) }
type Unmarshaler interface { UnmarshalJSON(data []byte) error }
```

## How It Works

### 接收者规则：值进指针对出

`MarshalJSON` 用值接收者（接收者不带 `*`，方法拿到的是副本）——编码不改变原值；`UnmarshalJSON` 必须用指针接收者——解码要把结果写回调用方的变量，值接收者的修改会被丢弃。方法名、参数、返回值一个字符都不能改，改了实现的就不是 json 包认的那个接口。

### 类型接线：type CustomTime time.Time

`type CustomTime time.Time` 定义的自定义类型（基于底层类型造出的新类型）与 time.Time 的方法集完全独立——正因不带原方法集，自己定义的 `MarshalJSON` 才不会与原类型冲突。要用回 `Format`，先 `time.Time(t)` 转回底层类型再调用。

### 时间布局："2006-01-02 15:04:05" 不是占位符

Go 的 `time.Format`/`time.Parse` 不用 `%Y-%m-%d` 这类占位符，而是以一个固定参考时间 `2006-01-02 15:04:05 MST`（Unix 时间 1136214245）为模板：写出这个参考时间在目标格式下的样子，解析与格式化就按位对应。

记忆口诀：1 月、2 日、3 时（15 是下午 3 点）、4 分、5 秒、6 年。

输出第 1 段的 `"at":"2026-09-24 10:30:00"` 与第 4 段默认格式的 `2026-09-24T10:30:00+08:00`，差别正是这份自定义布局替换了默认的 RFC3339。

### data 里带着引号与 null

`UnmarshalJSON` 收到的 `data` 是原始 JSON 字节：字符串值自带双引号（`"2026-09-24 10:30:00"`），整段为 null 时 data 就是字面量 `null`。

所以解析前要先剥引号（`strings.Trim`），对 `null` 要特判——直接把 `null` 塞给 `time.Parse` 必然报错。

### 错误要明确，不要静默零值

坏格式时返回带上下文的错误（`fmt.Errorf` + `%w` 包装底层 parse 错误）。

调用方才能用 `errors.Is/As`（沿错误链判断与提取的标准工具）溯源（见 [23_error_basics](../23_error_basics/)、[24_error_chain](../24_error_chain/)）。

输出第 3 段的 `解析错误: CustomTime 只接受 "2006-01-02 15:04:05" 格式，收到 "24/09/2026 10:30": ...` 就是带上下文的报错。

注意 `encoding/json` 的行为：某个字段解析失败时其余字段照常解析，出错字段保持零值——输出第 3 段的 `错误对象未改动: name="坏数据" at="0001-01-01 00:00:00"` 演示的正是这一点。这意味着"失败"不等于"回滚"，复用旧 struct 接收数据时要小心字段被半更新。

### 关键语法速查

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func (t CustomTime) MarshalJSON() ([]byte, error)` | 接管编码 | 值接收者；返回值必须是合法 JSON 字节 |
| `func (t *CustomTime) UnmarshalJSON(data []byte) error` | 接管解码 | 指针接收者才能写回；签名不可改 |
| `time.Time(t).Format(layout)` | 自定义类型转回底层类型再格式化 | `CustomTime` 不继承 time.Time 的方法集 |
| `time.ParseInLocation(layout, s, time.Local)` | 按布局解析 | 不带时区信息时用本地时区，避免 UTC 偏移 |
| `type CustomTime time.Time` | 基于底层类型的自定义类型 | 与 time.Time 方法集完全独立，是接线的前提 |

这套"反射（reflect，标准库里在运行时读取和操作类型信息的包）+ 接口检测"的编解码路径。

也把接口机制（[05_struct_iface](../05_struct_iface/)）、tag 映射（[28_struct_tags](../28_struct_tags/)）与动态派发（[29_reflect_call](../29_reflect_call/)）串进了同一个真实场景。

## Pitfalls & Q&A

坑清单（每条均可复现）：

- **返回不带引号的裸字符串**。现象：`MarshalJSON` 返回 `[]byte("2026-09-24 10:30:00")` 后，整个文档序列化报错。原因：不带引号不是合法 JSON。解法：字符串值自己带引号再返回。
- **`UnmarshalJSON` 写成值接收者**。现象：解析"成功"但结果消失，排查许久才发现原因。原因：值接收者改的是副本，写不回调用方变量。解法：接收者加 `*`。
- **忘了剥引号**。现象：`string(data)` 直接 `time.Parse`，报 "parsing time" 错误。原因：data 是原始 JSON 字节，自带双引号。解法：先 `strings.Trim` 剥引号。
- **漏判 null**。现象：字段显式传 null 时直接报错，而调用方认为 null 应等价于零值（类型的默认初始值）。原因：data 是字面量 `null`，直接解析必然失败。解法：解析前特判 `null`，归一到零值。
- **直接调 `CustomTime.Format()`**。现象：编译错误，方法不存在。原因：基于类型的定义不继承原方法集。解法：先 `time.Time(t)` 转换再调。

**Q：为什么 `MarshalJSON` 返回 `([]byte, error)` 而不是 string？**

JSON 值未必是字符串——可能想输出对象或数字，`[]byte` 表达"一段原始 JSON 字节"这一通用形态。返回的切片会被 json 包原样嵌入输出文档，所以它自身必须是合法 JSON。

**Q：只想改时间格式，有没有比实现接口更轻的办法？**

有——字段保持 `time.Time` 给 struct 加嵌套类型也行，更常用的是第三方库的 LayoutString 类方案，或自定义类型嵌入：`type CustomTime struct{ time.Time }` 借嵌入继承方法集再覆盖 `MarshalJSON`。

显式实现两个接口最直白、零依赖，中小项目与教学场景首选。

**Q：`UnmarshalJSON` 报错后，struct 里其他字段是什么状态？**

`encoding/json` 逐字段解码，一个字段失败不会中断整体：其余字段保持已解析的值，出错字段保持零值，函数返回第一个错误。"失败"不等于"回滚"——这正是输出第 3 段展示的行为。

**Q：接口检测发生在什么时候，有性能代价吗？**

运行时逐值检测（类型断言到接口），每个实现 Marshaler 的字段都比默认路径多一次接口调用与一次字节拼接。热路径（程序反复执行的高频代码段）的大批量序列化可考虑缓存 encoder 或代码生成，但绝大多数业务场景这点开销可以忽略。

若想弄清接口检测在运行时如何发生，见 [../../internals/01_slices/](../../internals/01_slices/)。
