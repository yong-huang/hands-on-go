# 14 · 分布式锁：互斥、TTL 与防误删的竞赛

> 多个服务实例同时扣库存、改同一条订单时，单机 `sync.Mutex` 帮不上忙——锁必须放在所有实例都能看到的地方。本篇用 Go 从零实现一把分布式锁，把最关键的三件事变成可运行的事实：**SET NX 互斥**、**TTL 自动过期防死锁**、**唯一 value/holder 校验防误删**，并还原一个经典事故场景——持锁方迟到后误删别人的锁。

## Background

单机程序里用 `sync.Mutex`（互斥锁）保护共享状态就够了。但服务是多实例部署时，锁状态存在各自的内存里互不可见——两台机器可以同时"拿到锁"，互斥形同虚设。

锁必须放到一个所有实例都能看到的共享裁决点（生产环境是 Redis、etcd、ZooKeeper 这类协调存储）。搬到共享存储后，新的挑战接踵而至：

1. **跨进程互斥**：多个客户端竞争同一把锁，只能有一个成功；
2. **持锁崩溃**：持有锁的进程崩溃后，锁若永远不释放，其他客户端全部卡死——需要自动过期兜底；
3. **误释放**：C1 的锁过期后被 C2 拿走，C1 迟到回来随手一删，删掉的是 C2 的锁——需要身份校验。

## What

**分布式锁**是一种跨进程的互斥机制：把锁状态存放在所有实例共享的裁决点，任一时刻同一 key 只允许一个持有者，用于协调不同机器上的服务对同一资源的访问。

心智模型一句话：分布式锁 = 互斥（NX）+ 兜底（TTL）+ 身份（唯一 value/holder），三者缺一不可。多个客户端向锁管理器竞争同一 key，`NX`（不存在才设置）让失败方立刻被拒，`lockEntry` 记录持有者与过期时间，cleanup 协程对应 Redis 的键过期机制。

可以把分布式锁想象成会议室门口的白板预约：想开会先写上名字（NX，写不上去就说明有人占着），每条预约带自动擦除时间（TTL——time to live，到期自动失效，开会的人消失后房间不会永久锁死），擦除前要先核对名字（唯一 value，防止擦掉别人的预约）。

但和白板不同的是：检查"有没有人写"和"写下自己名字"必须是不可分割的一步——分两步做，两个人可能同时看到空板、又同时写下。

## When to Use

这节回答"什么协作问题需要分布式锁，什么问题它管不了"。

典型场景：

- **多副本的定时任务**：结算、对账任务每小时跑一次，但服务有多个副本——用锁保证同一时刻只有一个副本执行。
- **跨实例的临界区保护**：多个实例并发扣同一批库存、改同一条订单，这段操作必须互斥。
- **持锁方随时可能崩溃**：流程较长时，锁必须能自动过期，否则一次崩溃永久卡死。

何时不用：

- 单机单进程：`sync.Mutex` 就够，共享存储反而引入故障点。
- 对正确性要求极高、不容任何窗口：分布式锁（包括 Redlock）仍存在 GC 停顿/时钟漂移导致的失效窗口，需要 fencing token（带序号的访问令牌：每次获锁序号递增，存储层拒绝旧序号的写入）或 etcd 租约这类更强机制（见 Pitfalls 的 Q）。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 单机 `sync.Mutex` | 只在单进程内有效 | 单机单进程 |
| 单节点 Redis 锁 | `SET NX PX` + Lua 释放，实现简单（本篇模拟的语义） | 常见互斥场景，容忍极小失效窗口 |
| Redlock | 向 N 个独立 Redis 节点申请、多数派成功才持有；缓解主从切换丢锁 | 只有 Redis 可用且要降低单点风险时 |
| etcd 租约 / ZooKeeper 临时节点 | 租约 + watch，语义更强 | 要求更强保证的协调场景 |

## Quick Start

前置条件：只需安装 Go。本篇不连接真实 Redis，用内存（`sync.Map` + `sync.Mutex`）模拟 Redis 语义——`Lock/TryLock/Unlock/cleanup` 四个操作与 `SET NX PX`、比对 value 后 `DEL`、键过期一一对应。

理解了模拟版，迁移到真实 Redis 只差一条命令。

