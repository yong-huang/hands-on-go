package main

import "fmt"

// 26_iface_assert：接口组合与类型断言
//
// 本程序演示四件事：
//   1. 接口嵌入：Reader + Writer 组合出 ReadWriter（io 包同款手法）
//   2. 结构体实现方法集 → 自动满足组合后的接口（无 implements 关键字）
//   3. 类型断言：单值形式 v.(T) 失败会 panic（用 recover 演示），
//      双值形式 v, ok := x.(T) 失败只给 ok=false
//   4. 类型 switch：switch v := x.(type) 一次分派多种类型
//
// 运行：go run main.go

// Reader 只会读
type Reader interface {
	Read() string
}

// Writer 只会写
type Writer interface {
	Write(s string)
}

// ReadWriter = Reader + Writer：接口嵌入，小接口组合成大接口
type ReadWriter interface {
	Reader
	Writer
}

// Memory 实现了 Read 和 Write，于是自动满足 Reader、Writer、ReadWriter 三个接口
type Memory struct{ data []string }

func (m *Memory) Read() string {
	if len(m.data) == 0 {
		return ""
	}
	s := m.data[len(m.data)-1]
	m.data = m.data[:len(m.data)-1]
	return s
}

func (m *Memory) Write(s string) { m.data = append(m.data, s) }

func main() {
	fmt.Println("========== 1. 接口组合：Reader + Writer → ReadWriter ==========")
	var rw ReadWriter = &Memory{}
	rw.Write("hello")
	rw.Write("world")
	fmt.Println("rw.Read() =", rw.Read(), "| 剩余一读:", rw.Read())
	// var r Reader = rw   // OK：大接口可隐式转回小接口
	fmt.Println("Memory 通过方法集自动满足 Reader/Writer/ReadWriter，无需 implements")

	fmt.Println("\n========== 2. 双值断言：v, ok := x.(T) ==========")
	var x interface{} = "gopher"
	if s, ok := x.(string); ok {
		fmt.Println("x.(string) 成功:", s, "（长度", len(s), "）")
	}
	if n, ok := x.(int); !ok {
		fmt.Println("x.(int) 失败但安全: ok =", ok, "，n = 零值", n)
	}

	fmt.Println("\n========== 3. 单值断言：v := x.(T)，失败会 panic ==========")
	safeAssert(42)
	safeAssert("text") // 这一步内部 panic，被 recover 拦截

	fmt.Println("\n========== 4. 类型 switch：一次分派多种类型 ==========")
	describe("go")
	describe(100)
	describe(true)
	describe(3.14)

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 接口嵌入=小接口组合大接口，io.Reader/io.Writer 就是这么拼出 io.ReadWriter 的")
	fmt.Println("2. 方法集即满足：实现了全部方法就自动是接口的实现者")
	fmt.Println("3. 双值断言安全返回 ok；单值断言失败直接 panic，仅在『必定成功』时用")
	fmt.Println("4. 多分支分派优先用类型 switch，比一串 if-ok 断言更清晰且零成本抽象")
}

// safeAssert 演示单值断言失败 → panic → defer/recover 拦截，程序不死
func safeAssert(x interface{}) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recover 捕获断言 panic:", r)
		}
	}()
	fmt.Println("尝试 x.(int)，x =", x)
	v := x.(int) // x 是 int 时成功；否则 panic: interface conversion
	fmt.Println("断言成功: v =", v)
}

// describe 用类型 switch 分派，v 在每个分支里已具有对应的具体类型
func describe(x interface{}) {
	switch v := x.(type) {
	case string:
		fmt.Printf("类型 switch → string: %q (len=%d)\n", v, len(v))
	case int:
		fmt.Printf("类型 switch → int: %d (翻倍=%d)\n", v, v*2)
	case bool:
		fmt.Printf("类型 switch → bool: %v (取反=%v)\n", v, !v)
	default:
		fmt.Printf("类型 switch → 其他: %T = %v\n", v, v)
	}
}
