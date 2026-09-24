package main

// 29_reflect_call：反射动态创建实例与动态调用
//
// reflect 不仅能"看"（27_reflect_basics），还能"造"与"调"：
//   1. reflect.New(T)：按类型造出一个指向零值的指针，Elem() 解引用后可写字段
//   2. MethodByName("Greet")：按名字字符串取出方法（这是插件/框架的基础能力）
//   3. Call(args)：参数与返回值都是 []reflect.Value，需要手工打包与拆包
//   4. 普通函数同理：reflect.ValueOf(fn).Call(...) 动态调用任意函数
//
// 代价：反射调用比直接调用慢约一个数量级，且失去编译期检查——名字写错要跑到运行时才 panic。
//
// 运行：go run main.go

import (
	"fmt"
	"reflect"
)

// Greeter 演示对象：一个字段 + 一个方法
type Greeter struct {
	Prefix string
}

// Greet 带参数、带返回值的方法（签名是 MethodByName 调用的目标）
func (g Greeter) Greet(name string) string {
	return g.Prefix + name
}

// add 演示动态调用普通函数
func add(a, b int) int {
	return a + b
}

func main() {
	println("========== 1. reflect.New：按类型动态创建实例 ==========")
	greeterType := reflect.TypeOf(Greeter{})        // 拿到类型（不需要任何实例）
	ptr := reflect.New(greeterType)                 // *Greeter，指向零值的新实例
	inst := ptr.Elem()                              // 解引用得到 Greeter 本体（可 Set）
	inst.FieldByName("Prefix").SetString("Dr. ")    // 按字段名写字段
	g := ptr.Interface().(*Greeter)                  // 还原为普通值，可继续静态使用
	println("动态创建:", g.Greet("Who"), "| 字段 =", g.Prefix == "Dr. ")

	println("\n========== 2. MethodByName + Call：动态调用方法 ==========")
	method := ptr.MethodByName("Greet") // 注意：在指针值上取方法（含值接收者方法集合）
	println("方法存在:", method.IsValid())
	args := []reflect.Value{reflect.ValueOf("Watson")} // 参数必须逐个包装成 reflect.Value
	results := method.Call(args)                       // 返回值也是 []reflect.Value
	greeting := results[0].String()                    // 拆包：按位置取出第 0 个返回值
	println("动态调用结果:", greeting)

	println("\n========== 3. 动态调用普通函数 ==========")
	fn := reflect.ValueOf(add)                        // 函数也是一等公民，可转 reflect.Value
	typ := fn.Type()                                  // 动态检查签名：参数个数/类型
	println("签名:", typ.String(), "| in =", typ.NumIn(), "| out =", typ.NumOut())
	sum := fn.Call([]reflect.Value{reflect.ValueOf(2), reflect.ValueOf(40)})[0].Int()
	println("add(2, 40) =", sum)

	println("\n========== 4. 对照：直接调用 vs 反射调用 ==========")
	direct := Greeter{Prefix: "Dr. "}.Greet("Watson") // 编译期就绑定的直接调用
	println("直接调用:", direct, "| 与反射结果一致:", direct == greeting)

	const n = 100000
	for i := 0; i < n; i++ {
		Greeter{Prefix: "Dr. "}.Greet("Watson") // 直接调用 10 万次
	}
	dyn := reflect.ValueOf(Greeter{Prefix: "Dr. "}).MethodByName("Greet")
	for i := 0; i < n; i++ {
		dyn.Call([]reflect.Value{reflect.ValueOf("Watson")}) // 反射调用 10 万次
	}
	fmt.Println("两种方式各执行 100000 次（数量级差异请在基准测试中量化）")
}
