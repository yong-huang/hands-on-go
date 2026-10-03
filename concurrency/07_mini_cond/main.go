// 项目 7：mini Cond —— 通知列表与丢失唤醒
//
// 分步演示：
//
//	步骤 1  有界队列阻塞版：空队列让消费者睡去，生产者投放后 Signal 唤醒
//	步骤 2  3 个等待者：Signal 按票号逐个唤醒
//	步骤 3  丢失唤醒反例：条件检查与 Wait 之间放锁，通知落空
//	步骤 4  多生产多消费者压测：队列清空、无人残留
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Cond 是条件变量（condition variable）：让 goroutine 等待"条件为真"的事件，
// 被 Signal/Broadcast 唤醒。条件的本体是外部状态（如队列长度），Cond 只负责等待与通知。
type Cond struct {
	L      sync.Locker // 保护条件的锁；Wait 前必须持有，Wait 内部会放掉再拿回
	notify notifyList  // 票号通知列表（复刻 runtime 的 notifyList 协议）
}

func NewCond(l sync.Locker) *Cond { return &Cond{L: l} }

// notifyList 是通知列表：先领票、后入睡，Signal 按票号 FIFO 唤醒。
type notifyList struct {
	mu     sync.Mutex
	wait   uint64          // 已发出的票号总数
	notify uint64          // 已通知的票号数；notify == wait 表示当前无人等待
	queue  []chan struct{} // 每张未通知的票一个容量 1 的个人唤醒通道，队头票号最小
}

// register 领票：唤醒通道在领票这一刻就存在，之后的 Signal 不会落空，
// 这是"领票 → 放锁 → 入睡"三步中不丢唤醒的关键。
func (nl *notifyList) register() chan struct{} {
	nl.mu.Lock()
	defer nl.mu.Unlock()
	nl.wait++
	ch := make(chan struct{}, 1) // 容量 1：唤醒先于入睡到达也能接住
	nl.queue = append(nl.queue, ch)
	return ch
}

// notifyOne 唤醒队头的一张票；无人等待则什么都不发生（通知不存储、不补发）。
func (nl *notifyList) notifyOne() {
	nl.mu.Lock()
	defer nl.mu.Unlock()
	if nl.notify == nl.wait {
		return
	}
	ch := nl.queue[0]
	nl.queue = nl.queue[1:]
	nl.notify++
	ch <- struct{}{} // 容量 1，必不阻塞
}

// notifyAll 唤醒当前所有等待者。
func (nl *notifyList) notifyAll() {
	nl.mu.Lock()
	defer nl.mu.Unlock()
	for _, ch := range nl.queue {
		ch <- struct{}{}
	}
	nl.notify = nl.wait
	nl.queue = nil
}

// 等待票号数：演示与测试用来确认"对方已领票"，消除时序不确定。
func (nl *notifyList) pendingWaiters() uint64 {
	nl.mu.Lock()
	defer nl.mu.Unlock()
	return nl.wait - nl.notify
}

// Wait 释放锁并挂起，醒来后重新持锁。
// 顺序与 runtime 一致：先领票、再放锁、后入睡。
func (c *Cond) Wait() {
	ch := c.notify.register() // 1. 领票（通道已就位）
	c.L.Unlock()              // 2. 放锁：让改变条件的人拿得到锁
	<-ch                      // 3. 入睡；唤醒先到也被容量 1 的通道接住
	c.L.Lock()                // 4. 醒来重新持锁，调用方必须再检查条件
}

// Signal 唤醒一个等待者，Broadcast 唤醒全部；持不持 L 调用都合法。
func (c *Cond) Signal()    { c.notify.notifyOne() }
func (c *Cond) Broadcast() { c.notify.notifyAll() }

// BoundedQueue 是用 mini Cond 实现的有界阻塞队列。
type BoundedQueue struct {
	mu       sync.Mutex
	notFull  *Cond // 条件：队列未满，生产者等它
	notEmpty *Cond // 条件：队列非空，消费者等它
	items    []int
	capacity int
}

func NewBoundedQueue(capacity int) *BoundedQueue {
	q := &BoundedQueue{capacity: capacity}
	q.notFull = NewCond(&q.mu)
	q.notEmpty = NewCond(&q.mu)
	return q
}

func (q *BoundedQueue) Put(v int) {
	q.mu.Lock()
	for len(q.items) == q.capacity { // 必须用 for：醒来后条件可能又被别人占满
		q.notFull.Wait()
	}
	q.items = append(q.items, v)
	q.notEmpty.Signal()
	q.mu.Unlock()
}

