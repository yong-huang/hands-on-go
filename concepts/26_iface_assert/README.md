# 26 · 接口组合与类型断言：小接口拼大接口，运行时验明正身

> Go 的接口是隐式满足的，但真正的威力在两处：**接口嵌入**——把 Reader、Writer 这样的小接口拼成 ReadWriter 大接口（标准库 io 包同款手法）；**类型断言**——从接口变量里取出具体类型的值。断言有单值与双值两种形式，前者失败会 panic，后者只给一个 ok；多种类型分派则交给类型 switch。本实验把这套规则全部跑给你看。

## 1. 为什么需要它

只用"大而全"的接口定义会丢掉 Go 接口设计的精髓：**小接口才能被广泛实现**。`io.Reader` 只有一个方法，所以无数类型都能当 Reader 用；而需要"既能读又能写"的函数，用接口嵌入把两个小接口拼成 `io.ReadWriter`，而不是重新定义一遍方法。另一边，拿到一个 `interface{}`（或宽接口）之后怎么取回具体类型？这正是类型断言与类型 switch 的领地——用错形式（该双值时用单值）就是一次线上 panic。本实验用 `Memory` 类型把"实现方法集 → 自动满足三个接口 → 运行时断言分派"的完整链路跑出来。

## 2. 总览：核心机制一图看懂

![接口组合与类型断言：Reader+Writer 拼 ReadWriter，运行时断言分派](images/assert.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/26_iface_assert/images/assert.html)
> （或本地打开 [`images/assert.html`](images/assert.html)）。

**心智模型一句话**：接口嵌入发生在编译期（小接口拼大接口、方法集即满足），类型断言发生在运行时（问接口变量里装的具体类型是谁）——上行是组合，下行走完"实现 → 双值断言 → 单值断言 → 类型 switch"的验证链。

## 3. 快速开始

```bash
cd concepts/26_iface_assert
go run main.go
```

预期输出（节选）：

```text
========== 1. 接口组合：Reader + Writer → ReadWriter ==========
rw.Read() = world | 剩余一读: hello
========== 2. 双值断言：v, ok := x.(T) ==========
x.(string) 成功: gopher （长度 6 ）
x.(int) 失败但安全: ok = false ，n = 零值 0
========== 3. 单值断言：v := x.(T)，失败会 panic ==========
尝试 x.(int)，x = 42
断言成功: v = 42
尝试 x.(int)，x = text
recover 捕获断言 panic: interface conversion: interface {} is string, not int
========== 4. 类型 switch：一次分派多种类型 ==========
类型 switch → string: "go" (len=2)
类型 switch → int: 100 (翻倍=200)
类型 switch → bool: true (取反=false)
类型 switch → 其他: float64 = 3.14
```

## 4. 核心概念

### 4.1 接口嵌入：小接口组合成大接口

`ReadWriter` 的方法集就是把 `Reader` 和 `Writer` 的方法"抄"进来：

```go
type ReadWriter interface {
    Reader
    Writer
}
```

这叫**接口嵌入**，等价于 `io` 包里的 `io.ReadWriter`。嵌入可以多层，`io.ReadWriteCloser` 就是三个小接口的拼装。大接口的值可以隐式赋给小接口变量（`var r Reader = rw` 合法），反过来不行——编译器只看方法集是不是超集。

### 4.2 方法集即满足

`*Memory` 实现了 `Read` 和 `Write`，于是它同时是 `Reader`、`Writer`、`ReadWriter` 的实现者——没有 `implements` 关键字，编译器在赋值那一刻自动检查。代价是报错晦涩：漏实现一个方法时，错误只在赋值处出现（"does not implement ..."），不在类型定义处。

### 4.3 双值断言 vs 单值断言

```go
s, ok := x.(string) // 双值：失败 ok=false，s 是 string 零值 ""，程序继续
v := x.(int)        // 单值：失败直接 panic: interface conversion
```

双值形式是防御性代码的标配；单值形式只在"逻辑上必定成功"时使用（例如前一步刚用类型 switch 确认过类型）。本实验用 `defer/recover` 演示了单值断言失败的 panic 长什么样——recover 能救场，但那是事故处理，不是控制流。

