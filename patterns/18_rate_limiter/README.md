# 18 · 滑动窗口限流：时间戳日志 + 窗口滑动淘汰 + 计数判定

> 限流（对单位时间内的请求数设上限，超出的请求直接拒绝）是 API 网关和 Web 服务保护下游的核心手段。固定窗口实现最简单，却有一个致命弱点：窗口边界处可能放行 2 倍流量。本实验实现另一种经典算法——**滑动窗口（Sliding Window）**，用"每个 IP 一份时间戳日志"精确控制任意连续 1 秒内的请求数，并封装成标准 Go HTTP 中间件。跑一次 `go run`，可以看到第 4 个请求如何被 429 挡下、睡过窗口后又如何恢复。

## Background

后端服务随时可能被突发流量或恶意客户端打挂，需要在入口对请求数设上限。最简单的做法是固定窗口：把时间切成一段段（如每 60s 一个计数器），数每段的请求个数。但它有两个窗口的交界处可以"两头各放 N 个"的缺陷——59s 放 3 个、61s 又放 3 个，瞬时 6 个请求全部打到下游，这个瞬时超额叫临界突刺。

滑动窗口（Sliding Window）为此而来：不切格子，改为逐请求记录时间戳，让"任意连续 window 内不超过 maxRequests"精确成立，没有突刺。

精确是有代价的：内存从固定窗口的 O(1) 计数器变成 O(窗口内请求数) 的时间戳日志——这是选型时最常被追问的权衡点。

## What

**滑动窗口限流器**是一种用时间戳日志实现精确限流的组件：为每个客户端（按 IP 区分）维护一份有序时间戳日志，请求到来时先淘汰窗口外的旧时间戳、再计数判定，超过上限就返回 429（HTTP 状态码，Too Many Requests）。

心智模型一句话：每个客户端一份有序时间戳日志，请求来了先"滑"掉过期尾巴、再数剩余——够数就 429，没够就记下此刻并放行。判定之后对日志的读写全部发生在 `rl.mu.Lock()` 临界区内（临界区——同一时刻只允许一个 goroutine 执行的代码段）。

可以把限流器想象成游乐园的检票员：每个游客带一本时间戳票据，检票员先翻掉一小时前已作废的旧票，再数剩下的有效票——够了就拦下，不够就盖上当前时刻放行。但和检票员不同的是：限流器要同时服务成千上万名游客（并发），且被拦下的请求不留下任何票据——否则打满的客户端永远恢复不了。

## When to Use

这节回答"哪些限流需求适合滑动窗口日志，哪些该换算法"。

典型场景：

- **API 计费与合同配额**：条款写明"每秒最多 N 次"时，需要任意连续窗口内都不超——固定窗口的临界突刺在计费场景就是违约。
- **保护下游不被突发流量打挂**：在 Web 服务入口用中间件（包装 handler、在业务逻辑前执行的函数）把超额请求挡在业务代码之前。
- **多客户端配额隔离**：每个 IP 独立一份时间戳日志，客户端之间互不挤占。

何时不用：

- 业务需要突发友好（允许短时 burst，瞬时超量）时选令牌桶：滑动窗口严格卡总量，不允许任何突刺。
- 内存敏感或 QPS（每秒请求数）极高时：每 IP O(n) 的时间戳开销大，选格子近似或令牌桶。
- 多实例分布式部署时：单机 `map + Mutex` 状态不共享，总配额 = 实例数 × maxRequests，需要中心化方案。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 固定窗口计数器 | O(1) 内存最简，但窗口边界瞬时可达 2N | 精度要求低、实现成本优先 |
| 滑动窗口日志（本篇） | 任意连续窗口精确，内存 O(n)/IP | 计费配额等需要精确上限的场景 |
| Sliding Window Counter | 把窗口切成小格子做加权近似，内存 O(格子数) | 想要接近滑动的精度又想省内存 |
| 令牌桶 | 恒定速率补充令牌、桶容量 b 允许 burst，O(1) | 流量整形、对客户端突发友好 |
| Redis ZSET（有序集合，按分数排序的成员集合）滑动日志 | 中心化存储，多实例共享配额 | 分布式部署、统一限流 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/18_rate_limiter
go run rate_limiter.go
```

预期输出（节选，时间戳随运行时刻变化）：

```text
=== Sliding Window Rate Limiter Middleware ===

Config: window=1s, maxRequests=3

--- Test 1: Single IP (192.168.1.1) sends 5 requests ---

  Request 1: [200] OK — request from 192.168.1.1
  Request 2: [200] OK — request from 192.168.1.1
  Request 3: [200] OK — request from 192.168.1.1
  Request 4: [429] 429 Too Many Requests — rate limit exceeded for 192.168.1.1 (BLOCKED)
  Request 5: [429] 429 Too Many Requests — rate limit exceeded for 192.168.1.1 (BLOCKED)
  ...

--- Test 3: Window expiry (172.16.0.1) ---

  Before sleep — Request 4: [429] 429 Too Many Requests — ... (expected 429)

  Sleeping 1s to let window expire...
  After sleep  — Request 5: [200] OK — request from 172.16.0.1 (window expired, should be 200)
