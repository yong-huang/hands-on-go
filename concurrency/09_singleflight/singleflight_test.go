package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xsf "golang.org/x/sync/singleflight"
)

// 验收①：100 goroutine 同 key 并发请求，后端计数器只落 1 次。
func TestDedupSameKey(t *testing.T) {
	var g Group
	var backend atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]any, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			v, _, _ := g.Do("hot", func() (any, error) {
				backend.Add(1)
				time.Sleep(50 * time.Millisecond)
				return "value", nil
			})
			results[i] = v
		}(i)
	}
	close(start)
	wg.Wait()

	if got := backend.Load(); got != 1 {
		t.Fatalf("backend executed %d times, want exactly 1", got)
	}
	for i, v := range results {
		if v != "value" {
			t.Fatalf("caller %d got %v, want shared \"value\"", i, v)
		}
	}
}

// 验收②：不同 key 互不阻塞——4 个 key 的 fn 同时在执行。
func TestIndependentKeys(t *testing.T) {
	var g Group
	var inFlight, maxInFlight atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			<-start
			g.Do(k, func() (any, error) {
				n := inFlight.Add(1)
				for {
					max := maxInFlight.Load()
					if n <= max || maxInFlight.CompareAndSwap(max, n) {
						break
					}
				}
				time.Sleep(50 * time.Millisecond)
				inFlight.Add(-1)
				return k, nil
			})
		}(fmt.Sprintf("k%d", i))
	}
	close(start)
	wg.Wait()

	if got := maxInFlight.Load(); got != 4 {
		t.Fatalf("max concurrent executions = %d, want 4 (keys must not block each other)", got)
	}
}

// 验收③（panic 行为前半）：发起者不被吞——收到原始 panic 值。
func TestPanicReachesInitiator(t *testing.T) {
	var g Group
	sentinel := errors.New("boom")
	got := make(chan any, 1)
	go func() {
		defer func() { got <- recover() }()
		g.Do("k", func() (any, error) {
			panic(sentinel)
		})
	}()
	if r := <-got; r != sentinel {
		t.Fatalf("initiator recovered %v, want the original sentinel", r)
	}
}

// 验收③（panic 行为后半）：等待者同样收到原始 panic 值；
// 且失败的 call 被清除，同 key 下一次调用重新执行 fn。
func TestPanicReachesWaitersAndCleansUp(t *testing.T) {
	var g Group
	sentinel := errors.New("boom")
	var backend atomic.Int64
	ready := make(chan struct{})
	initiatorGot := make(chan any, 1)
	waiterGot := make(chan any, 3)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // 发起者：注册后放行等待者，等它们领号再 panic，顺序确定
		defer wg.Done()
		defer func() { initiatorGot <- recover() }() // 发起者同样应收到原始值
		g.Do("k", func() (any, error) {
			backend.Add(1)
			close(ready)
			for g.dupsOf("k") < 3 {
				time.Sleep(time.Millisecond)
			}
			panic(sentinel)
		})
	}()
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			defer func() { waiterGot <- recover() }()
			g.Do("k", func() (any, error) {
				backend.Add(1) // 不应执行
				return nil, nil
			})
		}()
	}
	wg.Wait()
	if r := <-initiatorGot; r != sentinel {
		t.Fatalf("initiator recovered %v, want the original sentinel", r)
	}
	for i := 0; i < 3; i++ {
		if r := <-waiterGot; r != sentinel {
			t.Fatalf("waiter %d recovered %v, want the original sentinel", i, r)
		}
	}
	if got := backend.Load(); got != 1 {
		t.Fatalf("backend executed %d times, want 1 (waiters must not run fn)", got)
	}
	// key 已清理：下一次调用是全新执行，不再关联失败的 call
	v, err, _ := g.Do("k", func() (any, error) { return "fresh", nil })
	if err != nil || v != "fresh" {
		t.Fatalf("post-panic Do: got %v, %v", v, err)
	}
}

// 对照实测：x/sync/singleflight 在同样的同 key 并发下同样只落一次。
func TestXSyncDedupParity(t *testing.T) {
	var g xsf.Group
	var backend atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = g.Do("hot", func() (any, error) {
				backend.Add(1)
				time.Sleep(20 * time.Millisecond)
				return "value", nil
			})
		}()
	}
	close(start)
	wg.Wait()
	if got := backend.Load(); got != 1 {
		t.Fatalf("x/sync backend executed %d times, want 1", got)
	}
}

// -race 压测：混合 key、随机 panic、持续并发，全部调用者收场、无死锁。
func TestStressMixedKeysWithPanics(t *testing.T) {
	var g Group
	var wg sync.WaitGroup
	var executed atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				key := fmt.Sprintf("k%d", i%10)
				func() {
					defer func() { _ = recover() }() // panic 是本压测的合法负载
					g.Do(key, func() (any, error) {
						executed.Add(1)
						if i%3 == 0 {
							panic(errors.New("load"))
						}
						return key, nil
					})
				}()
			}
		}(w)
	}
	wg.Wait() // 返回即证明 panic 路径没有卡死任何等待者
}
