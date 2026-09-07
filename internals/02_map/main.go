package main

import (
	"encoding/json"
	"flag"
	"fmt"
)

// quiet 抑制演示打印（-json 模式下使用）
var quiet = flag.Bool("json", false, "输出 JSON 状态快照（供 viz.py）")

// =============================================================================
// 02_map：Map 底层实现（教学模拟版）
//
// 为什么不直接读真实内存？
//   旧版代码用 unsafe 硬读 runtime 的 hmap 结构。但 map 的内部布局是
//   "实现细节"，不是稳定契约：Go 1.24 起运行时已改用 Swiss Table
//   （internal/runtime/maps，开放寻址 + 8 槽组 + 控制字），老的 hmap/bmap
//   结构体不存在了。硬读会读到哈希种子的随机字节，输出全是垃圾。
//
// 本程序改为"模拟"经典 hmap/bmap 设计（Go 1.23 及更早），用纯 Go 结构
// 复刻它的行为，从而展示哈希表的核心概念：
//   哈希 → 低 B 位定位桶 → tophash 快速匹配 → 溢出链解决冲突
//   → 负载因子 > 6.5 触发扩容（桶数翻倍 + 全量重哈希）
// 这些概念在 hmap 和 Swiss Table 两种实现里完全通用，且不受 Go 版本影响。
// =============================================================================

// bucketSize 每个桶最多容纳的键值对数量（与真实运行时一致，为 8）
const bucketSize = 8

// maxLoadFactor 负载因子阈值：count / 桶数 > 6.5 时触发扩容
const maxLoadFactor = 6.5

// bmap 模拟运行时的一个桶
type bmap struct {
	tophash [bucketSize]uint8  // 每个 key 哈希的高 8 位，用于先比对、少做字符串比较
	keys    [bucketSize]string // 简化：key 用 string（真实运行时时紧凑内存布局）
	vals    [bucketSize]string // 简化：value 用 string
	used    int                // 本桶已占用的槽位数
	next    *bmap              // 溢出桶：桶满了就把新桶挂到链表尾部
}

// hmap 模拟 map 的顶层结构
type hmap struct {
	count    int     // 元素总数
	B        int     // 桶数 = 1 << B
	buckets  []*bmap // 桶数组（长度为 1<<B）
	hashSeed uint32  // 哈希种子（真实运行时每个 map 随机生成）
}

// newHmap 创建模拟 map
func newHmap(B int) *hmap {
	return &hmap{
		B:        B,
		buckets:  make([]*bmap, 1<<B),
		hashSeed: 0x9e3779b9, // 固定种子：保证每次运行结果一致，便于讲解
	}
}

// hash FNV-1a 简化版 32 位哈希（确定性，便于讲解）
func (m *hmap) hash(key string) uint32 {
	h := m.hashSeed
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 0x01000193 // FNV prime
	}
	return h
}

// bucketIdx 桶下标：取哈希的低 B 位（桶数是 2 的幂，用掩码而非取模）
func (m *hmap) bucketIdx(h uint32) int {
	return int(h) & (len(m.buckets) - 1)
}

// tophashOf 快速匹配值：取哈希的高 8 位
func tophashOf(h uint32) uint8 {
	return uint8(h >> 24)
}

// bucketCount 桶的总数（2 的 B 次方）
func (m *hmap) bucketCount() int { return 1 << m.B }

// shouldGrow 是否该扩容：判断"再插一个就超过负载因子阈值"（与运行时逻辑一致）
func (m *hmap) shouldGrow() bool {
	return float64(m.count+1)/float64(m.bucketCount()) > maxLoadFactor
}

// insertRaw 把 key 放入对应桶（不检查扩容、不计数），供插入和重哈希复用
func (m *hmap) insertRaw(h uint32, key, val string) {
	th := tophashOf(h)
	idx := m.bucketIdx(h)
	if m.buckets[idx] == nil {
		m.buckets[idx] = &bmap{}
	}

	// 沿溢出链找空位；满了就新建溢出桶
	cur := m.buckets[idx]
	for cur.used == bucketSize {
		if cur.next == nil {
			cur.next = &bmap{}
			if !*quiet {
				fmt.Printf("     桶 %d 已满 → 新建溢出桶，挂到链表尾部\n", idx)
			}
		}
		cur = cur.next
	}

	i := cur.used
	cur.tophash[i] = th
	cur.keys[i] = key
	cur.vals[i] = val
	cur.used++
}

