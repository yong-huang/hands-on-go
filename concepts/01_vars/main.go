package main

// 01_vars：变量、常量与类型推断
//
// Go 是静态类型语言，但写起来几乎不用手写类型——编译器的类型推断替你做了。
// 本程序演示四件事：
//   1. var 显式声明 vs := 短声明（以及各自的使用边界）
//   2. 编译器推断：字面量决定默认类型（100 是 int 不是 int64）
//   3. 常量：显式类型 vs 无类型常量（无类型常量的"延迟定型"是独有的）
//   4. iota：Go 里唯一的枚举姿势
//
// 运行：go run main.go

func main() {
	println("========== 1. var 显式声明 vs := 短声明 ==========")
	// var 声明：可以只声明不初始化（零值兜底），可以在包级使用
	var a int = 10      // 显式类型
	var b = 3.14        // 省略类型：由右值推断为 float64
	var c string        // 只声明不赋值 → 零值 ""
	d := true           // 短声明：只能在函数内用，且必须立刻初始化
	println("a =", a, "| b =", b, "| c 零值 =", c == "" /*打印布尔比较，避免空串看不见*/, "| d =", d)

	println("\n========== 2. 类型推断：字面量的默认类型 ==========")
	e := 100        // 整数字面量默认 int（64 位平台上 8 字节）
	f := 1.5        // 小数默认 float64（没有 float32 推断）
	g := "go"       // 字符串
	h := 'A'        // 单引号是 rune（int32 别名），不是 string！
	i := byte(200)  // 显式转成 uint8 别名 byte
	println("e =", e, "| f =", f, "| g =", g)
	println("h 是 rune(int32):", h == 65, "| i 是 byte(uint8):", i == 200)
	// 下面的推断陷阱：
	// var j float64 = 100   // OK：无类型常量 100 可以赋给 float64
	// k := 100; var l float64 = k  // 编译错：k 已定型为 int，不能赋给 float64

	println("\n========== 3. 常量：显式类型 vs 无类型常量 ==========")
	const pi = 3.14159           // 无类型常量：编译期常量，类型"延迟定型"
	const maxInt int = 1 << 31   // 显式类型常量
	var f1 float64 = pi          // OK：pi 此处"变成" float64
	// var n int = pi            // 编译错：3.14159 不是整数
	var n2 int = maxInt / 2      // 有类型常量参与运算保持 int
	println("pi 赋给 float64:", f1 > 3.14 && f1 < 3.15, "| maxInt/2 =", n2)

	println("\n========== 4. iota：枚举 ==========")
	type Weekday int
	const (
		Sunday Weekday = iota // 0
		Monday                // 1（iota 逐行 +1，后面的常量省略表达式即重复上一行）
		Tuesday               // 2
	)
	const (
		KB = 1 << (10 * iota) // 1<<0 = 1
		MB                    // 1<<10
		GB                    // 1<<20
	)
	println("Sunday=0 Monday=1 Tuesday=2:", int(Sunday) == 0 && int(Monday) == 1 && int(Tuesday) == 2)
	println("KB=1 MB=1024 GB=1048576:", KB == 1 && MB == 1024 && GB == 1048576)

	println("\n========== 总结 ==========")
	println("1. := 只能在函数内，var 到处可用；未初始化的变量有零值")
	println("2. 字面量推断默认类型：整数→int，小数→float64，单引号→rune")
	println("3. 无类型常量类型延迟定型，是它既能赋给 int 也能赋给 float64 的原因")
	println("4. iota 在 const 块内逐行 +1，省略表达式即重复上一行")
}
