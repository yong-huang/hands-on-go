package main

import (
	"context"
	"os"
	"runtime"
	"testing"

	"go.uber.org/goleak"
)

// TestMain：包级泄漏门禁。所有测试跑完后（且全部通过时）做一次
// goroutine 快照比对，任何测试漏收的 goroutine 都在这里被抓红。
// 这三行就是接入其他库（15/17/18/19）所需的全部改动，模板见 README。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// 常规门禁下的绿灯用例：worker 经 ctx 收工，计数回落基线。
func TestWorkerStopsCleanly(t *testing.T) {
	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	StartWorker(ctx)
	waitGrew(base)
	cancel()
	waitBack(base)
	if n := runtime.NumGoroutine(); n > base {
		t.Fatalf("worker 未退出：%d > 基线 %d", n, base)
	}
}

// 抓红演示：快照 diff 的现场还原。默认跳过以保持门禁绿；
// GOLEAK_RED=1 时先验证基线干净，再故意泄漏，第二次校验应抓红。
// 两次 VerifyNone 的差值正是"快照 diff"原理的直接演示。
func TestVerifyNoneCatchesLeak(t *testing.T) {
	if os.Getenv("GOLEAK_RED") != "1" {
		t.Skip("抓红演示需 GOLEAK_RED=1；命令见 README 的 Quick Start")
	}
	goleak.VerifyNone(t) // 基线：此刻干净
	StartLeakyWorker()   // 制造泄漏：goroutine 永久阻塞在 leakyWorker
	goleak.VerifyNone(t) // 应抓红：快照里多出一个无人认领的 goroutine
}

// backgroundWorker：模拟第三方库的合法常驻 goroutine（如连接池的空闲
// 读循环）。生命周期与进程同长、功能无害——但 goleak 只认调用栈，
// 不认意图，这正是误报（false positive）的来源。
func backgroundWorker(stop <-chan struct{}) {
	<-stop
}

// 误报演示：无害的常驻 worker 被当成泄漏；用 IgnoreAnyFunction 豁免
// 后转绿。生产代码里豁免是最后手段，优先收编 goroutine 的退出权。
func TestFalsePositiveAndIgnore(t *testing.T) {
	if os.Getenv("GOLEAK_RED") != "1" {
		t.Skip("误报演示需 GOLEAK_RED=1；命令见 README 的 Quick Start")
	}
	stop := make(chan struct{})
	go backgroundWorker(stop)
	defer close(stop)

	err := goleak.Find() // 误报现场：backgroundWorker 被判为泄漏
	if err == nil {
		t.Fatal("演示前提失败：应抓到 backgroundWorker")
	}
	t.Logf("误报现场：\n%v", err)
	err = goleak.Find(
		goleak.IgnoreAnyFunction("concurrency/20_goleak_gate.backgroundWorker"),
	)
	if err != nil {
		t.Fatalf("IgnoreAnyFunction 豁免后应转绿: %v", err)
	}
}
