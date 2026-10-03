# 08 · sync.Map 源码级复盘：read/dirty/misses 的双 map 协议

> 本篇复盘 sync.Map 经典实现的 read/dirty/misses 协议，用带观测钩子的复刻逐帧验证 miss 累积与 promote 流转，再用三方基准对比读多写少、写多、高频增删三种负载。读完本篇能说清读路径为什么无锁、dirty 何时重建何时提升，以及这个协议的性能悬崖长什么样。

## Background

并发读一个几乎不变的关键字到值的映射，最朴素的工具是原生 map 配 `sync.Mutex`：任何读取也要拿锁，读者互相排队。换成 `sync.RWMutex` 后读者能共享，但每次读锁仍是两次原子操作加缓存行争用，读者一多，锁本身就成为热点。

痛点在读多写少的缓存场景集中爆发：上万次读取挤在一把锁上，写入占比不到百分之一，却决定所有人的延迟。人们想要一种结构，让读路径完全不碰锁。

Go 1.9 为此引入 `sync.Map`：读走一份只读快照，不加锁；写入进受锁保护的缓冲视图，攒到一定 miss 量再合并回快照。这套 read/dirty/misses 协议服役到 Go 1.23。

之后实现换代：Go 1.24 起实验性换用并发哈希字典树 HashTrieMap，Go 1.25 成为默认，Go 1.26 定稿。本篇用复刻加钩子复盘经典协议，基准则用本机 go1.26 的真 sync.Map 实测。

## What

经典 sync.Map 是一个读写分离的并发映射：读操作走无锁的只读快照，写操作进入持锁的缓冲视图，miss 攒够后缓冲整体提升（promote）为快照。协议由五个部件驱动：

| 部件 | 职责 |
|---|---|
| `read` | 只读快照（map + amended 标记），Load 快路径的唯一数据源，atomic.Pointer 发布 |
| `read.amended` | true 表示 dirty 里有 read 没有的 key，Load 落空后要去 dirty 找 |
| `dirty` | 完整视图，持 mu 才能读写；由 read 全量复制而来 |
| `misses` | Load 落到 dirty 的次数；达到 `len(dirty)` 触发 promote |
| `entry` | 值的原子槽位，可 CAS 原地改值——这是"entry 只读加速"的本体 |

可以把 read 想成橱窗里的价格牌，顾客看牌不加锁；dirty 是柜台后的总账本，上新货先抄一遍橱窗（重建），抄完把新货记上。但和真实橱窗不同的是，价格牌本身支持原地改价：改已有 key 的值是单一 CAS，橱窗都不用进，这正是 entry 只读加速的含义。

## When to Use

**条目写一次、读多次的增长型缓存**。进程内路由表、配置快照、去重集合：key 写入后几乎只读，读路径长期停留在无锁快照上。

**多个 goroutine 操作互不相交的 key 集合时**。各写各的 key、互不覆盖，冲突集中在结构变更而非同一 entry。

**何时不用**：业务需要 map 语义之外的不变量（范围查询、按序遍历）或更好的类型安全时，普通 map 配锁更直接；写入密集时 sync.Map 的维护成本反而高于一把普通的锁。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| map + `Mutex` | 读写全串行，类型安全 | 写多、结构频繁变化 |
| map + `RWMutex` | 读共享、写独占 | 读多写少、key 集合稳定 |
| `sync.Map` | 读无锁，写走缓冲视图 | 增长型缓存、key 互不相交 |
| 分片 map | 16 把锁分摊争用 | 写也不少、key 可哈希分桶 |

## Quick Start

需要 go 1.26，无第三方依赖。运行协议分步演示：

```bash
go run ./08_syncmap_internals/
```

真实输出（协议流转是确定性的，各数值每次运行一致）：

```text
== 步骤 1：新 key 落 dirty、首读必 miss、promote 后 read 命中零计数 ==
  首个新 key：DirtyBuilt=1（诚实预期：1）；此刻 read 为空，key 只在 dirty
  首读 1 次：Misses=1 Promotes=1（诚实预期：1 1，新 key 必经一次慢路径）
  再读 1000 次：Misses=1（诚实预期：仍为 1）——entry 只读加速
== 步骤 2：miss 累积到 len(dirty)，触发 promote ==
  写 3 个 key：DirtyBuilt=1（诚实预期：1，只有首个新 key 重建）；len(dirty)=3
  2 次 miss：Misses=2 Promotes=0（诚实预期：2 0，未达阈值 3）
  第 3 次 miss：Misses=3 Promotes=1（诚实预期：3 1）；dirty=nil、amended 翻回 false
  promote 后 Load("cpu")=M4——数据完好，此后命中新 read
== 步骤 3：dirty 反复重建的性能悬崖 ==
  round 0：promote 后 read 规模=10，存 1 个新 key 重建 1 次（累计 2，复制 10 个 entry）
  round 1：promote 后 read 规模=21，存 1 个新 key 重建 1 次（累计 3，复制 21 个 entry）
  round 2：promote 后 read 规模=32，存 1 个新 key 重建 1 次（累计 4，复制 32 个 entry）
  round 3：promote 后 read 规模=43，存 1 个新 key 重建 1 次（累计 5，复制 43 个 entry）
  round 4：promote 后 read 规模=54，存 1 个新 key 重建 1 次（累计 6，复制 54 个 entry）
  结论：read 攒到 N 条后，每个新 key 都付 O(N) 复制——经典协议的 dirty 重建悬崖；
  高频增删 key 的三方基准（真 sync.Map / RWMutex+map / 分片）：go test -bench=. -benchmem -run=^$
```

