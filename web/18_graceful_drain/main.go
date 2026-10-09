// 实验 18：优雅停机与任务排空——50 个在途任务的零丢失演练。
//
//	第 1 步：灌入 50 个任务（每个 ~20ms），worker 池 4 并发
//	第 2 步：立即触发停机（任务还在跑）——四阶段顺序执行
//	第 3 步：零丢失对账——processed=50，死信=0；停机日志全序可审计
//	第 4 步：超时路径——卡死的任务让排空超时，剩余进死信 + 强制退出日志
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func main() {
	fmt.Println("== 第 1~3 步：50 任务零丢失演练 ==")
	runNormal()

	fmt.Println()
	fmt.Println("== 第 4 步：排空超时——卡死任务进死信，强制退出有日志 ==")
	runTimeout()
}

// runNormal：50 个任务入队后立即停机——drain 等它们全部做完，零丢失。
func runNormal() {
	phaseLog := NewPhaseLog()
	var dbClosed bool
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(ctx context.Context) error { return nil }),
		Ready:     readyTrue(),
		DepClose:  func() error { dbClosed = true; return nil },
		Log:       phaseLog,
		DrainTime: 5 * time.Second,
	}
	orch.Pool = NewDrainablePool(4, 64, func(Job) {
		time.Sleep(20 * time.Millisecond) // 模拟在途工作
	})

	for i := 0; i < 50; i++ {
		orch.Pool.Enqueue(Job{URL: fmt.Sprintf("task-%d", i)})
	}
	// 不等任务跑完，立即停机——排空语义接管（4 并发消化 50 个 ≈ 250ms）

	drained := orch.Shutdown(context.Background())

	processed := orch.Pool.processed.Load()
	fmt.Printf("  50 任务：processed=%d，死信=%d，排空在期限内=%v\n",
		processed, len(orch.Pool.DeadLetters()), drained)
	if processed != 50 || len(orch.Pool.DeadLetters()) != 0 || !drained {
		fatal(fmt.Errorf("零丢失断言失败"))
	}
	if !dbClosed {
		fatal(fmt.Errorf("依赖未关闭"))
	}
	fmt.Println("  零丢失对账通过；停机时序（可审计）：")
	for _, line := range phaseLog.All() {
		fmt.Println("   ", line)
	}
}

// runTimeout：任务卡死 → 排空超时 → 剩余进死信 + 强制退出日志。
func runTimeout() {
	phaseLog := NewPhaseLog()
	block := make(chan struct{})
	orch := &Orchestrator{
		Server:    shutdownerFunc(func(ctx context.Context) error { return nil }),
		Ready:     readyTrue(),
		Log:       phaseLog,
		DrainTime: 300 * time.Millisecond, // 期限压短，演示可见
	}
	orch.Pool = NewDrainablePool(1, 64, func(Job) {
		<-block // 模拟卡死的任务（永不完成）
	})

	for i := 0; i < 5; i++ {
		orch.Pool.Enqueue(Job{URL: fmt.Sprintf("stuck-%d", i)})
	}
	time.Sleep(50 * time.Millisecond) // 第 1 个被 worker 拿走（卡死），4 个在队列

	drained := orch.Shutdown(context.Background())
	if drained {
		fatal(fmt.Errorf("超时路径不应报告成功"))
	}
	dead := orch.Pool.DeadLetters()
	fmt.Printf("  排空超时：死信 %d 条（队列剩余任务），强制退出路径已记录\n", len(dead))
	if len(dead) != 4 {
		fatal(fmt.Errorf("死信 = %d， want 4（worker 手里那个无法强取）", len(dead)))
	}
	close(block) // 松手：卡死的任务结束，goroutine 清零（goleak 友好）
	orch.Pool.wg.Wait()
}

// shutdownerFunc / readyTrue：演示装配的小助手。
type shutdownerFunc func(context.Context) error

func (f shutdownerFunc) Shutdown(ctx context.Context) error { return f(ctx) }

func readyTrue() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}
