package main

import (
	"testing"
)

// runForTest：固定种子跑一个场景，回调里顺带收集交付序号。
func runForTest(t *testing.T, total int, loss float64, seed int64) ScenarioResult {
	t.Helper()
	r, err := runScenario(total, loss, seed)
	if err != nil {
		t.Fatalf("场景失败: %v", err)
	}
	return r
}

// assertExactlyOnce：验收①②合并断言——交付序号恰为 0..n-1（零丢失、
// 零重复交付），应用层交付计数恰等于消息数。
func assertExactlyOnce(t *testing.T, r ScenarioResult, total int) {
	t.Helper()
	if r.AppDelivered != int64(total) {
		t.Fatalf("应用层交付 %d 条, want %d", r.AppDelivered, total)
	}
	for i, seq := range r.Seqs {
		if seq != int32(i) {
			t.Fatalf("第 %d 个交付的序号 = %d, want %d（零丢失 ⇒ 序号连续）", i, seq, i)
		}
	}
}

// TestStopWaitThroughLoss：验收①③——30% 双向丢包下 200 条消息零丢失
// 零重复交付，且必然发生超时重传（次数实录，验收③由 README 引用此数）。
func TestStopWaitThroughLoss(t *testing.T) {
	r := runForTest(t, 200, 0.30, timeSeed(1))
	assertExactlyOnce(t, r, 200)
	t.Logf("总发送 %d 次，超时重传 %d 次，去重 %d 条", r.Tx, r.Retrans, r.Dups)
	if r.Tx <= 200 {
		t.Fatalf("30%% 双向丢包下总发送 %d 次竟无重传，链路/统计必有假", r.Tx)
	}
	if r.Dups < 0 {
		t.Fatalf("Dups = %d", r.Dups)
	}
}

// TestZeroLossNoRetrans：对照组——无损链路上停等协议零重传、零重复，
// 200 条恰好发送 200 次。
func TestZeroLossNoRetrans(t *testing.T) {
	r := runForTest(t, 200, 0, timeSeed(2))
	assertExactlyOnce(t, r, 200)
	if r.Retrans != 0 {
		t.Fatalf("无损链路重传 %d 次, want 0", r.Retrans)
	}
	if r.Dups != 0 {
		t.Fatalf("无损链路收到重复 DATA %d 条, want 0", r.Dups)
	}
}

// TestDedupUnderHeavyLoss：验收②的重压版——50% 双向丢包下 ACK 极易丢，
// 重复 DATA 必然出现；去重机制保证应用层仍然恰一次交付。
func TestDedupUnderHeavyLoss(t *testing.T) {
	r := runForTest(t, 50, 0.50, timeSeed(3))
	assertExactlyOnce(t, r, 50)
	t.Logf("50%% 丢包下：总发送 %d 次，去重 %d 条重复 DATA", r.Tx, r.Dups)
	if r.Dups == 0 {
		t.Fatal("50% 双向丢包下竟无一重复 DATA，去重路径未被覆盖")
	}
}

// timeSeed：每个测试用不同种子，互不干扰且可复现。
func timeSeed(n int64) int64 { return n * 1000 }
