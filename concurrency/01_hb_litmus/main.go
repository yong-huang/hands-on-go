// 项目 1：happens-before 探针——用 litmus 测试实证重排
//
// litmus 测试（判定内存模型是否允许某个交错结果的最小双线程程序）：
// 只有两三个 goroutine、两三个变量，结果要么允许要么禁止，没有灰色地带。
//
// 运行方式：
//
//	go run .       // 普通运行：观察"重排被看见"的异常计数
//	go run -race . // 第 2、3 步会打印 WARNING: DATA RACE（预期行为）
//
// 注意必须用 `go run .`（包模式）：`go run main.go` 只编译单个文件，
// 不包含按 build tag 拆分的 race_on.go/race_off.go，会报 undefined: raceEnabled。
//
// 输出分两类：标注（固定）的行每次运行相同；标注（每次不同）的行
// 依赖机器核数与调度时机，数字只看量级，不要逐次对比。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

// 普通运行给足迭代量让异常稳定出现；-race 下每次内存访问都慢一个量级以上，按比例缩小。
var (
	writerIters = 8_000_000 // 第 2 步 writer 的写循环次数（每次不同）
	sbRounds    = 300_000   // 第 3 步 store buffer litmus 的轮数（每次不同）
	fixedRounds = 200_000   // 第 4、5 步修复版的轮数（结果固定为 0）
	readerProcs = 8         // 第 2 步参与"读检查"的 goroutine 数
)

func init() {
	if raceEnabled {
		writerIters /= 10
		sbRounds /= 10
		fixedRounds /= 20
	}
}

func main() {
	fmt.Println("== 第 1 步：三个 litmus 测试在测什么 ==")
	fmt.Println("MP（message passing，消息传递）：T1 写 data 再写 flag；T2 读到 flag 新值后读 data。")
	fmt.Println("SB（store buffer，写缓冲）    ：两个 goroutine 各写各的 flag，再读对方的 flag。")
	fmt.Println("问的问题只有一个：这两段程序允许出现\"违背写入顺序的观测\"吗？")
	fmt.Println()

	fmt.Println("== 第 2 步：MP 无同步版——不建立任何 happens-before 边 ==")
	n := mpUnsync(writerIters)
	// 每次不同：异常次数从几千到几万不等，取决于核数与调度；偶尔为 0 也不代表没有重排。
	fmt.Printf("异常（看到 flag 新值但 data 旧值）：%d 次（每次不同）\n\n", n)

	fmt.Println("== 第 3 步：SB 无同步版——双方各写各的 flag，再读对方的 ==")
	d := sbUnsync(sbRounds)
	// 每次不同：同一轮里双方都读到对方旧值，就是写缓冲延迟可见性的直接证据。
	fmt.Printf("双重旧值轮数：%d / %d 轮（每次不同，可能为 0）\n\n", d, sbRounds)

	fmt.Println("== 第 4 步：MP channel 修复版——发送/接收建立 happens-before 边 ==")
	fmt.Printf("异常：%d 次（固定，0 才是验收线）\n\n", mpChannel(fixedRounds))

	fmt.Println("== 第 5 步：MP atomic 修复版——atomic 写/读配对建立边 ==")
	fmt.Printf("异常：%d 次（固定，0 才是验收线）\n", mpAtomic(fixedRounds))

	fmt.Printf("\n（本机 %d 个逻辑核；go run -race . 时第 2、3 步会报 DATA RACE）\n", runtime.NumCPU())
}

