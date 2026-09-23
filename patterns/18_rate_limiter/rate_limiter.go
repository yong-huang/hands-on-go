// Sliding Window Rate Limiter Middleware
// Interview topic: rate limiting, sliding window, HTTP middleware, IP-level throttling

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// ── 1. Sliding Window Rate Limiter Core ──

// RateLimiter implements IP-level sliding window rate limiting.
// Each IP address has its own window of request timestamps.
type RateLimiter struct {
	mu         sync.Mutex
	requests   map[string][]time.Time // IP → sorted timestamps
	window     time.Duration          // window length (e.g. 1 second)
	maxRequests int                   // max requests allowed per window
}

// NewRateLimiter creates a limiter with the given window size and max requests.
func NewRateLimiter(window time.Duration, max int) *RateLimiter {
	return &RateLimiter{
		requests:   make(map[string][]time.Time),
		window:     window,
		maxRequests: max,
	}
}

// Allow checks whether a request from the given IP is within the rate limit.
// It removes expired timestamps, checks the count, and records the new timestamp.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Remove timestamps outside the current sliding window
	timestamps := rl.requests[ip]
	valid := timestamps[:0] // reuse underlying array
	for _, t := range timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.requests[ip] = valid

	// Check if within limit
	if len(valid) >= rl.maxRequests {
		return false
	}

	// Record this request
	rl.requests[ip] = append(valid, now)
	return true
}

// ── 2. HTTP Middleware ──

// Middleware returns an HTTP middleware that rate-limits by client IP.
// It extracts IP from X-Forwarded-For or X-Real-Ip headers, falling back to RemoteAddr.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !rl.Allow(ip) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, "429 Too Many Requests — rate limit exceeded for %s\n", ip)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP gets the client IP from headers or RemoteAddr.
func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For: client, proxy1, proxy2
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	if rip := r.Header.Get("X-Real-Ip"); rip != "" {
		return strings.TrimSpace(rip)
	}
	// RemoteAddr is "ip:port"
	parts := strings.Split(r.RemoteAddr, ":")
	return parts[0]
}

// ── 3. Demo ──

func main() {
	fmt.Println("=== Sliding Window Rate Limiter Middleware ===")
	fmt.Println()

	// Config: 1-second window, max 3 requests per IP
	rl := NewRateLimiter(1*time.Second, 3)
	fmt.Printf("Config: window=%v, maxRequests=%d\n\n", rl.window, rl.maxRequests)

	// Create a simple downstream handler
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK — request from %s\n", extractIP(r))
	})

	// Wrap with rate limiter middleware
	handler := rl.Middleware(downstream)

	// ── Test 1: Single IP exceeds limit ──
	fmt.Println("--- Test 1: Single IP (192.168.1.1) sends 5 requests ---")
	fmt.Println()
	for i := 1; i <= 5; i++ {
		req := httptest.NewRequest("GET", "/api", nil)
		req.Header.Set("X-Forwarded-For", "192.168.1.1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Printf("  Request %d: [%d] %s", i, rec.Code, strings.TrimSpace(rec.Body.String()))
		if rec.Code == 429 {
			fmt.Print(" (BLOCKED)")
		}
		fmt.Println()
	}

	// ── Test 2: Different IPs each get their own quota ──
	fmt.Println()
	fmt.Println("--- Test 2: Three different IPs, 3 requests each ---")
	fmt.Println()
	ips := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	for _, ip := range ips {
		for reqIdx := 1; reqIdx <= 3; reqIdx++ {
			req := httptest.NewRequest("GET", "/api", nil)
			req.Header.Set("X-Real-Ip", ip)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			fmt.Printf("  %s req %d: [%d] %s\n", ip, reqIdx, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	// ── Test 3: IP gets blocked, then waits for window to expire ──
	fmt.Println()
	fmt.Println("--- Test 3: Window expiry (172.16.0.1) ---")
	fmt.Println()
	// Exhaust limit
	for i := 1; i <= 3; i++ {
		req := httptest.NewRequest("GET", "/api", nil)
		req.Header.Set("X-Forwarded-For", "172.16.0.1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Printf("  Before sleep — Request %d: [%d] %s\n", i, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	// One more should be rejected
	req := httptest.NewRequest("GET", "/api", nil)
	req.Header.Set("X-Forwarded-For", "172.16.0.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	fmt.Printf("  Before sleep — Request 4: [%d] %s (expected 429)\n", rec.Code, strings.TrimSpace(rec.Body.String()))

	// Wait for window to expire
	fmt.Println()
	fmt.Printf("  Sleeping %v to let window expire...\n", rl.window)
	time.Sleep(rl.window + 10*time.Millisecond)

	// After window expiry, should be allowed again
	req = httptest.NewRequest("GET", "/api", nil)
	req.Header.Set("X-Forwarded-For", "172.16.0.1")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	fmt.Printf("  After sleep  — Request 5: [%d] %s (window expired, should be 200)\n", rec.Code, strings.TrimSpace(rec.Body.String()))

	fmt.Println()

	// ── Test 4: RemoteAddr fallback ──
	fmt.Println("--- Test 4: RemoteAddr fallback (no headers) ---")
	fmt.Println()
	for i := 1; i <= 4; i++ {
		req := httptest.NewRequest("GET", "/api", nil)
		// Set RemoteAddr directly (httptest uses it)
		req.RemoteAddr = "1.2.3.4:5678"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		fmt.Printf("  Request %d: [%d] %s", i, rec.Code, strings.TrimSpace(rec.Body.String()))
		if rec.Code == 429 {
			fmt.Print(" (BLOCKED)")
		}
		fmt.Println()
	}
}
