package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestStack：小型窗口（n 请求 / w 时长）+ buffer 日志 + 内存导出器。
func newTestStack(t *testing.T, limit int64, window time.Duration) (*RateLimiter, *Obs, *spanRecorder, http.Handler, *testBuffer) {
	t.Helper()
	limiter, err := NewRateLimiter(redisAddr, limit, window)
	if err != nil {
		t.Skipf("Redis 不可用：%v", err)
	}
	t.Cleanup(func() { limiter.Close() })
	rec := &spanRecorder{}
	tp, err := SetupTracing(1.0, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	logBuf := newTestBuffer()
	obs := NewObs(logBuf, tp)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("done"))
	})
	// RequestID 最外层：它注入的 logger/context 对内层限流中间件可见
	var h http.Handler = limiter.Middleware(obs, mux)
	h = obs.RequestID(h)
	return limiter, obs, rec, h, logBuf
}

func fire(t *testing.T, h http.Handler, user, reqID string) (int, http.Header) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	req.Header.Set("X-User-Id", user)
	if reqID != "" {
		req.Header.Set("X-Request-Id", reqID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Header()
}

// TestSlidingWindowBoundary：验收①——limit 100，第 101 起 429 且带 Retry-After。
func TestSlidingWindowBoundary(t *testing.T) {
	limiter, _, _, h, _ := newTestStack(t, 100, 10*time.Second)
	user := fmt.Sprintf("boundary-%d", time.Now().UnixNano())

	allowed := 0
	var retryAfter string
	for i := 1; i <= 105; i++ {
		code, hdr := fire(t, h, user, "")
		if code == http.StatusOK {
			allowed++
			continue
		}
		if code != http.StatusTooManyRequests {
			t.Fatalf("第 %d 个请求 = %d", i, code)
		}
		if retryAfter == "" {
			retryAfter = hdr.Get("Retry-After")
		}
	}
	if allowed != 100 {
		t.Errorf("放行 %d， want 恰好 100", allowed)
	}
	if retryAfter == "" || retryAfter == "0" {
		t.Errorf("Retry-After = %q， want 正数秒", retryAfter)
	}
	if got := limiter.ExceededTotal(); got < 5 {
		t.Errorf("指标累计 %d， want ≥5", got)
	}
}

// TestWindowSlides：滑动语义——窗口滑过之后恢复放行（固定窗口做不到的连续性）。
func TestWindowSlides(t *testing.T) {
	limiter, _, _, h, _ := newTestStack(t, 3, 500*time.Millisecond)
	user := fmt.Sprintf("slides-%d", time.Now().UnixNano())

	for i := 0; i < 3; i++ {
		if code, _ := fire(t, h, user, ""); code != http.StatusOK {
			t.Fatalf("前 3 个应全过，第 %d 个 = %d", i+1, code)
		}
	}
	if code, _ := fire(t, h, user, ""); code != http.StatusTooManyRequests {
		t.Fatal("第 4 个应被限流")
	}
	time.Sleep(600 * time.Millisecond) // 窗口滑过
	if code, _ := fire(t, h, user, ""); code != http.StatusOK {
		t.Errorf("窗口滑过后仍被限流 = %d（滑动语义失效）", code)
	}
	_ = limiter
}

// TestFourPiecesCorrelation：验收②——被限流请求的 request_id 同时出现在
// 日志（buffer）、指标（计数 ≥1）、trace（span 属性）三处。
func TestFourPiecesCorrelation(t *testing.T) {
	_, obs, rec, h, logBuf := newTestStack(t, 1, 10*time.Second)
	user := fmt.Sprintf("corr-%d", time.Now().UnixNano())
	const probeID = "probe-corr-001"

	// 用掉唯一配额
	if code, _ := fire(t, h, user, "first"); code != http.StatusOK {
		t.Fatalf("首个请求 = %d", code)
	}
	code, _ := fire(t, h, user, probeID)
	if code != http.StatusTooManyRequests {
		t.Fatalf("探针 = %d， want 429", code)
	}

	// ① 日志：该 request_id 的 rate limited 行
	logText := logBuf.String()
	if !strings.Contains(logText, `"request_id":"`+probeID+`"`) ||
		!strings.Contains(logText, `"msg":"rate limited"`) {
		t.Errorf("日志缺联动记录:\n%s", logText)
	}
	// ② 指标：该用户计数 ≥1
	if got := obs.RateLimitedTotal(user); got < 1 {
		t.Errorf("指标 = %d， want ≥1", got)
	}
	// ③ 追踪：span 带 ratelimit.exceeded 属性
	found := false
	for _, s := range rec.collect() {
		if s.Attrs["ratelimit.exceeded"] == "true" {
			found = true
		}
	}
	if !found {
		t.Error("trace 无 ratelimit.exceeded 属性")
	}
	// 日志行是合法 JSON 且 request_id 与请求一致
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if !strings.Contains(line, "rate limited") {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Errorf("日志非 JSON: %v", err)
		}
	}
}

// TestPerUserIsolation：各用户独立窗口——bob 被限不影响 alice。
func TestPerUserIsolation(t *testing.T) {
	_, _, _, h, _ := newTestStack(t, 2, 10*time.Second)
	bob := fmt.Sprintf("bob-%d", time.Now().UnixNano())
	alice := fmt.Sprintf("alice-%d", time.Now().UnixNano())

	for i := 0; i < 2; i++ {
		if code, _ := fire(t, h, bob, ""); code != http.StatusOK {
			t.Fatal("bob 配额异常")
		}
	}
	if code, _ := fire(t, h, bob, ""); code != http.StatusTooManyRequests {
		t.Fatal("bob 应被限流")
	}
	if code, _ := fire(t, h, alice, ""); code != http.StatusOK {
		t.Errorf("alice 首请求 = %d， want 200（bob 的限流不应外溢）", code)
	}
}
