package main

// obs.go：slog 结构化日志 + 请求 ID 贯通 + 敏感字段脱敏。
//
// 三件事：
//
//	请求 ID：客户端传 X-Request-Id 就透传，没传就生成；进 context，
//	         中间件与 handler 的每条日志都带同一个值（断言跨层一致）
//	结构化：slog JSON handler——机器可检索（grep request_id=xxx 一屏聚合）
//	脱敏：  password/token 一类字段只允许经 Redact 写入日志，值替换为 ***
//
// 脱敏的正确位置是"写字段的助手"，不是"人记得别写"——助手强制、人记性不可靠。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type ctxKey string

const (
	reqIDKey  ctxKey = "request_id"
	loggerKey ctxKey = "logger"
)

// Redact：敏感值的替身。日志里只允许出现它。
const Redact = "***"

// NewLogger：JSON slog，输出 stderr——容器世界的标准日志通道。
func NewLogger() *slog.Logger {
	return newBufLogger(os.Stderr)
}

// newBufLogger：JSON slog 到任意写入端（测试注入 buffer）。
func newBufLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// NewRequestID：16 字节 crypto/rand → 32 hex。
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// RequestIDFrom：取请求 ID；中间件保证一定存在。
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(reqIDKey).(string)
	if v == "" {
		return "unknown"
	}
	return v
}

// LoggerFrom：取带 request_id 预注的 logger。
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// RequestID：请求 ID 中间件（根 logger 取全局默认）。
func RequestID(next http.Handler) http.Handler {
	return requestIDWith(slog.Default())(next)
}

// RequestIDWithRoot：测试可注入根 logger 的版本。
func RequestIDWithRoot(root *slog.Logger) func(http.Handler) http.Handler {
	return requestIDWith(root)
}

func requestIDWith(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
			if id == "" || len(id) > 128 { // 超长/为空都重新生成，防头部注入垃圾
				id = NewRequestID()
			}
			logger := base.With("request_id", id)
			ctx := context.WithValue(r.Context(), reqIDKey, id)
			ctx = context.WithValue(ctx, loggerKey, logger)
			w.Header().Set("X-Request-Id", id) // 响应头回带，客户端可对账
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog：访问日志中间件——方法、路径、状态、耗时，request_id 已在 logger 里。
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := timeNow()
		logger := LoggerFrom(r.Context())
		next.ServeHTTP(w, r)
		logger.Info("access",
			"method", r.Method,
			"path", r.URL.Path,
			"duration_ms", timeNow().Sub(start).Milliseconds(),
		)
	})
}

// LogSafe：脱敏写日志的唯一入口——keysAndValues 里的 password/token/
// authorization/secret 键，值一律替换。新增敏感字段时在这里加键名，
// 而不是祈祷每个调用点都记得。
func LogSafe(logger *slog.Logger, level slog.Level, msg string, keysAndValues ...any) {
	safe := make([]any, 0, len(keysAndValues))
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		key, _ := keysAndValues[i].(string)
		if isSensitive(key) {
			if _, nonEmpty := keysAndValues[i+1].(string); nonEmpty && keysAndValues[i+1] != "" {
				keysAndValues[i+1] = Redact
			}
		}
		safe = append(safe, keysAndValues[i], keysAndValues[i+1])
	}
	logger.Log(context.Background(), level, msg, safe...)
}

// slogLevelInfo：LogSafe 的便捷级别常量。
const slogLevelInfo = slog.LevelInfo

func isSensitive(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.Contains(k, "token") ||
		strings.Contains(k, "authorization") || strings.Contains(k, "secret")
}
