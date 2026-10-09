package main

// cache.go：cache-aside 读路径（实验 07 同款）+ 三种写策略共用的失效原语。
//
// 写缓存一致性的三个候选策略，写侧各自组合：
//
//	先删后更：DEL 缓存 → 更库     （坏例：并发回源把旧值填回去，驻留整个 TTL）
//	先更后删：更库 → DEL 缓存     （坏例：读 miss 回源读到旧值，回填发生在 DEL 之后）
//	最终策略：更库 → InvalidateLocked（失效与回填共用同一把 per-key 锁 + TTL 兜底）
//
// 关键洞察：先更后删的残余窗口源于"失效"与"回填"互不知晓——失效不拿回填的锁，
// 就拦不住一个正在飞行中的旧值回填。失效走同一把锁，两种交错都被顺序钉死。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound：回源未命中的哨兵错误。
var ErrNotFound = errors.New("not found")

// Cache：cache-aside 读路径 + 可共享的 per-key 锁表。
type Cache struct {
	rdb   *redis.Client
	locks keyLocks

	hits   atomic.Int64
	misses atomic.Int64
	loads  atomic.Int64
}

// NewCache：连接 Redis（127.0.0.1:16379）。池按千级并发配置。
func NewCache(addr string) (*Cache, error) {
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

func (c *Cache) Close() error { return c.rdb.Close() }

// GetOrLoad：cache-aside 读路径。per-key 互斥 + 锁内重查（实验 07 的恰好一次回源）。
func (c *Cache) GetOrLoad(ctx context.Context, key string, ttl time.Duration,
	load func(ctx context.Context) (string, error)) (string, error) {

	if v, ok, err := c.tryGet(ctx, key); ok || err != nil {
		return v, err
	}
	c.misses.Add(1)

	mu := c.locks.get(key)
	mu.Lock()
	defer mu.Unlock()
	if v, ok, err := c.tryGet(ctx, key); ok || err != nil {
		return v, err
	}

	c.loads.Add(1)
	val, err := load(ctx)
	if err != nil {
		return "", err
	}
	c.rdb.Set(ctx, key, val, ttl)
	return val, nil
}

// Del：裸删除（先删后更策略的写侧原语，故意不拿锁——它是坏例的一部分）。
func (c *Cache) Del(ctx context.Context, key string) {
	c.rdb.Del(ctx, key)
}

// InvalidateLocked：最终策略的失效原语——与回填共用同一把 per-key 锁。
// 等待中的回填完成回填后才轮到失效（失效删掉它），失效先做则拦住后续回填起点。
// 无论哪种交错，锁释放时缓存里都不残留旧值。
func (c *Cache) InvalidateLocked(ctx context.Context, key string) {
	mu := c.locks.get(key)
	mu.Lock()
	defer mu.Unlock()
	c.rdb.Del(ctx, key)
}

// Stats：hits/misses/loads 计数。
func (c *Cache) Stats() (hits, misses, loads int64) {
	return c.hits.Load(), c.misses.Load(), c.loads.Load()
}

func (c *Cache) tryGet(ctx context.Context, key string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, key).Result()
	if err == nil {
		c.hits.Add(1)
		return v, true, nil
	}
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	return "", false, err
}

// keyLocks：per-key 互斥锁表（实验 07 同款）。
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
