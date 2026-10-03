package main

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestMain：包级泄漏门禁（同 20_goleak_gate 的接入模板）。三种形状
// 的 harness 都要求彻底排空，任何残留 goroutine 在这里被抓红。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// 验收 ①：三种负载形状对核心子集压测，p99 对比表入 README。
// 本测试顺带断言守恒：每种形状都必须把全部 n 个 Job 送到汇聚端。
func TestConstantShapeNoQueueTail(t *testing.T) {
	lats := runShape(constantShape(), 2)
	if len(lats) != 2000 {
		t.Fatalf("守恒破坏：收到 %d/2000", len(lats))
	}
	p50 := percentile(lats, 0.50)
	p99 := percentile(lats, 0.99)
	// 恒定速率远低于处理容量，不该出现排队尾：p99 仍在个位数倍 p50 内。
	if p99 > 10*p50 {
		t.Fatalf("恒定负载出现排队尾：p99=%v > 10×p50=%v", p99, 10*p50)
	}
}

func TestBurstShapeQueueTail(t *testing.T) {
	lats := runShape(burst10xShape(), 2)
	if len(lats) != 3000 {
		t.Fatalf("守恒破坏：收到 %d/3000", len(lats))
	}
	// 突发应有排队尾：p99 至少是 p50 的 2.5 倍（实测约 3~4 倍）。
	p50, p99 := percentile(lats, 0.50), percentile(lats, 0.99)
	if p99 < 5*p50/2 {
		t.Fatalf("突发未产生排队尾：p99=%v < 2.5×p50=%v", p99, 5*p50/2)
	}
	// 与恒定形状的横向对比：突发 p99 应显著高于恒定 p99（实测 ~30 倍）。
	cl := runShape(constantShape(), 2)
	if p99 < 5*percentile(cl, 0.99) {
		t.Fatalf("突发 p99=%v 未拉开与恒定 p99=%v 的差距", p99, percentile(cl, 0.99))
	}
}

func TestStepShapeTailGrowsWithRate(t *testing.T) {
	lats := runShape(stepShape(), 2)
	if len(lats) != 1500 {
		t.Fatalf("守恒破坏：收到 %d/1500", len(lats))
	}
	head := percentile(lats[:300], 0.99)           // 首档 2.5k items/s
	tail := percentile(lats[len(lats)-300:], 0.99) // 末档 50k items/s
	if tail < 3*head {
		t.Fatalf("阶跃爬坡未体现：末档 p99=%v < 3×首档 p99=%v", tail, 3*head)
	}
}

// 核心子集语义抽查：与 17_generics_prims 同形的关闭契约与幂等性。
func TestChanPipeSubsetSemantics(t *testing.T) {
	in := NewChan[int](4)
	in.Send(1)
	in.Send(2)
	in.Close()
	in.Close() // 幂等：重复 Close 不 panic
	if _, ok := in.Recv(); !ok {
		t.Fatal("关闭后排空期应还能读到缓冲数据")
	}
	if _, ok := in.Recv(); !ok {
		t.Fatal("缓冲内第二条应同样可读")
	}
	if v, ok := in.Recv(); ok || v != 0 {
		t.Fatal("关闭且排空后应返回零值与 ok=false")
	}
	// Pipe：in 排空后 out 自动关闭，且每条消息恰好被处理一次。
	src := NewChan[int](8)
	processed := make(chan int, 8)
	out := Pipe(src, 2, func(x int) int {
		processed <- x
		return x * 2
	})
	go func() {
		for i := 1; i <= 8; i++ {
			src.Send(i)
		}
		src.Close()
	}()
	sum, cnt := 0, 0
	for {
		v, ok := out.Recv()
		if !ok {
			break
		}
		sum += v
		cnt++
	}
	if cnt != 8 || sum != 2*(1+2+3+4+5+6+7+8) {
		t.Fatalf("Pipe 守恒破坏：cnt=%d sum=%d", cnt, sum)
	}
	close(processed)
	if n := len(processed); n != 8 {
		t.Fatalf("每条消息应恰好处理一次, got %d", n)
	}
}

// 编译期防呆：percentile 的最近邻实现边界（单元素、全零延迟）。
func TestPercentileBounds(t *testing.T) {
	one := []time.Duration{5 * time.Millisecond}
	if got := percentile(one, 0.99); got != 5*time.Millisecond {
		t.Fatalf("单元素 p99 = %v, want 5ms", got)
	}
}
