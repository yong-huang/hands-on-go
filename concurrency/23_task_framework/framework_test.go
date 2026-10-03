package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// drainMetrics：等框架停稳后取指标快照（Stop 已保证全部任务有结局）。
func drainMetrics(t *testing.T, f *Framework) (submitted, succeeded, failed, panicked, rejected int64) {
	t.Helper()
	f.Cancel()
	f.Stop()
	return f.metrics.snapshot()
}

// TestConservationWithPanics：清单验收 ①——10^4 任务、约 10% panic，
// 完成数 = 成功数 + 失败数严格守恒，且无 goroutine 泄漏。
// go test -short 时缩到 1000 个任务（-race -count=50 跑小规模版）。
func TestConservationWithPanics(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	total := 10000
	if testing.Short() {
		total = 1000
	}
	f := New(8, 64, Block, 200*time.Millisecond)

	var wg sync.WaitGroup
	for id := 0; id < total; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var fn TaskFn
			if rand.IntN(10) == 0 {
				fn = func(ctx context.Context) error {
					panic(fmt.Sprintf("boom-%d", id))
				}
			} else {
				fn = func(ctx context.Context) error { return nil }
			}
			if err := f.Submit(id, fn); err != nil {
				t.Errorf("Submit(%d) = %v，阻塞策略下不应失败", id, err)
			}
		}(id)
	}
	wg.Wait()

	s, ok, fail, panicked, rejected := drainMetrics(t, f)
	if s != int64(total) {
		t.Fatalf("Submitted = %d, want %d", s, total)
	}
	if s != ok+fail {
		t.Fatalf("守恒律破坏：Submitted(%d) != Succeeded(%d) + Failed(%d)", s, ok, fail)
	}
	if panicked == 0 {
		t.Fatalf("10%% 随机 panic 下 Panicked = 0，panic 隔离未生效")
	}
	if rejected != 0 {
		t.Fatalf("阻塞策略下 Rejected = %d, want 0", rejected)
	}
}

// TestRejectStrategy：拒绝策略下队列满返回 ErrQueueFull，
// 被拒任务只记 Rejected 不破坏守恒律。
func TestRejectStrategy(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	f := New(1, 2, Reject, 0) // 1 worker、队列 2：制造拥堵

	release := make(chan struct{})
	var submitted int64
	var mu sync.Mutex
	for id := 0; id < 50; id++ {
		err := f.Submit(id, func(ctx context.Context) error {
			<-release // 全部堵在 worker 手里，把队列与 worker 塞满
			return nil
		})
		if err == nil {
			mu.Lock()
			submitted++
			mu.Unlock()
		} else if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("Submit(%d) = %v, want ErrQueueFull 或 nil", id, err)
		}
	}
	close(release)

	s, ok, fail, _, rejected := drainMetrics(t, f)
	if rejected == 0 {
		t.Fatalf("拥堵场景下 Rejected = 0，拒绝策略未生效")
	}
	if s != ok+fail {
		t.Fatalf("守恒律破坏：Submitted(%d) != Succeeded(%d) + Failed(%d)", s, ok, fail)
	}
	if s+rejected != 50 {
		t.Fatalf("受理(%d) + 被拒(%d) != 总提交 50", s, rejected)
	}
}

// TestTaskTimeout：慢任务超过单任务时限后计失败，
// 且任务本身尊重 ctx——wrapper goroutine 能自然退出，不泄漏。
func TestTaskTimeout(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	f := New(2, 8, Block, 50*time.Millisecond)

	err := f.Submit(1, func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Second): // 远超 50ms 时限
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("Submit = %v", err)
	}
	_, ok, fail, _, _ := drainMetrics(t, f)
	if fail != 1 || ok != 0 {
		t.Fatalf("超时任务应计失败：Succeeded=%d Failed=%d", ok, fail)
	}
}

// TestCancelFastStop：取消后，运行中与排队中的协作式任务快速以失败结局退出，
// 守恒律依然成立——每个被受理的任务都被计数。
// 注意：Submit 用并发 goroutine 发起。串行提交时 Block 背压会真实阻塞
// 提交方（等前面的 10s 任务跑完），Cancel 就没机会在任务运行中触发了。
func TestCancelFastStop(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	f := New(2, 8, Block, 0)

	task := func(ctx context.Context) error {
		select {
		case <-time.After(10 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	var stopped int64 // 拿到 ErrStopped 的提交数（任务未被受理，不计入 Submitted）
	for id := 0; id < 20; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if err := f.Submit(id, task); err != nil {
				if !errors.Is(err, ErrStopped) {
					t.Errorf("Submit(%d) = %v", id, err)
					return
				}
				atomic.AddInt64(&stopped, 1)
			}
		}(id)
	}
	time.Sleep(100 * time.Millisecond) // 让队列填满、两个 worker 进入任务
	f.Cancel()
	wg.Wait() // 阻塞中的提交方经 ctx.Done 逃生口返回
	f.Stop()

	s, ok, fail, _, _ := f.metrics.snapshot()
	if s != ok+fail {
		t.Fatalf("守恒律破坏：Submitted(%d) != Succeeded(%d) + Failed(%d)", s, ok, fail)
	}
	if want := int64(20); s+stopped != want {
		t.Fatalf("受理(%d) + 被停机拒收(%d) != 总提交 %d", s, stopped, want)
	}
	if fail == 0 {
		t.Fatalf("取消后应有任务以失败结局退出，Failed = 0")
	}
}

// TestSubmitAfterStop：停机后再提交返回 ErrStopped。
func TestSubmitAfterStop(t *testing.T) {
	f := New(1, 4, Block, 0)
	f.Stop()
	if err := f.Submit(1, func(ctx context.Context) error { return nil }); !errors.Is(err, ErrStopped) {
		t.Fatalf("停机后 Submit = %v, want ErrStopped", err)
	}
}
