package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 固定版 litmus 的轮数：保证 -count=100 反复压测时单次运行仍在毫秒级。
const testRounds = 2000

// TestMessagePassingChannelFixed：验收②的 channel 侧。
// 发送/接收边建立后，"flag 新值可见、data 旧值"必须为 0，
// go test -race -count=100 零报告是唯一标准。
func TestMessagePassingChannelFixed(t *testing.T) {
	for round := 0; round < 25; round++ {
		if got := mpChannel(testRounds); got != 0 {
			t.Fatalf("channel 修复版第 %d 轮出现 %d 次异常，happens-before 边失效", round, got)
		}
	}
}

// TestMessagePassingAtomicFixed：验收②的 atomic 侧。
// atomic 写/读配对（顺序一致语义）+ 程序顺序，同样必须零异常。
func TestMessagePassingAtomicFixed(t *testing.T) {
	for round := 0; round < 25; round++ {
		if got := mpAtomic(testRounds); got != 0 {
			t.Fatalf("atomic 修复版第 %d 轮出现 %d 次异常，顺序一致承诺失效", round, got)
		}
	}
}

// TestStoreBufferAtomicSeqCstFixed：SB litmus 在 Go atomic（顺序一致）下
// 不允许出现"双重旧值"。这是 MP 用 Release/Acquire 就够、SB 必须更强
// 全序语义的对照点（展开见 02_atomic_orders）。
func TestStoreBufferAtomicSeqCstFixed(t *testing.T) {
	const rounds = 2000
	var a, b atomic.Int32
	var wg sync.WaitGroup
	sawOldA := make([]bool, rounds+1)
	sawOldB := make([]bool, rounds+1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			a.Store(v)
			sawOldA[r] = b.Load() < v
		}
	}()
	go func() {
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			b.Store(v)
			sawOldB[r] = a.Load() < v
		}
	}()
	wg.Wait()
	for r := 1; r <= rounds; r++ {
		if sawOldA[r] && sawOldB[r] {
			t.Fatalf("第 %d 轮出现双重旧值：Go atomic 的顺序一致语义被打破", r)
		}
	}
}

// TestGoStatementEdge：验收③里"go 语句"这条边的可执行版。
// go 语句同步先于新 goroutine 开始：语句前写的普通变量，新 goroutine 直接可见，
// 不需要 channel/atomic 兜底——本测试在 -race 下零报告即证明。
func TestGoStatementEdge(t *testing.T) {
	for round := 0; round < 100; round++ {
		data := make([]int, 64)
		for i := range data {
			data[i] = i // go 语句之前的写
		}
		var got atomic.Int64
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { // 新 goroutine 从 go 语句处获得全部前置写
			defer wg.Done()
			sum := 0
			for _, v := range data {
				sum += v
			}
			got.Store(int64(sum))
		}()
		wg.Wait()
		if want := int64(64 * 63 / 2); got.Load() != want {
			t.Fatalf("go 语句边失效：sum=%d, want=%d", got.Load(), want)
		}
	}
}

// TestGoStatementEdgeWitness：更苛刻的变体——go 语句后的主 goroutine 不做任何
// 额外同步，直接断言新 goroutine 观测到了写。channel 回传只用于回收结果，
// 不参与建立"写 → 读"的边（边由 go 语句自己提供）。
func TestGoStatementEdgeWitness(t *testing.T) {
	done := make(chan struct{})
	var x int // 普通 int：靠 go 语句边保证可见
	x = 42
	go func() {
		if x != 42 { // 若 go 语句不构成边，这里可能读到 0
			t.Errorf("新 goroutine 读到 x=%d, want 42", x)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("goroutine 未结束")
	}
}
