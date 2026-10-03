// 项目 20：goleak 泄漏防护测试基建——把 goroutine 快照 diff 挂成测试门禁。
//
// goleak 的三步原理：
//  1. 快照：runtime.Stack(buf, true) 抓取当前全部 goroutine 的调用栈；
//  2. 过滤：默认放行四类已知栈——testing 框架、CGo syscall、os/signal、
//     runtime.ReadTrace（v1.3.0 options.go 的四个默认过滤器）；
//  3. 比对：过滤后仍有剩余，即"可疑泄漏"，报错并附完整调用栈。
//
// VerifyTestMain 把第 3 步挂在 m.Run() 之后：任何一个测试漏收的
// goroutine 都会在包级别被抓红——测试即门禁（gate）。
//
// 运行方式：go run main.go。goroutine 计数为实测值，基线每次不同，
// 差值是固定的。
package main

import (
	"context"
	"fmt"
	"runtime"
	"time"
)

// leakyWorker：泄漏版后台 worker 的本体。它永远阻塞在无人关闭的
// stop 通道上——"起了但没地方收"的最小形态。命名函数而非匿名闭包，
// 是为了让 goleak.IgnoreAnyFunction 能用稳定的名字豁免它。
func leakyWorker(stop <-chan struct{}) {
	<-stop // 无人 close：goroutine 永久滞留
}

// StartLeakyWorker：启动泄漏版 worker。返回后调用方对它无任何句柄。
func StartLeakyWorker() {
	stop := make(chan struct{})
	go leakyWorker(stop)
}

// StartWorker：修复版。退出权交给调用方的 ctx，cancel 即收工——
// 这就是 18_mini_context 的 cancel 树在生命周期管理里的位置。
func StartWorker(ctx context.Context) {
	go func() {
		<-ctx.Done() // cancel 后立即退出，不残留
	}()
}

func main() {
	fmt.Println("== 第 1 步：goleak 三步原理 ==")
	fmt.Println("快照(runtime.Stack 全量抓栈) → 过滤(testing/syscall/signal/trace 白名单) → 比对(剩余即泄漏)。")
	fmt.Println("VerifyTestMain 把比对挂在 m.Run() 之后：任何测试漏收的 goroutine 都在包级被抓红。")
	fmt.Println()

	// 第 2 步：制造一个泄漏，观察 goroutine 计数有去无回。
	fmt.Println("== 第 2 步：泄漏版 worker——起了没地方收 ==")
	base := runtime.NumGoroutine()
	StartLeakyWorker()
	waitGrew(base)
	fmt.Printf("NumGoroutine：%d → %d（+1，且没有任何句柄能收回它）\n",
		base, runtime.NumGoroutine())
	fmt.Println()

	// 第 3 步：修复版——退出权交给 ctx，cancel 后计数回落。
	fmt.Println("== 第 3 步：修复版 worker——退出权交给 ctx ==")
	base2 := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	StartWorker(ctx)
	waitGrew(base2)
	running := runtime.NumGoroutine()
	cancel()
	waitBack(base2)
	fmt.Printf("NumGoroutine：%d → %d → cancel 后回到 %d（基线每次不同，回落是固定的）\n",
		base2, running, runtime.NumGoroutine())
	fmt.Println()

	// 第 4 步：门禁的三条命令（输出入档见 README）。
	fmt.Println("== 第 4 步：把门禁挂上——三条命令 ==")
	fmt.Println("抓红演示：GOLEAK_RED=1 go test -run TestVerifyNoneCatchesLeak ./20_goleak_gate/")
	fmt.Println("误报演示：GOLEAK_RED=1 go test -run TestFalsePositiveAndIgnore ./20_goleak_gate/")
	fmt.Println("门禁常规：go test -race ./20_goleak_gate/   （TestMain 已挂 VerifyTestMain）")
}

// waitGrew：等计数涨到 base+1（泄漏 goroutine 到达阻塞点）。
func waitGrew(base int) {
	for i := 0; i < 2000 && runtime.NumGoroutine() <= base; i++ {
		time.Sleep(time.Millisecond)
	}
}

// waitBack：等计数回落到 base（退出完成）。
func waitBack(base int) {
	for i := 0; i < 2000 && runtime.NumGoroutine() > base; i++ {
		time.Sleep(time.Millisecond)
	}
}
