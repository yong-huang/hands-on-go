package main

import (
	"fmt"
	"reflect"
)

// 27_reflect_basics：反射基础
//
// 反射让程序在运行时检视自身的类型与值：struct 有哪些字段、字段是什么类型、
// 值能不能被改写——这些都是编译期写死的知识，反射把它推迟到运行时。
// 本程序演示四件事：
//  1. reflect.TypeOf / ValueOf 双入口：一个看类型，一个看值
//  2. 类型名 vs Kind：Named type 与底层种类的区别
//  3. 遍历结构体字段：Name / Type / Value / Kind 逐列打印
//  4. 可寻址性：ValueOf(变量) 不可 Set，reflect.New + Elem() 才可写
//
// 注意：反射走运行时检查，比直接访问慢一个数量级以上，热路径慎用。
//
// 运行：go run main.go

// Employee 演示用结构体：三个导出字段 + tag
type Employee struct {
	Name   string  `json:"name"`
	Age    int     `json:"age"`
	Salary float64 `json:"salary"`
}

func main() {
	emp := Employee{Name: "Ada", Age: 36, Salary: 9200.5}

	fmt.Println("========== 1. TypeOf / ValueOf 双入口 ==========")
	t := reflect.TypeOf(emp)
	v := reflect.ValueOf(emp)
	fmt.Printf("TypeOf: Name=%s Kind=%s NumField=%d\n", t.Name(), t.Kind(), t.NumField())
	fmt.Printf("ValueOf: Kind=%s CanSet=%v\n", v.Kind(), v.CanSet())

	fmt.Println("\n========== 2. 类型名 vs Kind ==========")
	// 自定义命名类型：类型名是 ID，Kind 才是底层种类
	type ID int
	var id ID = 7
	fmt.Printf("ID: Name=%s Kind=%s\n", reflect.TypeOf(id).Name(), reflect.TypeOf(id).Kind())
	fmt.Printf("map[string]int 的 Kind=%s（Name 为空，匿名类型没有名字）\n",
		reflect.TypeOf(map[string]int{}).Kind())

	fmt.Println("\n========== 3. 遍历结构体字段：Name/Type/Value/Kind ==========")
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fv := v.Field(i)
		fmt.Printf("字段%d: Name=%-7s Type=%-8s Kind=%-8s Value=%v tag=%q\n",
			i, f.Name, f.Type, fv.Kind(), fv.Interface(), f.Tag.Get("json"))
	}

	fmt.Println("\n========== 4. 可寻址性：CanSet 与 reflect.New ==========")
	// ValueOf(emp) 拿到的是 emp 的副本，不可寻址 → CanSet=false
	fmt.Printf("ValueOf(变量): CanSet=%v\n", v.CanSet())
	// reflect.New 分配一个 *Employee，Elem() 解引用到本体 → CanSet=true
	p := reflect.New(t)         // *Employee 的 Value
	settable := p.Elem()        // 指向新分配的 Employee 本体
	fmt.Printf("reflect.New + Elem(): CanSet=%v\n", settable.CanSet())
	settable.FieldByName("Name").SetString("Grace")
	settable.FieldByName("Age").SetInt(45)
	written := settable.Interface().(Employee)
	fmt.Printf("反射写入后: %+v（原 emp 未受影响: %v）\n", written, emp.Name == "Ada")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. TypeOf 看类型、ValueOf 看值；字段遍历用 NumField + Field(i)")
	fmt.Println("2. Name 是类型名（匿名类型为空），Kind 是底层种类（ID 的 Kind 仍是 int）")
	fmt.Println("3. ValueOf(变量) 是副本不可 Set；reflect.New + Elem() 才是可写入口")
	fmt.Println("4. 反射有运行时开销，能静态写就别反射；序列化/ORM 这类通用框架才值得")
}
