package main

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁。
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

// TestDeleteFirstStalePersists：验收②前半——先删后更的坏例。
// 回源读走旧值后，写者的 DEL+更库拦不住这次回填，旧值驻留缓存。
func TestDeleteFirstStalePersists(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	var db atomic.Int64
	db.Store(100)
	key := "test:delete-first"
	c.Del(ctx, key)

	readDone := make(chan struct{}) // 握手：读者已读走旧值
	loader := func(ctx context.Context) (string, error) {
		v := strconv.FormatInt(db.Load(), 10)
		close(readDone)
		time.Sleep(60 * time.Millisecond)
		return v, nil
	}

	done := make(chan string, 1)
	go func() {
		v, _ := c.GetOrLoad(ctx, key, time.Minute, loader)
		done <- v
	}()
	<-readDone // 时序由握手保证，不再依赖 sleep 撞运气
	db.Store(101)
	c.Del(ctx, key) // 先删（空挥）后更——坏例时序里 DEL 先于更库，此处两者都已发生

	got := <-done
	if got != "100" {
		t.Fatalf("回填值 = %s， want 旧值 100（坏例未复现）", got)
	}
	if again, _ := c.GetOrLoad(ctx, key, time.Minute, loader); again != "100" {
		t.Errorf("后续读 = %s， want 100（旧值应驻留缓存）", again)
	}
	c.Del(ctx, key)
}

// TestUpdateFirstUnlockedStaleWindow：验收②后半——先更后删（失效不拿锁）的窄窗口。
// 读者回源在途时写者完成更库+DEL，回填落在 DEL 之后，旧值进缓存。
func TestUpdateFirstUnlockedStaleWindow(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	var db atomic.Int64
	db.Store(200)
	key := "test:update-first-unlocked"
	c.Del(ctx, key)

	readDone := make(chan struct{})
	loader := func(ctx context.Context) (string, error) {
		v := strconv.FormatInt(db.Load(), 10)
		close(readDone)
		time.Sleep(60 * time.Millisecond)
		return v, nil
	}

	done := make(chan string, 1)
	go func() {
		v, _ := c.GetOrLoad(ctx, key, time.Minute, loader)
		done <- v
	}()
	<-readDone
	db.Store(201)
	c.Del(ctx, key) // 更库后删——但删在回填之前，拦不住它

	got := <-done
	if got != "200" {
		t.Fatalf("回填值 = %s， want 旧值 200（窄窗口未复现）", got)
	}
	if again, _ := c.GetOrLoad(ctx, key, time.Minute, loader); again != "200" {
		t.Errorf("后续读 = %s， want 200（旧值应驻留缓存）", again)
	}
	c.Del(ctx, key)
}

// TestInvalidateLockedClosesWindow：最终策略的核心机制断言——
// 失效与回填共用一把锁后，"在途回填"必然先落地、后被失效删掉，旧值无处驻留。
func TestInvalidateLockedClosesWindow(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	var db atomic.Int64
	db.Store(300)
	key := "test:invalidate-locked"
	c.Del(ctx, key)

	loader := func(ctx context.Context) (string, error) {
		v := strconv.FormatInt(db.Load(), 10)
		time.Sleep(60 * time.Millisecond)
		return v, nil
	}

	done := make(chan string, 1)
	go func() {
		v, _ := c.GetOrLoad(ctx, key, time.Minute, loader)
		done <- v
	}()
	time.Sleep(15 * time.Millisecond)
	db.Store(301)
	c.InvalidateLocked(ctx, key) // 等在途回填落地后再删

	got := <-done
	if got != "300" {
		t.Fatalf("在途回填值 = %s， want 300", got)
	}
	// 关键差异：锁内失效删掉了刚回填的旧值，下一个读者必须回源拿到新值
	next, err := c.GetOrLoad(ctx, key, time.Minute, loader)
	if err != nil || next != "301" {
		t.Errorf("失效后读 = %s err=%v， want 301（旧值不应驻留）", next, err)
	}
	c.Del(ctx, key)
}

// TestFinalStrategyZeroDirty：验收③——2 万次读写交错（演示跑满 10 万），
// 最终策略下脏读必须为 0。
func TestFinalStrategyZeroDirty(t *testing.T) {
	c := newTestCache(t)
	ctx := context.Background()
	var db atomic.Int64
	db.Store(1000)
	const key = "test:final"
	const totalReads = 20000

	loader := func(ctx context.Context) (string, error) {
		return strconv.FormatInt(db.Load(), 10), nil
	}

	var dirty, reads atomic.Int64
	invalidated := atomic.Int64{} // 已完成失效的版本号（判定基准，见 main.go 注释）
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < totalReads/4; i++ {
				r0 := invalidated.Load()
				v, err := c.GetOrLoad(ctx, key, time.Minute, loader)
				if err != nil {
					t.Errorf("读失败: %v", err)
					return
				}
				reads.Add(1)
				got, _ := strconv.ParseInt(v, 10, 64)
				if got < r0 {
					dirty.Add(1)
				}
			}
		}()
	}
	var writes int64
	for writes < 200 {
		db.Add(1)
		writes++
		c.InvalidateLocked(ctx, key)
		invalidated.Store(writes)
		time.Sleep(100 * time.Microsecond)
	}
	wg.Wait()

	if d := dirty.Load(); d != 0 {
		t.Errorf("2 万次交错出现 %d 次脏读——失效锁未生效", d)
	}
	if reads.Load() != totalReads {
		t.Errorf("完成 %d 次读， want %d", reads.Load(), totalReads)
	}
}
