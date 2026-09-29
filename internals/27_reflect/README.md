# 27 · reflect 底层：Type 与 Value 的一次动态调用闭环

> 序列化库为什么能"一份代码吃所有类型"？答案是反射（reflect，程序在运行时检视与操作自身类型和值的标准库能力）。读完本篇，你能用一份 `User` 结构体跑通 TypeOf → New/Set → Call → 手写 miniJSON 对拍的完整闭环，并说清它的两堵墙：只读的 Value 写不进数据、未导出字段完全不可见。

## Background

`encoding/json` 能把任意 struct 变成 JSON，你却写不出一个"接受任意类型"的函数——若不用反射，通用逻辑只能靠 type switch 穷举已知类型，或让用户手写序列化钩子，两者都覆盖不了用户自定义类型。

更常见的困境是"只背过结论"：知道反射慢，却没见过它慢在哪、边界在哪。于是有人到处回避反射把代码写复杂，有人在热路径上随手 `reflect` 出一个数量级的开销。

本实验回答三个问题：**反射能读到什么**（字段/tag/偏移）、**能改什么**（只有 Value 内部记录了内存地址——可寻址——时才能写）、**代价是什么**（每步边界检查 + 装箱拆包）。最后手写 10 行 mini JSON 序列化器与 `encoding/json` 对拍，输出逐字节一致——原理不神秘。

## What

**定义**：反射是一套运行时类型检视与操作机制——`reflect.Type` 是类型元数据（编译器生成、描述类型布局的只读信息），`reflect.Value` 是运行时值容器，二者配合可在不知道具体类型的情况下读字段、改数据、调方法。

**心智模型**：可以把 `reflect.Type` 想象成产品说明书（只读，写满字段名、偏移、tag），把 `reflect.Value` 想象成产品本体。

但和真实说明书不同的是：`ValueOf` 拿到的本体默认锁在展柜里（只读快照，`CanSet=false`），只有顺着 `New→Elem` 指针链才拿到可触碰的实体。

**机制一句话**：Type 线只读，Value 线分两层（`ValueOf` 只读、`New→Elem` 才可写），两线在 miniJSON 处汇合——Type 给字段名和 tag，Value 给字段值；未导出字段是两条线共同的盲区。

## When to Use

### 典型场景

- 写通用库：序列化、ORM（对象与数据库表行互转的库）、配置绑定——需要读字段名和 tag（结构体字段后的元数据字符串，如 `json:"name"`）来驱动行为。
- 运行时动态构造与调用：按名字查方法并调用、根据配置动态建对象，类型在编译期写不进代码。
- 测试与调试工具：深度比较两个任意结构体、自动生成随机测试数据。

### 何时不用

- 类型在编译期已知：直接写代码，编译器还能做检查与优化。
- 性能敏感路径：反射调用约慢一个数量级，优先缓存反射结果或改用代码生成。

### 同类方案对比

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| type switch 穷举 | 编译期写死类型清单，快，但覆盖不了用户新类型 | 类型集合已知且稳定 |
| 反射 | 运行时动态处理任意类型，每步带边界检查，慢一个量级 | 必须操作"编译期未知"的类型 |
| 代码生成 | 编译期展开，零运行时开销，多一步构建流程 | 类型可枚举且性能敏感 |

## Quick Start

前置条件：go run main.go 仅用标准库；viz.py 画图需另装 Python 3 与 matplotlib。运行：

```bash
cd internals/27_reflect
go run main.go          # 文本演示：TypeOf → New+Set → Call → 对拍
go run main.go -json    # 输出 JSON 快照（字段表/调用结果/两份 JSON）
python3 viz.py          # 重新生成实测数据图（字段表 + 对拍结果）
```

预期输出（节选）：

```text
User, Kind=struct, 5 字段
  Name     string   tag=json:"name"            off=0
  Age      int      tag=json:"age"             off=16
  secret   string   tag=                       off=56
  未导出字段 secret：CanInterface=false，反射看不到
== MethodByName.Call =====
  Dr. Ada
  mini: {"name":"Ada","age":36,"admin":true,"tags":["go","sys"]}
  std : {"name":"Ada","age":36,"admin":true,"tags":["go","sys"]}
  一致：true
```

诚实预期：`off=56` 的 `secret` 只显示了一行"反射看不到"——未导出字段的值读不出来；`mini:` 与 `std:` 两行逐字节一致，对应输出里的 `一致：true`。`Kind` 是类型的底层种类（struct/int/slice 这一类）。

