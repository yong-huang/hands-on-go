# 01 · Slice 底层实现：三字段头、共享与扩容分离

> slice 是"看起来像数组、用起来像动态数组"的核心类型，但 `len`/`cap` 背后的内存行为常被想当然：切片到底复制不复制数据？append 之后两个变量为什么"悄悄分手"？本实验用 `unsafe` 直接读出 slice 的真实内存布局，把这两个陷阱变成看得见的地址与数值。理解了 slice，后面的 map、channel 学习才有抓手。

## 1. 为什么需要它

写 Go 第一年就会踩两个坑：把切片传给函数后在函数内 `append`，外面看不到变化；两个切片共享底层数组，改一个另一个跟着变。这些行为的根源全部在 slice 的内存表示里——**slice 变量只是一个 24 字节的头**（`runtime.slice`：Data/Len/Cap 三字段），数据永远在底层数组里。

这个布局是 Go 官方承诺的稳定结构（不随版本变化），所以可以放心用 `unsafe` 读取真实 slice 的内部状态来学习——这也是它与 map（运行时故意隐藏细节）不同之处。程序演示四件事：slice 头长什么样、append 扩容规则、多 slice 共享底层数组、append 超容量后与旧数组分离。

## 2. 总览：核心机制一图看懂

![多个 slice 共享同一底层数组（内存别名陷阱）](images/sharing.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/01_slices/images/sharing.html)
> （或本地打开 [`images/sharing.html`](images/sharing.html)）。

**心智模型一句话**：slice 变量只是 `{data, len, cap}` 三字段头，`s[a:b]` 不复制数据——data 指针加偏移、len/cap 重新计算，所以天然共享内存。图中三个 header 通过 `+0B / +8B / +16B` 的偏移指向同一个底层数组，重叠窗口里的元素就是共享的。

## 3. 快速开始

```bash
cd internals/01_slices
go run main.go          # 文本演示：结构 → 扩容 → 共享 → 分离
go run main.go -json    # 输出 JSON 状态快照（地址/容量数值）
```

预期输出（节选，地址每次运行不同）：

```text
original                     data=0x...e190  len=5   cap=10
sliceA = original[1:4]       data=0x...e198  len=3   cap=9
地址关系：sliceA 偏移 original 8 字节，sliceB 偏移 16 字节
-- 修改 sliceA[0]（即 original[1]）--
修改后: sliceA[0]=999  sliceB[0]=21  original[1]=999
sliceA 扩容后: len=10 cap=18 data=0x...8000
data 地址: 0x...e198 -> 0x...8000  true
```

每一步"在看什么"都写在 `main.go` 的注释里；`-json` 快照给出与文本演示一致的机器可读数值。

## 4. 核心概念

### 4.1 三字段头 sliceHeader

```go
type sliceHeader struct {
    Data uintptr // 底层数组起始地址
    Len  int     // 有效元素个数
    Cap  int     // 到底层数组末尾的可用容量
}
```

`var s []int` 时三个字段全为 0；`make([]int, 2, 4)` 分配容量 4 的底层数组但 len 只开放前 2 个。slice 本质是一个"数组视图"——它自己不持有数据。

### 4.2 append 的扩容机制（growslice）

`growslice`（runtime/slice.go）分两步：先按规则算目标容量——所需容量超过 2×cap 时直接按需分配；否则 cap < 256 翻倍，cap ≥ 256 按 `(cap+3*256)/4` 递增；再按元素大小向上对齐到内存 size class（mallocgc）。实测对比（go1.26）：cap 512 估算 832、实际 848；cap 2048 估算 2752、实际 3072——差值就是对齐的贡献。

### 4.3 切片共享底层数组

`original`（cap=10）切出 `sliceA=original[1:4]`、`sliceB=original[2:5]`，地址只差 8/16 字节。把 `sliceA[0]`（即 `original[1]`）改为 999 后打印：`original[1]` 一并变为 999，而 `sliceB[0]` 看的是 `original[2]`，仍是 21——**重叠窗口内的元素才共享**。

### 4.4 append 超容量 → 重新分配与分离

