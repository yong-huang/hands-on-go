package main

// obs.go：观测四件套的第四块——把"被限流"这件事同时写进 日志/指标/trace，
// 三处以同一个 request_id 串起来（验收②的联动断言对象）。
//
// 日志：实验 13 的模式（JSON slog + request_id 注入）
// 指标：实验 14 的模式（计数器 + label）
// 追踪：实验 15 的模式（span + 属性）

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	stdouttrace "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Obs：观测集合——logger 根、内存指标表、TracerProvider。
type Obs struct {
	logger *slog.Logger
	counts *countTable
	tp     *sdktrace.TracerProvider
}

type countTable struct {
	mu sync.Mutex
	v  map[string]*int64
}

// NewObs：装配（logger 输出到 w，测试注入 buffer；tp 可为 nil——无追踪时跳过）。
func NewObs(w io.Writer, tp *sdktrace.TracerProvider) *Obs {
	return &Obs{
		logger: slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})),
		counts: &countTable{v: map[string]*int64{}},
		tp:     tp,
	}
}

// RateLimitedTotal：某用户被限流的累计次数（指标读取口）。
func (o *Obs) RateLimitedTotal(user string) int64 {
	o.counts.mu.Lock()
	defer o.counts.mu.Unlock()
	if n, ok := o.counts.v[user]; ok {
		return *n
	}
	return 0
}

// RequestID：透传/生成（实验 13 同款，简化版）。
func (o *Obs) RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				id = "unknown"
			} else {
				id = hex.EncodeToString(b)
			}
		}
		logger := o.logger.With("request_id", id)
		ctx := context.WithValue(r.Context(), reqIDCtx{}, id)
		ctx = context.WithValue(ctx, loggerCtx{}, logger)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type reqIDCtx struct{}
type loggerCtx struct{}

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(reqIDCtx{}).(string)
	if v == "" {
		return "unknown"
	}
	return v
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtx{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// ---------- 追踪装配（实验 15 同款，sync 导出 + 属性捕获） ----------

// spanRecorder：内存导出器，捕获名字/TraceID/属性供测试断言。
type spanRecorder struct {
	mu    sync.Mutex
	spans []recSpan
}

type recSpan struct {
	Name    string
	TraceID string
	Attrs   map[string]string
}

func (r *spanRecorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range spans {
		attrs := map[string]string{}
		for _, a := range s.Attributes() {
			attrs[string(a.Key)] = a.Value.Emit()
		}
		r.spans = append(r.spans, recSpan{
			Name: s.Name(), TraceID: s.SpanContext().TraceID().String(), Attrs: attrs,
		})
	}
	return nil
}

func (r *spanRecorder) Shutdown(context.Context) error { return nil }

func (r *spanRecorder) collect() []recSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recSpan(nil), r.spans...)
}

// SetupTracing：装配 Provider（sync 导出：即时可断言）。
func SetupTracing(sampling float64, exporter sdktrace.SpanExporter) (*sdktrace.TracerProvider, error) {
	if exporter == nil {
		var err error
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(os.Stdout))
		if err != nil {
			return nil, err
		}
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampling))),
		sdktrace.WithSyncer(exporter),
	), nil
}

// testBuffer：并发安全的日志缓冲（测试注入）。
type testBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func newTestBuffer() *testBuffer { return &testBuffer{} }

func (b *testBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *testBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// RateLimited：三件套同时记录一次限流。
func (o *Obs) RateLimited(r *http.Request, user string, retryAfter time.Duration) {
	// ① 日志：JSON 行——logger 已由 RequestID 预注 request_id，这里不重复写
	loggerFrom(r.Context()).Warn("rate limited",
		"user", user, "path", r.URL.Path,
		"retry_after_ms", retryAfter.Milliseconds())
	// ② 指标：rate_limited_total{user}
	o.counts.mu.Lock()
	if o.counts.v[user] == nil {
		o.counts.v[user] = new(int64)
	}
	*o.counts.v[user]++
	o.counts.mu.Unlock()
	// ③ 追踪：限流事件 span（无 tp 时跳过——noop 安全）
	if o.tp != nil {
		tracer := o.tp.Tracer("marklet/ratelimit")
		_, span := tracer.Start(r.Context(), "rate limit check")
		span.SetAttributes(
			attribute.Bool("ratelimit.exceeded", true),
			attribute.String("ratelimit.user", user),
		)
		span.End()
	}
}
