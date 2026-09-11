# 14 · 分布式锁：互斥、TTL 与防误删的竞赛

> 多个服务实例同时扣库存、改同一条订单时，单机 `sync.Mutex` 帮不上忙——锁必须放在所有实例都能看到的地方。本实验用 Go 从零实现一把分布式锁，把面试必考的三件事变成可运行的事实：**SET NX 互斥**、**TTL 自动过期防死锁**、**唯一 value/holder 校验防误删**，并演示最经典的翻车现场——持锁方迟到后误删别人的锁。

## 1. 为什么需要它

分布式锁面临的三个核心挑战：

1. **跨进程互斥**：不同机器上的服务无法共享内存锁，锁状态必须存到一个共享的裁决点（生产环境是 Redis/etcd/ZooKeeper）
2. **持锁崩溃**：持有锁的进程崩溃后锁永远不释放 → 需要 **TTL 自动过期** 兜底
3. **误释放**：C1 的锁过期后被 C2 拿走，C1 迟到回来一挥手把 C2 的锁删了 → 需要 **holder/唯一 value 校验**

本实验用内存（`sync.Map` + `sync.Mutex`）**模拟 Redis 语义**——没有连接真实 Redis，但 `Lock/TryLock/Unlock/cleanup` 四个操作与 `SET NX PX` / 比对 value 后 `DEL` / 键过期一一对应。理解了模拟版，迁移到真实 Redis 只差一条命令。

## 2. 总览：核心机制一图看懂

![分布式锁架构：多客户端竞争一把锁（模拟 Redis 语义）](images/lock.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/14_dist_lock/images/lock.html)
> （或本地打开 [`images/lock.html`](images/lock.html)）。

**心智模型一句话**：分布式锁 = 互斥（NX）+ 兜底（TTL）+ 身份（唯一 value/holder），三者缺一不可。图中多个客户端向锁管理器竞争同一 key，`NX` 让失败方立刻被拒，`lockEntry` 记录持有者与过期时间，cleanup 协程对应 Redis 的键过期机制。

## 3. 快速开始

```bash
cd interview/14_dist_lock
go run dist_lock.go
```

预期输出（节选；Demo 4 多 goroutine 竞争的获得顺序每次运行不同，故未摘录）：

```text
1. Basic Lock/Unlock:
   service-A acquired lock (TTL=3s)
   service-B TryLock failed (lock held by service-A)
   service-A released lock
   service-B acquired lock after release

2. Holder Verification:
   service-B tried to unlock -> DENIED (not holder)
   service-A unlocked -> OK (is holder)

3. TTL Auto-Expiry:
   service-A acquired lock (TTL=500ms)
   (waited 600ms, lock should be expired)
   service-B acquired lock (auto-expired)

5. TryLock Non-Blocking Mode:
   3 TryLock attempts: 0 succeeded, 3 failed
```

## 4. 核心概念

### 4.1 锁条目 lockEntry：一把锁的全部状态

```go
type lockEntry struct {
    holder    string    // 持有者标识（对应 Redis 锁的"唯一 value"）
    expiresAt time.Time // 过期时间（对应 SET 的 PX TTL）
}
```

注册表是 `sync.Map`（key → `*lockEntry`），外加一把 `sync.Mutex` 保护「检查锁状态 → 设置锁」的 check-then-store 原子性——这正是 Redis 端 `SET key value NX PX ttl` 单命令原子性的模拟。

### 4.2 TryLock / Lock：非阻塞与阻塞两种获取

- `TryLock`：`mu` 保护下单次检查——锁存在且未过期就返回 false，否则写入新条目返回 true，**立即返回不等待**
- `Lock`：循环执行与 TryLock 相同的检查，失败则 `Sleep(10ms)` 后重试，直到锁空闲或**已过期可抢占**

### 4.3 Unlock：holder 校验防误删

```go
if entry.holder != holder {
    return false // 持有者不匹配，拒绝释放
}
dl.locks.Delete(key)
```

这就是 Redis 锁"比对唯一 value 再 DEL"的模拟版——真实 Redis 里必须用 Lua 脚本把 GET 比对与 DEL 做成原子操作，否则检查与删除之间锁仍可能转手。

### 4.4 竞争与过期：误删事故的时间线

![竞争时间线：TTL 过期后迟到误删 vs value 校验（模拟 Redis 语义）](images/competition.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/14_dist_lock/images/competition.html)
> （或本地打开 [`images/competition.html`](images/competition.html)）。

怎么看：段① C1 持锁但业务超时（模拟 GC 停顿/网络抖动，超过 TTL）；段② TTL 到期锁自动释放，C2 用自己的 value 接管；段③ C1 迟到——不校验直接 `DEL` 就删掉了 C2 的锁（互斥被破坏），改进方案是先比对 value 不匹配就拒绝。Demo 3 复现了"过期后他人可获锁"：TTL=500ms，等 600ms 后 service-B 成功获取。

