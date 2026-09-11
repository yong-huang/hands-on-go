# 07 · Token Bucket 令牌桶限流器：恒速补充、桶封顶、按需消费

> 限流是后端与面试的核心话题：API 网关、数据库保护、防爬策略都需要精确控制请求速率。令牌桶（Token Bucket）是最经典的限流算法，Nginx 的 `limit_req`、Go 标准库 `golang.org/x/time/rate` 都基于它。它的优势是**允许短时突发流量（burst），同时保证长期平均速率不超过阈值**。本实验用 Go 从零实现一个线程安全的令牌桶，并用 100 个并发请求实测验证。

## 1. 为什么需要它

固定窗口计数在窗口边界可能出现 2 倍突发，滑动窗口内存开销大；而很多场景（如开放 API）希望"平时匀速、短时能扛突发"。令牌桶用一个形象的模型同时解决这两点：系统以恒定速率往桶里放令牌，每个请求取走 1 个令牌才能通过——**桶里攒下的令牌就是突发余量，桶空了就拒绝**。

程序演示三件事：100 个 goroutine 并发抢一个桶、顺序请求观察令牌缓慢恢复、`-race` 验证并发安全。

## 2. 总览：核心机制一图看懂

![令牌桶数据流：恒速补充 → 桶封顶 → 请求取令牌](images/bucket.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/07_token_bucket/images/bucket.html)
> （或本地打开 [`images/bucket.html`](images/bucket.html)）。

**心智模型一句话**：时间以 `rate` 个/秒的速度往桶里加令牌，桶最多装 `burst` 个（多余的丢弃）；每个请求在锁内"先补充、再取 1 个"，取不到就拒绝。速率与容量两个参数定一切。

## 3. 快速开始

```bash
cd interview/07_token_bucket
go run token_bucket.go        # 并发演示 + 顺序演示
go run -race token_bucket.go  # 数据竞争检测（应无 WARNING）
```

并发演示预期输出（节选，goroutine 编号每次不同，但合计恒定）：

```text
Config: rate=10 tokens/sec, burst=20

  [goroutine 02] ALLOWED
  [goroutine 50] REJECTED (no tokens)
  ...
Results: 20 allowed, 80 rejected (total=100)

Remaining tokens: 0.00
```

顺序演示（rate=2/s、burst=5、每 200ms 一个请求，节选）：

```text
  Request  1: ALLOWED (tokens=4.00)
  Request  7: ALLOWED (tokens=0.42)
  Request  8: REJECTED (tokens=0.82)   ← 打印的是请求后余额，0.82 < 1 所以本轮被拒
  Request  9: ALLOWED (tokens=0.22)    ← 200ms 又补了 0.4 个，攒够 1 个再取
```

桶初始装满 20 个令牌，100 个 goroutine 几乎同时到达，恰好放行 20 个；之后每次请求间隔 200ms 只补充 `2 × 0.2 = 0.4` 个令牌，令牌数在 1 上下波动——放行与拒绝交替出现。

## 4. 核心概念

### 4.1 令牌桶模型与两个参数

| 要素 | 含义 | 类比 |
|------|------|------|
| `tokens` | 当前可用令牌数（`float64`，支持分数令牌） | 桶里的水 |
| `rate` | 每秒补充的令牌数 | 水龙头流速 |
| `burst` | 桶的最大容量 | 桶的容积 |
| `last` | 上次补充时刻 | 计时起点 |

核心公式：`tokens += rate × elapsed`，但不超过 `burst`。`rate` 决定长期平均速率，`burst` 决定突发容忍度。

### 4.2 Lazy 补充：不用定时器

实现里**没有**后台 goroutine 定时加水，而是在每次 `Allow()` 加锁后按 `now - last` 一次性补齐：

```go
func (tb *TokenBucket) refill() {
    now := time.Now()
    elapsed := now.Sub(tb.last).Seconds()
    if elapsed > 0 {
        tb.tokens += tb.rate * elapsed
        if tb.tokens > tb.burst {
            tb.tokens = tb.burst   // 桶满截断：闲置再久也只补到 burst
        }
        tb.last = now
    }
}
```

这是惰性计算思想：把"随时间的连续补充"推迟到"下一次请求时结算"。好处是无常驻线程、闲置零开销；代价是久置后首个请求会一次性结算到 `burst` 封顶（这正是突发的来源，不是 bug）。`elapsed > 0` 的判断防止时钟回拨/同刻重复结算。

### 4.3 Allow()：加锁 → 补充 → 取令牌

