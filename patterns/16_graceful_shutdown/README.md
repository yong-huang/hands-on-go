# 16 · Graceful Shutdown：信号、drain 与超时兜底

> 滚动部署时一个 `kill -9` 就能让正在处理的请求全部断掉。优雅关闭的三步——捕获信号、停止接新、等旧的跑完（带超时兜底）——是 Go 后端的经典考点，也是每个线上服务必备的零件。本实验用标准库 `http.Server.Shutdown` + `sync.WaitGroup` 做了一个**能真实跑完的 demo**，还顺手演示了一个反直觉事实：标注"将被取消"的请求其实没有被取消。

## 1. 为什么需要它

三种关闭方式的效果天差地别：`kill -9` 立即终止、活跃请求全断；`srv.Close()` 立即关闭监听和所有连接、活跃请求被中断；只有 `srv.Shutdown(ctx)` 会**停止接受新连接、等活跃请求做完**——但它需要超时兜底，否则一个卡死的请求就能让进程永远退不出去。本实验把这套机制拆成三个可见的部分：信号如何变成 channel、WaitGroup 如何追踪活跃请求、context 超时如何兜底。

## 2. 总览：核心机制一图看懂

![Graceful Shutdown：信号 → drain → 超时兜底](images/shutdown.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/16_graceful_shutdown/images/shutdown.html)
> （或本地打开 [`images/shutdown.html`](images/shutdown.html)）。

**心智模型一句话**：`Shutdown` 只关 listener、不杀活跃连接；middleware 里的 `wg.Add/Done` 负责计数；`select { wg.Wait 完成 | ctx 超时 }` 二选一后无论如何都调 `Shutdown` 收尾。图里红色支路是"请求卡死"时的强制路径——正常情况下走不到。

## 3. 快速开始

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

注意两点：**信号后只等了约 1 秒**就 clean 退出（req-2 在 t=4s 自然跑完，远没碰到 10s 超时）；`req-2` 打印的是 `finished` 而不是被取消——原因见 §4.3。

## 4. 核心概念

### 4.1 信号捕获：`os/signal` 把信号变成 channel

`signal.Notify(sig, os.Interrupt)` 之后，SIGINT/SIGTERM 不再直接杀进程，而是投递到 `sig` channel——于是可以用 `select` 把"等信号"和"等其他事件"组合起来。demo 用 `time.AfterFunc(3s)` 往同一个 channel 发 `os.Interrupt`，模拟真实的 Ctrl-C。

### 4.2 活跃请求追踪：middleware 层的 WaitGroup

`activeMiddleware` 在每个请求进入时 `wg.Add(1)`、结束时 `defer wg.Done()`，对所有路由透明。drain 时另一个 goroutine 跑 `wg.Wait()`，配合 `select` 与 10s 超时赛跑：清零则打印 `all in-flight requests completed`，超时则打印 `timeout after 10s, forcing shutdown`。

### 4.3 本 demo 的关键真相：那两个"请求"不是真请求

![一次真实 drain 的时间线（demo 实测）](images/timeline.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/16_graceful_shutdown/images/timeline.html)
> （或本地打开 [`images/timeline.html`](images/timeline.html)）。

demo 里 req-1/req-2 是**直接操作 WaitGroup 的裸 goroutine**，没有走 HTTP，也没有 `r.Context()` 可取消——所以代码注释里"will be cancelled"的 req-2 实际上正常跑完并打印 `finished`。真实的取消传播发生在真 HTTP 请求上：`Shutdown` 的 ctx 超时后，handler 里通过 `<-r.Context().Done()` 感知并提前退出（`slowHandler` 的 select 就是为此写的）。时间线上看得很清楚：t=3 信号到达，t=4 req-2 自然结束，drain 只等了 1s。

### 4.4 Shutdown 与超时的传播链

`drainAndShutdown` 无论走完哪个分支都会 `srv.Shutdown(ctx)`：它先关掉 listener（新连接进不来），再等活跃连接收尾；若 ctx 已超时，活跃请求的 `r.Context()` 被级联取消，进程随后退出，残留连接由操作系统关闭。

## 5. 关键代码与坑清单

| 代码点 | 作用 | 为什么重要 |
|---|---|---|
| `signal.Notify(sig, os.Interrupt)` | 信号→channel | 不捕获的话 SIGTERM 直接杀进程（K8s 滚更场景） |
| `wg.Add(1) / defer wg.Done()` | 活跃请求计数 | 必须在 middleware 层，进出配对 |
| `select { <-done / <-ctx.Done() }` | 清零 vs 超时 | 没有超时兜底，一个卡死请求就挂住整个退出 |
| `srv.Shutdown(ctx)` | 优雅关闭核心 API | 与 `Close()` 的区别：不杀活跃连接 |

坑清单：

- demo 的"请求"是裸 goroutine：注释说会被取消，实际不会被取消——真正会被取消的只有走 HTTP、带 `r.Context()` 的请求
- `Shutdown` 之后不要再 `ListenAndServe`；返回 `http.ErrServerClosed` 属正常，要像 demo 一样显式过滤
- K8s 场景：graceful 超时必须小于 `terminationGracePeriodSeconds`（默认 30s），否则 SIGKILL 强杀前功尽弃
- 多服务依赖链的关闭顺序要**反向**：先摘流量入口（Gateway），最后停最下游

## 6. 文件结构

```text
patterns/16_graceful_shutdown/
├── README.md                     # 本文件：原理 + 运行指引 + 深入要点
├── graceful_shutdown.go          # 主教材：middleware 计数 → 信号 → drain → Shutdown
└── images/
    ├── shutdown.workflow.json    # 图源：关闭流程（Typed JSON IR）
    ├── shutdown.html             # 交互版：自包含单文件
    ├── shutdown.svg              # README 内嵌双主题矢量图
    ├── timeline.sequence.json    # 图源：实测 drain 时间线
    ├── timeline.html             # 交互版
    └── timeline.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：如何防止请求丢失？**
A：`Shutdown` 关 listener 不关活跃连接 + middleware 层 WaitGroup 计数 + `wg.Wait()` 等清零，三者缺一不可。

**Q：请求一直不完成怎么办？**
A：`context.WithTimeout` 兜底；超时后 `Shutdown` 返回 `DeadlineExceeded`，活跃请求的 `r.Context()` 被取消，进程直接退出（OS 关残留 TCP）。

**Q：`Shutdown` 和 `Close` 的区别？**
A：`Shutdown` 停接新、等旧的；`Close` 立即全关。紧急回滚用 `Close`，正常发布用 `Shutdown`。

**Q：K8s 下要注意什么？**
A：捕获 SIGTERM（默认会杀进程）；graceful 超时 < `terminationGracePeriodSeconds`；用 `preStop` hook 先从服务发现摘除自己，避免摘流与 drain 竞争。

**Q：多个服务怎么协调关闭顺序？**
A：反向依赖序——先停 Gateway 停止进新流量，drain 完再停下游；用 context 链或 channel 逐级传播关闭信号。

## 8. 总结

优雅关闭 = 信号进 channel + WaitGroup 计数 + `Shutdown(ctx)` 三件套，超时是唯一的安全网。本 demo 还顺带提醒：**看注释不如跑一遍**——"will be cancelled"的请求实际活得好好的，因为取消只传播给真 HTTP 请求。读时间线（§4.3 的图）比背结论更能回答一切追问。

下一篇 [17_mini_redis](../17_mini_redis/) 把视角从"关闭服务"转向"实现服务"：手写一个 RESP 协议的迷你 Redis。
