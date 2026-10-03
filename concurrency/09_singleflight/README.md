# 09 · singleflight 手写：同 key 并发只放行一次的请求合并器

> 本篇手写一个请求合并器（singleflight）：同一 key 的并发调用只放行一个执行，其余等待并共享结果。包含同 key 去重、不同 key 并行、panic 原值传播三个断言测试，并与 golang.org/x/sync/singleflight v0.23.0 逐行对照出行为差异表。读完本篇能说清热点 key 缓存击穿是怎么被合并掉的，以及哪些场景合并器救不了。

## Background

热点缓存条目失效的瞬间，成百上千个请求同时发现缓存为空，全部涌向后端——数据库瞬间被打出峰值，这个现象叫缓存击穿。给整段重建逻辑加一把全局锁可以挡住它，代价是不同 key 的重建互相排队；给每个 key 一把锁又引入锁表的生命周期管理。

痛点的本质：重复的不是"访问"，而是"同一时刻对同一 key 的在途请求"。人们需要的不是锁，而是合并——第一个请求执行，其余请求等它的结果。Go 官方在 x/sync 里给出的答案就是 singleflight：按 key 记录在途调用，后来者挂起等待，结果共享。

本篇从零写一个行为对齐的最小实现，再用对照库验证合并语义一致，差异集中在 panic 包装与少量辅助 API 上。

## What

singleflight Group 是一个请求合并器：同一 key 同时只允许一个 fn 在执行，并发到达的调用者共享同一个 call 的结果。它由两个部件组成：

| 部件 | 职责 |
|---|---|
| `Group.m` | `map[string]*call`：key 到在途调用的登记表，`mu` 保护 |
| `call` | 一次在途执行：`wg` 供等待者挂起，`val`/`err` 共享结果，`dups` 记共享者数，`panicked` 标记 panic |

可以把 Group 想成机场的拼车调度台：第一位乘客包车出发（执行 fn），同目的地的后来者直接上车分摊（等待并共享结果）；发车时刻登记表上这趟车就注销，下一位乘客重新包车。但和拼车不同的是，Group 不记历史订单——结果不缓存，前后两批请求之间没有合并，这是它与 memoization 的分界线。

## When to Use

**热点 key 缓存重建**。缓存失效回源的瞬间，把 N 个并发的回源请求合并成 1 次后端调用，这是它最经典的使用位置：`Do(key, func() { return loadFromDB(key) })`。

**单进程内的并发初始化与防重**。首次访问时建立连接、加载配置、预热资源：多个 goroutine 同时触发，只执行一次。

**何时不用**：需要跨进程合并（多实例各自有自己的 Group）时要用分布式方案；需要结果缓存时，Group 管不了前后批请求，外层仍要配真正的缓存。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 全局锁 | 所有 key 互相排队 | 临界区极短、竞争少 |
| 每 key 一把锁 | 锁表生命周期要自管 | key 集合稳定且需要多次复用 |
| singleflight | 只合并同时在途的调用 | 突发并发击穿后端 |
| 缓存 | 跨时间的复用 | 与 singleflight 叠加，各管一头 |

## Quick Start

需要 go 1.26 与 golang.org/x/sync（对照用，`go get golang.org/x/sync@latest` 已拉取 v0.23.0）。运行分步演示：

```bash
go run ./09_singleflight/
```

真实输出（步骤 2 的耗时随机器略有浮动，其余各数值确定）：

```text
== 步骤 1：100 个 goroutine 同 key，后端只落 1 次 ==
  后端执行次数=1（诚实预期：1；100 个调用者共享同一份结果）
== 步骤 2：不同 key 互不阻塞，4 路并行 ==
  后端执行次数=4（诚实预期：4），总耗时 81ms（诚实预期：约 80~120ms 而非 320ms+）
== 步骤 3：panic 不吞——发起者与等待者都拿到原值 ==
  发起者 recover 到=backend exploded（诚实预期：backend exploded，未包一层）
  等待者 recover 到=backend exploded
  等待者 recover 到=backend exploded
  等待者 recover 到=backend exploded
  panic 后同 key 再次 Do 正常执行——失败的 call 已从 map 清除
== 步骤 4：对照 golang.org/x/sync/singleflight v0.23.0 ==
  x/sync 同 key 100 并发：后端执行次数=1（诚实预期：1，与手写版一致）
```

