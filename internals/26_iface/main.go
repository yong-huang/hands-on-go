package main

// 项目 26：interface 动态派发开销
//
// 同一方法五种叫法：
//   1. 直接调用（小函数会被内联）——静态派发
//   2. 直接调用但 //go:noinline ——把内联踢掉，看纯 call 成本
//   3. 接口调用 c.calc(i)——走 itab 表两跳寻址的动态派发
//   4. 类型断言 c.(*Sampler) 后再直接调用
//   5. type switch 分支后直接调用
//
// 真相：动态派发 = 一次(或两次)间接寻址，几纳秒级；真正的差距往往来自
//   "接口调用挡死了内联"——itab 调用点编译器不敢把实现抠进来。
//
// 运行：go run main.go [-json]

import (
	"encoding/json"
	"flag"
	"fmt"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

var sink int

type Calc interface{ Calc(x int) int }

type Sampler struct{ base int }

// 小方法：直接调用时可被内联
func (s *Sampler) Calc(x int) int { return s.base*31 + x }

//go:noinline
func (s *Sampler) CalcNI(x int) int { return s.base*31 + x }

// 常数方法：直接调用时编译器直接折叠成常量，循环都能删掉；
// 走接口 itab 时编译器不敢抠实现 → 每次真调 → 差距被放大成"优化被挡死"
func (s *Sampler) ConstVal() int { return 42 }

func bench(name string, f func(int), n int) (string, float64) {
	t0 := time.Now()
	for i := 0; i < n; i++ {
		f(i)
	}
	return name, float64(time.Since(t0).Nanoseconds()) / float64(n)
}

func run() {
	s := &Sampler{base: 7}
	var c Calc = s // 接口变量（动态派发入口）
	ss := s
	_ = ss

	const n = 50_000_000

	r1, d1 := bench("direct(内联)", func(i int) {
		sink = s.Calc(i) // 编译器：内联 + 静态
	}, n)
	r2, d2 := bench("direct noinline", func(i int) {
		sink = s.CalcNI(i)
	}, n)
	r3, d3 := bench("interface itab", func(i int) {
		sink = c.Calc(i) // itab: 查表 → 方法指针
	}, n)
	r4, d4 := bench("type-assert+直接", func(i int) {
		p := c.(*Sampler)
		sink = p.Calc(i) // 断言通过 → 直接调用
	}, n)
	r5, d5 := bench("type-switch", func(i int) {
		switch v := c.(type) {
		case *Sampler:
			sink = v.Calc(i)
		default:
			panic("类型错")
		}
	}, n)

	// 常数方法的对照：直接(内联折叠) vs 接口(挡死折叠)
	r6, d6 := bench("direct const(折叠)", func(i int) {
		sink = s.ConstVal()
	}, n)
	type ConstI interface{ ConstVal() int }
	var ci ConstI = s
	r7, d7 := bench("iface const(挡死)", func(i int) {
		sink = ci.ConstVal()
	}, n)

	res := map[string]interface{}{
		"direct":    d1,
		"directNI":  d2,
		"iface":     d3,
		"assert":    d4,
		"tswitch":   d5,
		"directConst": d6,
		"ifaceConst":  d7,
		"ratioIfaceDirect":   d3 / d1,
		"ratioIfaceDirectNI": d3 / d2,
		"ratioConst":         d7 / d6,
	}
	if !silent {
		pr("  调用方式                 ns/op\n")
		pr("  %-24s %8.2f\n", r1, d1)
		pr("  %-24s %8.2f\n", r2, d2)
		pr("  %-24s %8.2f\n", r3, d3)
		pr("  %-24s %8.2f\n", r4, d4)
		pr("  %-24s %8.2f\n", r5, d5)
		pr("\n  常数方法对照：\n")
		pr("  %-24s %8.2f  （循环被常量折叠删掉）\n", r6, d6)
		pr("  %-24s %8.2f  （itab 每次真调）\n", r7, d7)
		pr("  差距 %.1fx —— 这就是接口隐藏的代价：挡住编译器的优化眼\n",
			res["ratioConst"])
		b, _ := json.MarshalIndent(res, "", "  ")
		pr("\n--- JSON 快照 ---\n%s\n", b)
	} else {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	}
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	run()
}