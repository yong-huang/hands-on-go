package main

// 13_buffered_chan：有缓冲 Channel——异步队列
//
// 无缓冲 channel 是"同步握手"：发送方必须等到接收方伸手。
// 有缓冲 channel 则像一个容量固定的传送带：没满就能塞了就走，满了发送方才被卡住。
// 本程序演示缓冲 3 的 channel 的完整生命周期：
//   1. 未满可发：前 3 条瞬间塞进缓冲，发送方不等接收方
//   2. 满则阻塞：第 4 条起"尝试发送"与"已发送"之间出现明显停顿
//   3. close + for range：发送方关闭后，消费者把缓冲里剩余的排空才退出
//
// 运行：go run main.go

import (
	"fmt"
	"time"
)

func main() {
	ch := make(chan int, 3) // 缓冲容量 3 的 int 队列
	fmt.Printf("创建 channel：cap(ch)=%d len(ch)=%d\n", cap(ch), len(ch))

	// 生产者：发 10 条。每条发送前打印"尝试发送"，成功后打印"已发送"。
	// 两条打印之间的时间差，就是这次 send 实际花掉的时间（0 = 进了缓冲，>0 = 阻塞等消费者）。
	go func() {
		for i := 1; i <= 10; i++ {
			before := time.Now()
			fmt.Printf("[生产者] 尝试发送 %2d（此刻缓冲占用 len=%d/%d）\n", i, len(ch), cap(ch))
			ch <- i // 缓冲未满立刻返回；满了在这里阻塞，直到消费者取走一个
			waited := time.Since(before).Round(time.Millisecond)
			note := "进缓冲，无等待"
			if waited > 0 {
				note = fmt.Sprintf("缓冲满，阻塞了 %v", waited)
			}
			fmt.Printf("[生产者] 已发送   %2d（%s）\n", i, note)
			time.Sleep(20 * time.Millisecond) // 放慢生产节奏，让输出可读
		}
		close(ch) // 发完关闭：不是"清空"，只是宣告"再也没有新消息了"
		fmt.Println("[生产者] close(ch)：已关闭，消费者排空后 for range 会自然退出")
	}()

	// 消费者：先睡 120ms，故意让生产者把缓冲塞满并阻塞一次，再开始读。
	time.Sleep(120 * time.Millisecond)
	received := 0
	for v := range ch { // close 且缓冲排空后循环自动结束
		received++
		fmt.Printf("[消费者] 收到 %2d（此刻缓冲剩余 len=%d/%d）\n", v, len(ch), cap(ch))
		time.Sleep(30 * time.Millisecond)
	}

	// 关闭后的额外语义：立即返回零值 + ok=false，不会阻塞
	v, ok := <-ch
	fmt.Printf("关闭后再读：v=%d ok=%v（零值兜底，不阻塞）\n", v, ok)

	fmt.Printf("对账：发完 10 条、收到 %d 条，len(ch)=%d\n", received, len(ch))
}
