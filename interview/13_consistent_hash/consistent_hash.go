// Consistent Hashing —— 一致性哈希
// 面试高频题: 哈希环、虚拟节点、最小数据迁移

package main

import (
	"fmt"
	"hash/crc32"
	"sort"
)

// ── 1. 一致性哈希核心 ──

// ConsistentHash 一致性哈希结构
type ConsistentHash struct {
	replicas int              // 每个物理节点的虚拟节点数
	ring     []uint32         // 哈希环上所有虚拟节点的哈希值（有序）
	hashMap  map[uint32]string // 哈希值 → 物理节点名
	keys     map[string]bool   // 当前物理节点集合
}

// NewConsistentHash 创建一致性哈希实例，replicas 为虚拟节点倍数
func NewConsistentHash(replicas int) *ConsistentHash {
	return &ConsistentHash{
		replicas: replicas,
		hashMap:  make(map[uint32]string),
		keys:     make(map[string]bool),
	}
}

// hash 使用 crc32 计算字符串的哈希值
func hash(key string) uint32 {
	return crc32.ChecksumIEEE([]byte(key))
}

// Add 向哈希环添加一个物理节点（含 replicas 个虚拟节点）
func (ch *ConsistentHash) Add(node string) {
	ch.keys[node] = true
	for i := 0; i < ch.replicas; i++ {
		vkey := fmt.Sprintf("%s#%d", node, i)
		h := hash(vkey)
		ch.ring = append(ch.ring, h)
		ch.hashMap[h] = node
	}
	sort.Slice(ch.ring, func(i, j int) bool { return ch.ring[i] < ch.ring[j] })
}

// Remove 从哈希环移除一个物理节点及其所有虚拟节点
func (ch *ConsistentHash) Remove(node string) {
	if !ch.keys[node] {
		return
	}
	delete(ch.keys, node)
	for i := 0; i < ch.replicas; i++ {
		vkey := fmt.Sprintf("%s#%d", node, i)
		h := hash(vkey)
		delete(ch.hashMap, h)
	}
	ch.rebuildRing()
}

// rebuildRing 重建有序哈希环
func (ch *ConsistentHash) rebuildRing() {
	ch.ring = ch.ring[:0]
	for h := range ch.hashMap {
		ch.ring = append(ch.ring, h)
	}
	sort.Slice(ch.ring, func(i, j int) bool { return ch.ring[i] < ch.ring[j] })
}

// Get 查找 key 对应的物理节点（顺时针最近）
func (ch *ConsistentHash) Get(key string) string {
	if len(ch.ring) == 0 {
		return ""
	}
	h := hash(key)
	idx := sort.Search(len(ch.ring), func(i int) bool { return ch.ring[i] >= h })
	if idx == len(ch.ring) {
		idx = 0 // 环形回绕
	}
	return ch.hashMap[ch.ring[idx]]
}

// Nodes 返回当前所有物理节点名（按字母序）
func (ch *ConsistentHash) Nodes() []string {
	nodes := make([]string, 0, len(ch.keys))
	for n := range ch.keys {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return nodes
}

// ── 2. 辅助函数 ──

// distributeKeys 将 numKeys 个 key 分布到哈希环上，返回 node→count 和 mapping
func distributeKeys(ch *ConsistentHash, numKeys int) (map[string]int, []string) {
	counts := make(map[string]int)
	mapping := make([]string, numKeys)
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		node := ch.Get(key)
		counts[node]++
		mapping[i] = node
	}
	return counts, mapping
}

// countMigrated 比较新旧映射，返回迁移的 key 数量
func countMigrated(oldMapping, newMapping []string) int {
	migrated := 0
	for i := range oldMapping {
		if oldMapping[i] != newMapping[i] {
			migrated++
		}
	}
	return migrated
}

// ── 3. 演示入口 ──

func main() {
	fmt.Println("=== Consistent Hashing Demo ===\n")

	ch := NewConsistentHash(50) // 每节点 50 个虚拟节点

	// Step 1: 添加 3 个节点，分布 1000 个 key
	fmt.Println("1. Add 3 nodes (A, B, C), distribute 1000 keys:")
	for _, node := range []string{"A", "B", "C"} {
		ch.Add(node)
		fmt.Printf("   Added node %s (replicas=%d)\n", node, ch.replicas)
	}
	counts, mapping3 := distributeKeys(ch, 1000)
	for _, node := range []string{"A", "B", "C"} {
		fmt.Printf("     Node %s: %d keys\n", node, counts[node])
	}
	fmt.Printf("     Total ring points: %d\n\n", len(ch.ring))

	// Step 2: 添加节点 D，统计迁移率
	fmt.Println("2. Add node D, measure migration rate:")
	ch.Add("D")
	newCounts, newMapping := distributeKeys(ch, 1000)
	migrated := countMigrated(mapping3, newMapping)
	fmt.Println("   Distribution after adding D:")
	for _, node := range ch.Nodes() {
		fmt.Printf("     Node %s: %d keys\n", node, newCounts[node])
	}
	fmt.Printf("   Migrated keys: %d / 1000 (%.1f%%)\n", migrated, float64(migrated)/10)
	fmt.Printf("   Total ring points: %d\n\n", len(ch.ring))

	// Step 3: 移除节点 A，统计影响
	fmt.Println("3. Remove node A:")
	preRemoveMapping := make([]string, 1000)
	for i := 0; i < 1000; i++ {
		preRemoveMapping[i] = ch.Get(fmt.Sprintf("key-%d", i))
	}

	ch.Remove("A")
	postCounts, _ := distributeKeys(ch, 1000)
	aKeys := 0
	for _, n := range preRemoveMapping {
		if n == "A" {
			aKeys++
		}
	}
	fmt.Println("   Distribution after removing A:")
	for _, node := range ch.Nodes() {
		fmt.Printf("     Node %s: %d keys\n", node, postCounts[node])
	}
	fmt.Printf("   Keys previously on A (redistributed): %d\n", aKeys)
	fmt.Printf("   Only A's keys migrated, B/C/D unaffected\n\n")

	// Step 4: 总结
	fmt.Println("4. Key Takeaways:")
	fmt.Println("   - Adding a node migrates ~1/N keys (N = node count)")
	fmt.Println("   - Removing a node only affects its own keys")
	fmt.Println("   - Virtual nodes improve load balance significantly")
	fmt.Printf("   - Ring has %d virtual points for %d physical nodes\n", len(ch.ring), len(ch.keys))
}
