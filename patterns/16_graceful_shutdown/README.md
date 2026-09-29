# 16 · Graceful Shutdown：信号、drain 与超时兜底

> 滚动部署时一个 `kill -9` 就能让正在处理的请求全部断掉。优雅关闭的三步——捕获信号、停止接新、等旧的跑完（带超时兜底）——是每个线上服务必备的零件。本实验用标准库 `http.Server.Shutdown` + `sync.WaitGroup` 做了一个**能真实跑完的 demo**，还演示了一个反直觉事实：标注"将被取消"的请求其实没有被取消。

## Background

服务更新、扩缩容时进程总要重启，最直接的方式是发信号让进程退出。`kill -9`（强制终止，进程无法捕获）立即杀死进程，所有正在处理的请求当场断掉——对用户表现为连接重置，写了一半的数据库事务悬在中间。

退出方式不同，结果天差地别：

1. `kill -9`：立即终止，活跃请求全断；
2. `srv.Close()`：立即关闭监听和所有连接，活跃请求被中断；
3. `srv.Shutdown(ctx)`：**停止接受新连接、等活跃请求做完**——但它需要超时兜底，否则一个卡死的请求就能让进程永远退不出去。

第三种就是优雅关闭。它不是单个 API 调用，而是信号处理、请求计数、超时控制三件事的配合，本实验把每件事都变成可观察的输出。

## What

**优雅关闭**（graceful shutdown）是一种进程退出策略：收到终止信号后先停止接受新请求，再等已在处理的请求完成（这个等待阶段常叫 drain，排空——不再进新活、把存量处理完），超时则强制退出，保证不主动打断任何一笔业务。

心智模型一句话：`Shutdown` 只关 listener（监听器——绑定端口、接收新连接的对象）、不杀活跃连接；middleware（中间件——包在 handler 外、对所有路由统一生效的函数）里的 `wg.Add/Done` 负责计数。

`select { wg.Wait 完成 | ctx 超时 }` 二选一后无论如何都调 `Shutdown` 收尾。

可以把优雅关闭想象成餐厅打烊：先挂出"停止接待新客"的牌子（关 listener），等已落座的客人吃完（WaitGroup 计数归零），同时设一个最晚清场时间（超时兜底）——到点未走也请离场，不能永远等下去。但和餐厅不同的是：这里的"请离场"是直接断开连接，超时后残留连接由操作系统接管。

`WaitGroup` 是标准库的并发计数器（`Add` 加一、`Done` 减一、`Wait` 等清零）。本实验把机制拆成三个可见的部分：信号如何变成 channel、WaitGroup 如何追踪活跃请求、context 超时如何兜底。

## When to Use

这节回答"哪些服务退出时需要这套流程，哪些不需要"。

典型场景：

- **滚动发布**：K8s 这类平台更新容器时发 SIGTERM（kill 命令默认发送的终止信号），服务必须趁退出前的宽限期把存量请求处理完，否则每次发布都产生一批失败请求。
- **慢请求、长连接服务**：上传、导出、流式响应一跑几十秒，被硬杀的代价远高于多等几秒。
- **带副作用的请求**：处理中包含数据库事务或消息确认，中断会留下半成品状态。

何时不用：

- 一次性脚本、无状态的只读服务：直接退出没有损失，信号处理是多余的复杂度。
- 请求可安全重试且下游幂等（同一操作执行多次结果相同）：客户端重试即可兜底，不必为此拉起全套机制。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| `kill -9` 强杀 | 立即终止，活跃请求全断 | 进程已失控（死锁、僵死）时的最后手段 |
| `srv.Close()` | 立即关闭监听与所有连接，活跃请求被中断 | 紧急回滚、需要立刻释放端口时 |
| `srv.Shutdown(ctx)` + 超时兜底 | 停接新、等旧的，超时强制收尾 | 正常发布的默认选择（本篇方案） |

## Quick Start

前置条件：只需安装 Go，无需真实流量——demo 自己模拟信号。

```bash
cd patterns/16_graceful_shutdown
go run graceful_shutdown.go   # 全自动演示：3s 后模拟 SIGINT
```

预期输出（实测，地址/时长为一次真实运行）：

```text
=== Graceful Shutdown Demo ===
Server starting on :18080
  [server] listening on :18080
  [demo] launching 2 slow requests (1.5s and 4s)...
  [req-1] started (will take 1.5s)
  [req-2] started (will take 4s — will be cancelled)
  [req-1] finished

  [demo] simulating SIGINT after 3s...

  [signal] received interrupt — shutting down...
  [shutdown] draining active requests (timeout=10s)...
  [req-2] finished
  [drain] all in-flight requests completed
  [shutdown] clean shutdown (elapsed: 1.000910417s)
```

输出读法：

- t=0：两个慢请求启动，计划耗时 1.5s 与 4s；
- t=3s：模拟的 SIGINT 到达，server 进入 drain 阶段；
- t=4s：req-2 自然跑完，drain 只等了约 1 秒，远未触及 10s 超时。

注意两点：**信号后只等了约 1 秒**就 clean 退出（req-2 在 t=4s 自然跑完，远没碰到 10s 超时）；`req-2` 打印的是 `finished` 而不是被取消——原因见 How It Works 的"裸 goroutine"一节。

