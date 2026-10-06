# 12 · 中间件链与 context 贯穿：洋葱、断连感知与 panic 兜底

> 中间件是包在 handler 外、对所有路由统一生效的函数链，执行顺序形如洋葱：请求穿入五层、响应按镜像穿出。本篇实测三件事：五层进入/退出严格镜像、客户端断开 TCP 后服务端 handler 在 52ms 内感知退出（而非白等满 10 秒）、handler panic 被转成 500 而进程照常服务。

## Background

二十个路由都要日志、超时、鉴权、panic 恢复——在每个 handler 里复制粘贴这四段，改一处就要改二十处。中间件把"所有请求都要过的公共逻辑"抽成链，路由只写业务。

但中间件引入了两个新问题。其一，多层包裹后执行顺序是否可控、可预测——嵌套写反一层，鉴权就跑在日志后面。其二，handler 在途时客户端断开了连接：没有取消机制的 handler 会把 10 秒的计算白算完，结果丢进一个已消失的连接。

两个问题的标准答案都在 `context` 里：中间件沿 `http.Handler` 的签名天然组成调用栈，`r.Context()` 则由 `http.Server` 在客户端断开时自动取消——handler 只需 select 它。本篇把这两条跑成可断言的行为。

## What

中间件链是一种装饰器模式的应用：`Chain(A, B, C).Then(handler)` 生成的新 handler，请求依次穿过 A→B→C→handler，响应按 C→B→A 穿出，进出严格镜像。

一句话心智模型：**洋葱模型就是函数调用栈的可视化——A 调 B、B 调 C、C 调 handler，调用栈天然保证"进去的顺序倒过来出来"，defer 语句就是这个镜像的实现机制**。

但和普通函数调用不同的是，这条栈里流动的 `r.Context()` 携带着取消信号：客户端断开会从最外层"灌入"取消，任何一层 select 它都能提前退场——控制流不止从上往下，还能从旁边掐断。

本篇的五层与各自职责：

| 层 | 职责 | 顺序 |
|---|---|---|
| A-auth | 鉴权 | 最外层（最先看到请求） |
| B-log | 日志 | 记录进入与退出 |
| C-recover | panic→500 | defer recover 兜底 |
| D-timeout | 超时控制 | context 截止 |
| E-biz | 业务 | 最内层（紧贴 handler） |

## When to Use

中间件适合"与业务无关、所有请求公用"的横切逻辑。

- **接入手感类需求**：日志、指标打点、trace 传播——新增一层，不改任何 handler
- **安全与稳定兜底**：鉴权、限流、panic 恢复——必须先于业务执行，链的位置就是保证
- **请求级资源管理**：超时 context、取消传播——把项目的并发纪律（concurrency/18）接到 HTTP 层

何时不用：只对一个路由生效的逻辑（直接写在 handler 里）；需要改变响应体内容的场景（中间件拿不到 handler 内部的业务结果，硬拆会有耦合）。

| 写法 | 顺序可控 | 复用性 | 适用 |
|---|---|---|---|
| handler 内复制 | ✅ | ❌ 复制粘贴 | 原型 |
| 中间件链（本篇） | ✅ 声明序即执行序 | ✅ 一处实现全路由 | 生产 |
| 全局 wrapper 函数 | ❌ 手工嵌套易错 | ✅ | 过渡形态 |

## Quick Start

前置条件：Go 1.21+，127.0.0.1:19595/19596。运行：

```bash
cd net/12_middleware_ctx
go run .
go test -race -count=3 .
```

真实输出：

```text
== 第 1 步：五层洋葱——进入序与退出序镜像 ==
  A-auth.in → B-log.in → C-recover.in → D-timeout.in → E-biz.in → handler: health → E-biz.out → D-timeout.out → C-recover.out → B-log.out → A-auth.out
  ⇒ 进入序与退出序镜像（对称=true）——洋葱模型的名字就是这么来的。

== 第 2 步：客户端断连感知——handler 不白算 10 秒 ==
  handler 在 52ms 后提前退出（验收 ≤100ms，而非等满 10s）

== 第 3 步：panic 兜底——炸的是请求，不是进程 ==
  /panic → 500（被 recover 中间件转成 500）
  下一个 /health → 200（进程存活，服务如常）
  ⇒ recover 拦在中间件层：单请求的爆炸不外溢。
```

测试验收（`-race` 下连跑 3 轮全绿）：

