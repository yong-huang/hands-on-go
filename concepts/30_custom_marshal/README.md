# 30 · 自定义序列化：让 json 包按你的格式编解码

> `encoding/json` 默认把 `time.Time` 序列化成 RFC3339（`2026-09-24T10:30:00+08:00`），前端要 `2026-09-24 10:30:00` 怎么办？答案不是在业务代码里到处手动转换，而是给类型实现 `json.Marshaler` / `json.Unmarshaler` 两个接口，从此整个编解码器都听你的。这是接口机制最实用的落地点，也是本系列 concepts 线的收官实验。

## 1. 为什么需要它

struct tag（见 [28_struct_tags](../28_struct_tags/)）只能改"键名"，改不了"值的形态"。当值的默认 JSON 形态不合需求——时间格式、金额用"分"存储却要输出"元"、枚举要输出字符串——就需要接管整个字段的编解码过程。Go 的方案极简：`json` 包在编解码每个值前都会问一句"你实现了 Marshaler/Unmarshaler 吗"，实现了就交给你。理解这条路径，你就读懂了 time.Time、big.Int 与所有第三方类型的 JSON 行为。

## 2. 总览：核心机制一图看懂

![自定义 JSON 序列化时序图](images/marshal.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/30_custom_marshal/images/marshal.html)
> （或本地打开 [`images/marshal.html`](images/marshal.html)）。

**心智模型一句话**：`json.Marshal` 每编码一个值都先做接口检测——发现 `MarshalJSON() ([]byte, error)` 就交出编码权；反向解码时发现 `UnmarshalJSON([]byte) error` 就交出解码权，指针接收者保证结果能写回你的变量。

## 3. 快速开始

本实验是全系列唯一带测试文件的实验。仓库根目录没有 go.mod，因此用"单文件模式"直接运行（不需要 go.mod）：

```bash
cd concepts/30_custom_marshal
go run main.go                 # 跑演示
go test main_test.go main.go   # 跑测试（单文件模式，列出全部 .go 文件）
```

> 若你想用 `go test ./...` 的常规方式，可在本目录建一个 `go.mod`（如 `module custom_marshal` + `go 1.26`）即可，代码无需任何改动。

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

三个用例分别覆盖：正常 round-trip、坏格式明确报错、零值稳定往返。

## 4. 核心概念

### 4.1 两个接口，签名是固定的

```go
type Marshaler   interface { MarshalJSON() ([]byte, error) }
type Unmarshaler interface { UnmarshalJSON(data []byte) error }
```

`MarshalJSON` 用**值接收者**（编码不改变原值）；`UnmarshalJSON` 必须用**指针接收者**——解码要把结果写回调用方的变量，值接收者的修改会被丢弃。方法名、参数、返回值一个字符都不能改，改了实现的就不是 json 包认的那个接口。

### 4.2 时间布局："2006-01-02 15:04:05" 不是占位符

Go 的 `time.Format`/`time.Parse` 不用 `%Y-%m-%d` 这类占位符，而是以一个固定参考时间 `2006-01-02 15:04:05 MST`（Unix 时间 1136214245）为模板：你写出这个参考时间在目标格式下的样子，解析/格式化就按位对应。记忆口诀：1 月、2 日、3 时（15 是下午 3 点）、4 分、5 秒、6 年。

### 4.3 data 里带着引号与 null

`UnmarshalJSON` 收到的 `data` 是**原始 JSON 字节**：字符串值自带双引号（`"2026-09-24 10:30:00"`），整段为 null 时 data 就是字面量 `null`。所以解析前要先剥引号（`strings.Trim`），对 `null` 要特判——直接把 `null` 塞给 `time.Parse` 必然报错。

### 4.4 错误要明确，不要静默零值

坏格式时返回带上下文的错误（`fmt.Errorf` + `%w` 包装底层 parse 错误），调用方才能 `errors.Is/As` 溯源（见 [23_error_basics](../23_error_basics/)、[24_error_chain](../24_error_chain/)）。注意 `encoding/json` 的行为：某个字段解析失败时**其余字段照常解析**，出错字段保持零值——main.go 第 3 节演示了这一点。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `func (t CustomTime) MarshalJSON() ([]byte, error)` | 接管编码 | 值接收者；返回值必须是合法 JSON 字节 |
| `func (t *CustomTime) UnmarshalJSON(data []byte) error` | 接管解码 | 指针接收者才能写回；签名不可改 |
| `time.Time(t).Format(layout)` | 自定义类型转回底层类型再格式化 | `CustomTime` 不继承 time.Time 的方法集 |
| `time.ParseInLocation(layout, s, time.Local)` | 按布局解析 | 不带时区信息时用本地时区，避免 UTC 偏移 |
| `type CustomTime time.Time` | 基于底层类型的自定义类型 | 与 time.Time 方法集完全独立，是接线的前提 |

