// 项目 19：errgroup 手写与结构化并发——WaitGroup+sync.Once+信号量+取消的组合。
//
// 结构对照（x/sync/errgroup，差异表见 README）：
//
//	type Group struct {        // 节选
//	    cancel  func()          // WithContext 注入的取消函数；无 ctx 时为 nil
//	    wg      sync.WaitGroup  // 全组联结：Wait 等所有任务返回
//	    sem     chan struct{}   // SetLimit 的并发令牌；nil = 无上限
//	    errOnce sync.Once       // 首错独占：只有第一个错误生效
//	    err     error           // Wait 的返回值
//	}
//
// 结构化并发三原则：
//  1. 谁派生谁联结：任务 goroutine 的生命周期被 Wait 圈定，不外溢；
//  2. 首错联动取消：一个任务失败，整组通过 ctx.Done() 收到停机令；
//  3. 并发有上限：SetLimit 的令牌通道让"派生 N 个"变成"至多 limit 个同时在跑"。
//
// 运行方式：go run main.go。第 2 步的取消耗时每次不同，量级为微秒，
// 验收线 < 10ms；其余输出固定。
package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- mini errgroup ----------

// Group：一组共享取消与首错收集的子任务。
type Group struct {
	cancel  func()         // WithContext 注入；首错或 Wait 时调用
	wg      sync.WaitGroup // 联结器：Wait 等全部任务返回
	sem     chan struct{}  // 并发令牌；nil = 无上限
	errOnce sync.Once      // 首错独占
	err     error          // 第一个非 nil 错误
}

// WithContext：派生一个可取消 ctx；组内首错或 Wait 返回时它被取消。
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{cancel: cancel}, ctx
}

// SetLimit：限制同时在跑的任务数为至多 n。必须在没有任务活跃时调用。
func (g *Group) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if active := len(g.sem); active != 0 {
		panic(fmt.Errorf("errgroup: %d 个任务仍在活跃时不可改上限", active))
	}
	g.sem = make(chan struct{}, n)
}

// Go：派生一个任务。已设上限时阻塞，直到有令牌可用——提交方背压，
// 而不是无限派生。
func (g *Group) Go(f func() error) {
	if g.sem != nil {
		g.sem <- struct{}{}
	}
	g.wg.Add(1)
	go func() {
		defer g.done()
		g.collect(g.safeCall(f))
	}()
}

// TryGo：上限满时不阻塞，直接报告"没派出去"。
func (g *Group) TryGo(f func() error) bool {
	if g.sem != nil {
		select {
		case g.sem <- struct{}{}:
		default:
			return false
		}
	}
	g.wg.Add(1)
	go func() {
		defer g.done()
		g.collect(g.safeCall(f))
	}()
	return true
}

// done：任务收尾。先还令牌再 wg.Done——顺序反了的话，Wait 可能在令牌
// 归还前返回，留下一个满的信号量。
func (g *Group) done() {
	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
}

// safeCall：panic 转错误。与 x/sync 刻意不 recover 的取舍不同，见 README。
func (g *Group) safeCall(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("task panicked: %v", r)
		}
	}()
	return f()
}

// collect：首错独占登记，并触发全组取消。
func (g *Group) collect(err error) {
	if err == nil {
		return
	}
	g.errOnce.Do(func() {
		g.err = err
		if g.cancel != nil {
			g.cancel() // close(done) 广播：全组所有 <-ctx.Done() 同时醒
		}
	})
}

// Wait：联结全部任务，返回首错。Wait 返回后组内 goroutine 保证清零。
func (g *Group) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel() // 正常收尾也取消：让"等 ctx"的收尾路径不必区分成败
	}
	return g.err
}

// ---------- 分步演示 ----------