// insert 插入键值对（含扩容判定）
func (m *hmap) insert(key, val string) {
	if m.shouldGrow() {
		m.grow()
	}
	h := m.hash(key)
	m.insertRaw(h, key, val)
	m.count++
	if !*quiet {
		fmt.Printf("  插入 %-14q hash=0x%08x tophash=0x%02x bucket=%d\n",
			key, h, tophashOf(h), m.bucketIdx(h))
	}
}

// grow 扩容：桶数翻倍，并把所有旧元素重新哈希到新桶数组
func (m *hmap) grow() {
	oldBuckets := m.buckets
	oldB := m.B
	m.B++
	m.buckets = make([]*bmap, 1<<m.B)
	if !*quiet {
		fmt.Printf("  ▶ 触发扩容：再插 1 个就会超过 6.5×%d 的负载因子，B: %d -> %d\n",
			m.bucketCount(), oldB, m.B)
	}

	for _, b := range oldBuckets {
		for cur := b; cur != nil; cur = cur.next {
			for i := 0; i < cur.used; i++ {
				m.insertRaw(m.hash(cur.keys[i]), cur.keys[i], cur.vals[i])
			}
		}
	}
	if !*quiet {
		fmt.Printf("    重新哈希 %d 个元素到 %d 个桶\n", m.count, 1<<m.B)
	}
}

// lookup 查找 key，展示 tophash 预筛 + 溢出链遍历的过程
func (m *hmap) lookup(key string) (string, bool) {
	h := m.hash(key)
	th := tophashOf(h)
	idx := m.bucketIdx(h)
	fmt.Printf("  查找 %-14q hash=0x%08x tophash=0x%02x → bucket=%d\n", key, h, th, idx)

	b := m.buckets[idx]
	probes := 0
	for cur := b; cur != nil; cur = cur.next {
		for i := 0; i < cur.used; i++ {
			probes++
			if cur.tophash[i] == th && cur.keys[i] == key {
				fmt.Printf("  命中：bucket=%d slot=%d，比较了 %d 个槽位\n", idx, i, probes)
				return cur.vals[i], true
			}
		}
	}
	fmt.Printf("  未命中：比较了 %d 个槽位\n", probes)
	return "", false
}

// dump 打印当前内部状态
func (m *hmap) dump(label string) {
	n := m.bucketCount()
	fmt.Printf("  [状态] %s：count=%d  B=%d  桶数=%d  负载因子=%.2f（阈值 6.5）\n",
		label, m.count, m.B, n, float64(m.count)/float64(n))
}

func main() {
	flag.Parse()
	if *quiet {
		fmt.Println(mapJSON())
		return
	}

	fmt.Println("========== 1. 哈希过程：一个 key 怎么找到它的桶 ==========")
	m := newHmap(2) // B=2 → 4 个桶
	for _, k := range []string{"hello", "world", "go", "metal"} {
		h := m.hash(k)
		fmt.Printf("  %-8q hash=0x%08x  tophash(高8位)=0x%02x  桶下标(低2位)=%d\n",
			k, h, tophashOf(h), m.bucketIdx(h))
	}

	fmt.Println("\n========== 2. 插入与桶布局 ==========")
	m2 := newHmap(1) // B=1 → 2 个桶，每个桶 8 个槽，共 16 槽
	fmt.Println("初始：B=1，2 个桶。负载因子阈值 = 6.5×2 = 13，插到第 14 个会扩容")
	for i := 0; i < 15; i++ {
		m2.insert(fmt.Sprintf("key_%02d", i), fmt.Sprintf("v%d", i))
	}
	m2.dump("插入 15 个 key")

	fmt.Println("\n========== 3. 查找过程 ==========")
	m2.lookup("key_05")
	m2.lookup("key_05") // 重复查找观察不同桶
	m2.lookup("missing")

	fmt.Println("\n========== 4. 哈希冲突与溢出桶 ==========")
	m3 := newHmap(4) // B=4 → 16 个桶，容量 128，插 12 个不会触发扩容
	fmt.Println("挑出 12 个哈希后落在同一个桶的 key，观察溢出链：")
	collide := findCollidingKeys(m3, 12)
	for _, k := range collide {
		m3.insert(k, "x")
	}
	m3.dump("冲突演示")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. map 顶层是 hmap：count（元素数）+ B（对数桶数）+ buckets（桶数组）")
	fmt.Println("2. 定位 key：hash → 低 B 位得桶下标，高 8 位得 tophash 用于快速预筛")
	fmt.Println("3. 冲突解决：桶满后通过溢出桶（链表）解决，最坏退化为链表")
	fmt.Println("4. 扩容条件：负载因子(count/桶数) > 6.5 → 桶数翻倍 + 全量重哈希")
	fmt.Println("5. 为什么用 2 的幂：桶下标 = hash & (2^B - 1)，位运算替代取模")
	fmt.Println("6. 现代 Go（1.24+）改用 Swiss Table（开放寻址+SIMD 匹配），")
	fmt.Println("   但桶定位、负载因子、扩容重哈希这些核心思想完全一致")
}

