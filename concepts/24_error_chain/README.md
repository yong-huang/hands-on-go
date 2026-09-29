# 24 · 错误链：自定义 error 类型 + %w 三层包装 + Unwrap/As 溯源

> 错误链（error chain）是 Go 多层架构错误处理的骨架：底层返回带结构化字段的错误，中间每层用 `%w` 包装加上下文，链条却原样保留；顶层用 `errors.As` 取回底层字段、用 `errors.Is` 做语义判定。读完本篇你将掌握三层包装链的搭建方式、按类型/按值两种溯源的原理，以及"目标类型失配、`%v` 断链"等典型坑的成因与解法。

## Background

真实请求往往穿过多层：存储层 → 服务层 → API 层。每层都想加一句自己的上下文（"在哪一层、带什么参数错的"），这没有争议。

问题出在"加上下文"的写法上。惯用的 `fmt.Errorf("...: %v", err)` 只是把内层错误的消息拼进新字符串——消息变长了，但新旧错误之间不再有任何程序上的关联。于是顶层调用方失去了两样能力：

- 无法提取底层错误携带的字段（比如数据库错误码），没法写重试或告警逻辑
- 无法按预定义的错误值做判定，只能对着字符串做脆弱的 `strings.Contains` 匹配

标准库为此提供了一组配套：`%w` 包装动词让每层的新错误通过指针链住内层，`errors.Is/As` 沿链溯源。写法只差一个字符，换来结构化溯源与语义判定两种能力。

## What

错误链是用 `fmt.Errorf` 的 `%w` 动词逐层包装后形成的一串错误对象：每层包装返回一个新错误，消息里带上本层上下文，内部持有指向内层错误的指针。打印最外层会看到全部消息拼成的长文本，程序却仍能沿指针逐层走回源头。

可以把错误链想象成一条单链表：每层包装是链头节点，`Unwrap()` 就是指向内层的 next 指针。但和普通链表不同的是，链头的"值"是递归拼接的消息——`Error()` 一调用就输出整条链的完整文本。

**心智模型一句话**：错误链是一条单向指针串——每层 `%w` 都在链头加一节；`errors.As` 沿链找**类型**（取结构化字段），`errors.Is` 沿链找**值**（做语义判定），`errors.Unwrap` 则是手动剥一层的底层原语。

## When to Use

- 写多层服务时：存储、服务、API 各层逐层 `%w` 包装上抛，日志可读（消息逐层加长），顶层又能溯源回底层错误。
- 上层要按错误码做程序化处理时：底层错误定义成携带 `Code` 等字段的自定义类型，顶层用 `errors.As` 取出字段，驱动重试、告警或降级。
- 顶层要做语义响应时：把"资源不存在"这类业务语义定义成哨兵错误（包级固定错误值），用 `errors.Is` 判定后返回 404 等对应状态。

何时不用它：

- 纯透传的胶水层：没有新增信息就别包装，直接 `return err`，否则日志里堆满重复前缀
- 单层小函数、无人需要判定的错误：一次性消息足够，不必搭链
- 程序自身 bug 这类不可恢复错误：用 panic/recover 体系（见 [25_panic_recover](../25_panic_recover/)），不走错误链

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `errors.Is` | 沿链按值比较，返回布尔 | 只需"是不是这类错"的语义判定 |
| `errors.As` | 沿链按类型匹配并写入变量 | 需要取出底层的结构化字段 |
| `errors.Unwrap` | 手动剥一层，链尽返回 nil | 调试时想看清链条结构 |
| `%v` 拼接 | 只有字符串，无溯源能力 | 确定无人需要判定的一次性错误 |

## Quick Start

前置条件：已安装 Go 并可用 `go run`，无第三方依赖。

```bash
cd concepts/24_error_chain
go run main.go
```

真实输出（共 22 行，节选）：

