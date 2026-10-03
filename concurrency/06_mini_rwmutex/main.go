// 项目 6：mini RWMutex —— readerCount 负数协议
//
// 分步演示：
//
//	步骤 1  并发读：多个读者同时进入临界区，readerCount 累加到 N
//	步骤 2  独占写：写临界区期间没有任何读者，读者也互相排斥写者
//	步骤 3  写阻塞读（写者优先）：写者一到，新读者被挡在门外
//	步骤 4  死锁复现：持读锁 → 写者到达 → 同一 goroutine 再取读锁
//	        （真实死锁，演示进程打印状态后退出）
package main

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// maxReaders 把 readerCount 的取值空间劈成两半：
// [0, maxReaders) 表示活跃读者个数；减去 maxReaders 变成负数表示"有写者已到达"。
// 一个 int32 同时编码两种状态，这就是"readerCount 负数协议"。
const maxReaders = 1 << 30

// RWMutex 是一个读写锁：任意多个读者可同时持有，写者独占。
type RWMutex struct {
	w           sync.Mutex    // 写者之间的互斥：多个写者在此排队
	writerSem   chan struct{} // 写者等待"所有活跃读者离开"的信号量（无缓冲，一次交接）
	readerSem   chan struct{} // 被写者挡住的读者在此排队，写者 Unlock 时逐个交接唤醒
	readerCount atomic.Int32  // >=0：活跃读者数；<0：有写者已注册（值 = 读者数 - maxReaders）
	readerWait  atomic.Int32  // 挂起写者还在等的读者个数：递减到 0 的那个读者唤醒写者
}

func newRWMutex() *RWMutex {
	return &RWMutex{writerSem: make(chan struct{}), readerSem: make(chan struct{})}
}

// RLock 加读锁：readerCount 加 1 后为负，说明有写者已注册，读者排队让路。
func (rw *RWMutex) RLock() {
	if rw.readerCount.Add(1) < 0 {
		<-rw.readerSem // 写者 Unlock 时逐个 send，每次 send 与一个读者的 receive 交接
	}
}

// RUnlock 释放读锁：若是挂起写者等待的最后一个读者，由它唤醒写者。
func (rw *RWMutex) RUnlock() {
	if r := rw.readerCount.Add(-1); r < 0 {
		if rw.readerWait.Add(-1) == 0 {
			rw.writerSem <- struct{}{} // 最后一个离开的读者交接写者
		}
	}
}

// Lock 加写锁：先解决写者之间的竞争，再把 readerCount 压成负数挡住新读者，
// 然后等已在临界区里的读者全部离开。
func (rw *RWMutex) Lock() {
	rw.w.Lock()
	// 减 maxReaders：新读者的 Add(1) 结果必为负而排队；返回值 + maxReaders 即当时活跃读者数
	r := rw.readerCount.Add(-maxReaders) + maxReaders
	if r != 0 && rw.readerWait.Add(r) != 0 {
		<-rw.writerSem // 等这些读者全部 RUnlock；最后一个离开的读者唤醒本写者
	}
}

// Unlock 释放写锁：readerCount 加回 maxReaders 恢复正数区间，
// 排队的读者个数即返回值 r，逐个交接唤醒，最后放行下一个写者。
func (rw *RWMutex) Unlock() {
	r := rw.readerCount.Add(maxReaders)
	for i := int32(0); i < r; i++ {
		rw.readerSem <- struct{}{} // 与一个排队读者的 receive 交接，唤醒一个
	}
	rw.w.Unlock()
}

func main() {
	fmt.Println("== 步骤 1：并发读：4 个读者同时进入 ==")
	rw := newRWMutex()
	rw.RLock() // 主 goroutine 先持有读锁，证明读锁可共享
	var inside atomic.Int32
	var wg sync.WaitGroup
	for i := 1; i <= 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rw.RLock()
			defer rw.RUnlock()
			n := inside.Add(1)
			// 在看什么：4 个读者都进来了，此刻 readerCount 应为正数 5（含主 goroutine）
			fmt.Printf("  读者 %d 进入，临界区内读者数=%d，readerCount=%d\n", id, n, rw.readerCount.Load())
		}(i)
	}
	wg.Wait()
	rw.RUnlock()

	fmt.Println("== 步骤 2：独占写：写临界区期间读者进不来 ==")
	var active atomic.Int32
	var violated atomic.Int32
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				rw.Lock()
				if active.Add(1) != 1 {
					violated.Add(1) // 临界区内出现第 2 个持有者：互斥被破坏
				}
				active.Add(-1)
				rw.Unlock()
			}
		}()
	}
	wg.Wait()
	fmt.Printf("  600 次写临界区，互斥破坏次数=%d（诚实预期：恒为 0，任何非 0 都是 bug）\n", violated.Load())

	fmt.Println("== 步骤 3：写阻塞读：写者到达后，新读者排队 ==")
	rw.RLock() // 读者先持锁
	writerAcquired := make(chan struct{})
	go func() { rw.Lock(); close(writerAcquired); time.Sleep(20 * time.Millisecond); rw.Unlock() }()
	for rw.readerCount.Load() >= 0 {
		time.Sleep(time.Millisecond) // 等写者注册（readerCount 变负）
	}
	newReaderIn := make(chan struct{})
	go func() { rw.RLock(); close(newReaderIn); rw.RUnlock() }()
	time.Sleep(80 * time.Millisecond)
	// 在看什么：写者还没拿到锁，新读者也没进来——负数协议把两边都挡住了
	select {
	case <-writerAcquired:
		fmt.Println("  写者已进入（新读者仍未放行）")
	case <-newReaderIn:
		fmt.Println("  异常：写者还在等，新读者却插队了（不应发生）")
	default:
		fmt.Printf("  写者等待中，readerCount=%d（负数=写者已注册）；新读者也未放行\n", rw.readerCount.Load())
	}
	rw.RUnlock() // 读者放手 → 写者进 → 写者 Unlock 再唤醒新读者
	<-writerAcquired
	<-newReaderIn
	fmt.Println("  顺序确认：写者先于新读者拿到锁（写者优先）")

	fmt.Println("== 步骤 4：死锁复现：持读锁 → 写者到 → 同 goroutine 再取读锁 ==")
	rw.RLock()                // G1 持有读锁
	go func() { rw.Lock() }() // 写者注册：readerCount 变负，等 G1 的 RUnlock
	for rw.readerCount.Load() >= 0 {
		time.Sleep(time.Millisecond)
	}
	go func() { rw.RLock() }() // G1 同一个逻辑流再取读锁：Add(1) 为负，排队
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("  死锁现场：readerCount=%d readerWait=%d\n", rw.readerCount.Load(), rw.readerWait.Load())
	fmt.Println("  写者在 writerSem 等最后一个读者 RUnlock；")
	fmt.Println("  内层 RLock 在 readerSem 等写者 Unlock；外层 RUnlock 永远执行不到——三方互等。")
	fmt.Println("  （本步演示真实死锁，goroutine 无法恢复，进程在此退出）")
	os.Exit(0)
}
