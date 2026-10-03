// 项目 23：有界并发任务框架——本系列 22 个项目的收官综合。
//
// 能力清单（每一项都能在系列里找到出处）：
//
//	有界 worker + 任务队列        → 17_generics_prims 的流水线骨架
//	panic 隔离（panic 转错误）    → 19_errgroup 的失败不炸组
//	取消/超时（协作式）           → 18_mini_context 的 cancel 树 + WithTimeout
//	互斥与原子计数                → 05_mini_mutex / 02_atomic_orders
//	背压（阻塞/拒绝两种策略）     → 22_load_shapes 压测的对象
//	泄漏门禁（测试无残留）        → 20_goleak_gate 的 VerifyNone
//
// 守恒律（框架的核心断言）：Submitted == Succeeded + Failed。
// 任务只有两种结局，计数一旦提交就必然落到其中一边，一个都不会丢。
//
// 运行方式：go run main.go。失败任务的 id 与计数每次运行都不同
// （panic 用随机数决定），总量与守恒关系是固定的。
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- 错误定义 ----------

var (
	// ErrStopped：框架已停机后再提交。
	ErrStopped = errors.New("framework stopped")
	// ErrQueueFull：拒绝策略下队列已满。
	ErrQueueFull = errors.New("queue full")
)

// ---------- 配置与指标 ----------

// Strategy：背压策略——队列满时 Submit 怎么办。
type Strategy int

const (
	// Block：阻塞调用方，直到有空位（天然背压，消费端反压生产端）。
	Block Strategy = iota
	// Reject：立即返回 ErrQueueFull（调用方自己决定降级/重试）。
	Reject
)

// Metrics：框架运行指标。全部原子量，worker 与提交方无锁读写。
type Metrics struct {
	Submitted atomic.Int64 // 成功进入队列的任务数
	Succeeded atomic.Int64 // Fn 返回 nil
	Failed    atomic.Int64 // Fn 返回 error / 超时 / 被取消 / panic
	Panicked  atomic.Int64 // Failed 中因 panic 结局的个数（子集计数）
	Rejected  atomic.Int64 // Reject 策略下被拒的提交次数（不计入 Submitted）

	// 守恒律：Submitted == Succeeded + Failed（验收测试的断言对象）
}

// snapshot：一次性取指标快照，供打印与断言（避免读多个原子量时互相错位）。
func (m *Metrics) snapshot() (submitted, succeeded, failed, panicked, rejected int64) {
	return m.Submitted.Load(), m.Succeeded.Load(), m.Failed.Load(),
		m.Panicked.Load(), m.Rejected.Load()
}

// ---------- 任务与框架 ----------

// TaskFn：任务本体。约定协作式取消——想被超时/取消打断，就周期性看 ctx。
type TaskFn func(ctx context.Context) error

type task struct {
	id int
	fn TaskFn
}

// Framework：有界并发任务框架。
type Framework struct {
	queue    chan task
	workers  sync.WaitGroup // 等 worker 退出
	pending  sync.WaitGroup // 等全部已受理任务跑完（含还在排队/阻塞提交的）
	stopping atomic.Bool    // 停机闸：置位后 Submit 一律拒绝
	ctx      context.Context
	cancel   context.CancelFunc
	metrics  Metrics

	workersN  int
	queueSize int
	strategy  Strategy
	taskTL    time.Duration // 单任务超时
}