## 5. 关键字段与坑清单

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `holder` | 持有者唯一标识 | 解锁前校验身份，防止删掉别人的锁 |
| `expiresAt` | 过期时间点 | 持锁方崩溃后锁自动可抢占，防死锁的最后防线 |
| `mu sync.Mutex` | 保护单 key 原子检查-设置 | 模拟 `SET NX` 的原子性；没有它两个客户端可同时获锁 |
| `Lock` 的 10ms 轮询 | 阻塞获取的重试间隔 | 简单但有延迟；生产可用 pub/sub 或 Redisson 式 watch |
| `cleanup`（1s ticker） | 后台清理过期条目 | 对应 Redis 键过期；不清理则过期锁一直占内存 |

坑清单：

- **SETNX + EXPIRE 两步不原子**：中间崩溃锁永不过期 → 必须用 `SET key value NX PX ttl` 一条命令
- **unlock 不校验 value**：误删他人锁，互斥失效 → Lua 脚本原子比对 + DEL
- **业务时长超过 TTL**：锁提前易主 → watchdog 后台续租，或把 TTL 设得足够大并接受失败重试
- **依赖系统时钟**：时钟回拨会影响 `time.Now().After(expiresAt)` 判断；Redis 侧同样有这个问题
- **轮询式 Lock 效率低**：最坏 O(n) 次轮询；本实现为教学取舍

## 6. 文件结构

```text
interview/14_dist_lock/
├── README.md                        # 本文件：原理 + 运行指引 + 深入要点
├── dist_lock.go                     # 主教材：锁条目 → Lock/TryLock/Unlock/cleanup → 5 个 Demo
└── images/
    ├── lock.architecture.json       # 图源：锁架构与 NX/TTL/value 三要素（Typed JSON IR）
    ├── lock.html                    # 交互版：可缩放/高亮连线的自包含页面
    ├── lock.svg                     # README 内嵌双主题矢量图
    ├── competition.sequence.json    # 图源：TTL 过期 → 误删 vs value 校验时间线
    ├── competition.html             # 交互版
    └── competition.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：Redis 分布式锁为什么 NX 和 TTL 缺一不可？**
A：NX（不存在才设置）提供互斥——两个客户端只有一个能成功；TTL 提供兜底——持锁方崩溃后锁自动释放，否则死锁。反过来看：只有 NX 没有 TTL，进程崩溃锁成死锁；只有 TTL 没有 NX，所有人都写得上，毫无互斥。

**Q：锁过期后持锁方迟到了，会发生什么？怎么防误删？**
A：C1 的锁过期，C2 获锁；C1 恢复后若直接 DEL 会删掉 C2 的锁，导致 C2、C3 同时认为自己持锁。防法：加锁时写入唯一 value（如 UUID），解锁用 Lua 脚本原子执行"GET 比对相同才 DEL"。本实验的 `holder` 字段就是这个语义（Demo 2：service-B 解锁被 DENIED）。

**Q：业务执行时间不确定，TTL 该怎么办？**
A：watchdog 续租——后台协程在锁快过期时延长 TTL（Redisson 的做法），业务完成后停止续租并释放。注意续租也要用 Lua 原子校验 value。

**Q：Redlock 是什么？和单节点锁比强在哪？**
A：向 N 个独立 Redis 节点同时发 `SET NX PX`，拿到 ≥N/2+1 个才算成功，释放时向所有节点 DEL。它缓解单节点主从切换丢锁的问题（锁刚写进 master 还没同步就 failover）；但仍不解决 GC 停顿/时钟漂移，更强保证要靠 fencing token 或 etcd 租约。

**Q：本实现和生产环境的差距？**
A：本实现是单机内存模拟——进程崩溃所有锁丢失、无网络分区、无持久化；`Lock` 是 10ms 轮询而非通知。生产应使用 Redis（`SET NX PX` + Lua 释放）、etcd（租约 + watch）或 ZooKeeper（临时顺序节点），并考虑 fencing token。

## 8. 总结

分布式锁的全部行为都能从三要素推出来：NX 给互斥，TTL 给兜底，唯一 value 给身份。本实验用 `go run dist_lock.go` 把每一环都跑给你看：竞争被拒绝、崩溃被过期救回、误删被 holder 校验挡下。记住验证方法——把 Demo 3 的 500ms 改小、把 Unlock 的校验删掉，亲眼看互斥怎么被破坏，比背结论可靠得多。

下一篇 [15_http_router](../15_http_router/) 看另一个面试高频件：HTTP 路由器如何用 radix 树在静态、参数、通配路由之间做匹配。
