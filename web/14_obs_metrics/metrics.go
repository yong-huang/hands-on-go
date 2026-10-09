package main

// metrics.go：手写 Prometheus 文本格式 + 请求延迟直方图 + 分位数估计。
//
// RED 三件套：Rate（请求数 counter）、Errors（按 code 计数）、Duration（直方图）。
// 文本格式（exposition format）的四种行：
//
//	# HELP <名> <说明> / # TYPE <名> <类型>
//	<名>{label="值",...} <数值>
//	直方图额外输出 <名>_bucket{le="上界"}、<名>_sum、<名>_count
//
// 分位数从累计桶线性插值估计（Prometheus 同款算法）——桶越密越准，
// 桶的选择本身就是一次"我关心哪个量级"的表态。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// buckets：延迟分桶（秒）。上界覆盖 marklet 的真实量级（本地 <5ms，慢路径 >500ms）。
var buckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

// Metrics：并发安全的指标仓。真实生产会用 prometheus/client_golang——
// 手写一遍是为了讲清文本格式与直方图的数据结构。
type Metrics struct {
	mu        sync.Mutex
	counts    map[string]*uint64 // "path|code" -> 次数
	countsSum uint64
	observed  []float64 // 直方图样本（真实生产是流式累计，这里留样本做分位对照）
}

func NewMetrics() *Metrics { return &Metrics{counts: map[string]*uint64{}} }

// ObserveRequest：中间件记录一次请求（计数 + 延迟）。
func (m *Metrics) ObserveRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.record(r.URL.Path, rec.code, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) { s.code = code; s.ResponseWriter.WriteHeader(code) }

func (m *Metrics) record(path string, code int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := path + "|" + strconv.Itoa(code)
	if m.counts[k] == nil {
		u := new(uint64)
		m.counts[k] = u
	}
	*m.counts[k]++
	m.countsSum++
	m.observed = append(m.observed, d.Seconds())
}

// Quantile：分位数估计（Prometheus 算法）：找样本所在桶，
// 在桶内按"该桶已累计的排名占比"线性插值。q∈(0,1]。
// 桶 granularity 的误差天然存在——这是"分位数与实测一致（对照组）"实验的意义。
func (m *Metrics) Quantile(q float64) float64 {
	m.mu.Lock()
	sorted := append([]float64(nil), m.observed...)
	m.mu.Unlock()
	if len(sorted) == 0 {
		return 0
	}
	sort.Float64s(sorted)
	count := float64(len(sorted))
	rank := q * count
	var cumulative float64
	for i, b := range buckets {
		inBucket := 0.0
		for _, v := range sorted {
			if v <= b {
				inBucket++
			} else {
				break
			}
		}
		newCumulative := inBucket
		if newCumulative >= rank {
			// 桶内线性插值：假设样本在 [上一下界, 本上界] 均匀分布
			lower := 0.0
			if i > 0 {
				lower = buckets[i-1]
			}
			fraction := (rank - cumulative) / (newCumulative - cumulative)
			return lower + fraction*(b-lower)
		}
		cumulative = newCumulative
	}
	return sorted[len(sorted)-1]
}

// Render：输出 exposition 文本（Content-Type 由调用方设置）。
func (m *Metrics) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintln(&b, "# HELP http_requests_total Total HTTP requests.")
	fmt.Fprintln(&b, "# TYPE http_requests_total counter")
	keys := make([]string, 0, len(m.counts))
	for k := range m.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.SplitN(k, "|", 2)
		fmt.Fprintf(&b, "http_requests_total{path=%q,code=%q} %d\n",
			parts[0], parts[1], *m.counts[k])
	}
	fmt.Fprintln(&b, "# HELP http_request_duration_seconds Request latency.")
	fmt.Fprintln(&b, "# TYPE http_request_duration_seconds histogram")
	for _, b2 := range buckets {
		inBucket := 0.0
		for _, v := range m.observed {
			if v <= b2 {
				inBucket++
			}
		}
		fmt.Fprintf(&b, "http_request_duration_seconds_bucket{le=%q} %d\n",
			formatLE(b2), int64(inBucket))
	}
	fmt.Fprintf(&b, "http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", len(m.observed))
	fmt.Fprintf(&b, "http_request_duration_seconds_sum %f\n", sum(m.observed))
	fmt.Fprintf(&b, "http_request_duration_seconds_count %d\n", len(m.observed))
	return b.String()
}

func formatLE(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func sum(vs []float64) float64 {
	var s float64
	for _, v := range vs {
		s += v
	}
	return s
}
