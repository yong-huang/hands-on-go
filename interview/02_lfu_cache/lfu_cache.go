// LFU Cache —— 最不经常使用缓存
// 面试高频题: 频率桶 + 双向链表、min_freq 跟踪、O(1) get/put

package main

import (
	"container/list"
	"fmt"
)

// ── 1. LFU 核心 ──

type item struct {
	key, value, freq int
	elem             *list.Element // 指向所在频率桶链表中的节点
}

type LFUCache struct {
	cap      int
	minFreq  int
	items    map[int]*item
	freqList map[int]*list.List // freq → 双向链表
}

func NewLFU(cap int) *LFUCache {
	return &LFUCache{
		cap:      cap,
		items:    make(map[int]*item),
		freqList: make(map[int]*list.List),
	}
}

func (c *LFUCache) Get(key int) (int, bool) {
	it, ok := c.items[key]
	if !ok {
		return 0, false
	}
	c.incrementFreq(it)
	return it.value, true
}

func (c *LFUCache) Put(key, value int) {
	if c.cap == 0 {
		return
	}
	if it, ok := c.items[key]; ok {
		it.value = value
		c.incrementFreq(it)
		return
	}
	// 容量满，淘汰 minFreq 桶的最久未使用（链表尾部）
	if len(c.items) >= c.cap {
		l := c.freqList[c.minFreq]
		back := l.Back()
		evict := back.Value.(*item)
		l.Remove(back)
		delete(c.items, evict.key)
	}
	// 插入新 item，freq=1
	it := &item{key: key, value: value, freq: 1}
	if c.freqList[1] == nil {
		c.freqList[1] = list.New()
	}
	it.elem = c.freqList[1].PushFront(it)
	c.items[key] = it
	c.minFreq = 1
}

// 频率 +1：从旧桶移到新桶
func (c *LFUCache) incrementFreq(it *item) {
	oldFreq := it.freq
	l := c.freqList[oldFreq]
	l.Remove(it.elem)
	if l.Len() == 0 {
		delete(c.freqList, oldFreq)
		if c.minFreq == oldFreq {
			c.minFreq++
		}
	}
	it.freq++
	if c.freqList[it.freq] == nil {
		c.freqList[it.freq] = list.New()
	}
	it.elem = c.freqList[it.freq].PushFront(it)
}

func (c *LFUCache) Show() {
	fmt.Printf("  LFU(cap=%d, minFreq=%d, size=%d):\n", c.cap, c.minFreq, len(c.items))
	for f := 1; f <= 10; f++ {
		l := c.freqList[f]
		if l == nil || l.Len() == 0 {
			continue
		}
		fmt.Print("    freq=", f, ": ")
		for e := l.Front(); e != nil; e = e.Next() {
			v := e.Value.(*item)
			fmt.Printf("[%d:%d] ", v.key, v.value)
		}
		fmt.Println()
	}
}

// ── 2. 主入口 ──

func main() {
	fmt.Println("=== LFU Cache ===\n")

	c := NewLFU(3)

	fmt.Println("1. Put:")
	for _, kv := range [][2]int{{1, 10}, {2, 20}, {3, 30}} {
		c.Put(kv[0], kv[1])
		fmt.Printf("  Put(%d,%d)\n", kv[0], kv[1])
	}
	c.Show()

	fmt.Println("2. Get 提升频率:")
	c.Get(1) // freq: 1→2
	c.Get(1) // freq: 2→3
	c.Get(2) // freq: 1→2
	fmt.Println("  Get(1) x2, Get(2) x1")
	c.Show()

	fmt.Println("3. 容量满 Put → 淘汰 minFreq 桶尾:")
	c.Put(4, 40) // freq=1 桶有 [3]，淘汰 key=3
	fmt.Println("  Put(4,40) → 淘汰 key=3 (freq=1, 最久未访问)")
	c.Show()

	fmt.Println("4. 同频率淘汰最久未使用:")
	c.Get(4) // freq: 1→2, 现在 freq=2 桶: [2, 4]，2 在前(最近)，4 在后
	c.Put(5, 50)
	fmt.Println("  Get(4) 后 Put(5,50) → 淘汰 key=2 (freq=2 桶尾)")
	c.Show()
}
