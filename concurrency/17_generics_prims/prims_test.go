// 项目 17 验收测试：四组件在 -race 高压下行为正确。
// ① 组件库全绿（本文件）；② 三级流水线组装见 main.go 与
// TestPipelineEndToEnd；③ API 文档与"何时不用"见 README。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestPipeMapSum：Pipe 是映射——数量与聚合值都守恒（workers>1 时不保序）。
func TestPipeMapSum(t *testing.T) {
	in := NewChan[int](0)
	out := Pipe(in, 3, func(x int) int { return x * x })
	go func() {
		for i := 1; i <= 1000; i++ {
			in.Send(i)
		}
		in.Close()
	}()
	var sum, cnt int64
	for {
		v, ok := out.Recv()
		if !ok {
			break
		}
		sum += int64(v)
		cnt++
	}
	if cnt != 1000 {
		t.Fatalf("收到 %d 个, want 1000", cnt)
	}
	if want := int64(1000 * 1001 * 2001 / 6); sum != want { // Σx²
		t.Fatalf("Σx² = %d, want %d", sum, want)
	}
}

// TestPipeWorkers1PreservesOrder：单 worker 时输出顺序 = 输入顺序。
func TestPipeWorkers1PreservesOrder(t *testing.T) {
	in := NewChan[int](0)
	out := Pipe(in, 1, func(x int) int { return x })
	go func() {
		for i := 0; i < 100; i++ {
			in.Send(i)
		}
		in.Close()
	}()
	for i := 0; i < 100; i++ {
		v, ok := out.Recv()
		if !ok || v != i {
			t.Fatalf("第 %d 个收到 (%v,%v)", i, v, ok)
		}
	}
}

// TestMergeTotalConserved：扇入总量守恒，来源顺序不保证。
func TestMergeTotalConserved(t *testing.T) {
	ins := make([]*Chan[int], 4)
	for i := range ins {
		ins[i] = NewChan[int](0)
	}
	out := Merge(ins...)
	for i, in := range ins {
		go func(in *Chan[int], base int) {
			for j := 0; j < 250; j++ {
				in.Send(1)
			}
			in.Close()
		}(in, i)
	}
	var cnt int64
	for {
		_, ok := out.Recv()
		if !ok {
			break
		}
		cnt++
	}
	if cnt != 1000 {
		t.Fatalf("扇入收到 %d, want 1000", cnt)
	}
}

