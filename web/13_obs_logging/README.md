# 13 · slog 结构化日志：请求 ID 贯通与敏感字段脱敏

> 出了问题就要能回答"这个请求经历了什么"。本篇给 marklet 装上观测地基：
> 请求 ID 中间件（客户端传 X-Request-Id 则透传，没传则生成）把中间件与 handler
> 的每条日志串成同一 ID；slog JSON 输出让一条请求的轨迹一屏聚合；
> 敏感字段强制脱敏——日志里 grep 不到密码与令牌。

## Background

日志的最原始形态是 `fmt.Println`：出了问题翻 stderr，人肉把散落的行按时间排好。
服务一多就撑不住——高并发下两个请求的行交织在一起，你根本分不清哪行属于哪个请求。

第一个解法是请求 ID：每个请求进门领唯一 ID，之后每行日志都带着。第二个是
结构化：日志变键值对（JSON 一行一条），`grep request_id=xxx` 即聚合完整轨迹，
是日志平台的通用货币。第三个最隐蔽：调试时把密码打进日志——明文留存期
比数据库还长，等于二次泄露。

## What

slog（Go 1.21 起的标准库结构化日志）三角色：Logger（入口）、Handler（输出
格式，这里 JSONHandler）、Attr（键值属性）。marklet 的封装 = 两层中间件 + 一个写入口：

| 组件 | 职责 |
|---|---|
| RequestID（外层） | 透传/生成 X-Request-Id，注入 context，logger 预带 ID |
| AccessLog（内层） | 记录方法、路径、耗时——取到的 logger 已带 ID |
| LogSafe | 脱敏写日志的唯一入口：password/token 类键值替换为 *** |

可以把请求 ID 想成超市小票的流水号：购物的每个环节（称重、收银、打包）都打印
同一个号，事后对账按号一拉就是全过程。但和小票不同的是，流水号必须支持"顾客自带"——
上游网关已经生成了追踪 ID 时，透传它才能把两个系统的轨迹接起来。

## When to Use

- **多服务排障**：一个请求穿过网关、API、DB，request_id 是唯一能把这些行缝起来的线
- **给用户回执**：把 ID 放进报错提示（"引用此 ID 联系客服"），支持不需要翻服务端
- **合规审计**：登录、改密等敏感操作的日志要求可检索且不含明文凭证

何时不用：一次性脚本、CLI 工具——没有"多个请求交织"的问题，fmt 足够。

| 做法 | 可检索 | 跨层一致 | 泄露风险 |
|---|---|---|---|
| fmt.Println | ❌ | ❌ | 靠人记性 |
| slog 但各层自取 logger | ✅ | ❌（ID 传丢） | 靠人记性 |
| 中间件注入 + LogSafe（本篇） | ✅ | ✅（类型保证） | 助手强制脱敏 |

## Quick Start

前置条件：Go ≥ 1.21（slog 起始版本）。运行：

```bash
cd web/13_obs_logging
go run .
go test -race -count=3 .
```

真实输出（`go run .`，stderr 每行一条 JSON；节选）：

```text
{"time":"2026-10-09T20:28:37.498655+08:00","level":"INFO","msg":"handler doing work","request_id":"my-trace-id-001","step":1}
{"time":"2026-10-09T20:28:37.498659+08:00","level":"INFO","msg":"access","request_id":"my-trace-id-001","method":"GET","path":"/api/v1/work","duration_ms":0}
  客户端传 X-Request-Id: my-trace-id-001 → 响应头回带 "my-trace-id-001"，日志两行同 ID
{"time":"2026-10-09T20:28:37.498869+08:00","level":"INFO","msg":"login attempt","request_id":"b47fb8c7b8a0c15b75bb24908752e7ea","username":"alice","password":"***","token":"***"}
  登录日志已写 stderr——password/token 应为 ***
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁）：

```text
ok  	marklet/13_obs_logging	1.605s
```

诚实预期：request_id 与时间戳每次运行都不同；demo 的服务跑完自动退出，
想手工体验可以加 `select{}` 自行改造后用 curl 带 `-H "X-Request-Id: test-1"`。

## How It Works

### 中间件顺序：注入者必须在外层

`use(mux, RequestID, AccessLog)`——RequestID 在外层先执行，构造带 ID 的
logger 放进 `r.Context()`，内层取到的即注入后的版本。顺序反了，AccessLog
拿到原始 context，退回默认 logger（实测踩中：healthz 行以 text 现形）。

### logger 预注 vs 每次手带

RequestID 放进 context 的不是 ID 字符串，而是 `logger.With("request_id", id)`
整个 logger——下游每条日志自动带 ID。手写 `"request_id", id` 会漏（忘了取、
取到空串），预注从类型上消灭失误。响应头回带同值，客户端报障直接引用。

### LogSafe：把脱敏变成机制而不是纪律

人因是"每个调用点都要记得"，解法是收口：写日志只允许走 `LogSafe`，它扫描
password/token/authorization/secret 键名，命中即替换为 ***。非敏感字段
（username）原样保留。测试断言三件事：明文不存在、*** 存在、username 原样。

## Pitfalls & Q&A

- **AccessLog 放在 RequestID 外层**：现象是 access 行没有 request_id 或退回
  text 格式（本篇实测）。原因：外层拿不到内层注入的 context。解法：注入者
  在外、使用者在内
- **透传头不设防**：现象是客户端塞 500 个垃圾字节，日志里被原样记录。
  原因：透传没有上限校验。解法：空串或超长（>128）一律重新生成
- **脱敏靠 code review 提醒**：现象是某次紧急调试的日志带上了明文密码。
  原因：纪律没有机制兜底。解法：LogSafe 单一入口 + 泄露断言进测试
- **日志打在 next.ServeHTTP 之前取耗时**：现象是 duration 恒为 0。
  原因：next 还没执行。解法：记录起点、执行、再算差值（AccessLog 的三行结构）

**Q：为什么 ID 用 crypto/rand 而不是自增或时间戳？**
A：自增 ID 泄露业务量（每秒多少请求一目了然）且多实例会撞号；时间戳精度不够。
128 位随机数的碰撞概率在宇宙尺度上可忽略，且不携带任何业务信息。

**Q：为什么响应头也要回带 X-Request-Id？**
A：对账需要双向可见：用户报障说"我的请求失败了"，拿出响应头里的 ID，
服务端一 grep 就是完整轨迹。没有回带，用户侧与服务端各说各话。

**Q：JSON 日志比文本大那么多，值得吗？**
A：检索成本的反向。文本日志排查 = 人肉读 + 正则猜；JSON 日志 = 字段索引直接查。
存储的差价远低于工程师排障的时间价——这也是日志平台全部只收结构化输入的原因。

**Q：敏感字段还有哪些容易漏？**
A：本篇的键名清单之外要警惕：URL 查询参数里的 token（?token=xxx）、JSON 嵌套
深处的 credential、错误信息里内嵌的 SQL（可能带绑定值）。原则：凡是"泄露后
可直接登录"的值都不进日志；不确定时打码——多打一行 *** 的代价远低于泄露。
