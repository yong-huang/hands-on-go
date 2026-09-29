# 20 · Concurrent LRU：分片锁下的高并发 O(1) 缓存

> 经典单线程 LRU（[01_lru_cache](../01_lru_cache/)）一把锁就够，但 100 个 goroutine 抢一把全局锁时，所有 Get/Put 被迫串行，缓存反而成为吞吐瓶颈。本实验实现工业界标准的 **Sharded LRU**：key 经 FNV-32a 哈希路由到 32 个独立分片，每片自带 `sync.RWMutex` + 哈希表 + 手写双向链表，不同分片互不阻塞。程序自带 100×10000 并发压测与顺序正确性校验，让"锁粒度"成为可跑的数字。

## Background

LRU（Least Recently Used，最近最少使用——容量满时淘汰最久未被访问条目的缓存策略）的经典实现是 `map + 双向链表`，它是纯单线程结构：Get 命中要 `moveToFront`（写链表），Put 要淘汰尾部（写链表），所以任何访问都是写操作。

并发使用时只能加一把全局 `sync.Mutex`（互斥锁），**并行度直接退化为 1**：100 个 goroutine（Go 的轻量并发执行单元）抢一把锁，所有 Get/Put 被迫串行排队，缓存反而成为吞吐瓶颈。

解法不是换更快的锁，而是缩小锁的管辖范围：把数据按 key 哈希分散到 N 个分片，每个分片是一台完全独立的 LRU。竞争被 N 个锁均摊，不同分片的请求完全并行——这就是工业界缓存（BigCache、freecache 等）共同采用的 Sharded LRU 结构。

## What

**Concurrent LRU（Sharded LRU）**是一种把数据分片（shard——数据的独立分区）与 LRU 淘汰结合的并发缓存：key 经 FNV-32a（把任意字符串映射为 32 位整数的哈希算法）取模路由到固定数量的分片，每分片自带锁、哈希表与双向链表，互不干扰。

心智模型一句话：ConcurrentLRU 不是"一个带锁的 LRU"，而是"`FNV-32a(key) % 32` 路由到的 32 台独立小 LRU"——锁、链表、map、淘汰策略全部下沉到 shard 内部，顶层只剩一个无锁的路由函数。

可以把整套结构想象成一栋楼的 32 个独立储物间：物品按编号规则分到固定房间（哈希路由），每间房各有一把锁和自己的"最久没人动就清走"规矩（片内 LRU），不同房间的存取完全并行。

但和储物间不同的是：房间容量按片计算（总容量 = 分片数 × 每片容量），编号相近的物品若都落进同一间，那间会先满先淘汰——全局看各分片的淘汰进度并不均匀。

## When to Use

这节回答"哪些缓存场景值得上分片 LRU，哪些用更简单的结构就够"。

典型场景：

- **高并发读写的进程内缓存**：多个 goroutine 频繁 Get/Put，本文压测在这个形态下（100 goroutine × 10000 轮）实测吞吐约 1200 万 ops/sec。
- **容量受限的本地缓存**：内存有上限，满了按最近使用淘汰，替远程缓存挡住热点读。
- **对延迟敏感的路径**：命中与淘汰都是 O(1)，且只锁单片，不拖累无关请求。

何时不用：

- 单 goroutine 或低并发：经典单线程 LRU 更简单，一把锁足够（见 [01_lru_cache](../01_lru_cache/)）。
- key 稳定、写一次读万千的配置表：`sync.Map` 足够，不需要容量与淘汰逻辑。
- 跨进程、跨实例共享：这是进程内缓存，不解决分布式一致性。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 单线程 LRU + 全局锁 | 实现最简，并行度退化为 1 | 单 goroutine、低并发 |
| 分片 LRU（本篇） | 32 把锁各管一片，不同分片完全并行 | 高并发进程内缓存 |
| `sync.Map` | 无容量控制、无淘汰，写路径是全局锁 | key 稳定的读多写少场景 |
| BigCache / freecache | 工业级实现，分片 512 / 256 | 生产级大容量本地缓存 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/20_concurrent_lru
go run concurrent_lru.go
```

预期输出（节选，命中率与耗时每次运行不同）：

```text
=== Concurrent LRU Benchmark ===

Goroutines  : 100
Ops/goroutine: 10000
Total ops    : 1000000
Hits         : 678576
Misses       : 321424
Hit rate     : 67.86%
Elapsed      : 162.960125ms
Throughput   : 12272941 ops/sec

=== Correctness Check ===
PASS: 'a' was evicted (capacity=4, 5th insert)
PASS: 'e'='5' present
PASS: 'c' was evicted after 'b' was promoted
```

压测配置：32 分片 × 每片 256 容量（总容量 8192），100 个 goroutine 各做 10000 轮 Get+Put，实测吞吐约 1200 万 ops/sec。

正确性校验：故意用 **1 个分片、容量 4**，使总容量确定，可断言精确的淘汰顺序（含"Get('b') 提升后 Put 淘汰 'c' 而非 'b'"这条 LRU 语义）。

## How It Works

按一次请求的路径走：`getShard` 哈希路由 → 片内加锁 → map 定位 → 链表维护 LRU 序。

### 手写双向链表 + 单哨兵

不依赖 `container/list`（其 `Element.Value` 是 `interface{}`，多一层装箱与堆分配）。

`lruList` 只有一个 sentinel 节点，`head`/`tail` 都指向它，空链表即 `sentinel.next == sentinel`——`pushFront`/`remove`/`popBack` 全部无需判空分支：

```go
type lruList struct{ head, tail *entry } // head==tail==sentinel