```text
========== 1. 三层包装链：消息逐层加长，链条逐层相扣 ==========
api: service: db error 1050: invalid user id

========== 2. errors.As：沿链定位 *DBErr，提取结构化字段 ==========
命中 *DBErr → Code=1050, Msg="invalid user id"（As 会逐层 Unwrap，不在乎包了几层）

========== 3. errors.Is：判定链中的语义哨兵 ==========
err2 本体: api: service: query users: id=999: record not found
errors.Is(err2, ErrNotFound): true → 判定为资源不存在，返回 404
errors.Is(err2, ErrForbidden): false → 其他语义不误判

========== 4. errors.Unwrap：手动剥一层，链的本质是单向指针 ==========
第 0 层: api: service: query users: id=999: record not found
剥 1 层: service: query users: id=999: record not found
再往下剥: record not found → 露出哨兵本身，下一层 Unwrap 返回 nil，链到头了

========== 5. 完整链条：一层层 %w 拼出来的字符串形式 ==========
layer 0: api: service: query users: id=999: record not found
layer 3: record not found
```

搭链的代码就是三层函数。这段在做什么：底层返回自定义错误或包装哨兵，中间两层各用 `%w` 加一节链：

```go
// queryUser 模拟底层：既返回自定义类型，也可能返回哨兵错误。
func queryUser(id int) error {
	if id <= 0 {
		return &DBErr{Code: 1050, Msg: "invalid user id"}
	}
	if id > 100 {
		return fmt.Errorf("query users: id=%d: %w", id, ErrNotFound)
	}
	return nil
}

// service 模拟中间层：加上下文（service: ...），用 %w 保住链条。
func service(id int) error {
	if err := queryUser(id); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return nil
}

// handler 模拟顶层：再加一层上下文，链变成三层。
func handler(id int) error {
	if err := service(id); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	return nil
}
```

## How It Works

### 自定义 error 类型：错误也能携带结构化数据

`error` 只是 `interface { Error() string }`，任何类型实现它就是 error。

`DBErr{Code int, Msg string}` 用**指针接收者**实现 `Error() string`，于是 `*DBErr` 既能当普通错误返回（打印 `db error 1050: invalid user id`），又能把 `Code` 这种程序化处理需要的字段带在身上——这是字符串哨兵做不到的：

```go
// DBErr 是底层（存储层）的错误类型：除了消息还携带结构化字段 Code。
// 实现 Error() string 后它就是合法的 error。
type DBErr struct {
	Code int
	Msg  string
}

func (e *DBErr) Error() string {
	return fmt.Sprintf("db error %d: %s", e.Code, e.Msg)
}

// ErrNotFound 是语义哨兵：调用方用它做"资源不存在"这类业务判定。
var (
	ErrNotFound  = errors.New("record not found")
	ErrForbidden = errors.New("forbidden")
)
```

### `%w` 多层包装：每层都是"链头 + 内层指针"

`fmt.Errorf("service: %w", err)` 返回的 `*fmt.wrapError` 有两个字段：格式化后的消息，和指向内层的 `unwrap()`。顶层再包一次，就得到三层链。

`Error()` 是递归拼出来的——所以 Quick Start 第 1 段输出里看到 `api: service: db error 1050: ...` 一条完整消息，内部却是三个独立对象靠指针相扣。

### `errors.As`：按类型溯源，取回结构化字段

`errors.As(err, &dbErr)`（目标是 `**DBErr`）从链头开始，逐层检查"这一层的动态类型是不是 `*DBErr`"，命中就把那一层的值写进 `dbErr`。它对包装层数无感，是"上层要读底层错误码"时的标准入口。

输出第 2 段的 `命中 *DBErr → Code=1050` 正是它写回的结果。注意目标必须传**指针的指针**：要往里写值。

### `errors.Is` 与 `errors.Unwrap`：按值判定与手动剥层

