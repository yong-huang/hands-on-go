package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// goleak 门禁（卡死任务用例在结束时松手，保证清零）。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestFiftyJobsZeroLoss：验收①——50 个在途任务停机后全部完成或进死信，
// 本路径下全部完成（零丢失、零死信）。
func TestFiftyJobsZeroLoss(t *testing.T) {
	phaseLog := NewPhaseLog()
	var done atomic.Int64
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(context.Context) error { return nil }),
		Ready:     readyTrue(),
		Log:       phaseLog,
		DrainTime: 5 * time.Second,
	}
	orch.Pool = NewDrainablePool(4, 64, func(Job) {
		time.Sleep(20 * time.Millisecond)
		done.Add(1)
	})
	for i := 0; i < 50; i++ {
		if err := orch.Pool.Enqueue(Job{URL: "t"}); err != nil {
			t.Fatalf("入队: %v", err)
		}
	}

	if drained := orch.Shutdown(context.Background()); !drained {
		t.Fatal("排空超时（5s 内 50 个 20ms 任务应能做完）")
	}
	if got := done.Load(); got != 50 {
		t.Errorf("完成 %d， want 50（任务丢失）", got)
	}
	if dead := len(orch.Pool.DeadLetters()); dead != 0 {
		t.Errorf("死信 %d， want 0", dead)
	}
	if !strings.Contains(phaseLog.All()[0], "readyz 已摘除") {
		t.Errorf("第一日志行应是摘流量: %v", phaseLog.All())
	}
}

// TestShutdownPhaseOrder：验收③——四阶段按序可审计。
func TestShutdownPhaseOrder(t *testing.T) {
	phaseLog := NewPhaseLog()
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(context.Context) error { return nil }),
		Ready:     readyTrue(),
		Log:       phaseLog,
		DrainTime: time.Second,
	}
	orch.Pool = NewDrainablePool(1, 8, func(Job) {})
	orch.Shutdown(context.Background())

	all := phaseLog.All()
	if len(all) != 5 {
		t.Fatalf("日志 %d 行， want 5: %v", len(all), all)
	}
	// 时间戳单调不减 + 阶段编号 ①→④ 严格递增
	for i := 1; i < len(all); i++ {
		if tsOf(all[i]) < tsOf(all[i-1]) {
			t.Errorf("时序回退: %q 早于 %q", all[i], all[i-1])
		}
	}
	for i, want := range []string{"①", "②", "③", "③", "④"} {
		if !strings.Contains(all[i], want) {
			t.Errorf("第 %d 行缺阶段 %s: %q", i+1, want, all[i])
		}
	}
	// 摘流量必须是第一步（LB 停派发先于一切）
	if !strings.Contains(all[0], "readyz") {
		t.Error("停机第一步必须是摘 readyz")
	}
}

func tsOf(line string) int64 {
	var ms int64
	fmt.Sscanf(line, "[%dms]", &ms)
	return ms
}

// TestDrainTimeoutDeadLetters：验收②——任务卡死时排空超时，
// 队列剩余任务进死信（带原因），强制路径有日志，worker 手里的不重复计。
func TestDrainTimeoutDeadLetters(t *testing.T) {
	phaseLog := NewPhaseLog()
	block := make(chan struct{})
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(context.Context) error { return nil }),
		Ready:     readyTrue(),
		Log:       phaseLog,
		DrainTime: 200 * time.Millisecond,
	}
	orch.Pool = NewDrainablePool(1, 64, func(Job) {
		<-block // 卡死
	})
	for i := 0; i < 5; i++ {
		orch.Pool.Enqueue(Job{URL: "stuck"})
	}
	time.Sleep(30 * time.Millisecond) // 第 1 个进入 worker（卡死）

	if drained := orch.Shutdown(context.Background()); drained {
		t.Fatal("超时路径不应报告成功")
	}
	dead := orch.Pool.DeadLetters()
	if len(dead) != 4 {
		t.Fatalf("死信 %d 条， want 4（队列存量，卡死的那个不重复计）", len(dead))
	}
	for _, d := range dead {
		if d.Err != "drain timeout" {
			t.Errorf("死信原因 = %q， want drain timeout", d.Err)
		}
	}
	if !strings.Contains(strings.Join(phaseLog.All(), "\n"), "排空超时") {
		t.Error("超时路径缺强制退出日志")
	}
	close(block) // 松手清零（goleak）
	orch.Pool.wg.Wait()
}

// TestEnqueueAfterClose：停机后不再接单。
func TestEnqueueAfterClose(t *testing.T) {
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(context.Context) error { return nil }),
		Ready:     readyTrue(),
		Log:       NewPhaseLog(),
		DrainTime: time.Second,
	}
	orch.Pool = NewDrainablePool(1, 8, func(Job) {})
	orch.Shutdown(context.Background())
	if err := orch.Pool.Enqueue(Job{URL: "late"}); err == nil {
		t.Error("停机后入队应返回错误")
	}
}
