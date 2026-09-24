package main

import "fmt"

// 05_struct_iface：结构体、方法与接口
//
// 本程序演示四件事：
//   1. struct 定义 + 方法（值接收者 vs 指针接收者）
//   2. 接口的隐式实现：类型不需要声明 "implements Shape"
//   3. 方法集差异：值类型变量只有值接收者方法可进接口方法集
//   4. []Shape 接口切片多态遍历：同一循环调用不同实现
//
// 运行：go run main.go

// Shape 是接口：只声明行为（能做什么），不声明数据。
// Circle、Rectangle 都"长得像"它，就自动实现它——这就是隐式实现。
type Shape interface {
	Area() float64
	Perimeter() float64
	Name() string
}

// Circle 圆：所有方法都是值接收者。
type Circle struct {
	Radius float64
}

func (c Circle) Area() float64      { return 3.14159 * c.Radius * c.Radius }
func (c Circle) Perimeter() float64 { return 2 * 3.14159 * c.Radius }
func (c Circle) Name() string       { return "Circle" }

// Rectangle 矩形：Area/Perimeter 用值接收者，Scale 用指针接收者（要改字段）。
type Rectangle struct {
	Width, Height float64
}

func (r Rectangle) Area() float64      { return r.Width * r.Height }
func (r Rectangle) Perimeter() float64 { return 2 * (r.Width + r.Height) }
func (r Rectangle) Name() string       { return "Rectangle" }

// Scale 用指针接收者：修改的是调用者手中的那份副本。
func (r *Rectangle) Scale(k float64) { r.Width *= k; r.Height *= k }

func main() {
	fmt.Println("========== 1. struct 定义与值接收者方法 ==========")
	c := Circle{Radius: 2}
	fmt.Printf("%s Area=%.4f Perimeter=%.4f\n", c.Name(), c.Area(), c.Perimeter())
	// 值接收者拿到的是副本：方法内改字段不影响原值

	fmt.Println("\n========== 2. 接口的隐式实现 ==========")
	// 注意：Circle/Rectangle 没写任何 "implements Shape"。
	// 只要方法集覆盖接口的全部方法，就自动满足接口——解耦了定义与使用。
	var s Shape = c // Circle 直接赋给 Shape 接口
	fmt.Printf("s 的动态类型是 %T, Area=%.4f\n", s, s.Area())

	fmt.Println("\n========== 3. 方法集差异：值类型 vs 指针类型 ==========")
	r := Rectangle{Width: 3, Height: 4}
	var s2 Shape = r    // OK：Rectangle 的 Area/Perimeter 是值接收者，值类型方法集包含它们
	fmt.Printf("%s Area=%.1f\n", s2.Name(), s2.Area())
	// var bad Shape = r 后调用 r.Scale？值类型的接口方法集不含 *Rectangle 的 Scale。
	// 但普通值变量调用指针接收者方法是 OK 的（r 可寻址，编译器自动取 &r）：
	r.Scale(2) // 等价于 (&r).Scale(2)，Width/Height 变为 6/8
	fmt.Printf("Scale(2) 后 Area=%.1f (3x4=12 -> 6x8=48)\n", r.Area())

	fmt.Println("\n========== 4. []Shape 接口切片：多态遍历 ==========")
	shapes := []Shape{c, Rectangle{Width: 3, Height: 4}, Circle{Radius: 1}}
	total := 0.0
	for _, sh := range shapes { // 同一个循环、同一组调用，走的是不同实现
		fmt.Printf("%-9s Area=%.4f Perimeter=%.4f\n", sh.Name(), sh.Area(), sh.Perimeter())
		total += sh.Area()
	}
	fmt.Printf("总面积 = %.4f\n", total)

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 方法 = 接收者 + 函数；要改字段用指针接收者，只读用值接收者")
	fmt.Println("2. 接口是隐式实现的：小接口 + 多类型，定义与使用解耦")
	fmt.Println("3. 值类型的接口方法集只有值接收者方法；*T 的方法集包含两者")
	fmt.Println("4. []Shape 一个循环遍历所有实现，这就是 Go 的多态")
}
