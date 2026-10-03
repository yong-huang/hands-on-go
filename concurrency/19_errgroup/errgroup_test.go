package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xerrgroup "golang.org/x/sync/errgroup"
)

// 验收 ①：N 任务一失败，其余全部收到 ctx 取消，且传播耗时 < 10ms。
func TestFirstErrorCancelsWholeGroup(t *testing.T) {
	g, ctx := WithContext(context.Background())
	const n = 8
	var failAt atomic.Int64 // 任务 0 失败时刻（纳秒）
	var canceled atomic.Int64
	latencies := make(chan time.Duration, n-1)

	g.Go(func() error {
		time.Sleep(time.Millisecond) // 让其余任务先进入等待态
		failAt.Store(time.Now().UnixNano())
		return errors.New("boom: task 0")
	})
	for i := 1; i < n; i++ {
		g.Go(func() error {
			select {
			case <-ctx.Done(): // 首错触发的停机令
				latencies <- time.Since(time.Unix(0, failAt.Load()))
				canceled.Add(1)
				return nil
			case <-time.After(2 * time.Second):
				return errors.New("unreachable: 2s 内未收到取消")
			}
		})
	}

	err := g.Wait()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Wait 应返回首错 boom, got %v", err)
	}
	if got := canceled.Load(); got != n-1 {
		t.Fatalf("应全部收到取消: got %d/%d", got, n-1)
	}
	for i := 0; i < n-1; i++ {
		if d := <-latencies; d > 10*time.Millisecond {
			t.Fatalf("取消传播耗时 %v 超过验收线 10ms", d)
		}
	}
}

