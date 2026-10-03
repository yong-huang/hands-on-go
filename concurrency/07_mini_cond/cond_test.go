package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitCondReady(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

// 验收 ①：mini Cond 版有界队列，多生产多消费，队列清空、goroutine 全部退出。
func TestBoundedQueueStress(t *testing.T) {
	base := runtime.NumGoroutine()
	q := NewBoundedQueue(8)
	const producers, consumers, perProducer = 8, 8, 200
	total := producers * perProducer
	var consumed atomic.Int64
	var wg sync.WaitGroup

	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				q.Put(i)
			}
		}()
	}
	for c := 0; c < consumers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total/consumers; i++ {
				q.Get()
				consumed.Add(1)
			}
		}()
	}
	wg.Wait() // 返回本身即证明没有任何 goroutine 卡死在 Wait 上

	if got := consumed.Load(); got != int64(total) {
		t.Fatalf("consumed %d, want %d", got, total)
	}
	if len(q.items) != 0 {
		t.Fatalf("queue not drained: %d items left", len(q.items))
	}
	// 无 goroutine 残留：数量回落到测试开始时的水平
	waitCondReady(t, func() bool { return runtime.NumGoroutine() <= base })
}

// 容量路径：队列满时 Put 阻塞，Get 后恢复。
func TestPutBlocksWhenFull(t *testing.T) {
	q := NewBoundedQueue(2)
	q.Put(1)
	q.Put(2)
	done := make(chan struct{})
	go func() { q.Put(3); close(done) }()
	select {
	case <-done:
		t.Fatal("Put returned although queue is full")
	case <-time.After(150 * time.Millisecond):
	}
	q.Get() // 腾出一个位置
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Put not woken by Get")
	}
}

// Signal 只唤醒一个等待者：1 个数据 + 1 次 Signal，恰好 1 人消费，其余仍在等待。
func TestSignalWakesExactlyOne(t *testing.T) {
	q := NewBoundedQueue(8)
	var mu sync.Mutex
	var got []int
	var wg sync.WaitGroup
	for id := 1; id <= 3; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.mu.Lock()
			for len(q.items) == 0 {
				q.notEmpty.Wait()
			}
			mu.Lock()
			got = append(got, id)
			mu.Unlock()
			q.mu.Unlock()
		}()
	}
	waitCondReady(t, func() bool { return q.notEmpty.notify.pendingWaiters() == 3 })

	q.mu.Lock()
	q.items = append(q.items, 99)
	q.notEmpty.Signal()
	q.mu.Unlock()

	waitCondReady(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	time.Sleep(50 * time.Millisecond) // 给错误实现留暴露窗口
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("signal woke %d waiters, want exactly 1", len(got))
	}
	mu.Unlock()
	if w := q.notEmpty.notify.pendingWaiters(); w != 2 {
		t.Fatalf("pending waiters = %d, want 2 (others must stay parked)", w)
	}
	// 清理：补 2 个数据 + Broadcast，让其余两个等待者退出
	q.mu.Lock()
	q.items = append(q.items, 1, 2)
	q.notEmpty.Broadcast()
	q.mu.Unlock()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("after broadcast got = %d, want 3", len(got))
	}
}

// Broadcast 唤醒全部等待者。
func TestBroadcastWakesAll(t *testing.T) {
	q := NewBoundedQueue(8)
	started := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.mu.Lock()
			for len(q.items) == 0 {
				q.notEmpty.Wait()
			}
			started <- struct{}{}
			q.mu.Unlock()
		}()
	}
	waitCondReady(t, func() bool { return q.notEmpty.notify.pendingWaiters() == 3 })

	q.mu.Lock()
	q.items = append(q.items, 1, 2, 3)
	q.notEmpty.Broadcast()
	q.mu.Unlock()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast did not wake all waiters")
	}
}

// 验收 ②：丢失唤醒反例——条件检查与 Wait 之间放锁，Signal 确定性地落在领票之前。
// 第二个握手（proceed）保证"改条件 + Signal"先于领票完成，通知按协议丢弃；
// 随后用正确顺序（持锁改条件 + Broadcast）闭环。
func TestLostWakeup(t *testing.T) {
	q := NewBoundedQueue(8)
	checked := make(chan struct{})
	proceed := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		// 错误顺序：检查条件后放锁，再等待；两步之间的通知会丢失
		q.mu.Lock()
		empty := len(q.items) == 0
		q.mu.Unlock()
		close(checked)
		if empty {
			<-proceed // 停在缺口上：此刻改条件，通知必然赶在领票之前
			q.mu.Lock()
			q.notEmpty.Wait()
			q.mu.Unlock()
		}
	}()

	<-checked // 消费者已检查完（看到空队列）、停在缺口
	q.mu.Lock()
	q.items = append(q.items, 7)
	q.notEmpty.Signal()
	noWaiters := q.notEmpty.notify.pendingWaiters() == 0 // 此刻确实无人领票
	q.mu.Unlock()
	close(proceed)
	if !noWaiters {
		t.Fatal("signal raced past registration; handshake broken")
	}

	select {
	case <-consumerDone:
		t.Fatal("consumer woke up; lost-wakeup window not reproduced")
	case <-time.After(200 * time.Millisecond):
	}
	if len(q.items) != 1 {
		t.Fatalf("item missing while consumer sleeps: %d", len(q.items))
	}

	// 正确顺序补救：持锁改条件 + Broadcast；消费端醒来后由调用方决定是否重查
	q.mu.Lock()
	q.items = append(q.items, 8)
	q.notEmpty.Broadcast()
	q.mu.Unlock()
	select {
	case <-consumerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer never woke after broadcast")
	}
	// 错误版消费者不重查条件：两条数据无人消费，这正是它的代价
	if len(q.items) != 2 {
		t.Fatalf("items consumed by the broken consumer: %d left", len(q.items))
	}
}

// 正确顺序：持锁检查，for+Wait，醒来重查。生产者在消费者领票后才改条件，也能被接住。
func TestCorrectOrderNoLostWakeup(t *testing.T) {
	q := NewBoundedQueue(8)
	got := make(chan int, 1)
	go func() {
		q.mu.Lock()
		for len(q.items) == 0 { // 正确顺序：条件检查与 Wait 同锁、循环重查
			q.notEmpty.Wait()
		}
		v := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		got <- v
	}()
	// 先确认消费者已领票，再投放：验证"领票→放锁→入睡"期间的通知不丢失
	waitCondReady(t, func() bool { return q.notEmpty.notify.pendingWaiters() == 1 })
	q.Put(42)
	select {
	case v := <-got:
		if v != 42 {
			t.Fatalf("got %d, want 42", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("correctly ordered consumer not woken")
	}
}