func (l *lruList) popBack() *entry {
    if l.head.next == l.tail { return nil } // 空表防御
    victim := l.tail.prev
    l.remove(victim)
    return victim
}
```

### shard：锁与数据的绑定单元

```go
type shard struct {
    mu    sync.RWMutex
    cap   int                    // 每片容量；总容量 = shardCount * cap
    list  lruList                // 片内 LRU 序
    items map[string]*entry      // 片内 O(1) 索引
}
```

路由 `getShard` 用 `fnv.New32a()` 算哈希后 `% shardCount`：相同 key 恒达同一分片（这是 LRU 语义成立的前提），不同 key 大概率分散。分片数取 2 的幂（本实现 32），除法变取模且分布均匀。

### Get：读锁查找 + 写锁提升，两段加锁

命中后要 `moveToFront`（改链表），必须写锁；但 Go 的 `RWMutex` **不支持 RLock 原地升级**（持读锁时再 Lock 会死锁）。

实现拆成两段：miss 时只花一次读锁就返回；命中则 `RUnlock` → `Lock`，并在写锁内**二次检查** `s.items[key]`——两段间隙里该 key 可能已被其他 goroutine 更新甚至淘汰，不复查就会把悬空节点提升到链表头。

### Put：写锁内原子完成"更新/淘汰/插入"

整段持写锁：已存在则改值 + 提升；不存在且满了先 `popBack` 淘汰尾部再 `pushFront` 插入。淘汰是分片局部行为——某个分片满不会驱逐其他分片的热数据，代价是**全局看各分片淘汰进度不均**（偏斜 key 会提前挤爆个别分片）。

输出印证：`PASS: 'c' was evicted after 'b' was promoted` 验证的正是 Get 提升、Put 淘汰这套链表序维护；`PASS: 'a' was evicted (capacity=4, 5th insert)` 则来自这条淘汰路径。

### 字段速查

| 字段/调用 | 含义 | 为什么重要 |
|---|---|---|
| `shardCount uint32` | 分片数，取模的模数 | uint32 与 hash 值同型，避免类型转换；2 的幂分布最匀 |
| `perShardCap` | 每片容量 | 总容量 = shardCount × perShardCap，不随访问倾斜变化 |
| `Get` 的二次检查 | 写锁内重查 `items[key]` | 防 RUnlock→Lock 间隙节点被并发 Put 删除后悬空提升 |
| `popBack` 的 nil 判断 | 空链表返回 nil | 容量逻辑上不会空淘汰，但防御性检查让链表可独立复用 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **RLock 里不能直接 Lock**：现象是程序死锁。原因：`RWMutex` 不支持原地升级。解法：先 `RUnlock` 再 `Lock`，并承担两段之间的窗口——用二次检查弥补。
- **`fnv.New32a()` 每次 Get/Put 都分配一个 hash 对象**：现象是隐藏的 GC 压力。原因：每次调用分配新对象。解法：复用 `hash.Hash64` 或手写 FNV 常量计算（正确但可优化）。
- **正确性校验必须用 1 个分片**：现象是多分片下测不出 LRU 语义。原因：总容量 = 32×cap，5 次插入根本不会触发淘汰。解法：校验用例收敛到单分片，让容量语义确定。
- **命中率输出每次不同**（本次 67.86%，此前出现过 68.88%）：现象是压测数字逐位波动。原因：100 个 goroutine 的交错调度不确定。解法：压测数字只看量级，不逐位对比。
- **手写链表记得清 `e.prev/e.next = nil`**：现象是已删除节点迟迟不被回收。原因：`remove` 不清指针会拖着整条链防 GC 回收。解法：在 `remove` 里把 `e.prev/e.next` 置 `nil`。

**Q：分片 LRU 与单线程 LRU，代码上差在哪？**
A：三层变化——① 把 `Mutex + map + list` 打包成 `shard` 结构；② 顶层加 `getShard` 哈希路由；③ 锁从一把变 32 把。

链表部分（哨兵、pushFront、popBack）与单线程版完全同构，可直接复用——这说明分片是**并发层的组合模式**，不侵入 LRU 本身。

**Q：锁粒度怎么选？分片数越多越好吗？**
A：并行度上限 = 分片数。太少（2~4）竞争仍集中；太多则单片数据变少、哈希偏斜被放大，且总容量（分片数 × 每片容量）随之膨胀。常见取 2 的幂的 32~256：BigCache 512、freecache 256。

分片数应结合内存预算和 key 偏斜程度定，不是拍脑袋越大越好。

**Q：Get 直接全程 Lock 一把梭不行吗？**
A：可以，写多场景直接全程 Lock 换简单性。两段加锁的价值是 miss 路径只花读锁；机制细节见 How It Works 的 Get 一节。

**Q：`sync.Map` 能替代分片 LRU 吗？**
A：不能直接替代。`sync.Map` 适合 key 稳定的读多写少场景，无容量控制、无淘汰、写路径是全局锁。分片 LRU 每片可限容量、写只锁一片，是通用并发缓存的正解；`sync.Map` 更适合配置表这类"写一次读万千"的数据。
