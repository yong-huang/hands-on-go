// 项目 15：mini channel——锁 + 环形缓冲 + 发送/接收等待队列的运行时复刻。
// 先按对 hchan（runtime/chan.go 里 channel 的内部结构）的理解盲写，压测到绿
// 后对照真实源码出差异表（见 README）。
//
// 结构对照：
//
//	type hchan struct {          // runtime/chan.go（节选）
//	    qcount   uint           // 环形缓冲中元素个数
//	    dataqsiz uint           // 环形缓冲容量
//	    buf      unsafe.Pointer // 环形缓冲底层数组
//	    sendx    uint           // 下一个发送写入的下标
//	    recvx    uint           // 下一个接收读取的下标
//	    recvq    waitq          // 阻塞中的接收者队列
//	    sendq    waitq          // 阻塞中的发送者队列
//	    lock     mutex          // 保护以上全部字段
//	}
//
// 运行方式：go run main.go。标注（固定）的行每次相同；标注（每次不同）的
// 行含时序类信息，只看量级与相对关系。
package main

import (
	"fmt"
	"sync"
	"time"
)

// waiter：等待队列的一个节点，发送者与接收者共用。
// 发送者入队时把值放进 elem；接收者入队后 elem 是收货槽位。
// 唤醒用 close(ready)：close 不可阻塞也不会丢，天然的"叫醒一个"信号。
type waiter[T any] struct {
	next  *waiter[T]    // FIFO 链表后继
	elem  T             // 发送者：待交付的值；接收者：收货槽位
	ok    bool          // 接收者醒来时读：true = 拿到真实值
	bad   bool          // 发送者醒来时读：true = 因 close 被释放，回去 panic
	ready chan struct{} // 被唤醒时 close 掉
}

// wqueue：FIFO 等待队列（对应 hchan 的 waitq，标准库是双向链表 + sudog）。
type wqueue[T any] struct {
	head, tail *waiter[T]
}

func (q *wqueue[T]) push(w *waiter[T]) {
	if q.tail == nil {
		q.head, q.tail = w, w
		return
	}
	q.tail.next = w
	q.tail = w
}

func (q *wqueue[T]) pop() *waiter[T] {
	w := q.head
	if w == nil {
		return nil
	}
	q.head = w.next
	if q.head == nil {
		q.tail = nil
	}
	w.next = nil
	return w
}

// Chan：迷你 channel，零值不可用，用 New 创建（对应 make(chan T, cap)）。
type Chan[T any] struct {
	mu     sync.Mutex
	buf    []T // 环形缓冲（无缓冲 channel 长度为 0）
	qcount int // 环形缓冲中的元素个数（对应 len）
	sendx  int // 下一个发送写入的下标（环上回绕）
	recvx  int // 下一个接收读取的下标（环上回绕）
	closed bool
	recvq  wqueue[T] // 排队中的接收者
	sendq  wqueue[T] // 排队中的发送者
}

// New：创建容量为 cap 的迷你 channel。
func New[T any](cap int) *Chan[T] {
	return &Chan[T]{buf: make([]T, cap)}
}

// Send：阻塞发送。nil channel 永久阻塞（select{} 只在全体 goroutine 都
// 睡着时才被 runtime 判死锁，与其他 goroutine 并存时就是普通阻塞）。
func (ch *Chan[T]) Send(v T) {
	if ch == nil {
		select {}
	}
	w := &waiter[T]{elem: v, ready: make(chan struct{})}

	ch.mu.Lock()
	if ch.closed { // 向已关闭发送：立即 panic（对应 runtime 的 fast-path 分支）
		ch.mu.Unlock()
		panic("send on closed channel")
	}
	if r := ch.recvq.pop(); r != nil {
		// 直接交接：有等待中的接收者，值不过缓冲（rendezvous）。
		// 标准库 chansend 同样在此分支 memmove 后 goready 接收者。
		r.elem = v
		r.ok = true
		ch.mu.Unlock()
		close(r.ready)
		return
	}
	if ch.qcount < len(ch.buf) {
		ch.buf[ch.sendx] = v // 缓冲未满：写环，推进 sendx
		ch.sendx = (ch.sendx + 1) % len(ch.buf)
		ch.qcount++
		ch.mu.Unlock()
		return
	}
	ch.sendq.push(w) // 缓冲满且无人接：排队停车
	ch.mu.Unlock()
	<-w.ready
	if w.bad { // 停车期间 channel 被 close：醒来补 panic
		panic("send on closed channel")
	}
}

