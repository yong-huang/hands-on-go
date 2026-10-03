// 项目 15 验收测试：
// ① 收发 / 缓冲满阻塞 / 关闭三语义在 -race 下高压可过；
// ② 关闭后接收立即返回零值、向已关闭发送 panic 的断言；
// ③ 与 runtime hchan 的差异表见 README。
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestUnbufferedRendezvous：无缓冲收发，直接交接且总量守恒。
func TestUnbufferedRendezvous(t *testing.T) {
	ch := New[int](0)
	const (
		gs   = 4
		perG = 250
	)
	var sum atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2 * gs)
	for i := 0; i < gs; i++ {
		go func(base int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				ch.Send(base*perG + j)
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				v, ok := ch.Recv()
				if !ok {
					t.Error("未关闭的 channel 收到 ok=false")
					return
				}
				sum.Add(int64(v))
			}
		}()
	}
	wg.Wait()
	want := gs * perG * (gs*perG - 1) / 2 // 0..gs*perG-1 的和
	if sum.Load() != int64(want) {
		t.Fatalf("sum = %d, want %d", sum.Load(), want)
	}
}

// TestBufferedFIFOAndWrap：缓冲收发保持 FIFO，且跨环回绕不乱序。
func TestBufferedFIFOAndWrap(t *testing.T) {
	ch := New[int](3)
	go func() {
		for i := 0; i < 10; i++ {
			ch.Send(i)
		}
	}()
	for i := 0; i < 10; i++ {
		v, ok := ch.Recv()
		if !ok || v != i {
			t.Fatalf("第 %d 个收到 (%v,%v), want (%d,true)", i, v, ok, i)
		}
	}
}

// TestBufferedFullBlocks：缓冲满后发送阻塞，接收腾位后发送者完成。
func TestBufferedFullBlocks(t *testing.T) {
	ch := New[int](1)
	ch.Send(1) // 装满
	done := make(chan struct{})
	go func() {
		ch.Send(2)
		close(done)
	}()
	select { // 发送者必须停在 sendq
	case <-done:
		t.Fatal("缓冲已满，Send 不应返回")
	case <-time.After(20 * time.Millisecond):
	}
	if got, _ := ch.Recv(); got != 1 {
		t.Fatalf("收到 %d, want 1", got)
	}
	select {
	case <-done: // 腾位后发送者被唤醒
	case <-time.After(time.Second):
		t.Fatal("接收腾位后发送者未被唤醒")
	}
	if ch.Len() != 1 {
		t.Fatalf("Len = %d, want 1", ch.Len())
	}
}

// TestRecvBlocksWhenEmpty：缓冲空时接收阻塞，发送到达后被唤醒。
func TestRecvBlocksWhenEmpty(t *testing.T) {
	ch := New[int](2)
	done := make(chan struct{})
	go func() {
		v, ok := ch.Recv()
		if !ok || v != 42 {
			t.Errorf("收到 (%v,%v), want (42,true)", v, ok)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("缓冲为空，Recv 不应返回")
	case <-time.After(20 * time.Millisecond):
	}
	ch.Send(42)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("发送后接收者未被唤醒")
	}
}

// TestCloseRecvZeroImmediately：关闭后接收立即返回零值 + ok=false。
// 用自定义结构体验证"零值"而非恰好的 0。
type point struct{ X, Y int }

func TestCloseRecvZeroImmediately(t *testing.T) {
	ch := New[point](0)
	ch.Close()
	start := time.Now()
	v, ok := ch.Recv()
	if ok {
		t.Fatal("已关闭 channel 接收的 ok 应为 false")
	}
	if v != (point{}) {
		t.Fatalf("收到 %v, want 零值 point{}", v)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("关闭后接收阻塞了 %v，应立即返回", elapsed)
	}
}

// TestCloseWithBufferedItemsDrains：close 不清缓冲：剩余值按序以 ok=true
// 取完，之后才进入零值 + ok=false 阶段。
func TestCloseWithBufferedItemsDrains(t *testing.T) {
	ch := New[int](2)
	ch.Send(1)
	ch.Send(2)
	ch.Close()
	for i := 1; i <= 2; i++ {
		v, ok := ch.Recv()
		if !ok || v != i {
			t.Fatalf("第 %d 个收到 (%v,%v), want (%d,true)", i, v, ok, i)
		}
	}
	if v, ok := ch.Recv(); ok || v != 0 {
		t.Fatalf("排空后收到 (%v,%v), want (0,false)", v, ok)
	}
}

// TestCloseBroadcastWakeAllReceivers：close 广播——全部排队接收者醒来。
func TestCloseBroadcastWakeAllReceivers(t *testing.T) {
	ch := New[int](0)
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if _, ok := ch.Recv(); ok {
				t.Error("close 唤醒的接收者 ok 应为 false")
			}
		}()
	}
	time.Sleep(20 * time.Millisecond) // 等全部接收者停进 recvq
	ch.Close()
	waitTimeout(t, &wg, time.Second)
}

