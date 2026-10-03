package main

import (
	"runtime"
	"testing"
	"time"
)

// TestRunnextExperimentRuns：跑 runnext 实验本体，断言结构（每轮 A、B 恰好各完成一次）。
// 完成顺序是调度器与时机的函数：无 race 的安静运行里后 spawn 的 B 稳定插队，
// 但 -race 下 GC/唤醒事件会抢占 runnext 槽位，顺序不可强断言（README 有实测对比）。
func TestRunnextExperimentRuns(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)

	seqs := expRunnextSeqs(5)
	if len(seqs) != 5 {
		t.Fatalf("实验轮数=%d，期望 5", len(seqs))
	}
	for i, s := range seqs {
		if len(s) != 2 || (s != "AB" && s != "BA") {
			t.Fatalf("trial %d: 非法序列 %q", i, s)
		}
	}
	t.Logf("runnext 实验序列（-race 下不保证 BA）: %v", seqs)
}

// TestBatchCompletesAll：三种 GOMAXPROCS 档位下，批量任务全部完成且编号完整。
// 完成顺序是调度器决定的，只断言集合完整性，不断言顺序。
func TestBatchCompletesAll(t *testing.T) {
	for _, procs := range []int{1, 2, 8} {
		old := runtime.GOMAXPROCS(procs)
		reset()
		runBatch(40, 0)
		got := snapshot()
		runtime.GOMAXPROCS(old)

		seen := make(map[int]bool, len(got))
		for _, id := range got {
			seen[id] = true
		}
		if len(got) != 40 || len(seen) != 40 {
			t.Fatalf("procs=%d: 完成 %d 个、去重 %d 个，期望 40", procs, len(got), len(seen))
		}
		for id := 0; id < 40; id++ {
			if !seen[id] {
				t.Fatalf("procs=%d: 编号 %d 未完成", procs, id)
			}
		}
	}
}

// TestQueueOverflowCompletes：600 个任务越过本地队列 256 上限后仍应全部完成。
func TestQueueOverflowCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过溢出实验")
	}
	old := runtime.GOMAXPROCS(1)
	reset()
	done := make(chan struct{})
	go func() {
		runBatch(600, 0) // 测试里不自旋，只验证排空语义
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("600 任务 30s 未排空")
	}
	runtime.GOMAXPROCS(old)

	got := snapshot()
	if len(got) != 600 {
		t.Fatalf("完成 %d 个，期望 600", len(got))
	}
	seen := make(map[int]bool)
	for _, id := range got {
		if seen[id] {
			t.Fatalf("编号 %d 重复完成", id)
		}
		seen[id] = true
	}
}
