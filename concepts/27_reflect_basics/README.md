# 27 · 反射基础：运行时打开类型的盒子

> 编译期，struct 有哪些字段是写死的知识；但写序列化、ORM、配置加载这类通用框架时，代码要面对"任意类型"。反射（`reflect`）把类型检查从编译期搬到运行时：程序可以在跑起来之后问"这个 struct 有哪些字段、每个字段是什么类型、这个值能不能改"。本实验用一份可运行的 demo 把双入口、Kind 语义、字段遍历与可写边界逐条跑给你看。

## 1. 为什么需要它

没有反射，想打印任意 struct 的每个字段就得为每个类型手写一份代码。`encoding/json` 能用一个 `json.Marshal` 序列化任何 struct，靠的正是反射：运行时读类型信息、遍历字段、按 tag 决定键名。理解反射的四个基本功——`TypeOf`/`ValueOf` 双入口、Name 与 Kind 的区别、`NumField`/`Field(i)` 遍历、`CanSet` 可写边界——是用好这些框架、也是看懂第 28/29/30 篇（tag 解析、动态调用、自定义序列化）的前提。

## 2. 总览：核心机制一图看懂

![反射基础：TypeOf/ValueOf 双入口，一个只读，一个可写](images/reflect.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/27_reflect_basics/images/reflect.html)
> （或本地打开 [`images/reflect.html`](images/reflect.html)）。

**心智模型一句话**：`TypeOf` 与 `ValueOf` 是两个并列入口，一个给类型信息、一个给值信息；汇聚到字段遍历（Name/Type/Value/Kind），只有来自 `reflect.New + Elem()` 的可寻址 Value 才能走到写入这一步。

## 3. 快速开始

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

## 4. 核心概念

### 4.1 双入口：TypeOf 看类型，ValueOf 看值

`reflect.TypeOf(x)` 返回 `reflect.Type`（接口）：类型名、Kind、字段列表、方法集都在这。`reflect.ValueOf(x)` 返回 `reflect.Value`（结构体）：包着值本身，能读能（条件允许时）写。两者的 `Kind()` 一致，看类型用前者、操作值用后者。

### 4.2 Name 与 Kind：名字与种类

`Name()` 是声明时的类型名，匿名类型（如 `map[string]int`）名字为空；`Kind()` 是底层种类——`type ID int` 的 Name 是 `ID`，Kind 仍是 `int`。写通用代码时判断"能做什么"要用 Kind（`switch v.Kind()`），不能拿 Name 比较。

### 4.3 字段遍历：NumField + Field(i)

对 Kind 为 `struct` 的 Value：`NumField()` 给字段数，`Field(i)` 按声明顺序取出每个字段；字段名和 tag 则在 `Type.Field(i)` 的 `StructField` 上。demo 里逐列打印的 Name/Type/Value/Kind 正是"类型侧取名、值侧取值"的拼装。

### 4.4 可寻址性：CanSet 决定能否写

`ValueOf(emp)` 拿到的是 `emp` 的**副本**（参数按值传入接口），不可寻址，`CanSet()` 为 false，强写会 panic。三条可写路径：`ValueOf(&emp).Elem()` 取原变量本体；或 `reflect.New(t)` 分配新对象后 `Elem()` 写入；字段 Value 与包含它的 struct Value 都要可寻址。`Set` 系列按类型各归各位：`SetString`/`SetInt`/`SetFloat`。

## 5. 关键字段与坑清单

| API | 含义 | 为什么重要 |
|---|---|---|
| `reflect.TypeOf(x)` | 返回 `reflect.Type` | 类型元信息唯一入口 |
| `reflect.ValueOf(x)` | 返回 `reflect.Value` | 值的读写入口（默认只读） |
| `t.NumField()` / `t.Field(i)` | 字段数 / `StructField` | 字段名、类型、tag 在类型侧 |
| `v.Field(i)` / `v.Interface()` | 字段值 / 还原为 `any` | 遍历打印的标准姿势 |
| `v.CanSet()` | 是否可写 | false 时 `Set` 直接 panic |
| `reflect.New(t).Elem()` | 分配 + 解引用 | 造出 CanSet=true 的 Value |