// TestSendOnClosedPanics：快路径——已关闭后再发送，立即 panic。
func TestSendOnClosedPanics(t *testing.T) {
	ch := New[int](4)
	ch.Close()
	msg := catchPanic(func() { ch.Send(1) })
	if msg != "send on closed channel" {
		t.Fatalf("panic 消息 = %q, want %q", msg, "send on closed channel")
	}
}

// TestSendParkedThenClosePanics：慢路径——停车中的发送者被 close 唤醒后
// 补 panic（对应 runtime 中 sudog 被 close 释放的路径）。
func TestSendParkedThenClosePanics(t *testing.T) {
	ch := New[int](0)
	msgCh := make(chan string, 1)
	go func() {
		msgCh <- catchPanic(func() { ch.Send(1) })
	}()
	time.Sleep(20 * time.Millisecond) // 等发送者停进 sendq
	ch.Close()
	select {
	case msg := <-msgCh:
		if msg != "send on closed channel" {
			t.Fatalf("panic 消息 = %q, want %q", msg, "send on closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("close 未唤醒停车中的发送者")
	}
}

// TestCloseOfClosedPanics / TestCloseOfNilPanics：重复 close 与 close(nil)。
func TestCloseOfClosedPanics(t *testing.T) {
	ch := New[int](1)
	ch.Close()
	if msg := catchPanic(func() { ch.Close() }); msg != "close of closed channel" {
		t.Fatalf("panic 消息 = %q", msg)
	}
}

func TestCloseOfNilPanics(t *testing.T) {
	var ch *Chan[int]
	if msg := catchPanic(func() { ch.Close() }); msg != "close of nil channel" {
		t.Fatalf("panic 消息 = %q", msg)
	}
}

// TestNilChanBlocksForever：nil channel 收发永久阻塞。
func TestNilChanBlocksForever(t *testing.T) {
	var ch *Chan[int]
	done := make(chan struct{})
	go func() {
		ch.Send(1)
		ch.Recv()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("nil channel 的收发应永久阻塞")
	case <-time.After(30 * time.Millisecond):
	}
}

// TestFIFOAcrossQueuedSenders：缓冲满后继续发送的值排在队尾，接收顺序
// 仍严格等于发送顺序（runtime 的"吃队头、发送者补队尾"协议）。
func TestFIFOAcrossQueuedSenders(t *testing.T) {
	ch := New[int](2)
	const total = 20
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < total; i++ {
			ch.Send(i)
		}
	}()
	for i := 0; i < total; i++ {
		v, ok := ch.Recv()
		if !ok || v != i {
			t.Fatalf("第 %d 个收到 (%v,%v), want (%d,true)", i, v, ok, i)
		}
	}
	wg.Wait()
}

// TestStressMixed：三语义混合高压——多发送者、多接收者、中途 close，
// 接收总量与发送总量守恒，close 后接收排空进入零值阶段。
func TestStressMixed(t *testing.T) {
	ch := New[int](4)
	const (
		gs   = 4
		perG = 250
	)
	total := gs * perG
	var (
		mu     sync.Mutex
		recvN  int
		sendWG sync.WaitGroup
		recvWG sync.WaitGroup
	)
	stop := make(chan struct{})
	sendWG.Add(gs)
	for i := 0; i < gs; i++ {
		go func() {
			defer sendWG.Done()
			for j := 0; j < perG; j++ {
				ch.Send(1) // 值恒为 1，接收计数即发送计数
			}
		}()
	}
	recvWG.Add(3)
	for g := 0; g < 3; g++ {
		go func() {
			defer recvWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, ok := ch.Recv(); ok {
					mu.Lock()
					recvN++
					mu.Unlock()
				}
			}
		}()
	}
	waitTimeout(t, &sendWG, time.Second) // 发送全部完成
	close(stop)                          // 接收者见 stop 即退出
	ch.Close()                           // 仍停在 recvq 里的被广播唤醒
	waitTimeout(t, &recvWG, time.Second)
	for { // 主 goroutine 收尾：缓冲剩余值也计入，直到排空拿到 ok=false
		if _, ok := ch.Recv(); !ok {
			break
		}
		mu.Lock()
		recvN++
		mu.Unlock()
	}
	mu.Lock()
	got := recvN
	mu.Unlock()
	if got != total {
		t.Fatalf("接收 %d, want %d", got, total)
	}
}

// catchPanic：运行 f 并把 panic 消息（或 "<无>"）作为字符串返回。
func catchPanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		} else {
			msg = "<无>"
		}
	}()
	f()
	return "<无>"
}

// waitTimeout：限时等待 WaitGroup（卡住即失败，避免测试死死挂住）。
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
		t.Fatalf("等待 goroutine 超过 %v，疑似死锁或丢唤醒", d)
	}
}