并发正确性与协议断言用竞态检测器验证：

```bash
go test -race -count=10 -timeout 300s ./08_syncmap_internals/
```

真实输出：

```text
ok  	concurrency/08_syncmap_internals	1.380s
```

## How It Works

promote 的完整流转如下，方括号标注锁点（全部互斥由唯一的一把 `mu` 保护）：

```text
写已有 key ── read 命中：entry CAS 原地改值，无锁 ──────────────────────────▶ 完成
写新 key   ── [锁点1 mu] read 未 amended：从 read 复制建 dirty，amended=true
                        ▶ dirty[key]=entry ── [解锁] ──▶ 完成
Load 落空  ── read miss 且 amended ── [锁点2 mu] 双检 read → 查 dirty
                        ── misses++；misses <  len(dirty)：解锁返回
                        ── misses >= len(dirty)：promote ──▶ dirty 原地升级为 read，
                           dirty=nil、misses=0、amended=false ── [解锁] ──▶ 完成
```

双检（进锁后再看一次 read）不可省略：排队拿锁期间可能已有别人完成了 promote，此时 key 已在 read 里。

Quick Start 步骤 2 的三次 miss 正是流转的逐帧回放——第 3 次 miss 达到阈值，`dirty=nil`、`amended=false`，计数被钩子如实记录为 `Promotes=1`。

门槛判断在 `missLocked`，它是"攒一批再合并"策略的全部：

```go
func (m *ProtocolMap) missLocked() {
	m.misses++
	m.Misses.Add(1)
	if m.misses < len(m.dirty) {
		return // 阈值 = len(dirty)：dirty 越大，容忍的 miss 越多
	}
	m.read.Store(&readOnly{m: m.dirty}) // 原地升级：map 本体换手，条目不复制
	m.dirty = nil
	m.misses = 0
	m.Promotes.Add(1)
}
```

三种负载的三方实测（Apple M5 Pro，`go test -bench=. -benchmem -run='^$'`，18 并行度）：

| 场景 | sync.Map | RWMutex+map | 分片 16 |
|---|---|---|---|
| 读多写少（95% Load） | 29.7 ns/op | 98.1 ns/op | 44.6 ns/op |
| 写多（覆盖 8 热点） | 154.7 ns/op | 133.2 ns/op | 77.2 ns/op |
| 高频新增 key | 72.4 ns/op | 423.8 ns/op | 118.1 ns/op |

读多写少下真 sync.Map 快 3.3 倍，写多下慢于分片——与协议的取向一致。

高频新增一行值得单独解释：真 sync.Map（go1.26 的哈希字典树）反而是最快的，因为它已没有 dirty；经典协议在这个场景每写一个新 key 都全量复制 read——本篇的 ProtocolMap 复刻把它复现成步骤 3 的悬崖，复制量随 read 规模 10、21、32、43、54 逐轮放大。

## Pitfalls & Q&A

**把高频新增 key 的负载套在经典 sync.Map 上**。现象：写入吞吐随条目规模线性劣化。原因：每次 promote 后第一个新 key 触发 dirty 全量重建，规模越大复制越贵。

解法：这类负载换分片 map，或升级工具链——go1.24 起 `GOEXPERIMENT` 可开启哈希字典树实现，1.25 起默认，悬崖随旧实现一起消失。

**以为读路径"绝对零锁"**。现象：read 未命中且 `amended=true` 时，Load 仍会拿 `mu` 查 dirty。原因：快照只覆盖已提升的条目。推论是"新写入的 key 首读必慢"——Quick Start 步骤 1 的 `Misses=1` 就是这条规律的实测。

**Q：为什么提升阈值取 `len(dirty)` 而不是固定值？** 阈值越大，重建越稀，但每次重建后 miss 的容忍窗口越长。取 `len(dirty)` 让"提升前经历的 miss 数"与"重建的复制成本"同阶，两者大致对价；固定小阈值会让大 map 频繁白付复制钱。

**Q：观测真 sync.Map 的内部状态还可能吗？** go1.26 起不可能——`read`/`dirty`/`misses` 已随旧实现移除，包内也没有导出计数。

本篇的做法是自带钩子的复刻：`Misses`/`Promotes`/`DirtyBuilt` 三个计数器把流转变成可断言的测试（`TestPromoteFlow`），这也是研究任何黑盒内部协议的通用替代手段。
