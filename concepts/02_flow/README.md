# 02 · 流程控制：一条 for 走天下，if/switch 自带初始化语句

> C 语言给了你 `for`/`while`/`do-while` 三种循环，Go 只留了一种——但这一种能写出全部形态。作为交换，`if` 和 `switch` 获得了别家没有的能力：自带一条初始化语句，把变量作用域压到最小。本实验用一个经典 FizzBuzz 把 for 三形态、无表达式 switch、带初始化的 if/switch 全部跑一遍，每条规则都有一行可验证的输出。

## 1. 为什么需要它

从其他语言转来的人常在两处不适应：找不到 `while` 关键字（其实是 `for n <= 15 {}`）；`i % 15 == 0` 的判断放在 `i % 3 == 0` 之后，FizzBuzz 永远出不来。这些困惑的根源是**循环只有一种但形态有三样**与**switch 分支自上而下短路**两条规则的边界。本实验把边界跑出来：FizzBuzz 的判定树就是无表达式 switch 的活教材，§3 的每行输出都能在 §5 的坑清单里找到对应规则。

## 2. 总览：核心机制一图看懂

![FizzBuzz 判定流：for 循环 × 无表达式 switch](images/flow.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/02_flow/images/flow.html)
> （或本地打开 [`images/flow.html`](images/flow.html)）。

**心智模型一句话**：for 负责把 1..15 逐个送进判定，switch 负责自上而下取第一个为 true 的 case——`i%15` 必须排在最前，因为 15 的倍数也满足 `i%3==0`，命中即返回、不会落到后面的分支。

## 3. 快速开始

```bash
cd concepts/02_flow
go run main.go
```

预期输出（节选，共 26 行）：

```text
========== 1. FizzBuzz 1..15：无表达式 switch ==========
1 2 Fizz 4 Buzz Fizz 7 8 Fizz Buzz 11 Fizz 13 14 FizzBuzz

========== 2. for 的三种形态 ==========
while 式循环数出 Fizz 的个数 = 5
for range 找到含 Buzz 的位置：
  results[4]=Buzz
  results[9]=Buzz
  results[14]=FizzBuzz

========== 3. if 带初始化语句 vs 普通判断 ==========
i=1: r=1 ≠ 0 → 原样输出 1
i=2: r=2 ≠ 0 → 原样输出 2
i=3: r==0 → Fizz（r 只在这个 if/else 里可见）

========== 4. switch 带初始化语句 + fallthrough ==========
n=15: n%5=0 → Buzz 类
n=9: n%5=4≠0 但 n%3==0 → Fizz 类
n=10: n%5=0 → Buzz 类
n=7: 普通数（m=2 未命中任何分支）
```

## 4. 核心概念

### 4.1 for 的三种形态

Go 只有 `for` 一个循环关键字，省略不同部分就得到三种形态：**三段式** `for i := 1; i <= 15; i++`（init; cond; post）；**while 式** `for n <= 15 {}`（只留条件，分号全省）；**for range** `for i, s := range results`（遍历切片，i 是下标、s 是元素）。省略 cond 的 `for {}` 就是死循环，配合 `break` 使用。

### 4.2 无表达式 switch：if-else 链的整洁写法

`switch { case n%15 == 0: ... }` 不带表达式时，每个 case 是一个布尔表达式，自上而下取第一个为 true 的分支，后面的全部短路。它和 if-else 链语义等价，但把"判定什么"和"命中做什么"排版对齐，FizzBuzz 这种判定树一眼可读。

### 4.3 if / switch 带初始化语句

`if r := i % 3; r == 0` 在条件前先执行一条语句，变量 `r` 只在 if/else 作用域内可见——出了这个 if 就是编译错。`switch m := n % 5; { ... }` 同理。这是 Go 的惯用法：把变量的生命周期压到恰好够用的最小范围。

### 4.4 case 默认不穿透

Go 的 case 命中后自动 break，不会落到下一支；要穿透必须显式写 `fallthrough`（且 fallthrough 会无条件进入紧邻的下一个 case，不看它的条件）。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `for i := 1; i <= 15; i++` | 三段式循环 | Go 唯一的循环关键字的第一形态 |
| `for n <= 15` | while 式 | 前后两段全省，分号也不用写 |
| `for i, s := range xs` | range 遍历 | 下标 + 元素一起拿；只要下标写 `for i := range` |
| `switch { case cond: }` | 无表达式 switch | 自上而下取首个 true，等价 if-else 链 |
| `if r := f(); r == 0` | 带初始化语句 | r 仅 if/else 内可见，作用域最小化 |

坑清单：

- 判定顺序错了 FizzBuzz 就废：`i%15==0` 必须排在 `i%3==0`、`i%5==0` 之前，否则 15 先被 `%3` 命中输出 Fizz
- 无表达式 switch 的 case 是布尔表达式，写 `case n % 3`（不带比较）编译错——它不是按值匹配
- `if r := i % 3; r == 0` 的 `r` 在 if 块外不可见，外部引用直接编译错，不是警告
- case 默认不穿透；`fallthrough` 无条件进下一支，哪怕它的条件为 false
- 未使用的循环变量是编译错（不是警告），演示代码里每个变量都要"被用掉"

## 6. 文件结构

```text
concepts/02_flow/
├── README.md                 # 本文件：原理 + 运行指引 + 深入要点
└── images/
    ├── flow.workflow.json    # 图源：FizzBuzz 判定流（Typed JSON IR）
    ├── flow.html             # 交互版：自包含单文件，可缩放/切换主题
    └── flow.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 Go 砍掉 while 和 do-while 只留 for？**
A：三段式的 init/cond/post 本来就都可以省，`for n <= 15 {}` 与 while 完全等价，`for {}` 就是 while(true)。少一个关键字，少一套语法规则；do-while 的语义用 `for { ... if !cond { break } }` 两行就能表达，不值得为一个低频形态增加记忆负担。

**Q：什么时候用无表达式 switch 而不是 if-else 链？**
A：判定条件超过两个、且都属于同一主题时用 switch：case 对齐排版让"判定树"结构可见，命中即短路也避免 if-else 链里嵌套的 else 心智负担。只有一两个条件、或条件之间没有并列关系时，if 更自然。

**Q：带初始化语句的 if 到底好在哪？**
A：它把"算出一个中间值"和"用这个值判定"绑在同一个语句里，变量的作用域被编译器强制限制在 if/else 内。好处是读到 if 之外时可以确定 `r` 已死，不会在 50 行之后被误用——这是 Go 用作用域规则替代"变量该在哪声明"的口头约定。

**Q：`i%15==0` 换成先判 `%3` 再判 `%15` 能补救吗？**
A：只能在 case 里再嵌套判断，可读性更差。正确做法就是利用 switch 自上而下短路的性质，把最特殊的条件放最前——这也是无表达式 switch 表达"优先级判定"的天然优势。

## 8. 总结

for 把数据逐轮送进来，switch 用短路性质把判定树排成一列，if 的初始化语句把中间值锁在最小作用域——三者组合就能把 FizzBuzz 写成一眼读完的直线代码。把 §3 的输出逐行对照 §5 的坑清单跑一遍，流程控制的这组规则就不用背了。

下一篇 [03_arrays_maps](../03_arrays_maps/) 看数组、切片与 map：定长值类型、动态引用与键值容器，用词频统计把 len/cap 差异跑给你看。
