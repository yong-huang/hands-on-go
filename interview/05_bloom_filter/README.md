# 05 · Bloom Filter：位数组 + k 个哈希的"可能存在"判定

> Bloom Filter（布隆过滤器）是一种**空间高效的概率型数据结构**，用来回答"这个元素可能见过吗"：它**绝不会漏报**（Add 过的元素查询一定返回 true），但**可能误报**（没加过的元素也可能返回 true）。这个不对称的代价换来了比 HashSet 低 50~100 倍的内存占用，因此广泛用于缓存穿透防护、海量去重、垃圾邮件过滤。本实验用 Go 实现完整的 Bloom Filter（FNV-1a 双重哈希），并实测误判率与理论公式的差距。

## 1. 为什么需要它

缓存穿透是典型场景：恶意请求一个数据库里根本不存在的 key，缓存永远不命中，每次都打到数据库。如果能在缓存前放一个 Bloom Filter 记录"所有存在的 key"，查询返回 false 就直接拒绝——虽然偶尔误放行（假阳性），但**永远不会错杀**（假阴性），这正是防御场景需要的性质。

它之所以省内存，是因为**只存"指纹"不存元素本身**：一个 10000 位的位数组（1.25 KB）就能以约 1% 的误判率容纳 1000 个任意长度的字符串。代价是：不能删除元素、不能枚举内容、误判率随填充度上升。

## 2. 总览：核心机制一图看懂

![Bloom Filter：元素经 k 个哈希置位，查询按位判定](images/bloom.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/05_bloom_filter/images/bloom.html)
> （或本地打开 [`images/bloom.html`](images/bloom.html)）。

**心智模型一句话**：Add 和查询都走同一条 `hash(item) → k 个位数组索引` 的路径——Add 把 k 个位全置 1；查询发现**任一位为 0 就一定不存在**（那个位从没被置过 1），**全为 1 只能说可能存在**（可能都是别人置的）。

## 3. 快速开始

```bash
cd interview/05_bloom_filter
go run bloom_filter.go
```

预期输出（节选，误判率数值每次运行基本一致，大规模测试的实测值会小幅波动）：

```text
=== Bloom Filter Demo ===

Filter: size=10000 bits, numHashes=7

--- Adding items ---
  Add("apple") ... Add("elderberry")        # 5 个已存在元素全部 MayContain=true

--- Query non-existing items (false positive test) ---
  MayContain("grape") = false ...           # 10 个未加入元素全部 false

=== Large-Scale False Positive Test ===
Added: 10000 items, Filter bits: 100000, Hash functions: 7
False positives: 432 / 10000 = 4.32%
Estimated FP rate (formula): 0.8194%

=== Hash Function Count Trade-off ===
  k= 1  bits=100000  actual FP=9.08%  estimated FP=9.52%
  k= 3  bits=100000  actual FP=3.39%  estimated FP=1.74%
  k= 7  bits=100000  actual FP=4.36%  estimated FP=0.82%
```

注意最后两段：**实测误判率（4.32%）明显高于公式估计（0.82%）**——本实现用双重哈希 `h1 + i*h2`，k 个索引彼此相关而非独立，理论公式"k 个独立哈希"的前提不严格成立。这是面试时展示"知其所以然"的好素材。

## 4. 核心概念

### 4.1 位数组：[]uint64 而非 []bool

```go
type BloomFilter struct {
    bitArray  []uint64 // 位数组，每个 uint64 存 64 个 bit
    size      uint     // 总位数 m
    numHashes uint     // 哈希函数数量 k
    count     uint     // 已添加元素数 n
}
```

用 `[]uint64` 打包位，内存只有 `[]bool` 的 1/8。置位与检查都是 O(1) 位运算：

```go
func (bf *BloomFilter) setBit(pos uint) { bf.bitArray[pos/64] |= 1 << (pos % 64) }
func (bf *BloomFilter) getBit(pos uint) bool { return bf.bitArray[pos/64]&(1<<(pos%64)) != 0 }
```

### 4.2 双重哈希：2 次哈希生成 k 个索引

```go
h1 := fnvHash(item, 0)       // FNV-1a，种子 0
h2 := fnvHash(item, 0xBEEF)  // 同一算法，不同种子
indices[i] = (h1 + i*h2) % bf.size
```

相比调用 k 个独立哈希函数，只需 2 次哈希计算，k 个索引由线性组合生成（Kirsch–Mitzenmacher 方案）。副作用见 §3：索引相关性推高实际误判率。

### 4.3 Add 与 MayContain：同一枚硬币的两面

```go
func (bf *BloomFilter) Add(item string) {
    for _, pos := range bf.hash(item) { bf.setBit(pos) }  // k 个位全置 1
    bf.count++
}
func (bf *BloomFilter) MayContain(item string) bool {
    for _, pos := range bf.hash(item) {
        if !bf.getBit(pos) { return false }  // 任一 0 → 一定不存在
    }
    return true                              // 全 1 → 可能存在（可能误报）
}
```

