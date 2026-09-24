// 12 · 无缓冲 channel：发送与接收的"会合"(rendezvous) 同步
//
// 无缓冲 chan 的每次发送都必须等到一个接收方就绪才能完成——两者在时间上
// 重合的瞬间就是一次"握手"。本 demo 用 ping/pong 两个 goroutine 通过
// 两个无缓冲 chan（ball / ack）交替接力，证明执行顺序被 channel 严格编排。
package main

import (
	"fmt"
	"time"
)

const rounds = 10

func main() {
	// ball: pong → ping 的回传令牌；serve: ping → pong 的发球令牌。
	// 两者都无缓冲：任何一次 send 都会阻塞，直到对端恰好在这行 recv 上等待。
	ball := make(chan int)
	serve := make(chan int)
	done := make(chan struct{}) // 无缓冲，仅作结束信号（关闭即广播）

	// pong goroutine：收到 serve 才回球，回球本身也是一次握手。
	go func() {
		for {
			n, ok := <-serve // 阻塞：等 ping 的球到手
			if !ok {
				close(done)
				return
			}
			fmt.Printf("第 %2d 次: pong 收到 → 立即回球 (t=%s)\n", n, time.Now().Format("15:04:05.000"))
			ball <- n // 阻塞：ping 不在 <-ball 上等，这里就走不动
		}
	}()

	start := time.Now()
	for i := 1; i <= rounds; i++ {
		serve <- i // 握手①：ping 发球，阻塞直到 pong 到达 <-serve
		<-ball     // 握手②：main 侧等回球，阻塞直到 pong 执行 ball <- i
	}
	close(serve) // 关闭后 pong 的 `n, ok := <-serve` 得 ok=false，退出并关闭 done
	<-done       // 等 pong 退出，避免 main 抢先结束

	fmt.Printf("完成 %d 次往返，耗时 %s（平均每次握手 ≈ %s）\n",
		rounds, time.Since(start).Round(time.Microsecond),
		time.Since(start).Round(time.Microsecond)/(2*rounds))

	// 对照：有缓冲版本
	//
	// buf := make(chan int, 1)
	// buf <- 42 // 立即返回：值滑进缓冲区，接收方此刻不必存在
	//
	// 缓冲区让"发送"与"接收"在时间上解耦，也就失去了无缓冲版本的
	// 那层"发送方阻塞到接收方就绪"的同步保证——本 demo 的严格
	// ping/pong 交替正是靠这层握手才能成立的。
}

// 验证方法：观察输出的时间戳与序号——顺序恒定，绝不会出现
// 连续两次 ping 而中间没有 pong 的情况，因为每次发送都强制
// 等到了对端的接收。