### 4.4 类型 switch：多类型分派的正解

```go
switch v := x.(type) {
case string:  // 这里 v 已是 string
case int:     // 这里 v 已是 int
default:      // v 保持接口类型本身
}
```

一个 `switch` 覆盖所有分支，比一串 `if s, ok := x.(T); ok` 更清晰，且变量 `v` 在每个分支里自动具有对应的具体类型——可以直接调用该类型的方法，无需再断言。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `type RW interface { Reader; Writer }` | 接口嵌入 | io.ReadWriter 的拼装方式 |
| `var rw ReadWriter = &Memory{}` | 方法集自动满足 | 无 implements，赋值即检查 |
| `s, ok := x.(T)` | 双值断言 | 失败安全：ok=false、零值 |
| `v := x.(T)` | 单值断言 | 失败 panic，仅限"必定成功"场景 |
| `switch v := x.(type)` | 类型 switch | 每个分支内 v 具有具体类型 |

坑清单：

- 单值断言失败不是返回零值，是 panic：`interface conversion: interface {} is string, not int`（§3 输出可见原文）
- 接口变量的**零值是 nil**：对 nil 接口做任何断言都 panic，先 `if x == nil` 再断言
- 值接收者与指针接收者的方法集不同：`Memory`（值）只有值接收者方法，含 `Write` 的接口只能由 `*Memory` 满足——所以本实验 `var rw ReadWriter = &Memory{}`
- 类型 switch 的 `case` 里不能写多类型合并后共用具体类型变量（`case int, int64:` 时 v 保持接口类型）
- 大接口转小接口不需要断言（方法集是超集即可隐式赋值）；小接口转大接口才需要断言

## 6. 文件结构

```text
concepts/26_iface_assert/
├── README.md                      # 本文件：原理 + 运行指引 + 深入要点
├── main.go                        # demo：接口组合 / 双值断言 / 单值断言+recover / 类型 switch
└── images/
    ├── assert.architecture.json   # 图源：Reader+Writer 组合与断言验证链（Typed JSON IR）
    ├── assert.html                # 交互版：自包含单文件，可缩放/切换主题
    └── assert.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么标准库全是 `io.Reader` 这种单方法小接口，而不是一个大 IO 接口？**
A：小接口的方法集要求低，能被最多的类型实现——文件、网络连接、bytes.Buffer、甚至 `strings.Reader` 都是 `io.Reader`。消费方按需组合（ReadWriter、ReadWriteCloser），接口越组合越精确，实现方却只需实现最小集。

**Q：双值断言失败时 ok=false，第一个变量是什么？**
A：T 的零值（§3 输出里 `n = 0`）。所以双值形式永不 panic，但必须检查 ok 再用值——只拿值不看 ok 等于把单值断言的 panic 换成了静默的逻辑错误。

**Q：类型 switch 和一串 if-ok 断言，除了好看还有什么区别？**
A：语义上等价，但类型 switch 对同一接口值只做一次类型判定分派，且分支内变量自动带具体类型；编译器还能检查 case 类型是否合法（重复 case 报错）。运行时两者都是查接口的 itab/类型字，性能无实质差异。

**Q：接口的动态类型与动态值是什么？断言到底在比什么？**
A：接口变量内部是 (动态类型, 动态值) 二元组。断言 `x.(T)` 比的是**动态类型**是否与 T 相同（或 T 是接口时动态类型是否实现 T）。这解释了两个经典坑：动态类型相同但值不同时断言照样成功；nil 指针装入接口后接口不等于 nil（typed-nil）。运行时表示的深入原理见 [internals](../../internals/01_slices/) 相关篇目。

## 8. 总结

编译期用接口嵌入把小接口拼成恰好的大接口，运行期用双值断言/类型 switch 安全地取回具体类型——前者决定了"你能被多少人实现"，后者决定了"你拿到宽接口后如何不 panic 地用"。把 §3 的输出对照 §5 的坑清单逐行跑一遍，这套规则就内化了。

下一篇 [27_reflect_basics](../27_reflect_basics/) 看反射基础：`reflect.TypeOf`/`ValueOf` 双入口与字段遍历，把"运行时验明正身"推进到"运行时解剖结构"。
