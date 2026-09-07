package main

// 项目 23：内联优化与函数调用开销
//
// 编译器只内联"够小"的函数（内联预算 ~80 条语句），把小函数塞进调用点，
// 省掉 call 的 prologue/epilogue，还能让整个循环被整体优化。
// 本项目对比四种组合：
//   smallAdd(可内联) vs smallAddNI(//go:noinline 强制不内联)
//   bigLoop(体量大、天然不内联) vs bigLoopNI(一样)
//
// 注意：不能把被测函数塞进通用 bench(fn)(...) —— 函数值会让 call 走间接
// 调用，把"内联"直接废掉；要写四段裸循环各自计时。
//
// 运行：go run main.go [-json]
//       go build -gcflags=-m main.go  看编译器自己报的 can inline / cannot

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

var sink int

// 小函数：在内联预算内，默认会被内联。8 个参数让 noinline 时必须
// 栈上传参（寄存器放不下），call 开销被放大，内联 vs 非内联差得明显
func smallSum8(a, b, c, d, e, f, g, h int) int { return a + b + c + d + e + f + g + h }

//go:noinline
func smallSum8NI(a, b, c, d, e, f, g, h int) int { return a + b + c + d + e + f + g + h }

// 大函数：体量远超内联预算，默认就不内联（这里强制 noinline 只是对照）
func bigLoop(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += (i*i)%7 + (i*i*i)%13 + ((i+3)*(i+5))%11
		s ^= (i >> 2)
		s -= (i%17)*3
	}
	return s
}

//go:noinline
func bigLoopNI(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += (i*i)%7 + (i*i*i)%13 + ((i+3)*(i+5))%11
		s ^= (i >> 2)
		s -= (i%17)*3
	}
	return s
}

// 常数函数：内联后编译器直接折叠成常量，调用处的整个循环都能删掉
func constForty() int { return 42 }

//go:noinline
func constFortyNI() int { return 42 }

func benchSmall(n int) time.Duration {
	t0 := time.Now()
	acc := 0
	for i := 0; i < n; i++ {
		acc += smallSum8(i,i+1,i+2,i+3,i+4,i+5,i+6,i+7) // 内联后这里没有函数调用
	}
	sink = acc
	return time.Since(t0)
}

func benchSmallNI(n int) time.Duration {
	t0 := time.Now()
	acc := 0
	for i := 0; i < n; i++ {
		acc += smallSum8NI(i,i+1,i+2,i+3,i+4,i+5,i+6,i+7) // 强制真调用
	}
	sink = acc
	return time.Since(t0)
}

func benchBig(n int) time.Duration {
	t0 := time.Now()
	sink = bigLoop(n)
	return time.Since(t0)
}

func benchBigNI(n int) time.Duration {
	t0 := time.Now()
	sink = bigLoopNI(n)
	return time.Since(t0)
}

func benchConst(n int) time.Duration {
	t0 := time.Now()
	acc := 0
	for i := 0; i < n; i++ {
		acc += constForty() // 内联 → 循环被常量折叠整个删除
	}
	sink = acc
	return time.Since(t0)
}

func benchConstNI(n int) time.Duration {
	t0 := time.Now()
	acc := 0
	for i := 0; i < n; i++ {
		acc += constFortyNI() // noinline → 每次真调一次函数
	}
	sink = acc
	return time.Since(t0)
}

func run() {
	const smallN = 200_000_000
	const bigN = 2_000_000
	const cfN = 200_000_000

	tS := benchSmall(smallN)
	tNI := benchSmallNI(smallN)
	tB := benchBig(bigN)
	tBI := benchBigNI(bigN)
	tCF := benchConst(cfN)
	tCFNI := benchConstNI(cfN)

	res := map[string]interface{}{
		"smallInlineNs": float64(tS.Nanoseconds()) / smallN,
		"smallNoinlNs":  float64(tNI.Nanoseconds()) / smallN,
		"bigNs":         float64(tB.Nanoseconds()) / bigN,
		"bigNoinlNs":    float64(tBI.Nanoseconds()) / bigN,
		"cfInlineNs":    float64(tCF.Nanoseconds()) / cfN,
		"cfNoinlNs":     float64(tCFNI.Nanoseconds()) / cfN,
		"smallRatio":    float64(tNI) / float64(tS),
		"bigRatio":      float64(tBI) / float64(tB),
		"cfRatio":       float64(tCFNI) / float64(tCF),
	}

	if !silent {
		pr("== 小函数：内联 vs noinline（ns/op）================\n")
		pr("  smallSum8(内联)  : %8.2f ns/op\n", res["smallInlineNs"])
		pr("  smallSum8NI(强制): %8.2f ns/op\n", res["smallNoinlNs"])
		pr("  差：%.1fx —— M 系 OOO CPU 把 call 开销吞大半，但仍是真开销\n\n",
			res["smallRatio"])
		pr("== 大函数：内联与否几乎无差 =====================\n")
		pr("  bigLoop  : %8.2f ns/op\n", res["bigNs"])
		pr("  bigLoopNI: %8.2f ns/op\n", res["bigNoinlNs"])
		pr("  差：%.2fx —— 主体几百条语句，call 占比 ≈ 0；内联救不了大函数\n\n",
			res["bigRatio"])
		pr("== 常数函数：内联解锁常量折叠 ===================\n")
		pr("  constForty(内联)  : %8.3f ns/op (整个循环被删)\n", res["cfInlineNs"])
		pr("  constFortyNI(强制): %8.2f ns/op (每次真调用)\n", res["cfNoinlNs"])
		pr("  差：%.0fx —— 内联让编译器把循环摞成 42*n\n\n", res["cfRatio"])
		pr("  go build -gcflags=-m main.go 会告诉你编译器认不认内联\n")
		b, _ := json.MarshalIndent(res, "", "  ")
		pr("--- JSON 快照 ---\n%s\n", b)
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
	runtime.KeepAlive(nil)
}