```go
func (tb *TokenBucket) Allow() bool {
    tb.mu.Lock()
    defer tb.mu.Unlock()
    tb.refill()           // 先按时间差补充
    if tb.tokens >= 1 {   // 有整令牌才放行
        tb.tokens--
        return true
    }
    return false          // 令牌不足，拒绝
}
```

### 4.4 并发安全：为什么必须加锁

不加锁时两个 goroutine 可能都读到 `tokens=1`、各自减 1，出现负余额——放行了 2 个请求却只扣了 1 个令牌。`Mutex` 保证 `refill() + 消费` 是原子的；`go run -race` 可验证无数据竞争。

## 5. 关键字段与坑清单

| 字段/设计 | 为什么这样写 |
|---|---|
| `tokens float64` | 支持分数令牌：rate=0.5 表示 2 秒攒 1 个；不足 1 时不能取 |
| `last time.Time` | 惰性补充的计时锚点；只在 elapsed>0 时推进，防重复结算 |
| `burst` 截断 | `min(tokens, burst)` 丢弃多余令牌，保证突发上限=burst 而非无限累积 |
| 初始装满 | `NewTokenBucket` 中 `tokens: burst`，允许冷启动即享满额突发 |
| `mu` 锁粒度 | 锁住"补充+判断+消费"整个临界区，而不是只锁 `tokens--` |

坑清单：

- 打印 `tokens` 在 `Allow()` 返回之后，看到的是**消费后余额**：`REJECTED (tokens=0.82)` 不是扣成了负数，而是余额不足 1 被拒
- `refill()` 不推进 `last`（elapsed≤0）时不要误判为"没补充"；这是防时钟回拨的保护
- 令牌桶允许突发 ≠ 无限突发：上限就是 `burst`，想更平滑就调小 burst 或改漏桶
- 锁只保护单进程内存态；多实例部署时每台各有独立的桶，需要分布式限流见 §7

## 6. 文件结构

```text
interview/07_token_bucket/
├── README.md                  # 本文件：原理 + 运行指引 + 深入要点
├── token_bucket.go            # 主教材：TokenBucket → refill/Allow → 并发与顺序演示
└── images/
    ├── bucket.dataflow.json   # 图源：令牌补充→桶→取令牌数据流（Typed JSON IR）
    ├── bucket.html            # 交互版：可缩放/高亮连线的自包含页面
    └── bucket.svg             # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：令牌桶为什么"懒补充"而不是起一个定时器？**
A：定时器方案有空闲开销、精度受 tick 限制，还有 goroutine 生命周期问题；lazy 方案在每次 `Allow()` 时按 `now-last` 一次性结算，无常驻线程、数学上精确，闲置零开销。`golang.org/x/time/rate` 用的就是同样的思路。

**Q：突发流量是怎么"冒出来"的？上限在哪？**
A：桶初始装满 burst 个；闲置期间令牌持续累积（封顶 burst）。突发到来时先消费存量，最多 burst 个请求立即通过，之后只能按 rate 补一个过一个。

**Q：令牌桶 vs 漏桶？**
A：令牌桶控制**平均速率、允许突发**（输入侧整形，输出速率取决于请求到达）；漏桶以**恒定速率**处理请求（输出侧平滑，不允许突发）。API 网关多用令牌桶，流量整形/削峰填谷用漏桶。

**Q：手写令牌桶最容易错什么？**
A：四个点——① 不加锁导致 `refill+消费` 非原子，余额变负；② 补充后忘了 `min(tokens, burst)` 截断，闲置后突发无上限；③ 补充无条件推进 `last`，时钟回拨或同刻多次调用会重复结算；④ `tokens` 用 int 导致低速率限流失真（应 float64，判 `>= 1` 才取）。

**Q：单机令牌桶怎么扩展到分布式？**
A：单机桶只保护一个进程，多实例各有独立配额。常见方案：Redis + Lua 脚本把"补充+判断+扣减"做成原子操作（或 INCR+EXPIRE 计数窗口）；或集中式 Token Server 统一发牌。代价是网络延迟影响精度、Redis 需高可用。

## 8. 总结

令牌桶的全部行为都能从两个参数推出来：`rate` 定长期平均速率，`burst` 定突发上限。实现上记住三条——惰性补充（不配定时器）、桶满截断、锁住"补充+消费"临界区。验证方法比背结论可靠：`go run token_bucket.go` 看 100 个并发请求恰好放行 burst 个。

下一篇 [08_worker_pool](../08_worker_pool/) 看限流的另一面：用固定数量 worker 消费任务 channel，从"拒绝多余的"换成"排队慢慢做"。
