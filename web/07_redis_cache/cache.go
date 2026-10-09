package main

// cache.go：cache-aside 读路径 + 精确单次回源（防击穿）+ 空值缓存（防穿透）。
//
// cache-aside 的读序：先查缓存，命中直接回；未命中回源（DB/上游），回填缓存并设 TTL。
// 两个经典事故与各自解法：
//
//	击穿：热点 key 过期/冷启动瞬间，千级并发同时回源 → per-key 互斥 + 锁内重查，
//	      回源次数 =1 是硬保证（singleflight 的本质形态，见 GetOrLoad 的注释）
//	穿透：查询根本不存在的 key，缓存永远不命中，回源被打满 → 空值也缓存（短 TTL）
//
// 与 x/sync/singleflight（concurrency/09 手写其机制）的关系：WaitGroup 版只能合并
// "同时在飞"的请求，任何固定窗口在千级并发下都有边界泄漏；本篇要的是可断言的
// "恰好一次"，所以用互斥锁版——阻塞等价于无限大的合并窗口。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound：回源未命中的哨兵错误——调用方与缓存层的边界语义。
var ErrNotFound = errors.New("not found")

// nilMarker：空值缓存的占位值。穿透防护的关键：不存在也缓存，
// 让后续同 key 请求在缓存层终结，而不是穿透到回源。
const nilMarker = "\x00nil"

// nilMarkerTTL：空值标记的存活期，且命中即滑动续期——被持续查询的"不存在"
// 持续生效（TTL 永远跑不赢查询队列），风暴平息后 2 秒自然过期，
// "后来真存在"的数据最迟在无人问津 2 秒后可见。
const nilMarkerTTL = 2 * time.Second

// Cache：cache-aside 读路径。三个原子计数器就是验收的观测点。
type Cache struct {
	rdb   *redis.Client
	locks keyLocks

	hits   atomic.Int64
	misses atomic.Int64
	loads  atomic.Int64 // 真实回源次数（合并后）
}

// NewCache：连接 Redis（127.0.0.1:16379，容器见 main.go 头注释）。
func NewCache(addr string) (*Cache, error) {
	// 池与超时按千级并发配置：默认池（10×CPU）在 1000 并发下会排队到 i/o timeout
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		PoolSize:     200,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, errors.New("redis 不可用（先 docker start marklet-redis）: " + err.Error())
	}
	return &Cache{rdb: rdb}, nil
}

// Close：释放连接。
func (c *Cache) Close() error { return c.rdb.Close() }

// GetOrLoad：cache-aside 读路径的完整实现。
//   - 命中：直接返回（hits+1）
//   - 未命中：进入该 key 的临界区，锁内重查后再回源（loads+1）并回填
//   - 回源 ErrNotFound：缓存空值标记（短 TTL，防穿透）
//
// per-key 锁 + 锁内重查为什么是"恰好一次"：前任的回填 happens-before 它释放锁，
// 后任拿到锁后的重查必然命中——阻塞等价于无限大的合并窗口，不靠调度运气。
func (c *Cache) GetOrLoad(ctx context.Context, key string, ttl time.Duration,
	load func(ctx context.Context) (string, error)) (string, error) {

	if v, ok, err := c.tryGet(ctx, key); ok || err != nil {
		return v, err
	}
	c.misses.Add(1)

	mu := c.locks.get(key)
	mu.Lock()
	defer mu.Unlock()

	// 临界区内重查：阻塞期间别人的回填已经落地
	if v, ok, err := c.tryGet(ctx, key); ok || err != nil {
		return v, err
	}

	c.loads.Add(1)
	val, err := load(ctx)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// 空值缓存：TTL 取 5s——足够盖过最慢的请求队列（见 Q// 空值缓存：短 TTL 足够挡住穿透窗口，又不会让"后来真存在"的数据迟到太久A），又不至于让"后来
			// 真存在"的数据迟到太久
			c.rdb.Set(ctx, key, nilMarker, 5*time.Second)
		}
		return "", err
	}
	c.rdb.Set(ctx, key, val, ttl)
	return val, nil
}

// tryGet：查一次缓存。ok=false 表示未命中（Redis 自身故障走 err 通道，
// 不被当成未命中——故障该报警，而不是触发回源风暴）。
func (c *Cache) tryGet(ctx context.Context, key string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, key).Result()
	if err == nil {
		c.hits.Add(1)
		if v == nilMarker {
			c.rdb.Expire(ctx, key, nilMarkerTTL) // 滑动续期：错误可忽略，尽力而为
			return "", false, ErrNotFound
		}
		return v, true, nil
	}
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	return "", false, err
}

// Stats：命中/未命中/回源计数（演示与测试的观测点）。
func (c *Cache) Stats() (hits, misses, loads int64) {
	return c.hits.Load(), c.misses.Load(), c.loads.Load()
}

// keyLocks：per-key 互斥锁表。演示规模下不淘汰（锁数量 = 历史冷 key 数）；
// 生产化路径是分片锁或带淘汰的并发 map——README 的 Q&A 有展开。
type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (k *keyLocks) get(key string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	if m, ok := k.locks[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	k.locks[key] = m
	return m
}
