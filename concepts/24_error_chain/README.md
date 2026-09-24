# 24 · 错误链：自定义 error 类型 + %w 三层包装 + Unwrap/As 溯源

> 真实请求往往穿过多层：存储层 → 服务层 → API 层。每层都想加一句自己的上下文（"在哪一层错的"），但最上层的调用方还需要两样东西：底层错误的**结构化字段**（如数据库错误码）和**语义判定**（是不是"资源不存在"）。`%w` 包装链把这三层串成一条单向指针串——消息越拼越长，链条原样保留；`errors.As` 按类型溯源取字段，`errors.Is` 按语义溯源做判定。本实验用一条三层包装链把这套配合技完整跑一遍。

## 1. 为什么需要它

只用 `fmt.Errorf("...: %v", err)` 加上下文，消息是好看了，但错误链被生生折断：上层既无法提取底层 `DBErr` 里的 `Code` 字段，也无法用 `errors.Is` 识别哨兵错误——只能对着字符串做脆弱的 `strings.Contains`。而有了 `%w`，每一层包装返回的新错误都通过 `Unwrap()` 指向内层，链条从顶层一路通到源头。代价几乎为零：写法从 `%v` 换成 `%w`，换来的是结构化溯源和语义判定两种能力。

## 2. 总览：核心机制一图看懂

![三层错误链：底层 *DBErr → 中层 service: %w → 顶层 api: %w，Unwrap/As 溯源](images/chain.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/24_error_chain/images/chain.html)
> （或本地打开 [`images/chain.html`](images/chain.html)）。

**心智模型一句话**：错误链是一条单向指针串——每层 `%w` 都在链头加一节；`errors.As` 沿链找**类型**（取结构化字段），`errors.Is` 沿链找**值**（做语义判定），`errors.Unwrap` 则是手动剥一层的底层原语。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 自定义 error 类型：错误也能携带结构化数据

`error` 只是 `interface { Error() string }`，任何类型实现它就是 error。`DBErr{Code int, Msg string}` 用**指针接收者**实现 `Error() string`，于是 `*DBErr` 既能当普通错误返回（打印 `db error 1050: invalid user id`），又能把 `Code` 这种程序化处理需要的字段带在身上——这是字符串哨兵做不到的。

### 4.2 `%w` 多层包装：每层都是"链头 + 内层指针"

`fmt.Errorf("service: %w", err)` 返回的 `*fmt.wrapError` 有两个字段：格式化后的消息，和指向内层的 `unwrap()`。顶层再包一次，就得到三层链。`Error()` 是递归拼出来的——所以打印时看到 `api: service: db error 1050: ...` 一条完整消息，内部却是三个独立对象靠指针相扣。

### 4.3 `errors.As`：按类型溯源，取回结构化字段

`errors.As(err, &dbErr)`（目标是 `**DBErr`）从链头开始，逐层检查"这一层的动态类型是不是 `*DBErr`"，命中就把那一层的值写进 `dbErr`。它对包装层数无感，是"上层要读底层错误码"时的标准入口。注意目标必须传**指针的指针**：要往里写值。

### 4.4 `errors.Is` 与 `errors.Unwrap`：按值判定与手动剥层

`errors.Is(err, ErrNotFound)` 同样沿链走，只是每层做 `==` 比较（若错误实现了 `Is(error) bool` 还会先调它），命中哨兵即返回 true。`errors.Unwrap` 是二者的底层原语：每调一次剥一层，返回内层 error；链尽返回 `nil`。第 5 段输出的 `layer 0..3` 就是手动循环 Unwrap 打出的完整链条。

## 5. 关键字段与坑清单

