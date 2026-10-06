# 19 · gRPC 四种流式：Unary、ServerStreaming、ClientStreaming、Bidi

> gRPC 把 RPC 归纳为四种形态，本篇在同一个服务上各实现一个并全部实测：Unary 回显、ServerStreaming 倒数推流、ClientStreaming 攒批求和、Bidi 双向 1000 条守恒。

> 慢消费实验给出现实存在感——服务端推 10 万条、消费端限速，`Send` 的阻塞由 HTTP/2 流控承接，零丢失零乱序。拦截器演示错误码的短路与改写。

## Background

项目 8 手写的多路复用（seq + pending 表）解决的是"一条连接并发多个请求"；gRPC 在 HTTP/2 的流之上把这套机制标准化，并且把 RPC 的"形状"归纳成四种：一问一答、服务端连发、客户端连发、双向独立收发。选对形状是 gRPC 接口设计的第一个决策——形状错了，后续全在补丁。

四种形状与底层机制的映射：Unary 最接近传统 RPC；ServerStreaming 是服务端推送（订阅、下载）；ClientStreaming 是客户端攒批（上传、打点）；Bidi 是实时对话（聊天、指标流）。

它们共享同一套 HTTP/2 连接——一个 gRPC 连接上可以同时跑任意形态、任意数量的调用，互不阻塞（HTTP/2 流级隔离，项目 11）。

验证形状正确性的统一标准是**守恒**：发多少、收多少、一条不丢一条不重。Bidi 1000 条的守恒断言是本篇的核心测试。

## What

gRPC 流式 RPC 是在 HTTP/2 流上按序传输 protobuf 消息的四类调用形态：Unary（1→1）、ServerStreaming（1→N）、ClientStreaming（N→1）、Bidi（N→N），N 侧各自独立收发。

一句话心智模型：**gRPC 连接是一条公路，四种 RPC 是四种行车方式——Unary 是点对点快递，ServerStreaming 是班车沿线放客，ClientStreaming 是货车的沿途收货，Bidi 是双向车道的会车**。

但和公路不同的是，所有车共享一条 HTTP/2 连接的带宽：某一路的慢会经流控压低自己的发送速度，不堵其他路——这就是慢消费实验要实测的"背压的现实存在感"。

四种形态的本篇实现与守恒实测：

| 形态 | 方法 | 语义 | 本篇实测 |
|---|---|---|---|
| Unary | Echo | 请求→大写回显 | 即时返回 |
| ServerStreaming | Countdown | 从 n 倒数到 1 | 100 条全部递减到达 |
| ClientStreaming | Sum | 收齐求和 | 500 条和正确 |
| BidiStreaming | Chat | 收 n 发 2n | 1000 条守恒（乱序容忍） |

## When to Use

四种形态对应四类真实场景，选型即接口设计。

- **Unary**：常规 CRUD、配置读取——请求响应一一对应时永远首选
- **ServerStreaming**：订阅行情/日志 tail/大结果集分页下发——服务端有"一串数据"要给一个客户端
- **ClientStreaming**：批量打点、文件分块上传、指标聚合——客户端有"一串数据"要给服务端
- **Bidi**：聊天、协同编辑、实时行情双向订阅——两端都产生独立的数据流

何时不用：公网面向用户走 REST/GraphQL（生态与缓存语义）；一问一答且无强类型需求时 JSON over HTTP 更通用；防火墙不允许 h2 明文（h2c）时需要加 TLS（项目 16 的 mTLS 可叠加）。

| 形态 | 方向 | 守恒断言方式 |
|---|---|---|
| Unary | 1→1 | 响应字段相等 |
| ServerStreaming | 1→N | 条数 + 顺序（倒数递减） |
| ClientStreaming | N→1 | 聚合值（求和） |
| Bidi | N→N | 序号集合守恒（乱序容忍） |

## Quick Start

前置条件：Go 1.21+、protoc 29.3 与 protoc-gen-go(-grpc)（本机已装，生成命令见 streamy.proto 头注释）。运行：

```bash
cd net/19_grpc_streams
go run .
go test -race -count=3 .
```

真实输出（节选）：

```text
== 第 1 步：Unary 与 ServerStreaming ==
  Echo → 1 "echo:hi"
  Countdown → 3 "t-minus"
  Countdown → 2 "t-minus"
  Countdown → 1 "t-minus"

== 第 2 步：ClientStreaming（Sum）与 Bidi（Chat，10 条）==
  Sum(1..5) → 15
  Chat → 2 (n×2)
  Chat → 4 (n×2)
  Chat → 6 (n×2)
  Chat → 8 (n×2)

== 第 3 步：慢消费背压——服务端推 10 万条，消费端限速 ==
  10 万条推流全部收完（600ms）——Send 的阻塞由 HTTP/2 流控承接，无丢失无乱序

== 第 4 步：拦截器短路（deny 开头的 note → PermissionDenied）==
  denied 请求 → PermissionDenied
  正常请求   → OK
```

