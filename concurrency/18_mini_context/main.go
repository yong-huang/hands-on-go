// 项目 18：mini context——cancelCtx 树、超时级联与 Value 链的复刻。
//
// 结构对照（context 包，本篇复刻其核心协议）：
//
//	type cancelCtx struct {   // 节选
//	    mu       sync.Mutex    // 保护 done/err/children
//	    done     chan struct{} // 惰性/立即创建，取消时 close 广播
//	    children map[*cancelCtx]struct{} // 子节点登记表
//	    err      error         // 取消原因，非 nil 即已取消
//	}
//
// 传播协议三原则：
//  1. 子随父亡：cancel 沿 children 递归，父取消则全部子树取消；
//  2. 子不弑父：子 cancel 只从父的 children 摘掉自己，父不受影响；
//  3. 先到先得：父子任意一方先取消，后取消的一方是幂等空操作。
//
// 运行方式：go run main.go。第 2 步的耗时数字每次不同，量级为微秒。
package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// 两个取消原因（标准库还有 Cause 体系，本复刻只有这两种，差异表见 README）。
var (
	ErrCanceled         = errors.New("context canceled")
	ErrDeadlineExceeded = errors.New("deadline exceeded")
)

// Context：与标准库同形的四方法接口。
type Context interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(key any) any
}

// backgroundCtx：根节点，永不取消，Done 返回 nil（谁等 nil channel 谁
// 永久阻塞，等价于"永不触发"）。
type backgroundCtx struct{}