## How It Works

### 信号捕获：`os/signal` 把信号变成 channel

`signal.Notify(sig, os.Interrupt)` 之后，SIGINT（Ctrl-C 对应的中断信号）/SIGTERM 不再直接杀进程，而是投递到 `sig` channel——于是可以用 `select`（同时等待多个 channel 的语句）把"等信号"和"等其他事件"组合起来。

demo 用 `time.AfterFunc(3s)` 往同一个 channel 发 `os.Interrupt`，模拟真实的 Ctrl-C。输出里的 `[signal] received interrupt — shutting down...` 就是这一步的产物。

### 活跃请求追踪：middleware 层的 WaitGroup

`activeMiddleware` 在每个请求进入时 `wg.Add(1)`、结束时 `defer wg.Done()`，对所有路由透明。

drain 时另一个 goroutine（Go 的轻量并发执行单元）跑 `wg.Wait()`，配合 `select` 与 10s 超时赛跑：清零则打印 `all in-flight requests completed`，超时则打印 `timeout after 10s, forcing shutdown`。

你在输出里看到的 `[drain] all in-flight requests completed`，就是"清零分支"胜出的结果。

### 本 demo 的关键真相：那两个"请求"不是真请求

demo 里 req-1/req-2 是**直接操作 WaitGroup 的裸 goroutine**，没有走 HTTP，也没有 `r.Context()` 可取消——所以代码注释里"will be cancelled"的 req-2 实际上正常跑完并打印 `finished`。

真实的取消传播发生在真 HTTP 请求上：`Shutdown` 的 ctx 超时后，handler 里通过 `<-r.Context().Done()` 感知并提前退出（`slowHandler` 的 select 就是为此写的）。

把时间摆出来：t=3s 信号到达，t=4s req-2 自然结束，drain 只等了 1s。

### Shutdown 与超时的传播链

`drainAndShutdown` 无论走完哪个分支都会 `srv.Shutdown(ctx)`：它先关掉 listener（新连接进不来），再等活跃连接收尾；若 ctx 已超时，活跃请求的 `r.Context()` 被级联取消，进程随后退出，残留连接由操作系统关闭。

### 关键代码点速查

| 代码点 | 作用 | 为什么重要 |
|---|---|---|
| `signal.Notify(sig, os.Interrupt)` | 信号→channel | 不捕获的话 SIGTERM 直接杀进程（K8s 滚更场景） |
| `wg.Add(1) / defer wg.Done()` | 活跃请求计数 | 必须在 middleware 层，进出配对 |
| `select { <-done / <-ctx.Done() }` | 清零 vs 超时 | 没有超时兜底，一个卡死请求就挂住整个退出 |
| `srv.Shutdown(ctx)` | 优雅关闭核心 API | 与 `Close()` 的区别：不杀活跃连接 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **注释说会被取消的请求没有被取消**：现象是 req-2 照常打印 `finished`。原因：demo 的"请求"是裸 goroutine，没走 HTTP，没有 `r.Context()` 可取消——真正会被取消的只有走 HTTP 的请求。解法：让取消生效必须走真实 HTTP 栈，handler 监听 `r.Context()`（见上文"关键真相"一节）。
- **`Shutdown` 之后误判退出失败**：现象是退出阶段报错。原因：`Shutdown` 之后不要再 `ListenAndServe`，返回 `http.ErrServerClosed` 属正常。解法：像 demo 一样显式过滤该错误。
- **K8s 下发布偶发强杀**：现象是滚动更新时部分容器被 SIGKILL（不可捕获的强杀信号）干掉。原因：graceful 超时必须小于 `terminationGracePeriodSeconds`（默认 30s），否则强杀先到，前功尽弃。解法：把 drain 超时设得比 grace period 略小。
- **下线时上游报错激增**：现象是停掉某服务后，调用它的服务开始大量失败。原因：多服务依赖链的关闭顺序没有**反向**。解法：先摘流量入口（Gateway），最后停最下游。

**Q：如何防止请求丢失？**
A：`Shutdown` 关 listener 不关活跃连接 + middleware 层 WaitGroup 计数 + `wg.Wait()` 等清零，三者缺一不可——机制分别见 How It Works 前三节。

**Q：请求一直不完成怎么办？**
A：`context.WithTimeout` 兜底；超时后 `Shutdown` 返回 `DeadlineExceeded`，活跃请求的 `r.Context()` 被取消，进程直接退出（OS 关残留 TCP）。

**Q：`Shutdown` 和 `Close` 的区别？**
A：一句话——`Shutdown` 停接新、等旧的；`Close` 立即全关。选型见 When to Use 的对比表。

**Q：K8s 下还要注意什么？**
A：捕获 SIGTERM（默认会杀进程）；graceful 超时 < `terminationGracePeriodSeconds`；用 `preStop` hook 先从服务发现摘除自己，避免摘流与 drain 竞争。

**Q：多个服务怎么协调关闭顺序？**
A：反向依赖序——先停 Gateway 停止进新流量，drain 完再停下游；用 context 链或 channel 逐级传播关闭信号。
