# 10 · Shard Map：分片锁并发安全 Map

> Go 的原生 map 不是并发安全的，并发读写会直接 `fatal error`（不可 recover）。保护它的手段有全局锁、`sync.RWMutex`、标准库 `sync.Map`，以及本篇的主角——分片锁 ShardMap：把一个大 map 拆成 N 个小 map，每个 shard 独立加锁，写并行度上限就是 shard 数。本篇实现一个完整的 ShardMap，并用真实基准回答三个工程问题：分片数怎么选？跨 key 操作怎么办？和 `sync.Map` 谁快？

## Background

在 map 里存共享状态，最省事的做法是不加任何保护。代价在并发那一刻出现：并发读写原生 map 会触发 `fatal error: concurrent map writes`——这是 Go 运行时的致命错误，不走 panic 通道，`recover`（捕获 panic 的恢复机制）救不回来，进程直接死。

保守的补救是 `sync.Mutex`（互斥锁）：全局一把锁，任何读写都先拿锁。低并发下够用；8 个 goroutine 一起写时，同一时刻只有 1 个能动手，锁竞争成为瓶颈。

`sync.RWMutex`（读写锁，读与读共享、写独占）改善了读，但写仍然独占全局。要写也并行，只能换思路：把锁的管辖范围缩小——把数据切成几块，每块配一把锁。具体怎么切、怎么路由，见下一节。

## What

**ShardMap（分片锁 Map）**是一种并发安全的 map 实现：把整个 key 空间拆成 N 个独立加锁的小 map（shard，分片），每次操作先算出 key 属于哪个 shard，再只锁那一个 shard。

心智模型一句话：每个 key 先过 FNV-32a（一种非加密哈希函数，计算快、分布均匀）哈希再对 shard 数取模（求余数），落进一个自带 `RWMutex` 的小 map；不同 shard 的操作完全并行，同 shard 内读走 `RLock` 共享、写走 `Lock` 独占。

可以把 ShardMap 想象成一家多窗口银行：客户按卡号尾数固定分到某个窗口（路由），每个窗口一条自己的队伍（锁），不同窗口互不干扰。但和银行窗口不同的是：同一个客户永远去同一个窗口（key 到 shard 的映射是固定的），而且没有统一收银台——跨窗口的业务（多 key 事务）没有原子支持。

核心思想是**缩小锁粒度**：8 个 goroutine 用全局锁同一时刻只有 1 个能写；拆成 4 个 shard 后最多 4 个写并行。这是经典的空间换时间——但本篇实测会看到，它并不是免费的（见 How It Works 的数据视角）。

## When to Use

这节回答"什么负载值得分片，什么负载用不上分片"。

典型场景：

- **高并发读写的共享表**：网关的连接表、限流计数器——多个 goroutine 频繁对不同 key 做 Set/Get，全局锁会形成排队点。
- **需要 `Len()` 或类型安全**：ShardMap 的 `Len()` 是 O(shardCount) 次加锁而非 O(n) 遍历，且可泛型化成 `ShardMap[K comparable, V any]`。
- **key 高频增删**：`sync.Map` 内部分 read（只读表）与 dirty（新建草案表）两张 map，key 变动积累后要把 dirty 整体重建成新 read，代价与 key 数成正比——高频增删时这笔开销反复发生，分片结构的写路径只锁一个 shard。

何时不用：

- 低并发、逻辑简单：`sync.Mutex` 一把锁最省事。
- 读多写少、key 稳定（如缓存）：这正是 `sync.Map` 的官方定位，无锁读路径更快。
- 只有一个 goroutine 访问：原生 map 即可，无需任何锁。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| `sync.Mutex` | 全局一把锁；读、写并发都没有 | 低并发、逻辑简单 |
| `sync.RWMutex` | 全局读写锁；读共享、写独占 | 读多写少、写并发低 |
| **ShardMap** | 每 shard 一把锁；同 shard 读共享、跨 shard 写并行 | 高并发读写、可控 key 分布 |
| `sync.Map` | 内部 read/dirty 双 map；读并发高、写并发中 | key 稳定、读多写少（如缓存） |

