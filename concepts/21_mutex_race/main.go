package main

// 21_mutex_race：竞态与锁
//
// 100 个 goroutine 并发给同一个计数器各加 1，理论结果永远是 100。
// 本程序用三种方式做同一件事，跑给你看差别：
//   1. 无锁 counter++        —— counter++ 不是原子操作（读-改-写三步），
//      并发交错会互相覆盖，结果 < 100 且每次运行都不同（丢失更新）
//   2. sync.Mutex 加锁       —— 临界区串行化，精确 100
//   3. atomic.AddInt64       —— 硬件级原子指令，精确 100，无锁更轻
//
// 运行：go run main.go          普通运行（无锁结果每次都不同）
//       go run -race main.go   竞态检测器会报告 DATA RACE（针对无锁版本）
//       go run main.go -json   输出三方计数的 JSON（供 viz.py 画图）

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

const goroutines = 100 // 100 个 goroutine，每个加 1，期望值 100

// incUnlocked：无锁并发 counter++ —— 演示丢失更新
func incUnlocked(wg *sync.WaitGroup) (final int64) {
	var counter int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter++ // 编译后是 读→加1→写回 三条指令，交错执行会覆盖彼此的写入
		}()
	}
	wg.Wait()
	return counter
}

// incMutex：sync.Mutex 把"读-改-写"包成临界区，串行化后不再丢更新
func incMutex(wg *sync.WaitGroup) (final int64) {
	var (
		mu      sync.Mutex
		counter int64
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			counter++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return counter
}

// incAtomic：atomic.AddInt64 用一条硬件原子指令完成读改写，无需加锁
func incAtomic(wg *sync.WaitGroup) (final int64) {
	var counter int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			atomic.AddInt64(&counter, 1)
		}()
	}
	wg.Wait()
	return counter
}

func main() {
	jsonOut := flag.Bool("json", false, "输出三方计数的 JSON（供 viz.py 使用）")
	flag.Parse()

	var wg sync.WaitGroup
	unlocked := incUnlocked(&wg)
	mutexed := incMutex(&wg)
	atomiced := incAtomic(&wg)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(map[string]any{
			"goroutines": goroutines,
			"expect":     goroutines,
			"results": []map[string]any{
				{"name": "无锁 counter++", "value": unlocked},
				{"name": "sync.Mutex", "value": mutexed},
				{"name": "atomic.AddInt64", "value": atomiced},
			},
		})
		return
	}

	lost := goroutines - int(unlocked)
	fmt.Println("========== 100 个 goroutine 并发加 1，期望值 100 ==========")
	fmt.Printf("① 无锁 counter++      结果 = %3d  丢失更新 %d 次（每次运行都不同！）\n", unlocked, lost)
	fmt.Printf("② sync.Mutex 加锁     结果 = %3d  精确\n", mutexed)
	fmt.Printf("③ atomic.AddInt64     结果 = %3d  精确\n", atomiced)
	fmt.Println("\n结论：counter++ 不是原子的；要么用锁把临界区串行化，要么用 atomic 原子指令。")
	fmt.Println("用 go run -race main.go 可看到竞态检测器对版本① 报 WARNING: DATA RACE。")
}