func (backgroundCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (backgroundCtx) Done() <-chan struct{}       { return nil }
func (backgroundCtx) Err() error                  { return nil }
func (backgroundCtx) Value(any) any               { return nil }

// Background：根上下文。
func Background() Context { return backgroundCtx{} }

// cancelCtx：可取消节点。done 构造即建（标准库 1.21+ 是惰性的，差异表）。
type cancelCtx struct {
	mu       sync.Mutex
	done     chan struct{}
	children map[*cancelCtx]struct{}
	err      error
}

func (c *cancelCtx) Done() <-chan struct{} { return c.done }

func (c *cancelCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *cancelCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *cancelCtx) Value(any) any { return nil }

// cancel：置错误 + close(done) 广播 + 递归取消整棵子树，最后摘除登记。
// 持锁递归是安全的：锁序永远是父到子，没有反向获取路径。
func (c *cancelCtx) cancel(err error) {
	c.mu.Lock()
	if c.err != nil { // 先到先得：已取消过，幂等返回
		c.mu.Unlock()
		return
	}
	c.err = err
	close(c.done) // 广播：所有 <-Done() 立即醒来
	for child := range c.children {
		child.cancel(err) // 子随父亡：错误沿树下传
	}
	c.children = nil // 整树已取消，登记表一次清空
	c.mu.Unlock()
}

// propagateCancel：把 child 挂到 parent 的取消体系上。
// 父也是本库可取消节点 → 走 children 登记；否则（外部实现/根）起
// watcher goroutine 盯 parent.Done()。
func propagateCancel(parent Context, child *cancelCtx) {
	if parent == nil {
		panic("cannot create context from nil parent")
	}
	if pc, ok := parent.(*cancelCtx); ok {
		pc.mu.Lock()
		if pc.err != nil { // 父已取消：孩子生下来就是取消态
			err := pc.err
			pc.mu.Unlock()
			child.cancel(err)
			return
		}
		if pc.children == nil {
			pc.children = make(map[*cancelCtx]struct{})
		}
		pc.children[child] = struct{}{}
		pc.mu.Unlock()
		return
	}
	if parent.Done() == nil { // Background 等永不取消的根：无需登记
		return
	}
	go func() { // 外部父：盯住它，一旦取消就拉响自己
		select {
		case <-parent.Done():
			child.cancel(parent.Err())
		case <-child.done: // 自己先取消：watcher 退出
		}
	}()
}

// CancelFunc：取消函数。幂等，重复调用是空操作。
type CancelFunc func()

// WithCancel：在 parent 下挂一个可取消节点。
func WithCancel(parent Context) (Context, CancelFunc) {
	c := &cancelCtx{done: make(chan struct{})}
	propagateCancel(parent, c)
	return c, func() { c.cancel(ErrCanceled) }
}

// timerCtx：带截止时间的节点，内嵌 cancelCtx 复用整棵传播协议。
type timerCtx struct {
	*cancelCtx
	timer    *time.Timer
	deadline time.Time
}

func (c *timerCtx) Deadline() (time.Time, bool) { return c.deadline, true }

// WithTimeout：WithDeadline 的相对时间形式。
func WithTimeout(parent Context, d time.Duration) (Context, CancelFunc) {
	return WithDeadline(parent, time.Now().Add(d))
}

// WithDeadline：父的截止时间更早时直接退化为 WithCancel（标准库同款
// 短路：父先到点自然会把子带走，自己的计时器没有存在意义）。
func WithDeadline(parent Context, d time.Time) (Context, CancelFunc) {
	if pd, ok := parent.Deadline(); ok && pd.Before(d) {
		return WithCancel(parent)
	}
	c := &timerCtx{cancelCtx: &cancelCtx{done: make(chan struct{})}, deadline: d}
	propagateCancel(parent, c.cancelCtx)
	c.timer = time.AfterFunc(time.Until(d), func() { c.cancel(ErrDeadlineExceeded) })
	return c, func() {
		// 先 Stop 再 cancel：省掉计时器一次无用到点。Err 归属由
		// cancel 的先到先得幂等性保证，与这两个调用的顺序无关。
		c.timer.Stop()
		c.cancelCtx.cancel(ErrCanceled)
	}
}

// valueCtx：单键值对，Value 沿父链逐层回溯（链表式作用域）。
type valueCtx struct {
	Context
	key, val any
}

func (c *valueCtx) Value(key any) any {
	if c.key == key { // 就近命中：子层遮蔽父层同名键
		return c.val
	}
	return c.Context.Value(key)
}

// WithValue：挂一个键值对；键必须可比较且非 nil（标准库同款约束）。
func WithValue(parent Context, key, val any) Context {
	if key == nil {
		panic("nil key")
	}
	return &valueCtx{Context: parent, key: key, val: val}
}

func main() {
	fmt.Println("== 第 1 步：取消树的三原则 ==")
	fmt.Println("子随父亡（cancel 沿 children 递归）；子不弑父（只摘自己的登记）；")
	fmt.Println("先到先得（重复 cancel 幂等）。")

	fmt.Println()
	fmt.Println("== 第 2 步：5 层树取消根，全部叶子 Done 的耗时 ==")
	root, cancelRoot := WithCancel(Background())
	node := root
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ { // 链式 5 层
		next, _ := WithCancel(node)
		for j := 0; j < 2; j++ { // 每层挂 2 个叶子，共 10 叶
			leaf, _ := WithCancel(next)
			wg.Add(1)
			go func() {
				<-leaf.Done()
				wg.Done()
			}()
		}
		node = next
	}
	start := time.Now()
	cancelRoot() // 取消根：递归沿 children 传播到全部叶子
	wg.Wait()
	fmt.Printf("cancel 后 10 个叶子全部 Done：%v（每次不同，验收线 < 10ms）\n",
		time.Since(start))

	fmt.Println()
	fmt.Println("== 第 3 步：超时级联——父超时，子自动取消且继承原因 ==")
	pctx, cancelP := WithTimeout(Background(), 30*time.Millisecond)
	defer cancelP()
	cctx, cancelC := WithCancel(pctx)
	defer cancelC()
	<-cctx.Done()
	fmt.Printf("子 Done 后 Err = %v（固定）\n", cctx.Err())

	fmt.Println()
	fmt.Println("== 第 4 步：子先取消——父与兄弟不受影响 ==")
	father, cancelF := WithCancel(Background())
	defer cancelF()
	son, cancelS := WithCancel(father)
	daughter, _ := WithCancel(father)
	cancelS()
	select {
	case <-father.Done():
		fmt.Println("父被波及（不应出现）")
	case <-daughter.Done():
		fmt.Println("兄弟被波及（不应出现）")
	case <-time.After(30 * time.Millisecond):
		fmt.Printf("30ms 后父与兄弟均未 Done，son.Err = %v（固定）\n", son.Err())
	}

	fmt.Println()
	fmt.Println("== 第 5 步：Value 链就近命中 ==")
	ctx := WithValue(WithValue(Background(), "user", "alice"), "req", 42)
	fmt.Printf("req = %v（固定）\n", ctx.Value("req"))
	fmt.Printf("user = %v（固定，跨层回溯）\n", ctx.Value("user"))
	fmt.Printf("missing = %v（固定，无此键返回 nil）\n", ctx.Value("missing"))
}
