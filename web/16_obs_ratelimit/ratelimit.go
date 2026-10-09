package main

// ratelimit.go：Redis ZSET 滑动窗口限流（Lua 保证原子）+ 可观测三件套接线。
//
// 滑动窗口的语义：任意"窗口时长"内的请求数不得超过 limit。ZSET 的成员是
// 每次请求（score=时间戳 ms），每次判定先清出窗口外的旧成员，再数窗口内数量。
// INCR+EXPIRE 的固定窗口有边界突刺（窗口交界处可放 2×limit），ZSET 版没有。
//
// 原子性靠 Lua：清旧、计数、写入、续期四步在 Redis 单线程里一次执行完，
// 并发请求不会互相踩踏（不显式加锁）。
//
// 超限响应：429 + Retry-After（窗口内最老请求离开后即可再进来——精确到请求，
// 而不是"等满整个窗口"）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// luaSlidingWindow：返回 {允许?(1/0), retry_after_ms}。
const luaSlidingWindow = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local n = redis.call('ZCARD', key)
if n < limit then
  redis.call('ZADD', key, now, tostring(now) .. ':' .. tostring(redis.call('INCR', key .. ':seq')))
  redis.call('PEXPIRE', key, window)
  redis.call('PEXPIRE', key .. ':seq', window)
  return {1, 0}
end
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local retry = math.ceil((tonumber(oldest[2]) + window - now))
return {0, retry}
`

// RateLimiter：按用户维度的滑动窗口限流器。
type RateLimiter struct {
	rdb    *redis.Client
	script *redis.Script
	limit  int64
	window time.Duration

	exceeded atomic.Int64 // 指标：rate_limited_total（全局累计，实验 14 的口径）
}

func NewRateLimiter(addr string, limit int64, window time.Duration) (*RateLimiter, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, PoolSize: 100,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, errors.New("redis 不可用（先 docker start marklet-redis）: " + err.Error())
	}
	return &RateLimiter{
		rdb: rdb, script: redis.NewScript(luaSlidingWindow),
		limit: limit, window: window,
	}, nil
}

func (r *RateLimiter) Close() error { return r.rdb.Close() }

// ExceededTotal：被限流请求的累计数（观测断言用）。
func (r *RateLimiter) ExceededTotal() int64 { return r.exceeded.Load() }

// Allow：判定一次请求。ok=false 时 retryAfter 是建议等待时长。
func (r *RateLimiter) Allow(ctx context.Context, user string) (ok bool, retryAfter time.Duration, err error) {
	now := time.Now().UnixMilli()
	res, err := r.script.Run(ctx, r.rdb, []string{"ratelimit:" + user},
		now, r.window.Milliseconds(), r.limit).Slice()
	if err != nil {
		return false, 0, err // Redis 故障：fail-open 还是 fail-close 是另一个决策，这里上抛
	}
	allowed, _ := res[0].(int64)
	if allowed == 1 {
		return true, 0, nil
	}
	retryMs, _ := res[1].(int64)
	r.exceeded.Add(1)
	return false, time.Duration(retryMs) * time.Millisecond, nil
}

// Middleware：限流中间件。被限流：429 + Retry-After + 观测三件套
// （日志带 request_id、计数器累加、span 打属性——联动断言见测试）。
func (r *RateLimiter) Middleware(obs *Obs, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user := req.Header.Get("X-User-Id")
		if user == "" {
			user = "anonymous"
		}
		ok, retryAfter, err := r.Allow(req.Context(), user)
		if err != nil {
			http.Error(w, "rate limiter unavailable", http.StatusInternalServerError)
			return
		}
		if ok {
			next.ServeHTTP(w, req)
			return
		}
		retrySec := int64(retryAfter/time.Second) + 1 // 向上取整到秒（HTTP 是秒精度）
		w.Header().Set("Retry-After", fmt.Sprint(retrySec))
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		obs.RateLimited(req, user, retryAfter)
	})
}
