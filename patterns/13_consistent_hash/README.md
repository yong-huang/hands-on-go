# 13 · 一致性哈希：哈希环、虚拟节点与最小数据迁移

> 分布式系统里节点随时可能上线、下线，传统 `hash(key) % N` 在 N 变化的瞬间会让**几乎所有 key 重新映射**，缓存全部失效。一致性哈希（Karger et al., 1997）把节点和 key 放到同一个哈希环上，增删节点只迁移约 1/N 的 key。本篇用 Go 实现完整的哈希环（含虚拟节点），并实测 1000 个 key 在 Add/Remove 前后的迁移量，把"最小迁移"从口号变成数字。

## Background

把 key 分摊到多台缓存节点上，最常用的公式是 `hash(key) % N`——哈希值对节点数取模（求余）。节点集合不变时它简单且均匀。

问题出在扩缩容的瞬间：缓存集群从 3 台扩到 4 台，`hash(key) % 3` 换成 `hash(key) % 4`，两个取模结果对绝大多数 key 都不同。后果是命中率瞬间跌到接近 0，原本由缓存挡下的请求全部落到数据库上，数据库被打穿。

需要一种新的放置规则，同时满足「平时均匀」和「增删节点只动一小部分数据」。1997 年 Karger 等人在论文中给出的答案，成了这个问题的标准解法。

## What

**一致性哈希（Consistent Hashing）**是一种分布式哈希方案（决定"哪个 key 由哪个节点负责"的规则）：把节点和 key 都用同一个哈希函数映射到一个值域首尾相接的环上，key 归顺时针遇到的第一个节点所有，节点增减只影响相邻弧段（环上两点之间的一段）的数据。

心智模型一句话：key 和节点都做 crc32（一种 32 位哈希函数）落到同一个环上，`Get` 用 `sort.Search` 二分找第一个 ≥ hash(key) 的环点，没有就回绕到 0——key 各自顺时针落到所属节点的弧段上。

哈希值来自真实运行，环点如 C#48 = 0x1c69b1f 是 D#12 之后顺时针的下一个点。

可以把哈希环想象成一座圆形剧场：节点是坐在圆周上的接待员，每个 key 从自己的位置顺时针走，归第一个遇到的接待员管。但和剧场不同的是：这里每个接待员带了几十个"分身"座位散布在圆周各处（虚拟节点，下一节展开），而且接待员离场时只交出自己座位之间那段观众，其余观众的归属纹丝不动。

## When to Use

这节回答"什么系统需要一致性哈希，什么系统取模就够"。

典型场景：

- **缓存集群动态扩缩容**：Memcached 一类缓存加减机器时，要求绝大多数已缓存的 key 不失效。
- **网关的粘性负载均衡**：Nginx/Envoy 类网关把同一 key 的请求固定路由到同一后端，后端增减时只影响一小部分流量。
- **分片存储的数据分布**：DynamoDB/Cassandra 的数据分片、Chord DHT 都基于这个思想。

何时不用：

- 节点集合几乎固定不变：取模哈希更简单，查找还是 O(1)（一致性哈希的 Get 是 O(log M)）。
- 平台已提供原生方案：Redis Cluster 用 16384 槽位的类似思想（槽位表而非环），用它即可。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 取模哈希 `hash(key) % N` | O(1) 查找；N 变化时几乎全部 key 重新映射 | 节点集合固定不变 |
| **一致性哈希** | 增删只迁约 1/N；Get 为 O(log M)，需虚拟节点保证均衡 | 节点动态增减的缓存、分片、粘性路由 |
| 槽位表（Redis Cluster 式） | 固定数量的槽位映射，而非环 | 平台已内置时的原生方案 |

## Quick Start

前置条件：只需安装 Go，纯标准库实现。

```bash
cd patterns/13_consistent_hash
go run consistent_hash.go
```

预期输出（节选，确定性输出，每次运行相同）：

```text
1. Add 3 nodes (A, B, C), distribute 1000 keys:
     Node A: 390 keys
     Node B: 181 keys
     Node C: 429 keys
     Total ring points: 150
2. Add node D, measure migration rate:
     Node A: 262 keys   Node B: 149 keys
     Node C: 286 keys   Node D: 303 keys
   Migrated keys: 303 / 1000 (30.3%)
3. Remove node A:
     Node B: 233 keys   Node C: 384 keys   Node D: 383 keys
   Keys previously on A (redistributed): 262
   Only A's keys migrated, B/C/D unaffected
```

三步演示对应三个结论：无足够虚拟点时负载不均（390/181/429）；加 D 只迁 30.3%（取模哈希≈100%）；摘 A 只有 A 的 262 个 key 顺时针并给邻居，B/C/D 纹丝不动。

核心数据结构与查找逻辑：

