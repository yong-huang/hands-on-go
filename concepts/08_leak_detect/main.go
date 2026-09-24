package main

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// 08_leak_detect：Goroutine 泄漏检测
//
// goroutine 没有父组件等它结束——main 退出前谁也不会替你回收。
// 泄漏的本质只有一句话：goroutine 永远阻塞在一个"再也不会就绪"的操作上。
// 本程序演示三种典型泄漏态及其修复对照，全程用 runtime.NumGoroutine() 观测：
//   1. 发送阻塞：往无人接收的无缓冲 channel 发送
//   2. 接收阻塞：从无人发送的 channel 接收
//   3. 死循环：for {} 缺少退出条件
// 每段流程：记基线 → 制造泄漏 → 等 100ms 观察 +N 不回落 → 立刻跑修复版看回落。

// baseline 返回当前 goroutine 数，并打印基线。
func baseline(stage string) int {
	n := runtime.NumGoroutine()
	fmt.Printf("  基线 goroutine 数: %d\n", n)
	return n
}

// observe 等 100ms 后再次读数，返回 (当前值, 增量)。
func observe(after int) (int, int) {
	time.Sleep(100 * time.Millisecond)
	n := runtime.NumGoroutine()
	return n, n - after
}

// leakSend 泄漏版：发送方阻塞，接收方永远不来。
func leakSend() { ch := make(chan int); ch <- 1 }

// fixedSend 修复版：缓冲为 1，发送即完成，goroutine 正常返回。
func fixedSend() { ch := make(chan int, 1); ch <- 1 }

// leakRecv 泄漏版：接收方阻塞，发送方永远不来。
func leakRecv() { <-make(chan int) }

// fixedRecv 修复版：发送方在函数返回前 close，接收方读到零值后退出。
func fixedRecv(done chan struct{}) {
	ch := make(chan int)
	go func() { defer close(done); close(ch) }() // 由发送方 close
	<-ch                                         // 读到零值 0，不再永久阻塞
}

// leakSpin 泄漏版：for {} 没有任何退出条件，永远占着一个 P 的时间片。
func leakSpin() {
	for {
	}
}

// fixedSpin 修复版：用 ctx.Done() 作为退出条件。
func fixedSpin(ctx context.Context) {
	for {
		select {
		case <-ctx.Done(): // 取消信号到达 → return
			return
		default:
		}
	}
}

func main() {
	total := 0 // 累计各泄漏段的"净残留"（泄漏态增量 - 修复后增量），供末尾汇总

	fmt.Println("========== 1. 发送阻塞：ch <- v 而无人接收 ==========")
	base := baseline("send")
	go leakSend()
	n, leak := observe(base)
	fmt.Printf("  泄漏版 +100ms: %d 个 (+%d 不回落，goroutine 永久阻塞在发送上)\n", n, leak)
	go fixedSend()
	n2, fixed := observe(n)
	fmt.Printf("  修复版 +100ms: %d 个 (+%d，缓冲吸收了那次发送，goroutine 已退出)\n", n2, fixed)
	total += leak - fixed

	fmt.Println("\n========== 2. 接收阻塞：<-ch 而无人发送 ==========")
	base = baseline("recv")
	go leakRecv()
	n, leak = observe(base)
	fmt.Printf("  泄漏版 +100ms: %d 个 (+%d 不回落，goroutine 永久阻塞在接收上)\n", n, leak)
	done := make(chan struct{})
	go fixedRecv(done)
	<-done // 等修复版 goroutine 真正走完 defer
	n2, fixed = observe(n)
	fmt.Printf("  修复版 +100ms: %d 个 (+%d，发送方 close 解除阻塞，数量回落)\n", n2, fixed)
	total += leak - fixed

	fmt.Println("\n========== 3. 死循环：for {} 缺退出条件 ==========")
	base = baseline("spin")
	go leakSpin()
	n, leak = observe(base)
	fmt.Printf("  泄漏版 +100ms: %d 个 (+%d 不回落，死循环永不返回)\n", n, leak)
	ctx, cancel := context.WithCancel(context.Background())
	go fixedSpin(ctx)
	nMid, spinLeak := observe(n)
	fmt.Printf("  修复版启动后: %d 个 (+%d，还在跑——退出条件未触发)\n", nMid, spinLeak)
	cancel() // 触发 ctx.Done，goroutine 从 select 的 Done 分支返回
	<-time.After(100 * time.Millisecond)
	n2 = runtime.NumGoroutine()
	fixed = n2 - n
	fmt.Printf("  cancel +100ms: %d 个 (+%d，ctx 取消让 goroutine 主动退出)\n", n2, fixed)
	total += leak - fixed

	fmt.Println("\n========== 总结 ==========")
	fmt.Printf("1. 发送/接收阻塞的修复思路：给缓冲、或由对端 close/channel 化退出信号\n")
	fmt.Printf("2. 死循环的修复思路：把退出条件交给 ctx.Done()/stop channel，而非裸 for{}\n")
	fmt.Printf("3. 三段净残留合计 = %d（每段泄漏态 +1、修复后归零；服务长跑时这是线性累积的）\n", total)
	fmt.Printf("4. 排查工具：runtime.NumGoroutine() 粗观测，pprof goroutine profile 看阻塞栈\n")
}
