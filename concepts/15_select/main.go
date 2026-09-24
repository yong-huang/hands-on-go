// 15 · select 多路复用：一个语句同时等待多个 channel 操作
package main

import (
	"fmt"
	"time"
)

// 场景一：select 监听两个 channel，观察"随机选择就绪分支"
func fanInDemo() {
	ch1 := make(chan string)
	ch2 := make(chan string)

	// 两个 goroutine 各自发 2 条消息，节奏几乎相同：
	// 两个分支经常同时就绪，select 每次用伪随机公平地挑一个。
	go func() {
		for i := 1; i <= 2; i++ {
			ch1 <- fmt.Sprintf("ch1 消息 #%d", i)
		}
	}()
	go func() {
		for i := 1; i <= 2; i++ {
			ch2 <- fmt.Sprintf("ch2 消息 #%d", i)
		}
	}()

	received := map[string]int{"ch1": 0, "ch2": 0}
	for i := 0; i < 4; i++ {
		// 两个 channel 就绪与否由调度决定；
		// 若同时就绪，select 均匀随机选择，不偏向书写顺序。
		select {
		case msg := <-ch1:
			received["ch1"]++
			fmt.Printf("  第 %d 次 select → 走 ch1 分支，收到 %q\n", i+1, msg)
		case msg := <-ch2:
			received["ch2"]++
			fmt.Printf("  第 %d 次 select → 走 ch2 分支，收到 %q\n", i+1, msg)
		}
	}
	fmt.Printf("  分支命中统计：ch1=%d 次，ch2=%d 次\n", received["ch1"], received["ch2"])
}

// 场景二：select + time.After 超时，避免无限等待
func timeoutDemo() {
	slow := make(chan string)
	go func() {
		time.Sleep(1 * time.Second) // 故意太慢，等不到
		slow <- "迟到的数据"
	}()

	fmt.Println("等待 slow channel（最多 500ms）……")
	select {
	case msg := <-slow:
		fmt.Println("  收到：", msg)
	case <-time.After(500 * time.Millisecond):
		fmt.Println("  500ms 超时！放弃等待，走超时分支")
	}
}

// 场景三：default 分支 = 非阻塞探测
func defaultDemo() {
	empty := make(chan int) // 无缓冲且没人发 → 此刻必然没数据

	select {
	case v := <-empty:
		fmt.Println("  意外收到", v)
	default:
		// 有 default 时 select 永不阻塞：所有分支没就绪就立刻走这里
		fmt.Println("  没数据，干别的去了")
	}
}

func main() {
	fmt.Println("== 场景一：select 监听 ch1/ch2，分支随机 ==")
	fanInDemo()

	fmt.Println("== 场景二：select + time.After 超时 ==")
	timeoutDemo()

	fmt.Println("== 场景三：default 非阻塞探测 ==")
	defaultDemo()
}