| 写法 | 语义 | 备注 |
| --- | --- | --- |
| `func (e *DBErr) Error() string` | 自定义 error 类型 | 指针接收者：链中传递/匹配都用 `*DBErr` |
| `fmt.Errorf("ctx: %w", err)` | 包装并保留链条 | 换成 `%v` 则链断，`As`/`Is` 全部判不中 |
| `errors.As(err, &dbErr)` | 按类型溯源 | `dbErr` 必须是 `*DBErr` 类型（即 `**DBErr`） |
| `errors.Is(err, ErrNotFound)` | 按值/语义判定 | 哨兵定义在包级，`Err` 前缀命名 |
| `errors.Unwrap(err)` | 手动剥一层 | 链尽返回 `nil`；`errors.Is/As` 内部就靠它 |

坑清单（每条都可在 demo 中复现）：

- **`%v` 冒充 `%w`**：`fmt.Errorf("service: %v", err)` 编译运行都正常，但 `Unwrap()` 没了——把 demo 里的 `%w` 改成 `%v`，第 2 段立刻打印"链中没有 *DBErr"。
- **`As` 目标类型失配**：写成 `var dbErr DBErr; errors.As(err, &dbErr)` 编译不过；写成匹配值类型 `DBErr` 而链里是 `*DBErr`，则永远判不中——类型必须逐字节一致。
- **接收者形式不一致**：`Error()` 用值接收者、链里挂的却是 `*DBErr`（或反之），`As` 同样失配。约定一种并全链一致。
- **多层包装后裸比对失效**：`err == ErrNotFound` 为 false（链头是最外层包装），必须用 `errors.Is`。

## 6. 文件结构

```text
concepts/24_error_chain/
├── README.md                    # 本文件：八节结构讲解
├── main.go                      # 演示程序：DBErr 定义 → 三层包装 → As/Is/Unwrap 溯源
└── images/
    ├── chain.architecture.json  # 图源 JSON（架构图管线输入）
    ├── chain.html               # 交互版架构图（浏览器打开探索）
    └── chain.svg                # 静态架构图（README 内嵌）
```

## 7. 深入要点

**Q1：为什么 `errors.As` 的第二个参数要传 `&dbErr`（`**DBErr`）？**
因为 `As` 需要把链中命中的那个值**写进**调用方的变量，形参是 `target any`，反射按 `target` 指向的类型做匹配。传值类型只能匹配、不能写入，所以签名为 `As(err error, target any)`，实际传一级指针的地址。

**Q2：`errors.Is` 和 `errors.As` 都沿链走，实现上差在哪？**
循环体几乎一样（都是 `for err != nil; err = Unwrap(err)`），差别只在每层的判定：`Is` 先 `==` 比较再询问 `Is(error) bool` 方法；`As` 用反射比较动态类型并做赋值。这也解释了为什么两者都"不在乎包了几层"。

**Q3：自定义错误该用哨兵（`ErrXxx`）还是结构化类型（`XxxError`）？**
看调用方需要什么：只需要"是不是这类错"的布尔判定，哨兵最轻；需要携带数据（错误码、重试时间、资源名）让上层程序化处理，就定义类型并配合 `errors.As`。两者可以同链共存——demo 里 `DBErr` 与 `ErrNotFound` 就各走一条链。

**Q4：什么时候该停止包装、直接把错误原样往上返回？**
加上下文是为了让"错误发生的位置"可追溯；如果当前层没有新增信息（比如只是透传的胶水代码），直接 `return err` 更好——包装每多一层，消息就多一截前缀，日志里全是重复噪音。经验法则：**只在真正做了决策或转换的层包装**。

## 8. 总结

错误链的三个动作对应三种需求：`%w` 包装是"加上下文不丢链"，`errors.As` 是"按类型取数据"，`errors.Is` 是"按语义做判定"；`errors.Unwrap` 则让你能亲手把这条链剥开看清本质。掌握它们，多层架构里的错误处理才能既可读又可判。错误处理的另一面是"不可恢复的异常"——panic 与 recover 见下一篇 [../25_panic_recover/](../25_panic_recover/)；错误链的运行时成本与包装器实现细节，见 internals 深入篇。
