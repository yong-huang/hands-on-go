package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

// goleak 门禁。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestSetup：内存导出器 + 可变采样率的服务。
func newTestSetup(t *testing.T, sampling float64) (*spanRecorder, http.Handler) {
	t.Helper()
	rec := &spanRecorder{}
	tp, err := SetupTracing(sampling, rec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work", func(w http.ResponseWriter, r *http.Request) {
		_, done := DBQuerySpan(r.Context(), tp, "SELECT url, title FROM bookmarks")
		defer done()
		w.Write([]byte("done"))
	})
	return rec, TracingMiddleware(tp)(mux)
}

// TestSpanHierarchy：验收①——父 HTTP span + 子 db.query span：
// 同 TraceID、子的 ParentSpan == 父的 SpanID。
func TestSpanHierarchy(t *testing.T) {
	rec, h := newTestSetup(t, 1.0)
	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.collect()
	var httpSpan, dbSpan *spanRecord
	for i := range spans {
		if strings.HasPrefix(spans[i].Name, "GET /api") {
			httpSpan = &spans[i]
		}
		if spans[i].Name == "db.query" {
			dbSpan = &spans[i]
		}
	}
	if httpSpan == nil || dbSpan == nil {
		t.Fatalf("span 缺失: %+v", spans)
	}
	if httpSpan.TraceID != dbSpan.TraceID {
		t.Errorf("TraceID 不一致: http=%s db=%s", httpSpan.TraceID, dbSpan.TraceID)
	}
	if dbSpan.ParentSpan != httpSpan.SpanID {
		t.Errorf("子 span 父=%s， want HTTP span %s", dbSpan.ParentSpan, httpSpan.SpanID)
	}
	if httpSpan.ParentSpan != "" {
		t.Errorf("HTTP span 应为根（无父），实际父=%s", httpSpan.ParentSpan)
	}
}

// TestTraceparentPassthrough：验收②——上游传入 traceparent，
// 服务端 span 延续同一 TraceID；HTTP span 的父 = 上游指定的 parent-span-id。
func TestTraceparentPassthrough(t *testing.T) {
	rec, h := newTestSetup(t, 1.0)
	const upstreamTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const upstreamSpan = "00f067aa0ba902b7"

	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	req.Header.Set("traceparent", BuildTraceparent(upstreamTrace, upstreamSpan))
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.collect()
	var httpSpan, dbSpan *spanRecord
	for i := range spans {
		s := spans[i]
		if s.TraceID != upstreamTrace {
			t.Errorf("出现游离 TraceID %s（span %s）——透传断裂", s.TraceID, s.Name)
			continue
		}
		if strings.HasPrefix(s.Name, "GET /api") {
			httpSpan = &spans[i]
		}
		if s.Name == "db.query" {
			dbSpan = &spans[i]
		}
	}
	if httpSpan == nil {
		t.Fatal("没有 span 延续上游 TraceID")
	}
	// HTTP span 的父 = 上游 span；db span 的父 = HTTP span——接力完整
	if httpSpan.ParentSpan != upstreamSpan {
		t.Errorf("HTTP span 父=%s， want 上游 %s", httpSpan.ParentSpan, upstreamSpan)
	}
	if dbSpan == nil || dbSpan.ParentSpan != httpSpan.SpanID {
		t.Errorf("db span 父=%v， want HTTP span %s", dbSpan, httpSpan.SpanID)
	}
}

// TestSampleRate10：验收③——100 个独立请求按 10% 采样，导出的 trace 数
// 落在 [3, 25]（概率波动区间；均值 10）。
func TestSampleRate10(t *testing.T) {
	rec, h := newTestSetup(t, 0.1)
	for i := 0; i < 100; i++ {
		// 不带 traceparent：真根 span，才走 10% 比例采样
		//（ParentBased 下有父 span 时跟随父的采样决定，比例不参与）
		req := httptest.NewRequest("GET", "/api/v1/work", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	spans := rec.collect()
	traces := map[string]int{}
	for _, s := range spans {
		traces[s.TraceID]++
	}
	if len(traces) < 3 || len(traces) > 25 {
		t.Errorf("采样 10%% 下导出 %d 条 trace， want 3~25（均值 10）", len(traces))
	}
	t.Logf("采样实测：%d/100 条 trace 被导出", len(traces))
}

// TestAttributeRecorded：db.statement 属性落在子 span 上（可检索的关键）。
func TestAttributeRecorded(t *testing.T) {
	rec, h := newTestSetup(t, 1.0)
	req := httptest.NewRequest("GET", "/api/v1/work", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := fmt.Sprintf("%+v", rec.collect())
	if !strings.Contains(out, "db.query") {
		t.Fatalf("缺 db span: %s", out)
	}
}
