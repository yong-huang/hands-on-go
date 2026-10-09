# 01 · marklet 骨架：路由、配置与优雅退出

> marklet 是一个书签/稍后读 Web 服务，本篇立起它的骨架：环境变量配置、Go 1.22 ServeMux
> 方法路由、双探针分离与 SIGTERM 优雅退出（graceful shutdown，先停新请求、再等存量收尾）。
> 真实输出将看到：摘除窗口内 readyz 立即 503 而 healthz 仍 200，进程在 2 秒验收线内干净退出。

## Background

一个 Web 服务最直接的写法：路由在一个 handler 函数里用 `if r.URL.Path == ...` 逐个分支分发，
端口号写死在代码里，停机靠 `kill -9` 强杀进程。

这些做法会在明确的场景撞墙：路由分支一多，"这个路径接受哪些方法"只能通读代码才知道；
`kill -9` 把正在处理的请求拦腰截断——客户端收到连接重置，写了一半的数据没有落盘机会。

三件事各有成熟解：Go 1.22 的 ServeMux 支持"方法 + 路径"一起注册路由；配置改读环境变量
（"十二要素应用"——Twelve-Factor App，一套云原生应用配置管理方法论——的要求）。

停机改用优雅退出：捕获 SIGTERM（Unix 的终止信号，`kill` 默认发送的就是它，区别于 `kill -9` 强杀），
先摘流量再等存量收尾。生产实践再补一条纪律：存活与就绪探针分成两个端点，停机才有抓手。

## What

marklet 骨架由三件套组成：配置加载（环境变量 → `Config` 结构体）、路由分发（ServeMux——
Go 标准库 `net/http` 内置的路由器——按"方法 + 路径"把请求分给对应 handler）、
停机序列（信号 → 摘除 → Shutdown → 超时兜底）。

三者都挂在一个 `ready` 开关上（`atomic.Bool`，并发读写也不出错的原子布尔，机制见 How It Works）。

可以把优雅退出想象成餐厅打烊：先立"暂停营业"的牌子（摘除就绪探针），等客人吃完
（在途请求收尾），最后锁门关灯（进程退出）。但和餐厅不同的是，"等客人"必须有期限——
超时后要由保安请离（超时强杀兜底）。两个探针分别回答两个问题：

| 端点 | 回答的问题 | 摘除窗口内的应答 |
|---|---|---|
| `GET /healthz` | 这个进程还活着吗，该不该被重启 | `200`（进程没问题） |
| `GET /readyz` | 这个实例还能接新请求吗 | `503`（别再派流量给它） |

## When to Use

双探针 + 优雅退出是所有"有存量请求要保护"的常驻服务的出厂配置。

- **容器编排**：Kubernetes 的 liveness/readiness probe 直接对准这两个端点，摘除与重启全自动
- **滚动发布**：旧实例走"摘流量 → 等存量 → 退出"，新实例接管流量，用户全程无感
- **挂负载均衡的服务**：LB（Load Balancer，把请求分摊到多台实例的组件）靠轮询 readyz 选派发目标

何时不用：一次性脚本、命令行工具、本地调试的小服务——进程即用即走，没有存量请求可保护，直接退出即可。

| 停机方式 | 存量请求 | 摘流量时机 | 适用 |
|---|---|---|---|
| `kill -9` | 被截断，客户端连接重置 | 无 | 不适用常驻服务 |
| 只调 `Shutdown` | 等收尾，但新流量还在派入 | 无 | 有明显缺陷 |
| 摘 readyz → Shutdown（本篇） | 等收尾 + 超时兜底 | Shutdown 之前 | 生产 |

## Quick Start

前置条件：Go ≥ 1.22（`"GET /healthz"` 这种注册语法 1.22 才有，本模块 go.mod 声明 go 1.26）；服务监听 `127.0.0.1:19610`。运行：

```bash
cd web/01_skeleton
go run .
go test -race -count=3 .
```

真实输出（`go run .`，程序拉起自身副本自演 SIGTERM 全过程）：

