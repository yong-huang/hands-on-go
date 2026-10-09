// 实验 07：Redis 缓存层——cache-aside、singleflight 防击穿、空值缓存防穿透。
//
//	第 1 步：cache-aside 回路——首读未命中回源回填，二读命中（命中率计数就位）
//	第 2 步：击穿防护——1000 并发查同一冷 key，回源次数必须是 1（singleflight 合并）
//	第 3 步：穿透防护——查询不存在的 key，两波 1000 并发，回源仍只有 1（空值缓存）
//	第 4 步：命中率实测——预热 10 个 key 后读 1000 次，命中率应 ≥ 90%
//
// 前置：Redis 容器（redis:7-alpine，16379）：
//
//	docker run -d --name marklet-redis -p 16379:6379 redis:7-alpine
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

const redisAddr = "127.0.0.1:16379"

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// herdGet：barrier 起步的并发读——count 个请求真正同时打向同一个 key，
// 而不是 spawn 错峰。返回这批请求的回源次数。
func herdGet(ctx context.Context, c *Cache, key string, count int,
	load func(ctx context.Context) (string, error)) int64 {

	start := make(chan struct{})
	var wg sync.WaitGroup
	before := func() int64 { _, _, n := c.Stats(); return n }()
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := c.GetOrLoad(ctx, key, time.Minute, load); err != nil && err != ErrNotFound {
				fmt.Fprintln(os.Stderr, "并发读失败:", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	_, _, after := c.Stats()
	return after - before
}

func main() {
	ctx := context.Background()
	cache, err := NewCache(redisAddr)
	if err != nil {
		fatal(err)
	}
	defer cache.Close()
	// 每次运行用独立 key 前缀：上一轮的缓存不该污染这一轮的演示数据
	p := fmt.Sprintf("demo%d:", time.Now().UnixNano())

	fmt.Println("== 第 1 步：cache-aside 回路——首读回源，二读命中 ==")
	loader := func(ctx context.Context) (string, error) { return "《Go 官网》", nil }
	v1, err := cache.GetOrLoad(ctx, p+"demo:key", time.Minute, loader)
	if err != nil {
		fatal(err)
	}
	v2, err := cache.GetOrLoad(ctx, p+"demo:key", time.Minute,
		func(ctx context.Context) (string, error) { return "不该被执行", nil })
	if err != nil {
		fatal(err)
	}
	hits, misses, loads := cache.Stats()
	fmt.Printf("  首读 → %q（回源 1 次），二读 → %q（回源仍 1 次）\n", v1, v2)
	fmt.Printf("  计数：hits=%d misses=%d loads=%d——首读 miss、二读 hit，回路闭合\n", hits, misses, loads)

	fmt.Println("== 第 2 步：击穿防护——1000 并发查同一冷 key，回源必须 = 1 ==")
	n := herdGet(ctx, cache, p+"hot:cold-start", 1000,
		func(ctx context.Context) (string, error) {
			time.Sleep(50 * time.Millisecond) // 回源窗口须盖过 1000 个请求的读处理时长（Redis 串行 ~20ms）
			return "热点数据", nil
		})
	fmt.Printf("  1000 个并发请求，回源 %d 次——singleflight 把同 key 风暴合并成一次加载\n", n)

	fmt.Println("== 第 3 步：穿透防护——不存在的 key，空值缓存终结风暴 ==")
	ghostLoader := func(ctx context.Context) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "", ErrNotFound
	}
	first := herdGet(ctx, cache, p+"ghost:never-exists", 50, ghostLoader)
	time.Sleep(50 * time.Millisecond)
	second := herdGet(ctx, cache, p+"ghost:never-exists", 50, ghostLoader)
	fmt.Printf("  第一波回源 %d 次（加载失败→空值入缓存），第二波回源 %d 次（空值直接终结）\n",
		first, second)

	fmt.Println("== 第 4 步：命中率实测——预热后大批量读 ==")
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("%swarm:%d", p, i)
		if _, err := cache.GetOrLoad(ctx, key, time.Minute, loader); err != nil {
			fatal(err)
		}
	}
	hits0, _, _ := cache.Stats()
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("warm:%d", i%10)
		if _, err := cache.GetOrLoad(ctx, key, time.Minute,
			func(ctx context.Context) (string, error) { return "不该回源", nil }); err != nil {
			fatal(err)
		}
	}
	hits, _, _ = cache.Stats()
	rate := float64(hits-hits0) / 1000 * 100
	fmt.Printf("  预热 10 个 key，读 1000 次：命中 %d 次，命中率 %.1f%%（验收线 90%%）\n",
		hits-hits0, rate)
}
