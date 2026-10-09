// 实验 08：缓存一致性策略实测——先删后更、先更后删与最终策略。
//
// 数据库 = 一个原子递增的版本号（真实库的替身，脏读判定因此可精确化：
// 读到的版本 < 读开始时已完成的最新版本 = 脏读）。
//
//	第 1 步：先删后更的坏例——DEL 之后、更库之前，并发回源把旧值填回去，
//	        旧值驻留整个 TTL（编排式复现）
//	第 2 步：先更后删（无锁）的坏例——读 miss 回源读到旧值，回填发生在 DEL
//	        之后（窄窗口，编排式复现）
//	第 3 步：最终策略——更库 + InvalidateLocked（失效与回填共用一把 per-key 锁）
//	        + TTL 兜底：10 万次读写交错，脏读必须为 0
//
// 前置：Redis 容器（16379，见实验 07）。运行方式：go run .；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const redisAddr = "127.0.0.1:16379"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	ctx := context.Background()
	cache, err := NewCache(redisAddr)
	if err != nil {
		fatal(err)
	}
	defer cache.Close()

	fmt.Println("== 第 1 步：先删后更——旧值回填，驻留整个 TTL ==")
	runDeleteFirst(ctx, cache)

	fmt.Println("== 第 2 步：先更后删（无锁）——回填发生在失效之后的窄窗口 ==")
	runUpdateFirstUnlocked(ctx, cache)

	fmt.Println("== 第 3 步：最终策略——10 万次读写交错，脏读必须为 0 ==")
	runFinalStrategy(ctx, cache)
}

// runDeleteFirst：编排式复现先删后更。
// 读者回源（已读旧值）→ 写者 DEL+更库 → 读者回填旧值 → 后续读者全部读到旧值。
func runDeleteFirst(ctx context.Context, cache *Cache) {
	var db atomic.Int64
	db.Store(100)
	key := "strategy:delete-first"
	cache.Del(ctx, key) // 确保冷启动

	loader := func(ctx context.Context) (string, error) {
		v := strconv.FormatInt(db.Load(), 10) // 回源读到旧值
		time.Sleep(80 * time.Millisecond)     // 业务耗时窗口：写者在这期间完成 DEL+更库
		return v, nil
	}

	done := make(chan string, 1)
	go func() {
		v, _ := cache.GetOrLoad(ctx, key, time.Minute, loader)
		done <- v
	}()
	time.Sleep(20 * time.Millisecond) // 读者已进入回源，旧值在手
	db.Store(101)                     // 写侧：先删（DEL，此处本来就空）后更库
	cache.Del(ctx, key)
	reader1 := <-done // 读者回填旧值 100
	reader2, _ := cache.GetOrLoad(ctx, key, time.Minute, loader)

	fmt.Printf("  库已是 101，缓存命中 %s（写入发生在 DEL 之后的回填）\n", reader1)
	fmt.Printf("  下一个读者仍命中 %s——旧值将驻留整个 TTL（%v）\n", reader2, time.Minute)
	_ = reader2
}

// runUpdateFirstUnlocked：编排式复现先更后删（失效不拿回填的锁）。
// 读者 miss 回源（慢）→ 写者更库 + DEL（此刻缓存还是空，DEL 空挥）→ 读者回填旧值。
func runUpdateFirstUnlocked(ctx context.Context, cache *Cache) {
	var db atomic.Int64
	db.Store(200)
	key := "strategy:update-first-unlocked"
	cache.Del(ctx, key)

	loader := func(ctx context.Context) (string, error) {
		v := strconv.FormatInt(db.Load(), 10) // 此刻读到旧值 200
		time.Sleep(80 * time.Millisecond)     // 回源耗时：写者的更库+DEL 落在这窗口里
		return v, nil
	}

	done := make(chan string, 1)
	go func() {
		v, _ := cache.GetOrLoad(ctx, key, time.Minute, loader)
		done <- v
	}()
	time.Sleep(20 * time.Millisecond) // 读者已回源读走旧值
	db.Store(201)                     // 写侧：先更库
	cache.Del(ctx, key)               // 再删缓存——DEL 空挥（回填还没发生）
	reader1 := <-done                 // 读者的回填落在 DEL 之后，旧值 200 进缓存
	reader2, _ := cache.GetOrLoad(ctx, key, time.Minute, loader)

	fmt.Printf("  更库+DEL 都已完成，缓存命中 %s（回填晚了 DEL 一步）\n", reader1)
	fmt.Printf("  下一个读者仍命中 %s——窄窗口，但真实流量下必然被踩中\n", reader2)
	_ = reader2
}

// runFinalStrategy：更库 + InvalidateLocked + TTL 兜底，10 万次交错零脏读。
// 脏读判定：r0 取"已失效完成的版本号"（invalidated，写者每次失效完成后推进），
// 读开始时采样 r0，读到更老的值 = 失效承诺被打破。db 与 invalidated 之间的
// 窗口（已更库、未失效完）是 cache-aside 天生的最终一致窗口，不在追责范围。
func runFinalStrategy(ctx context.Context, cache *Cache) {
	var db atomic.Int64
	db.Store(1000)
	const key = "strategy:final"
	const totalReads = 100000

	loader := func(ctx context.Context) (string, error) {
		return strconv.FormatInt(db.Load(), 10), nil // 回源总是读到当下版本
	}

	var dirty, reads atomic.Int64
	invalidated := atomic.Int64{} // 已完成失效的版本号（单一写者，单调递增）
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 4 个读者 × 25000 次读，每次读记录 r0 并与读到值比较
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < totalReads/4; i++ {
				select {
				case <-stop:
					return
				default:
				}
				r0 := invalidated.Load()
				v, err := cache.GetOrLoad(ctx, key, time.Minute, loader)
				if err != nil {
					fmt.Fprintln(os.Stderr, "读失败:", err)
					return
				}
				reads.Add(1)
				got, _ := strconv.ParseInt(v, 10, 64)
				if got < r0 { // 读开始前已完成的版本，读到更老的 = 脏读
					dirty.Add(1)
				}
			}
		}()
	}

	// 写者：500 次更库，每次走最终策略失效
	var writes int64
	for writes < 500 {
		db.Add(1)
		writes++
		cache.InvalidateLocked(ctx, key)
		invalidated.Store(writes) // 失效完成，承诺推进：此后不许再读到 < writes 的值
		time.Sleep(200 * time.Microsecond)
	}
	wg.Wait()
	close(stop)

	hits, _, loads := cache.Stats()
	fmt.Printf("  读 %d 次（命中 %d/回源 %d），写 %d 次，脏读 %d\n",
		reads.Load(), hits, loads, writes, dirty.Load())
	fmt.Println("  ⇒ 失效与回填共用一把锁后，两种交错都被顺序钉死；TTL 只是兜底，不再被依赖。")
}
