// 项目 3：false sharing 与缓存行填充
//
// CPU 缓存以缓存行（cache line）为单位搬运数据。两个变量即使毫无逻辑关系，
// 只要落在同一行，分别被不同核心热写，这一行就会在核心间来回弹跳——
// 这就是伪共享（false sharing）。给每个计数器独占一行（64 字节填充）即可消除。
//
// 运行方式：go run main.go
//
// 输出分两类：标注（固定）的行每次运行相同；标注（每次不同）的行是计时，
// 受调度与频率影响，倍数只看量级。
package main

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const (
	procs     = 8         // 8 个 goroutine 各热写自己的计数器
	demoIters = 2_000_000 // 演示环节每个 goroutine 的自增次数（每次不同）
	cellSize  = 64        // 缓存行宽度；Apple Silicon 与 x86 桌面端同为 64 字节
)

func main() {
	fmt.Printf("== 第 1 步：伪共享是什么（GOMAXPROCS=%d） ==\n", runtime.GOMAXPROCS(0))
	fmt.Printf("缓存行宽度 %d 字节：CPU 与内存之间搬运数据的最小单位。\n", cellSize)
	fmt.Println("核心 A 写一行里的任何 1 个字节，核心 B 手里同行的副本整行作废。")
	fmt.Println("两个计数器哪怕属于两个毫不相干的人，只要紧排在一行，就会互相拖慢。")
	fmt.Println()

	fmt.Println("== 第 2 步：无填充版——8 个 atomic.Int64 紧排 64 字节 ==")
	shared := newSharedCounters(procs)
	d := hammerCountersShared(shared, demoIters)
	fmt.Printf("8 goroutine 各自增 %d 次：耗时 %v（每次不同）\n\n", demoIters, d.Round(time.Millisecond))

	fmt.Println("== 第 3 步：填充版——每个计数器独占一条缓存行 ==")
	padded := newPaddedCounters(procs)
	p := hammerCountersPadded(padded, demoIters)
	fmt.Printf("8 goroutine 各自增 %d 次：耗时 %v（每次不同）\n\n", demoIters, p.Round(time.Millisecond))

	fmt.Printf("== 第 4 步：结论（每次不同） ==\n")
	fmt.Printf("加速倍数：%.1f 倍（倍数只看量级；若接近 1，排查方法见 README）\n", float64(d)/float64(p))
	fmt.Printf("对齐校验：填充版 %d/%d 个计数器落在 64 字节边界\n",
		alignedCount(padded), procs)
}

// hammerCountersShared：无填充计时。8 个计数器紧排 = 共享同一（或相邻）缓存行。
func hammerCountersShared(cs []atomic.Int64, iters int) time.Duration {
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < len(cs); i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				cs[id].Add(1) // 每次写都让别的核心手里那行失效
			}
		}(i)
	}
	wg.Wait()
	return time.Since(start)
}

// hammerCountersPadded：填充版计时。除计数器各自的行独立外，其余完全相同。
func hammerCountersPadded(cs []cell, iters int) time.Duration {
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < len(cs); i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				cs[id].n.Add(1)
			}
		}(i)
	}
	wg.Wait()
	return time.Since(start)
}

// cell：64 字节的独立缓存行。_ [56]byte 是显式填充——
// Go 没有 aligned(64) 属性，填充是唯一的语言层手段。
type cell struct {
	n atomic.Int64
	_ [cellSize - 8]byte // atomic.Int64 占 8 字节，凑满 64
}

// newPaddedCounters：分配并手工对齐到 64 字节边界。
// Go 的分配器只保证 8 字节对齐，按 64 补齐必须自己动手：
// 多分配一段，把起点推进到第一个 64 字节边界，元素间隔恰为 64，
// 因此后面的每个元素自动落上边界。指针算术走 unsafe.Add（1.17+ 的认可
// API），基址经 reflect 取出，避免 uintptr 与 unsafe.Pointer 来回互转。
func newPaddedCounters(n int) []cell {
	buf := make([]byte, n*cellSize+cellSize)
	base := reflect.ValueOf(&buf).Elem().Pointer() // uintptr：buf 首地址
	off := int((base+cellSize-1)&^(cellSize-1) - base)
	cs := unsafe.Slice((*cell)(unsafe.Add(unsafe.Pointer(&buf[0]), off)), n)
	for i := range cs {
		cs[i].n.Store(0)
	}
	return cs
}

// alignedCount：校验每个元素地址都落在 64 字节边界（演示诚实性检查）。
func alignedCount(cs []cell) int {
	ok := 0
	for i := range cs {
		if reflect.ValueOf(&cs[i]).Pointer()%cellSize == 0 {
			ok++
		}
	}
	return ok
}

// newSharedCounters：无填充对照组，8 个 Int64 紧排，1~2 条缓存行装下。
func newSharedCounters(n int) []atomic.Int64 {
	cs := make([]atomic.Int64, n)
	return cs
}
