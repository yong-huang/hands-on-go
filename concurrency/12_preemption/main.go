// 项目 12：抢占演进——协作式到信号抢占。
//
// 在 GOMAXPROCS=1 下跑两种循环，后台 goroutine 测自己从 spawn 到首次执行的延迟：
//
//	步骤 1：无函数调用的 tight loop（紧循环）——协作式调度对它无能为力，
//	        go1.14+ 信号抢占把它压回 ~10~20ms；
//	步骤 2：每 128 次迭代带一次函数调用的循环——函数调用就是协作式的让出点，
//	        两个时代都能及时让出，用来对照"1.14 到底补了什么"。
//
// 运行：go run ./12_preemption/
// 对照：GODEBUG=asyncpreemptoff=1 go run ./12_preemption/   （模拟 Go 1.13 前行为）
// 采集：GODEBUG=schedtrace=100 go run ./12_preemption/ -rounds 2
package main

import (
	"flag"
	"fmt"
	"runtime"
	"sort"
	"time"
)

// sink 把循环变量累加起来，防止编译器把无副作用的循环整体删掉。
var sink uint64

func main() {
	rounds := flag.Int("rounds", 5, "测量轮数")
	flag.Parse()

	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)

	fmt.Printf("== GOMAXPROCS=1，主 G 跑长循环，后台 G 报首次执行延迟 ==\n")
	fmt.Printf("go 版本: %s\n", runtime.Version())

	fmt.Printf("\n-- 步骤 1：无函数调用的 tight loop，%d 轮 --\n", *rounds)
	report(measureRounds(tightLoop, *rounds))

	fmt.Printf("\n-- 步骤 2：每 128 次迭代带一次函数调用的循环，%d 轮 --\n", *rounds)
	report(measureRounds(callLoop, *rounds))

	fmt.Println("\n诚实预期：步骤 1（无调用点）约 10~25ms，只能靠信号抢占；")
	fmt.Println("用 GODEBUG=asyncpreemptoff=1 关掉信号抢占后，步骤 1 变成≈整轮循环时长。")
	fmt.Println("步骤 2 自带函数调用点，两种模式下都更快——调用点是信号之外的打断路径，")
	fmt.Println("具体数值以实际输出为准。")
}

func report(delays []time.Duration) {
	sorted := append([]time.Duration(nil), delays...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i, d := range delays {
		fmt.Printf("  第 %d 轮：后台 G 首次执行延迟 = %v\n", i+1, d)
	}
	fmt.Printf("  min=%v median=%v max=%v\n", sorted[0], sorted[len(sorted)/2], sorted[len(sorted)-1])
}

// measureRounds 跑多轮测量，每轮换一种循环体。
func measureRounds(loop func(), rounds int) []time.Duration {
	delays := make([]time.Duration, 0, rounds)
	for r := 0; r < rounds; r++ {
		delays = append(delays, measureOnce(loop))
	}
	return delays
}

// measureOnce：spawn 一个后台 G，随后立刻进入 loop；
// 后台 G 第一次被调度时记录"现在距 spawn 过了多久"。
// 在看什么：这个延迟由"谁来打断长循环"决定——
// 信号抢占：sysmon 发现 G 跑超 10ms，发 SIGURG 打断，延迟约 10~20ms；
// 关闭信号抢占：只有循环自带让出点（如函数调用）才能轮到后台 G。
func measureOnce(loop func()) time.Duration {
	t0 := time.Now()
	ran := make(chan time.Duration, 1)
	go func() {
		ran <- time.Since(t0)
	}()
	loop()
	d := <-ran
	runtime.Gosched() // 让被挤到全局队列的主 G 回来，减少对下一轮的干扰
	return d
}

// tightLoop 是故意的最坏情况：约 3e8 次纯算术迭代，
// 无函数调用、无 channel、无内存写屏障点——协作式调度对它完全无能为力。
func tightLoop() {
	var s uint64
	for i := uint64(0); i < 3e8; i++ {
		s += i
	}
	sink = s
}

// callLoop 每 128 次迭代做一次函数调用。调用点在协作式时代就是安全点，
// 后台 G 在旧版 Go 里也能借此及时运行；信号抢占下它与 tightLoop 表现一致。
func callLoop() {
	var s uint64
	for i := uint64(0); i < 3e8; i += 128 {
		s += i
		noop(&s)
	}
	sink = s
}

//go:noinline
func noop(s *uint64) {
	if *s == 1<<63 {
		fmt.Println(*s)
	}
}