func main() {
	fmt.Println("== 第 1 步：三合一心智模型 ==")
	fmt.Println("Group = WaitGroup(联结) + sync.Once(首错独占) + 令牌通道(限流) + cancel(联动)。")
	fmt.Println()

	// 第 2 步：8 个任务，1 个失败，其余 7 个靠 ctx.Done() 停机。
	fmt.Println("== 第 2 步：一失败全组取消（验收线 < 10ms）==")
	g, ctx := WithContext(context.Background())
	g.SetLimit(8)
	var failAt atomic.Int64 // 任务 0 失败的时刻（纳秒）
	canceled := make(chan time.Duration, 7)
	for i := 0; i < 8; i++ {
		i := i
		g.Go(func() error {
			if i == 0 { // 任务 0：立即失败，记下失败时刻
				failAt.Store(time.Now().UnixNano())
				return errors.New("task 0 failed")
			}
			<-ctx.Done() // 其余任务：收到停机令即记录耗时
			canceled <- time.Since(time.Unix(0, failAt.Load()))
			return nil
		})
	}
	err := g.Wait()
	worst := time.Duration(0)
	for i := 0; i < 7; i++ {
		if d := <-canceled; d > worst {
			worst = d
		}
	}
	fmt.Printf("Wait 返回首错：%v\n", err)
	fmt.Printf("7 个幸存者最慢收到取消耗时：%v（每次不同，验收线 < 10ms）\n", worst)
	fmt.Println()

	// 第 3 步：SetLimit(3)，20 个任务，峰值并发恰好 = 3。
	fmt.Println("== 第 3 步：SetLimit 峰值并发 = limit ==")
	g2, _ := WithContext(context.Background())
	g2.SetLimit(3)
	var active, peak int64
	release := make(chan struct{})
	var once sync.Once
	for i := 0; i < 20; i++ {
		g2.Go(func() error {
			n := atomic.AddInt64(&active, 1)
			for { // 记录峰值（CAS 重试，读改写一体）
				p := atomic.LoadInt64(&peak)
				if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
					break
				}
			}
			if n == 3 { // 第 3 个任务到齐即放行：能同时到达 3 个是上限的直接证据
				once.Do(func() { close(release) })
			}
			<-release
			atomic.AddInt64(&active, -1) // 先减计数再还令牌：active ≤ 持令牌数 ≤ 3
			return nil
		})
	}
	g2.Wait()
	fmt.Printf("20 个任务、limit=3：实测峰值并发 = %d（固定）\n", peak)
	fmt.Println()

	// 第 4 步：panic 转错误。
	fmt.Println("== 第 4 步：panic 转错误 ==")
	g3, ctx3 := WithContext(context.Background())
	g3.Go(func() error { panic("boom") })
	g3.Go(func() error {
		<-ctx3.Done() // panic 触发的取消同样联动全组
		return nil
	})
	fmt.Printf("Wait 返回：%v（固定）\n", g3.Wait())
	fmt.Println()

	// 第 5 步：裸 goroutine 失控对照。两个 goroutine 会永远卡死，进程
	// 退出时由操作系统回收——这正是对照组存在的意义。
	fmt.Println("== 第 5 步：裸 goroutine 失控对照（失败无人管、泄漏无人收）==")
	base := runtime.NumGoroutine()
	errCh := make(chan error) // 无人接收：A 的错误无处汇报
	result := make(chan int)  // 无人发送：B 永远等 A 的结果
	go func() {               // 任务 A：失败，错误发往无人接收的通道
		errCh <- errors.New("task A failed")
	}()
	go func() { // 任务 B：等 A 的结果；A 失败了，这个等待永不结束
		<-result
	}()
	time.Sleep(100 * time.Millisecond) // 等两个 goroutine 到达阻塞点
	fmt.Println("A 失败后错误发往无人接收的通道，B 永远等 A 的结果：")
	fmt.Println("调用方既拿不到错误，也收不回 goroutine。errgroup 版两条都有解——")
	fmt.Println("错误经 Wait 返回，goroutine 经 Wait 清零。")
	fmt.Printf("goroutine 数：基线 %d → 泄漏后 %d（基线每次不同，差值固定为 2）\n",
		base, runtime.NumGoroutine())
}