// mpUnsync：无同步的 MP litmus。返回观测到"flag 新、data 旧"的次数。
// data/flag 故意用普通 int32——竞态本身就是实验对象。
// reader 必须先读 flag 再读 data：MP 的争用点在"用第一个读触发对第二个读的检查"。
func mpUnsync(writerIters int) int64 {
	var data, flag int32
	var anomalies atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // writer：程序顺序是先 data 后 flag
		defer wg.Done()
		for i := 1; i <= writerIters; i++ {
			v := int32(i)
			data = v // 写 1
			flag = v // 写 2：任何 reader 看到的 flag 都不允许超前 data
		}
	}()
	for r := 0; r < readerProcs; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writerIters/readerProcs; i++ {
				f := flag  // 读 1（触发检查的读）
				d := data  // 读 2
				if f > d { // flag 比 data 新 => 写 2 的效果先于写 1 可见 => 重排被看见
					anomalies.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	return anomalies.Load()
}

// sbUnsync：无同步的 SB litmus。轮号单调递增代替每轮清零，两份 bool 数组各自只被
// 一方写入、wg.Wait 之后才被读，判定本身不引入额外的同步边。
//
// 为什么"同一轮双方都读到旧值"是重排而不是调度延迟：
// A 记录 sawOldA[r] 要求 A 已执行本轮写 a=r；B 记录 sawOldB[r] 要求 B 已执行写 b=r。
// 两条写在同一轮都已执行、两次读却都没看见对方 => 可见性被写缓冲（store buffer）
// 延迟了，而不是"对方还没写"。
func sbUnsync(rounds int) int64 {
	var a, b int32
	sawOldA := make([]bool, rounds+1)
	sawOldB := make([]bool, rounds+1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // A：写 a，读 b
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			a = v              // 写自己这轮的 flag
			sawOldA[r] = b < v // 读对方：小于本轮 => 对方本轮的写还没可见
		}
	}()
	go func() { // B：写 b，读 a
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			b = v
			sawOldB[r] = a < v
		}
	}()
	wg.Wait()
	both := int64(0)
	for r := 1; r <= rounds; r++ {
		if sawOldA[r] && sawOldB[r] {
			both++
		}
	}
	return both
}

// mpChannel：channel 修复版。发送前的所有写，对完成接收的一方可见——
// 这就是"channel 发送 happens-before 对应接收完成"这条边在干活。
// ack 通道是必要的：若 writer 不等读者读完本轮就写下一轮的 data，
// "写 data(i+1)" 与 "读 data(i)" 之间没有任何边，会构成真竞态
// （-race 会如实报出——这是本次开发中真实踩到并被 -race 抓到的 bug）。
func mpChannel(rounds int) int64 {
	var data int32
	flag := make(chan int32)
	ack := make(chan struct{})
	var anomalies atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // writer
		defer wg.Done()
		for i := 1; i <= rounds; i++ {
			v := int32(i)
			data = v  // 写 data（此刻读者已 ack 上一轮，访问有序）
			flag <- v // 发送：把它之前的写"托付"给接收方
			<-ack     // 等读者读完本轮 data，再进入下一轮写
		}
		close(flag)
	}()
	wg.Add(1)
	go func() { // reader
		defer wg.Done()
		for v := range flag { // 接收完成 => 本轮 data=v 的写先于下面这次读
			d := data
			if v > d { // 理论上不可能；发生了就说明同步边失效
				anomalies.Add(1)
			}
			ack <- struct{}{} // 告诉 writer：本轮 data 我读完了
		}
	}()
	wg.Wait()
	return anomalies.Load()
}

// mpAtomic：atomic 修复版。reader 先 atomic 读 flag，后 atomic 读 data；
// 当 flag 的读观测到 flag 的写时，那次写同步先于这次读（Go 的 atomic 是
// 顺序一致语义），再叠加单 goroutine 内的程序顺序，data 的新值必然可见。
func mpAtomic(rounds int) int64 {
	var data, flag atomic.Int32
	var anomalies atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // writer：先写 data 后写 flag，顺序在单 goroutine 内天然成立
		defer wg.Done()
		for i := 1; i <= rounds; i++ {
			v := int32(i)
			data.Store(v)
			flag.Store(v)
		}
	}()
	wg.Add(1)
	go func() { // reader：先读 flag 后读 data
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			f := flag.Load()
			d := data.Load()
			if f > d {
				anomalies.Add(1)
			}
		}
	}()
	wg.Wait()
	return anomalies.Load()
}
