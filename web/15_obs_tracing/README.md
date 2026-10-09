# 15 · OpenTelemetry 链路追踪：父子 span、透传与采样

> marklet 的第三个观测件：OTel 链路追踪。一次请求产生父 span（HTTP）与子 span
> （db.query），同 TraceID、子挂父；外部传入 W3C traceparent 时延续同一 TraceID
> （跨服务接力）；采样率 10% 实测导出 12/100 条——追踪全量太贵，采样是常态。

## Background

日志（实验 13）能回答"这个请求经历了什么"，但只在单服务内成立。请求穿过网关、
API、数据库时，每个服务各打各的日志，ID 虽然透传了，"哪个请求慢、慢在哪一段"
仍要人工拼装。链路追踪把一次请求的每段操作记成一个 span，段与段之间有父子关系
与时间轴，整条链路一棵树——慢在哪、错在哪，树上直接看。

追踪的全部价值依赖两件事：传播协议统一（W3C traceparent 头，跨进程携带
TraceID/SpanID）与采样策略（全量追踪太贵，10% 采样后仍能覆盖故障模式——
出错的请求恰恰是低概率事件，这推高了"尾部采样"的价值）。

## What

OTel（OpenTelemetry，CNCF 的可观测性标准）的核心对象：Tracer（创建 span），
Span（一段有名字、有起止、有属性的 操作），TracerProvider（装配采样与导出），
Propagator（跨进程读写 traceparent）。

可以把 span 想成接力赛的秒表分段：整场比赛一个总表（TraceID），每棒一段
（SpanID），后一棒的开始时间不早于前一棒的交接。交接棒（traceparent 头）
由下一棒的队员从上一棒手里接过——透传就是"接过别人的比赛继续跑"，
采样则是"不是每场比赛都录像"。

| 概念 | 本篇的实例 |
|---|---|
| 根 span | `GET /api/v1/work`（HTTP 中间件创建，无父） |
| 子 span | `db.query`（handler 内创建，父=HTTP span） |
| 传播 | `traceparent: 00-<traceid>-<spanid>-<flags>` |
| 采样 | `ParentBased(TraceIDRatioBased(0.1))` |

## When to Use

- **多服务链路**：网关 → API → DB 的延迟分解，span 树一眼定位最慢一段
- **跨团队对账**：traceparent 透传让 A 团队的请求在 B 团队的 trace 里原样可见
- **性能优化的证据链**：实验 14 的指标发现 p99 恶化，trace 给出具体到查询的归因

何时不用：单体小服务且日志够用（span 的收益在"跨"字上）。

| 观测件 | 粒度 | 成本 |
|---|---|---|
| 日志（13） | 事件点 | 每条一次写 |
| 指标（14） | 全局聚合 | 恒定内存 |
| 追踪（本篇） | 请求内分段 | 每段一次导出，采样控制 |

## Quick Start

前置条件：Go ≥ 1.21；无外部依赖（span 输出 stdout，无需 collector）。运行：

```bash
cd web/15_obs_tracing
go run .
go test -race -count=3 .
```

真实输出（`go run .`，span 为 json 行——节选一条并截断，层级字段可见）：

```text
== 第 1 步：父 HTTP span + 子 DB span，同 TraceID ==
  span 已导出 stdout——层级关系由测试断言（TraceID 相同、子挂父）
== 第 2 步：透传——外部 traceparent 延续同一 TraceID ==
  传入 trace ID 4bf92f35...4736 → 服务端 span 延续它（测试断言）
== 第 3 步：采样 10%——100 次请求，导出 ~10 条 trace ==
  导出数量由内存导出器断言（区间 3~25；概率波动入档）

{"Name":"db.query","SpanContext":{"TraceID":"f50b88c4d65bb669165f323d83b0ae78","SpanID":"82688098ab242ad9",...},"Parent":{"TraceID":"f50b88c4d65bb669165f323d83b0ae78","SpanID":"91b1a512fe236466",...},"Attributes":[{"Key":"db.statement","Value":{"Type":"STRING","Value":"SELECT url, title FROM bookmarks"}}],...}（节选）
```

span 行的三个关键字段：`TraceID` 与父 span 相同（同链路）；`Parent.SpanID`
指向父（层级）；`Attributes` 里的 `db.statement` 让慢查询可定位到语句。

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁；采样实测值随概率波动）：

```text
ok  	marklet/15_obs_tracing	1.600s
```

诚实预期：stdout 的 span json 行非常长（含 Resource 等元数据），demo 运行时
刷屏属正常；采样数量是二项分布，[3, 25] 是均值 10 的宽容区间。

## How It Works

### 父子 span：context 是接力棒

HTTP 中间件 `Extract` 上游上下文，`Start` 在其上开根 span，新 ctx 传给
handler；handler 开 db 子 span 传入同一 ctx，`Start` 自动记父。整条链不显式
传 ID——context 保证 ctx 带着全部追踪状态。

### ParentBased：比例只对根生效

`ParentBased(ratio)` 的语义：根 span 按 10% 掷骰子；有父时**跟随父的采样
决定**——链路要么完整要么缺席。本篇实测踩中反例：给每个请求塞 `flags=01`
（已采样）的 traceparent 等于说"我爸采了"，100/100 全采。测试改为真根请求。

### 透传：跨进程接力的全部协议

`propagation.TraceContext` 的 Extract/Inject 即 traceparent 的解析与序列化：
服务端 Extract 后 span 继承上游 TraceID、记上游 SpanID 为父；下游调用时注入
回响应头。任何一段不透传，链路断成两截——测试断言游离 TraceID 即失败。

## Pitfalls & Q&A

- **有父 span 时期待比例采样**：现象是 100/100 全采或全不采（本篇实测）。
  原因：`ParentBased` 下比例只对根 span 生效。解法：测采样必须用真根请求；
  链路中途的采样由上游决定
- **BatchSpanProcessor 异步导出**：现象是测试结束一个 span 都没导出
  （本篇实测）。原因：批处理按队列延迟批量落盘。解法：测试用
  `WithSyncer`，生产用 Batcher
- **span 里塞敏感数据**：db.statement 带着用户参数进了追踪系统。解法：
  只放结构性信息（表名、操作类型），参数值脱敏或省略
- **忘挂 Propagator**：现象是服务端每条都是新 trace。原因：OTel 默认无
  propagator。解法：`otel.SetTextMapPropagator(propagation.TraceContext{})`

**Q：采样丢了故障请求怎么办？**
A：头部采样对稳定故障有效，对偶发慢请求会漏。工业方案是尾部采样：collector
收全量，"有错必留、慢必留、其余抽 10%"——代价是缓存整条链路。marklet 的
stdout 是开发形态，生产接 OTLP 后再上。

**Q：trace 里的 HTTP span 为什么用 `METHOD path` 命名？**
A：聚合按 span 名分组——用具体 id 命名会把每条请求拆成独立"名字"，聚合
失效。低基数命名（方法+路由模式）是 span 命名规范的核心条款。

**Q：span 和实验 13 的日志怎么配合？**
A：span 树给结构（哪段慢），日志给细节（发生了什么）。最佳实践是把 trace_id
注入日志字段——实验 13 的 request_id 与 trace_id 在 collector 侧联动，
两套观测件由此缝合。

**Q：为什么 stdout 导出也能算数？**
A：OTel 把"怎么采"（SDK）与"发去哪"（exporter）解耦。stdout 是最简单的
exporter，换 OTLP/gRPC 只改一行构造——开发期 stdout、生产期 collector，
是官方推荐路径。
