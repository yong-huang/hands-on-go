package main

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

// ---- 基准：无填充 vs 64 字节填充（数字写入 bench.txt 与 README） ----
//
// 文档化跑法（8 goroutine，即 8 缓存行同时热写）：
//
//	GOMAXPROCS=8 go test -bench . -run '^$' -benchtime 2s
//
// b.RunParallel 开 GOMAXPROCS 个 worker，每个 worker 用原子序号认领
// 自己独占的槽位，保证"各自热写自己的计数器"这一争用形状。

func benchCountShared(b *testing.B) {
	cs := newSharedCounters(8)
	var idx atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := int(idx.Add(1)-1) % len(cs) // 超配时落到已有槽位
		for pb.Next() {
			cs[id].Add(1)
		}
	})
}

func benchCountPadded(b *testing.B) {
	cs := newPaddedCounters(8)
	var idx atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := int(idx.Add(1)-1) % len(cs)
		for pb.Next() {
			cs[id].n.Add(1)
		}
	})
}

func BenchmarkCountShared(b *testing.B) { benchCountShared(b) }
func BenchmarkCountPadded(b *testing.B) { benchCountPadded(b) }

// BenchmarkCountShared1P：单 goroutine 对照，给出"无争用基线"，
// 用来在 README 里解释倍数的上限（填充版单核速度本身）。
func BenchmarkCountShared1P(b *testing.B) {
	cs := newSharedCounters(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cs[i%len(cs)].Add(1) // 单 goroutine 也会在 8 个计数器间轮转，但无跨核争用
	}
}

// TestPaddedCellsAligned：填充版的核心前提是每个计数器独占一行。
// 若手工对齐实现有误（例如落在边界之外），本测试直接失败。
func TestPaddedCellsAligned(t *testing.T) {
	cs := newPaddedCounters(8)
	if got := alignedCount(cs); got != len(cs) {
		t.Fatalf("对齐校验 %d/%d：填充实现失效", got, len(cs))
	}
	for i := 1; i < len(cs); i++ {
		prev := reflect.ValueOf(&cs[i-1]).Pointer()
		cur := reflect.ValueOf(&cs[i]).Pointer()
		if cur-prev != cellSize {
			t.Fatalf("相邻 cell 间距 %d 字节，want %d", cur-prev, cellSize)
		}
	}
}

// TestCountersCountCorrectly：两版实现都必须精确计数——
// 防止"编译器把循环优化掉导致填充版虚快"这类基准假阳性。
func TestCountersCountCorrectly(t *testing.T) {
	const iters = 10_000
	shared := newSharedCounters(8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				shared[id].Add(1)
			}
		}(i)
	}
	wg.Wait()
	for i := range shared {
		if got := shared[i].Load(); got != iters {
			t.Fatalf("shared[%d]=%d, want %d", i, got, iters)
		}
	}
	padded := newPaddedCounters(8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				padded[id].n.Add(1)
			}
		}(i)
	}
	wg.Wait()
	for i := range padded {
		if got := padded[i].n.Load(); got != iters {
			t.Fatalf("padded[%d]=%d, want %d", i, got, iters)
		}
	}
}

// TestPaddedFieldNotTouchingNeighbor：填充字段必须真实存在于内存布局中——
// 若未来有人把 _ [56]byte 误删，布局大小检查立刻暴露。
func TestPaddedFieldNotTouchingNeighbor(t *testing.T) {
	if sz := unsafe.Sizeof(cell{}); sz != cellSize {
		t.Fatalf("cell 布局大小 %d 字节，want %d（填充被删除？）", sz, cellSize)
	}
	if runtime.NumCPU() < 2 {
		t.Skip("单核环境下伪共享无从谈起")
	}
}
