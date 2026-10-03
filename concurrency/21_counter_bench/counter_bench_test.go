package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

// 守恒校验：并发加完后三实现的总数都必须分毫不差。
func TestCountersConserveTotal(t *testing.T) {
	const workers, per = 8, 10_000
	var mu MutexCounter
	var at AtomicCounter
	var sh ShardCounter
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				mu.Add()
				at.Add()
				sh.Add(w)
			}
		}()
	}
	wg.Wait()
	want := int64(workers * per)
	if got := mu.Load(); got != want {
		t.Fatalf("MutexCounter = %d, want %d", got, want)
	}
	if got := at.Load(); got != want {
		t.Fatalf("AtomicCounter = %d, want %d", got, want)
	}
	if got := sh.Load(); got != want {
		t.Fatalf("ShardCounter = %d, want %d", got, want)
	}
}

// 填充有效性：分片必须恰好占满一条 64B 缓存行，否则相邻分片仍伪共享。
func TestPaddedShardFillsCacheLine(t *testing.T) {
	if got := unsafe.Sizeof(paddedShard{}); got != 64 {
		t.Fatalf("paddedShard = %d 字节, want 64（一条缓存行）", got)
	}
}

// 分片掩码：任何分片号都应落在 [0, shardCount) 内。
func TestShardMaskBounds(t *testing.T) {
	var sh ShardCounter
	sh.Add(-7)
	sh.Add(shardCount + 3)
	if got := sh.Load(); got != 2 {
		t.Fatalf("负数与越界分片号应被掩码收编, got %d", got)
	}
}

// ---------- 三路基准：-cpu=1,2,4,8,16 -count=5 取中位数 ----------

func BenchmarkMutexCounter(b *testing.B) {
	b.ReportAllocs()
	var c MutexCounter
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Add()
		}
	})
}

func BenchmarkAtomicCounter(b *testing.B) {
	b.ReportAllocs()
	var c AtomicCounter
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Add()
		}
	})
}

func BenchmarkShardCounter(b *testing.B) {
	b.ReportAllocs()
	var c ShardCounter
	var seq atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		shard := int(seq.Add(1)) // 每个工作协程认领一个分片：本地性是分片方案的前提
		for pb.Next() {
			c.Add(shard)
		}
	})
}
