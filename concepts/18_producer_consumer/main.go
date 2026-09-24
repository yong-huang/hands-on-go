package main

import (
	"fmt"
	"sync"
)

// 18_producer_consumer：多生产者 × 多消费者
//
// 3 个生产者往同一个任务 channel 发任务，2 个消费者 for range 消费。
// 关键在两级 WaitGroup 的优雅关闭：
//   - wgProducers 只管生产者：每个生产者发完 Done
//   - 收尾 goroutine 先 wgProducers.Wait() 再 close(tasks)：
//     close 的前提是"再也不会有人发送"，只有"所有生产者都完成"能证明这一点
//   - wgConsumers 只管消费者：main 等 wgConsumers 后对账
//
// 运行：go run main.go

type Task struct {
	ID   int
	From int // 哪个生产者发的
}

func main() {
	tasks := make(chan Task) // 无缓冲：每次发送与接收都是一次握手

	// ---- 第一级 WaitGroup：管理生产者 ----
	var wgProducers sync.WaitGroup
	const numProducers = 3
	const tasksPerProducer = 4

	for p := 1; p <= numProducers; p++ {
		wgProducers.Add(1)
		go func(producerID int) {
			defer wgProducers.Done() // 发送结束后由 defer 保证 Done
			for i := 0; i < tasksPerProducer; i++ {
				t := Task{ID: producerID*100 + i, From: producerID}
				tasks <- t
				fmt.Printf("[P%d] 发送任务 %d\n", producerID, t.ID)
			}
			fmt.Printf("[P%d] 全部发送完毕，退出\n", producerID)
		}(p) // 循环变量作为参数传入，避免闭包共享
	}

	// ---- 收尾 goroutine：由"第三方"负责 close ----
	// close 是"宣布以后再无发送"。若让某个生产者自己 close，它无法知道
	// 其他生产者是否发完——提前 close 会让别的生产者 panic（向已关闭
	// channel 发送）。等 wgProducers 归零后 close，才满足 close 的前提。
	go func() {
		wgProducers.Wait()
		close(tasks)
		fmt.Println("--- tasks channel 已关闭（所有生产者都完成了）---")
	}()

	// ---- 第二级 WaitGroup：管理消费者 ----
	var wgConsumers sync.WaitGroup
	received := make([]int, numProducers+1) // 按生产者统计收到多少
	var mu sync.Mutex                       // 消费者并发写 received，需要锁
	const numConsumers = 2

	for c := 1; c <= numConsumers; c++ {
		wgConsumers.Add(1)
		go func(consumerID int) {
			defer wgConsumers.Done()
			// for range tasks：channel 被关闭且排空后循环自动结束。
			// 消费者侧绝不能 close：第一，close 只该由发送方触发，
			// 消费者无法知道"还有没有生产者要发"；第二，向已关闭
			// channel 发送会 panic，而消费者连有没有同伴在收都管不着。
			for t := range tasks {
				mu.Lock()
				received[t.From]++
				mu.Unlock()
				fmt.Printf("  [C%d] 处理任务 %d（来自 P%d）\n", consumerID, t.ID, t.From)
			}
			fmt.Printf("  [C%d] channel 已关闭，退出\n", consumerID)
		}(c)
	}

	wgConsumers.Wait() // main 只等消费者：消费者的退出就蕴含了生产者已收尾

	// ---- 对账：发送总数 == 接收总数 ----
	totalSent := numProducers * tasksPerProducer
	totalRecv := 0
	for _, n := range received {
		totalRecv += n
	}
	fmt.Printf("\n对账：发送 %d = 接收 %d？%v\n", totalSent, totalRecv, totalSent == totalRecv)
	for p := 1; p <= numProducers; p++ {
		fmt.Printf("  P%d 发了 %d、被收了 %d\n", p, tasksPerProducer, received[p])
	}
}
