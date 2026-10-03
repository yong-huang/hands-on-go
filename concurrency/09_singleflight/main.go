// 项目 9：singleflight 手写 —— call 去重与请求合并防击穿
//
// 分步演示：
//
//	步骤 1  100 个 goroutine 同 key 并发请求：后端只执行 1 次，其余共享结果
//	步骤 2  不同 key 互不阻塞：4 个 key 并行执行，总耗时≈单次耗时
//	步骤 3  panic 不吞：发起者与等待者都收到原始 panic 值，key 被清理
//	步骤 4  对照 golang.org/x/sync/singleflight v0.23.0：合并行为一致
package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	xsf "golang.org/x/sync/singleflight"
)

// call 是一次进行中的后端调用；同 key 的后来者共享它而不是再发一次请求。
type call struct {
	wg       sync.WaitGroup
	val      any
	err      error
	dups     int         // 共享者个数（发起者之外）
	panicked atomic.Bool // fn 是否 panic；等待者据此决定重新 panic
}

// Group 是一个请求合并器：同一 key 同时只允许一个 fn 在执行。
type Group struct {
	mu sync.Mutex
	m  map[string]*call // key -> 进行中的调用；fn 结束即删除
}

// Do 合并对同一 key 的并发调用：只有第一个到达者执行 fn，
// 其余等待并共享同一份 (val, err)。shared 表示结果是否被多人共享。
func (g *Group) Do(key string, fn func() (any, error)) (val any, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		c.dups++
		g.mu.Unlock()
		c.wg.Wait() // 在看什么：后来者不执行 fn，睡在这里等结果
		if c.panicked.Load() {
			panic(c.val) // 与发起者同源：等待者也收到原始 panic 值
		}
		return c.val, c.err, true
	}
	c := &call{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	g.doCall(c, key, fn)
	return c.val, c.err, c.dups > 0
}

// doCall 执行 fn 并保证：无论成败，等待者都被唤醒、key 从 map 清除。
// panic 不被吞下：记录、唤醒、清理之后按原始值重新 panic。
func (g *Group) doCall(c *call, key string, fn func() (any, error)) {
	defer func() {
		if r := recover(); r != nil {
			c.panicked.Store(true)
			c.val = r // 留给等待者 re-panic 用
			g.mu.Lock()
			delete(g.m, key)
			g.mu.Unlock()
			c.wg.Done()
			panic(r) // 原值继续向发起者传播
		}
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
		c.wg.Done()
	}()
	c.val, c.err = fn()
}

// dupsOf 读取某 key 进行中 call 的共享者数（演示与测试的确定性辅助）。
func (g *Group) dupsOf(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.m[key]; ok {
		return c.dups
	}
	return -1 // call 已结束
}

// waitUntil 轮询等待 f 为真。
func waitUntil(f func() bool) {
	for !f() {
		time.Sleep(time.Millisecond)
	}
}

func main() {
	fmt.Println("== 步骤 1：100 个 goroutine 同 key，后端只落 1 次 ==")
	var g Group
	var backend atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			g.Do("hot", func() (any, error) {
				backend.Add(1)
				time.Sleep(50 * time.Millisecond) // 模拟慢后端（如缓存重建）
				return "value", nil
			})
		}()
	}
	close(start)
	wg.Wait()
	// 在看什么：100 个请求只打穿到后端 1 次——热点 key 的缓存击穿被合并挡住
	fmt.Printf("  后端执行次数=%d（诚实预期：1；100 个调用者共享同一份结果）\n", backend.Load())

	fmt.Println("== 步骤 2：不同 key 互不阻塞，4 路并行 ==")
	var backend2 atomic.Int64
	begin := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			g.Do(k, func() (any, error) {
				backend2.Add(1)
				time.Sleep(80 * time.Millisecond)
				return k, nil
			})
		}(fmt.Sprintf("k%d", i))
	}
	wg.Wait()
	// 在看什么：4 个 key 各自执行、同时进行，总耗时接近 80ms 而非 4 倍串行
	fmt.Printf("  后端执行次数=%d（诚实预期：4），总耗时 %v（诚实预期：约 80~120ms 而非 320ms+）\n",
		backend2.Load(), time.Since(begin).Round(time.Millisecond))

	fmt.Println("== 步骤 3：panic 不吞——发起者与等待者都拿到原值 ==")
	var g3 Group
	sentinel := errors.New("backend exploded")
	ready := make(chan struct{})
	initiatorGot := make(chan any, 1)
	waiterGot := make(chan any, 3)
	go func() { // 发起者先注册执行权，进入 fn 后再放行等待者，顺序确定
		defer func() { initiatorGot <- recover() }()
		g3.Do("boom", func() (any, error) {
			close(ready) // 此刻 call 已注册：等待者必然成为共享者
			waitUntil(func() bool { return g3.dupsOf("boom") == 3 })
			panic(sentinel)
		})
	}()
	for w := 0; w < 3; w++ {
		go func() {
			<-ready
			defer func() { waiterGot <- recover() }()
			g3.Do("boom", func() (any, error) { return nil, nil })
		}()
	}
	fmt.Printf("  发起者 recover 到=%v（诚实预期：backend exploded，未包一层）\n", <-initiatorGot)
	for i := 0; i < 3; i++ {
		fmt.Printf("  等待者 recover 到=%v\n", <-waiterGot)
	}
	if _, err, _ := g3.Do("boom", func() (any, error) { return "fresh", nil }); err == nil {
		fmt.Println("  panic 后同 key 再次 Do 正常执行——失败的 call 已从 map 清除")
	}

	fmt.Println("== 步骤 4：对照 golang.org/x/sync/singleflight v0.23.0 ==")
	var xg xsf.Group
	var backend4 atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // start 在步骤 1 已关闭，这里直接放行
			_, _, _ = xg.Do("hot", func() (any, error) {
				backend4.Add(1)
				time.Sleep(50 * time.Millisecond)
				return "value", nil
			})
		}()
	}
	wg.Wait()
	fmt.Printf("  x/sync 同 key 100 并发：后端执行次数=%d（诚实预期：1，与手写版一致）\n", backend4.Load())
}
