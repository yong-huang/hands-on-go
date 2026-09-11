# 13 · 一致性哈希：哈希环、虚拟节点与最小数据迁移

> 分布式系统里节点随时可能上线、下线，传统 `hash(key) % N` 在 N 变化的瞬间会让**几乎所有 key 重新映射**，缓存全部失效。一致性哈希（Karger et al., 1997）把节点和 key 放到同一个哈希环上，增删节点只迁移约 1/N 的 key。本实验用 Go 实现完整的哈希环（含虚拟节点），并实测 1000 个 key 在 Add/Remove 前后的迁移量，把"最小迁移"从口号变成数字。

## 1. 为什么需要它

缓存集群从 3 台扩到 4 台时，`hash(key) % 3` 换成 `hash(key) % 4`，两个取模结果对绝大多数 key 都不同——命中率瞬间跌到接近 0，数据库被打穿。一致性哈希的解法：**节点和 key 都做同样的哈希，落到 [0, 2^32) 的环上；key 顺时针找第一个遇到的节点**。节点增减只改变环上的一小段，只有被"接管"弧段内的 key 需要搬家。

面试高频考点：哈希环原理、虚拟节点的作用、与取模哈希的迁移量对比、手写实现。本实验把四个都过一遍。

## 2. 总览：核心机制一图看懂

![一致性哈希环：key 顺时针找第一个节点](images/hash.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/13_consistent_hash/images/hash.html)
> （或本地打开 [`images/hash.html`](images/hash.html)）。

**心智模型一句话**：key 和节点都做 crc32 落到同一个环上，`Get` 用 `sort.Search` 二分找第一个 ≥ hash(key) 的环点，没有就回绕到 0——图中 4 个真实 key 各自顺时针落到所属节点的弧段上（哈希值来自真实运行，环点如 C#48 = 0x1c69b1f 是 D#12 之后顺时针的下一个点）。

## 3. 快速开始

```bash
cd interview/13_consistent_hash
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

## 4. 核心概念

### 4.1 哈希环与 Get

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

`ring` 是有序 uint32 切片，值域 [0, 2^32) 首尾相接构成逻辑上的环；`sort.Search` 二分找"顺时针第一个 ≥ h 的位置"，O(log M)。

### 4.2 虚拟节点（Add）

```go
for i := 0; i < ch.replicas; i++ {
    vkey := fmt.Sprintf("%s#%d", node, i) // A#0, A#1, ...
    h := hash(vkey)
    ch.ring = append(ch.ring, h)
    ch.hashMap[h] = node
}
sort.Slice(ch.ring, ...) // 保持有序
```

每个物理节点展开成 `replicas`（本实验 50）个虚拟点散布到环上。实测 3 节点各 50 点时分布 390/181/429 仍不均——**均衡是统计效应，虚拟点越多越均匀**，实践中每节点 100~200 个。总环点 = 节点数 × replicas：3 节点 150 点，加 D 后 200 点。

### 4.3 Remove：只影响相邻弧段

摘除节点时删掉它的全部虚拟点并重建有序 ring。原本落在这些弧段里的 key（实测 262 个）顺时针落到下一个节点，**其余 key 的映射完全不变**——这就是"最小迁移"的含义。被摘节点的负载由环上邻居按弧段长度分摊。

## 5. 关键字段与坑清单

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `ring []uint32` | 有序环点 | 二分查找的数据基础；乱序则 Get 结果错 |
| `hashMap[uint32]string` | 环点→物理节点 | 虚拟点共享同一物理节点映射 |
| `replicas` | 虚拟点倍数 | 均衡度 vs 查找开销的旋钮 |
| `idx == len(ring) → 0` | 环形回绕 | 漏掉则 hash 大于最大环点的 key 查不到节点 |

坑清单：

- **忘记回绕**：`sort.Search` 返回 `len(ring)` 时必须 `idx = 0`，这是手写题最常见错误
- **Remove 后不重建 ring**：本实现 `Remove` 删 `hashMap` 条目后调 `rebuildRing()` 重建切片，两步缺一不可，否则已删哈希值仍在环上被二分命中
- **虚拟点太少**：3 节点 × 1 点可能出现单节点承担 80%+ key；50 点仍有 ±10% 波动
- **Add 每次全量排序 O(M log M)**：节点少可接受；延迟敏感可换平衡树把 Add/Remove 降到 O(log M)

## 6. 文件结构

```text
interview/13_consistent_hash/
├── README.md                    # 本文件：原理 + 运行指引 + 深入要点
├── consistent_hash.go           # 主教材：哈希环 Add/Remove/Get + 迁移率实测
└── images/
    ├── hash.architecture.json   # 图源：哈希环顺时针查找（Typed JSON IR）
    ├── hash.html                # 交互版：可缩放/高亮连线的自包含页面
    └── hash.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：一致性哈希和取模哈希的区别？**
A：取模 `hash(key) % N` 在 N 变化时几乎全部 key 重新映射；一致性哈希只迁移被新节点接管的弧段，约 1/N（实测 3→4 节点迁 30.3%，取模≈100%）。代价是 Get 从 O(1) 变 O(log M)，且需要虚拟节点保证均衡。

**Q：虚拟节点解决什么问题？**
A：物理节点直接落环数量太少，分布随机性不足，可能出现单节点承担绝大部分 key。每个物理节点映射成几十上百个虚拟点，统计上把负载摊平；副作用是 ring 变长、二分稍慢。工业界（如 ketama）通常每节点 100~200 个。

**Q：摘除一个节点，多少数据要迁移？迁到哪？**
A：只有原本落在被摘节点虚拟点弧段内的 key 需要迁移（实测 262/1000），它们顺时针并入相邻节点的弧段；其他所有 key 的映射不变。新节点的负载由邻居"让出"弧段承担。

**Q：手写一致性哈希最容易错在哪？**
A：三点——① 二分结果等于 `len(ring)` 时忘记回绕到 0；② Remove 后忘记从 ring 中移除该节点的虚拟点；③ 虚拟节点命名冲突（`node#i`）导致不同节点哈希值相同覆盖 `hashMap`。

**Q：哪些系统在用？**
A：Memcached 客户端（ketama）、DynamoDB/Cassandra 的数据分片、Chord DHT、Nginx/Envoy 的一致性哈希负载均衡。Redis Cluster 用的是 16384 槽位的类似思想（槽位表而非环）。

## 8. 总结

一致性哈希 = **有序哈希环 + 顺时针二分查找 + 虚拟节点**：环结构让节点增减只波及一段弧，虚拟节点把负载摊平，`sort.Search` 把查找做到 O(log M)。记住验证方法——`go run consistent_hash.go` 亲眼看 1000 个 key 在 Add/Remove 前后只搬家 30.3% 和 262 个，比背结论可靠。

下一篇 [14_dist_lock](../14_dist_lock/) 看分布式另一道高频题：多客户端竞争 Redis 锁时，TTL 过期与迟到误删如何用一个唯一 value 化解。
