package main

import (
	"errors"
	"fmt"
)

// 04_funcs_defer：函数多返回值与 defer
//
// Go 的函数把"结果"和"出错"捆在一起返回，再用 defer 把清理动作钉在函数出口。
// 本程序演示四件事：
//   1. 多返回值：value, err 惯用法，调用方先判错再取值
//   2. 命名返回值：返回值是函数体内的具名变量，defer 里还能改写它
//   3. 多个 defer 的 LIFO 执行序：注册 1/2/3，执行 3/2/1
//   4. defer + recover：panic 被捕获后程序继续往下跑
//
// 运行：go run main.go

// divide 返回商与余数，同时演示 error 惯用法：出问题时 value 是零值，err 非 nil
func divide(a, b int) (int, int, error) {
	if b == 0 {
		return 0, 0, errors.New("除数不能为 0")
	}
	return a / b, a%b, nil
}

// status 演示命名返回值：ret 是具名变量，defer 在 return 之后仍能改写它
func status(healthy bool) (ret string) {
	defer func() {
		if ret == "warn" { // return 已把值放进了 ret，defer 是最后一次改写机会
			ret = "degraded"
		}
	}()
	if healthy {
		return "ok" // 等价于 ret = "ok"; return
	}
	return "warn"
}

// divideChecked 里的 defer 在 panic 发生时仍会执行——这正是它适合做清理的原因
func divideChecked(a, b int) (result int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("  [recover] 捕获 panic:", r)
			result = -1 // recover 后函数正常返回，命名返回值可在此定稿
		}
	}()
	return a / b // b == 0 时整数除法 panic: runtime error: integer divide by zero
}

func main() {
	fmt.Println("========== 1. 多返回值 + error 惯用法 ==========")
	q, r, err := divide(17, 5)
	fmt.Printf("17 ÷ 5 → 商 %d 余 %d, err = %v\n", q, r, err)
	_, _, err = divide(17, 0)
	fmt.Printf("17 ÷ 0 → 先判错: err = %q（值已不可信，永远是零值）\n", err)

	fmt.Println("\n========== 2. 命名返回值：defer 可改写 ==========")
	fmt.Println("status(true)  =", status(true))
	fmt.Println("status(false) =", status(false), "（return 的 warn 被 defer 改写为 degraded）")

	fmt.Println("\n========== 3. 多个 defer 的 LIFO 执行序 ==========")
	for i := 1; i <= 3; i++ {
		defer fmt.Println("  defer 执行:", i) // 注册顺序 1,2,3
	}
	fmt.Println("  三个 defer 已注册，main 即将结束")

	fmt.Println("\n========== 4. defer + recover 捕获 panic ==========")
	v := divideChecked(10, 0)
	fmt.Println("divideChecked(10, 0) 返回:", v)
	fmt.Println("main 继续执行——panic 被捕获后程序没有中断")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 多返回值：value, err 惯用法，调用方先判 err 再用值")
	fmt.Println("2. 命名返回值是普通变量，defer 在 return 之后仍可改写它")
	fmt.Println("3. defer 按 LIFO 执行：后注册的先执行，注册时参数即求值")
	fmt.Println("4. recover 只在 defer 函数里生效，捕获后函数正常返回、程序继续")
	// main 的三个 defer 在这里按 3,2,1 依次打印
}
