# 27 · 反射基础：运行时打开类型的盒子

> 反射（`reflect`，Go 标准库里在运行时读取和操作类型信息的包）把类型检查从编译期搬到运行时：程序跑起来之后能问"这个 struct 有哪些字段、每个字段是什么类型、这个值能不能改"。读完你能用 `TypeOf`/`ValueOf` 双入口取类型与值、按 Kind 写通用分派、用 `NumField`/`Field(i)` 遍历字段，并说清 `CanSet` 划定的可写边界。

## Background

没有运行时类型信息的日子里，想打印任意 struct 的每个字段，就得为每个类型手写一份代码：struct 加一个字段，打印函数跟着改一次。类型写死在代码里，通用工具无从谈起。

痛点集中在通用框架层：序列化、ORM（对象—关系映射，struct 与数据库表互转的框架）、配置加载面对的是"任意类型"，编译期根本不知道会来什么 struct。逐类型手写枚举，等于放弃"通用"二字。

`encoding/json` 却能用一个 `json.Marshal` 序列化任何 struct——靠的正是反射：运行时读类型信息、遍历字段、按 tag（字段旁反引号里的元数据）决定键名。代价是运行时开销与失去编译期检查，所以反射属于框架层工具，不是日常业务代码的默认选择。

## What

反射是一种在运行时检查类型、读写值的能力，Go 里由 `reflect` 包提供两个并列入口：`reflect.TypeOf(x)` 返回类型元信息，`reflect.ValueOf(x)` 返回包着值本身的 `reflect.Value`。

心智模型一句话：`TypeOf` 与 `ValueOf` 是两个并列入口，一个给类型信息、一个给值信息；汇聚到字段遍历（Name/Type/Value/Kind），只有来自 `reflect.New + Elem()` 的可寻址 Value 才能走到写入这一步。

可以把反射想象成"隔着包装盒验货"：TypeOf 读盒子外面的标签（类型名、种类、字段清单），ValueOf 打开盒子看到商品本身。但和真开箱不同的是，从普通变量拿到的 Value 只是商品的复印件（副本），要在原件上落笔，必须持有一份指向原件的凭证（指针再解引用）。

## When to Use

- 写通用序列化、打印或比较工具时：函数签名只有 `any`，需要在运行时枚举 struct 字段并按类型取值——`encoding/json` 与 `fmt` 包内部走的就是这条路。
- 写配置加载或 ORM 映射时：字段名与外部键名（数据库列、JSON/YAML 键）不一致，需要按声明顺序遍历字段、读 tag 建立映射。
- 写胶水代码时：调用方可能传入几十种 struct，为每种手写一份转换代码，不如在运行时统一遍历。

何时不用：

- 类型集合编译期已知：接口加类型 switch 或断言更快、有编译期检查，反射是最后手段。
- 热路径代码（程序反复执行的高频代码段）：反射每次操作都付装箱（boxing，值被拷贝一份存进接口）与检查的开销，循环里的 `v.Field(i)` 是典型误用。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 直接访问字段 `emp.Name` | 编译期检查，零额外开销 | 类型已知时一律选它 |
| 类型断言 / 类型 switch | 运行时一次判定，成本远低于反射 | 类型有限且可枚举 |
| 泛型 | 编译期实例化，无接口装箱 | 算法与具体类型无关、集合可枚举 |
| 反射 | 运行时读取任意类型的结构 | 类型在编译期不可知的框架层 |

## Quick Start

前置条件：本机已安装 Go 工具链（`go run` 可用），无第三方依赖。

```bash
cd concepts/27_reflect_basics
go run main.go
```

预期输出（节选，23 行）：

```text
========== 1. TypeOf / ValueOf 双入口 ==========
TypeOf: Name=Employee Kind=struct NumField=3
ValueOf: Kind=struct CanSet=false
========== 3. 遍历结构体字段：Name/Type/Value/Kind ==========
字段0: Name=Name    Type=string   Kind=string   Value=Ada tag="name"
字段1: Name=Age     Type=int      Kind=int      Value=36 tag="age"
字段2: Name=Salary  Type=float64  Kind=float64  Value=9200.5 tag="salary"
========== 4. 可寻址性：CanSet 与 reflect.New ==========
ValueOf(变量): CanSet=false
reflect.New + Elem(): CanSet=true
反射写入后: {Name:Grace Age:45 Salary:0}（原 emp 未受影响: true）
```

节选跳过了第 2 段（Name 与 Kind 的对照演示），完整程序共 23 行输出；诚实预期是进程以退出码 0 正常结束。

## How It Works

### 双入口：TypeOf 看类型，ValueOf 看值

`reflect.TypeOf(x)` 返回 `reflect.Type`（接口）：类型名、Kind、字段列表、方法集都在这。`reflect.ValueOf(x)` 返回 `reflect.Value`（结构体）：包着值本身，能读、条件允许时能写。两者 `Kind()` 一致——看类型用前者，操作值用后者。

输出第 1 段的 `TypeOf: Name=Employee Kind=struct NumField=3` 与 `ValueOf: Kind=struct CanSet=false` 就是两个入口各自交出的答卷。

### Name 与 Kind：名字与种类

`Name()` 是声明时的类型名，匿名类型（如 `map[string]int`）名字为空；`Kind()` 是底层种类——`type ID int` 的 Name 是 `ID`，Kind 仍是 `int`。写通用代码判断"能做什么"要用 Kind（`switch v.Kind()`），不能拿 Name 比较。

