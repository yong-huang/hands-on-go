package main

// 25_panic_recover：panic 与恢复
//
// panic 是"不可绕过的失败"：沿调用栈向上传播，逐层执行 defer。
// recover 只在 defer 函数里生效，把 panic 转回正常控制流。
// 本程序演示四件事：
//   1. safeCall 惯用法：defer + recover 把 panic 值转成 error 返回
//   2. 未捕获 panic 会终止进程（注释说明，不真崩）
//   3. 没有 panic 时 recover() 返回 nil
//   4. 子 goroutine 的 panic 主 goroutine 接不住，必须子 goroutine 自己 recover
//
// 运行：go run main.go

import "fmt"

// safeCall 把任意函数包起来：panic 被拦截并转成 error（Go 社区惯用法）。
func safeCall(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	fn()
	return nil
}

func risky() {
	panic("boom")
}

func calm() {}

// noPanic：没有 panic 的函数里调用 recover() 只会得到 nil，不会有副作用。
func noPanic() {
	defer func() {
		r := recover()
		fmt.Println("defer 中 recover():", r, "| 是 nil:", r == nil)
	}()
	fmt.Println("noPanic 正常运行，没人 panic")
}

func main() {
	fmt.Println("========== 1. safeCall：panic → error 惯用法 ==========")
	err1 := safeCall(risky) // panic 在 safeCall 的 defer 里被拦下
	fmt.Println("risky():", err1, "| 已转成 error:", err1 != nil)
	err2 := safeCall(calm)
	fmt.Println("calm():", err2, "(正常返回，err 为 nil)")
	fmt.Println("main 仍在继续执行 —— 捕获后程序不崩")

	fmt.Println("\n========== 2. 未捕获 panic：进程直接终止 ==========")
	// 取消下面的注释，panic 沿栈上抛、无人 recover，进程以非零码退出：
	//   panic: boom
	//   goroutine 1 [running]: ... （栈回溯）
	//   exit status 2
	// _ = risky 之前已通过 safeCall 验证过它，这里不真崩。
	fmt.Println("（见注释：panic 未被 recover → defer 依次执行 → 打印栈回溯 → exit 2）")

	fmt.Println("\n========== 3. 无 panic 时 recover() 返回 nil ==========")
	noPanic()
	fmt.Println("（noPanic 返回后，其 defer 已在上一行之前执行完毕）")

	fmt.Println("\n========== 4. 跨 goroutine：主 goroutine 接不住子 goroutine 的 panic ==========")
	// panic 只沿“当前 goroutine”的栈传播。下面的写法若去掉子 goroutine 内部的
	// recover，整个进程会崩，main 里任何 defer/recover 都无济于事：
	//   go func() { panic("child died") }()   // 主 goroutine 接不住！
	done := make(chan error, 1)
	go func() {
		defer func() { // 子 goroutine 必须自己兜底
			if r := recover(); r != nil {
				done <- fmt.Errorf("子 goroutine 内部捕获: %v", r)
			}
		}()
		panic("child died")
	}()
	fmt.Println(<-done)
	fmt.Println("程序正常收尾 —— 子 goroutine 内部 recover 后主流程安然无恙")
}