正确性与对照断言用竞态检测器验证：

```bash
go test -race -count=10 -timeout 120s ./09_singleflight/
```

真实输出：

```text
ok  	concurrency/09_singleflight	2.785s
```

## How It Works

`Do` 的分叉点只有一处——登记表查询。命中即共享，未命中即注册并执行：

```go
if c, ok := g.m[key]; ok {
	c.dups++
	g.mu.Unlock()
	c.wg.Wait()          // 后来者不执行 fn，睡在它的 WaitGroup 上
	if c.panicked.Load() {
		panic(c.val)     // 与发起者同源：等待者也收到原始 panic 值
	}
	return c.val, c.err, true
}
```

注册者的责任由 `doCall` 的 defer 兜底，这是整个机制正确性的关键：无论 fn 成功、失败还是 panic，等待者必须被唤醒、登记表必须清干净，否则后来者会永远等一个已经死掉的 call：

```go
defer func() {
	if r := recover(); r != nil {
		c.panicked.Store(true)
		c.val = r      // 留给等待者 re-panic 用
		// 先清理、后唤醒，最后按原值重新 panic——顺序不可颠倒
		g.mu.Lock(); delete(g.m, key); g.mu.Unlock()
		c.wg.Done()
		panic(r)
	}
	g.mu.Lock(); delete(g.m, key); g.mu.Unlock()
	c.wg.Done()
}()
```

先 `delete` 再 `wg.Done` 的顺序保证了"等待者醒来时，key 必然已清理"：等待者醒来后立刻发起新调用也拿到的是全新执行，步骤 3 末尾的"再次 Do 正常执行"验证的就是这条先后关系。

与 golang.org/x/sync/singleflight v0.23.0 的行为对照（逐行读其源码核对）：

| 行为 | 手写版 | x/sync v0.23.0 |
|---|---|---|
| 结果共享 | 等待者 `wg.Wait` 后取共享 `val`/`err`，返回 `shared` 标记 | 相同机制（`call.wg` + `dups` 计数） |
| panic 传播 | 发起者与等待者都按原始 panic 值重新 panic | 双方也重新 panic，但值被包成 `*panicError`（含堆栈）；有 `DoChan` 等待者时由后台 goroutine panic 使进程崩溃 |
| `runtime.Goexit` | 未识别：等待者被唤醒后拿到零值成功结果，属行为偏差 | 识别 Goexit，等待者转而执行 `runtime.Goexit()` |
| 失败后的重试 | call 从 map 删除，同 key 下次 Do 重新执行 fn | 相同（defer 中删除） |
| 辅助 API | 仅 `Do` | 另有 `DoChan`（异步收结果）与 `Forget`（手动失效 key） |

对照实测见步骤 4：同样的同 key 100 并发，x/sync 的后端计数同样是 1，合并语义同源。

## Pitfalls & Q&A

**把 singleflight 当缓存用**。现象：请求间隔到达时后端仍被逐次打到。原因：call 在 fn 结束的瞬间就从登记表删除，前后两批请求之间不存在合并。解法：外层配真正的缓存，Group 只负责挡住同一瞬间的并发。

**慢请求拖住所有等待者**。现象：一个卡死的后端把同 key 的全部调用者挂起。原因：等待者没有超时语义，`wg.Wait` 只能等。解法：把超时做进 fn（传 context，到期返回错误），让 call 尽快收敛，而不是指望 Group 中断它。

**Q：等待者在 panic 时拿到零值成功结果，有多严重？** 这是手写版相对 x/sync 的已知偏差（未识别 `runtime.Goexit` 的连带后果）。

生产实现建议照 x/sync 处理：用双 defer 区分 panic 与 Goexit，Goexit 时让等待者也 `runtime.Goexit()`，否则等待者会把"没有结果"当成"结果是零值"。

**Q：为什么不同的 key 互不阻塞？** 登记表的粒度是 key：`mu` 只在登记与清理的两瞬间被持有，fn 在锁外执行。步骤 2 里 4 个 key 的总耗时约等于单次耗时，正是锁外执行的直接证据。

相关的并发原语对照见 [08_syncmap_internals](../08_syncmap_internals/) 的三方基准。