```bash
go test -race -count=3 .
```

诚实预期：断连感知的退出时刻在 50~60ms 之间（客户端断开发生在 50ms，服务端感知是即时的，浮动来自调度）；五层镜像的精确序列是断言而非演示。

## How It Works

### Chain 的组装顺序：从后往前包

```go
for i := len(mws) - 1; i >= 0; i-- {
    next = mw(next)
}
```

组装从**最后一个**中间件开始向前包：E 先包住 handler，D 再包住 E……最先声明的 A 最终位于最外层。倒序遍历正是"声明序即执行序"的实现——想调整层级只需调整 Chain 的参数顺序。`Then(handler)` 触发真实的包裹，返回组装后的最终 handler。

### 断连感知：r.Context() 是服务端与客户端的合同

实测的关键一步是模拟"客户端消失"：用裸 TCP 发出请求（不读响应），50ms 后直接关闭连接。

服务端内核层感知到连接消失，`http.Server` 立即取消该请求的 `r.Context()`——handler 里 select `ctx.Done()` 的分支在 52ms 触发，提前退场。实测数字 52ms 里的 50ms 是客户端故意的等待，服务端感知本身是即时的。

没有这个机制，handler 会把 10 秒的计算白算完，再往一条已消失的连接里写结果。

### recover 层的位置：兜底必须在业务之外

`defer recover()` 只能捕获**当前 goroutine** 的 panic，而每个请求一个 goroutine——recover 必须位于请求 goroutine 之内，这正是中间件层（而非全局某个监控 goroutine）做 recover 的原因。

本篇实测：panic 后请求得到 500，进程存活，后续 20 次并发 panic 轰炸后 /health 依然 200。recover 层的位置在链中间（C 层）也说明另一件事：它外层（A/B）的 defer 照常执行——日志不会因为内层 panic 而丢失。

## Pitfalls & Q&A

- **Chain 组装顺序写成正序遍历**：现象是最先声明的中间件最后执行。原因：正序包裹会把第一个声明的包在最内层。解法：倒序遍历（本篇实现），或约定"后声明先执行"并写进文档
- **recover 放在独立 goroutine**：现象是 handler panic 依然打崩进程。原因：recover 只对当前 goroutine 生效，跨 goroutine 的 panic 无法捕获。解法：recover 必须与 panic 同 goroutine——即中间件层的请求 goroutine
- **断连实验用 http.Client 测**：现象是服务端 ctx 永不取消。原因：http.Client 的连接由 transport 持有，请求在途时"关掉另一个裸连接"不影响它。解法：用裸 TCP 发请求再关闭那条连接本身（本项目实录，`net.Dial` + `Close` 的组合）
- **中间件里忽略 r.Context()**：现象是链路有超时中间件，handler 还是算满 10 秒。原因：超时中间件只负责"到点取消 context"，handler 不 select 它就等于没超时。解法：所有长计算周期性 select ctx.Done（concurrency/18 的纪律平移到 HTTP 层）

**Q：中间件的执行顺序为什么重要到需要断言？**
A：安全语义依赖顺序：recover 必须在业务外层兜底、鉴权必须在业务之前、日志想记录完整进出就必须在最外。顺序错一层的后果（如日志丢失 panic 记录）在平时不可见，故障时才爆发——所以镜像断言是中间件链的基本测试。

**Q：超时中间件和项目 9 的 ReadTimeout 有什么区别？**
A：管的对象不同。ReadTimeout 管内核读连接的字节（客户端发得慢）；超时中间件管 handler 的处理时间（业务算得慢）。两者都要：前者防慢攻击（项目 9），后者防慢业务。超时中间件的实现就是 `context.WithTimeout(r.Context(), d)` 再替换请求上下文。

**Q：断连后 handler 里已经写出的数据怎么办？**
A：本篇 /slow 没写任何数据就退出了；若 handler 已 `w.Write` 部分数据再感知断连，已写出的字节对端已不可达，继续写会得到错误。取消语义是"尽早止损"，不是"可回滚"——长任务要产出一部分可用的结果，应在设计上分期提交。

**Q：这和项目 21 迷你 RPC 的关系？**
A：本篇的中间件链（日志/recover/超时）与断连感知，就是项目 21 RPC 框架服务器侧需要的全部横切能力——HTTP 中间件的组合模式会原样迁移到自研协议的 handler 链上。
