# 04 · String 底层实现：只读两字段头与共享拷贝

> string 最"熟悉"，但三个问题常被想当然：它底层长什么样？`s[a:b]` 真的不拷贝吗？为
> 什么和 `[]byte` 互转总会分配内存？string 头是官方承诺的稳定布局
> （`reflect.StringHeader`），本实验因此能用 `unsafe` 读出真实地址，把三件事变成
> 看得见的数字。

## 1. 为什么需要它

`len(s)` 对中文给出的数字比字符数大；按字节切片切出乱码；循环里反复 `string(b)`/
`[]byte(s)` 转换后性能骤降——根源都在 string 的内存表示里：**string 变量只是一个
{str, len} 的 16 字节头**，数据在别处、且只读。与 map/channel 的"实现细节"不同，
string 头是官方承诺的稳定结构，这正是本实验能用 `unsafe` 直接读头的前提。

## 2. 总览：核心机制一图看懂

![string 两字段头：字面量在只读数据区，运行时构造在堆/栈](images/header.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/04_string/images/header.html)
> （或本地打开 [`images/header.html`](images/header.html)）。

**心智模型一句话**：string = {str 指针, len 字节数}，数据在别处且只读；同为 "hello"，
字面量的数据在 rodata、运行时构造的在堆/栈——内容相同也各存一份，地址每次运行都不同，
但"不同区域"这件事永远成立。

## 3. 快速开始

```bash
cd internals/04_string
go run main.go          # 文本演示：头 → 切片共享 → 双向转换 → 不可变
go run main.go -json    # 输出 JSON 地址快照（供程序化读取）
```

预期输出（节选，地址每次运行不同）：

```text
  字面量     "hello"              data=0x10273b8b7 len=5
  运行时构造  "hello"              data=0x161455510048 len=5
  s   = "gopher"  data=0x10273b9e3 len=6
  sub = "ophe"    data=0x10273b9e4 len=4
  偏移：sub.data - s.data = 1 字节（不拷贝，只移动指针）
  两者不同 → 发生拷贝（true）
```

每一步"在看什么"都写在 `main.go` 注释里；`-json` 快照给出一致的地址与 copied 字段。

## 4. 核心概念

### 4.1 两字段头与数据所在区域

```go
type stringHeader struct {
    Data uintptr // 指向只读字节数组
    Len  int     // 字节数（不是字符数）
}
```

比 slice 少一个 cap。字面量被编译器放进只读数据区（rodata）；运行时用
`string([]byte{'h','e','l','l','o'})` 拼出的 string，数据落在堆/栈——上图并排展示
这两个头与各自的数据块：结构相同，内存区域不同。

### 4.2 字符串切片共享底层字节数组

![字符串切片 s[1:5] 只移指针，共享只读字节数组](images/sharing.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/04_string/images/sharing.html)
> （或本地打开 [`images/sharing.html`](images/sharing.html)）。

`s := "gopher"; sub := s[1:5]` 不复制字节：新头只是 `str + 1`、`len = 4`。实测
`sub.data - s.data = 1` 字节。没有 cap 意味着 slice 那种"容量内 append 写穿共享数组"
的隐患在 string 上不存在；共享的代价是谁都不能原地修改。

### 4.3 string 与 []byte 双向转换都拷贝

![string 与 []byte 双向转换各拷贝一次，改一方另一方不变](images/convert.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/04_string/images/convert.html)
> （或本地打开 [`images/convert.html`](images/convert.html)）。

string 只读、[]byte 可写，语义冲突不能共用内存，两个方向各分配、各拷贝一次：实测
`0x…8b7 → 0x…068` 与 `0x…078 → 0x…07a`；改可写方后只读方打印不变——两份数据。

### 4.4 不可变由编译期强制

`lit[0] = 'a'` 直接编译失败（`cannot assign to lit[0]`）：只读是语法层拦截的保证，
任何"可变"诉求都只能先拷贝成 `[]byte`。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `str uintptr` | 只读字节数组起始地址 | 切片 = 指针加偏移，零拷贝 |
| `len int` | 字节数 | 中文一个字符 3 字节；字节边界切片可能切碎字符 |
| `s[a:b]` | 新头：str+偏移、len 重算 | 零拷贝共享只读数据 |
| `[]byte(s)` / `string(b)` | 各拷贝一次 | 每次转换一次内存分配，O(n) |

坑清单：

- **len 是字节数不是字符数**：按字节边界切片可能把多字节字符拦腰切断，遍历请用
  `for _, r := range s`（按 rune）
- **高频 string↔[]byte 转换是分配大户**：拼接场景改用 `strings.Builder`，一次构造
  一次拷贝
- **不要对 map/channel 照搬 unsafe 读法**：hmap/hchan 是内部结构且随版本变动，读内部
  状态要走 reflect 等官方 API
- **地址每次运行都不同**：结论看"是否相等/偏移多少"，别背具体地址

## 6. 文件结构

```text
internals/04_string/
├── README.md                        # 本文件：原理 + 运行指引 + 深入要点
├── main.go                          # 主教材：unsafe 读头 → 共享 → 双向拷贝（-json 输出快照）
└── images/
    ├── header.architecture.json     # 图源：两字段头与数据所在区域（Typed JSON IR）
    ├── header.html                  # 交互版：可缩放/高亮连线的自包含页面
    ├── header.svg                   # README 内嵌双主题矢量图
    ├── sharing.architecture.json    # 图源：切片共享只读数组
    ├── sharing.html                 # 交互版
    ├── sharing.svg                  # README 内嵌双主题矢量图
    ├── convert.workflow.json        # 图源：string↔[]byte 双向拷贝
    ├── convert.html                 # 交互版
    └── convert.svg                  # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：string 的底层结构是什么？和 slice 差在哪？**
A：{str 指针, len 字节数} 两字段、16 字节，比 slice 少 cap；布局是官方承诺的稳定结构。

**Q：s[a:b] 会拷贝数据吗？**
A：不会。只生成新头：str 加偏移、len 重算；多个 string 共享同一块只读字节数组。

**Q：为什么 string 和 []byte 互转必然分配内存？**
A：语义上不能共用内存，两个方向各拷贝一次、O(n)；高频转换改用 strings.Builder。

**Q：string 的不可变靠什么保证？**
A：编译器在语法层拦截赋值（cannot assign to lit[0]）；运行时只能换数据或先转 []byte。

**Q：怎么安全地读 string 内部？map 也可以吗？**
A：string/slice 头是官方承诺布局，unsafe 读取仅适合学习；map 的 hmap 非稳定契约
（Go 1.24 起已换 Swiss Table），硬读只能得到垃圾。

## 8. 总结

string 的全部行为都能从"16 字节只读头 + 底层字节数组"推出来：切片是偏移所以零拷贝，
转换是语义冲突所以必分配，不可变是编译期保证。验证方法——`go run main.go` 打印每次的
地址与偏移，比背结论可靠得多。

下一篇 [05_interface](../05_interface/) 看接口的两字布局：eface 与 iface 如何用
{类型, 数据} 两个指针字支撑起类型断言，以及最经典的 nil 接口陷阱。
