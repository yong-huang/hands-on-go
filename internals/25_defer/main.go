package main

// 项目 25：defer 底层实现与性能
//
// 三个真相：
//   1. LIFO：defer 后进先出，展开时逆序收拢（资源后开先关）
//   2. 参数立即求值：defer 语句一行就把参数 value 算好存下，等到返回才
//      真正执行函数体；形参才是执行时再看（defer f(x) 是存入 x 的快照）
//   3. 开销：Go 1.14+ 大多走 open-coded defer（编译期展开，约零开销），
//      只有逃逸/循环特殊场景才退回堆上 defer。
//
// 运行：go run main.go [-json]

import (
	"encoding/json"
	"flag"
	"fmt"
	"sync"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

var log []string

func mark(s string) string {
	log = append(log, s)
	return s
}

// lifo：登记 1..5，观察逆序收拢 + 参数立即求值证据
func lifo() {
	defer pr("  执行(1): %s\n", mark("登记时求出 1"))
	defer pr("  执行(2): %s\n", mark("登记时求出 2"))
	defer pr("  执行(3): %s\n", mark("登记时求出 3"))
	defer pr("  执行(4): %s\n", mark("登记时求出 4"))
	defer pr("  执行(5): %s\n", mark("登记时求出 5"))
	pr("  （函数体先打这行；mark 已在登记时执行，值快照进 defer）\n")
}

// ---- 性能对照 ----
var mu sync.Mutex

var data [64]byte

func touchData() { data[0] += 1 }

func useDefer() {
	mu.Lock()
	defer mu.Unlock()
	_ = touchData
}

func noDefer() {
	mu.Lock()
	_ = touchData
	mu.Unlock()
}

func bench(name string, f func(), n int) time.Duration {
	t0 := time.Now()
	for i := 0; i < n; i++ {
		f()
	}
	return time.Since(t0)
}

func run() {
	lifo()
	order := append([]string{}, log...)

	const n = 5_000_000
	dD := bench("defer", useDefer, n)
	dN := bench("no-defer", noDefer, n)

	res := map[string]interface{}{
		"order": order,
		"deferNs":    float64(dD.Nanoseconds()) / n,
		"nodeferNs":  float64(dN.Nanoseconds()) / n,
		"ratio":      float64(dD) / float64(dN),
	}

	if !silent {
		pr("\n== 顺序 =============\n")
		pr(" 登记顺序：[登记时求出 1 2 3 4 5]（依次 defer）\n")
		pr(" 执行收拢：逆序（最后一个 defer 先跑）\n")
		pr(" 证据：mark() 在“登记时”就跑了（log 立即拼满 5 条），\n")
		pr("       真正打印发生在返回时 —— 这就是“参数立即求值、主体执行时”）\n")
		pr("\n== 性能 =============\n")
		pr("  useDefer (defer) : %8.2f ns/op\n", res["deferNs"])
		pr("  noDefer (手动)   : %8.2f ns/op\n", res["nodeferNs"])
		pr("  defer 额外开销   : %.2fx（open-coded defer，几乎白送）\n",
			res["ratio"])
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