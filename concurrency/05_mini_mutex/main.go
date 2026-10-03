// 项目 5：mini Mutex——双模式（正常/饥饿）简化复刻。CAS 复合状态字 +
// 欠账唤醒信号量，先盲写后对照标准库出差异表（见 README）。
//
// 状态字：bit0 locked（含"交接在途"的账面占用）、bit2 starving（所有权
// 定向交接中）、bit3 起为等待者计数（恒等于"还在排队"的人数）；bit1 是
// 标准库的 woken 位，本复刻省略，醒来一律重抢。
//
// 运行方式：go run main.go。输出标注（固定）的行每次相同；标注
// （每次不同）的行是计时，只看量级。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mutexLocked      = 1 << iota // 1
	_                            // bit1：标准库的 woken 位，本复刻省略
	mutexStarving                // 4
	mutexWaiterShift = 3         // 等待者计数从 bit3 起

	starvationThresholdNs = 1_000_000 // 等待超过 1ms 进入饥饿模式
	maxSpinRounds         = 4         // 自旋窗口：最多 4 轮观望
)

// spinSink：承接 spin 的空转结果，防止编译器把自旋循环优化掉。
// atomic 写：自旋者彼此并发，普通全局写会被 -race 判为竞态。
var spinSink atomic.Uint64

// Mutex：零值可用。信号量是双通道（懒初始化）：nudge 只是"去重抢"的
// 叫醒，handoff 才承载所有权。分开是因为"醒来看见 starving 位就当被
// 交接"会把别人排队置上的位误读成交接（实测双 owner 直接翻车）。
type Mutex struct {
	state   atomic.Int32
	wakeups atomic.Int32 // 普通唤醒的欠账额度（记了就不许丢）
	sema    atomic.Pointer[semaSet]
}

type semaSet struct {
	nudge   chan struct{} // 普通叫醒：丢了有欠账兜底
	handoff chan struct{} // 饥饿交接：阻塞投递，必须签收
}

func (m *Mutex) semaSet() *semaSet {
	if s := m.sema.Load(); s != nil {
		return s
	}
	s := &semaSet{nudge: make(chan struct{}), handoff: make(chan struct{})}
	if m.sema.CompareAndSwap(nil, s) {
		return s
	}
	return m.sema.Load()
}

// Lock：先试快路径（一次 CAS，无争用场景的全部成本），失败进慢路径。
func (m *Mutex) Lock() {
	if m.state.CompareAndSwap(0, mutexLocked) {
		return
	}
	m.lockSlow()
}

func (m *Mutex) lockSlow() {
	var waitStart int64
	starving, queued := false, false
	iter := 0
	for {
		old := m.state.Load()

		// 自旋窗口：锁被占、且仍是正常模式时，原地观望几轮，
		// 赌持有者马上释放，省掉一次睡眠加唤醒的来回。
		if old&(mutexLocked|mutexStarving) == mutexLocked && canSpin(iter) {
			spin()
			iter++
			continue
		}

		newState := old
		if old&(mutexLocked|mutexStarving) != 0 {
			// 被占或交接在途：排队（首次入队才 +1，重抢不重复计数）
			if !queued {
				newState += 1 << mutexWaiterShift
			}
		} else {
			newState |= mutexLocked // 空闲且正常模式：直接占有
		}
		if starving {
			newState |= mutexStarving
		}
		if !m.state.CompareAndSwap(old, newState) {
			continue // 状态被并发修改，重读重来
		}
		if old&(mutexLocked|mutexStarving) == 0 {
			m.leaveQueue(&queued)
			return // CAS 抢到了空闲锁
		}
		queued = true // 已入队，计数在状态字里，退出前必须撤销

		// 非饥饿等待者可先销欠账（假唤醒，不真睡，回去重抢）。
		// 所有权只走 handoff 通道，欠账永远冒充不了交接。
		if m.wakeups.Add(-1) >= 0 {
			iter = 0
			continue
		}
		if waitStart == 0 {
			waitStart = time.Now().UnixNano()
		}
		// 睡前复查：排队理由可能已消失（锁已空且无交接在途）。
		if now := m.state.Load(); now&(mutexLocked|mutexStarving) == 0 {
			continue
		}
		set := m.semaSet()
		select { // 停车：等普通叫醒或所有权令牌，醒来类型由通道显式区分
		case <-set.handoff: // 饥饿交接：locked 位由 Unlock 保留给我
			starving = starving || time.Now().UnixNano()-waitStart > starvationThresholdNs
			old = m.state.Load()
			delta := int32(-(1 << mutexWaiterShift)) // 撤销自己的排队计数
			if old&mutexStarving != 0 && (!starving || old>>mutexWaiterShift == 1) {
				delta -= mutexStarving // 我没饿或我是最后一个 waiter：退回正常模式
			}
			m.state.Add(delta)
			return
		case <-set.nudge: // 普通叫醒：不带所有权，回去重新竞争
			// 等待是否超阈值在这里结算：超时的 waiter 重排队时会把
			// starving 位置上，从而触发后续 Unlock 的交接路径。
			starving = starving || time.Now().UnixNano()-waitStart > starvationThresholdNs
		}
		iter = 0
	}
}