// 验收 ②：SetLimit 下峰值并发恰好 = limit（1/3/7 三档）。
// 不变式：任务先减 active 再在 done() 还令牌，因此 active ≤ 持令牌数 ≤ limit；
// 前 limit 个任务各持一个令牌堵在屏障上，第 limit 个进入即闭屏障，
// 所以 peak == limit 在任意调度下都可观测。
func TestSetLimitPeakEqualsLimit(t *testing.T) {
	for _, limit := range []int{1, 3, 7} {
		g, _ := WithContext(context.Background())
		g.SetLimit(limit)
		var active, peak int64
		release := make(chan struct{})
		var once sync.Once // limit=1 时每个任务都见到 n==limit，闭屏障只允许一次
		for i := 0; i < limit*5; i++ {
			g.Go(func() error {
				n := atomic.AddInt64(&active, 1)
				for { // 记录峰值：CAS 重试保证读改写一体
					p := atomic.LoadInt64(&peak)
					if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
						break
					}
				}
				if n == int64(limit) {
					once.Do(func() { close(release) }) // 第 limit 个到齐：放行全组
				}
				<-release
				atomic.AddInt64(&active, -1) // 先减计数再还令牌
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if peak != int64(limit) {
			t.Fatalf("limit=%d 实测峰值=%d, 应恰为 limit", limit, peak)
		}
	}
}

// TryGo：满载返回 false 不阻塞；Wait 后令牌归还、恢复可用。
func TestTryGoRespectsLimit(t *testing.T) {
	g, _ := WithContext(context.Background())
	g.SetLimit(2)
	block := make(chan struct{})
	for i := 0; i < 2; i++ {
		if !g.TryGo(func() error { <-block; return nil }) {
			t.Fatalf("第 %d 个任务应能派出", i+1)
		}
	}
	if g.TryGo(func() error { return nil }) {
		t.Fatal("limit=2 满载时 TryGo 应返回 false")
	}
	close(block)
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !g.TryGo(func() error { return nil }) {
		t.Fatal("Wait 后令牌应已归还，TryGo 应恢复可用")
	}
}

// panic 转错误：任务 panic 不炸进程，转为 Wait 返回的首错并联动取消。
func TestPanicBecomesErrorAndCancels(t *testing.T) {
	g, ctx := WithContext(context.Background())
	g.Go(func() error { panic("boom") })
	g.Go(func() error { <-ctx.Done(); return nil })
	err := g.Wait()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic 应转为 Wait 的首错, got %v", err)
	}
}

// Wait 正常收尾也取消 ctx：让"等 ctx"的收尾路径不必区分成败。
func TestWaitCancelsContextOnSuccess(t *testing.T) {
	g, ctx := WithContext(context.Background())
	g.Go(func() error { return nil })
	if err := g.Wait(); err != nil {
		t.Fatalf("无失败时 Wait 应返回 nil, got %v", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Wait 正常返回后 ctx 也应被取消")
	}
}

// 验收 ③：errgroup 对照组。同场景下失败联动 + 全组联结：
// A 失败 → B 经 ctx.Done 拿到退路 → Wait 返回首错 → goroutine 清零。
func TestErrgroupJoinsWhatBareGoroutineCannot(t *testing.T) {
	before := runtime.NumGoroutine()
	g, ctx := WithContext(context.Background())
	results := make(chan int)
	g.Go(func() error { return errors.New("task A failed") })
	g.Go(func() error {
		select {
		case <-results: // 等 A 的结果
			return nil
		case <-ctx.Done(): // A 失败联动出的退路
			return ctx.Err()
		}
	})
	if err := g.Wait(); err == nil || !strings.Contains(err.Error(), "task A failed") {
		t.Fatalf("Wait 应返回 A 的首错, got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("Wait 后组内 goroutine 未清零: %d > 基线 %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(time.Millisecond)
	}
}

// 验收 ③：裸 goroutine 失控对照——失败无人管、泄漏无人收。
// A 的错误发往无人接收的通道（调用方没安排读者），A 卡死在汇报；
// B 等 A 的结果，永不结束。调用方既拿不到错误，也没有任何句柄收尸。
// 本测试结束时 A 仍处于泄漏态（进程内多一个僵尸 goroutine，基线 +1），
// 这是失控案例本身：后续测试一律用各自起点作基线，不受影响。
func TestBareGoroutineRunaway(t *testing.T) {
	before := runtime.NumGoroutine()
	errCh := make(chan error) // 无缓冲、无读者：失败无处汇报
	results := make(chan int) // 无缓冲、无发送者：B 的等待永无结果
	go func() { errCh <- errors.New("task A failed") }()
	go func() { <-results }()

	// 等两个 goroutine 到达阻塞点。
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() < before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("对照 goroutine 未就位: %d < %d", runtime.NumGoroutine(), before+2)
		}
		time.Sleep(time.Millisecond)
	}
	// 失败无人管：errCh 没有任何读者，调用方对 A 的失败零感知。
	// 泄漏无人收：没有句柄能结束 A——它将伴随进程直到退出。
	time.Sleep(20 * time.Millisecond)
	if n := runtime.NumGoroutine(); n < before+2 {
		t.Fatalf("失控 goroutine 应持续存在: %d < %d", n, before+2)
	}
}

// 与 x/sync/errgroup 的行为对照：同场景（首错 + SetLimit）下首错语义
// 一致，为 README 差异表提供实测依据。
func TestParityWithXSyncErrgroup(t *testing.T) {
	xg, xctx := xerrgroup.WithContext(context.Background())
	xg.SetLimit(4)
	var xfailAt atomic.Int64
	var xlat atomic.Int64
	xg.Go(func() error {
		time.Sleep(time.Millisecond)
		xfailAt.Store(time.Now().UnixNano())
		return errors.New("x boom")
	})
	xg.Go(func() error {
		<-xctx.Done()
		xlat.Store(time.Since(time.Unix(0, xfailAt.Load())).Nanoseconds())
		return nil
	})
	if err := xg.Wait(); err == nil || err.Error() != "x boom" {
		t.Fatalf("x/sync 应返回首错, got %v", err)
	}
	if d := time.Duration(xlat.Load()); d <= 0 || d > 10*time.Millisecond {
		t.Fatalf("x/sync 取消传播 %v 不在 (0,10ms] 内", d)
	}

	// SetLimit 行为对照：满载 TryGo 返回 false。
	og, _ := WithContext(context.Background())
	og.SetLimit(1)
	block := make(chan struct{})
	og.Go(func() error { <-block; return nil })
	if og.TryGo(func() error { return nil }) {
		t.Fatal("手写版满载 TryGo 应为 false")
	}
	close(block)
	og.Wait()
}
