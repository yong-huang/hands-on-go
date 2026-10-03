package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

// 压测轮数：单次测试毫秒级，保证 -race -count=100 依然轻快。
const testRounds = 2000

// TestMessagePassingReleaseAcquire：验收①。
// Release 写 + Acquire 读的消息传递 litmus，零违例是唯一标准。
// Go 公开 API 是 SeqCst，强于 Release/Acquire，因此按 A/R 推理成立。
func TestMessagePassingReleaseAcquire(t *testing.T) {
	for round := 0; round < 25; round++ {
		if got := mpReleaseAcquire(testRounds); got != 0 {
			t.Fatalf("第 %d 轮出现 %d 次违例：Release/Acquire 配对失效", round, got)
		}
	}
}

// TestPublicationReleaseAcquire：发布不可变对象（Pointer.Store/Load 配对）。
// 订阅方拿到非 nil 指针后必须看到指针背后全部字段写入（签名 {v,^v} 校验）。
// 发布前的 nil 观测属正常现象，不计入违例。
func TestPublicationReleaseAcquire(t *testing.T) {
	for round := 0; round < 25; round++ {
		_, bad := publishReleaseAcquire(testRounds)
		if bad != 0 {
			t.Fatalf("第 %d 轮出现 %d 次签名不一致：发布语义失效", round, bad)
		}
	}
}

// TestStoreBufferSeqCstForbidsDoubleOld：SB litmus 在 Go 的 SeqCst 下
// 不允许"同一轮双方都读到对方旧值"。在 C++ 的 release/acquire 口径里
// 这个结果是允许的（A/R 不提供全序）——这正是两个语义层级的分界线。
func TestStoreBufferSeqCstForbidsDoubleOld(t *testing.T) {
	const rounds = 2000
	var a, b atomic.Int32
	sawOldA := make([]bool, rounds+1)
	sawOldB := make([]bool, rounds+1)
	var wg sync.WaitGroup
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
			t.Fatalf("第 %d 轮出现双重旧值：SeqCst 全序被打破", r)
		}
	}
}

// TestSpinLockMutualExclusion：CAS/acquire + Store/release 的自旋锁，
// 8 个 goroutine 各自对普通变量自增，终值必须精确等于总和。
// 普通变量的可见性完全依赖锁的 acquire/release 配对——锁的最小语义。
func TestSpinLockMutualExclusion(t *testing.T) {
	var l SpinLock
	var n int64
	const perG, procs = 2000, 8
	var wg sync.WaitGroup
	wg.Add(procs)
	for i := 0; i < procs; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				l.Lock()
				n++ // 普通写：互斥与可见性全部由锁承诺
				l.Unlock()
			}
		}()
	}
	wg.Wait()
	if want := int64(perG * procs); n != want {
		t.Fatalf("n=%d, want %d：自旋锁互斥或可见性失效", n, want)
	}
}

// TestLockHandoffVisibility：锁交接可见性——上一个持有者在临界区内写的
// 普通变量，下一个持有者 Lock 成功后必须全部可见。这条链就是
// "Release/Acquire 足够建锁"的可执行版本（验收②的推理落到代码）。
func TestLockHandoffVisibility(t *testing.T) {
	var l SpinLock
	state := 0 // 普通变量
	const rounds = 1000
	for i := 1; i <= rounds; i++ {
		l.Lock()
		state = i // 持有者 A 写
		l.Unlock()
		l.Lock()
		if state != i { // 持有者 B 读：必须看到 A 的写
			l.Unlock()
			t.Fatalf("第 %d 轮锁交接后读到 state=%d, want %d", i, state, i)
		}
		l.Unlock()
	}
}

// ---- 基准：自旋锁 vs sync.Mutex（数字入 README） ----

var sinkInt atomic.Int64 // sink：承接结果，防止编译器优化掉临界区

func BenchmarkSpinLockUncontended(b *testing.B) {
	var l SpinLock
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.Lock()
		sinkInt.Add(1)
		l.Unlock()
	}
}

func BenchmarkMutexUncontended(b *testing.B) {
	var m sync.Mutex
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.Lock()
		sinkInt.Add(1)
		m.Unlock()
	}
}

func BenchmarkSpinLockContended(b *testing.B) {
	var l SpinLock
	var local atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var n int64
		for pb.Next() {
			l.Lock()
			n++
			l.Unlock()
		}
		local.Add(n)
	})
}

func BenchmarkMutexContended(b *testing.B) {
	var m sync.Mutex
	var local atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var n int64
		for pb.Next() {
			m.Lock()
			n++
			m.Unlock()
		}
		local.Add(n)
	})
}
