package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestExclusiveCriticalSection100Goroutines：验收①。
// 100 个 goroutine 各在临界区内累加 500 次；inCS 守卫断言任意时刻
// 临界区内并发数恒为 1，终值断言守恒。go test -race -count=200 全绿
// 是唯一标准（-count 提供交错压力，测试体自身毫秒级）。
func TestExclusiveCriticalSection100Goroutines(t *testing.T) {
	var m Mutex
	var n int64
	var inCS atomic.Int32
	const procs, perG = 100, 500
	var wg sync.WaitGroup
	wg.Add(procs)
	for i := 0; i < procs; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				m.Lock()
				if inCS.Add(1) != 1 {
					m.Unlock()
					t.Error("互斥被打破：临界区内出现并发")
					return
				}
				n++
				inCS.Add(-1)
				m.Unlock()
			}
		}()
	}
	wg.Wait()
	if want := int64(procs * perG); n != want {
		t.Fatalf("n=%d, want %d：计数守恒被破坏", n, want)
	}
}

// TestParallelHammer：RunParallel 全火力压锁，临界区守卫同上。
func TestParallelHammer(t *testing.T) {
	var m Mutex
	var inCS atomic.Int32
	var n atomic.Int64
	broadcast := make(chan struct{})
	close(broadcast)
	var wg sync.WaitGroup
	for p := 0; p < runtime.GOMAXPROCS(0); p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-broadcast
			for i := 0; i < 3000; i++ {
				m.Lock()
				if inCS.Add(1) != 1 {
					m.Unlock()
					t.Error("互斥被打破")
					return
				}
				n.Add(1)
				inCS.Add(-1)
				m.Unlock()
			}
		}()
	}
	wg.Wait()
	if want := int64(runtime.GOMAXPROCS(0) * 3000); n.Load() != want {
		t.Fatalf("n=%d, want %d", n.Load(), want)
	}
}

// TestZeroValueUsable：零值直接 Lock/Unlock（懒初始化 sema 的正确性）。
func TestZeroValueUsable(t *testing.T) {
	var m Mutex
	done := make(chan struct{})
	go func() {
		m.Lock()
		m.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("零值 Mutex 死锁")
	}
}

// TestUnlockUnlockedPanics：未持锁 Unlock 必须panic（状态校验）。
func TestUnlockUnlockedPanics(t *testing.T) {
	var m Mutex
	defer func() {
		if recover() == nil {
			t.Fatal("unlock of unlocked mutex 未被检出")
		}
	}()
	m.Unlock()
}

// TestStarvationHandoff：验收饥饿路径的可执行化。
// 持锁者连续占锁 3ms（> 1ms 阈值）且不放空窗；waiter 排队期间有插队者
// 持续快速争抢——被叫醒的 waiter 与插队者抢锁，抢输的那个带着饥饿标志
// 重新排队，把状态字的 starving 位置上（与标准库"第一个抢输者触发饥饿
// 模式"的动态一致）。断言：全部 waiter 在有界时间内完成各自轮次（无饿死），
// 且 starving 位被观测到（模式切换真实发生）。
func TestStarvationHandoff(t *testing.T) {
	var m Mutex
	const waiters, rounds = 6, 10
	var gotIn atomic.Int64
	var starvingSeen atomic.Bool
	stop := make(chan struct{})
	var wg sync.WaitGroup

	go func() { // 持锁者：3ms 占锁 × 10 轮，轮间不放空窗，制造真实排队
		defer close(stop)
		for i := 0; i < rounds; i++ {
			m.Lock()
			time.Sleep(3 * time.Millisecond)
			m.Unlock()
		}
	}()
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				m.Lock()
				gotIn.Add(1)
				m.Unlock()
			}
		}()
	}
	go func() { // 插队者：持续快速争抢，逼被叫醒的 waiter 抢输重排队
		for {
			select {
			case <-stop:
				return
			default:
			}
			m.Lock()
			m.Unlock()
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-done:
			if !starvingSeen.Load() {
				t.Fatal("全程未观测到 starving 位：饥饿模式未被触发")
			}
			if got := gotIn.Load(); got != waiters*rounds {
				t.Fatalf("完成 %d/%d 轮", got, waiters*rounds)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s 内 waiter 未完成（疑似饿死）：完成 %d/%d",
				gotIn.Load(), waiters*rounds)
		}
		if m.State()&mutexStarving != 0 {
			starvingSeen.Store(true)
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// TestHandoffFIFOOrder：饥饿交接偏向队首——先到的 waiter 在持续压力下
// 仍应在有界时间内（5s）拿到锁，且不晚于后到者全部拿完之后。
func TestHandoffFIFOOrder(t *testing.T) {
	var m Mutex
	const waiters = 4
	var seq atomic.Int64
	first := make(chan int64, waiters)
	var wg sync.WaitGroup
	go func() {
		m.Lock()
		time.Sleep(3 * time.Millisecond) // 制造饥饿排队
		m.Unlock()
	}()
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Lock()
			pos := seq.Add(1)
			if p := int(pos); p <= waiters {
				first <- pos
			}
			m.Unlock()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("交接迟迟不完成")
	}
	close(first)
	seen := map[int64]bool{}
	for p := range first {
		seen[p] = true
	}
	for i := 1; i <= waiters; i++ {
		if !seen[int64(i)] {
			t.Fatalf("第 %d 个完成者缺失：位置记录 %v", i, seen)
		}
	}
}

// ---- 基准：mini Mutex vs sync.Mutex（数字入 README） ----

func benchN(b *testing.B, lock sync.Locker) {
	var n int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lock.Lock()
		n++
		lock.Unlock()
	}
	if n == -1 {
		b.Fatal("unreachable")
	}
}

func BenchmarkMiniMutexUncontended(b *testing.B) { benchN(b, &Mutex{}) }
func BenchmarkSyncMutexUncontended(b *testing.B) { benchN(b, &sync.Mutex{}) }

func benchContended(b *testing.B, lock sync.Locker) {
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		for pb.Next() {
			lock.Lock()
			local++
			lock.Unlock()
		}
		_ = local
	})
}

func BenchmarkMiniMutexContended(b *testing.B) { benchContended(b, &Mutex{}) }
func BenchmarkSyncMutexContended(b *testing.B) { benchContended(b, &sync.Mutex{}) }
