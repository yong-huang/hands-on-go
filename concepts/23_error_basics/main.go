package main

// 23_error_basics：error 基础——哨兵错误、%w 包装与 errors.Is 判定
//
// Go 的 error 只是一个内置接口：type error interface { Error() string }。
// 本程序演示四件事：
//   1. 哨兵 error：errors.New 定义的包级错误值，用 == 或 errors.Is 比对
//   2. 错误包装：fmt.Errorf 的 %w 动词保留错误链，errors.Is 能穿透包装
//   3. error 是接口：任何实现了 Error() string 的类型都是 error（自定义类型对比）
//   4. 错误必须被处理或显式忽略（_），"声明不接"直接是编译错
//
// 运行：go run main.go

import (
	"errors"
	"fmt"
)

// ErrDivisionByZero 是哨兵错误：包级变量，调用方拿它做判定锚点
var ErrDivisionByZero = errors.New("division by zero")

// safeDivide 除零时不返回裸哨兵，而是用 %w 包装：既保留原文，又保留判定链
func safeDivide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, fmt.Errorf("safeDivide(%g, %g): %w", a, b, ErrDivisionByZero)
	}
	return a / b, nil
}

// OpsError 是自定义 error 类型：error 本质是接口，实现 Error() string 即可
type OpsError struct {
	Op   string // 出错的操作名
	Code int    // 业务错误码
}

func (e *OpsError) Error() string { return fmt.Sprintf("%s failed (code %d)", e.Op, e.Code) }

func main() {
	fmt.Println("========== 1. 正常路径：err == nil ==========")
	// idiom：if 带初始化语句，err 的作用域被压进 if/else
	if v, err := safeDivide(10, 4); err != nil {
		fmt.Println("不应到达:", err)
	} else {
		fmt.Printf("10 / 4 = %g（err == nil，走正常分支）\n", v)
	}

	fmt.Println("\n========== 2. 哨兵错误 + %w 包装 + errors.Is 判定 ==========")
	_, err := safeDivide(1, 0)
	fmt.Println("err 本体:", err) // 带 safeDivide 前缀，不再是裸哨兵
	fmt.Println("== 裸比对（已失效）:", err == ErrDivisionByZero)
	fmt.Println("errors.Is 穿透包装（推荐）:", errors.Is(err, ErrDivisionByZero))
	if errors.Is(err, ErrDivisionByZero) {
		fmt.Println("→ 判定为除零：请把分母换成非零值后重试")
	}

	fmt.Println("\n========== 3. error 是接口：自定义类型也是 error ==========")
	var err2 error = &OpsError{Op: "query", Code: 404}
	fmt.Printf("err2 = %v（动态类型 %T，实现了 Error() string）\n", err2, err2)
	fmt.Println("errors.Is 对自定义类型不误判哨兵:", !errors.Is(err2, ErrDivisionByZero))

	fmt.Println("\n========== 4. 错误必须被处理或显式忽略 ==========")
	// 下面的写法是编译错：err declared and not used
	// v, err3 := safeDivide(9, 3) // err3 未使用 → 编译不过
	// 显式忽略：用 _ 丢弃 err，表明"我看过这里，决定不处理"
	v, _ := safeDivide(9, 3)
	fmt.Printf("v, _ := safeDivide(9, 3) → v = %g（err 被 _ 显式丢弃）\n", v)

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 哨兵 error 是包级锚点：errors.New 定义，调用方 errors.Is 判定")
	fmt.Println("2. %w 包装错误链：err == 哨兵失效，errors.Is 仍能穿透命中")
	fmt.Println("3. error 只是接口：任何类型实现 Error() string 都能当 error 用")
	fmt.Println("4. 错误不能悬空：要么 if err != nil 处理，要么 _ 显式忽略")
}
