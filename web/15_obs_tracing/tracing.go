package main

// tracing.go：OTel 链路追踪——Provider、HTTP 中间件、DB 子 span、W3C 透传、采样。
//
// trace（一次请求的全链路）由多个 span（一段操作）组成，span 间以
// TraceID（同链路共享）+ SpanID（每段唯一）+ 父子关系连接。
// 跨进程传播靠 W3C traceparent 头：traceparent: 00-<traceid>-<spanid>-<flags>。
//
// 采样：ParentBased(TraceIDRatioBased(0.1))——根 span 以 10% 概率采样；
// 有父 span 时跟随父的采样决定（同一链路要么全采要么全不采，不会断链）。

import (
	"context"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// spanRecorder：内存导出器——测试用它断言 span 层级/采样，替代 stdout。
type spanRecorder struct {
	mu    sync.Mutex
	spans []spanRecord
}

type spanRecord struct {
	Name       string
	TraceID    string
	SpanID     string
	ParentSpan string // 无父为空
}

func (r *spanRecorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range spans {
		parent := ""
		if s.Parent().SpanID().IsValid() {
			parent = s.Parent().SpanID().String()
		}
		r.spans = append(r.spans, spanRecord{
			Name:       s.Name(),
			TraceID:    s.SpanContext().TraceID().String(),
			SpanID:     s.SpanContext().SpanID().String(),
			ParentSpan: parent,
		})
	}
	return nil
}

func (r *spanRecorder) Shutdown(context.Context) error { return nil }
func (r *spanRecorder) collect() []spanRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]spanRecord(nil), r.spans...)
}

// SetupTracing：装配 Provider。exporter 传 nil 时用 stdout（演示模式）。
func SetupTracing(sampling float64, exporter sdktrace.SpanExporter) (*sdktrace.TracerProvider, error) {
	if exporter == nil {
		out, err := newStdoutExporter()
		if err != nil {
			return nil, err
		}
		exporter = out
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampling))),
		sdktrace.WithSyncer(exporter), // 同步导出：演示逐条可见，测试即时可断言
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL, semconv.ServiceName("marklet"),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{}) // W3C traceparent
	return tp, nil
}

// TracingMiddleware：从请求提取 traceparent（有则延续同一 trace），开 HTTP 父 span。
func TracingMiddleware(tp *sdktrace.TracerProvider) func(http.Handler) http.Handler {
	tracer := tp.Tracer("marklet/http")
	prop := propagation.TraceContext{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path)
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DBQuerySpan：包住一次 DB 操作的子 span（父 = 传入 ctx 的当前 span）。
func DBQuerySpan(ctx context.Context, tp *sdktrace.TracerProvider, query string) (context.Context, func()) {
	tracer := tp.Tracer("marklet/db")
	ctx, span := tracer.Start(ctx, "db.query")
	span.SetAttributes(attribute.String("db.statement", query))
	return ctx, func() { span.End() }
}

// TraceIDFrom：取当前 span 的 TraceID（演示与测试用）。
func TraceIDFrom(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// BuildTraceparent：按 W3C 格式构造 traceparent（测试模拟上游服务用）。
func BuildTraceparent(traceID, spanID string) string {
	return "00-" + traceID + "-" + spanID + "-01"
}
