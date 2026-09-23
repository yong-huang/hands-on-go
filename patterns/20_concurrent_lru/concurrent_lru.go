// Concurrent LRU Cache —— 分片并发 LRU 缓存
// 面试高频题: 分片锁 + 读写锁 + 双向链表, 高并发 O(1) get/put

package main

import (
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
)

// ── 1. 双向链表节点 ──

type entry struct {
	key, value string
	prev, next *entry
}

// ── 2. LRU 双向链表 (sentinel head/tail) ──

type lruList struct {
	head, tail *entry
}

func newLRUList() lruList {
	sentinel := &entry{}
	sentinel.next = sentinel
	sentinel.prev = sentinel
	return lruList{head: sentinel, tail: sentinel}
}

func (l *lruList) pushFront(e *entry) {
	e.prev = l.head
	e.next = l.head.next
	l.head.next.prev = e
	l.head.next = e
}

func (l *lruList) remove(e *entry) {
	e.prev.next = e.next
	e.next.prev = e.prev
	e.prev, e.next = nil, nil
}

func (l *lruList) moveToFront(e *entry) {
	l.remove(e)
	l.pushFront(e)
}

// popBack removes and returns the LRU entry (tail.prev).
func (l *lruList) popBack() *entry {
	if l.head.next == l.tail {
		return nil
	}
	victim := l.tail.prev
	l.remove(victim)
	return victim
}

// ── 3. 分片 (shard) ──

type shard struct {
	mu    sync.RWMutex
	cap   int
	list  lruList
	items map[string]*entry
}

func newShard(cap int) shard {
	l := newLRUList()
	return shard{cap: cap, list: l, items: make(map[string]*entry)}
}

// ── 4. 并发 LRU ──

type ConcurrentLRU struct {
	shards     []shard
	shardCount uint32
}

func NewConcurrentLRU(shardCount, perShardCap int) *ConcurrentLRU {
	shards := make([]shard, shardCount)
	for i := range shards {
		shards[i] = newShard(perShardCap)
	}
	return &ConcurrentLRU{shards: shards, shardCount: uint32(shardCount)}
}

func (c *ConcurrentLRU) getShard(key string) *shard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &c.shards[h.Sum32()%c.shardCount]
}

// Get acquires read lock for lookup, upgrades to write lock for moveToFront.
func (c *ConcurrentLRU) Get(key string) (string, bool) {
	s := c.getShard(key)
	s.mu.RLock()
	e, ok := s.items[key]
	if !ok {
		s.mu.RUnlock()
		return "", false
	}
	val := e.value
	s.mu.RUnlock()

	// Upgrade to write lock for moveToFront promotion.
	s.mu.Lock()
	if e2, ok2 := s.items[key]; ok2 {
		s.list.moveToFront(e2)
		val = e2.value
	}
	s.mu.Unlock()
	return val, true
}

// Put acquires write lock and inserts or updates the key.
func (c *ConcurrentLRU) Put(key, value string) {
	s := c.getShard(key)
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.items[key]; ok {
		e.value = value
		s.list.moveToFront(e)
		return
	}
	if len(s.items) >= s.cap {
		victim := s.list.popBack()
		if victim != nil {
			delete(s.items, victim.key)
		}
	}
	e := &entry{key: key, value: value}
	s.list.pushFront(e)
	s.items[key] = e
}

// ── 5. 并发压测演示 ──

func demo() {
	const numGoroutines = 100
	const opsPerGoroutine = 10000

	lru := NewConcurrentLRU(32, 256)
	var wg sync.WaitGroup
	var hits, misses int64

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id%20, j%500)
				val := fmt.Sprintf("val-%d", j)
				if _, ok := lru.Get(key); ok {
					atomic.AddInt64(&hits, 1)
				} else {
					atomic.AddInt64(&misses, 1)
				}
				lru.Put(key, val)
			}
		}(i)
	}

	wg.Wait()
	elapsed := time.Since(start)
	total := numGoroutines * opsPerGoroutine

	fmt.Printf("=== Concurrent LRU Benchmark ===\n\n")
	fmt.Printf("Goroutines  : %d\n", numGoroutines)
	fmt.Printf("Ops/goroutine: %d\n", opsPerGoroutine)
	fmt.Printf("Total ops    : %d\n", total)
	fmt.Printf("Hits         : %d\n", hits)
	fmt.Printf("Misses       : %d\n", misses)
	fmt.Printf("Hit rate     : %.2f%%\n", float64(hits)/float64(hits+misses)*100)
	fmt.Printf("Elapsed      : %v\n", elapsed)
	fmt.Printf("Throughput   : %.0f ops/sec\n\n", float64(total*2)/elapsed.Seconds())

	// Sequential correctness check: use 1 shard so total capacity = perShardCap.
	fmt.Println("=== Correctness Check ===")
	clru := NewConcurrentLRU(1, 4)
	clru.Put("a", "1")
	clru.Put("b", "2")
	clru.Put("c", "3")
	clru.Put("d", "4")
	clru.Put("e", "5") // evicts "a"

	if v, ok := clru.Get("a"); ok {
		fmt.Printf("FAIL: expected 'a' evicted, got %q\n", v)
	} else {
		fmt.Println("PASS: 'a' was evicted (capacity=4, 5th insert)")
	}
	if v, ok := clru.Get("e"); ok && v == "5" {
		fmt.Println("PASS: 'e'='5' present")
	} else {
		fmt.Println("FAIL: expected 'e'='5'")
	}
	clru.Get("b") // promote b
	clru.Put("f", "6") // evicts "c" (LRU since b was promoted)
	if v, ok := clru.Get("c"); ok {
		fmt.Printf("FAIL: expected 'c' evicted, got %q\n", v)
	} else {
		fmt.Println("PASS: 'c' was evicted after 'b' was promoted")
	}
}

// ── 6. 主入口 ──

func main() {
	demo()
}
