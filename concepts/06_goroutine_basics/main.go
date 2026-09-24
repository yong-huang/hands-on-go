package main

// 06_goroutine_basics：go 关键字启动 goroutine 与 sync.WaitGroup 等待
//
// go 关键字让"并发一个函数调用"只值 5 个字母，但 main 不会等它们——
// main 一退出，所有还没跑完的 goroutine 直接被带走。sync.WaitGroup 就是
// 标准库里"等一批 goroutine 干完活"的计数器：Add 加计数、Done 减计数、
// Wait 阻塞到归零。本程序演示：
//   1. go 关键字启动 10 个 goroutine，参数传递编号（正确姿势）
//   2. 两种 Add 姿势：循环外一次性 Add(10) / 循环内逐个 Add(1)
//   3. 没有 Wait 会怎样（注释演示，不真跑）
//   4. goroutine 完成顺序不确定
//
// 运行：go run main.go

import (
	"fmt"
	"sync"
)

// worker 是每个 goroutine 要跑的函数：编号通过参数传进来
//
// 注意：这里用"参数传递"而不是直接在函数体里引用循环变量 i。
// 直接引用 i 的写法是经典闭包陷阱（所有 goroutine 可能共享同一个 i），
// 那个坑留到 07_closure_trap 专门拆解；本实验先固定用正确姿势：
//
//	go worker(i, wg)   // i 在 go 语句求值，拷贝进 goroutine，安全
func worker(id int, wg *sync.WaitGroup) {
	defer wg.Done() // 2. Done：计数器 -1；用 defer 保证一定执行（哪怕 panic 也会先跑）
	fmt.Printf("  goroutine %d 完成\n", id)
}

func main() {
	fmt.Println("========== 1. 循环外一次性 Add(10) ==========")
	var wg sync.WaitGroup
	wg.Add(10) // Add 在启动前一次性加 10：适合"启动数量已知"的场景
	for i := 1; i <= 10; i++ {
		go worker(i, &wg) // go 关键字：立即返回，不阻塞；&wg 必须传指针
	}
	wg.Wait() // 阻塞直到计数器归零（每个 Done 减 1）
	fmt.Println("main: 10 个 goroutine 全部完成（完成顺序每次运行可能不同）")

	// 反面教材（不真跑）：
	// 若删掉上面的 wg.Wait()，main 大概率在 goroutine 还没被调度时就执行完并退出，
	// 输出可能一个 "goroutine x 完成" 都没有——go 语句只负责"启动"，不负责"等待"。

	fmt.Println("\n========== 2. 循环内逐个 Add(1) ==========")
	var wg2 sync.WaitGroup
	for i := 1; i <= 10; i++ {
		wg2.Add(1) // Add 放循环内、go 语句之前：适合"边生产边计数"的场景
		go worker(i, &wg2)
	}
	wg2.Wait()
	fmt.Println("main: 第二批 10 个也全部完成")

	// 计数规则补充（写在注释里，不在运行时验证）：
	// Add/Done 必须严格配对，计数为负会 panic：
	//   "sync: negative WaitGroup counter"
	// Wait 之后想复用 WaitGroup，必须等上一轮 Wait 返回再 Add（或用新实例）。

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. go f() 只是启动并发，main 不等它；不等待 = goroutine 随 main 一起消失")
	fmt.Println("2. WaitGroup 三件套配对：Add(n) 在 go 之前，Done 用 defer，Wait 阻塞到归零")
	fmt.Println("3. Add(10) 一次到位 vs 循环内 Add(1)，两种姿势都对，关键是先 Add 后 go")
	fmt.Println("4. goroutine 的调度顺序没有保证，完成顺序每次运行都可能不同")
}