// findCollidingKeys 找出 n 个哈希后落在同一个桶的 key（B 固定，确定性哈希）
func findCollidingKeys(m *hmap, n int) []string {
	groups := make(map[int][]string)
	for i := 0; i < 3000; i++ {
		k := fmt.Sprintf("collision_%04d", i)
		idx := m.bucketIdx(m.hash(k))
		groups[idx] = append(groups[idx], k)
	}
	var best []string
	for _, ks := range groups {
		if len(ks) > len(best) {
			best = ks
		}
	}
	if len(best) > n {
		best = best[:n]
	}
	return best
}

// =============================================================================
// JSON 状态快照：供 Python viz.py 生成可视化 PNG（-json 标志）
// =============================================================================

// bucketUsed 统计某个桶整条溢出链的元素总数
func bucketUsed(m *hmap, i int) int {
	n := 0
	for b := m.buckets[i]; b != nil; b = b.next {
		n += b.used
	}
	return n
}

// mapJSON 复刻文本演示的两个场景（插 15 个 key / 冲突链），输出 JSON 快照
func mapJSON() string {
	type seg struct {
		Used    int      `json:"used"`
		Tophash []int    `json:"tophash"` // 用 []int 而非 []uint8：[]uint8 会被 json 当 []byte 编码成 base64
		Keys    []string `json:"keys"`
	}
	type bucketState struct {
		Count    int     `json:"count"`
		B        int     `json:"b"`
		Buckets  int     `json:"buckets"`
		Load     float64 `json:"load"`
		Occupied []int   `json:"occupied"` // 每桶（含溢出）元素数
		Busiest  int     `json:"busiest"`  // 元素最多的桶下标
		Chain    []seg   `json:"chain"`    // 最忙桶的溢出链
	}
	snapshot := func(m *hmap) bucketState {
		occ := make([]int, m.bucketCount())
		best, bestLen := -1, 0
		for i := range occ {
			occ[i] = bucketUsed(m, i)
			if occ[i] > bestLen {
				bestLen, best = occ[i], i
			}
		}
		var chain []seg
		for b := m.buckets[best]; b != nil; b = b.next {
			ths := make([]int, b.used)
			for i := 0; i < b.used; i++ {
				ths[i] = int(b.tophash[i])
			}
			chain = append(chain, seg{
				Used:    b.used,
				Tophash: ths,
				Keys:    append([]string{}, b.keys[:b.used]...),
			})
		}
		return bucketState{
			Count: m.count, B: m.B, Buckets: m.bucketCount(),
			Load:     float64(m.count) / float64(m.bucketCount()),
			Occupied: occ, Busiest: best, Chain: chain,
		}
	}

	m2 := newHmap(1)
	for i := 0; i < 15; i++ {
		m2.insert(fmt.Sprintf("key_%02d", i), fmt.Sprintf("v%d", i))
	}
	m3 := newHmap(4)
	for _, k := range findCollidingKeys(m3, 12) {
		m3.insert(k, "x")
	}

	out := struct {
		M2 bucketState `json:"m2"`
		M3 bucketState `json:"m3"`
	}{snapshot(m2), snapshot(m3)}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}
