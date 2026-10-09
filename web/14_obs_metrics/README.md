# 14 · /metrics 与 pprof：RED 指标与剖析端点

> marklet 学会"被看见"：手写 Prometheus 文本格式暴露 RED 三件套（请求数、
> 错误码、延迟直方图），直方图分位数与已知分布做对照实验；pprof 挂独立端口
> 19639——主端口上它必须 404。

## Background

服务"能跑"和"能被运维"隔着两类数据。第一类是聚合数值：每秒多少请求、
错误率、延迟分布——需要计数器与直方图持续累积，以标准格式暴露给抓取系统
（Prometheus 定期拉 /metrics）。第二类是即时进程内状态：CPU 烧在哪个函数、
堆里谁在涨——剖析器（pprof，Go 内置）的地盘。

两类数据都含敏感信息（路径分布、内存里的函数名与栈），所以 pprof 的部署纪律
是独立端口、独立网络位——主端口上连路由都不该存在。

## What

RED 是微服务指标的通行口径：Rate（每秒请求数）、Errors（错误占比）、
Duration（延迟分布）。前两个用 counter（只增计数器，按 path+code 维度），
Duration 用 histogram（把延迟按预设上界分桶，输出每桶累计数 + 总和 + 总数）。

可以把直方图想成身高体检的分段统计：化验单不给你每个人的精确身高，
只给你"低于 160 的几人、160-170 的几人……"。分位数（p50/p99）从分段里
插值估计出来。但和体检不同的是，桶边界的选取决定了估计精度——250ms 的桶
意味着 p99 最多偏差 ±125ms，桶就是"你关心的量级"的表态。

| 指标 | 类型 | marklet 的维度 |
|---|---|---|
| http_requests_total | counter | path, code |
| http_request_duration_seconds | histogram | 全局（8 个桶 + Inf） |
| sum / count | histogram 附带 | 总耗时 / 总次数 |

## When to Use

- **容量与告警**：p99 延迟连涨三档、5xx 比例破 1%——告警的数据源就在 /metrics
- **上线对照**：发布前后各抓一次直方图，回归一眼可见
- **性能调查的入口**：指标告诉你"哪里慢"，pprof 告诉你"为什么慢"——两者是
  上下游关系（internals/22 的 CPU 剖析由此接入）

何时不用：一次性脚本（没有"持续"可言）；指标平台已统一接入的团队里
重复造计数器（直接用 prometheus/client_golang，本篇手写是为讲清格式）。

| 观测件 | 回答的问题 | 本篇实现 |
|---|---|---|
| /metrics | 系统现在怎么样 | 手写 exposition 文本 |
| pprof | 进程内部为什么这样 | 独立端口 + 4 个核心 handler |

## Quick Start

前置条件：Go ≥ 1.21。运行：

```bash
cd web/14_obs_metrics
go run .
go test -race -count=3 .
```

真实输出（`go run .`，跑完自动退出；stderr 另有日志）：

```text
== 第 1 步：/metrics——RED 指标随请求增长 ==
  http_requests_total{path="/api/v1/work",code="200"} 3
  http_requests_total{path="/healthz",code="200"} 1
  http_request_duration_seconds_count 4
  （Content-Type="text/plain; version=0.0.4; charset=utf-8"；直方图桶与 sum 行见 go test 输出）
== 第 2 步：直方图分位数对照——估计值 vs 实测 ==
  合成样本：100 个 4ms + 2 个 ~305ms → p50=2.6ms（want ≈4-5） p99=366.2ms（want ≈250-300）
== 第 3 步：pprof 端口隔离 ==
  19639 /debug/pprof/heap → 200，"heap profile: 4: 4464 [4: 4465] @ heap/1048576...
  19622 /debug/pprof/heap → 404（主端口不可达——安全隔离）
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁）：

```text
ok  	marklet/14_obs_metrics	1.584s
```

诚实预期：分位数是桶内插值的估计——p50 估计 2.6ms 而真值 4ms（4ms 样本全部
落在 0-5ms 桶，插值假设均匀分布）、p99 估计 366ms 而真值 305ms（250-500ms 桶
宽 250ms，误差上界 ±125ms）；对照组的意义就是把这份误差入档。

## How It Works

### exposition 文本格式：抓取系统的方言

Prometheus 定期拉走整个文本。四种行：`# HELP`（说明）、`# TYPE`（类型）、
样本行 `名字{label="值"} 数值`、直方图的 `_bucket/_sum/_count` 后缀。
label 是维度切分，数值必须可解析——验收①的"手工解析"即逐行校验此结构，
自定义指标照写即可被抓取。

### 直方图：不存样本的分位数

生产实现是流式累计（每桶一个计数器），不存单个样本——内存恒定。本篇为让
"估计 vs 实测"可对照，额外留了样本数组：`Quantile` 先找排名落在哪个桶，
再在桶内线性插值。对照组显示两个方向的误差：p50 偏低（均匀假设）、
p99 偏高（桶宽离散化）。

### pprof 的端口隔离

pprof 的 handler 挂在 19639 的独立 `http.ServeMux` 上，主端口 mux 里根本
没有 `/debug/pprof` 路由——不是"返回 403"，是"这条路径不存在"。独立端口
还能在网络层单独收紧（bind 内网 IP、防火墙）——pprof 泄露的栈与函数名
是攻击者免费的信息侦察。

## Pitfalls & Q&A

- **/metrics 路由忘注册**：现象是抓取端拿到 404 的空响应（本篇实测）。
  原因：中间件包了 mux，但 mux 里没有这条路由。解法：注册 + 标准
  Content-Type（`text/plain; version=0.0.4`）
- **指标无维度直接累加**：现象是"总请求数在涨，但不知道哪个路径慢"。
  原因：counter 没带 label。解法：path+code 维度在计数时打上
- **pprof 挂主端口**：现象是任何人 curl 即可拿到堆剖面。原因：图省事用
  `pprof.Handler()` 整包挂载。解法：独立端口 + 按需挂载核心 handler
- **把分位数估计当真值**：现象是拿 p99 估计值定 SLO，误差上界不明。
  解法：桶边界按关心的量级设计，SLO 用真实样本统计兜底校准

**Q：为什么 counter 只能增不能减？**
A：Prometheus 的算式用 `rate()` 对增量做除法，"减少"会被当成计数器重置
（进程重启）处理。当前活跃数这类可增减的量用 gauge 类型。类型即语义，
抓取端按语义计算。

**Q：直方图为什么不用"存全部样本再算精确分位数"？**
A：内存与 QPS 成正比（本篇的样本数组在真实生产里就是 OOM 隐患）。流式桶
内存恒定，代价是精度受桶宽限制——这是分布式指标系统的标准取舍，
对照组实验让这个代价可见、可校准。

**Q：pprof 的四个 handler 够用吗？**
A：核心是 heap（内存）、goroutine（泄漏）、profile（CPU 采样）、index（导航）。
trace/block/mutex 按需加。独立端口 + 显式挂载让暴露面可审计；实验 29 的
goroutine 剖析从这里接。

**Q：指标、日志、pprof 三者的分工？**
A：指标回答"有不有问题"（聚合告警），日志回答"哪些请求受影响"（实验 13 的
request_id 是钥匙），pprof 回答"代码为什么这样"（函数级根因）。排障顺序：
指标报警 → 日志定位 → pprof 剖析。