// leaveQueue：离队时撤销自己的排队计数（仅排过队才需要）。
func (m *Mutex) leaveQueue(queued *bool) {
	if *queued {
		m.state.Add(-1 << mutexWaiterShift)
		*queued = false
	}
}

// Unlock：正常模式清 locked 位 + 记欠账/投令牌；饥饿模式保留 locked 位，
// 用阻塞式发送把所有权连同令牌一起交给队首（令牌不许丢，丢了锁就没了主人）。
func (m *Mutex) Unlock() {
	for {
		old := m.state.Load()
		if old&mutexLocked == 0 {
			panic("mini: unlock of unlocked mutex")
		}
		handoff := old>>mutexWaiterShift != 0 && old&mutexStarving != 0
		newState := old
		if !handoff {
			newState &^= mutexLocked
		}
		if !m.state.CompareAndSwap(old, newState) {
			continue // 状态被并发修改，重读重来
		}
		if newState>>mutexWaiterShift == 0 {
			return // 无排队者，收工
		}
		set := m.semaSet()
		if handoff {
			set.handoff <- struct{}{} // 阻塞投递：排队者必然进入 select 签收
			return
		}
		m.wakeups.Add(1) // 先记账：这笔叫醒变成额度，永远不会丢
		select {         // 有停车者就直接叫醒；没有就留在欠账里
		case set.nudge <- struct{}{}:
		default:
		}
		return
	}
}

func canSpin(iter int) bool {
	return iter < maxSpinRounds && runtime.GOMAXPROCS(0) > 1
}

// spin：一小段空转（标准库用 30 条 PAUSE 指令，这里用带 sink 的算术循环替代）。
func spin() {
	x := uint64(1)
	for i := 0; i < 32; i++ {
		x += x << 1
	}
	spinSink.Store(x)
}

// State：演示用只读快照（不加锁读原子字段，仅供观察）。
func (m *Mutex) State() int32 { return m.state.Load() }

func main() {
	fmt.Println("== 第 1 步：状态字设计 ==")
	fmt.Println("bit0 locked / bit2 starving / bit3 起为等待者计数。")
	fmt.Println("快路径一次 CAS；慢路径自旋窗口 → 排队 → 欠账唤醒或停车。")
	fmt.Println("等待超 1ms 的 waiter 醒来后把锁切进饥饿模式：Unlock 定向交接队首。")
	fmt.Println()

	fmt.Println("== 第 2 步：快路径粗测——无争用 Lock/Unlock 100 万次 ==")
	m := &Mutex{}
	start := time.Now()
	for i := 0; i < 1_000_000; i++ {
		m.Lock()
		m.Unlock()
	}
	fmt.Printf("mini  Mutex：%-10s（每次不同）\n", time.Since(start).Round(time.Microsecond))
	var sm sync.Mutex
	start = time.Now()
	for i := 0; i < 1_000_000; i++ {
		sm.Lock()
		sm.Unlock()
	}
	fmt.Printf("sync  Mutex：%-10s（每次不同；严谨对比看 go test -bench）\n",
		time.Since(start).Round(time.Microsecond))
	fmt.Println()

	fmt.Println("== 第 3 步：100 goroutine 临界区互斥压测 ==")
	fmt.Printf("各自累加 5000 次：n = %d（固定，必须恰为 500000）\n", hammer(100, 5000))
	fmt.Println("饥饿模式的触发与交接由 TestStarvationHandoff 断言，见 mutex_test.go。")
}

// hammer：procs 个 goroutine 各加 perG 次，临界区内用 inCS 守卫验证互斥。
// 返回总和（必须恰为 procs*perG）。
func hammer(procs, perG int) int64 {
	var (
		m    Mutex
		n    int64
		inCS atomic.Int32
		wg   sync.WaitGroup
	)
	wg.Add(procs)
	for i := 0; i < procs; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				m.Lock()
				if inCS.Add(1) != 1 { // 临界区内并发数必须恒为 1
					panic("互斥被打破：临界区出现并发")
				}
				n++
				inCS.Add(-1)
				m.Unlock()
			}
		}()
	}
	wg.Wait()
	return n
}