坑清单：

- `ValueOf(变量)` 是副本：`CanSet()=false`，`SetString` 会 panic——要写原变量必须 `ValueOf(&emp).Elem()`
- 未导出字段永远不可 Set（即使 CanSet 检查通过前就会拦下），`Interface()` 对未导出字段也会 panic
- 判断类型能力用 `Kind` 不用 `Name`：`type ID int` 的 Name 是 `ID`，Kind 才是 `int`
- 匿名类型的 `Name()` 返回空串，做字符串比较时容易踩空
- 反射有运行时开销（类型断言、内存装箱、逐层检查），热路径能静态写就别反射
- 对 nil 接口调 `TypeOf`/`ValueOf` 得到 nil/零 Value，直接 `NumField()` 会 panic，先判 `IsValid()`

## 6. 文件结构

```text
concepts/27_reflect_basics/
├── README.md                       # 本文件：原理 + 运行指引 + 深入要点
├── main.go                         # 演示程序：双入口/Kind/字段遍历/CanSet 四段
└── images/
    ├── reflect.architecture.json   # 图源：TypeOf/ValueOf 双入口 → 遍历 → 写入（Typed JSON IR）
    ├── reflect.html                # 交互版：自包含单文件，可缩放/切换主题
    └── reflect.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 `ValueOf(emp)` 不可 Set，副本从哪来的？**
A：参数以 `any`（接口）传入，接口装箱会把值拷贝一份存进接口数据指针。反射拿到的是副本，改它没有意义，所以运行时用"不可寻址"标记直接禁止 `Set`。传 `&emp` 再 `Elem()` 才落到本体上。

**Q：Name 和 Kind 为什么要分开？**
A：Name 是人类起的名字，可以有无数个（`type ID int`、`type UserID int`）；Kind 只有 26 种（int、string、struct、slice、map……）。通用逻辑（比如"遍历 struct 字段"）只能按 Kind 分派，按 Name 比较会把同名不同构或同构不同名的类型全判错。

**Q：`Elem()` 出现在哪些场景，含义一样吗？**
A：三处：`Value.Elem()` 解引用指针（进可写世界的钥匙）；`Type.Elem()` 取 slice/map/chan 的元素类型；`reflect.New(t)` 里的 t 本身就是 Type，不需要再 Elem。拼写相同、语义按接收者区分。

**Q：反射慢在哪，慢多少？**
A：慢在三处——值进/出接口的装箱与逃逸、每次操作都要做 Kind 检查与安全断言、无法内联。常见量级是直接访问的几十到上百倍。序列化、ORM、依赖注入这类"一次配置、处处通用"的框架值得付这个代价；普通业务代码里 `v.Field(i)` 循环热路径就是误用。

**Q：反射和类型断言（26 篇）怎么选？**
A：类型有限、编译期能列全，用类型 switch/断言；类型在编译期不可知（要处理"任意 struct"），才上反射。断言是编译器帮忙的快速路径，反射是彻底交棒给运行时。

## 8. 总结

反射 = 运行时的类型自省：`TypeOf`/`ValueOf` 双入口拿到类型与值，`NumField + Field(i)` 遍历字段，`CanSet` 划定只读与可写的边界；代价是运行时开销，留给真正需要"处理任意类型"的框架。跑完 §3，再看图上"只读汇聚到遍历、可写来自 New+Elem"这条主路径，四件事就串起来了。

下一篇 [28_struct_tags](../28_struct_tags/) 把本篇的字段遍历用起来：读 struct tag 做 json 键名映射与校验——`encoding/json` 的最小复刻。运行时对象与接口的底层表示，见 [../../internals/27_reflect](../../internals/27_reflect/)。
