package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"time"
)

// quiet 抑制演示打印（-json 模式下使用），同时输出状态快照给 viz.py
var quiet = flag.Bool("json", false, "输出 JSON 状态快照（供 viz.py）")

// =============================================================================
// 03_channel：Channel 底层实现（教学模拟版）
//
// 为什么不直接读真实内存？
//   channel 的 hchan 不在公开 API 里，且字段布局随版本演进（Go 1.24 起
//   运行时大规模重构），unsafe 硬读极易读到垃圾字段，Flaky 又不稳定。
//   但 hchan 的核心设计十几年没变过，本程序用纯 Go 复刻它：
//
//     type hchan struct {        // runtime/chan.go（真实字段，已简化）
//         qcount   uint           // 当前队列中元素个数
//         dataqsiz uint           // 环形队列容量（cap）
//         buf      unsafe.Pointer // 环形队列的底层数组
//         sendx    uint           // 下一次发送写入的下标（环上）
//         recvx    uint           // 下一次接收读取的下标（环上）
//         recvq    waitq          // 被阻塞的接收方队列
//         sendq    waitq          // 被阻塞的发送方队列
//         lock     mutex          // 保护以上所有字段的锁
//     }
//
// 真实 go 代码里，外部观察 channel 状态的两个官方入口就是 len() 和 cap()，
// 与这里的 qcount / dataqsiz 一一对应。本程序演示：
//   1. hchan 结构 + 环形队列的 sendx/recvx 推进
//   2. 缓冲区满：发送阻塞；缓冲区空：接收阻塞
//   3. 环的"回绕"：sendx 越过尾端后复用前面的槽位
//   4. close 之后：发送 panic、接收返回零值
//   5. 无缓冲 channel：收发直接交接（rendezvous），不经过缓存
// =============================================================================

// hchan 模拟 channel 的底层结构（使用 ring 环形队列）
type hchan struct {
	qcount    uint  // 队列中元素个数（对应 len(ch)）
	dataqsiz  uint  // 环形队列容量（对应 cap(ch)）
	buf       []int // 环形队列底层数组（简化：元素为 int）
	sendx     uint  // 发送游标：下一个写入环的下标（递增不回绕）
	recvx     uint  // 接收游标：下一个读取环的下标（递增不回绕）
	closed    bool  // 是否已 close
	blocked   uint  // 被阻塞方数量（发送或接收）——简化"等待队列"
	pending   int   // 交接缓冲：无缓冲/队空时直接递过来的值
	pendingOk bool  // 上面那个值是否有效
}

// mask 环容量是 2 的幂时，用 & 而不是 %（与运行时一致：桶/环都用掩码）
func (ch *hchan) mask(n uint) uint { return n & (ch.dataqsiz - 1) }

// empty / full 判定
func (ch *hchan) empty() bool { return ch.qcount == 0 }
func (ch *hchan) full() bool  { return ch.qcount == ch.dataqsiz }

// newChan 新建一个容量为 size 的 channel 模拟（size=0 表示无缓冲）
func newChan(size uint) *hchan {
	return &hchan{dataqsiz: size, buf: make([]int, size)}
}

// send 模拟发送：能进环就进环，环满则阻塞（blocked++）
func (ch *hchan) send(v int) {
	if !*quiet {
		fmt.Printf("  发送 %-3d", v)
	}
	if ch.closed {
		if !*quiet {
			fmt.Println("  → panic: send on closed channel！")
		}
		return
	}
	// 无缓冲 or 队空且有接收方等待 → 直接交接，跳过环
	if (ch.dataqsiz == 0 || ch.qcount == 0) && ch.blocked > 0 {
		if !*quiet {
			fmt.Printf("  → qcount=0 且有接收方等待，直接交接（不占环）\n")
		}
		ch.pending, ch.pendingOk = v, true
		ch.blocked--
		return
	}
	if ch.dataqsiz == 0 {
		if !*quiet {
			fmt.Println("  → 无缓冲且暂无接收方，发送方阻塞等待")
		}
		ch.pending, ch.pendingOk = v, true
		ch.blocked++
		return
	}
	if ch.full() {
		if !*quiet {
			fmt.Println("  → 环满，发送方阻塞（等待接收方腾出槽位）")
		}
		ch.pending, ch.pendingOk = v, true
		ch.blocked++
		return
	}
	// 写入环
	idx := ch.mask(ch.sendx)
	ch.buf[idx] = v
	ch.sendx++
	ch.qcount++
	if !*quiet {
		fmt.Printf("  → 写入环槽%d，qcount=%d  sendx=%d↗\n", idx, ch.qcount, ch.sendx)
	}
}

