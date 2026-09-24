package main

// 11_worker_pool：Goroutine 池（channel + WaitGroup 的经典组合）
//
// 模式骨架只有四步：
//   1. 生产者把任务投进 jobs chan，投完 close(jobs) —— "没有更多任务了"
//   2. 固定 N 个 worker 从 jobs 里 for range 取任务（chan 关闭后循环自动退出）
//   3. 处理结果写进 results chan
//   4. 收集 goroutine for range results 直到 close(results)，主 goroutine WaitGroup 等它
//
// 优雅关闭的关键：close 只能由"发送方"调用，且只能关一次；
// 每个 goroutine 一条 WaitGroup 计数，Done 与 Add 严格配对。
//
// 运行：go run main.go

import (
	"fmt"
	"sync"
)

// Job 是一个任务：ID 编号，Input 是要计算的输入
type Job struct {
	ID    int
	Input int
}

// Result 是任务的计算结果
type Result struct {
	JobID int
	Value int
}

// process 是"业务逻辑"：这里算立方，故意耗时随输入变化让 worker 抢任务
func process(j Job) Result {
	return Result{JobID: j.ID, Value: j.Input * j.Input * j.Input}
}

func main() {
	const (
		numJobs    = 100 // 任务总数
		numWorkers = 5   // 固定 worker 数：并发度由池子大小决定，而不是任务数
	)

	jobs := make(chan Job, numJobs)     // 带缓冲：生产者一次性投完不用等 worker
	results := make(chan Result, numJobs) // 带缓冲：worker 写结果基本不阻塞

	// ---- 启动 5 个 worker：扇出 ----
	var workers sync.WaitGroup
	for w := 1; w <= numWorkers; w++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			// for range chan：一直接收，直到 chan 被关闭且缓存排空
			for j := range jobs {
				fmt.Printf("worker %d 处理任务 %d (输入 %d)\n", id, j.ID, j.Input)
				results <- process(j)
			}
		}(w) // 注意：worker 编号通过参数传入，避免闭包共享循环变量
	}

	// ---- 生产者：投 100 个任务后关闭 jobs ----
	for i := 1; i <= numJobs; i++ {
		jobs <- Job{ID: i, Input: i}
	}
	close(jobs) // 信号："没有更多任务"，worker 的 for range 随即逐个退出

	// workers.Wait 保证所有 worker 退出后，才能安全关闭 results（发送方关闭原则）
	workers.Wait()
	close(results)

	// ---- 收集者：独立 goroutine 汇总结果 ----
	var collector sync.WaitGroup
	got := make([]Result, 0, numJobs)
	// WaitGroup.Go（Go 1.25+）：自动 Add(1)、goroutine 内自动 Done，替代 Add+go+Done 三行
	collector.Go(func() {
		for r := range results {
			got = append(got, r)
		}
	})
	collector.Wait()

	// ---- 校验：100 个结果全部正确且无重复 ----
	seen := make(map[int]bool)
	ok := len(got) == numJobs
	for _, r := range got {
		if seen[r.JobID] || r.Value != r.JobID*r.JobID*r.JobID {
			ok = false
		}
		seen[r.JobID] = true
	}
	fmt.Printf("\n共 %d 个 worker，收到 %d/%d 个结果，全部正确且无重复: %v\n",
		numWorkers, len(got), numJobs, ok)
	fmt.Println("注意：哪个 worker 处理哪个任务是调度决定的，每次运行的分配都不同——结果集相同，过程日志不同。")
}
