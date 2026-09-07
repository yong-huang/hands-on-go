package main

// 项目 12：sync.Once 底层——atomic.Bool 双检 + 内存屏障
//
// sync.Once 只有两个字段：done（atomic.Bool，1 字节，刻意放第一个）和 m（Mutex）。
// Do 的语义是"f 只跑一次，且所有 Do 返回时 f 已经结束"，实现分两条路：
//   - 快路径：if !done.Load() → 直接返回（done 已置位，全部并发调用者都走这里）
//   - 慢路径：拿到 done==0 的人抢 mutex，抢到的跑 f，跑完才 done.Store(true)
// 关键点在注释里：done.Store 必须发生在 f 返回之后 —— 这是 release 语义，
// 保证看到 done==1 的 goroutine 也能看到 f 写入的所有副作用（happens-before）。
//
// 实验：
//   1. 100 个 goroutine 并发 Do：f 只执行一次
//   2. 快慢路径：f 睡 30ms 期间，后来的调用者全部阻塞在慢路径；之后走快路径
//   3. 内存屏障：done 置位前后，其它 goroutine 一定能读到 f 写的共享变量
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// 读 Once 的 done 字段：done 是第一个字段、atomic.Bool 占 1 字节，
// 直接把 *Once 按 *atomic.Bool 解读即可（只读，保持原子性）。
func readDone(o *sync.Once) bool {
	return (*atomic.Bool)(unsafe.Pointer(o)).Load()
}

// ---------- 实验 1：100 并发，只执行一次 ----------

func exp1() map[string]interface{} {
	pr("========== 1. 100 个 goroutine 并发 Do：f 只执行一次 ==========\n")
	var once sync.Once
	var executed int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			once.Do(func() {
				atomic.AddInt32(&executed, 1)
				time.Sleep(3 * time.Millisecond)
			})
		}()
	}
	close(start)
	wg.Wait()
	pr("   并发 100 个 Do 之后：f 实际执行次数 = %d\n", executed)
	pr("  → 只有抢到慢路径锁的那一个跑 f，其余 99 个在锁上排队等它结束\n")
	return map[string]interface{}{"executed": executed}
}

// ---------- 实验 2：快慢路径 + done 值变化 ----------

type sample struct {
	T_ms  float64 `json:"t_ms"`
	Done  bool    `json:"done"`
}

func exp2() map[string]interface{} {
	pr("\n========== 2. 快慢路径：done 在 f 返回后才置位 ==========\n")
	var once sync.Once
	res := make(map[string]interface{})
	start := time.Now()

	// 慢路径：抢到锁的调用者，f 睡 30ms 模拟慢初始化
	go func() {
		once.Do(func() {
			pr("   [%5.0fms] f 开始（done 此刻 = %v）\n",
				time.Since(start).Seconds()*1000, readDone(&once))
			time.Sleep(30 * time.Millisecond)
			pr("   [%5.0fms] f 结束，此刻才 done.Store(true)\n",
				time.Since(start).Seconds()*1000)
		})
	}()
	// f 还没跑完，另开 4 个并发调用者（都走慢路径阻塞）
	var slowLatList []float64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(2 * time.Millisecond) // 保证 f 已在跑
			t0 := time.Now()
			once.Do(func() {})
			slowLatList = append(slowLatList, float64(time.Since(t0).Microseconds())/1000)
		}()
	}
	wg.Wait()

	// f 结束后：快路径，零等待
	fastT0 := time.Now()
	once.Do(func() {})
	fastLat := float64(time.Since(fastT0).Microseconds()) / 1000
	pr("   done 最终值 = %v\n", readDone(&once))
	pr("   4 个慢路径调用者平均等待 %.1f ms（等 f 跑完）\n", avg(slowLatList))
	pr("   之后的快路径调用只花 %.2f ms\n", fastLat)

	res["slow_avg_ms"] = avg(slowLatList)
	res["fast_ms"] = fastLat
	res["done_final"] = boolToF(readDone(&once))
	return res
}

func boolToF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func avg(xs []float64) float64 {
	var s float64
	for _, v := range xs {
		s += v
	}
	return s / float64(len(xs))
}

// ---------- 实验 3：内存屏障——f 的副作用对后来者可见 ----------

func exp3() map[string]interface{} {
	pr("\n========== 3. 内存屏障：done 置位 = f 副作用已发布 ==========\n")
	var once sync.Once
	var result int32

	seen := make(chan bool, 16)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			once.Do(func() {
				result = 42 // f 的唯一副作用
			})
			seen <- atomic.LoadInt32(&result) == 42
		}()
	}
	wg.Wait()
	close(seen)
	all := 0
	ok := 0
	for v := range seen {
		all++
		if v {
			ok++
		}
	}
	pr("   16 个 goroutine 在 Do 返回后读 result：看到 42 的有 %d/%d\n", ok, all)
	pr("  → done.Store(true) 是 release-store，done.Load 是 acquire-load：\n")
	pr("    读到一个 done==1 的 goroutine，必能读到 f 全部已写入的变量\n")
	return map[string]interface{}{"visible": ok, "total": all}
}

// ---------- 总结 ----------

func summary() {
	pr("\n========== 总结 ==========\n")
	pr("1. Once = atomic.Bool（快路径判据）+ Mutex（慢路径串行化）\n")
	pr("2. done.Store 必须在 f 返回之后：release 语义 + mutex 双重保证\n")
	pr("3. 快路径无锁无等待；慢路径用 mutex 保证等待者看到 f 完整执行\n")
	pr("4. 错误实现 CAS(false,true) 就执行 → 第二个调用者不等 f 结束就返回\n")
}

// ---------- JSON ----------

func jsonOut() {
	res := map[string]interface{}{
		"exp1": exp1(),
		"exp2": exp2(),
		"exp3": exp3(),
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
		jsonOut()
		return
	}
	exp1()
	exp2()
	exp3()
	summary()
}