package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitWriterRegistered 自旋等待写者完成注册（readerCount 变负）。
// 写者注册后、真正 park 前有窗口，但后续断言只依赖"readerCount 为负"这一事实，
// 该事实一旦成立便不会自行消失，因此等待是确定性的。
func waitWriterRegistered(t *testing.T, rw *RWMutex) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for rw.readerCount.Load() >= 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer did not register within 2s")
		}
		runtime.Gosched()
	}
}

// 验收 ①：并发读 + 独占写。-race 下断言写者互斥、读者共享、读写互斥。
func TestConcurrentReadExclusiveWrite(t *testing.T) {
	rw := newRWMutex()
	var mu sync.Mutex // 保护下列三个普通计数（它们由 rw 之外的锁保护，避免干扰被测对象）
	concurrentWriters := 0
	maxConcurrentWriters := 0

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rw.Lock()
				mu.Lock()
				concurrentWriters++
				if concurrentWriters > maxConcurrentWriters {
					maxConcurrentWriters = concurrentWriters
				}
				mu.Unlock()
				mu.Lock()
				concurrentWriters--
				mu.Unlock()
				rw.Unlock()
			}
		}()
	}
	// 读者验证读写互斥：写临界区非空时读者不应看到计数非零。
	// 用"写者在临界区置位、离开清零"的标志位，读者在 RLock 内检查。
	var inWriter atomic.Bool
	var violations atomic.Int32
	for r := 0; r < 16; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				rw.RLock()
				if inWriter.Load() {
					violations.Add(1)
				}
				rw.RUnlock()
			}
		}()
	}
	// 一组写者负责翻转 inWriter 标志
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				rw.Lock()
				inWriter.Store(true)
				inWriter.Store(false)
				rw.Unlock()
			}
		}()
	}
	wg.Wait()
	if maxConcurrentWriters != 1 {
		t.Fatalf("writers overlapped: maxConcurrentWriters = %d", maxConcurrentWriters)
	}
	if v := violations.Load(); v != 0 {
		t.Fatalf("readers observed an active writer %d times", v)
	}
}

// 读者共享验证：4 个读者同时站在读临界区里。
// 结构上保证"同时"：每个读者上报进入后在屏障上等全体到齐，谁也不提前退出。
func TestReadersShare(t *testing.T) {
	rw := newRWMutex()
	rw.RLock() // 主 goroutine 持有读锁
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rw.RLock()
			entered <- struct{}{} // 无写者时 RLock 不会阻塞，进入即上报
			<-release             // 屏障：等全体读者都进来再一起离开
			rw.RUnlock()
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("readers did not all enter the read critical section")
		}
	}
	close(release)
	wg.Wait()
	rw.RUnlock()
}

// 验收 ②：写者到达后，新读者必须阻塞，且写者先于新读者获得锁（写者优先）。
func TestWriterBlocksNewReaders(t *testing.T) {
	rw := newRWMutex()
	var order []string
	var mu sync.Mutex
	record := func(ev string) { mu.Lock(); order = append(order, ev); mu.Unlock() }

	rw.RLock() // 读者先持锁
	writerAcquired := make(chan struct{})
	go func() {
		rw.Lock()
		record("writer")
		rw.Unlock()
		close(writerAcquired)
	}()
	waitWriterRegistered(t, rw)

	newReaderIn := make(chan struct{})
	go func() {
		rw.RLock()
		record("reader2")
		rw.RUnlock()
		close(newReaderIn)
	}()

	// 给两者充分时间走错：若新读者越过挂起写者，这里会抓到
	time.Sleep(150 * time.Millisecond)
	select {
	case <-newReaderIn:
		t.Fatal("new reader slipped past a pending writer")
	default:
	}

	rw.RUnlock() // 放行：写者先拿锁，写者 Unlock 唤醒排队的读者
	<-writerAcquired
	<-newReaderIn

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "writer" || order[1] != "reader2" {
		t.Fatalf("expected writer before reader2, got %v", order)
	}
}

// 验收 ③：复现"读锁内再取读锁 + 写者在等"的死锁，
// 并验证协议链条：外层 RUnlock → 写者 → 内层 RLock 的唤醒顺序。
func TestRecursiveRLockDeadlock(t *testing.T) {
	rw := newRWMutex()
	rw.RLock() // 外层读锁

	writerAcquired := make(chan struct{})
	go func() { rw.Lock(); close(writerAcquired); rw.Unlock() }()
	waitWriterRegistered(t, rw)

	// 同一逻辑流再取读锁：写者已注册，Add(1) 为负，读者排队。
	// 写者又在等这个 goroutine 尚未执行的 RUnlock——三方互等，内层永不返回。
	innerAcquired := make(chan struct{})
	go func() {
		rw.RLock()
		close(innerAcquired)
		rw.RUnlock()
	}()

	// 断言死锁确实发生：内层读锁与写锁在观察窗口内都拿不到。
	select {
	case <-innerAcquired:
		t.Fatal("inner RLock succeeded; expected deadlock")
	case <-writerAcquired:
		t.Fatal("writer acquired while outer reader still held; expected deadlock")
	case <-time.After(300 * time.Millisecond):
	}

	// 打破死锁：外层 RUnlock 是唯一出口。观察协议链条：
	// 最后一个读者离开（readerWait 归 0）唤醒写者，写者 Unlock 再唤醒内层读者。
	rw.RUnlock()
	select {
	case <-writerAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("writer not woken by last reader's RUnlock")
	}
	select {
	case <-innerAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("queued reader not woken by writer's Unlock")
	}
}

// 压力回归：高竞争下锁协议不丢唤醒、不错账。以 -race -count=200 反复执行。
func TestStressMixedLoad(t *testing.T) {
	rw := newRWMutex()
	type pair struct{ a, b int }
	var p pair // 由 rw 保护：写者写入 (i, 2i)，读者校验 b == 2a
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rw.Lock()
				p = pair{i, 2 * i}
				rw.Unlock()
			}
		}(w)
	}
	for r := 0; r < 12; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				rw.RLock()
				if p.b != 2*p.a {
					t.Errorf("torn read: %+v", p)
				}
				rw.RUnlock()
			}
		}()
	}
	wg.Wait()
}
