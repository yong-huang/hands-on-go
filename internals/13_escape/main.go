package main

// 项目 13：栈 vs 堆——逃逸分析
//
// Go 的变量分配位置由编译器逃逸分析决定。级联规则：**指针是否会逃逸**。
// 用 `go build -gcflags=-m` 可以看编译器说每个变量放哪。本项目包含 8 个场景：
//
//   逃逸（heap）：
//     1. 返回局部变量指针        — 指针出了函数作用域
//     2. 闭包捕获局部变量        — 闭包逃逸，被捕获者也逃逸
//     3. 大对象                  — 栈帧放不下，强制堆
//     4. 存进包级别变量          — 全局可及
//   5. interface 装箱（直接消费）  — 装箱本身不堆，接口逃逸才算堆分配
//   不逃逸（stack）：
//     6. 只在本函数内使用的值    — 留在栈上
//     7. 指针只被只读函数消费    — 读穿函数不会逃逸
//     8. 小的按值返回            — 栈上解决
//
// 运行时佐证：同样调用 N 次，逃逸场景会让 TotalAlloc 暴涨，栈上场景几乎为 0。
//
// 运行：go run main.go        文本演示（含 TotalAlloc 实测）
//       go run main.go -json 输出 JSON 快照
//       go build -gcflags=-m main.go 查看编译器的分配决策（viz.py 内部会跑）

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 逃逸场景 ----------

// 1. 返回局部变量指针 → 逃逸
//go:noinline
func returnLocal() *int {
	x := 42
	return &x
}

// 2. 闭包捕获局部变量 → 逃逸
//go:noinline
func captureClosure() func() int {
	a := 10
	return func() int { return a }
}

// 3. 大对象 → 逃逸（栈帧放不下）
type bigBuf = [8192]int64 // 64KB

//go:noinline
func bigStack() *bigBuf {
	b := &bigBuf{}
	return b
}

// 4. 存进包级别变量 → 逃逸
var global *int

//go:noinline
func plant() *int {
	x := 7
	global = &x
	return global
}

// 5. interface{} 传参 → 装进接口时逃逸
// 5. 返回 interface → 装箱的副本逃逸到堆
//go:noinline
func makeIface() interface{} {
	return [16]int{1: 11} // 128B 装箱，interface 逃逸 → 堆
}

//go:noinline
func useIface() int {
	if v, ok := makeIface().([16]int); ok {
		return v[1]
	}
	return 0
}

// ---------- 不逃逸场景 ----------

// 6. 只在本函数内使用 → 栈上
//go:noinline
func localOnly() int {
	x := 42
	y := x + 1
	return y
}

// 7. 指针只被"只读"函数消费 → 留在栈上
//go:noinline
func readOnly(p *int) int { return *p + 1 }

//go:noinline
func passReadOnly() int {
	x := 3
	return readOnly(&x)
}

// 8. 小对象按值返回 → 栈上 / 寄存器
type small struct{ v int }

//go:noinline
func returnByValueSmall() small { return small{v: 5} }

//go:noinline
func useSmall() int { return returnByValueSmall().v }

// ---------- 运行时佐证 ----------

type delta struct {
	Func string `json:"func"`
	Heap bool   `json:"heap"` // 预期逃逸?
	Alloc uint64 `json:"alloc_delta"` // 调用 N 次 TotalAlloc 增量
}

func measure() []delta {
	const n = 200_000

	// 先热一遍，把各种惰性初始化打出去
	_ = returnLocal(); _ = captureClosure(); _ = bigStack(); _ = plant()
	_ = useIface(); _ = localOnly(); _ = passReadOnly(); _ = useSmall()
	runtime.GC()
	sink := 0

	run := func(name string, expected bool, f func() int) delta {
		var pre, post runtime.MemStats
		runtime.ReadMemStats(&pre)
		for i := 0; i < n; i++ {
			sink += f()
		}
		runtime.ReadMemStats(&post)
		return delta{Func: name, Heap: expected, Alloc: post.TotalAlloc - pre.TotalAlloc}
	}

	mk1 := func() int { p := returnLocal(); return *p }
	mk2 := func() int { return captureClosure()() }
	mk3 := func() int { b := bigStack(); return int(b[1]) }
	mk4 := func() int { return *plant() }
	mk5 := func() int { return useIface() }
	mk6 := func() int { return localOnly() }
	mk7 := func() int { return passReadOnly() }
	mk8 := func() int { return useSmall() }

	return []delta{
		run("返回局部变量指针", true, mk1),
		run("闭包捕获", true, mk2),
		run("大对象(64KB)", true, mk3),
		run("包级变量可达", true, mk4),
		run("interface 装箱", false, mk5),
		run("纯局部使用", false, mk6),
		run("只读指针调用", false, mk7),
		run("按值返回小对象", false, mk8),
	}
}

func printDeltas(ds []delta) {
	pr("========== 运行时佐证：调用 20 万次后 TotalAlloc 增量 ==========\n")
	for _, d := range ds {
		mark := "逃逸(heap)"
		if !d.Heap {
			mark = "栈上"
		}
		pr("   %-14s %-11s TotalAlloc +%d B\n", d.Func, mark, d.Alloc)
	}
	pr("  → 逃逸场景每次调用都要堆分配 ⇒ TotalAlloc 线性上涨；栈上场景 ~0\n")
}

func summary() {
	pr("\n========== 用法 ==========\n")
	pr("go build -gcflags='-m' main.go   # 看编译器逐行的分配决策\n")
	pr("判定口诀：指针/闭包/大对象/全局可达 → 堆；只在本函数、只读传递 → 栈\n")
	pr("注意：interface 装箱本身 / 加不加 noinline 都会影响结论，以 gcflags 输出为准\n")
}

// ---------- JSON ----------

func jsonOut() {
	res := map[string]interface{}{"deltas": measure()}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
		jsonOut()
		return
	}
	printDeltas(measure())
	summary()
}