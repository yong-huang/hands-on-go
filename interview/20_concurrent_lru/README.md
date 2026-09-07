# 20 · Concurrent LRU：分片锁下的高并发 O(1) 缓存

> 单线程 LRU（[01_lru_cache](../01_lru_cache/)）一把锁就够，但 100 个 goroutine 抢一把全局锁时，所有 Get/Put 被迫串行，缓存反而成为吞吐瓶颈。本实验实现工业界标准的 **Sharded LRU**：key 经 FNV-32a 哈希路由到 32 个独立分片，每片自带 `sync.RWMutex` + 哈希表 + 手写双向链表，不同分片互不阻塞。程序自带 100×10000 并发压测与顺序正确性校验，把"锁粒度"这个面试高频词变成可跑的数字。

## 1. 为什么需要它

`map + 双向链表` 的经典 LRU 是纯单线程结构：Get 命中要 `moveToFront`（写链表），Put 要淘汰尾部（写链表），所以任何访问都是写操作——加一把全局 `sync.Mutex` 后，**并行度直接退化为 1**。解法不是换更快的锁，而是缩小锁的管辖范围：把数据按 key 哈希分散到 N 个分片，每个分片是一台完全独立的 LRU。竞争被 N 个锁均摊，不同分片的请求完全并行。

## 2. 总览：核心机制一图看懂

![分片并发 LRU：哈希路由到独立分片](images/clru.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/hands-on-go/interview/20_concurrent_lru/images/clru.html)
> （或本地打开 [`images/clru.html`](images/clru.html)）。

**心智模型一句话**：ConcurrentLRU 不是"一个带锁的 LRU"，而是"`FNV-32a(key) % 32` 路由到的 32 台独立小 LRU"——锁、链表、map、淘汰策略全部下沉到 shard 内部，顶层只剩一个无锁的路由函数。

## 3. 快速开始

```bash
cd interview/20_concurrent_lru
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

压测用 32 分片 × 每片 256 容量（总容量 8192），100 个 goroutine 各做 10000 轮 Get+Put，实测吞吐约 1200 万 ops/sec。正确性校验故意用 **1 个分片、容量 4**，使总容量确定，可断言精确的淘汰顺序（含"Get('b') 提升后 Put 淘汰 'c' 而非 'b'"这条 LRU 语义）。

## 4. 核心概念

### 4.1 手写双向链表 + 单哨兵

不依赖 `container/list`（其 `Element.Value` 是 `interface{}`，多一层装箱与堆分配）。`lruList` 只有一个 sentinel 节点，`head`/`tail` 都指向它，空链表即 `sentinel.next == sentinel`——`pushFront`/`remove`/`popBack` 全部无需判空分支：

```go
type lruList struct{ head, tail *entry } // head==tail==sentinel

