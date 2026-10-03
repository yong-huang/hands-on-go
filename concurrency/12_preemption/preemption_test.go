package main

import (
	"runtime"
	"sort"
	"testing"
)

// TestSignalPreemptionKeepsSchedulerAlive：tight loop 期间后台 G 的首次执行
// 延迟应有界（信号抢占生效）。取两种循环各 5 轮的中位数，宽松阈值 50ms，
// 避免把 CI 抖动误报为抢占失效；README 记录的是真实典型值（约 10~20ms）。
func TestSignalPreemptionKeepsSchedulerAlive(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)

	for name, loop := range map[string]func(){"tight": tightLoop, "call": callLoop} {
		delays := make([]int, 0, 5)
		for i := 0; i < 5; i++ {
			d := measureOnce(loop)
			delays = append(delays, int(d.Milliseconds()))
			t.Logf("%s round %d: background first-run delay = %dms", name, i+1, delays[i])
		}
		sort.Ints(delays)
		if med := delays[2]; med >= 50 {
			t.Fatalf("%s: 首跑延迟中位数 %dms ≥ 50ms，信号抢占疑似未生效", name, med)
		}
	}
}

// TestTightLoopRuns：tight loop 本体可完成且结果非零（未被编译器优化掉）。
func TestTightLoopRuns(t *testing.T) {
	tightLoop()
	if sink == 0 {
		t.Fatal("sink=0：tight loop 疑似被优化掉")
	}
}
