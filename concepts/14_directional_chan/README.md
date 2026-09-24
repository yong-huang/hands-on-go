# 14 · 单向 Channel：把方向写进函数签名

> channel 天生双向，但好的 API 只暴露需要的那一个方向。Go 用类型系统做这件事：`chan<- int` 只能发、`<-chan int` 只能收，约束发生在**编译期**——方向用错直接编译错，零运行时开销。本实验用一个生产者/消费者的最小完整实现，展示双向 chan 传参时的隐式转换单向。

## 1. 为什么需要它

没有方向约束时，任何拿到 channel 的函数都能读也能写——谁负责发送、谁负责关闭，全靠口头约定。把方向写进函数签名后，这些约定变成编译器强制执行的规则：消费者想"顺手发一条"？编译错。生产者忘了 close？类型上它才是唯一有权关闭的人。**约束从文档搬进了类型**，这是 Go 标准库随处可见的设计（如 `time.Tick` 返回 `<-chan time.Time`）。

## 2. 总览：核心机制一图看懂

![单向 Channel：双向通道的两个只读/只写投影](images/directional.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/concepts/14_directional_chan/images/directional.html)
> （或本地打开 [`images/directional.html`](images/directional.html)）。

**心智模型一句话**：main 持有唯一的双向 chan；传给生产者时隐式转成 `chan<-`（只写+close 的特权），传给消费者时转成 `<-chan`（只读）。转换永远单向：单向不能转回双向。

## 3. 快速开始

```bash
cd concepts/14_directional_chan
go run main.go
```

预期输出：

```text
收到: 10
收到: 20
收到: 30
收到: 40
收到: 50
channel 已关闭，消费者退出
```

main.go 的注释里埋了两行"取消注释就编译错"的代码（在只读参数上发送、在只写参数上接收），删掉注释前缀即可亲眼看到编译器的方向报错。

## 4. 核心概念

### 4.1 箭头记法：箭头指向 chan 就是只写

`chan<- int`：箭头朝 chan 里，"数据往里流"——只写；`<-chan int`：箭头朝外，"数据往外流"——只读。记法本身就是语义。

### 4.2 隐式转换与单向性

双向 `chan int` 传给 `chan<- int` 或 `<-chan int` 参数时自动转换，无需任何显式代码；反向转换不存在——单向丢掉了另一侧的能力，类型系统不还给你。

### 4.3 close 是发送方的特权

`close` 只对 `chan<-` 合法。这把"谁负责关闭"编码进了类型：消费者手里根本没有关闭的能力，重复 close 之类的事故在签名层面就被隔离了。

## 5. 关键字段与坑清单

| 类型 | 合法操作 | 编译报错示例 |
|---|---|---|
| `chan int` | 收、发、close | 全能持有者 |
| `chan<- int` | 发、close | `<-ch` → cannot receive from send-only |
| `<-chan int` | 收 | `ch<-` → cannot send to receive-only |

坑清单：

- 单向是"视图"不是新 channel：底层还是同一条 chan，转换不拷贝任何东西
- 方向转换单向：`<-chan` 转回 `chan` 编译错
- `for range ch` 与 `v, ok := <-ch` 都对 `<-chan` 可用——只读不等于只能读一次
- 把双向 chan 存进结构体再以单向暴露（字段私有 + 方法返回 `<-chan`）是标准封装手法

## 6. 文件结构

```text
concepts/14_directional_chan/
├── README.md                          # 本文件：原理 + 运行指引 + 深入要点
└── images/
    ├── directional.architecture.json  # 图源：双向通道与两个单向投影（Typed JSON IR）
    ├── directional.html               # 交互版：自包含单文件
    └── directional.svg                # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：单向 channel 有运行时开销吗？**
A：零。它只是编译期的类型视图，运行时全是同一个 `hchan` 指针。

**Q：为什么 close 只允许发送方？**
A：只有发送方知道"数据是否发完"；若允许接收方关闭，多发送方场景会互相干扰。类型系统把这条纪律固化了。

**Q：双向 chan 传给单向参数是拷贝吗？**
A：是 channel 头的值拷贝（和普通传参一样），指向同一底层数据；"转换"只发生在类型层面。

**Q：什么时候该用单向参数？**
A：只要函数对 channel 的使用是单向的——这几乎是所有情况。生产函数收 `chan<-`，消费函数收 `<-chan`，main 才持有双向。

## 8. 总结

单向 channel 把"谁能发、谁能收"从约定升级为类型约束，编译错误取代了运行时事故。看一遍 main.go 里两行注释掉的编译错代码，方向记法的读写就再也不会混了。

下一篇 [15_select](../15_select/) 看多路复用：一个 select 同时监听多个 channel，超时与非阻塞分支怎么写。
