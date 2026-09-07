# 18 · 滑动窗口限流：时间戳日志 + 窗口滑动淘汰 + 计数判定

> 限流是 API 网关和 Web 服务的保命机制。固定窗口实现最简单，却有一个致命弱点：窗口边界处可能放行 2 倍流量。本实验实现另一种经典算法——**滑动窗口（Sliding Window）**，用"每个 IP 一份时间戳日志"精确控制任意连续 1 秒内的请求数，并封装成标准 Go HTTP 中间件。跑一次 `go run`，你会亲眼看到第 4 个请求如何被 429 挡下、睡过窗口后又如何恢复。

## 1. 为什么需要它

固定窗口把时间切成一段段（如每 60s 一个计数器），两个窗口的交界处可以"两头各放 N 个"——59s 放 3 个、61s 又放 3 个，瞬时 6 个请求全部打到下游。滑动窗口的解法是**不切格子，逐请求记录时间戳**：每次请求先淘汰窗口外（早于 `now - window`）的旧时间戳，再数一数剩余数量是否达到阈值。这样"任意连续 window 内不超过 maxRequests"是精确成立的，没有临界突刺。

代价是内存从 O(1) 变成 O(窗口内请求数)——这是面试中最常被追问的权衡点（见 §7）。

## 2. 总览：核心机制一图看懂

![滑动窗口限流：窗口滑动淘汰与计数判定](images/limiter.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/18_rate_limiter/images/limiter.html)
> （或本地打开 [`images/limiter.html`](images/limiter.html)）。

**心智模型一句话**：每个 IP 一份有序时间戳日志，请求来了先"滑"掉过期尾巴、再数剩余——够数就 429，没够就记下此刻并放行。图中主路径从请求进入到放行/拒绝共 5 步，`allow` 之后的读写全部发生在 `rl.mu.Lock()` 临界区内。

## 3. 快速开始

```bash
cd interview/18_rate_limiter
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

4 组用例分别验证：单 IP 超限（1–3 放行、第 4 个起拒绝）、多 IP 独立配额（10.0.0.1/2/3 各自 3 次）、窗口过期恢复（睡 1s 后配额清零重来）、RemoteAddr 回退（无代理头时取 `1.2.3.4`）。

## 4. 核心概念

### 4.1 数据结构

```go
type RateLimiter struct {
    mu          sync.Mutex
    requests    map[string][]time.Time // IP → 有序时间戳日志
    window      time.Duration          // 窗口长度（1s）
    maxRequests int                    // 窗口内允许的最大请求数
}
```

注意本实现是**时间戳日志法**（sliding log）：精确但每个 IP 存 O(n) 个时间戳。它不是"时间轮/格子计数"（sliding window counter）——那是把窗口切成小格子做加权近似的另一种优化，精度换内存，见 §7 Q3。

### 4.2 Allow()：滑动淘汰 + 计数判定

```go
cutoff := now.Add(-rl.window)
timestamps := rl.requests[ip]
valid := timestamps[:0]            // 原地复用底层数组
for _, t := range timestamps {
    if t.After(cutoff) {           // 只保留窗口内的时间戳
        valid = append(valid, t)
    }
}
if len(valid) >= rl.maxRequests { return false } // 已满 → 429，且不记录
rl.requests[ip] = append(valid, now)             // 未满 → 记录并放行
```

三个关键设计：

- `valid := timestamps[:0]` 是 Go 惯用的 filter-in-place 技巧，淘汰过程零分配；
- **被拒绝的请求不追加时间戳**——否则持续打满的客户端永远无法恢复；
- 时间戳天然按追加有序，无需排序。

### 4.3 HTTP 中间件封装

`Middleware(next http.Handler) http.Handler` 是 Go 社区标准中间件签名：限流判定在 `next.ServeHTTP` 之前，被拒直接写 `429 Too Many Requests` 返回。多个中间件可洋葱式嵌套：`loggingMW(authMW(rateLimitMW(final)))`。

### 4.4 IP 提取优先级

`extractIP` 依次取 `X-Forwarded-For`（取第一段，即原始客户端）→ `X-Real-Ip` → `RemoteAddr`（去掉 `:port`）。在 Nginx/负载均衡后面，`RemoteAddr` 是代理 IP，必须依赖转发头；但转发头可伪造，直连场景应以 `RemoteAddr` 为准——信任边界是面试加分点。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `requests map[string][]time.Time` | IP → 时间戳日志 | 每个键独立配额；QPS 高时这是主要内存开销 |
| `window time.Duration` | 窗口长度 | `cutoff = now - window` 决定淘汰线 |
| `maxRequests int` | 窗口内上限 | 判定条件是 `>=`（满 3 即拒） |
| `timestamps[:0]` | 原地过滤 | 复用底层数组，但旧切片仍引用同一数组，别在外部长期持有 |
| `t.After(cutoff)` | 严格大于 | 恰好等于边界的旧时间戳会被淘汰，避免边界双计 |

坑清单：

- **忘了"拒绝不记账"**：把 `now` 先 append 再判断，会让被拒请求也占配额，客户端永远 429
- **并发安全**：`requests` map 的读和写都必须在 `mu` 临界区内，只锁写不锁读会 data race（用 `go test -race` 验证）
- **IP 提取被伪造**：直连时 `X-Forwarded-For` 客户端可随意设置，必须在可信代理层之后才采信
- **窗口过短 + 系统时钟跳变**：依赖 `time.Now()` 墙钟的实现受 NTP 调整影响，严肃场景考虑单调时钟语义
- **内存无上界**：恶意客户端用大量伪造 IP 可撑大 map，生产需配 LRU 淘汰冷 key 或分片锁

## 6. 文件结构

```text
interview/18_rate_limiter/
├── README.md                        # 本文件：原理 + 运行指引 + 面试要点
├── rate_limiter.go                  # 主教材：限流器核心 → 中间件 → extractIP → 4 组演示
└── images/
    ├── limiter.architecture.json    # 图源：滑动窗口机制架构图（Typed JSON IR）
    ├── limiter.html                 # 交互版：可缩放/高亮连线的自包含页面
    └── limiter.svg                  # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：滑动窗口 vs 固定窗口，临界突刺到底差在哪？**