// recv 模拟接收：能读就读，环空则阻塞
func (ch *hchan) recv() (int, bool) {
	if !*quiet {
		fmt.Printf("  接收")
	}
	if ch.closed && ch.empty() {
		if !*quiet {
			fmt.Println("  → channel 已关闭且无数据，返回零值 0, ok=false")
		}
		return 0, false
	}
	if ch.empty() {
		if !*quiet {
			fmt.Println("  → 环空：若无发送方则阻塞；有则直接交接")
		}
		if ch.blocked > 0 {
			v, ok := 0, false
			if ch.pendingOk {
				v, ok = ch.pending, true
				ch.pendingOk = false
			}
			if !*quiet {
				fmt.Printf("  → 与等待中的发送方直接交接（不占环）得到 %d\n", v)
			}
			ch.blocked--
			return v, ok
		}
		if !*quiet {
			fmt.Println("  → 环空且无发送方，接收方阻塞")
		}
		ch.blocked++
		return 0, false
	}
	idx := ch.mask(ch.recvx)
	v := ch.buf[idx]
	ch.recvx++
	ch.qcount--
	if !*quiet {
		fmt.Printf("  → 读环槽%d 得到 %d，qcount=%d  recvx=%d↗\n", idx, v, ch.qcount, ch.recvx)
	}
	return v, true
}

// closeCh 模拟关闭：置 closed；之后 send panic、recv 取完数据后返回零值
func (ch *hchan) closeCh() {
	if ch.closed {
		return
	}
	ch.closed = true
	if !*quiet {
		fmt.Println("  关闭 channel → closed=true；buf 里残留的数据仍可读出")
	}
}

func main() {
	flag.Parse()
	if *quiet {
		fmt.Println(channelJSON())
		return
	}

	fmt.Println("========== 1. hchan 结构 ==========")
	fmt.Println("channel 底层是一个带锁的环形队列（ring buffer）。")
	fmt.Println("核心 6 个字段：qcount/cap/sendx/recvx/两个等待队列。")
	fmt.Println("外部能看到的 len(ch)/cap(ch) 就是 qcount/dataqsiz。")

	fmt.Println("\n========== 2. 有缓冲 channel 的环形队列 ==========")
	ch := newChan(4) // cap=4
	fmt.Printf("新建 cap=4 的空 channel：qcount=%d sendx=%d recvx=%d\n",
		ch.qcount, ch.sendx, ch.recvx)
	snap(ch, "初始空环", "")

	fmt.Println("\n-- 连续发送 3 个元素 --")
	ch.send(10)
	ch.send(20)
	ch.send(30)
	snap(ch, "发送 {10,20,30} 后", "")

	fmt.Println("\n-- 再发第 4 个填满，第 5 个阻塞 --")
	ch.send(40)
	ch.send(50) // 满 → 阻塞
	snap(ch, "发送 {10,20,30,40} 后（队满）", "send(50) 被阻塞")

	fmt.Println("\n========== 3. 接收：recvx 前进 + 槽位复用 ==========")
	ch.recv() // 取走槽0
	ch.recv() // 取走槽1
	snap(ch, "接收 2 个后", "recvx 已到 2")

	fmt.Println("\n-- 环的空槽被新发送者复用（sendx 回绕到前面）--")
	ch.send(500)
	ch.send(600)
	snap(ch, "发送 {500,600} 后", "sendx 回绕 4→5，落到槽0/槽1")

	fmt.Println("\n========== 4. close 之后的语义 ==========")
	ch.closeCh()
	for i := 0; i < 5 && !ch.empty(); i++ {
		ch.recv()
	}
	snap(ch, "close 后把残留数据读完", "")
	ch.recv()  // closed 且空 → ok=false
	ch.send(5) // closed → panic

	fmt.Println("\n========== 5. 无缓冲 channel：直接交接 ==========")
	fmt.Println("cap=0 没有环。发送方与接收方必须配对出现，数据直接递过去：")
	ub := newChan(0)
	ub.send(42) // 无接收方 → 阻塞
	ub.recv()   // 与等待的发送方交接
	snap(ub, "cap=0 无环", "sendx/recvx 永远为 0")

	fmt.Println("\n========== 6. 真实 Go channel 对照实验 ==========")
	fmt.Println("真实 runtime：len()=qcount、cap()=dataqsiz，行为应与上面模拟一致。")
	realCh := make(chan int, 2)
	realCh <- 1
	realCh <- 2
	fmt.Printf("  真实 buffered chan: len=%d cap=%d（缓冲区已满）\n", len(realCh), cap(realCh))
	<-realCh
	fmt.Printf("  recv 一个后: len=%d（qcount 减 1，槽位可复用）\n", len(realCh))

	fmt.Println("\n-- 无缓冲 channel 的握手（rendezvous）--")
	done := make(chan bool)
	start := time.Now()
	go func() {
		got := <-realCh // 从满的缓冲读出
		fmt.Printf("    消费者拿到: %d\n", got)
		done <- true
	}()
	time.Sleep(100 * time.Millisecond)
	fmt.Println("    主协程等待消费者…（无缓冲发送会阻塞到接收方就绪）")
	// 再发一个（缓冲此刻空了一个），演示阻塞语义
	realCh <- 3
	<-done
	fmt.Printf("  总计耗时 %v：发送在等待接收方时被阻塞了\n", time.Since(start).Round(time.Millisecond))

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. channel 底层 = 带锁环形队列（hchan）：qcount/cap/sendx/recvx")
	fmt.Println("2. 发送：环没满就写 sendx 位置再前进；满了就阻塞进 sendq")
	fmt.Println("3. 接收：环没空就读 recvx 位置再前进；空了就阻塞进 recvq")
	fmt.Println("4. sendx/recvx 只增不回绕，靠 &(size-1) 映射到环下标 → 槽位复用")
	fmt.Println("5. 无缓冲 channel = 容量 0 的环：收发必须同时出现，直接交接")
	fmt.Println("6. close 后：send panic、recv 读完残留后返回零值")
}

