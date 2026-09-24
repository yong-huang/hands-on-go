// 19_pipeline：三级流水线（Pipeline 模式）
//
// 流水线把计算拆成一串 stage，每个 stage：
//  1. 从上游 channel 读数据；
//  2. 做且只做一件事（转换/聚合）；
//  3. 把结果写到下游 channel，读完上游后 close 下游。
//
// close 沿流水线逐级传播，下游的 for range 才能自然结束——
// 这是流水线不需要倒计数、不需要额外信号也能"收尾"的关键。
package main

import "fmt"

// ---------- stage 1：generate ----------

// generate 产生 1..nums。返回值是单向接收通道 <-chan：
// 调用方只能收不能发，类型系统层面保证 stage 职责边界。
func generate(nums int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out) // 产完即 close，通知下游"没有更多了"
		for i := 1; i <= nums; i++ {
			fmt.Printf("[stage1] generate 产出: %d\n", i)
			out <- i
		}
	}()
	return out
}

// ---------- stage 2：square ----------

// square 从 in 逐个读入，平方后发给 out。
// for range 一个 channel：通道被 close 且排空后循环自动退出，
// 因此这里不需要知道上游一共发多少个。
// 为了演示"数据形态变化"，每产出一个值就打印一行。
func square(in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out) // in 排空 → 循环结束 → close(out)，close 向下游传播
		for v := range in {
			sq := v * v
			fmt.Printf("[stage2] square 收到 %d → 产出 %d\n", v, sq)
			out <- sq
		}
	}()
	return out
}

// ---------- stage 3：sum ----------

// sum 聚合阶段：把上游所有值累加，最终结果（而非逐个元素）
// 经 out 发出。流水线末端把"元素流"折叠成"单个结果"。
func sum(in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		total := 0
		for v := range in {
			total += v
		}
		out <- total // 先发结果再 close，顺序不能反
		close(out)
	}()
	return out
}

func main() {
	fmt.Println("========== 三级流水线：generate → square → sum ==========")

	// 接线：每个 stage 的输出直接作为下一个 stage 的输入。
	// 返回类型是 <-chan，赋给变量后只能收，数据流向一目了然。
	nums := generate(10)
	squares := square(nums)
	results := sum(squares)

	// 三个 stage 各自的 goroutine 已经在并发运行，数据逐个流过；
	// stage1/stage2 的产出记录由各 stage 内部打印（顺序可能交错）。

	// 主 goroutine 从末端收结果：sum 要等 squares 全部处理完，
	// 所以这次接收天然是整条流水线的同步点。
	total := <-results
	fmt.Printf("[stage3] sum: 聚合完成，总和 = %d\n", total)
	fmt.Printf("最终结果: 1²+2²+...+10² = %d\n", total)

	fmt.Println("========== 收尾：close 沿 stage 逐级传播，无需计数信号 ==========")
}