func (l *lruList) popBack() *entry {
    if l.head.next == l.tail { return nil } // 空表防御
    victim := l.tail.prev
    l.remove(victim)
    return victim
}
```

### 4.2 shard：锁与数据的绑定单元

```go
type shard struct {
    mu    sync.RWMutex
    cap   int                    // 每片容量；总容量 = shardCount * cap
    list  lruList                // 片内 LRU 序
    items map[string]*entry      // 片内 O(1) 索引
}
```

路由 `getShard` 用 `fnv.New32a()` 算哈希后 `% shardCount`：相同 key 恒达同一分片（这是 LRU 语义成立的前提），不同 key 大概率分散。分片数取 2 的幂（本实现 32），除法变取模且分布均匀。

### 4.3 Get：读锁查找 + 写锁提升，两段加锁

命中后要 `moveToFront`（改链表），必须写锁；但 Go 的 `RWMutex` **不支持 RLock 原地升级**（持读锁时再 Lock 会死锁）。实现拆成两段：miss 时只花一次读锁就返回；命中则 `RUnlock` → `Lock`，并在写锁内**二次检查** `s.items[key]`——两段间隙里该 key 可能已被其他 goroutine 更新甚至淘汰，不复查就会把悬空节点提升到链表头。

### 4.4 Put：写锁内原子完成"更新/淘汰/插入"

整段持写锁：已存在则改值 + 提升；不存在且满了先 `popBack` 淘汰尾部再 `pushFront` 插入。淘汰是分片局部行为——某个分片满不会驱逐其他分片的热数据，代价是**全局看各分片淘汰进度不均**（偏斜 key 会提前挤爆个别分片）。

## 5. 关键字段与坑清单

| 字段/调用 | 含义 | 为什么重要 |
|---|---|---|
| `shardCount uint32` | 分片数，取模的模数 | uint32 与 hash 值同型，避免类型转换；2 的幂分布最匀 |
| `perShardCap` | 每片容量 | 总容量 = shardCount × perShardCap，不随访问倾斜变化 |
| `Get` 的二次检查 | 写锁内重查 `items[key]` | 防 RUnlock→Lock 间隙节点被并发 Put 删除后悬空提升 |
| `popBack` 的 nil 判断 | 空链表返回 nil | 容量逻辑上不会空淘汰，但防御性检查让链表可独立复用 |

坑清单：

- **RLock 里不能直接 Lock**：会死锁。必须先 RUnlock 再 Lock，并承担两段之间的窗口——用二次检查弥补
- **`fnv.New32a()` 每次 Get/Put 都分配一个 hash 对象**：正确但可优化（复用 hash.Hash64 或手写 FNV 常量计算），面试可主动指出
- **正确性校验必须用 1 个分片**：多分片下总容量 = 32×cap，5 次插入根本不会触发淘汰，测不出 LRU 语义
- **命中率输出每次不同**（本次 67.86%，此前出现过 68.88%）：100 个 goroutine 的交错调度不确定，压测数字只能看量级，不能逐位对比
- **手写链表记得清 `e.prev/e.next = nil`**（`remove` 里有）：不清会拖着整条链防 GC 回收

## 6. 文件结构

```text
interview/20_concurrent_lru/
├── README.md                      # 本文件：原理 + 运行指引 + 面试要点
├── concurrent_lru.go              # 主教材：链表 → shard → 路由 → Get/Put → 并发压测与正确性校验
└── images/
    ├── clru.architecture.json     # 图源：分片路由与锁粒度（Typed JSON IR）
    ├── clru.html                  # 交互版：可缩放/高亮连线的自包含页面
    └── clru.svg                   # README 内嵌双主题矢量图
```

## 7. 面试要点

**Q：分片 LRU 与 01 的单线程 LRU，代码上差在哪？**
A：三层变化——① 把 `Mutex + map + list` 打包成 `shard` 结构；② 顶层加 `getShard` 哈希路由；③ 锁从一把变 32 把。链表部分（哨兵、pushFront、popBack）与单线程版完全同构，可直接复用——这说明分片是**并发层的组合模式**，不侵入 LRU 本身。

**Q：锁粒度怎么选？分片数越多越好吗？**
A：并行度上限 = 分片数。太少（2~4）竞争仍集中；太多则单片数据变少、哈希偏斜被放大，且总容量（分片数 × 每片容量）随之膨胀。常见取 2 的幂的 32~256：BigCache 512、freecache 256。分片数应结合内存预算和 key 偏斜程度定，不是拍脑袋越大越好。

**Q：Get 为什么要两段加锁？直接 Lock 一把梭不行吗？**
A：`RWMutex` 不支持读锁原地升级，持 RLock 调 Lock 会死锁。两段式的价值是 miss 路径只花读锁；代价是 RUnlock 与 Lock 之间状态可变，必须在写锁内二次检查 key 仍存在。写多场景可直接全程 Lock 换简单性。

**Q：这个实现有哪些手写易错点？**
A：① 链表 remove 不清指针会拖住 GC；② Get 提升与并发 Put 竞争需要二次检查，否则悬空指针；③ 校验淘汰顺序必须收敛到单分片，否则容量语义对不上；④ hash 对象每次分配是隐藏的 GC 压力点。

**Q：`sync.Map` 能替代分片 LRU 吗？**
A：不能直接替代。`sync.Map` 适合 key 稳定的读多写少场景，无容量控制、无淘汰、写路径是全局锁。分片 LRU 每片可限容量、写只锁一片，是通用并发缓存的正解；`sync.Map` 更适合配置表这类"写一次读万千"的数据。

## 8. 总结

分片 LRU 的全部性能来自一个决定：**锁跟着数据分片走**。路由 O(1)，片内还是熟悉的"map + 双向链表 + 哨兵"，Get 两段加锁兼顾读并行与 LRU 语义，Put 在单片写锁内原子完成淘汰。面试时先讲清锁粒度为什么是性能杠杆，再主动暴露两段加锁与二次检查这类细节，比背"支持高并发"有说服力得多。

这是 interview 系列的最后一站；回到 [internals/01_slices](../../internals/01_slices/) 可以从数据结构底层再走一遍完整主线。
