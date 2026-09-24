# 28 · Struct Tag 解析：反引号里的元数据，运行时才拆封

> struct tag 是写在字段反引号里的字符串，编译器不解释它，`reflect` 也不替你解析语义——它只是躺在 `StructField.Tag` 里的一段文本。encoding/json、validate、orm 这些框架的全部"魔法"，起点都是同一个动作：运行时读出这段字符串，按自己的协议解析。本实验用 100 行标准库代码把这条流水线跑通：读 tag → json 键名映射 → validate 规则检查 → 简化序列化。

## 1. 为什么需要它

同一个 `User` 结构体，序列化时想要 `{"name":"Ada"}`，校验时想要"Name 必填、Age 不能为负"——又不希望为每个结构体手写重复代码。Go 的答案是 tag：把元数据以约定格式挂在字段上，通用代码通过反射统一解析。理解 tag 解析，就看穿了 json/validate 等框架的第一层封装。另一个经典困惑也在本实验解决：`Get` 把"没写 tag"和"写了空 tag"都折叠成 `""`，只有 `Lookup` 能区分两者。

## 2. 总览：核心机制一图看懂

![Struct Tag 解析流水线](images/tags.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/28_struct_tags/images/tags.html)
> （或本地打开 [`images/tags.html`](images/tags.html)）。

**心智模型一句话**：tag 解析是一条五步流水线——结构体声明 → 反射逐字段读 `Tag` → 按键名映射消费（序列化）、按规则消费（校验），最后汇成输出。上行是声明与反射读取，下分序列化/校验两条支线。

## 3. 快速开始

```bash
cd concepts/28_struct_tags
go run main.go
```

预期输出（节选，共 23 行）：

```text
========== 1. Lookup vs Get：区分"没写 tag"和"写了空 tag" ==========
A: Lookup=("a", true) | Get="a"
B: Lookup=("", true) | Get=""
C: Lookup=("", false) | Get=""
→ Get 把"没写"和"写了空串"都折叠成 ""；Lookup 用 ok 区分两者
========== 2. json 键名映射 → 简化序列化 miniMarshal ==========
miniMarshal: {"name":"Ada","age":"36","email":"ada@example.com"}
========== 4. validate：非法用户（Name 空 + Age 为负）==========
FAIL Name: required 但为零值
FAIL Age: -1 < min=0
```

## 4. 核心概念

### 4.1 tag 的读取：Lookup 与 Get

字段的 tag 存在 `reflect.StructField.Tag`（类型 `reflect.StructTag`）里。`Get(key)` 只返回值，查不到返回 `""`；`Lookup(key)` 返回 `(值, ok)`。Demo 结构体三个字段（写了 `"a"` / 写了空串 / 没写）的真实输出证明：只有 `Lookup` 的 `ok` 能区分"没写"与"写了空串"。判断 tag 是否存在，永远用 `Lookup`。

### 4.2 json 键名映射的约定

`encoding/json` 的键名规则可以几行复刻：`Lookup("json")` 未命中或值为 `"-"` → 跳过该字段；命中则取逗号前的部分作键名（`"age,omitempty"` 的键是 `age`）；键名为空（`json:""`）退回字段名。本实验的 `keyOf` 就是这套规则。

### 4.3 validate：按规则消费 tag

校验是 tag 的另一种消费方式：`Lookup("validate")` 未命中就跳过（所以 `Email` 不参与校验）；命中后解析规则串——`required` 检查零值（`Value.IsZero`），`min=N` 用 `strconv` 取出 N 再比较。校验函数不需要知道字段含义，只认 Kind 与规则串。

### 4.4 miniMarshal：简化序列化器

只输出带 json tag 的字段，按 `Kind` 格式化后拼成 `{"key":"value",...}`。它与 `encoding/json` 的差距只在细节（嵌套、转义、omitempty），原理完全一致：读 tag → 键名映射 → 值格式化。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `` `json:"name" validate:"required"` `` | 多个 tag 空格分隔，各自独立解析 | key:value 对，value 可含逗号选项 |
| `Tag.Lookup(key)` | 返回 `(值, ok)` | 唯一能区分"没写"与"空串"的手段 |
| `Tag.Get(key)` | 只返回值 | 查不到与空串都返回 `""` |
| `json:"-"` | 明确跳过该字段 | 与"没写 tag"是两个语义 |
| `json:"age,omitempty"` | 键名 + 选项 | 解析时要先按逗号切分 |

坑清单：

- `f.Tag.Get("json") == ""` 不能证明没写 tag——`json:""` 也返回 `""`，判存在用 `Lookup`
- `keyOf` 解析 tag 值要取逗号前部分，否则 `"age,omitempty"` 会被当成键名
- 未导出字段（小写开头）反射读不到有效值，tag 写了也白写
- tag 字符串用反引号而非双引号：双引号里的 `\"` 转义会让 tag 解析失败
- 校验失败不应 panic，返回报告（或 error 切片）让调用方决定怎么处理

## 6. 文件结构

```text
concepts/28_struct_tags/
├── README.md                # 本文件：原理 + 运行指引 + 深入要点
├── main.go                  # Demo：Lookup/Get 对比 + keyOf + validate + miniMarshal
└── images/
    ├── tags.workflow.json   # 图源：tag 解析五步流水线（Typed JSON IR）
    ├── tags.html            # 交互版：自包含单文件，可缩放/切换主题
    └── tags.svg             # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 tag 用反引号字符串而不是结构化的注解语法？**
A：Go 刻意不给语言加注解机制——tag 就是一段普通字符串常量，编译期原样存进类型元数据，语义完全交给消费方约定。这换来了零语言成本：任何库都能定义自己的 tag 协议，代价是没有编译期检查（tag 写错只在运行时表现为"不生效"）。

**Q：`Lookup` 和 `Get` 底层差别是什么？**
A：`Get(key)` 等价于 `v, _ := Tag.Lookup(key); return v`。`StructTag.Lookup` 按约定格式扫描字符串：跳过空格、匹配 key、校验冒号与引号配对，格式非法的片段会被跳过——所以格式错误的 tag 不会报错，只是"看不见"。

**Q：reflect 遍历字段解析 tag，性能如何？**
A：慢一个数量级以上：`TypeOf`/`Field` 有分配，tag 解析每次都重新扫描字符串。框架的标准优化是首次解析后把"字段 → 键名/规则"的映射缓存起来，`encoding/json` 内部就是这么做的。

**Q：`validate:"required"` 对 int 字段会怎样？**
A：本实验用 `IsZero` 统一判断，`Age=0` 也会判为"必填缺失"——这正是很多校验库要求指针类型（`*int`，nil 才算缺）的原因：零值与"没填"在值类型上无法区分，反射只能看到值。

## 8. 总结

tag 解析的本质是"字符串协议 + 反射读取"：`Lookup` 拿到原始文本，消费方按约定解析出键名映射与校验规则，序列化与校验只是同一份元数据的两种消费。跑通 §3 的输出后，可以对照 §5 的坑清单改几个 tag 看行为变化。

下一篇 [../29_reflect_call/](../29_reflect_call/) 看反射的动态侧：`reflect.New` 创建实例、`MethodByName` 查找方法、`Call` 拆包参数与返回值。
