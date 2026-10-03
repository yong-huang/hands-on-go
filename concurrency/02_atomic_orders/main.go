// 项目 2：atomic 语义层级——SeqCst vs Acquire/Release
//
// Go 的 sync/atomic 公开 API 没有内存序参数：所有原子操作都是顺序一致
// （SeqCst）语义。Release/Acquire 是一个"推理层级"——顺序一致的 Store
// 必然兼具 release 语义，Load 必然兼具 acquire 语义，因此可以按
// Release 写 + Acquire 读来推理，而实现只会更强。
//
// 运行方式：go run main.go
//
// 输出分两类：标注（固定）的行每次运行相同；标注（每次不同）的行
// 是粗测计时，受机器负载影响，只看量级。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	rounds    = 100_000 // 各 litmus 的轮数（结果固定为 0 异常）
	lockIters = 200_000 // 计时环节每个 goroutine 的加解锁次数
	lockProcs = 4       // 计时环节的 goroutine 数
)

func main() {
	fmt.Println("== 第 1 步：内存序的三个层级 ==")
	fmt.Println("Relaxed（松散）  ：只保证这个操作本身原子，不承诺任何顺序。")
	fmt.Println("Acquire/Release ：写方出闸（release）把之前的写托付，读方进闸（acquire）全收。")
	fmt.Println("SeqCst（顺序一致）：在 A/R 之上再给所有原子操作排一条全局唯一的时间线。")
	fmt.Println("Go 公开 API 只有 SeqCst 一档，A/R 是它必然满足的子集，用来做推理。")
	fmt.Println()

	fmt.Println("== 第 2 步：发布对象——Release 写 + Acquire 读（推理口径） ==")
	nilN, badN := publishReleaseAcquire(rounds)
	fmt.Printf("发布前 nil 观测：%d 次（正常：指针尚未发布）\n", nilN)
	fmt.Printf("字段签名不一致：%d 次（固定，0 才是验收线）\n\n", badN)

	fmt.Println("== 第 3 步：flag 消息传递——Release/Acquire 配对 ==")
	fmt.Printf("异常（flag 新 data 旧）：%d（固定，0 才是验收线）\n\n", mpReleaseAcquire(rounds))

	fmt.Println("== 第 4 步：store buffer litmus——只有 SeqCst 能封住 ==")
	fmt.Printf("双重旧值轮数：%d / %d 轮（固定，0；A/R 语义下本允许失败，见 README）\n\n", sbSeqCst(rounds), rounds)

	fmt.Println("== 第 5 步：CAS 自旋锁——锁的最小实现 ==")
	var l SpinLock
	var n int64 // 故意用普通变量：锁的 acquire/release 配对负责可见性
	var wg sync.WaitGroup
	for i := 0; i < lockProcs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10_000; j++ {
				l.Lock()
				n++ // 临界区内普通写：互斥 + 可见性全部由锁承诺
				l.Unlock()
			}
		}()
	}
	wg.Wait()
	fmt.Printf("4 goroutine 各加 10000：n = %d（固定，必须恰为 40000）\n\n", n)

	fmt.Println("== 第 6 步：无争用粗测——自旋锁 vs sync.Mutex（每次不同） ==")
	sl := benchLockUncontended(&l, lockIters)
	var m sync.Mutex
	mu := benchLockUncontendedLock(&m, lockIters)
	// 每次不同：受频率漂移与负载影响；谁快谁慢以 go test -bench 为准，
	// 粗测只回答"两者同量级"。
	fmt.Printf("SpinLock 无争用 %d 次：%s\n", lockIters, sl)
	fmt.Printf("sync.Mutex 无争用 %d 次：%s（每次不同，只看量级）\n", lockIters, mu)
	fmt.Printf("\n（本机 %d 个逻辑核；争用场景的严谨基准用 go test -bench . -run '^$'）\n", runtime.NumCPU())
}

