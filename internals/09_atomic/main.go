package main

// 项目 9：Atomic 底层——CAS 与内存屏障
//
// 实验：
//   1. 非原子 vs 原子计数器：并发 c++ 丢更新 vs AddInt64 精确
//   2. CAS 无锁更新：CompareAndSwap 重试循环；CAS 自旋锁保护临界区
//   3. 内存屏障：release Store / acquire Load 保证「先写数据、再置就绪」的顺序可见
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON，供 viz.py 画图
// 竞争检测：go run -race main.go 会对非原子版本报告数据竞争（原子版无告警）

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
)

const goroutines = 16
const perG = 100000

// ---------- 实验 1：非原子 vs 原子计数器 ----------

func exp1() int64 {
	fmt.Println("========== 1. 非原子 vs 原子计数器 ==========")
	var c int64 // 非原子：读-改-写 三步，并发下互相覆盖
	var a int64 // 原子：硬件保证整个读-改-写不可分割
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c++                    // 读到同一旧值 → 更新丢失
				atomic.AddInt64(&a, 1) // 底层用带内存屏障的原子指令
			}
		}()
	}
	wg.Wait()

	expected := int64(goroutines * perG)
	fmt.Printf("  期望值              : %d\n", expected)
	fmt.Printf("  非原子  c++         : %d    （丢了 %d）\n", c, expected-c)
	fmt.Printf("  原子    AddInt64    : %d    （精确）\n", a)
	fmt.Println("  -> 并发 ++ 是读-改-写三步，多 goroutine 读到同一旧值后各自 +1，")
	fmt.Println("     只有最后一次写回生效，其余更新被覆盖丢失")
	fmt.Println("  -> 用 go run -race 运行会在这里报告数据竞争（原子版无告警）")
	return expected
}

// ---------- 实验 2：CAS 无锁更新 + 自旋锁 ----------

func exp2(silent bool) (int64, int64) {
	if !silent {
		fmt.Println("\n========== 2. CAS 无锁更新与自旋锁 ==========")
	}

	// CAS 无锁计数器：比较-交换失败说明别人抢先改了，重读重试
	var v int64
	var retries int64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				for {
					old := atomic.LoadInt64(&v)
					if atomic.CompareAndSwapInt64(&v, old, old+1) {
						break
					}
					atomic.AddInt64(&retries, 1)
				}
			}
		}()
	}
	wg.Wait()
	if silent {
		return v, retries
	}
	fmt.Printf("  CAS 无锁计数: %d（精确）  CAS 失败重试 %d 次\n", v, retries)
	fmt.Println("  -> CAS = 先比较再交换（底层一条指令）。old 变了说明被人抢先，重读重试")
	fmt.Println("     无锁：不加锁、只靠原子重试，高竞争时浪费一点 CPU 但不会阻塞")

	// CAS 自旋锁：拿锁 = CAS(0→1) 成功；放锁 = Store(0)
	var lock int32
	var protected int64
	var wg2 sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			for j := 0; j < perG; j++ {
				for !atomic.CompareAndSwapInt32(&lock, 0, 1) {
					runtime.Gosched()
				}
				protected++
				atomic.StoreInt32(&lock, 0)
			}
		}()
	}
	wg2.Wait()
	if !silent {
		fmt.Printf("  自旋锁保护计数: %d（精确，临界区不长，自旋比休眠划算）\n", protected)
	}
	return v, retries
}

// ---------- 实验 3：内存屏障 ----------

func exp3(silent bool) int64 {
	if !silent {
		fmt.Println("\n========== 3. 内存屏障：release Store / acquire Load ==========")
	}
	const rounds = 20000
	var data int64
	var ready int32
	var violations int64
	done := make(chan struct{})

	// 消费者：等 ready 就绪（acquire Load），再读 data，然后复位令牌
	go func() {
		for i := 0; i < rounds; i++ {
			for atomic.LoadInt32(&ready) == 0 {
				runtime.Gosched()
			}
			if atomic.LoadInt64(&data) != 42 {
				atomic.AddInt64(&violations, 1)
			}
			atomic.StoreInt32(&ready, 0)
		}
		close(done)
	}()

	// 生产者：先写 data，再置 ready（release Store）
	for i := 0; i < rounds; i++ {
		data = 42
		atomic.StoreInt32(&ready, 1)
		for atomic.LoadInt32(&ready) == 1 {
			runtime.Gosched()
		}
	}
	<-done

	if !silent {
		fmt.Printf("  release/acquire 握手 %d 轮，消费者读到脏数据 %d 次\n", rounds, violations)
		fmt.Println("  -> atomic.Store = release 屏障：它之前的写入必须先对外可见")
		fmt.Println("     atomic.Load  = acquire 屏障：它之后的读取必须在屏障之后发生")
		fmt.Println("     用原子读写 ready，就保证了 data 与 ready 的顺序（happens-before）")
		fmt.Println("     若 ready 换成普通变量，编译器/CPU 可能重排顺序，-race 会报竞争")
	}
	return violations
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 原子操作 = 硬件支持的不可分割的读-改-写（或比较-交换）")
	fmt.Println("   在 arm64 上实测编译为：AddInt64 -> LDADDAL（自带 acquire+release）")
	fmt.Println("   CAS -> LDAXR + STLXR 独占对；Load -> LDAR(acquire)；Store -> STLR(release)")
	fmt.Println("2. CAS 失败就重读重试：无锁结构的通用套路（乐观并发）")
	fmt.Println("3. 原子操作自带内存屏障：Store 是 release、Load 是 acquire，")
	fmt.Println("   提供 happens-before 顺序保证，无需手写栅栏指令")
}

// ---------- JSON ----------

func jsonOut() {
	var c, a int64
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c++
				atomic.AddInt64(&a, 1)
			}
		}()
	}
	wg.Wait()
	expected := int64(goroutines * perG)

	casVal, retries := exp2(true)

	out := map[string]interface{}{
		"exp1": map[string]interface{}{
			"expected":   expected,
			"non_atomic": c,
			"lost":       expected - c,
			"atomic":     a,
		},
		"exp2": map[string]interface{}{
			"cas":     casVal,
			"retries": retries,
		},
		"exp3": map[string]interface{}{
			"rounds":     20000,
			"violations": exp3(true),
		},
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		jsonOut()
		return
	}
	exp1()
	exp2(false)
	exp3(false)
	summary()
}