func (q *BoundedQueue) Get() int {
	q.mu.Lock()
	for len(q.items) == 0 {
		q.notEmpty.Wait()
	}
	v := q.items[0]
	q.items = q.items[1:]
	q.notFull.Signal()
	q.mu.Unlock()
	return v
}

// waitUntil 等待 f 为真（演示辅助）。
func waitUntil(f func() bool) {
	for !f() {
		time.Sleep(time.Millisecond)
	}
}

func main() {
	fmt.Println("== 步骤 1：空队列让消费者睡去，投放后 Signal 唤醒 ==")
	q := NewBoundedQueue(2)
	got := make(chan int, 1)
	go func() { got <- q.Get() }()
	waitUntil(func() bool { return q.notEmpty.notify.pendingWaiters() == 1 }) // 确认消费者已领票
	q.Put(42)
	fmt.Printf("  消费者取到 %d（睡在 notEmpty 上，被 Put 里的 Signal 唤醒）\n", <-got)

	fmt.Println("== 步骤 2：3 个等待者，3 次 Signal 按票号逐个唤醒 ==")
	q2 := NewBoundedQueue(8)
	var awakened []int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id := 1; id <= 3; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			q2.mu.Lock()
			for len(q2.items) == 0 {
				q2.notEmpty.Wait()
			}
			mu.Lock()
			awakened = append(awakened, id)
			mu.Unlock()
			q2.mu.Unlock()
		}(id)
	}
	waitUntil(func() bool { return q2.notEmpty.notify.pendingWaiters() == 3 })
	for v := 1; v <= 3; v++ {
		q2.mu.Lock()
		q2.items = append(q2.items, v)
		q2.notEmpty.Signal() // 每次只唤醒队头一张票
		q2.mu.Unlock()
	}
	wg.Wait()
	fmt.Printf("  被唤醒的集合=%v（诚实预期：{1,2,3} 全部醒来、各消费一条；", awakened)
	fmt.Println("编号可能乱序——FIFO 只保证唤醒顺序，记录发生在醒来后重新抢锁）")

	fmt.Println("== 步骤 3：丢失唤醒反例：检查条件后放锁，再 Wait ==")
	q3 := NewBoundedQueue(8)
	checked := make(chan struct{})
	proceed := make(chan struct{})
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		q3.mu.Lock()
		empty := len(q3.items) == 0
		q3.mu.Unlock() // 反例第一步：检查完就放锁，条件保护出现缺口
		close(checked)
		if empty {
			<-proceed // 停在缺口上，保证下面的 Signal 赶在领票之前
			q3.mu.Lock()
			q3.notEmpty.Wait() // 反例第二步：睡之前没有重新检查条件
			q3.mu.Unlock()
		}
	}()
	<-checked // 消费者已检查完（看到空队列）、停在缺口
	q3.mu.Lock()
	q3.items = append(q3.items, 7)
	fmt.Printf("  Signal 时无人领票（pendingWaiters=%d）：通知按协议丢弃\n", q3.notEmpty.notify.pendingWaiters())
	q3.notEmpty.Signal() // 无人等待：通知落空，数据躺在队列里
	q3.mu.Unlock()
	close(proceed)
	select {
	case <-consumerDone:
		fmt.Println("  异常：消费者竟然醒了（不应发生）")
	case <-time.After(150 * time.Millisecond):
		fmt.Println("  消费者仍在睡：Signal 落空——队列里有数据、消费者却醒不来，丢失唤醒复现")
	}
	// 补救：持锁改条件再 Broadcast；消费者醒来、退出 Wait 返回（它不会重查条件，直接结束）
	q3.mu.Lock()
	q3.items = append(q3.items, 8)
	q3.notEmpty.Broadcast()
	q3.mu.Unlock()
	<-consumerDone
	fmt.Printf("  Broadcast 后消费者醒来退出；但它没重查条件，两条数据仍留在队列（剩余 %d 条）\n", len(q3.items))
	fmt.Println("  正确顺序：持锁检查条件，不满足就 for+Wait，醒来后重查——q.Get() 就是这个写法")

	fmt.Println("== 步骤 4：4 生产者 × 4 消费者，2000 个元素清空 ==")
	q4 := NewBoundedQueue(4)
	const total = 2000
	var consumed int64
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total/4; i++ {
				q4.Put(1)
			}
		}()
	}
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < total/4; i++ {
				q4.Get()
				atomic.AddInt64(&consumed, 1)
			}
		}()
	}
	wg.Wait()
	fmt.Printf("  消费 %d，队列剩余 %d（诚实预期：2000 / 0；消费者各自取 500 个，天然收敛）\n",
		consumed, len(q4.items))
}
