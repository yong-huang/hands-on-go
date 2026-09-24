package main

// 20_fan_in_out：扇出（fan-out）与扇入（fan-in）
//
// 扇出：把一个 channel 的数据分给多个 worker 并行消费——
//       Go 的天然语义保证一条消息只会被一个 worker 收到，不会重复处理。
// 扇入：把多个 worker 的结果 channel 合并回一个 channel，
//       关键是 sync.WaitGroup：等全部结果 channel close 之后，再 close 合并 channel。
//
// 数据流：source(1..9) → 3 个 worker 各自平方 → merge → main 汇总（平方和 285）
//
// 运行：go run main.go

import (
	"fmt"
	"sync"
)

// source 生产 1..9，产完即 close——close 是下游 for range 退出的信号。
func source() <-chan int {
	out := make(chan int)
	go func() {
		for i := 1; i <= 9; i++ {
			out <- i
		}
		close(out)
	}()
	return out
}

// worker 从共享的 source channel 消费（fan-out 的关键：3 个 worker 绑定同一个 channel）。
// 一条消息只会被其中一个 worker 的 <-in 接走，各 worker 互不重复。
// 处理完（for range 因 close 退出）后关闭自己的结果 channel。
func worker(id int, in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for v := range in {
			fmt.Printf("worker %d 处理 %d -> %d\n", id, v, v*v)
			out <- v * v
		}
	}()
	return out
}

// merge 扇入：把多个结果 channel 合并为一个。
// 每个 channel 配一个 goroutine 转发，wg.Done 在转发完（即该 channel close）后调用；
// wg.Wait() 返回说明所有输入都已排空，此时才安全 close(merged)——
// 谁生产谁 close，merge 是 merged 的唯一生产者，close 权归它。
func merge(ins ...<-chan int) <-chan int {
	merged := make(chan int)
	var wg sync.WaitGroup
	wg.Add(len(ins))
	for _, in := range ins {
		go func(c <-chan int) {
			defer wg.Done()
			for v := range c {
				merged <- v
			}
		}(in)
	}
	go func() {
		wg.Wait()
		close(merged)
	}()
	return merged
}

func main() {
	src := source()

	// fan-out：3 个 worker 共享消费同一个 source
	workers := make([]<-chan int, 0, 3)
	for id := 1; id <= 3; id++ {
		workers = append(workers, worker(id, src))
	}

	// fan-in：合流后统一消费
	merged := merge(workers...)

	sum := 0
	count := 0
	for v := range merged {
		sum += v
		count++
	}
	fmt.Printf("共收到 %d 个平方值，总和 = %d（= 1²+2²+…+9²）\n", count, sum)
}