// =============================================================================
// JSON 状态快照：供 Python viz.py 生成可视化 PNG（-json 标志）
// =============================================================================

// ringState 一次环状态的快照
type ringState struct {
	Cap      uint   `json:"cap"`
	Qcount   uint   `json:"qcount"`
	Sendx    uint   `json:"sendx"`
	Recvx    uint   `json:"recvx"`
	Blocked  uint   `json:"blocked"`
	Occupied []bool `json:"occupied"`
	Values   []int  `json:"values"`
	Caption  string `json:"caption"`
	Note     string `json:"note"`
}

// states 记录整个演示过程中的一系列环状态
var states []ringState

// snap 记录当前环状态快照（在 main 的演示过程中被调用）
func snap(ch *hchan, caption, note string) {
	if !*quiet {
		return // 文本模式不记录
	}
	occ := make([]bool, ch.dataqsiz)
	for k := uint(0); k < ch.qcount; k++ {
		occ[ch.mask(ch.recvx+k)] = true
	}
	states = append(states, ringState{
		Cap:      ch.dataqsiz,
		Qcount:   ch.qcount,
		Sendx:    ch.sendx,
		Recvx:    ch.recvx,
		Blocked:  ch.blocked,
		Occupied: occ,
		Values:   append([]int{}, ch.buf...),
		Caption:  caption,
		Note:     note,
	})
}

// channelJSON 运行演示并输出所有环状态快照的 JSON
func channelJSON() string {
	ch := newChan(4)
	snap(ch, "初始空环", "")
	ch.send(10)
	ch.send(20)
	ch.send(30)
	snap(ch, "发送 {10,20,30} 后", "")
	ch.send(40)
	ch.send(50) // 满 → 阻塞
	snap(ch, "发送 {10,20,30,40} 后（队满）", "send(50) 被阻塞")
	ch.recv()
	ch.recv()
	snap(ch, "接收 2 个后", "recvx 已到 2")
	ch.send(500)
	ch.send(600)
	snap(ch, "发送 {500,600} 后", "sendx 回绕 4→5，落到槽0/槽1")
	ch.closeCh()
	for i := 0; i < 5 && !ch.empty(); i++ {
		ch.recv()
	}
	snap(ch, "close 后把残留数据读完", "")
	ch.recv()  // closed 且空
	ch.send(5) // closed → panic

	ub := newChan(0)
	ub.send(42)
	ub.recv()
	snap(ub, "cap=0 无环", "sendx/recvx 永远为 0")

	b, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}