![append 超出 cap 重新分配新数组（旧数组与他人共享）](images/realloc.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/01_slices/images/realloc.html)
> （或本地打开 [`images/realloc.html`](images/realloc.html)）。

`sliceA` 扩容前 len=3、cap=9，连续追加 7 个元素触发扩容：分配新数组（cap 9→18）并拷贝旧数据，`oldAddr != 新 data` 判定为 true；而 original 与 sliceB 的地址不变、仍指向旧数组——"切片后 append 会悄悄分离"。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `Data uintptr` | 底层数组起始地址 | 偏移它就实现了"零拷贝切片" |
| `Len` | 有效元素个数 | `len(s)` 是 O(1) 读头，不是遍历 |
| `Cap` | 到数组末尾的容量 | 决定 append 是否触发扩容/分离 |
| `s[lo:hi]` | 视图：data+lo，len=hi-lo，cap 减 lo | 第三个参数 `s[lo:hi:cap]` 可主动限 cap 防共享 |

坑清单：

- 记录扩容前地址不能用在会被覆盖的变量：源码先把 `ha.Data` 拷进局部变量 `oldAddr`，否则 append 后对比不出前后差异
- JSON 快照里 `uintptr` 是十进制，需在 Go 侧用 `fmt.Sprintf("0x%x", ...)` 格式化成十六进制
- 实际 cap 是"规则 + size class 对齐"的结果，只按 growslice 公式估算会偏小——程序把估算与实际并排打印
- 容量内的 append 会写进共享数组的尾部空位：`go run main.go -json` 里 `original` 的第 5 个元素被 sliceA 的 append 覆盖成 100，这是比"分离"更隐蔽的别名写入

## 6. 文件结构

```text
internals/01_slices/
├── README.md                      # 本文件：原理 + 运行指引 + 深入要点
├── main.go                        # 主教材：unsafe 读头 → 扩容 → 共享 → 分离（-json 输出快照）
└── images/
    ├── sharing.architecture.json  # 图源：三视图共享一份数据（Typed JSON IR）
    ├── sharing.html               # 交互版：可缩放/高亮连线的自包含页面
    ├── sharing.svg                # README 内嵌双主题矢量图
    ├── realloc.workflow.json      # 图源：append 扩容与分离流程
    ├── realloc.html               # 交互版
    └── realloc.svg                # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：slice 作为参数传递，函数内 append 为什么有时外面看不到？**
A：传的是 24 字节头的值拷贝。函数内 append 若在原 cap 内，写入的是同一底层数组（len 不变时外面看不见新元素，但按索引能读到）；若触发扩容则换新数组，完全不影响外面。

**Q：`s[lo:hi]` 之后 cap 是多少？为什么这么设计？**
A：`cap(s) - lo`。新视图从 lo 开始、与原数组共用尾部，append 才能原地写入；这正是共享陷阱的来源，需要隔离时用三索引切片 `s[lo:hi:hi]`。

**Q：append 的扩容策略是什么？为什么实际 cap 有时和规则对不上？**
A：所需超过 2×cap 按所需；否则小容量翻倍、大容量约 1.25 倍递增。最终按元素大小向上对齐到 size class，所以 512 估 832 实得 848。

**Q：怎么安全地把切片交给别人修改而不影响自己？**
A：`append(s[:0:0], s...)` 或显式 `make + copy` 强制换底层数组；反之想保持共享就别触发扩容。

**Q：为什么可以用 unsafe 读 slice 内部？map 也可以吗？**
A：sliceHeader 布局是官方承诺的稳定结构，unsafe 读取仅适合学习；map 的 `hmap` 无此承诺且运行时故意不暴露，不应硬编码。

## 8. 总结

slice 的全部行为都能从"24 字节头 + 底层数组"推出来：共享是 data 偏移的自然结果，分离是扩容换数组的必然代价。记住验证方法——`go run main.go` 把每次的地址变化打印出来，比背结论可靠得多。

下一篇 [02_map](../02_map/) 看另一种"头 + 存储桶"的结构：map 的指针为什么被运行时刻意藏起来，以及桶内 tophash 如何加速查找。