// New：启动 workers 个常驻 worker。taskTL 为单任务超时，<=0 表示不限时。
func New(workers, queueSize int, strategy Strategy, taskTL time.Duration) *Framework {
	if workers <= 0 || queueSize <= 0 {
		panic("workers/queueSize 必须 > 0")
	}
	f := &Framework{
		queue:     make(chan task, queueSize),
		workersN:  workers,
		queueSize: queueSize,
		strategy:  strategy,
		taskTL:    taskTL,
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	for i := 0; i < workers; i++ {
		f.workers.Add(1)
		go f.worker()
	}
	return f
}

// worker：从队列取任务执行，队列不关闭就一直在岗。
// 停机顺序由 Stop 保证：先确认没有新任务/排队任务，才 close 队列。
func (f *Framework) worker() {
	defer f.workers.Done()
	for t := range f.queue {
		f.runOne(t)
	}
}

// Submit：提交一个任务。返回 ErrStopped（已停机）或 ErrQueueFull（拒绝策略且队列满）。
// 计数时机：进入队列才算 Submitted，被拒的只记 Rejected——守恒律因此恒成立。
func (f *Framework) Submit(id int, fn TaskFn) error {
	if f.stopping.Load() {
		return ErrStopped
	}
	f.pending.Add(1) // 从这一刻起，Stop 必须等到本任务有结局
	t := task{id: id, fn: fn}

	if f.strategy == Reject {
		select {
		case f.queue <- t:
			f.metrics.Submitted.Add(1)
			return nil
		default:
			f.pending.Done()
			f.metrics.Rejected.Add(1)
			return ErrQueueFull
		}
	}
	// Block：满了就等。ctx.Done 是唯一的逃生口（避免停机时卡死提交方）。
	select {
	case f.queue <- t:
		f.metrics.Submitted.Add(1)
		return nil
	case <-f.ctx.Done():
		f.pending.Done()
		return ErrStopped
	}
}

// runOne：跑一个任务，保证它有一个结局计数（成功/失败），然后 pending.Done。
// panic 隔离：wrapper goroutine 里 recover，把 panic 转成 error，worker 不炸。
// 超时语义：协作式——到点后放弃等待并计失败；不遵守 ctx 的 Fn 会在后台
// 自然跑完后把结果写进带缓冲的 done 通道（不阻塞、不泄漏，但结果被丢弃）。
func (f *Framework) runOne(t task) {
	defer f.pending.Done()

	taskCtx := f.ctx
	var tcancel context.CancelFunc
	if f.taskTL > 0 {
		taskCtx, tcancel = context.WithTimeout(f.ctx, f.taskTL)
		defer tcancel()
	}

	done := make(chan error, 1) // 带缓冲：放弃等待后 wrapper 也能安全收尾
	go func() {
		defer func() {
			if r := recover(); r != nil {
				f.metrics.Panicked.Add(1)
				done <- fmt.Errorf("task %d panic: %v", t.id, r)
			}
		}()
		done <- t.fn(taskCtx)
	}()

	var err error
	select {
	case err = <-done:
	case <-taskCtx.Done():
		err = fmt.Errorf("task %d 未在期限内完成: %w", t.id, taskCtx.Err())
	}
	if err == nil {
		f.metrics.Succeeded.Add(1)
	} else {
		f.metrics.Failed.Add(1)
	}
}

// Stop：优雅停机——先关闸（新提交拒绝），等已受理任务全部有结局，
// 再关队列让 worker 退出。与 Cancel 组合使用即"快速停机"。
func (f *Framework) Stop() {
	f.stopping.Store(true)
	f.pending.Wait() // 队列里排队的任务也会被 worker 取走跑完
	close(f.queue)   // 此刻不可能再有发送方
	f.workers.Wait()
}

// Cancel：广播取消。运行中/排队中的协作式任务会尽快以失败结局退出，
// 之后仍需 Stop 完成停机流程（守恒律不受影响：每个任务都会被计数）。
func (f *Framework) Cancel() { f.cancel() }

// Metrics 返回指标指针，供调用方读数。
func (f *Framework) Metrics() *Metrics { return &f.metrics }

// ---------- 演示 ----------

func main() {
	fmt.Println("== 第 1 步：启动框架（8 worker，队列 64，阻塞背压，单任务超时 200ms）==")
	f := New(8, 64, Block, 200*time.Millisecond)

	fmt.Println()
	fmt.Println("== 第 2 步：提交 10000 个任务 ==")
	fmt.Println("约 10% 的任务会 panic（rand 决定，id 每次不同）；少量任务睡 400ms 触发超时；")
	fmt.Println("其余任务做 1ms 的伪工作。提交方与 worker 并发跑，总量固定。")

	var wg sync.WaitGroup
	const total = 10000
	for id := 0; id < total; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = f.Submit(id, demoTask(id))
		}(id)
	}
	wg.Wait()

	fmt.Println()
	fmt.Println("== 第 3 步：Cancel + Stop（快速停机）==")
	f.Cancel()
	f.Stop()

	s, ok, fail, panicN, rejected := f.metrics.snapshot()
	fmt.Println()
	fmt.Println("== 第 4 步：指标与守恒律 ==")
	fmt.Printf("Submitted=%d Succeeded=%d Failed=%d (Panicked=%d) Rejected=%d\n",
		s, ok, fail, panicN, rejected)
	fmt.Printf("守恒律：Submitted(%d) == Succeeded(%d) + Failed(%d) → %v\n",
		s, ok, fail, s == ok+fail)
	fmt.Printf("panic 占失败比：%.1f%%（其余为超时/取消）\n",
		100*float64(panicN)/float64(fail))
}

// demoTask：按 id 构造三种任务——panic 任务（约 10%）、慢任务（触发超时）、普通任务。
func demoTask(id int) TaskFn {
	switch {
	case rand.IntN(10) == 0: // 约 10%：panic 隔离的验收对象
		return func(ctx context.Context) error {
			time.Sleep(time.Millisecond) // 稍微错开，让交错真实发生
			panic(fmt.Sprintf("boom-%d", id))
		}
	case id%500 == 0: // 少量慢任务：睡 400ms > 200ms 超时，且尊重 ctx
		return func(ctx context.Context) error {
			select {
			case <-time.After(400 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	default:
		return func(ctx context.Context) error {
			time.Sleep(time.Millisecond)
			return nil
		}
	}
}