测试验收（`-race` 下连跑 3 轮全绿）：

```bash
go test -race -count=3 .
```

诚实预期：背压实验耗时取决于消费速率（本篇 1000 条/5ms ≈ 600ms~1s）；Chat 的到达顺序与发送顺序在本篇实现下一致，但协议不保证跨流顺序——守恒断言按序号集合而非顺序。

## How It Works

### Bidi 1000 条守恒：发送、接收、配对三段独立

测试把 Bidi 拆成三段：发送方 goroutine 连发 1000 条后 `CloseSend`；接收方循环 `Recv` 直到 EOF，按 `N/2` 反解原始序号记进集合。

收发用 WaitGroup 汇合后断言"集合大小 = 1000 且序号 1..1000 全部在列"。乱序不影响断言（集合不保序），丢一条立即暴露——这就是流式 RPC 的守恒断言模板。

### 慢消费背压：Send 阻塞的现实存在感

服务端 `Send` 并不是无限快的写入：HTTP/2 为每条流维护发送窗口，接收方不读，窗口耗尽后 `Send` 就阻塞——这是 gRPC 背压的实现机制。

实验给消费端注入限速（每 2000 条睡 1ms），实测 10 万条全部送达：慢消费不丢数据，代价是推送侧整体变慢。推论：ServerStreaming 的消费端必须持续 Recv（或取消），"收着不读"就是自己给自己上背压。

### 拦截器：短路与改写的分界

一元拦截器包住 handler：前置校验失败直接 `return nil, status.Error(PermissionDenied, ...)` 是**短路**——handler 不会执行（测试用闭包标志断言）。

handler 返回错误后包装 `status.Errorf(Internal, ...)` 是**改写**——把内部错误对外统一脱敏。两段逻辑在同一个拦截器里以"是否调用 handler"分界，顺序敏感。

## Pitfalls & Q&A

- **消费端不读也不取消**：现象是服务端 Send 卡住、goroutine 滞留。原因：流控窗口耗尽后 Send 阻塞，无人 Recv 就永远等。
  解法：消费端要么持续 Recv 要么 cancel context（Send 会因 ctx 取消而失败返回）
- **Bidi 里先 CloseSend 再继续 Recv**：现象是误以为 CloseSend 会终止整个调用。原因：CloseSend 只关发送方向，接收方向继续有效。解法：发送完显式 CloseSend + 循环 Recv 到 EOF，才是完整收尾
- **拦截器只注册了一元版**：现象是流式方法的拦截逻辑不生效。原因：gRPC 区分 UnaryInterceptor 与 StreamInterceptor 两条链。解法：流式需求用 `grpc.ChainStreamInterceptor`，拦截器签名换成 `func(srv, ss, info, handler)`
- **守恒断言按到达顺序**：现象是偶发失败。原因：bidi 的响应完成顺序不受协议保证。解法：按序号集合断言（本篇 TestBidiThousandConserved 的实现）

**Q：四种形态怎么选？**
A：两个问题定位：数据是一串还是一条？两端都产数据吗？

一串且服务端产 → ServerStreaming；一串且客户端产 → ClientStreaming；都串 → Bidi；都是单条 → Unary。形状定了以后接口就稳了——半途改形状是破坏性变更。

**Q：Send 之后要不要立即 CloseSend？**
A：只有 ClientStreaming/Bidi 在"不再发送"时才调 CloseSend（让对端 Recv 看到 EOF 结束收流）。ServerStreaming 的服务端靠 return 结束流；Unary 没有流。

乱调 CloseSend 是 gRPC 新手的第二大翻车点（第一大是忘记 defer cancel，concurrency/18 的纪律）。

**Q：拦截器和项目 12 的 HTTP 中间件什么关系？**
A：同一模式的两套实现：执行顺序（洋葱/链）、短路（直接返回错误码）、改写（包装错误）三概念一一对应。差异在签名——HTTP 中间件包 handler 函数，gRPC 拦截器按 Unary/Stream 分两条注册链。

**Q：这和项目 21 迷你 RPC 的关系？**
A：项目 8+18+19 三个实验拼起来就是 gRPC 的最小成分：多路复用（8）+ protobuf 线格式（18）+ 流式语义与拦截器（19）。收官项目 21 把它们与 mTLS（16）合流为一个可压测的完整框架。