## Quick Start

前置条件：运行 demo 只需安装 Go；复算 viz.py 的实测曲线需 Python 3 与 matplotlib、numpy（pip install matplotlib numpy）。

```bash
cd patterns/10_shard_map
go run shard_map.go     # 基本 API demo + 并发基准 + 分片路由分布
python3 viz.py          # 实测分片数-吞吐曲线的数据处理脚本
```

预期输出（节选，耗时与倍数每次运行不同）：

```text
ShardMap: 4 shards created
Get("name") = gopher (ok=true)
Len() = 3
After Delete("version"): Len() = 2
--- Concurrent Benchmark ---
ShardMap:  8 goroutines x 100000 ops = 107.911167ms
sync.Map:  8 goroutines x 100000 ops = 47.098292ms
sync.Map is 2.3x faster than ShardMap
--- Shard Distribution ---
  key=alpha    -> shard[3]
  key=beta     -> shard[3]
  key=gamma    -> shard[2]
```

读这份输出有一个要点：基准部分 **sync.Map 反而更快**——这份混合负载（Set+Get+Delete 各一次）下，本机 18 核上约快 1.8~2.3 倍。ShardMap 的收益要看负载形状，别预设"分片一定更快"。

ShardMap 的骨架只有两个结构体：

```go
type shard struct {
    mu   sync.RWMutex
    data map[string]interface{}
}

type ShardMap struct {
    shards     []shard
    shardCount uint32
}
```

这段在做什么：每个 shard 自带锁和 map，`ShardMap` 本身没有任何锁——锁全部下沉到 shard。

## How It Works

按一次操作的路径走：key → 哈希取模路由 → 目标 shard 加锁 → 读写 → 解锁。

### 路由：FNV-32a + 取模

```go
func (sm *ShardMap) getShard(key string) *shard {
    h := fnv.New32a()
    h.Write([]byte(key))
    idx := h.Sum32() % sm.shardCount
    return &sm.shards[idx]
}
```

这段在做什么：把 key 映射到某个 shard 的唯一入口。选 FNV-32a 而非 MD5/SHA：非加密哈希计算极快，雪崩效应（输入微变导致输出大幅分散）足够，32 位空间对"几十个 shard"绰绰有余。两个 key 落同一 shard 不影响正确性，只是竞争同一把锁。

demo 的完整路由（N=4）：alpha→3、beta→3、gamma→2、delta→1、epsilon→1、zeta→3。你在输出里看到的 `key=alpha -> shard[3]` 等三行，就来自这里。

### 读写与遍历的锁语义

`Get` 用 `RLock`（同 shard 多读并发），`Set`/`Delete` 用 `Lock`（独占该 shard），不同 shard 之间零互相影响。

两个聚合操作的代价要注意：`Range` 逐 shard 加读锁遍历，**不提供快照一致性**（遍历看到的不是某一时刻的完整副本）；`Len()` 逐 shard 取 `len()` 后求和，代价是 O(shardCount) 次加锁，不是 O(1)。

### 数据视角：实测扩展性与"不如 sync.Map"的真相

8 goroutine 混合负载（Set+Get，每 10 次 1 次 Delete）、每档 5 次取中位数的实测结论：

- shard 数从 1 → 64，吞吐从 9.6M → 38.3M ops/s 持续上升（18 核），但仍明显次线性——shard=8 时只有 18.4M，远没到 8 倍
- 同一负载下 `sync.Map`（54.9M）> ShardMap 32 分片（31.0M）> shard=1（9.6M）
- 一个反直觉的细节：**shard=1 的 ShardMap（9.6M）比一把裸 `sync.Mutex`（11.4M）还慢**——RWMutex 比 Mutex 更重，分片救不了"只有一个 shard"的退化设计

