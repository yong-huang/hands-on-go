# 06 · 数组与 slice：值内联与 24 字节 header 的内存布局对比

> "数组和 slice 到底差在哪"是 Go 初学者最常困惑的问题之一：为什么数组赋值/传参会拷走整份数据，而 slice 却能"引用式"地共享？本实验用 `unsafe.Sizeof` 与 `reflect.SliceHeader` 直接打印两者的运行时内存布局，把答案变成看得见的字节数与地址。理解了这层布局，后面 slice 共享、扩容分离一类的问题都能自己推出来。

## 1. 为什么需要它

写 Go 时两类 bug 高频出现：把大数组传给函数导致意外的整份拷贝；以为切片是独立副本，改一个另一个跟着变。这些行为的根源都在内存表示里——**数组是"值"：N 个元素直接内联在变量里；slice 是"结构体"：变量只有 `{Data, Len, Cap}` 三个字段共 24 字节，数据在它指向的底层数组上**。

程序用 `unsafe` 直接读出真实布局来验证这一点，并对比两者作为函数参数的传递代价，最后演示经典的内存别名问题。数组与 slice 的布局是语言规范层面的稳定结构，所以这种 unsafe 读取用来学习是可靠的。

## 2. 总览：核心机制一图看懂

![数组值内联 vs slice 24B header 指向底层数组，附 a[1:4] 窗口偏移](images/layout.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/06_array_vs_slice/images/layout.html)
> （或本地打开 [`images/layout.html`](images/layout.html)）。

**心智模型一句话**：数组变量就是数据本身，slice 变量只是数据的"取景框"。上排对比两者的变量本体（40B 内联 vs 24B header），下排是切片 `a[1:4]`——两个 header 框住同一块底层数组，data 只相差 8 字节（1 个元素）。

## 3. 快速开始

```bash
cd internals/06_array_vs_slice
go run main.go          # 文本演示：数组内联 → slice header → 传参 → 切片窗口
go run main.go -json    # 输出 JSON 状态快照（机器可读的地址/字节数值）
```

预期输出（节选，地址每次运行不同）：

```text
sizeof(arr) = 40 字节（5 个元素直接放在变量里，没有指针）
&arr[0] = 0x…eb8  value=1
&arr[1] = 0x…ec0  value=2        ← 相邻元素间隔 8 字节
&arr     = 0x…eb8（== &arr[0]，数组名就是首元素地址）
sizeof(sl) = 24 字节（只有 header 的三个字段）
&sl     = 0x…f10   Data    = 0x…ee0   Len=5  Cap=5
函数内 &a = 0x…db8（按值传入 → 拷贝整 40 字节）
函数内 &s = 0x…db8（只拷 24 字节 header），s.Data = 0x…ee0（与外面共享底层数组）
sB = a[1:4]   Data=0x…008  Len=3  Cap=4
偏移 = Data(sB) - Data(sA) = 8 字节（= 1 个元素）
改后: sA = [1 99 3 4 5]  sB = [99 3 4]（内存别名）
```

每一步"在看什么"都写在 `main.go` 的注释里；`-json` 快照给出与文本演示一致的机器可读数值。

## 4. 核心概念

### 4.1 数组是内联的连续"值"

`[5]int` 的 5 个元素直接平铺在变量占用的内存里，`unsafe.Sizeof` 得到 40 字节（5 × 8，无任何指针层）。程序逐个打印 `&arr[0]` 到 `&arr[4]`，地址间距正好 8 字节，并验证 `&arr == &arr[0]`——数组名就是首元素地址，它没有 header，就是一块连续内存本身。

### 4.2 slice 是 { Data, Len, Cap } 结构体

slice 变量本身只有 3 个字段共 24 字节：`Data`（指向底层数组的指针）、`Len`（元素个数）、`Cap`（容量上限）。程序读出字段并验证 `&sl != Data`——header 变量里没有数据，真正的 5 个 int 躺在 `Data` 指向的底层数组上。

### 4.3 传参：全量拷贝 vs 只拷 header

`copyArr` 按值接 `[5]int`，函数内 `&a` 与调用方不同（整份 40 字节的拷贝）；`copySl` 按值接 `[]int`，函数内 `&s` 同样不同，但 `s.Data` 与调用方相同——只拷了 24 字节 header，底层数组共享。所以函数内 `s[i] = x` 对调用方可见，而修改 `arr[i]` 不影响外界。

