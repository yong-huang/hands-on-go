package main

// orchestrator.go：优雅停机编排器——摘流量 → HTTP 收尾 → 队列排空 → 关依赖。
//
// 停机的复杂性不在任何单步（每一步单独都简单），而在顺序与超时：
//
//	① readyz 摘除：负载均衡停止派发新流量（实验 01 的探针语义）
//	② HTTP Shutdown：监听器关闭、在途请求收尾（实验 01 的 drain）
//	③ 队列排空：停止接单，等在途任务做完；超时未做完的进死信（不丢、有记录）
//	④ 关闭依赖：DB/Redis 连接最后关——排空期间还要用它们
//
// 每一步记入 PhaseLog（带相对毫秒），停机全过程可审计——"顺序错了没有"
// 从口口相传变成一条断言。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Job：最简任务（本篇只关心它的存在与完成与否）。
type Job struct {
	URL string
}

// DeadLetter：排空超时进死信的任务记录。
type DeadLetter struct {
	Job Job
	Err string
	At  time.Time
}

// PhaseLog：停机阶段审计日志（记录相对启动点的毫秒与阶段名）。
type PhaseLog struct {
	mu      sync.Mutex
	base    time.Time
	entries []string
}

func NewPhaseLog() *PhaseLog { return &PhaseLog{base: time.Now()} }

func (p *PhaseLog) Mark(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries = append(p.entries,
		fmt.Sprintf("[%4dms] %s", time.Since(p.base).Milliseconds(), fmt.Sprintf(format, args...)))
}

func (p *PhaseLog) All() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.entries...)
}

// Orchestrator：停机编排的依赖集合。
type Orchestrator struct {
	Server interface {
		Shutdown(context.Context) error
	}
	Pool      *DrainablePool
	Ready     *atomic.Bool
	DepClose  func() error // 模拟的 DB/Redis 连接关闭
	Log       *PhaseLog
	DrainTime time.Duration
}

// Shutdown：按序执行四阶段。返回排空是否在期限内完成。
func (o *Orchestrator) Shutdown(ctx context.Context) bool {
	// ① 摘流量：readyz 先行——LB 停止派发（实验 01 的语义）
	o.Ready.Store(false)
	o.Log.Mark("① readyz 已摘除：停止接新流量")

	// ② HTTP 收尾：监听器关闭、在途请求等完（带 ctx 超时）
	if err := o.Server.Shutdown(ctx); err != nil {
		o.Log.Mark("② HTTP Shutdown 超时（%v）——按失败路径继续", err)
	} else {
		o.Log.Mark("② HTTP Shutdown 完成：在途请求已收尾")
	}

	// ③ 队列排空：期限内的任务做完，超时的进死信
	o.Log.Mark("③ 队列排空开始（期限 %s）", o.DrainTime)
	drained := o.Pool.Drain(o.DrainTime)
	if drained {
		o.Log.Mark("③ 队列排空完成：processed=%d dead=%d", o.Pool.processed.Load(), len(o.Pool.DeadLetters()))
	} else {
		o.Log.Mark("③ 排空超时：剩余任务已进死信（processed=%d dead=%d）",
			o.Pool.processed.Load(), len(o.Pool.DeadLetters()))
	}

	// ④ 依赖最后关：排空期间还需要它们
	if o.DepClose != nil {
		_ = o.DepClose()
	}
	o.Log.Mark("④ 依赖连接已关闭：进程可退出")
	return drained
}

// DrainablePool：可排空的 worker 池。
//
// 排空语义的关键：workers 用 `for j := range p.queue` 消费——close(queue) 后
// 仍会把缓冲里的存量取完才退出（"关店前把排队的客人服务完"）。
// 超时路径：aborted 旗标让 workers 把取到的任务直接送死信；
// drain 循环与 workers 同时接收（channel 保证每任务恰好一次交付），
// 所以每个未完成任务都会进死信且只进一次。
type DrainablePool struct {
	queue     chan Job
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	aborted   atomic.Bool
	processed atomic.Int64
	deadMu    sync.Mutex
	dead      []DeadLetter
	runJob    func(Job)
}

func NewDrainablePool(workers, queueSize int, runJob func(Job)) *DrainablePool {
	p := &DrainablePool{queue: make(chan Job, queueSize), runJob: runJob}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for j := range p.queue { // close 后继续消费存量，直到清空
				if p.aborted.Load() {
					p.deadLetter(j, "drain timeout")
					continue
				}
				p.runJob(j)
				p.processed.Add(1)
			}
		}()
	}
	return p
}

// Enqueue：入队；池关闭后返回错误（停机后不再接单）。
func (p *DrainablePool) Enqueue(j Job) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("queue closed")
	}
	p.mu.Unlock()
	select {
	case p.queue <- j:
		return nil
	default:
		return errors.New("queue full")
	}
}

func (p *DrainablePool) deadLetter(j Job, reason string) {
	p.deadMu.Lock()
	p.dead = append(p.dead, DeadLetter{Job: j, Err: reason, At: time.Now()})
	p.deadMu.Unlock()
}

// Drain：关闭入口，等全部任务做完；超时则置 abort 旗标并直接把队列存量
// 逐个送死信（与 workers 竞争接收，channel 保证恰好一次）。worker 手里
// 正在跑的任务无法强取——等它自然结束或悬挂（README 有边界讨论）。
func (p *DrainablePool) Drain(timeout time.Duration) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.closed = true
	p.mu.Unlock()
	close(p.queue)

	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		p.aborted.Store(true)
		for j := range p.queue {
			p.deadLetter(j, "drain timeout")
		}
		return false
	}
}

func (p *DrainablePool) DeadLetters() []DeadLetter {
	p.deadMu.Lock()
	defer p.deadMu.Unlock()
	return append([]DeadLetter(nil), p.dead...)
}