// Recv：阻塞接收。第二个返回值对应内建 v, ok := <-ch 的 ok。
func (ch *Chan[T]) Recv() (v T, ok bool) {
	if ch == nil {
		select {}
	}
	ch.mu.Lock()
	if s := ch.sendq.pop(); s != nil {
		// 有等待中的发送者：先吃缓冲队头，再把发送者的值补到队尾，
		// 保证 FIFO（标准库 chanrecv 对带缓冲 channel 的同款协议）。
		if ch.qcount > 0 {
			v = ch.buf[ch.recvx]
			ch.recvx = (ch.recvx + 1) % len(ch.buf)
			ch.buf[ch.sendx] = s.elem
			ch.sendx = (ch.sendx + 1) % len(ch.buf)
		} else { // 无缓冲：发送者直接把值递到手里
			v = s.elem
		}
		s.ok = true
		ch.mu.Unlock()
		close(s.ready)
		return v, true
	}
	if ch.qcount > 0 {
		v = ch.buf[ch.recvx]
		var zero T
		ch.buf[ch.recvx] = zero // 清槽位，避免对象被环形缓冲吊住不回收
		ch.recvx = (ch.recvx + 1) % len(ch.buf)
		ch.qcount--
		ch.mu.Unlock()
		return v, true
	}
	if ch.closed { // 缓冲已空且已关闭：零值 + ok=false，立即返回
		ch.mu.Unlock()
		var zero T
		return zero, false
	}
	r := &waiter[T]{ready: make(chan struct{})}
	ch.recvq.push(r) // 无可取、未关闭：排队停车
	ch.mu.Unlock()
	<-r.ready
	return r.elem, r.ok
}

// Close：关闭广播。排队的接收者全部拿到 ok=false；排队的发送者醒来 panic。
// 缓冲里已有的值不受影响，接收方按序取完后才进入零值阶段。
func (ch *Chan[T]) Close() {
	if ch == nil {
		panic("close of nil channel")
	}
	ch.mu.Lock()
	if ch.closed {
		ch.mu.Unlock()
		panic("close of closed channel")
	}
	ch.closed = true
	for s := ch.sendq.pop(); s != nil; s = ch.sendq.pop() {
		s.bad = true
		close(s.ready)
	}
	for r := ch.recvq.pop(); r != nil; r = ch.recvq.pop() {
		r.ok = false
		var zero T
		r.elem = zero
		close(r.ready)
	}
	ch.mu.Unlock()
}

// Len / Cap：对应内建 len(ch) 与 cap(ch)。
func (ch *Chan[T]) Len() int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.qcount
}

func (ch *Chan[T]) Cap() int { return len(ch.buf) }

// debug：演示用内部快照（演示专用，正式 API 只有 Len/Cap）。
func (ch *Chan[T]) debug() string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return fmt.Sprintf("qcount=%d sendx=%d recvx=%d closed=%v 排队[发%d 收%d]",
		ch.qcount, ch.sendx, ch.recvx, ch.closed, len0(&ch.sendq), len0(&ch.recvq))
}

func len0[T any](q *wqueue[T]) int {
	n := 0
	for w := q.head; w != nil; w = w.next {
		n++
	}
	return n
}

func main() {
	fmt.Println("== 第 1 步：结构总览 ==")
	fmt.Println("Chan[T] = 锁 + 环形缓冲(sendx/recvx) + 发送/接收两条 FIFO 等待队列。")
	fmt.Println("发送：交接空闲接收者 → 写缓冲 → 排队停车，三选一；接收对称。")
	fmt.Println()

	fmt.Println("== 第 2 步：无缓冲直接交接（rendezvous） ==")
	unbuf := New[string](0)
	said := make(chan struct{})
	go func() {
		unbuf.Send("ping")
		fmt.Println("发送方：ping 已递到接收者手里（固定）")
		close(said)
	}()
	v, _ := unbuf.Recv()
	<-said // 等发送方打完再继续，输出顺序固定
	fmt.Printf("接收方：收到 %q（固定）\n", v)
	fmt.Println()

	fmt.Println("== 第 3 步：环形缓冲回绕（cap=3） ==")
	ring := New[int](3)
	for i := 1; i <= 3; i++ {
		ring.Send(i)
	}
	fmt.Printf("发满 3 个：           %s\n", ring.debug())
	ring.Recv()
	fmt.Printf("收走 1 个：           %s\n", ring.debug())
	ring.Send(4)
	fmt.Printf("再发 4（sendx 回绕）：%s\n", ring.debug())
	fmt.Println()

	fmt.Println("== 第 4 步：缓冲满发送阻塞 ==")
	full := New[int](1)
	full.Send(1)
	blocked := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		close(blocked) // 先标记已进入 Send
		full.Send(2)
		fmt.Println("发送方 2：接收者取走 1 之后才发出（固定）")
		close(sent)
	}()
	<-time.After(50 * time.Millisecond) // 让发送者 2 停进 sendq
	fmt.Printf("50ms 后发送 2 仍未返回：%s（固定）\n", full.debug())
	got, _ := full.Recv()
	fmt.Printf("接收方取到 %d，发送 2 随即完成（固定）\n", got)
	<-sent // 等发送方 2 打完再继续，输出顺序固定
	fmt.Println()

	fmt.Println("== 第 5 步：close 广播唤醒全部排队接收者 ==")
	idle := New[int](0)
	var wg sync.WaitGroup
	wg.Add(3)
	for i := 1; i <= 3; i++ {
		go func(i int) {
			defer wg.Done()
			_, ok := idle.Recv()
			fmt.Printf("接收者 %d：醒来 ok=%v（固定）\n", i, ok)
		}(i)
	}
	<-time.After(50 * time.Millisecond)
	idle.Close()
	wg.Wait()
	fmt.Println()

	fmt.Println("== 第 6 步：向已关闭 channel 发送 panic ==")
	func() {
		defer func() {
			fmt.Printf("recover 到：%v（固定）\n", recover())
		}()
		dead := New[int](0)
		dead.Close()
		dead.Send(1)
	}()
}
