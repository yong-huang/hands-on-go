// Trie —— 前缀树 / 字典树（Autocomplete）
// 面试高频题: 前缀树 Insert/Search/StartsWith O(L)、DFS 自动补全

package main

import (
	"fmt"
	"sort"
	"strings"
)

// ── 1. Trie 核心 ──

// TrieNode 前缀树节点
type TrieNode struct {
	children map[rune]*TrieNode // 子节点映射
	isEnd    bool               // 标记是否为单词结尾
}

// Trie 前缀树
type Trie struct {
	root *TrieNode
}

// NewTrie 创建前缀树
func NewTrie() *Trie {
	return &Trie{root: &TrieNode{children: make(map[rune]*TrieNode)}}
}

// Insert 插入单词，O(L) 其中 L 为单词长度
func (t *Trie) Insert(word string) {
	node := t.root
	for _, ch := range word {
		if _, ok := node.children[ch]; !ok {
			node.children[ch] = &TrieNode{children: make(map[rune]*TrieNode)}
		}
		node = node.children[ch]
	}
	node.isEnd = true
}

// Search 精确搜索单词是否存在
func (t *Trie) Search(word string) bool {
	node := t.findNode(word)
	return node != nil && node.isEnd
}

// StartsWith 是否存在以 prefix 为前缀的单词
func (t *Trie) StartsWith(prefix string) bool {
	return t.findNode(prefix) != nil
}

// findNode 查找前缀对应的节点，辅助方法
func (t *Trie) findNode(prefix string) *TrieNode {
	node := t.root
	for _, ch := range prefix {
		if _, ok := node.children[ch]; !ok {
			return nil
		}
		node = node.children[ch]
	}
	return node
}

// Autocomplete 返回以 prefix 为前缀的最多 k 个单词（DFS 收集后按字典序排序）
func (t *Trie) Autocomplete(prefix string, k int) []string {
	node := t.findNode(prefix)
	if node == nil {
		return nil
	}
	var results []string
	var dfs func(n *TrieNode, path []rune)
	dfs = func(n *TrieNode, path []rune) {
		if len(results) >= k {
			return
		}
		if n.isEnd {
			results = append(results, prefix+string(path))
		}
		// 按字符字典序遍历，保证结果有序
		chars := make([]rune, 0, len(n.children))
		for ch := range n.children {
			chars = append(chars, ch)
		}
		sort.Slice(chars, func(i, j int) bool { return chars[i] < chars[j] })
		for _, ch := range chars {
			dfs(n.children[ch], append(path, ch))
		}
	}
	dfs(node, nil)
	if len(results) > k {
		results = results[:k]
	}
	return results
}

// ── 2. 演示 ──

func run_demo() {
	fmt.Println("=== Trie / Autocomplete ===\n")

	trie := NewTrie()

	// 1. 插入单词
	fmt.Println("1. Insert:")
	words := []string{"apple", "app", "application", "apply", "apt", "banana", "band", "bank"}
	for _, w := range words {
		trie.Insert(w)
		fmt.Printf("  Insert(%q)", w)
		fmt.Println()
	}

	// 2. 精确搜索
	fmt.Println("\n2. Search:")
	for _, w := range []string{"app", "apple", "appl", "ban", "banana"} {
		fmt.Printf("  Search(%q) = %v\n", w, trie.Search(w))
	}

	// 3. 前缀搜索
	fmt.Println("\n3. StartsWith:")
	for _, p := range []string{"app", "ap", "ban", "bat"} {
		fmt.Printf("  StartsWith(%q) = %v\n", p, trie.StartsWith(p))
	}

	// 4. 自动补全
	fmt.Println("\n4. Autocomplete:")
	prefixes := []string{"app", "ap", "ban", "b", "c"}
	for _, p := range prefixes {
		results := trie.Autocomplete(p, 5)
		fmt.Printf("  Autocomplete(%q, 5) = [%s]\n", p, strings.Join(results, ", "))
	}

	// 5. 限制返回数量
	fmt.Println("\n5. Autocomplete (k=2):")
	results := trie.Autocomplete("app", 2)
	fmt.Printf("  Autocomplete(%q, 2) = [%s]\n", "app", strings.Join(results, ", "))

	// 6. 空前缀匹配所有
	fmt.Println("\n6. Autocomplete (empty prefix, k=3):")
	results = trie.Autocomplete("", 3)
	fmt.Printf("  Autocomplete(%q, 3) = [%s]\n", "", strings.Join(results, ", "))
}

// ── 3. 主入口 ──

func main() {
	run_demo()
}