## How It Works

### Type：编译期布局的运行时投影

`TypeOf(u)` 返回的 `Name=User, Kind=struct, 5 字段` 全部来自编译器生成的类型描述符——反射只是"读说明书"。你在输出里看到的字段表，就来自这张描述符。

`StructField.Offset` 原样暴露内存布局：Name=0 / Age=16 / Admin=24 / Tags=32 / secret=56（string 16 字节、bool 占 1 再被下一个 8 对齐，与 [17_align](../17_align/) 互为印证）。

偏移即字段起始地址相对结构体开头的字节数。

### Value 的两层：只读与可寻址

`ValueOf(u)` 拿到的是"只读快照"，`CanSet=false`，写不进任何字段。

想写必须走指针链：`reflect.New(T)` 得到指针 Value，`.Elem()` 解引用后才**可寻址**（Value 内部记录了内存地址），`SetString/SetInt/Set` 才有意义。

### 动态调用与方法表

`MethodByName("FullName").Call([]Value{"Dr. "})` 返回 `Dr. Ada`。

运行时按名字查方法表 → 构造调用帧（一次函数调用在栈上的参数与返回值区域）→ 装箱参数、拆包返回值（把具体值包进 `interface{}` 再取出），内含 recover 转 panic，语义与直接调用等价。

"慢一个数量级"就来自这里：一次反射调用 ≈ 一整个调用帧的构造加拆包；而直接调用的参数传递只是一条寄存器赋值。

### miniJSON 对拍：原理不神秘

10 行手写的 `miniJSON` 按同样的三板斧工作：`NumField` 遍历 → `Tag.Lookup("json")` 取键名 → 按 `Kind` 分派写出。与 `encoding/json` 输出逐字节一致（输出里的 `一致：true`）。

注意 `secret` 被跳过——写通用序列化时先判 `CanInterface()` 是必须步骤，否则遇到未导出字段直接 panic。

### 关键字段与方法速查

| 字段/方法 | 含义 | 为什么重要 |
|---|---|---|
| `t.Field(i).Offset` | 字段在 struct 内的字节偏移 | 编译期定死，反射白送的内存布局信息 |
| `v.CanSet()` | 是否可写 | 只有 `New→Elem` 链条上的 Value 才为 true |
| `v.CanInterface()` | 能否安全取回 interface | 未导出字段的门禁，强取会 panic |
| `Tag.Lookup("json")` | 安全读 tag | `Get` 遇空 tag 无法区分"没写"和"写空" |

## Pitfalls & Q&A

**坑 1：对未导出字段直接 `.Interface()`。** 现象：程序 panic。原因：`CanInterface=false`，语言级门禁拦截强取。解法：遍历字段先判 `CanInterface()`，miniJSON 里第一个踩中的就是它。

**坑 2：逐元素写 Slice 字段。** 现象：`Index().Set` 报错不可写入。原因：非可寻址容器拿不到元素地址。解法：整块 `Set(reflect.ValueOf(...))` 替换整个切片。

**坑 3：用 `Get` 读 tag。** 现象：字段没写 tag 时无法与"写了个空 tag"区分。原因：`Get` 对缺失键返回空串，语义二义。解法：用 `Tag.Lookup`（返回是否存在标志），并自己拆 `,omitempty`（`strings.Split(tag, ",")[0]`）。

**坑 4：批量数据用 `Call` 逐元素调。** 现象：处理大切片时耗时爆炸。原因：每次 `Call` 都要构造调用帧、装箱拆包，代价按次数线性累加。解法：构一个切片一次反射，或干脆用代码生成。

**Q：反射能访问未导出字段吗？**
读结构信息（名字/类型/偏移）可以，但 `Interface()`/`Set` 都被 `CanInterface`/`CanSet` 双重拦截——语言级围墙，没有合法后门。

`ValueOf` 与 `New+Elem` 的可写性差异，见 How It Works 的"Value 的两层"一节。

**Q：miniJSON 与 encoding/json 的差距说明什么？**
说明 json 包没有魔法——就是"反射读 tag + 按 Kind 写输出"；差别在健壮性（嵌套/循环指针/自定义 `Marshaler`——用户实现自己 JSON 输出逻辑的接口）与性能优化。

**Q：怎么降低反射的代价？**
缓存 `Type`/字段信息复用、批量一次反射、或用代码生成在编译期展开（做不到运行时动态时优先选后者）。反射调用的开销构成已在 How It Works 的"动态调用与方法表"一节拆解，不再重复。
