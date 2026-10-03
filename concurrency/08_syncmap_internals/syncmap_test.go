package main

import (
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"testing"
)

// 验收① 的协议流转断言：用观测钩子逐步核对 miss 累积与 promote 的锁点行为。
func TestPromoteFlow(t *testing.T) {
	m := NewProtocolMap()
	m.Store("a", 1)
	m.Store("b", 2)
	if got := m.DirtyBuilt.Load(); got != 1 {
		t.Fatalf("DirtyBuilt = %d, want 1 (first new key rebuilds dirty)", got)
	}
	if !m.read.Load().amended {
		t.Fatal("amended should be true after storing a key outside read")
	}

	m.Load("x") // miss 1：1 < len(dirty)=2，不提升
	if got := m.Misses.Load(); got != 1 {
		t.Fatalf("Misses = %d, want 1", got)
	}
	if m.read.Load().amended != true || m.dirty == nil {
		t.Fatal("map promoted too early")
	}
	m.Load("x") // miss 2：2 == len(dirty)=2，达到阈值
	if got := m.Promotes.Load(); got != 1 {
		t.Fatalf("Promotes = %d, want 1", got)
	}
	if m.read.Load().amended {
		t.Fatal("amended should be false after promotion")
	}
	if m.dirty != nil {
		t.Fatal("dirty should be nil after promotion")
	}
	if v, ok := m.Load("a"); !ok || v != 1 {
		t.Fatalf("Load after promotion: got %v,%v", v, ok)
	}
}

// entry 只读加速：read 命中的 Load 不需要 mu——主流程持锁时它照样返回。
func TestReadHitLockFree(t *testing.T) {
	m := NewProtocolMap()
	m.Store("hot", 42)
	if _, ok := m.Load("hot"); !ok { // 新 key 首读必经 dirty 慢路径，顺带触发 promote
		t.Fatal("setup load failed")
	}
	// 此时 hot 已在 read（promote 的产物），Load 只走只读快照
	m.mu.Lock() // 模拟慢路径正在持锁
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if v, ok := m.Load("hot"); !ok || v != 42 {
				t.Errorf("read-hit Load failed while mu held: %v %v", v, ok)
				break
			}
		}
	}()
	<-done
	m.mu.Unlock()
	// setup 的那次慢路径 Load 记了 1 次 miss；锁定窗口内的 100 次命中不得新增
	if got := m.Misses.Load(); got != 1 {
		t.Fatalf("read hits should not count misses, want 1, got %d", got)
	}
}

// miss 后走 dirty 命中：amended 路径，且 miss 计数落账。
func TestDirtyHitPath(t *testing.T) {
	m := NewProtocolMap()
	m.Store("old", 1) // read 里
	m.Store("new", 2) // 只在 dirty
	if v, ok := m.Load("new"); !ok || v != 2 {
		t.Fatalf("dirty hit failed: %v %v", v, ok)
	}
	if got := m.Misses.Load(); got != 1 {
		t.Fatalf("Misses = %d, want 1 (dirty hit counts as a miss)", got)
	}
}

// -race 压测：并发读写混合下协议不丢更新。
func TestConcurrentMixed(t *testing.T) {
	m := NewProtocolMap()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				k := fmt.Sprintf("k%d", i%10)
				m.Store(k, w*1000+i)
				if v, ok := m.Load(k); !ok || v == nil {
					t.Errorf("lost value for %s", k)
				}
			}
		}(w)
	}
	wg.Wait()
	for i := 0; i < 10; i++ {
		if _, ok := m.Load(fmt.Sprintf("k%d", i)); !ok {
			t.Fatalf("key k%d missing after writes", i)
		}
	}
}

// ---- 三方基准：真 sync.Map / RWMutex+map / 分片 ----

// RWMap 是 RWMutex 保护的原生 map。
type RWMap struct {
	mu sync.RWMutex
	m  map[string]any
}

func NewRWMap() *RWMap { return &RWMap{m: make(map[string]any)} }

func (m *RWMap) Load(k string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.m[k]
	return v, ok
}

func (m *RWMap) Store(k string, v any) {
	m.mu.Lock()
	m.m[k] = v
	m.mu.Unlock()
}

// ShardedMap 把 key 哈希到 16 个分片，每片独立 RWMutex，降低争用面。
type ShardedMap struct {
	sh []Shard
}

type Shard struct {
	mu sync.RWMutex
	m  map[string]any
}

const shardCount = 16

func NewShardedMap() *ShardedMap {
	s := &ShardedMap{sh: make([]Shard, shardCount)}
	for i := range s.sh {
		s.sh[i].m = make(map[string]any)
	}
	return s
}

func shardOf(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % shardCount)
}

func (m *ShardedMap) Load(k string) (any, bool) {
	s := &m.sh[shardOf(k)]
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[k]
	return v, ok
}

func (m *ShardedMap) Store(k string, v any) {
	s := &m.sh[shardOf(k)]
	s.mu.Lock()
	s.m[k] = v
	s.mu.Unlock()
}

func benchLoadStore(b *testing.B, load func(string) (any, bool), store func(string, any)) {
	b.Helper()
	for i := 0; i < 8; i++ { // 8 个热 key
		store(fmt.Sprintf("k%d", i), i)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			k := fmt.Sprintf("k%d", i%8)
			if i%20 == 0 { // 5% 写
				store(k, i)
			} else {
				load(k)
			}
			i++
		}
	})
}

// 场景一：读多写少（95% Load / 5% Store，8 个热 key）。
func BenchmarkReadMostly(b *testing.B) {
	b.Run("sync.Map", func(b *testing.B) {
		var m sync.Map
		benchLoadStore(b, func(k string) (any, bool) { return m.Load(k) }, func(k string, v any) { m.Store(k, v) })
	})
	b.Run("RWMutex+map", func(b *testing.B) {
		m := NewRWMap()
		benchLoadStore(b, m.Load, m.Store)
	})
	b.Run("Sharded16", func(b *testing.B) {
		m := NewShardedMap()
		benchLoadStore(b, m.Load, m.Store)
	})
}

// 场景二：写多（反复覆盖 8 个热 key）。
func BenchmarkWriteHeavy(b *testing.B) {
	b.Run("sync.Map", func(b *testing.B) {
		var m sync.Map
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				m.Store(fmt.Sprintf("k%d", i%8), i)
				i++
			}
		})
	})
	b.Run("RWMutex+map", func(b *testing.B) {
		m := NewRWMap()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				m.Store(fmt.Sprintf("k%d", i%8), i)
				i++
			}
		})
	})
	b.Run("Sharded16", func(b *testing.B) {
		m := NewShardedMap()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				m.Store(fmt.Sprintf("k%d", i%8), i)
				i++
			}
		})
	})
}

// 验收③ 场景三：高频增删 key——每个操作引入一个全新 key（经典实现每轮触发 dirty 重建）。
func BenchmarkChurnNewKeys(b *testing.B) {
	b.Run("sync.Map", func(b *testing.B) {
		var m sync.Map
		var n atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				k := fmt.Sprintf("churn-%d", n.Add(1))
				m.Store(k, k)
			}
		})
	})
	b.Run("RWMutex+map", func(b *testing.B) {
		m := NewRWMap()
		var n atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				k := fmt.Sprintf("churn-%d", n.Add(1))
				m.Store(k, k)
			}
		})
	})
	b.Run("Sharded16", func(b *testing.B) {
		m := NewShardedMap()
		var n atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				k := fmt.Sprintf("churn-%d", n.Add(1))
				m.Store(k, k)
			}
		})
	})
}
