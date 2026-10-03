// 项目 16：select 均匀性与 selectgo 加锁顺序实证。
//
// selectgo（runtime/select.go 里 select 的编译产物）的三个关键行为：
//  1. 多个 case 就绪时随机选一个轮询起点（fastrandn），所以双就绪通道
//     的命中占比长期趋近 50/50；
//  2. 对所有 case 的 channel 按地址排序后统一加锁，把"多路等待"变成
//     一个原子动作——按用户书写顺序逐个加锁会引入 ABBA 死锁；
//  3. 带 default 时没有任何 case 就绪就走 default，不停车——用它做
//     轮询就是忙等，CPU 占用实测见第 4 步。
//
// 运行方式：go run main.go。第 2/4 步的数字每次不同，只看量级与占比。
package main

import (
	"fmt"
	"syscall"
	"time"
)

func main() {
	fmt.Println("== 第 1 步：selectgo 的三个行为 ==")
	fmt.Println("多路就绪随机轮询起点；按 channel 地址排序加锁（原子多路等待）；")
	fmt.Println("default 让 select 变成非阻塞试探。")
	fmt.Println()

	fmt.Println("== 第 2 步：双就绪通道 10^6 次 select 均匀性 ==")
	a, b := uniformity(1_000_000)
	fmt.Printf("通道 A：%d 次（%.2f%%）\n", a, 100*float64(a)/1e6)
	fmt.Printf("通道 B：%d 次（%.2f%%）\n", b, 100*float64(b)/1e6)
	fmt.Printf("偏差 |A-B|/N = %.3f%%（每次不同；验收线 < 5%%）\n",
		100*float64(abs(a-b))/1e6)
	fmt.Println()

	fmt.Println("== 第 3 步：锁序死锁——朴素两步加锁 vs select 的排序加锁 ==")
	fmt.Printf("朴素按书写顺序两步拿两把锁：死锁轮次 = %d/3（详见 naiveLockOrder）\n",
		naiveLockOrder(3))
	crossSelect(10_000)
	fmt.Println("select 交叉收发对：10^4 次交接全部完成，无一卡死（固定）")
	fmt.Println()

	fmt.Println("== 第 4 步：select+default 忙等 vs 阻塞 select 的 CPU 占用 ==")
	busy := busyPollCPU(300 * time.Millisecond)
	block := blockWaitCPU(300 * time.Millisecond)
	fmt.Printf("select+default 空转 300ms：CPU 时间 %v（每次不同）\n", busy)
	fmt.Printf("阻塞 select 等 300ms 超时：CPU 时间 %v（每次不同）\n", block)
	fmt.Println("同一墙上时长，忙等几乎占满一核，阻塞等待几乎为 0（量级结论固定）。")
}

// uniformity：两个缓冲各装 n/2 个令牌的通道，双路恒就绪，select n 次，
// 返回两路命中数。随机轮询起点（fastrandn）决定占比。
func uniformity(n int) (a, b int) {
	half := n / 2
	chA := make(chan int, half)
	chB := make(chan int, half)
	for i := 0; i < half; i++ {
		chA <- 1
		chB <- 1
	}
	for i := 0; i < n; i++ {
		select {
		case <-chA:
			a++
		case <-chB:
			b++
		}
	}
	return a, b
}

// naiveLockOrder：模拟"selectgo 若按用户书写顺序逐个加锁"的灾难场景。
// 两把锁用容量 1 的 channel 表示：g1 先 A 后 B，g2 先 B 后 A——经典
// ABBA 互等。持第一把锁后停 1ms，让两方都拿到第一把再互相卡住。
// 返回观察到死锁的轮次（一方在 50ms 内拿不到第二把锁即记一次）。
func naiveLockOrder(rounds int) int {
	deadlocks := 0
	for r := 0; r < rounds; r++ {
		lockA := make(chan struct{}, 1)
		lockB := make(chan struct{}, 1)
		results := make(chan bool, 2) // true = 拿全了两把锁
		goroutine := func(first, second chan struct{}) {
			select {
			case first <- struct{}{}: // 拿到第一把
			case <-time.After(time.Second):
				results <- false
				return
			}
			time.Sleep(time.Millisecond) // 制造 ABBA 窗口
			select {
			case second <- struct{}{}: // 拿到第二把
				results <- true
			case <-time.After(50 * time.Millisecond): // 互等，超时判死锁
				results <- false
			}
		}
		go goroutine(lockA, lockB)
		go goroutine(lockB, lockA)
		got1, got2 := <-results, <-results
		if !got1 || !got2 {
			deadlocks++
		}
	}
	return deadlocks
}

// crossSelect：两个无缓冲通道交叉收发的 select 对。A 等"发给 aCh 或从
// bCh 收"，B 对称。若存在"锁住 aCh 等 bCh"的窗口，这对 select 会互卡；
// 实际永不发生——selectgo 按地址排序后一次性加锁，多路等待是原子的。
func crossSelect(rounds int) {
	aCh := make(chan struct{})
	bCh := make(chan struct{})
	stop := make(chan struct{})
	worker := func(send, recv chan struct{}) {
		for {
			select { // 三路一起等：不存在"占住 send 等 recv"的中间状态
			case send <- struct{}{}:
			case <-recv:
			case <-stop:
				return
			}
		}
	}
	go worker(aCh, bCh) // A：aCh 发送方 / bCh 接收方
	go worker(bCh, aCh) // B：对称
	n := 0
	for n < rounds {
		select {
		case <-aCh:
			n++
		case <-bCh:
			n++
		}
	}
	close(stop) // 双方在下一轮 select 看到 stop 即退出，不残留
}

// cpuTime：进程累计 CPU 时间（用户态 + 内核态），来自 getrusage。
func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	sec := time.Duration(ru.Utime.Sec+ru.Stime.Sec) * time.Second
	usec := time.Duration(ru.Utime.Usec+ru.Stime.Usec) * time.Microsecond
	return sec + usec
}

// busyPollCPU：select+default 空转 wall 时长，返回消耗的 CPU 时间。
func busyPollCPU(wall time.Duration) time.Duration {
	never := make(chan struct{})
	before := cpuTime()
	start := time.Now()
	for time.Since(start) < wall {
		select {
		case <-never: // 永不就绪
		default: // 立即走 default：空转
		}
	}
	return cpuTime() - before
}

// blockWaitCPU：阻塞 select 等 wall 时长超时，返回消耗的 CPU 时间。
func blockWaitCPU(wall time.Duration) time.Duration {
	never1, never2 := make(chan struct{}), make(chan struct{})
	before := cpuTime()
	select {
	case <-never1:
	case <-never2:
	case <-time.After(wall): // 唯一会触发的分支：runtime 停车，不占 CPU
	}
	return cpuTime() - before
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