A：固定窗口每个窗口一个计数器，边界两侧各放 N 个，瞬时可达 2N；滑动窗口按时间戳精确统计任意连续 window，任意时刻回看 1s 都不会超过 maxRequests。代价是内存 O(n)/key vs O(1)/key，以及每次请求 O(n) 的淘汰遍历。

**Q：滑动窗口和令牌桶怎么选？**
A：滑动窗口回答"过去 N 秒来了多少"，严格限制任意区间的总量，适合精确配额（API 计费限流）；令牌桶以恒定速率补充令牌、桶容量 b 允许 burst，适合流量整形（客户端突发友好）。滑动窗口内存 O(n)，令牌桶 O(1)。

**Q：滑动窗口内存太贵，怎么优化？**
A：① Sliding Window Counter——把窗口切成小格子（如 1s 切 10 格）只存每格计数，用 `当前格 + 上一格 × 权重` 近似，内存降到 O(格子数)；② Redis ZSET 存时间戳，`ZREMRANGEBYSCORE` 淘汰 + `ZCARD` 计数，配 Lua 保证原子。

**Q：手写这个限流器最容易错什么？**
A：三点——① 拒绝的请求不能记录时间戳，否则打满后永远无法恢复；② map 读写都要在同一把锁内，`go test -race` 可抓漏锁；③ `timestamps[:0]` 原地复用虽省分配，但要理解它共享底层数组，不能把旧切片再交给别人。

**Q：单机 map+Mutex 怎么演进到分布式限流？**
A：单机状态不共享，多实例总配额 = N × maxRequests。分布式方案：Redis ZSET + Lua（滑动日志）、`CL.THROTTLE`（RedisCell，令牌桶）、或集中 Token Server；要考虑网络延迟带来的精度损失与 Redis 故障时的降级策略（fail-open 还是 fail-closed）。

## 8. 总结

滑动窗口的全部行为都能从"时间戳日志 + 淘汰 + 计数"推出来：精确性来自逐请求记录，代价来自 O(n) 内存与遍历；中间件封装让限流与业务完全解耦。记住验证方法——`go run rate_limiter.go` 亲眼看着第 4 个请求变 429、睡过窗口后恢复 200，比背结论可靠得多。

下一篇 [19_task_scheduler](../19_task_scheduler/) 把限流之后的任务接住：优先级/延时队列、timer 触发与失败重试如何组装成一个任务调度器。
