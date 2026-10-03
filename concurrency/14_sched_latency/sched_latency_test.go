package main

import (
	"testing"
)

// TestQuantileMonotonicAndSamples：空闲窗口至少产生 1000 个
// Runnable→Running 延迟样本，且分位数单调（p50 ≤ p95 ≤ p99，均非负）。
func TestQuantileMonotonicAndSamples(t *testing.T) {
	s := measureWindow(false)
	t.Logf("idle: samples=%d p50=%v p95=%v p99=%v", s.count, dur(s.p50), dur(s.p95), dur(s.p99))
	if s.count < 1000 {
		t.Fatalf("样本 %d < 1000，窗口内迁移量不足", s.count)
	}
	if s.p50 < 0 || s.p50 > s.p95 || s.p95 > s.p99 {
		t.Fatalf("分位数非单调: %v %v %v", s.p50, s.p95, s.p99)
	}
}

// TestSaturatedScenario：满载场景可完成、样本充足、分位数同样单调。
// 满载 p99 应显著高于空闲 p99——这正是排队被放大的证据；
// 但 -race 下两组都被拖慢，比值有波动，只断言数量级不倒挂过分
// （放宽为满载 p99 ≥ 空闲 p50）。
func TestSaturatedScenario(t *testing.T) {
	idle := measureWindow(false)
	sat := measureWindow(true)
	t.Logf("idle: samples=%d p50=%v p99=%v", idle.count, dur(idle.p50), dur(idle.p99))
	t.Logf("saturated: samples=%d p50=%v p99=%v", sat.count, dur(sat.p50), dur(sat.p99))

	if sat.count < 1000 {
		t.Fatalf("满载样本 %d < 1000", sat.count)
	}
	if sat.p50 < 0 || sat.p50 > sat.p95 || sat.p95 > sat.p99 {
		t.Fatalf("满载分位数非单调: %v %v %v", sat.p50, sat.p95, sat.p99)
	}
	if sat.p99 < idle.p50 {
		t.Fatalf("满载 p99(%v) < 空闲 p50(%v)，分布倒挂", dur(sat.p99), dur(idle.p50))
	}
}
