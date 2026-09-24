package main

// 09_runtime_info：Goroutine 栈与 runtime 信息
//
// runtime 包把"程序自己"暴露给程序自己。本程序演示三件事：
//   1. runtime.Stack(buf, false)：把当前 goroutine 的调用栈打印进缓冲区
//   2. runtime.Goexit()：提前结束当前 goroutine，但 defer 照常执行
//      （对比 os.Exit：整个进程直接死，defer 不跑；panic：可 recover，Goexit 不行）
//   3. goid：runtime 没有公开 API 拿 goroutine id，
//      只能从 runtime.Stack 输出的首行解析——黑客手段，生产请用 context 传值
//
// 运行：go run main.go

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// parseGoid 从 runtime.Stack 输出的首行解析 goroutine id。
// 首行形如 "goroutine 17 [running]:"——第二个字段就是 id。
// 注意：这是"黑客手段"，仅适合调试/日志；生产代码传递身份请用 context.Value。
func parseGoid(stack []byte) uint64 {
	firstLine := string(stack[:strings.IndexByte(string(stack), '\n')])
	fields := strings.Fields(firstLine) // ["goroutine", "17", "[running]:"]
	id, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0 // 解析失败兜底，正常不会走到
	}
	return id
}

// worker 演示 Goexit：defer 照常执行，函数"永不返回"但资源不泄漏。
func worker(exited chan<- struct{}) {
	defer func() {
		fmt.Println("  [worker] defer 执行了——Goexit 不跳过 defer")
		close(exited) // 用 defer 通知 main：Goexit 后仍能到达这里
	}()

	// 对比注释区：
	//   os.Exit(1)：立刻终止整个进程，所有 defer（包括 main 的）都不执行；
	//   return：只是从当前函数返回，外层函数继续跑；
	//   panic：向上传播且可被 recover 捕获；Goexit 不可 recover，只结束当前 goroutine。
	runtime.Goexit() // 以下两行永远到不了，但上面的 defer 会执行

	fmt.Println("  [worker] 这行永远不会打印")
	exited <- struct{}{}
}

func main() {
	fmt.Println("========== 1. runtime.Stack：打印当前 goroutine 调用栈 ==========")
	buf := make([]byte, 1024)
	n := runtime.Stack(buf, false) // false=只当前 goroutine；true=dump 全部（排查泄漏常用）
	lines := strings.Split(string(buf[:n]), "\n")
	head := lines
	if len(head) > 4 {
		head = head[:4]
	}
	fmt.Println(strings.Join(head, "\n"))
	fmt.Println("...(共", n, "字节，栈帧地址每次运行不同)")

	fmt.Println("\n========== 2. runtime.Goexit：提前退出，defer 照常执行 ==========")
	exited := make(chan struct{})
	go worker(exited)
	<-exited // 等 worker 的 defer 跑完，证明 Goexit 不吞 defer
	fmt.Println("  [main] worker 已被 Goexit 结束：defer 执行了，后面的代码没执行")

	fmt.Println("\n========== 3. goid：没有公开 API，只能从栈首行解析 ==========")
	goid := parseGoid(buf[:n])
	fmt.Printf("  解析到当前 goroutine id = %d（每次运行可能不同）\n", goid)
	fmt.Println("  runtime 刻意不暴露 goid：id 会复用、语义不稳定；")
	fmt.Println("  生产代码给 goroutine 带\"身份\"请用 context.WithValue 传递。")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. runtime.Stack(buf, false) 打印当前 goroutine 栈，true 则 dump 全部")
	fmt.Println("2. runtime.Goexit 只结束当前 goroutine：defer 执行、不可 recover")
	fmt.Println("3. goid 只能从栈输出解析（黑客手段），传身份请用 context")
}