// publishReleaseAcquire：发布不可变对象。writer 构造好后一次 Pointer.Store
// （release 口径）；reader Pointer.Load（acquire 口径）拿到非 nil 指针后，
// 保证能看到指针背后全部字段的写入。载荷是 {v, ^v} 签名对：一致则异或必为
// 全 1；发布前的 nil 属正常现象，单独统计。
func publishReleaseAcquire(rounds int) (nilSeen int64, bad int64) {
	var p atomic.Pointer[[2]int32]
	var badCnt, nilCnt atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // 发布方
		defer wg.Done()
		for i := 1; i <= rounds; i++ {
			v := int32(i)
			p.Store(&[2]int32{v, ^v}) // release：两个字段的写随指针一起发布
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() { // 订阅方 ×4
			defer wg.Done()
			for i := 0; i < rounds/4; i++ {
				ps := p.Load() // acquire：非 nil 则背后字段写入全部可见
				if ps == nil {
					nilCnt.Add(1)
					continue
				}
				if ps[0]^ps[1] != ^int32(0) { // 签名被破坏：acquire 语义失效
					badCnt.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	return nilCnt.Load(), badCnt.Load()
}

// mpReleaseAcquire：flag 消息传递 litmus 的 Release/Acquire 推理口径。
// writer：先写 data（普通写），再 Store flag（release）；reader：先 Load flag
// （acquire），命中后再读 data。推理链条与 01 的 atomic 版完全一致。
func mpReleaseAcquire(rounds int) int64 {
	var data, flag atomic.Int32
	var anomalies atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // writer
		defer wg.Done()
		for i := 1; i <= rounds; i++ {
			v := int32(i)
			data.Store(v) // release 写 1：数据
			flag.Store(v) // release 写 2：信号（Go 实现为 SeqCst，强于所需）
		}
	}()
	go func() { // reader
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			f := flag.Load() // acquire 读 1：信号
			d := data.Load() // acquire 读 2：数据
			if f > d {
				anomalies.Add(1)
			}
		}
	}()
	wg.Wait()
	return anomalies.Load()
}

// sbSeqCst：store buffer litmus。在 C++ 里用 memory_order_release/acquire
// 实现它允许出现"双重旧值"（同一轮双方都读到对方旧值）——A/R 不提供全序。
// Go 的 atomic 是 SeqCst，双重旧值被全局时间线封死，因此这里必须是 0。
// 单方面读到旧值在两种语义下都允许，不统计。
func sbSeqCst(rounds int) int64 {
	var a, b atomic.Int32
	sawOldA := make([]bool, rounds+1)
	sawOldB := make([]bool, rounds+1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			a.Store(v)
			sawOldA[r] = b.Load() < v
		}
	}()
	go func() {
		defer wg.Done()
		for r := 1; r <= rounds; r++ {
			v := int32(r)
			b.Store(v)
			sawOldB[r] = a.Load() < v
		}
	}()
	wg.Wait()
	both := int64(0)
	for r := 1; r <= rounds; r++ {
		if sawOldA[r] && sawOldB[r] { // 同一轮双方都旧：只有 A/R 世界允许
			both++
		}
	}
	return both
}

// SpinLock：CAS + release 解锁的最小自旋锁。
// Lock 的 CAS 成功即 acquire；Unlock 的 Store(0) 即 release。
type SpinLock struct{ v atomic.Int32 }

func (l *SpinLock) Lock() {
	for !l.v.CompareAndSwap(0, 1) {
		runtime.Gosched() // 让出 P，避免持锁者在本 P 上饿死（标准库用 procyield 空转）
	}
}

func (l *SpinLock) Unlock() {
	l.v.Store(0) // release：临界区内的全部写对下一个 Lock 成功者可见
}

func benchLockUncontended(l *SpinLock, iters int) string {
	var snoop atomic.Int64 // sink：防止编译器把空临界区优化掉
	start := time.Now()
	for i := 0; i < iters; i++ {
		l.Lock()
		snoop.Add(1) // 最小临界区
		l.Unlock()
	}
	return time.Since(start).Round(time.Microsecond).String()
}

func benchLockUncontendedLock(m *sync.Mutex, iters int) string {
	var snoop atomic.Int64
	start := time.Now()
	for i := 0; i < iters; i++ {
		m.Lock()
		snoop.Add(1)
		m.Unlock()
	}
	return time.Since(start).Round(time.Microsecond).String()
}