**不会漏判的根因**：位只从 0 变 1、永不清零，所以 Add 过的元素 k 个位永远保持为 1。**会误判的根因**：其它元素的哈希可能恰好覆盖了查询元素的 k 个位，查询者无从区分"我置的 1"和"别人置的 1"。

### 4.4 误判率公式与实测偏差

理论误判率 `FP = (1 - e^(-kn/m))^k`，最优参数 `m = -n·ln(p)/ln(2)²`、`k = (m/n)·ln 2`。本实现固定 `m = 10n`、k=7（接近 n=1000、p≈1% 的理论最优组合 m≈9585、k≈7）。实测却偏差明显：插入满 10000 元素后实际误判 4.32%，公式只给 0.82%——双重哈希的索引相关性是主因，此外哈希把 seed 拼进输入再 FNV（`fmt.Fprintf(h, "%s%d", item, seed)`）也非严格独立。k 折中实验里实际误判在 3~9% 波动、k=3 附近最好，同样偏离了"公式最优点 k=7"。

## 5. 关键字段与坑清单

| 字段/函数 | 含义 | 为什么重要 |
|---|---|---|
| `bitArray []uint64` | 打包位数组 | 空间 1/8 于 `[]bool`；`pos/64` 定字、`pos%64` 定位 |
| `size (m)` | 总位数，代码取 `10n` | 决定误判率下限；太小则位很快被占满 |
| `numHashes (k)` | 哈希个数 | 太少漏置位误判高，太多占满位数组误判也高 |
| `count (n)` | 已添加元素数 | `FalsePositiveRate()` 用 n/m/k 估当前误判率 |
| `hash()` | 双重哈希生成 k 索引 | 相关性使实际 FP 高于理论值（见 §4.4） |

坑清单：

- **不能删除元素**：多个元素共享位，清零会把别人的位也清掉，造成假阴性——这是 Bloom Filter 唯一不可触碰的红线；要删除需换 Counting Bloom Filter（每位变计数器）
- **误判率别照公式报**：双重哈希实现下实测可能是公式估计的数倍，压测后再给结论
- **哈希要引入种子差异**：`fnvHash(item, 0)` 与 `fnvHash(item, 0xBEEF)` 靠 seed 区分 h1/h2；两次同参哈希只会得到同一组索引
- **`m = n*10` 是硬编码默认，k=0 才自动算 k**：换目标误判率需自己按公式改 m

## 6. 文件结构

```text
interview/05_bloom_filter/
├── README.md                # 本文件：原理 + 运行指引 + 深入要点
├── bloom_filter.go          # 主教材：位数组 → 双重哈希 → Add/MayContain → 误判率实测
└── images/
    ├── bloom.dataflow.json  # 图源：k 个哈希 → 位数组置位 → 查询判定（Typed JSON IR）
    ├── bloom.html           # 交互版：可缩放/高亮连线的自包含页面
    └── bloom.svg            # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：为什么 Bloom Filter 会有误判，却永远不会漏判？**
A：判断依据是"k 个哈希位是否全为 1"。Add 过的元素自己把 k 个位置了 1，且位从不清零，所以一定返回 true（无假阴性）；但其他元素可能碰巧把这些位也置成了 1，查询者无法区分 1 的来源，于是产生假阳性。

**Q：为什么不能删除元素？怎么解决？**
A：删除需要把位清 0，但一个位可能被多个元素共享，清 0 会让别人被"漏判"。变体 Counting Bloom Filter 把每个位换成计数器，删除时减 1，代价是空间从 1 bit/位涨到 4 bit 左右。

**Q：m 和 k 怎么选？**
A：给定预期元素数 n 和目标误判率 p：`m = -n·ln(p)/ln(2)²`、`k = (m/n)·ln 2`。n=10000、p=1% 时 m≈95851 bits（约 12 KB）、k≈7。本实现简化为 m=10n、k=7。

**Q：空间优势有多大？**
A：存 100 万个 URL，HashSet（存字符串）约 50~100 MB，Bloom Filter（p=1%）约 1.2 MB——50~100 倍的差距，因为只存位指纹、不存元素本身。

**Q：手写实现最容易错在哪？**
A：① 位打包的 `pos/64` 与 `pos%64` 写反；② h1/h2 忘记用不同种子，k 个索引退化为重复值；③ 误判率公式套用在双重哈希实现上不成立（实测 4.32% vs 公式 0.82%），要说明前提是独立哈希。

## 8. 总结

Bloom Filter 的全部性质都从"位数组只置位不清零 + k 个哈希位共同作证"推出来：不清零给了"绝不漏判"的保证，也埋下"别人置的 1 无法区分"的误判。实测（`go run bloom_filter.go`）显示双重哈希实现的误判率会高于独立哈希下的理论公式——背公式不如跑一次程序。要支持删除，看 Counting Bloom Filter；下一篇 [06_ring_buffer](../06_ring_buffer/) 看另一个用取模复用固定数组的结构：环形队列的 head/tail 回绕与满判别。
