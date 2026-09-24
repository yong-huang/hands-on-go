package main

// 16_chan_close：channel 关闭语义
//
// close 做三件事：告诉接收方"不会再有新值"、唤醒所有等待者、让 for range 退出。
// 本程序演示四件事：
//   1. 关闭后读取：v := <-ch 得零值；v, ok := <-ch 得 ok=false
//   2. for range 在 close 且缓冲排空后自动退出
//   3. 重复 close 同一个 channel 会 panic（用 defer+recover 捕获演示）
//   4. close 是广播：1 个发送方 close，3 个接收 goroutine 同时收到退出信号
//
// 运行：go run main.go

import (
	"fmt"
	"sync"
)

func main() {
	fmt.Println("========== 1. 关闭后读取：零值 + ok=false ==========")
	ch := make(chan int, 2)
	ch <- 42
	close(ch)     // 缓冲里还有 1 个值，close 不清空缓冲
	v, ok := <-ch // 先读到缓冲里的真值
	fmt.Println("排空前 v, ok =", v, ok, "（缓冲余值照常读出）")
	<-ch            // 把最后一个值读走，channel 彻底排空
	v0 := <-ch      // 关闭且已排空：立即返回，不阻塞
	v1, ok1 := <-ch // 双值形式能区分"真收到了零值"和"没值了"
	fmt.Println("排空后 v0 =", v0, "| v1, ok1 =", v1, ok1, "（ok=false 表示已关闭且排空）")

	fmt.Println("\n========== 2. for range：close + 排空后自动退出 ==========")
	nums := make(chan int, 5)
	for i := 1; i <= 5; i++ {
		nums <- i * i
	}
	close(nums) // 发送方发完即 close，这是 range 能退出的前提
	sum := 0
	for n := range nums { // 读到 1/4/9/16/25 后，channel 关闭且排空 → 循环结束
		sum += n
	}
	fmt.Println("range 收到 1..5 的平方，sum =", sum, "（1+4+9+16+25）")

	fmt.Println("\n========== 3. 重复 close：panic，但可以被 recover ==========")
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("recover 到:", r)
			}
		}()
		once := make(chan struct{})
		close(once)
		close(once) // 二次 close：panic: close of closed channel
		fmt.Println("这行永远不会打印")
	}()
	fmt.Println("程序没崩，继续执行")

	fmt.Println("\n========== 4. close 广播：3 个接收者同时退出 ==========")
	broadcast := make(chan struct{})
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-broadcast // channel 关闭时，所有等待的接收者都被唤醒（零值 struct{}）
			fmt.Printf("worker %d 收到退出信号\n", id)
		}(i)
	}
	close(broadcast) // 一次 close，广播给全部 3 个接收者
	wg.Wait()
	fmt.Println("3 个 worker 全部退出（wg.Wait 无阻塞直接返回）")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 关闭+排空后读取不阻塞：单值形式得零值，双值形式得 ok=false")
	fmt.Println("2. for range 退出条件 = 已 close 且已排空；只 close 不排空会先读完余值")
	fmt.Println("3. 重复 close panic；谁发送谁 close，多发送方要额外同步（如 sync.Once）")
	fmt.Println("4. close 是广播原语：一个 close 唤醒全部等待的接收者")
}
