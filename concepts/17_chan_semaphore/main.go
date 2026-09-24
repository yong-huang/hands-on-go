package main

// 17_chan_semaphore：用 channel 当信号量做限流
//
// 经典的"令牌桶"限流器，全部用语言内置件搭成，不依赖任何库：
//   - 容量为 N 的 buffered channel 就是信号量：往里放令牌=发送，取令牌=接收
//   - 一个独立 goroutine 用 time.Ticker 每 200ms 放一个令牌（恒速 5 个/秒）
//   - 15 个请求 goroutine 各自 <-tokens，取到令牌才有资格执行业务
//
// 你会看到的现象：
//   - 开头 0ms 附近一口气放行约 5 个（桶里预存的突发余量）
//   - 之后每 ~200ms 放行 1 个，节奏被令牌严格锁死
//
// 运行：go run main.go（总时长约 3 秒）

import (
	"fmt"
	"sync"
	"time"
)

const (
	burst    = 5                      // 令牌桶容量：允许的瞬时并发（突发上限）
	rate     = 200 * time.Millisecond // 补令牌周期：5 个/秒
	requests = 15                     // 模拟的请求总数
)

// startRefiller 启动一个独立 goroutine，用 Ticker 恒速往桶里放令牌。
// 返回 stop 函数：先停 Ticker 再 close 通道，收尾顺序不能反——
// 若先 close，refiller 再发送就是对已关闭通道发送，直接 panic。
func startRefiller(tokens chan struct{}) (stop func()) {
	ticker := time.NewTicker(rate)
	stopCh := make(chan struct{}) // 关闭信号（由 stop 关，只关一次）
	done := make(chan struct{})   // 退出确认（由 refiller 关）
	go func() {
		defer close(done)
		for {
			select {
			case <-ticker.C:
				select {
				case tokens <- struct{}{}: // 桶满则丢弃这个令牌（等价于容量上限）
				default:
				}
			case <-stopCh:
				return
			}
		}
	}()
	return func() {
		ticker.Stop()
		close(stopCh)
		<-done // 等 refiller 真正退出，之后 close tokens 才安全
		close(tokens)
	}
}

func main() {
	start := time.Now()
	elapsed := func() string {
		return fmt.Sprintf("%6.0fms", float64(time.Since(start).Microseconds())/1000)
	}

	tokens := make(chan struct{}, burst) // 信号量：桶里每个令牌=1 个放行名额
	for i := 0; i < burst; i++ {         // 预存满桶令牌：允许开头一小波突发
		tokens <- struct{}{}
	}
	stop := startRefiller(tokens)

	var wg sync.WaitGroup
	fmt.Println("t=    0ms 启动：桶容量 5，补充速率 5 令牌/秒，共 15 个请求")
	for i := 1; i <= requests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-tokens // 取不到令牌就阻塞在这儿——这就是限流本体，一行代码
			fmt.Printf("请求 %2d 取到令牌，开始执行 @ t=%s\n", id, elapsed())
			time.Sleep(30 * time.Millisecond) // 模拟业务耗时，不影响放行节奏
		}(i)
	}

	wg.Wait() // 15 个请求全部拿到令牌并执行完
	stop()    // 停 Ticker → 等 refiller 退出 → close(tokens)
	fmt.Println("全部请求执行完毕，令牌桶已关闭")
}
