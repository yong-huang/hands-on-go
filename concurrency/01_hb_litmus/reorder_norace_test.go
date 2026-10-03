//go:build !race

// 无同步 litmus 的"重排确实可被观测"证据，只在非 -race 构建下参与编译：
// -race 会给每次内存访问插桩并按 vector clock 判定，插桩后的访问行为
// 串行化，0 异常不能说明任何问题（且无同步版本在 -race 下会直接报
// DATA RACE——那是 go run -race main.go 的演示职责，见验收①）。
package main

import "testing"

// TestUnsyncLitmusObservesReorder：断言不带任何同步时重排真实可观测。
// 本机（Apple Silicon，P 核近 TSO）异常率很低（每百万次读检查个位数），
// 因此采用"多轮重试、命中即停"的跑法；全部轮次跑完仍为 0 才判失败。
func TestUnsyncLitmusObservesReorder(t *testing.T) {
	const iters = 4_000_000
	const attempts = 6 // 最多 6×4M 次读检查，仍无一次重排才失败
	total := 0
	for i := 0; i < attempts; i++ {
		got := mpUnsync(iters)
		total += int(got)
		if got > 0 {
			t.Logf("第 %d 轮观测到 %d 次重排（累计 %d）", i+1, got, total)
			return
		}
	}
	t.Fatalf("%d 次读检查未观测到任何重排，演示环境异常（核数不足或被限流）", attempts*iters)
}
