// 项目 16 验收测试：
// ① 双就绪通道 10^6 次 select，两路占比偏差 < 5%；
// ② 朴素两步加锁的 ABBA 互等（selectgo 排序加锁消除的正是它）；
// ③ select+default 忙等与阻塞 select 的 CPU 占用对比。
package main

import (
	"runtime"
	"testing"
	"time"
)

// TestSelectUniformity：双就绪通道 10^6 次，偏差必须 < 5%
// （fastrandn 随机轮询起点下的统计预期是 < 0.2%）。
func TestSelectUniformity(t *testing.T) {
	const n = 1_000_000
	a, b := uniformity(n)
	pa := float64(a) / n
	dev := pa - 0.5
	if dev < 0 {
		dev = -dev
	}
	if dev >= 0.05 {
		t.Fatalf("A 占比 %.4f，偏差 %.2f%% >= 5%%（a=%d b=%d）", pa, dev*100, a, b)
	}
	t.Logf("a=%d b=%d 占比 %.3f%% 偏差 %.3f%%", a, b, pa*100, dev*100)
}

// TestNaiveLockOrderDeadlocks：按书写顺序两步拿两把锁，两方各持其一
// 即互等。持第一把锁后停 1ms 制造 ABBA 窗口，死锁应稳定出现。
func TestNaiveLockOrderDeadlocks(t *testing.T) {
	if deadlocks := naiveLockOrder(3); deadlocks < 1 {
		t.Fatalf("3 轮 ABBA 场景观察到 %d 次死锁，至少应出现 1 次", deadlocks)
	}
}

// TestSelectPairNoDeadlock：同构场景换成 select 交叉收发，10^4 次交接
// 必须全部完成（排序加锁使多路等待原子化）。
func TestSelectPairNoDeadlock(t *testing.T) {
	done := make(chan struct{})
	go func() {
		crossSelect(10_000)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("select 交叉收发对未在 5s 内完成 10^4 次交接，疑似卡死")
	}
}

// TestSelectDefaultBusyCPU：同一墙上时长，忙等的 CPU 消耗远大于阻塞
// 等待。getrusage 采样，阈值放宽以容忍调度抖动与 -race 开销。
func TestSelectDefaultBusyCPU(t *testing.T) {
	procs := runtime.GOMAXPROCS(0)
	if procs < 2 { // 单核下忙等也会被频繁打断，阈值不可靠
		t.Skip("需要多核环境")
	}
	const wall = 150 * time.Millisecond
	busy := busyPollCPU(wall)
	block := blockWaitCPU(wall)
	if busy < wall/2 {
		t.Fatalf("忙等 150ms 只消耗 %v CPU（应接近墙上时长的一半以上）", busy)
	}
	if block > wall/2 {
		t.Fatalf("阻塞等待 150ms 消耗了 %v CPU（应接近 0）", block)
	}
	t.Logf("busy=%v block=%v (wall=%v)", busy, block, wall)
}