// TestSplitRoundRobin：轮转分发是确定性的——第 i 个元素进分支 i%n。
// 各分支必须并发消费：轮转是严格交替，某分支不消费整个分发就停摆
// （见 TestSplitHeadOfLineBlocking）。
func TestSplitRoundRobin(t *testing.T) {
	in := NewChan[int](0)
	outs := Split(in, 2)
	go func() {
		for i := 0; i < 10; i++ {
			in.Send(i)
		}
		in.Close()
	}()
	got := make([][]int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for b, out := range outs {
		go func(b int, out *Chan[int]) {
			defer wg.Done()
			for {
				v, ok := out.Recv()
				if !ok {
					return
				}
				got[b] = append(got[b], v)
			}
		}(b, out)
	}
	waitTimeout(t, &wg, 2*time.Second)
	want := [][]int{{0, 2, 4, 6, 8}, {1, 3, 5, 7, 9}}
	for b := range outs {
		if len(got[b]) != len(want[b]) {
			t.Fatalf("分支 %d = %v, want %v", b, got[b], want[b])
		}
		for i := range want[b] {
			if got[b][i] != want[b][i] {
				t.Fatalf("分支 %d = %v, want %v", b, got[b], want[b])
			}
		}
	}
}

// TestSplitHeadOfLineBlocking：只消费分支 0 时，分支 1 的未交付元素会
// 卡住整个分发——第二轮应停在分支 0（第 2 个元素进不来）。这是轮转
// 扇出的固有语义，不是缺陷；每个分支都必须有人消费。
func TestSplitHeadOfLineBlocking(t *testing.T) {
	in := NewChan[int](0)
	outs := Split(in, 2)
	go func() {
		for i := 0; i < 10; i++ {
			in.Send(i)
		}
		in.Close()
	}()
	if v, ok := outs[0].Recv(); !ok || v != 0 {
		t.Fatalf("第一个元素 = (%v,%v), want (0,true)", v, ok)
	}
	select { // 分发卡在"给分支 1 送 1"，分支 0 的 2 永远来不了
	case v := <-recvAsync(outs[0]):
		t.Fatalf("分支 0 意外收到 %v：轮转不应跳过阻塞分支", v)
	case <-time.After(100 * time.Millisecond):
	}
}

// recvAsync：在独立 goroutine 里做一次 Recv，结果回传（可超时放弃）。
func recvAsync(c *Chan[int]) <-chan int {
	out := make(chan int, 1)
	go func() {
		v, _ := c.Recv()
		out <- v
	}()
	return out
}

// TestCloseSemantics：Close 幂等；向已关闭通道 Send 与内置一致地 panic
// （库的关闭契约：联结所有发送方后再 Close，见包注释）。
func TestCloseSemantics(t *testing.T) {
	c := NewChan[int](0)
	c.Close()
	c.Close() // 幂等
	defer func() {
		// 内置的 panic 值是 runtime.plainError，不是 string，先转文本再比
		if r := fmt.Sprint(recover()); r != "send on closed channel" {
			t.Fatalf("panic = %v, want send on closed channel", r)
		}
	}()
	c.Send(1)
	t.Error("向已关闭通道的 Send 应当 panic")
}

// TestCloseAfterAllSenders：关闭契约——wg 联结所有发送方之后再 Close。
// 让"先发完者先 Close"是错的：先完成者会关掉还在发送的通道。消费到
// 排空，总量守恒。
func TestCloseAfterAllSenders(t *testing.T) {
	c := NewChan[int](4)
	const (
		gs   = 4
		perG = 250
	)
	var wg sync.WaitGroup
	wg.Add(gs)
	for g := 0; g < gs; g++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c.Send(1)
			}
		}()
	}
	go func() { // 联结：所有发送方结束才关闭（Close 幂等）
		wg.Wait()
		c.Close()
	}()
	var recv int64
	done := make(chan struct{})
	go func() { // 一直消费到 Close 且排空（ok=false）才退出
		defer close(done)
		for {
			if _, ok := c.Recv(); !ok {
				return
			}
			recv++
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("消费者未在 Close 后排空退出")
	}
	if recv != gs*perG {
		t.Fatalf("接收 %d, want %d", recv, gs*perG)
	}
}

// TestPipelineEndToEnd：三级流水线（变换 → 扇出 → 扇入）端到端守恒，
// 期望值可精确计算（与 main.go 第 3 步同构）。
func TestPipelineEndToEnd(t *testing.T) {
	src := NewChan[int](0)
	sq := Pipe(src, 2, func(x int) int64 { return int64(x) * int64(x) })
	parts := Split(sq, 3)
	merged := Merge(
		Pipe(parts[0], 2, func(x int64) int64 { return x }),
		Pipe(parts[1], 2, func(x int64) int64 { return x }),
		Pipe(parts[2], 2, func(x int64) int64 { return x }),
	)
	go func() {
		for i := 1; i <= 999; i++ {
			src.Send(i)
		}
		src.Close()
	}()
	var got, cnt int64
	for {
		v, ok := merged.Recv()
		if !ok {
			break
		}
		got += v
		cnt++
	}
	if cnt != 999 {
		t.Fatalf("收到 %d, want 999", cnt)
	}
	if want := int64(999 * 1000 * 1999 / 6); got != want { // Σx²
		t.Fatalf("Σx² = %d, want %d", got, want)
	}
}

// TestNoGoroutineLeak：流水线收尾后所有组件 goroutine 必须退出。
func TestNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	src := NewChan[int](0)
	merged := Merge(Split(Pipe(src, 2, func(x int) int { return x }), 2)...)
	go func() {
		for i := 0; i < 100; i++ {
			src.Send(i)
		}
		src.Close()
	}()
	for {
		if _, ok := merged.Recv(); !ok {
			break
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 { // 容忍 runtime 自身波动
		if time.Now().After(deadline) {
			t.Fatalf("goroutine 泄漏：before=%d now=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitTimeout：限时等待 WaitGroup（卡住即失败，避免测试挂死）。
func waitTimeout(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("等待 goroutine 超过 %v，疑似停摆", d)
	}
}