```text
time=2026-10-06T09:52:02.539+08:00 level=INFO msg="marklet 骨架已启动" addr=127.0.0.1:19610
== 第 1 步：正常服务——三个端点各司其职 ==
GET /healthz               → 200 ok
GET /readyz                → 200 ready
GET /api/v1/bookmarks      → 200 {"bookmarks":[],"note":"skeleton: data layer lands in lab 02"}
== 第 2 步：SIGTERM——摘除窗口里 readyz 与 healthz 分裂 ==
[  125ms] kill -TERM
time=2026-10-06T09:52:02.660+08:00 level=INFO msg=收到信号，开始停机 signal=terminated
time=2026-10-06T09:52:02.660+08:00 level=INFO msg="readyz 已摘除，等待负载均衡停止派发" drain_delay=400ms
[  125ms] GET /readyz  → 503 摘除已生效
[  126ms] GET /healthz → 200 存活如常
time=2026-10-06T09:52:03.060+08:00 level=INFO msg="开始 Shutdown：拒绝新连接，等在途请求收尾"
[  827ms] SIGTERM 前的在途 /slow → 200，Shutdown 等它收尾
== 第 3 步：退出收尾——限时退出，端口归还 ==
time=2026-10-06T09:52:03.609+08:00 level=INFO msg="drain 完成：HTTP 已停，进程即将退出"
[ 1076ms] 进程退出：耗时 950ms（验收 ≤2000ms），退出码 0
[ 1076ms] 新连接 → 已拒绝（connect: connection refused）——端口已归还
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁——检测 goroutine 泄漏的测试库，残留即失败）：

```text
ok  	marklet/01_skeleton	3.913s
```

诚实预期：毫秒时间戳每次运行都不同；退出耗时约 950ms 主要由 `/slow` 的 800ms 处理时长决定，
用 `MARKLET_SLOW_DELAY=100ms` 调小即可更快退场；19610 被占用时子进程报 listen 错误退出。

## How It Works

### 方法路由：注册语句同时回答"哪个方法、哪条路径"

Go 1.22 起，ServeMux 的注册模式支持 `"GET /healthz"` 形式：方法成为匹配条件的一部分。
方法不匹配返回 `405`，路径不存在返回 `404`，两种失败从此语义分明。

### 双探针：一个原子开关切出两种答案

`ready` 是 `atomic.Bool` 而非普通 `bool`：停机路径与所有请求 handler goroutine 并发读写它，
普通布尔是数据竞争（data race，多个 goroutine 无同步访问同一内存），`-race` 下直接报红。

摘除窗口指 `ready.Store(false)` 之后、`Shutdown` 之前的时间片：LB 轮询 readyz 拿到 503 摘出实例，
healthz 仍 200 所以不重启进程。`MARKLET_DRAIN_DELAY=400ms` 把这个窗口拉长到肉眼可见。

### 停机序列：顺序即语义

```go
ready.Store(false)            // ① 新请求止步
time.Sleep(cfg.DrainDelay)    // ② LB 的观察期
srv.Shutdown(ctx)             // ③ 关监听器，等在途请求自然收尾
```

输出印证第 ③ 步：`kill` 发生于 `[125ms]`，`/slow` 在 `[827ms]` 才返回 200——它发出于
kill 之前，`Shutdown` 为它多等了约 300ms。超时兜底：`Shutdown` 超时返回 `DeadlineExceeded`，
进程以非零退出码结束。

可测试性来自一个小接口：`graceful` 只要求参数满足 `Shutdown(context.Context) error`，
真服务器与 `httptest` 测试服务器都满足——测试能在毫秒级复演整个停机序列。

## Pitfalls & Q&A

- **Shutdown 之后再去探 readyz**：现象是新连接直接被拒（connection refused）。
  原因：`Shutdown` 一启动就关闭监听器，503 只在摘除窗口内可观测。解法：窗口内轮询断言（本篇测试实录）
- **ready 用普通 bool**：现象是 `-race` 报 Data race。原因：信号路径与 handler goroutine
  并发读写。解法：`atomic.Bool`，读写都是原子操作
- **Shutdown 不设超时**：现象是进程永远不退出。原因：一个永不返回的 handler 拖住等待。
  解法：`context.WithTimeout` 包住 `Shutdown`，超时走非零退出码
- **把依赖健康塞进 healthz**：现象是数据库抖动时进程被编排器连环重启。原因："进程存活"
  与"依赖可用"被混进一个信号。解法：healthz 只答进程问题，依赖健康归 readyz 或诊断端点

**Q：为什么先摘 readyz 再 Shutdown，而不是直接 Shutdown？**
A：`Shutdown` 关监听器是毫秒级动作，LB 的就绪轮询周期却是秒级——直接 Shutdown 会让 LB
在一个轮询周期内继续把新请求派到已关监听的实例上。摘除窗口就是留给 LB 的确认时间。

**Q：退出码 0 和非 0 有什么讲究？**
A：进程管理器按退出码决策：0 表示计划内停机，安静重启即可；非 0 表示异常（如超时了存量没收完），
应触发告警。`os.Exit(1)` 那一行就是在维护这份契约。

**Q：/slow 里为什么要 select `r.Context().Done()`？**
A：`Shutdown` 只承诺"等在途请求收尾"，不承诺等满处理时长——超时兜底触发后存量仍会被放弃。
select 取消信号能在被放弃时提前止损（与 net/12 的断连感知同源）。

**Q：配置为什么全走环境变量，而不是配置文件？**
A：同一份二进制在不同环境只需换环境变量，这是容器编排的原生注入方式；`Config` 结构体同时是
测试的注入点——测试用毫秒级的 `quietConfig` 复演停机序列，不必等真实的 5 秒超时。