```

4 组用例分别验证：

- 单 IP 超限：1–3 放行、第 4 个起拒绝；
- 多 IP 独立配额：10.0.0.1/2/3 各自 3 次；
- 窗口过期恢复：睡 1s 后配额清零重来；
- RemoteAddr 回退：无代理头时取 `1.2.3.4`。

## How It Works

按一次请求的判定路径走：取 IP → 淘汰过期时间戳 → 计数判定 → 记录或拒绝。

### 数据结构

```go
type RateLimiter struct {
    mu          sync.Mutex
    requests    map[string][]time.Time // IP → 有序时间戳日志
    window      time.Duration          // 窗口长度（1s）
    maxRequests int                    // 窗口内允许的最大请求数
}
```

注意本实现是**时间戳日志法**（sliding log）：精确但每个 IP 存 O(n) 个时间戳。它不是"时间轮/格子计数"（sliding window counter）——那是把窗口切成小格子做加权近似的另一种优化，精度换内存，见文末 Q&A。

### Allow()：滑动淘汰 + 计数判定

```go
	cutoff := now.Add(-rl.window)

	// Remove timestamps outside the current sliding window
	timestamps := rl.requests[ip]
	valid := timestamps[:0] // reuse underlying array
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.requests[ip] = valid

	// Check if within limit
	if len(valid) >= rl.maxRequests {
		return false
	}

	// Record this request
	rl.requests[ip] = append(valid, now)
	return true
```

三个关键设计：

- `valid := timestamps[:0]` 是 Go 惯用的 filter-in-place 技巧，淘汰过程零分配；
- **被拒绝的请求不追加时间戳**——否则持续打满的客户端永远无法恢复；
- 时间戳天然按追加有序，无需排序。

你在输出里看到的 Request 4 变 429，就来自 `len(valid) >= rl.maxRequests` 的判定；睡 1s 后恢复 200，则是因为旧时间戳全部落到窗口外、被淘汰清零。

### HTTP 中间件封装

`Middleware(next http.Handler) http.Handler` 是 Go 社区标准中间件签名：限流判定在 `next.ServeHTTP` 之前，被拒直接写 `429 Too Many Requests` 返回。

多个中间件可洋葱式嵌套：`loggingMW(authMW(rateLimitMW(final)))`。

### IP 提取优先级

`extractIP` 依次取 `X-Forwarded-For`（取第一段，即原始客户端）→ `X-Real-Ip` → `RemoteAddr`（去掉 `:port`）。

在 Nginx/负载均衡后面，`RemoteAddr` 是代理 IP，必须依赖转发头；但转发头可伪造，直连场景应以 `RemoteAddr` 为准——信任边界是容易被忽略的细节。

### 字段速查

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `requests map[string][]time.Time` | IP → 时间戳日志 | 每个键独立配额；QPS 高时这是主要内存开销 |
| `window time.Duration` | 窗口长度 | `cutoff = now - window` 决定淘汰线 |
| `maxRequests int` | 窗口内上限 | 判定条件是 `>=`（满 3 即拒） |
| `timestamps[:0]` | 原地过滤 | 复用底层数组，但旧切片仍引用同一数组，别在外部长期持有 |
| `t.After(cutoff)` | 严格大于 | 恰好等于边界的旧时间戳会被淘汰，避免边界双计 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **忘了"拒绝不记账"**：现象是客户端打满后永远 429。原因：把 `now` 先 append 再判断，被拒请求也占配额。解法：先判定、后记录（见上文 Allow 的关键设计）。
- **只锁写不锁读**：现象是 `go test -race` 报 data race（数据竞争——多个 goroutine 并发访问同一变量且至少一个是写）。原因：`requests` map 的读和写都必须在 `mu` 临界区内。解法：读写同一把锁，用 `-race` 验证。
- **IP 提取被伪造**：现象是限流对恶意客户端失效。原因：直连时 `X-Forwarded-For` 客户端可随意设置。解法：只在可信代理层之后采信转发头，直连以 `RemoteAddr` 为准。
- **窗口过短 + 系统时钟跳变**：现象是 NTP（网络时间协议，用于校准系统时钟）调整后限流判定抖动。原因：依赖 `time.Now()` 墙钟。解法：严肃场景考虑单调时钟语义。
- **内存无上界**：现象是 map 被大量伪造 IP 撑大。原因：每个 IP 一份日志且无淘汰机制。解法：生产需配 LRU（最近最少使用——最久未访问的先淘汰）淘汰冷 key 或分片锁。

**Q：滑动窗口 vs 固定窗口，临界突刺到底差在哪？**
A：固定窗口每个窗口一个计数器，边界两侧各放 N 个，瞬时可达 2N；滑动窗口按时间戳精确统计任意连续 window，任意时刻回看 1s 都不会超过 maxRequests。

代价是内存 O(n)/key vs O(1)/key，以及每次请求 O(n) 的淘汰遍历。

**Q：滑动窗口和令牌桶怎么选？**
A：滑动窗口回答"过去 N 秒来了多少"，严格限制任意区间的总量，适合精确配额（API 计费限流）；令牌桶以恒定速率补充令牌、桶容量 b 允许 burst，适合流量整形（客户端突发友好）。滑动窗口内存 O(n)，令牌桶 O(1)。

**Q：滑动窗口内存太贵，怎么优化？**
A：① Sliding Window Counter——把窗口切成小格子（如 1s 切 10 格）只存每格计数，用 `当前格 + 上一格 × 权重` 近似，内存降到 O(格子数)。

② Redis ZSET 存时间戳，`ZREMRANGEBYSCORE` 淘汰 + `ZCARD` 计数，配 Lua 脚本（在 Redis 服务端原子执行多条命令）保证原子。

**Q：单机 map+Mutex 怎么演进到分布式限流？**
A：单机状态不共享，多实例总配额 = N × maxRequests。

分布式方案：Redis ZSET + Lua（滑动日志）、`CL.THROTTLE`（RedisCell，令牌桶）、或集中 Token Server。

要考虑网络延迟带来的精度损失与 Redis 故障时的降级策略（fail-open：Redis 故障时放行全部请求；fail-closed：故障时拒绝全部请求）。