### 字段遍历：NumField + Field(i)

对 Kind 为 `struct` 的 Value：`NumField()` 给字段数，`Field(i)` 按声明顺序取出每个字段；字段名与 tag 挂在 `Type.Field(i)` 返回的 `StructField`（类型侧的字段描述结构）上。

输出第 3 段每行的 Name/Type/tag 三列来自 `Type.Field(i)`，`Value=` 一列来自值侧的 `v.Field(i)`——"类型侧取名、值侧取值"的拼装。

### 可寻址性：CanSet 决定能否写

`ValueOf(emp)` 拿到的是 `emp` 的副本：参数按值装箱（boxing，值被拷贝一份存进接口的数据指针）传入，反射拿到的是复印件，改它没有意义，所以运行时用"不可寻址"标记（Value 背后没有连着可落笔的真实内存）直接禁止 `Set`。三条可写路径：

- `ValueOf(&emp).Elem()`：经指针落到原变量本体；
- `reflect.New(t)` 分配新对象后 `Elem()` 写入；
- 字段 Value 与包含它的 struct Value 都必须可寻址。

`Set` 系列按类型各归各位：`SetString`/`SetInt`/`SetFloat`。

输出第 4 段正是对照：`ValueOf(变量): CanSet=false`，`reflect.New + Elem(): CanSet=true`；`反射写入后: {Name:Grace Age:45 Salary:0}` 且原 emp 未受影响——写入发生在 `New` 造出的新对象上。

### 关键 API 速查

| API | 含义 | 为什么重要 |
|---|---|---|
| `reflect.TypeOf(x)` | 返回 `reflect.Type` | 类型元信息唯一入口 |
| `reflect.ValueOf(x)` | 返回 `reflect.Value` | 值的读写入口（默认只读） |
| `t.NumField()` / `t.Field(i)` | 字段数 / `StructField` | 字段名、类型、tag 在类型侧 |
| `v.Field(i)` / `v.Interface()` | 字段值 / 还原为 `any` | 遍历打印的标准写法 |
| `v.CanSet()` | 是否可写 | false 时 `Set` 直接 panic |
| `reflect.New(t).Elem()` | 分配 + 解引用 | 造出 CanSet=true 的 Value |

## Pitfalls & Q&A

坑清单（每条均可复现）：

- **`ValueOf(变量)` 是副本**。现象：`SetString` 直接 panic。原因：装箱拷贝，`CanSet()=false`。解法：要写原变量必须 `ValueOf(&emp).Elem()`。
- **未导出字段永远不可 Set**。现象：`Set` 在 CanSet 检查之前就被拦下，`Interface()` 对未导出字段也会 panic。原因：未导出（小写开头，包外不可见）字段的访问边界，反射同样遵守。解法：只在定义包内部处理未导出字段，框架层只消费导出字段。
- **拿 Name 判断类型能力**。现象：`type ID int` 被当成陌生类型拒之门外。原因：Name 是人起的名字，能力由 Kind 决定。解法：通用分派一律 `switch v.Kind()`。
- **匿名类型的 `Name()` 返回空串**。现象：按名字做字符串比较时踩空。原因：`map[string]int` 这类匿名类型没有名字。解法：比较用 Kind 与元素类型，不依赖 Name。
- **把反射写进热路径**。现象：循环里的 `v.Field(i)` 明显拖慢程序。原因：装箱、Kind 检查、安全断言每次都要付。解法：能静态写就静态写，反射只放在框架边界。
- **对 nil 接口直接操作**。现象：`NumField()` panic。原因：对 nil 接口调 `TypeOf`/`ValueOf` 得到 nil/零 Value。解法：先判 `v.IsValid()` 再用。

**Q：Name 和 Kind 为什么要分开？**

Name 是人类起的名字，可以有无数个（`type ID int`、`type UserID int`）；Kind 只有 26 种（int、string、struct、slice、map……）。

通用逻辑（比如"遍历 struct 字段"）只能按 Kind 分派——按 Name 比较会把同名不同构、同构不同名的类型全判错。

**Q：`Elem()` 出现在哪些场景，含义一样吗？**

三处：`Value.Elem()` 解引用指针（进可写世界的钥匙）；`Type.Elem()` 取 slice/map/chan 的元素类型；`reflect.New(t)` 里的 t 本身就是 Type，不需要再 Elem。拼写相同，语义按接收者区分。

**Q：反射慢在哪，慢多少？**

慢在三处——值进/出接口的装箱与逃逸（编译器判定变量活不过当前栈帧后，改在堆上分配）、每次操作都要做 Kind 检查与安全断言、无法内联（编译器把函数调用直接展开成函数体以省去调用开销的优化）。常见量级是直接访问的几十到上百倍。

序列化、ORM、依赖注入这类"一次配置、处处通用"的框架值得付这个代价；普通业务代码里把 `v.Field(i)` 放进循环热路径就是误用。

**Q：反射和类型断言（从接口值里取出具体类型的操作）怎么选？**

类型有限、编译期能列全，用类型 switch/断言；类型在编译期不可知（要处理"任意 struct"），才上反射。断言是编译器帮忙的快速路径，反射是彻底交棒给运行时。

运行时对象与接口的底层表示，见 [../../internals/27_reflect](../../internals/27_reflect/)。