### 4.4 切片产生新 header，只移动指针

`sA := a[:]` 与 `sB := a[1:4]` 是两个独立的 header，但 `Data(sB) - Data(sA) = 8` 字节——切片只是把 Data 指针挪了一个元素、重算 Len=3/Cap=4，不复制数据。随后 `sB[0] = 99`，`sA` 变成 `[1 99 3 4 5]`（即 `a[1]` 被改）：两个取景框框住同一块内存，这就是内存别名。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `unsafe.Sizeof(arr)` | 数组变量占的字节数 = N×元素大小 | 证明元素内联、无指针层（40 = 5×8） |
| `SliceHeader.Data` | 底层数组起始地址 | `&sl != Data` 直接证明"header 里没有数据" |
| `SliceHeader.Len/Cap` | 元素数 / 容量上限 | `a[1:4]` 后 len=3、cap=4（cap 从偏移处数到数组尾） |
| `&arr` vs `&arr[0]` | 数组名即首元素地址 | 传参时整份拷贝的根源 |
| `Data(sB)-Data(sA)` | 切片偏移（本例 8B） | 偏移 ≠ 拷贝：窗口移动，数据不动 |

坑清单：

- **把子切片当独立副本**：`sB := a[1:4]` 后 `sB[0] = 99` 连 `a` 一起变；要真拷贝用 `append([]int(nil), s...)` 或 `make + copy`
- **函数内改 slice 元素"意外"影响调用方**：传 slice 共享底层数组，`s[i] = x` 对外生效，和数组按值传递的直觉完全相反
- **误以为 slice 传参总是引用语义**：header 本身是按值拷贝的，函数内 append 超过 cap 换新数组后不会反映回调用方；推荐函数显式返回新 slice
- **`reflect.SliceHeader` 只是学习手段**：Data 指针不能脱离 GC 长期持有，生产代码避免这种 unsafe 依赖

## 6. 文件结构

```text
internals/06_array_vs_slice/
├── README.md                          # 本文件：原理 + 运行指引 + 面试要点
├── main.go                            # 主教材：数组内联 → slice header → 传参 → 切片窗口（-json 输出快照）
└── images/
    ├── layout.architecture.json       # 图源：值内联 vs 24B header + 切片窗口（Typed JSON IR）
    ├── layout.html                    # 交互版：可缩放/高亮连线的自包含页面
    └── layout.svg                     # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：`[5]int` 和 `[]int` 作为函数参数有什么本质区别？**
A：数组按值传递，拷贝整个 40 字节，函数内改动不影响外界；slice 传的是 24 字节 header 的值拷贝，底层数组共享，函数内按索引改元素对外可见（但 append 换底层数组后不回传）。

**Q：为什么 `unsafe.Sizeof([5]int)` 是 40 而 slice 是 24？**
A：数组变量的内存就是 5 个 int 的连续排列（5×8）；slice 变量只有 `{Data uintptr, Len int, Cap int}` 三个机器字，共 24 字节，数据在 Data 指向的底层数组上。

**Q：`sB := a[1:4]` 之后 sB 的 len 和 cap 各是多少？为什么？**
A：len=3（4-1），cap=4（cap(a)-1=5-1）。新 header 的 data 只前进 1×8=8 字节，窗口从 a[1] 框到数组末尾，所以 cap 是"从偏移处数到尾"。

**Q：怎么证明切片没有复制数据？**
A：打印 `Data(sB) - Data(sA)`，本实验实测正好 8 字节（1 个元素）；再改 `sB[0]`，`sA[1]` 同步变成 99——同一块内存的两个窗口。

**Q：数组名是指针吗？**
A：不是。`&arr == &arr[0]` 只是数值上等于首元素地址；数组是纯值类型，赋值/传参发生整份拷贝，这与 C 里"数组名退化为指针"完全不同。

## 8. 总结

数组与 slice 的一切行为差异都能从一句话推出：**数组变量是数据本身，slice 变量是 24 字节的取景框**。拷贝代价、共享别名、切片偏移，全是这句话的推论。记住验证方法——`go run main.go` 把字节数与地址打印出来，比背结论可靠得多。

下一篇 [07_goroutine](../07_goroutine/) 从数据结构转向并发：goroutine 的栈从 2KB 起步如何按需增长，以及为什么它比线程轻量得多。
