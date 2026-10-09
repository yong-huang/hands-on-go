package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁：singleflight 等待者、redis 连接没清干净在这里现形。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	c, err := NewCache(redisAddr)
	if err != nil {
		t.Skipf("Redis 不可用（docker start marklet-redis 可启动）：%v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// counterLoader：可计数回源器——load 次数是所有断言的核心观测量。
type counterLoader struct {
	n     atomic.Int64
	delay time.Duration
}

func (l *counterLoader) load(ctx context.Context) (string, error) {
	l.n.Add(1)
	if l.delay > 0 {
		time.Sleep(l.delay)
	}
	return "loaded-value", nil
}

// TestCacheAsideRoundtrip：cache-aside 回路——首读回源回填、二读命中不再回源。
func TestCacheAsideRoundtrip(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	loader := &counterLoader{}
	key := fmt.Sprintf("test:aside:%d", time.Now().UnixNano())

	if v, err := c.GetOrLoad(ctx, key, time.Minute, loader.load); err != nil || v != "loaded-value" {
		t.Fatalf("首读 v=%q err=%v", v, err)
	}
	if got := loader.n.Load(); got != 1 {
		t.Fatalf("首读回源 %d 次， want 1", got)
	}
	if v, err := c.GetOrLoad(ctx, key, time.Minute, loader.load); err != nil || v != "loaded-value" {
		t.Fatalf("二读 v=%q err=%v", v, err)
	}
	if got := loader.n.Load(); got != 1 {
		t.Errorf("二读不应回源，实际回源 %d 次", got)
	}
}

// TestThunderingHerdSingleLoad：验收②——1000 并发查同一冷 key，
// singleflight 合并后回源次数必须恰好为 1。
func TestThunderingHerdSingleLoad(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	loader := &counterLoader{delay: 50 * time.Millisecond} // 回源耗时放大并发窗口
	key := fmt.Sprintf("test:herd:%d", time.Now().UnixNano())

	start := make(chan struct{}) // barrier：1000 个请求同时起飞，而不是 spawn 错峰
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := c.GetOrLoad(ctx, key, time.Minute, loader.load)
			if err != nil || v != "loaded-value" {
				t.Errorf("并发读 v=%q err=%v", v, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := loader.n.Load(); got != 1 {
		t.Errorf("回源 %d 次， want 1（singleflight 未生效）", got)
	}
}

// TestPenetrationNilCaching：验收③——不存在的 key：第一波只回源 1 次
// （空值入缓存），第二波零回源（空值缓存终结穿透）。
func TestPenetrationNilCaching(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	var loads atomic.Int64
	ghost := fmt.Sprintf("test:ghost:%d", time.Now().UnixNano())

	loader := func(ctx context.Context) (string, error) {
		loads.Add(1)
		time.Sleep(30 * time.Millisecond) // 回源窗口盖过整波读处理时长；TTL 5s 见 cache.go
		return "", ErrNotFound
	}

	runWave := func() {
		start := make(chan struct{}) // barrier：同波请求同时起飞
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := c.GetOrLoad(ctx, ghost, time.Minute, loader); !errors.Is(err, ErrNotFound) {
					t.Errorf("不存在 key 的错误 = %v， want ErrNotFound", err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}

	runWave()
	if got := loads.Load(); got != 1 {
		t.Fatalf("第一波回源 %d 次， want 1", got)
	}
	time.Sleep(20 * time.Millisecond)
	runWave()
	if got := loads.Load(); got != 1 {
		t.Errorf("第二波又回源 %d 次——空值缓存未生效，穿透未终结", got-1)
	}
}

// TestHitRateAbove90：验收①——预热后大批量读，命中率必须 ≥ 90%。
func TestHitRateAbove90(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	noop := func(ctx context.Context) (string, error) { return "v", nil }

	for i := 0; i < 10; i++ { // 预热 10 个 key
		if _, err := c.GetOrLoad(ctx, fmt.Sprintf("test:warm:%d", i), time.Minute, noop); err != nil {
			t.Fatal(err)
		}
	}
	hits0, _, _ := c.Stats()
	for i := 0; i < 1000; i++ { // 全部命中
		if _, err := c.GetOrLoad(ctx, fmt.Sprintf("test:warm:%d", i%10), time.Minute, noop); err != nil {
			t.Fatal(err)
		}
	}
	hits, misses, _ := c.Stats()
	rate := float64(hits-hits0) / float64(hits-hits0+misses)
	if rate < 0.9 {
		t.Errorf("命中率 %.1f%% < 90%%", rate*100)
	}
}

// TestLoaderErrorNotCached：回源报错（非 NotFound）不能被缓存成空值——
// 否则一次抖动会把错误放大成整个 TTL 的"假装不存在"。
func TestLoaderErrorNotCached(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	key := fmt.Sprintf("test:err:%d", time.Now().UnixNano())

	boom := errors.New("upstream boom")
	if _, err := c.GetOrLoad(ctx, key, time.Minute,
		func(ctx context.Context) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v， want boom", err)
	}
	// 错误未入缓存：下一次回源重新发生
	if _, err := c.GetOrLoad(ctx, key, time.Minute,
		func(ctx context.Context) (string, error) { return "recovered", nil }); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
}
