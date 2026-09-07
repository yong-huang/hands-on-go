package main

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// shard is a single bucket protected by its own RWMutex.
type shard struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

// ShardMap is a concurrent-safe map that distributes keys across N shards.
// Each shard has an independent lock, so operations on different shards
// do not block each other.
type ShardMap struct {
	shards     []shard
	shardCount uint32
}

// NewShardMap creates a ShardMap with the given number of shards.
// A good default is runtime.NumCPU().
func NewShardMap(shardCount int) *ShardMap {
	if shardCount <= 0 {
		shardCount = 1
	}
	sm := &ShardMap{
		shards:     make([]shard, shardCount),
		shardCount: uint32(shardCount),
	}
	for i := range sm.shards {
		sm.shards[i].data = make(map[string]interface{})
	}
	return sm
}

// getShard returns the shard index for the given key using FNV-32a hash.
func (sm *ShardMap) getShard(key string) *shard {
	h := fnv.New32a()
	h.Write([]byte(key))
	idx := h.Sum32() % sm.shardCount
	return &sm.shards[idx]
}

// Get retrieves the value for a key. Returns (value, true) if found.
func (sm *ShardMap) Get(key string) (interface{}, bool) {
	s := sm.getShard(key)
	s.mu.RLock()
	v, ok := s.data[key]
	s.mu.RUnlock()
	return v, ok
}

// Set stores a key-value pair in the appropriate shard.
func (sm *ShardMap) Set(key string, value interface{}) {
	s := sm.getShard(key)
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
}

// Delete removes a key from the map.
func (sm *ShardMap) Delete(key string) {
	s := sm.getShard(key)
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
}

// Range iterates over all key-value pairs. The callback receives each pair;
// return false to stop iteration early. The iteration order is not
// deterministic.
func (sm *ShardMap) Range(fn func(key string, value interface{}) bool) {
	for i := range sm.shards {
		s := &sm.shards[i]
		s.mu.RLock()
		for k, v := range s.data {
			if !fn(k, v) {
				s.mu.RUnlock()
				return
			}
		}
		s.mu.RUnlock()
	}
}

// Len returns the total number of entries across all shards.
func (sm *ShardMap) Len() int {
	count := 0
	for i := range sm.shards {
		s := &sm.shards[i]
		s.mu.RLock()
		count += len(s.data)
		s.mu.RUnlock()
	}
	return count
}

// ---------------------------------------------------------------------------
// Benchmark helpers
// ---------------------------------------------------------------------------

// benchmarkShardMap runs concurrent read/write on our ShardMap.
func benchmarkShardMap(goroutines, opsPerGoroutine int) time.Duration {
	sm := NewShardMap(32)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("key-%d-%d", id, i)
				sm.Set(key, i)
				sm.Get(key)
				if i%10 == 0 {
					sm.Delete(key)
				}
			}
		}(g)
	}
	wg.Wait()
	return time.Since(start)
}

// benchmarkSyncMap runs the same workload on sync.Map.
func benchmarkSyncMap(goroutines, opsPerGoroutine int) time.Duration {
	var m sync.Map

	var wg sync.WaitGroup
	wg.Add(goroutines)

	start := time.Now()
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("key-%d-%d", id, i)
				m.Store(key, i)
				m.Load(key)
				if i%10 == 0 {
					m.Delete(key)
				}
			}
		}(g)
	}
	wg.Wait()
	return time.Since(start)
}

// ---------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------

func demo() {
	const shardCount = 4

	sm := NewShardMap(shardCount)
	fmt.Printf("ShardMap: %d shards created\n\n", shardCount)

	// Basic operations
	sm.Set("name", "gopher")
	sm.Set("lang", "go")
	sm.Set("version", 1.22)

	if v, ok := sm.Get("name"); ok {
		fmt.Printf("Get(\"name\") = %v (ok=%v)\n", v, ok)
	}
	if _, ok := sm.Get("missing"); !ok {
		fmt.Printf("Get(\"missing\") = (_, %v)\n", ok)
	}

	fmt.Printf("Len() = %d\n", sm.Len())

	// Range iteration
	fmt.Print("\nRange iteration:\n")
	sm.Range(func(key string, value interface{}) bool {
		fmt.Printf("  %s => %v\n", key, value)
		return true
	})

	// Delete
	sm.Delete("version")
	fmt.Printf("\nAfter Delete(\"version\"): Len() = %d\n", sm.Len())

	// Concurrent benchmark
	fmt.Println("\n--- Concurrent Benchmark ---")
	const goroutines = 8
	const opsPerGoroutine = 100000

	shardMapDur := benchmarkShardMap(goroutines, opsPerGoroutine)
	fmt.Printf("ShardMap:  %d goroutines x %d ops = %v\n",
		goroutines, opsPerGoroutine, shardMapDur)

	syncMapDur := benchmarkSyncMap(goroutines, opsPerGoroutine)
	fmt.Printf("sync.Map:  %d goroutines x %d ops = %v\n",
		goroutines, opsPerGoroutine, syncMapDur)

	if shardMapDur > 0 && syncMapDur > 0 {
		ratio := float64(syncMapDur) / float64(shardMapDur)
		if ratio > 1 {
			fmt.Printf("\nShardMap is %.1fx faster than sync.Map\n", ratio)
		} else {
			fmt.Printf("\nsync.Map is %.1fx faster than ShardMap\n", 1/ratio)
		}
	}

	// Show shard distribution
	fmt.Println("\n--- Shard Distribution ---")
	testKeys := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	for _, key := range testKeys {
		h := fnv.New32a()
		h.Write([]byte(key))
		idx := h.Sum32() % uint32(shardCount)
		fmt.Printf("  key=%-8s -> shard[%d]\n", key, idx)
	}
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	demo()
}
