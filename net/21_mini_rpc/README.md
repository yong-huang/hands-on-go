# 21 · 🏁 迷你 RPC 框架：协议、安全与元数据的合流

> net 系列收官：mTLS 加密的迷你 RPC 框架——1000 请求经双向证书验证到双后端，成功率 ≥99.5%，goleak 门禁绿，`-race` 零报告。帧协议复用项目 8 的多路复用、mTLS 复用项目 16 的双向认证、泄漏门禁复用项目 20 的 goleak——前 20 篇的能力在这一个框架里合流。

## Background

前 20 篇分别验证了网络编程的各个子问题：TCP 状态机（01）、帧协议（05/06）、多路复用（08）、超时/取消（12/18）、mTLS（16）、泄漏防护（20）。但"各部分正确"不等于"组合正确"——集成才是检验学习的终极考场。

一个可用的 RPC 框架需要同时满足四个约束：每条消息经 mTLS 加密且双方身份可验（安全）；请求带唯一 id 经单连接多路复用（效率）；每个请求有超时且失败有结局（可靠）；框架退出后无 goroutine 泄漏（卫生）。四条约束同时成立才是"可用"——本篇把它们合流并给出可复现的验收。

## What

迷你 RPC 框架是一个 mTLS 加密的请求-响应系统：客户端通过轮询负载均衡向多个后端发出带 reqID 的帧，服务端处理后按 reqID 回写响应，全部流量经双向证书验证。

一句话心智模型：**框架是一台"加密挂号信分拣机"——每封信（请求）有唯一编号（reqID），经加密管道（mTLS）投递到多工位（后端池），收信回执按编号配对；每封信要么有回执（Succeeded）要么有退条（Failed），一个不丢**。

帧格式（头 13 字节 + 载荷）：`[kind 1B][reqID 8B][len 4B][payload]`，reqID 扩展为 64 位以支撑高吞吐不复用。

## When to Use

自建 RPC 框架适合"通用 RPC 不够用、定制需求真实存在"的时刻。

- **需要按业务定制协议语义**：帧级路由、按调用方鉴权、自定义负载均衡——通用 gRPC 的扩展点不够灵活
- **性能敏感的内部链路**：省去 gRPC 的拦截器栈与反射开销，帧头 13 字节直透
- **学习验证**：把项目 8+16+18 的知识组合成一个可压测的系统——这是本篇的核心价值

何时不用：面向公网或跨团队的服务——gRPC 的生态、跨语言代码生成与治理工具不可替代；对延迟不敏感的管理接口——HTTP 更通用。

| 方案 | mTLS | 流式 | 定制点 | 适用 |
|---|---|---|---|---|
| gRPC | ✅ 内建 | 四种流 | 拦截器 | 通用微服务 |
| 本篇框架 | ✅ 手工 mTLS | reqID 多路复用 | 帧协议任意定制 | 学习/特殊路由 |
| HTTP+JSON | 可选 | 无 | HTTP 语义 | 简单集成 |

## Quick Start

前置条件：Go 1.21+、`go.uber.org/goleak`（自动拉取）。运行：

```bash
cd net/21_mini_rpc
go run .
go test -race -count=3 .           # 快速回归（-short 100 请求）
go test -race -count=1 .           # 完整版（1000 请求）
```

测试验收实录（`-race` 下连跑 3 轮全绿）：

```text
--- PASS: TestMTLSRequired
--- PASS: TestShortSuccessRate
--- PASS: TestSuccessRateAndNoLeak
ok  	gonet/21_mini_rpc
```

诚实预期：完整版（1000 请求经 mTLS）在 -race 下约 5~8 秒；成功率因并发调度偶有 99.5%~100% 波动；goleak 门禁在每次测试出口自动检查。

## How It Works

### 三层架构：协议、传输、业务

帧协议（protocol.go）定义 13 字节帧头与编解码；传输层（framework.go）实现 mTLS 服务端/客户端与请求配对；业务层只需实现 `Handler` 回调。

层次间通过 `Handler` 函数与 `pending` channel 解耦——替换任何一层不影响其他两层。这套分层是从项目 5（帧）、08（复用）、12（中间件）的架构决策中提炼的。

### 守恒律在框架层的最终形态

`Submitted == Succeeded + Failed`：框架层的 Call 方法保证每个请求要么拿到响应（Succeeded）要么拿到错误（Failed），不存在"发出去了但不知道结果"的第三态。

超时、连接断开、panic 全部归入 Failed。这条不变式让上层永远能做出确定性决策——重试、告警或忽略，但绝不会"丢了一条不知道"。

### mTLS 的框架级集成

`NewServer` 与 `NewClient` 接受 `*tls.Config`，mTLS 配置（项目 16 的 `RequireAndVerifyClientCert`）在框架创建时一次性注入，后续所有连接自动双向验证。客户端无证书时连接在握手期被服务端拒绝，Call 报错——安全不是开关而是框架属性。

## Pitfalls & Q&A

- **多后端共享全局计数器**：现象是并发测试计数对不上。原因：多个 goroutine 无锁写同一个 map。解法：每连接独立 Client 实例（本篇），或用 atomic（本篇 Metrics）
- **框架 goroutine 泄漏**：现象是 goleak 报 goroutine 栈残留。原因：读循环阻塞在已死的连接上。解法：Close 时关闭全部连接（Server 的 `conns` 表 + Client 的 `conn.Close()`），读循环因 Read 错误自然退出
- **测试串行化导致假绿**：现象是单测通过但并发场景失败。原因：竞态只在并发时触发。解法：测试用 `sync.WaitGroup` 真并发 + `-race` + goleak 三重门禁

**Q：为什么 reqID 从 int32 扩展为 uint64？**
A：项目 8 的 int32 在 1000 请求/秒的下耗尽需要 25 天；int64 耗尽需要 29 万年。扩展后不复用序号，消除了"旧响应匹配到新请求"的 ABA 问题——这是高吞吐 RPC 的必要条件。

**Q：和 gRPC 的差距在哪？**
A：gRPC 有四层本篇没有：HTTP/2 传输层（连接复用与流控的成熟实现）、protobuf 序列化（跨语言 schema）、丰富的拦截器生态、以及经过大规模验证的可靠性。本篇的价值是把 gRPC 的黑盒拆成 500 行可读代码——理解了这 500 行，gRPC 的每个配置项都不再是魔法。

**Q：23 篇系列学完了能做什么？**
A：把这张能力清单当检查表用：设计协议时对照帧格式纪律（项目 2/5/18）；写并发代码时对照 happens-before 与泄漏防护（concurrency 01-20）；搭服务时对照超时/安全/诊断（net 09-17）；评估现成 RPC 框架时知道它在每一层做了什么取舍（net 08/19/21）。

**Q：系列 23 篇的复用关系？**

| 本篇能力 | 来源项目（所在系列） |
|---|---|
| 帧协议 + 请求 id 多路复用 | [08_mux_pipeline](../08_mux_pipeline/) |
| mTLS 双向认证 | [16_mtls](../16_mtls/) |
| goroutine 泄漏门禁 | goleak gate（concurrency/20） |
| 原子计数与内存序 | atomic orders（concurrency/02） |
| 超时与取消传播 | mini context（concurrency/18） |
| handler panic 隔离 | [12_middleware_ctx](../12_middleware_ctx/) |
| TLS 证书链 | [15_ca_chain](../15_ca_chain/) |
| 并发压测方法论 | load shapes（patterns/22） |
