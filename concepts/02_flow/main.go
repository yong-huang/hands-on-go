package main

// 02_flow：流程控制——for 三种形态、if 带初始化语句、无表达式 switch
//
// Go 的流程控制刻意精简：只有 for 一种循环（却能写出三种形态），
// if 和 switch 都可以带一条初始化语句，switch 不写表达式时就是一串
// "true 条件"的判定链——经典 FizzBuzz 正好把这些一次跑完。
//
// 运行：go run main.go

import (
	"fmt"
	"strings"
)

// fizzBuzz 用无表达式 switch 判定一个数：15 的倍数→FizzBuzz，3→Fizz，5→Buzz
func fizzBuzz(n int) string {
	// switch 不带表达式：每个 case 是一个布尔表达式，自上而下取第一个为 true 的分支
	switch {
	case n%15 == 0: // 既是 3 又是 5 的倍数，必须最先判
		return "FizzBuzz"
	case n%3 == 0:
		return "Fizz"
	case n%5 == 0:
		return "Buzz"
	default:
		return fmt.Sprint(n)
	}
}

func main() {
	fmt.Println("========== 1. FizzBuzz 1..15：无表达式 switch ==========")
	results := make([]string, 0, 15)
	// 形态一：C 风格三段式 for（init; cond; post）
	for i := 1; i <= 15; i++ {
		results = append(results, fizzBuzz(i))
	}
	fmt.Println(strings.Join(results, " "))

	fmt.Println("\n========== 2. for 的三种形态 ==========")
	// 形态一已在上面用过：for i := 1; i <= 15; i++ { ... }
	// 形态二：while 式——只留条件，前两个分号可以全省
	count := 0
	n := 1
	for n <= 15 { // 等价于 for ; n <= 15 ; {
		if n%3 == 0 { // 顺手数 Fizz
			count++
		}
		n++
	}
	fmt.Println("while 式循环数出 Fizz 的个数 =", count)

	// 形态三：for range——遍历切片，i 是下标，s 是元素
	buzzes := []string{}
	for i, s := range results {
		if strings.Contains(s, "Buzz") {
			buzzes = append(buzzes, fmt.Sprintf("results[%d]=%s", i, s))
		}
	}
	fmt.Println("for range 找到含 Buzz 的位置：")
	for _, line := range buzzes {
		fmt.Println(" ", line)
	}

	fmt.Println("\n========== 3. if 带初始化语句 vs 普通判断 ==========")
	for i := 1; i <= 3; i++ {
		// if 可以带一条初始化语句，变量 r 只在 if/else 作用域内可见
		if r := i % 3; r == 0 {
			fmt.Printf("i=%d: r==0 → Fizz（r 只在这个 if/else 里可见）\n", i)
		} else {
			fmt.Printf("i=%d: r=%d ≠ 0 → 原样输出 %d\n", i, r, i)
		}
	}

	fmt.Println("\n========== 4. switch 带初始化语句 + fallthrough ==========")
	for _, n := range []int{15, 9, 10, 7} {
		// switch 也可以带初始化语句；case 默认不穿透，fallthrough 强制进下一支
		switch m := n % 5; {
		case m == 0:
			fmt.Printf("n=%d: n%%5=%d → Buzz 类\n", n, m)
		case n%3 == 0:
			fmt.Printf("n=%d: n%%5=%d≠0 但 n%%3==0 → Fizz 类\n", n, m)
		default:
			fmt.Printf("n=%d: 普通数（m=%d 未命中任何分支）\n", n, m)
		}
	}

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. Go 只有 for 一种循环：三段式 / while 式 / for range 三种形态")
	fmt.Println("2. if/switch 的初始化语句把变量作用域压到最小，是 Go 的惯用法")
	fmt.Println("3. 无表达式 switch 是 if-else 链的整洁写法，case 自上而下短路")
	fmt.Println("4. case 默认不穿透；要穿透必须显式写 fallthrough")
}