`errors.Is(err, ErrNotFound)` 同样沿链走，只是每层做 `==` 比较（若错误实现了 `Is(error) bool` 还会先调它），命中哨兵即返回 true——输出第 3 段的 `true → 判定为资源不存在，返回 404` 与 `false → 其他语义不误判` 就是两次判定的结果。

`errors.Unwrap` 是二者的底层原语：每调一次剥一层，返回内层 error；链尽返回 `nil`。第 5 段输出的 `layer 0..3` 就是手动循环 Unwrap 打出的完整链条。

### 关键写法速查

| 写法 | 语义 | 备注 |
| --- | --- | --- |
| `func (e *DBErr) Error() string` | 自定义 error 类型 | 指针接收者：链中传递/匹配都用 `*DBErr` |
| `fmt.Errorf("ctx: %w", err)` | 包装并保留链条 | 换成 `%v` 则链断，`As`/`Is` 全部判不中 |
| `errors.As(err, &dbErr)` | 按类型溯源 | `dbErr` 必须是 `*DBErr` 类型（即 `**DBErr`） |
| `errors.Is(err, ErrNotFound)` | 按值/语义判定 | 哨兵定义在包级，`Err` 前缀命名 |
| `errors.Unwrap(err)` | 手动剥一层 | 链尽返回 `nil`；`errors.Is/As` 内部就靠它 |

## Pitfalls & Q&A

踩坑清单（每条都可在 demo 中复现）：

- **`%v` 冒充 `%w`**。现象：`fmt.Errorf("service: %v", err)` 编译运行都正常，但溯源全部失效。原因：`Unwrap()` 没了，链条断在这一层。解法：把 demo 里的 `%w` 改成 `%v`，第 2 段立刻打印"链中没有 *DBErr"——需要保链时永远用 `%w`。
- **`As` 目标类型失配**。现象：`var dbErr DBErr; errors.As(err, &dbErr)` 编译不过；写成匹配值类型 `DBErr` 而链里是 `*DBErr`，则永远判不中。原因：类型必须逐字节一致。解法：目标声明成链中挂的同一类型（`*DBErr`）。
- **接收者形式不一致**。现象：`Error()` 用值接收者、链里挂的却是 `*DBErr`（或反之），`As` 同样失配。解法：约定一种接收者形式并全链一致。
- **多层包装后裸比对失效**。现象：`err == ErrNotFound` 为 false。原因：链头是最外层包装，不等于任何内层值。解法：必须用 `errors.Is`。

**Q：为什么 `errors.As` 的第二个参数要传 `&dbErr`（`**DBErr`）？**

A：因为 `As` 需要把链中命中的那个值**写进**调用方的变量，形参是 `target any`，反射按 `target` 指向的类型做匹配。传值类型只能匹配、不能写入，所以签名为 `As(err error, target any)`，实际传一级指针的地址。

**Q：`errors.Is` 和 `errors.As` 都沿链走，实现上差在哪？**

A：循环体几乎一样（都是 `for err != nil; err = Unwrap(err)`），差别只在每层的判定：`Is` 先 `==` 比较再询问 `Is(error) bool` 方法；`As` 用反射比较动态类型并做赋值。这也解释了为什么两者都"不在乎包了几层"。

**Q：自定义错误该用哨兵（`ErrXxx`）还是结构化类型（`XxxError`）？**

A：看调用方需要什么：只需要"是不是这类错"的布尔判定，哨兵最轻；需要携带数据（错误码、重试时间、资源名）让上层程序化处理，就定义类型并配合 `errors.As`。两者可以同链共存——demo 里 `DBErr` 与 `ErrNotFound` 就各走一条链。

**Q：什么时候该停止包装、直接把错误原样往上返回？**

A：加上下文是为了让"错误发生的位置"可追溯；如果当前层没有新增信息（比如只是透传的胶水代码），直接 `return err` 更好——包装每多一层，消息就多一截前缀，日志里全是重复噪音。经验法则：只在真正做了决策或转换的层包装。