坑清单：

- `MarshalJSON` 返回 `[]byte("2026-09-24 10:30:00")`（不带引号）不是合法 JSON，整个文档报错——字符串值必须自己带引号
- `UnmarshalJSON` 写成值接收者：解析"成功"但结果消失，排查半天才发现是接收者类型
- `data` 直接 `string(data)` 后去 `time.Parse`：忘了剥引号，报 "parsing time" 错误
- 漏判 `null`：字段显式传 null 时直接报错，而调用方往往认为 null 应等价于零值
- `CustomTime` 不能直接调 `.Format()`——基于类型的定义不带原方法集，要先 `time.Time(t)` 转换

## 6. 文件结构

```text
concepts/30_custom_marshal/
├── README.md                    # 本文件：原理 + 运行指引 + 深入要点
├── main.go                      # 演示：marshal 一圈 / unmarshal 一圈 / 坏格式报错 / 零值对照
├── main_test.go                 # 三个用例：round-trip、坏格式、零值（全系列唯一测试文件）
└── images/
    ├── marshal.sequence.json    # 图源：双向编解码 + 错误路径时序（Typed JSON IR）
    ├── marshal.html             # 交互版：自包含单文件，可缩放/切换主题
    └── marshal.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：`go test` 怎么跑？为什么用单文件模式？**
A：本仓库根目录刻意不放 go.mod（每个实验都是 `go run main.go` 的独立单文件），所以用 `go test main_test.go main.go` 把两个文件一起列给编译器即可，测试照常运行。若在目录内 `go mod init` 之后，就可以用更常规的 `go test ./...` 与 `go test -v`。

**Q：为什么 `MarshalJSON` 返回 `([]byte, error)` 而不是 string？**
A：JSON 值未必是字符串——你可能想输出对象或数字，`[]byte` 表达"一段原始 JSON 字节"这一通用形态。返回的切片会被 json 包原样嵌入输出文档，所以它自身必须是合法 JSON。

**Q：只想改时间格式，有没有比实现接口更轻的办法？**
A：有——字段保持 `time.Time`，给 struct 加一条嵌别的类型也行；但更常用的是第三方库（如 wttrin 的 LayoutString 方案）或自定义类型嵌入：`type CustomTime struct{ time.Time }` 借嵌入继承方法集再覆盖 MarshalJSON。显式实现两个接口最直白、零依赖，教学与中小项目首选。

**Q：`UnmarshalJSON` 报错后，struct 里其他字段是什么状态？**
A：`encoding/json` 逐字段解码，一个字段失败不会中断整体：其余字段保持已解析的值，出错字段保持零值，函数返回第一个错误。这意味着"失败"不等于"回滚"——复用旧 struct 接收数据时要小心字段被半更新的情况。

**Q：接口检测发生在什么时候，有性能代价吗？**
A：运行时逐值检测（类型断言到接口），每个实现 Marshaler 的字段都比默认路径多一次接口调用与一次字节拼接。热路径的大批量序列化可考虑缓存 encoder 或代码生成，但绝大多数业务场景这点开销可以忽略。

## 8. 总结

自定义序列化的全部内容就是两个固定签名的方法：值接收者的 `MarshalJSON` 交出编码权，指针接收者的 `UnmarshalJSON` 交出解码权，外加一条纪律——返回合法 JSON、错误要明确。它是 [05_struct_iface](../05_struct_iface/) 接口机制、[28_struct_tags](../28_struct_tags/) tag 映射与 [29_reflect_call](../29_reflect_call/) 动态派发的实战合流点：json 包正是靠反射 + 接口检测把这一切串起来的。

至此 concepts 线 30 个实验收官。若想弄清"接口检测到底怎么在运行时发生"，回到 [../../internals/01_slices/](../../internals/01_slices/) 从运行时底层把整条线再走一遍。
