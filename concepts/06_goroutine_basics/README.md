# 06 · Goroutine 基础：go 只管启动，WaitGroup 负责等待

> `go f()` 五个字母就能并发一个函数调用，但它只是"启动"，不是"等待"——main 一退出，所有还没跑完的 goroutine 直接被带走，一行输出都可能来不及打印。`sync.WaitGroup` 是标准库里"等一批 goroutine 干完活"的计数器。本实验用 10 个 goroutine 各打印编号的 demo，把 Add/Done/Wait 的配对规则跑给你看。

## 1. 为什么需要它

`go worker(i)` 执行完立即返回，main 继续往下走；如果 main 是 main 函数的最后一行逻辑，函数一返回整个进程退出，还没被调度的 goroutine 全部消失。初学者最常见的现象就是"goroutine 怎么一条都没打印"——不是没启动，是没人等它。WaitGroup 用一个原子计数器解决这个问题：启动前 Add，完成时 Done，main 在 Wait 上阻塞到计数归零。

## 2. 总览：核心机制一图看懂

![main → 10 个 goroutine 启动 → Add/Done/Wait 时序](images/wait.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/06_goroutine_basics/images/wait.html)
> （或本地打开 [`images/wait.html`](images/wait.html)）。

**心智模型一句话**：WaitGroup 是 main 与 10 个 goroutine 之间的计数器——main 先加满（Add）、再放人（go）、然后在 Wait 上等；每个 goroutine 完成时减一（Done），减到零，main 解除阻塞。注意 10 个 goroutine 的完成顺序没有任何保证。

## 3. 快速开始

```bash
cd concepts/06_goroutine_basics
go run main.go
```

输出节选（完成顺序**每次运行都可能不同**，以下仅为一次真实运行的快照）：

```text
========== 1. 循环外一次性 Add(10) ==========
  goroutine 10 完成
  goroutine 6 完成
  goroutine 7 完成
  goroutine 8 完成
  goroutine 9 完成
  goroutine 2 完成
  ...（略）
main: 10 个 goroutine 全部完成（完成顺序每次运行可能不同）

========== 2. 循环内逐个 Add(1) ==========
  goroutine 10 完成
  goroutine 5 完成
  ...（略）
main: 第二批 10 个也全部完成

========== 总结 ==========
1. go f() 只是启动并发，main 不等它；不等待 = goroutine 随 main 一起消失
2. WaitGroup 三件套配对：Add(n) 在 go 之前，Done 用 defer，Wait 阻塞到归零
3. Add(10) 一次到位 vs 循环内 Add(1)，两种姿势都对，关键是先 Add 后 go
4. goroutine 的调度顺序没有保证，完成顺序每次运行都可能不同
```

## 4. 核心概念

### 4.1 go 关键字：启动 ≠ 等待

`go worker(i, &wg)` 在当前 goroutine 里对参数求值并拷贝，然后把它交给运行时调度，语句本身立即返回。它不产生返回值、不阻塞、不报错——goroutine 没跑完 main 就退出，它们就静默消失。

### 4.2 WaitGroup 的计数器模型

`Add(n)` 把计数加 n，`Done()` 等价于 `Add(-1)`，`Wait()` 阻塞到计数归零。三条纪律：**Add 必须发生在对应 go 语句之前**（否则 Wait 可能在 Add 之前就看到 0 而提前放行）；**Done 用 defer**（保证哪怕函数中途 return 也计数必达）；**计数不能为负**（多 Done 一次直接 panic：`sync: negative WaitGroup counter`）。

### 4.3 两种 Add 姿势

启动数量已知时，循环外一次性 `wg.Add(10)`；边生产边启动（比如从 channel 收任务）时，循环内每个 go 之前 `wg.Add(1)`。两种都对，共同点是"先 Add 后 go"。

### 4.4 编号经参数传递：避开闭包陷阱

`go worker(i, &wg)` 里 `i` 在 go 语句执行时就被求值拷贝。若改成在函数体里直接引用外层循环变量 `i`，所有 goroutine 可能共享同一个变量、全部打出同一个值——这是经典闭包陷阱，`07_closure_trap` 会专门拆解它的成因与三种修法。

## 5. 关键字段与坑清单

| 语法 | 含义 | 为什么重要 |
|---|---|---|
| `go worker(i, &wg)` | 启动并发，立即返回 | 参数当场拷贝；不等待完成 |
| `wg.Add(10)` | 计数 +10 | 必须在 go 语句之前 |
| `defer wg.Done()` | 计数 -1 | defer 保证必达，等于 `Add(-1)` |
| `wg.Wait()` | 阻塞到计数归零 | main 不 Wait = goroutine 随退出消失 |
| `&wg` 传指针 | WaitGroup 不要拷贝 | 拷贝后 Done 减的是副本，Wait 永远等不到 |

坑清单（每条都可在 demo 里复现）：

- 删掉 `wg.Wait()`：程序大概率一行 "goroutine x 完成" 都不打印就退出——go 只管启动
- `Add` 放到 goroutine 内部第一行：Wait 可能提前放行，时序竞态
- `Done` 次数多于 `Add`：panic `sync: negative WaitGroup counter`
- WaitGroup 按值传给 goroutine：计数减在副本上，main 的 Wait 死等
- 以为输出顺序 = 启动顺序：调度顺序没有保证，完成顺序每次运行都可能不同

## 6. 文件结构

```text
concepts/06_goroutine_basics/
├── README.md                  # 本文件：原理 + 运行指引 + 深入要点
├── main.go                    # demo：两批各 10 个 goroutine，两种 Add 姿势
└── images/
    ├── wait.sequence.json     # 图源：main → Add → go ×10 → Done ×10 → Wait 放行（Typed JSON IR）
    ├── wait.html              # 交互版：自包含单文件，可缩放/切换主题
    └── wait.svg               # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 Add 必须在 go 语句之前？**
A：Wait 只看计数器当前值。如果 Add 在 goroutine 内部才执行，main 可能在计数还是 0 时就通过了 Wait——"先 Add 后 go"保证 Wait 开始前计数已就位。循环内 Add(1) 同理：Add 与 go 紧挨着，且 Add 在前。

**Q：WaitGroup 和 channel 等待怎么选？**
A：只关心"全部完成"用 WaitGroup 最直白；如果要在 goroutine 之间传结果数据，channel 一并承担同步职责（`for range` 到关闭为止也是等待）。两者不冲突，`11_worker_pool`、`18_producer_consumer` 里是组合用法。

**Q：Done 能不用 defer 吗？**
A：能，但不该。函数路径一多（提前 return、panic），手动 Done 极易漏调，结果就是 Wait 死等。defer 把"必达"交给语言机制，代价几乎为零。

**Q：WaitGroup 能复用吗？**
A：能，但有纪律：一轮 Wait 返回之后才能开始下一轮 Add。Wait 还没返回就 Add 会与减计数竞争，典型替代是每轮任务用一个新实例。

## 8. 总结

go 关键字把"启动并发"降到五个字母，WaitGroup 把"等待完成"收敛成一个计数器配对：Add 在 go 前、Done 用 defer、Wait 等归零。把 §3 的输出多跑几次，亲眼确认完成顺序每次不同，"调度无保证"就不用背了。

下一篇 [07_closure_trap](../07_closure_trap/) 看闭包陷阱：为什么在 goroutine 里直接引用循环变量会全部打出同一个值，参数拷贝、循环内重赋值与 shadow 三种修法各适合什么场景。底层 WaitGroup 原理深入见 [../../internals/10_waitgroup/](../../internals/10_waitgroup/)。
