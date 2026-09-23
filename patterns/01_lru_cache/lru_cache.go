// LRU Cache —— 最近最少使用缓存
// 面试高频题: 哈希表 + 双向链表、O(1) get/put

package main

import (
	"container/list"
	"fmt"
	"math/rand"
)

// ── 1. LRU 核心 ──

type entry struct{ key, value int }

type LRUCache struct {
	cap  int
	m    map[int]*list.Element
	order *list.List
}

func New(cap int) *LRUCache {
	return &LRUCache{cap: cap, m: make(map[int]*list.Element), order: list.New()}
}

func (c *LRUCache) Get(key int) (int, bool) {
	if e, ok := c.m[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*entry).value, true
	}
	return 0, false
}

func (c *LRUCache) Put(key, value int) {
	if e, ok := c.m[key]; ok {
		e.Value.(*entry).value = value
		c.order.MoveToFront(e)
		return
	}
	if c.order.Len() >= c.cap {
		b := c.order.Back()
		c.order.Remove(b)
		delete(c.m, b.Value.(*entry).key)
	}
	e := c.order.PushFront(&entry{key, value})
	c.m[key] = e
}

func (c *LRUCache) Show() {
	fmt.Print("  Cache: ")
	for e := c.order.Front(); e != nil; e = e.Next() {
		v := e.Value.(*entry)
		fmt.Printf("[%d:%d] ", v.key, v.value)
	}
	fmt.Println()
}

// ── 2. 主入口 ──

func main() {
	fmt.Println("=== LRU Cache ===\n")

	c := New(3)

	fmt.Println("1. Put:")
	for _, kv := range [][2]int{{1, 10}, {2, 20}, {3, 30}} {
		c.Put(kv[0], kv[1])
		fmt.Printf("  Put(%d,%d)  ", kv[0], kv[1])
		c.Show()
	}

	fmt.Println("\n2. 淘汰:")
	c.Put(4, 40)
	fmt.Print("  Put(4,40) → 淘汰key=1  ")
	c.Show()

	fmt.Println("\n3. Get:")
	for _, k := range []int{2, 4, 1} {
		v, ok := c.Get(k)
		s := "命中"
		if !ok {
			s = "未命中"
		}
		fmt.Printf("  Get(%d)=%d (%s)  ", k, v, s)
		c.Show()
	}

	fmt.Println("\n4. 随机测试:")
	rand.Seed(42)
	for i := 0; i < 10; i++ {
		if rand.Intn(2) == 0 {
			k := rand.Intn(5) + 1
			c.Get(k)
		} else {
			c.Put(rand.Intn(5)+1, rand.Intn(100))
		}
	}
	c.Show()
}
