// 项目 11：work-stealing 实证——用三个实验区分 runnext / 本地队列 / 全局队列。
//
// 运行：go run ./11_work_stealing/            （三档 GOMAXPROCS 全套实验）
//
//	go run ./11_work_stealing/ -mode=runnext
//	go run ./11_work_stealing/ -mode=queue
//
// 配套采集：GODEBUG=schedtrace=100 go run ./11_work_stealing/ -mode=queue
package main

import (
	"flag"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// sink 接收自旋结果，防止忙等循环被编译器优化掉。
var sink atomic.Uint64

// order 记录任务完成顺序（spawn 时的编号，按完成先后 push）。
var (
	orderMu sync.Mutex
	order   []int
)

func record(id int) {
	orderMu.Lock()
	order = append(order, id)
	orderMu.Unlock()
}

func reset() { orderMu.Lock(); order = nil; orderMu.Unlock() }

func snapshot() []int {
	orderMu.Lock()
	defer orderMu.Unlock()
	return append([]int(nil), order...)
}

func main() {
	mode := flag.String("mode", "all", "all | runnext | queue")
	flag.Parse()

	switch *mode {
	case "runnext":
		expRunnext(10)
	case "queue":
		expQueueOverflow(600, time.Millisecond)
	case "all":
		expOrderTable()
		fmt.Println()
		expRunnext(10)
		fmt.Println()
		expQueueOverflow(600, time.Millisecond)
	}
}

// expOrderTable：12 个短任务，GOMAXPROCS=1/2/8 三档，看完成顺序。
// 在看什么：1 档时最后一个 spawn 的任务最先完成（runnext 插队），其余按编号升序；
// 2/8 档时多 P 并行加偷任务，顺序被打乱——乱序本身就是 steal 的证据。
// 诚实预期：1 档顺序基本可复现；2/8 档的具体乱序每次运行不同。
func expOrderTable() {
	fmt.Println("== 实验 1：12 个短任务在 GOMAXPROCS 三档下的完成顺序 ==")
	for _, procs := range []int{1, 2, 8} {
		runtime.GOMAXPROCS(procs)
		reset()
		runBatch(12, 0)
		got := snapshot()
		fmt.Printf("  GOMAXPROCS=%d  完成顺序=%v\n", procs, got)
	}
	runtime.GOMAXPROCS(runtime.NumCPU())
}

// runBatch 顺序 spawn n 个任务；spawner 在 spawn 完后睡一会，
// 让本地队列先堆满、任务在 spawner 阻塞期间开始被调度。
func runBatch(n int, spinMs int) {
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if spinMs > 0 {
				spin(time.Duration(spinMs) * time.Millisecond)
			}
			record(id)
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // spawner 阻塞，把 P 让给队列里的任务
	wg.Wait()
}

// expRunnext：GOMAXPROCS=1 下，先 spawn A 再 spawn B，随后 spawner 阻塞。
// 预测：B 占着 runnext 插队位，先于排在本地队列里的 A 执行。
// 在看什么：多次重复里 B 先完成的占比。安静运行（无 -race）下应接近全部 BA。
func expRunnext(trials int) {
	seqs := expRunnextSeqs(trials)
	bFirst := 0
	for _, s := range seqs {
		if s[0] == 'B' {
			bFirst++
		}
	}
	fmt.Printf("== 实验 2：runnext 插队（GOMAXPROCS=1，A 先 spawn、B 后 spawn）==\n")
	fmt.Printf("  %d 次实验，B（后 spawn）先完成 %d 次；序列=%v\n", trials, bFirst, seqs)
}

// expRunnextSeqs 是实验本体，返回每轮的完成序列（"BA" 表示 B 先完成）。
// 拆出来供测试复用：-race 下会有 GC/唤醒事件抢占 runnext 槽位，顺序不保证。
func expRunnextSeqs(trials int) []string {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)

	seqs := make([]string, 0, trials)
	for t := 0; t < trials; t++ {
		var mu sync.Mutex
		var seq []string
		done := func(name string) func() {
			return func() {
				mu.Lock()
				seq = append(seq, name)
				mu.Unlock()
			}
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); defer done("A")(); spin(200 * time.Microsecond) }()
		go func() { defer wg.Done(); defer done("B")(); spin(200 * time.Microsecond) }()
		time.Sleep(30 * time.Millisecond) // spawner 让出 P
		wg.Wait()
		mu.Lock()
		seqs = append(seqs, seq[0]+seq[1])
		mu.Unlock()
	}
	return seqs
}

// expQueueOverflow：GOMAXPROCS=1 下一次 spawn n 个任务（每个自旋 spinMs）。
// 本地队列上限 256，装不下的连同本地队列一半被移送全局队列。
// 在看什么：完成顺序的"分段"——本地队列段（编号连续）与全局队列段交错；
// 开 GODEBUG=schedtrace=100 还能看到 runqueue（全局）与 [n]（本地）同时非零。
// 诚实预期：分段点位置随溢出细节变化，以实际输出为准。
func expQueueOverflow(n int, spinDur time.Duration) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)

	fmt.Printf("== 实验 3：本地队列 256 上限，%d 个任务溢出到全局队列 ==\n", n)
	reset()
	runBatch(n, int(spinDur.Milliseconds()))

	got := snapshot()
	// 找出所有"编号跳跃"点：相邻完成任务的编号差大于 1，即换段信号。
	breaks := []int{}
	for i := 1; i < len(got); i++ {
		if got[i]-got[i-1] > 1 || got[i]-got[i-1] < -1 {
			breaks = append(breaks, i)
		}
	}
	fmt.Printf("  完成 %d 个任务；顺序跳变点位置=%v\n", len(got), breaks)
	fmt.Printf("  前 8 个完成=%v，最后 4 个完成=%v\n", got[:8], got[len(got)-4:])
}

func spin(d time.Duration) {
	var s uint64
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		s++
	}
	sink.Add(s)
}
