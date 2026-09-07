// Token Bucket Rate Limiter
// Interview topic: rate limiting, concurrency safety with sync.Mutex

package main

import (
	"fmt"
	"sync"
	"time"
)

// ── 1. Token Bucket Core ──

// TokenBucket implements a token-bucket rate limiter.
// Tokens are refilled at `rate` per second, up to `burst` max capacity.
type TokenBucket struct {
	tokens float64
	rate   float64 // tokens per second
	burst  float64 // maximum bucket capacity
	last   time.Time
	mu     sync.Mutex
}

// NewTokenBucket creates a bucket filled to burst capacity.
func NewTokenBucket(rate, burst float64) *TokenBucket {
	return &TokenBucket{
		tokens: burst,
		rate:   rate,
		burst:  burst,
		last:   time.Now(),
	}
}

// refill adds tokens based on elapsed time since last refill.
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.last).Seconds()
	if elapsed > 0 {
		tb.tokens += tb.rate * elapsed
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}
		tb.last = now
	}
}

// Allow tries to consume one token. Returns true if allowed.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// ── 2. Demo: Concurrent Requests ──

func demo() {
	fmt.Println("=== Token Bucket Rate Limiter ===")
	fmt.Println()

	tb := NewTokenBucket(10, 20)

	fmt.Printf("Config: rate=%.0f tokens/sec, burst=%.0f\n", tb.rate, tb.burst)
	fmt.Println()

	var mu sync.Mutex
	allowed := 0
	rejected := 0

	// 100 goroutines fire a request each, roughly simultaneously
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ok := tb.Allow()
			mu.Lock()
			if ok {
				allowed++
				fmt.Printf("  [goroutine %02d] ALLOWED\n", id)
			} else {
				rejected++
				fmt.Printf("  [goroutine %02d] REJECTED (no tokens)\n", id)
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	fmt.Println()
	fmt.Printf("Results: %d allowed, %d rejected (total=%d)\n", allowed, rejected, allowed+rejected)
	fmt.Println()
	fmt.Printf("Remaining tokens: %.2f\n", tb.tokens)
}

// ── 3. Sequential Demo ──

func demoSequential() {
	fmt.Println("--- Sequential Demo ---")
	fmt.Println()

	tb := NewTokenBucket(2, 5) // 2 tokens/sec, burst 5

	for i := 0; i < 10; i++ {
		ok := tb.Allow()
		status := "ALLOWED"
		if !ok {
			status = "REJECTED"
		}
		fmt.Printf("  Request %2d: %s (tokens=%.2f)\n", i+1, status, tb.tokens)
		time.Sleep(200 * time.Millisecond) // 200ms between requests
	}
}

// ── 4. Main ──

func main() {
	demo()
	fmt.Println()
	demoSequential()
}