次线性的原因：瓶颈除了锁竞争，还有 hash 计算、内存带宽和调度。本实验 key 每次都不同、写密集，恰好是 `sync.Map` 的 read-only 优化失效的场景，但它的无锁读路径仍然占优；ShardMap 的翻身仗在需要 `Len()`、类型安全（可泛型化）或更大 key 空间时。

`python3 viz.py` 可复算这组实测曲线（实测值，非推算）。

### 关键字段一览

| 字段/方法 | 含义 | 为什么重要 |
|---|---|---|
| `shard.mu RWMutex` | 每 shard 独立锁 | 锁粒度 = 竞争面；绝不能提成 ShardMap 级字段 |
| `shardCount uint32` | 分片数，取模的模数 | 决定写并行度上限；`NewShardMap(≤0)` 兜底为 1 |
| `getShard` | key → shard 的唯一入口 | 所有读写必须先路由，漏路由 = 数据竞争 |
| `Range(fn)` | 逐 shard 读锁遍历 | 无快照一致性；fn 返回 false 提前终止 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **并发读写原生 map**：现象是 `fatal error: concurrent map writes`，进程直接死，`defer recover` 无效。原因：运行时致命错误不走 panic 通道。解法：换并发安全结构——这正是本篇的出发点。
- **shard 数太少退化**：现象是 shard=1 时比裸 `sync.Mutex` 还慢。原因：等价于一把 `RWMutex`，还额外付哈希与 RWMutex 的开销。解法：分片数取 `runtime.NumCPU()` 量级及以上（选法见下方 Q）。
- **绕过路由直接摸 map**：现象是数据竞争。原因：没走 `getShard`，多个 goroutine 摸到同一个底层 map。解法：shard 数组保持私有，只暴露带路由的方法。
- **Range 回调里写同一 shard**：现象是重入死锁。原因：`RWMutex` 不可重入，遍历已持读锁。解法：回调里只做只读操作，写入延后批量执行。
- **跨 key 原子操作没有**：现象是无法保证多 key 一致更新。原因：两个 key 常落不同 shard，加不了同一把锁。解法：① 按 shard 下标升序依次 `Lock` 两个 shard（全局固定顺序防死锁）；② 退化成单锁 map；③ 让关联 key 落同一 shard（如用同一前缀参与哈希）。
- **拿 Range/Len 当一致性快照**：现象是一致性判断偶发出错。原因：锁逐 shard 拿放，读到的是"移动中的快照"。解法：需要一致性就整体加锁或 Copy-on-Write（写时复制）。遍历本身不会崩——写者被当前 shard 的读锁挡在外面。
- **`Len()` 当精确值用**：现象是统计偏差。原因：竞态下逐 shard 求和只是近似。解法：按近似值用，精确计数另加全局计数器。
- **value 用 `interface{}` 丢类型**：现象是取出后必须类型断言。原因：demo 为通用性用 `interface{}`。解法：生产实现泛型化 `ShardMap[K comparable, V any]`。

**Q：分片数怎么选？**
A：经验起点是 `runtime.NumCPU()` 或 32。太少（1~2）接近全局锁；太多（>128）白白占内存、初始化变慢，收益递减。实测（8 goroutine）吞吐随 shard 数单调升但次线性，因为瓶颈还有 hash 计算、内存带宽和调度。

常取 2 的幂，把 `% N` 换成 `& (N-1)`。

**Q：为什么不直接用 sync.Map？**
A：本机实测同一混合负载 `sync.Map` 反而快约 1.8~2.3 倍。

但 ShardMap 有三张牌：可泛型化（类型安全）、`Len()` 只需 O(shardCount) 次锁而非 O(n) 遍历、写路径只锁一个 shard 而不涉及 read/dirty 提升逻辑。`sync.Map` 官方定位是"key 稳定、读多写少"（如缓存）；key 高频增删时它的 dirty 重建代价大。