```go
type ConsistentHash struct {
    replicas int               // 每个物理节点的虚拟节点数
    ring     []uint32          // 所有虚拟节点的哈希值（有序）
    hashMap  map[uint32]string // 哈希值 → 物理节点名
    keys     map[string]bool   // 物理节点集合
}

func (ch *ConsistentHash) Get(key string) string {
    h := hash(key) // crc32.ChecksumIEEE
    idx := sort.Search(len(ch.ring), func(i int) bool { return ch.ring[i] >= h })
    if idx == len(ch.ring) {
        idx = 0 // 环形回绕：超过最大值回到起点
    }
    return ch.hashMap[ch.ring[idx]]
}
```

这段在做什么：`ring` 是有序 uint32 切片，值域 [0, 2^32) 首尾相接构成逻辑上的环；`sort.Search` 二分找"顺时针第一个 ≥ h 的位置"，复杂度 O(log M)。你在输出里看到的每台节点分到的 key 数，就是这段查找对 1000 个 key 重复执行的结果。

## How It Works

按增删节点的两条路径走：Add 把虚拟点铺上环，Remove 把一段弧的 key 交给邻居。

### 虚拟节点（Add）

```go
for i := 0; i < ch.replicas; i++ {
    vkey := fmt.Sprintf("%s#%d", node, i) // A#0, A#1, ...
    h := hash(vkey)
    ch.ring = append(ch.ring, h)
    ch.hashMap[h] = node
}
sort.Slice(ch.ring, ...) // 保持有序
```

这段在做什么：每个物理节点展开成 `replicas`（本实验 50）个虚拟点散布到环上，所以输出里 `Total ring points: 150` = 3 节点 × 50，加 D 后变 200。

虚拟点数量与均衡度的关系要靠实测说话：3 节点各 50 点时分布 390/181/429 仍不均——**均衡是统计效应，虚拟点越多越均匀**，实践中每节点 100~200 个。

### Remove：只影响相邻弧段

摘除节点时删掉它的全部虚拟点并重建有序 ring。原本落在这些弧段里的 key（实测 262 个）顺时针落到下一个节点，**其余 key 的映射完全不变**——这就是"最小迁移"的含义。

被摘节点的负载由环上邻居按弧段长度分摊，输出第 3 步的 `Only A's keys migrated, B/C/D unaffected` 就是对这句话的验证。

### 关键字段一览

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `ring []uint32` | 有序环点 | 二分查找的数据基础；乱序则 Get 结果错 |
| `hashMap[uint32]string` | 环点→物理节点 | 虚拟点共享同一物理节点映射 |
| `replicas` | 虚拟点倍数 | 均衡度 vs 查找开销的旋钮 |
| `idx == len(ring) → 0` | 环形回绕 | 漏掉则 hash 大于最大环点的 key 查不到节点 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **忘记回绕**：现象是 hash 大于最大环点的 key 查不到节点。原因：`sort.Search` 返回 `len(ring)` 时未处理。解法：`idx = 0`——这是自己实现时最常见的错误。
- **Remove 后不重建 ring**：现象是 Get 命中已摘除的节点。原因：本实现 `Remove` 删 `hashMap` 条目后要调 `rebuildRing()` 重建切片，两步缺一不可，否则已删哈希值仍在环上被二分命中。
- **虚拟点太少**：现象是 3 节点 × 1 点时单节点可能承担 80%+ key。原因：环上点太少，分布随机性不足。解法：50 点仍有 ±10% 波动，实践中每节点 100~200 个。
- **虚拟点命名冲突**：现象是不同节点的哈希值相同、`hashMap` 互相覆盖。原因：虚拟点名 `node#i` 拼出了重复字符串。解法：确保节点名 + 序号的组合全局唯一。
- **Add 每次全量排序 O(M log M)**：现象是节点多时 Add/Remove 变慢。原因：每次重排整个环。解法：延迟敏感可换平衡树把 Add/Remove 降到 O(log M)。

**Q：一致性哈希和取模哈希的区别？**
A：取模 `hash(key) % N` 在 N 变化时几乎全部 key 重新映射；一致性哈希只迁移被新节点接管的弧段，约 1/N（实测 3→4 节点迁 30.3%，取模≈100%）。代价是 Get 从 O(1) 变 O(log M)，且需要虚拟节点保证均衡。

**Q：虚拟节点解决什么问题？**
A：物理节点直接落环数量太少，分布随机性不足，可能出现单节点承担绝大部分 key。每个物理节点映射成几十上百个虚拟点，统计上把负载摊平；副作用是 ring 变长、二分稍慢。工业界（如 ketama）通常每节点 100~200 个。

**Q：摘除一个节点，多少数据要迁移？迁到哪？**
A：一句话——只有被摘节点虚拟点弧段内的 key（实测 262/1000）顺时针并入相邻节点，其余映射不变；细节见上文 Remove 小节。

**Q：哪些系统在用？**
A：Memcached 客户端（ketama）、DynamoDB/Cassandra 的数据分片、Chord DHT、Nginx/Envoy 的一致性哈希负载均衡。Redis Cluster 用的是 16384 槽位的类似思想（槽位表而非环）。
