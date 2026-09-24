package main

// 07_closure_trap：Goroutine 闭包陷阱——循环变量被共享
//
// 闭包捕获的是"变量"（引用），不是"值"（拷贝）。当多个 goroutine 捕获同一个
// 循环变量时，它们看到的是同一个地址：循环跑完后变量已变成最后一个值，
// 所有 goroutine 打印出来的全相同。
//
// 版本说明：Go 1.22 起（go.mod 声明 go >= 1.22），for 循环变量每次迭代都是
// 新变量，原生的 `for _, v := range xs` + `go func(){ use(v) }()` 已经安全，
// 本仓库 go.mod 声明的是 1.26，原生写法不再复现陷阱。但"闭包捕获引用而非值"
// 这条语言规则从未变：只要变量是显式共享的（如下面的 loopVar），陷阱照样发生。
//
// 运行：go run main.go

import (
	"fmt"
	"sync"
)

func main() {
	ids := []int{1, 2, 3, 4, 5}

	// ========== 1. 陷阱模拟：所有闭包捕获同一个 loopVar ==========
	// 用显式共享变量 loopVar 复现"1.22 之前的世界"：
	// go func 捕获的是 &loopVar，循环结束时 loopVar == 5，全部输出 5。
	fmt.Println("========== 1. 陷阱：闭包共享同一个 loopVar ==========")
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int]int{} // 记录每个值被打印了几次
	loopVar := 0
	for _, v := range ids {
		loopVar = v // 每次迭代重写同一个变量
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			seen[loopVar]++ // 读到的都是循环结束后的最终值
			mu.Unlock()
		}()
	}
	wg.Wait()
	fmt.Println("各值出现次数（期望各 1 次）：")
	for _, v := range ids {
		fmt.Printf("  值 %d 出现 %d 次\n", v, seen[v])
	}

	// ========== 2. 修正 1：参数传递（按值拷贝进 goroutine） ==========
	fmt.Println("\n========== 2. 修正 1：参数传递拷贝值 ==========")
	var wg2 sync.WaitGroup
	got2 := make([]int, 0, len(ids))
	var mu2 sync.Mutex
	for _, v := range ids {
		wg2.Add(1)
		go func(n int) { // v 的当前值在 go 语句执行时被拷贝为 n
			defer wg2.Done()
			mu2.Lock()
			got2 = append(got2, n)
			mu2.Unlock()
		}(v)
	}
	wg2.Wait()
	fmt.Println("收到的值（顺序不定，共 5 个）：", len(got2), "个")

	// ========== 3. 修正 2：循环内重赋值（每次迭代一个新变量） ==========
	fmt.Println("\n========== 3. 修正 2：循环内重赋值 ==========")
	var wg3 sync.WaitGroup
	count3 := map[int]int{}
	var mu3 sync.Mutex
	for _, v := range ids {
		shared := v // 新的同名变量：每轮迭代都是新地址，闭包各捕获各的
		wg3.Add(1)
		go func() {
			defer wg3.Done()
			mu3.Lock()
			count3[shared]++
			mu3.Unlock()
		}()
	}
	wg3.Wait()
	ok3 := true
	for _, v := range ids {
		if count3[v] != 1 {
			ok3 = false
		}
	}
	fmt.Println("每个值恰好出现 1 次:", ok3)

	// ========== 4. 修正 3：shadow（v := v，1.22 之前的经典写法） ==========
	fmt.Println("\n========== 4. 修正 3：shadow v := v ==========")
	var wg4 sync.WaitGroup
	sum4 := 0
	var mu4 sync.Mutex
	for _, v := range ids {
		v := v // 遮蔽外层 v：函数体内新建局部 v，闭包捕获的是这个新 v
		wg4.Add(1)
		go func() {
			defer wg4.Done()
			mu4.Lock()
			sum4 += v
			mu4.Unlock()
		}()
	}
	wg4.Wait()
	fmt.Println("1+2+3+4+5 =", sum4)

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 闭包捕获变量的引用而非值：共享同一变量 => 看到同一份最终值")
	fmt.Println("2. go.mod 声明 go >= 1.22 后原生循环变量每次迭代都是新变量，原生写法已安全")
	fmt.Println("3. 但显式共享变量（loopVar/struct 字段/包级变量）的道理不变，仍需三种修正之一")
	fmt.Println("4. 三种修正本质相同：让每个 goroutine 捕获一个独立的存储位置")
}