```bash
cd patterns/14_dist_lock
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

三段输出对应三要素：第 1 段是互斥与释放，第 2 段 `DENIED` 是身份校验挡下误删，第 3 段是 TTL 到期后他人可获锁；末段是持锁状态下 TryLock 全部失败（非阻塞语义）。

一把锁的全部状态只有一个结构体：

```go
type lockEntry struct {
    holder    string    // 持有者标识（对应 Redis 锁的"唯一 value"）
    expiresAt time.Time // 过期时间（对应 SET 的 PX TTL）
}
```

这段在做什么：注册表是 `sync.Map`（key → `*lockEntry`），外加一把 `sync.Mutex` 保护「检查锁状态 → 设置锁」的 check-then-store 原子性——这正是 Redis 端 `SET key value NX PX ttl` 单命令原子性的模拟。

## How It Works

按三种操作的路径走：TryLock/Lock 获取 → Unlock 校验释放 → cleanup 清理过期条目。

### TryLock / Lock：非阻塞与阻塞两种获取

- `TryLock`：`mu` 保护下单次检查——锁存在且未过期就返回 false，否则写入新条目返回 true，**立即返回不等待**；
- `Lock`：循环执行与 TryLock 相同的检查，失败则 `Sleep(10ms)` 后重试，直到锁空闲或**已过期可抢占**。

你在输出里看到的 `service-B TryLock failed (lock held by service-A)`，就是 TryLock 单次检查返回 false 的路径。

### Unlock：holder 校验防误删

```go
if entry.holder != holder {
    return false // 持有者不匹配，拒绝释放
}
dl.locks.Delete(key)
```

这段在做什么：这就是 Redis 锁"比对唯一 value 再 DEL"的模拟版——真实 Redis 里必须用 Lua 脚本把 GET 比对与 DEL 做成原子操作，否则检查与删除之间锁仍可能转手。

输出里 `service-B tried to unlock -> DENIED (not holder)` 就来自这个分支。

### 误删事故的时间线

用时间线还原一次误删事故（C1、C2 为两个客户端）：

1. 段①：C1 持锁但业务超时（模拟 GC 停顿/网络抖动，超过 TTL）；
2. 段②：TTL 到期锁自动释放，C2 用自己的 value 接管；
3. 段③：C1 迟到——不校验直接 `DEL` 就删掉了 C2 的锁（互斥被破坏），改进方案是先比对 value 不匹配就拒绝。

Demo 3 复现了时间线的段②："过期后他人可获锁"——TTL=500ms，等 600ms 后 service-B 成功获取。

### 关键字段一览

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `holder` | 持有者唯一标识 | 解锁前校验身份，防止删掉别人的锁 |
| `expiresAt` | 过期时间点 | 持锁方崩溃后锁自动可抢占，防死锁的最后防线 |
| `mu sync.Mutex` | 保护单 key 原子检查-设置 | 模拟 `SET NX` 的原子性；没有它两个客户端可同时获锁 |
| `Lock` 的 10ms 轮询 | 阻塞获取的重试间隔 | 简单但有延迟；生产可用 pub/sub 或 Redisson 式 watch |
| `cleanup`（1s ticker） | 后台清理过期条目 | 对应 Redis 键过期；不清理则过期锁一直占内存 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **SETNX + EXPIRE 两步不原子**：现象是中间崩溃后锁永不过期，其他客户端全部卡死。原因：两步之间进程死亡，EXPIRE 没执行。解法：必须用 `SET key value NX PX ttl` 一条命令。
- **unlock 不校验 value**：现象是误删他人锁、互斥失效。原因：直接 `DEL` 不看持有者。解法：Lua 脚本原子比对 + DEL（见上文 Unlock 小节）。
- **业务时长超过 TTL**：现象是锁提前易主，两个客户端同时认为持锁。原因：业务跑得比 TTL 慢。解法：watchdog 后台续租，或把 TTL 设得足够大并接受失败重试。
- **依赖系统时钟**：现象是时钟回拨后过期判断错乱。原因：`time.Now().After(expiresAt)` 依赖本地时钟，Redis 侧同样有这个问题。解法：没有完美解，需接受这一失效窗口或升级到 fencing token 这类带序号的机制。
- **轮询式 Lock 效率低**：现象是高竞争下等待延迟高、最坏 O(n) 次轮询。原因：每 10ms 盲目重试。解法：本实现为教学取舍；生产可用 pub/sub 或 Redisson 式 watch。

**Q：Redis 分布式锁为什么 NX 和 TTL 缺一不可？**
A：NX（不存在才设置）提供互斥——两个客户端只有一个能成功；TTL 提供兜底——持锁方崩溃后锁自动释放，否则死锁。反过来看：只有 NX 没有 TTL，进程崩溃锁成死锁；只有 TTL 没有 NX，所有人都写得上，毫无互斥。

**Q：锁过期后持锁方迟到了，会发生什么？**
A：一句话——C2 获锁后，C1 若直接 DEL 会删掉 C2 的锁，导致 C2、C3 同时认为自己持锁；防法是加锁时写入唯一 value（如 UUID），解锁用 Lua 原子执行"GET 比对相同才 DEL"。时间线还原见上文"误删事故的时间线"。

**Q：业务执行时间不确定，TTL 该怎么办？**
A：watchdog 续租——后台协程在锁快过期时延长 TTL（Redisson 的做法），业务完成后停止续租并释放。注意续租也要用 Lua 原子校验 value。

**Q：Redlock 是什么？和单节点锁比强在哪？**
A：向 N 个独立 Redis 节点同时发 `SET NX PX`，拿到 ≥N/2+1 个才算成功，释放时向所有节点 DEL。

它缓解单节点主从切换丢锁的问题（锁刚写进 master 还没同步就 failover）；但仍不解决 GC 停顿/时钟漂移，更强保证要靠 fencing token 或 etcd 租约。

**Q：本实现和生产环境的差距？**
A：本实现是单机内存模拟——进程崩溃所有锁丢失、无网络分区、无持久化；`Lock` 是 10ms 轮询而非通知。

生产应使用 Redis（`SET NX PX` + Lua 释放）、etcd（租约 + watch）或 ZooKeeper（临时顺序节点），并考虑 fencing token。
