package main

// 项目 7：Goroutine 底层——栈的增长与隔离
//
// Go 1.26 已封禁对 runtime 内部符号（runtime.getg / runtime.allgs）的
// //go:linkname 链接，无法再直接读 g.stack.lo/hi。改用纯受支持的
// 手段观察同一个底层事实：
//
//  1. goid：解析 runtime.Stack 官方输出（"goroutine N [...]"）
//  2. 栈向下生长：每调一层，SP 下降一个帧的高度
//  3. 栈扩容：每帧压 4KB 递归，逼栈翻倍扩容；用 runtime.MemStats 的
//     StackInuse（当前所有 goroutine 栈内存总量）实时观察增长
//  4. 栈隔离：N 个 goroutine 同时深递归，StackInuse ≈ 基线 + N × 单栈，
//     证明每个 goroutine 各占一段独立栈
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 状态快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"unsafe"
)

var sink uint64

// ---------- 栈内存总量（KB） ----------

func stackKB() int64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int64(ms.StackInuse / 1024)
}

// ---------- goid（从 runtime.Stack 官方格式解析） ----------

var goidRe = regexp.MustCompile(`^goroutine (\d+)`)

func goid() int64 {
	buf := make([]byte, 128)
	n := runtime.Stack(buf, false)
	m := goidRe.FindSubmatch(buf[:n])
	if len(m) < 2 {
		return -1
	}
	id, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return id
}

// ---------- 第 1 节：goid ----------

func section1() {
	fmt.Println("========== 1. goid：每个 goroutine 有独立编号 ==========")
	fmt.Printf("  主 goroutine: goid = %d\n", goid())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		fmt.Printf("  子 goroutine: goid = %d\n", goid())
	}()
	wg.Wait()
}

// ---------- 第 2 节：栈向下生长 ----------

//go:noinline
func spMarker() uintptr {
	var x byte
	x = 0x5a
	sink += uint64(x)
	return uintptr(unsafe.Pointer(&x))
}

//go:noinline
func inner(outerSP uintptr) uintptr {
	sp := spMarker()
	fmt.Printf("    outer 帧 SP ≈ 0x%x\n", outerSP)
	fmt.Printf("    inner 帧 SP ≈ 0x%x（比 outer 低 → 栈向下生长）\n", sp)
	return sp
}

func section2() {
	fmt.Println("\n========== 2. 栈向下生长：每调一层，SP 下降一帧 ==========")
	sp := spMarker()
	fmt.Printf("  当前帧 SP ≈ 0x%x\n", sp)
	inner(sp)
}

// ---------- 第 3 节：深递归逼栈扩容 ----------

var growth []int64 // 每 64 层记录一次 StackInuse (KB)
var tracking bool

//go:noinline
func recurse(depth int) {
	var buf [4096]byte // 每帧 4KB，把栈撑大
	buf[0] = byte(depth) + 1
	sink += uint64(buf[0])
	if tracking && depth%64 == 0 {
		growth = append(growth, stackKB())
	}
	if depth < 320 {
		recurse(depth + 1)
	}
}

func section3() {
	fmt.Println("\n========== 3. 深递归压 4KB 帧 → 栈自动翻倍扩容 ==========")
	fmt.Println("  初始栈仅 ~2KB；用 MemStats.StackInuse（全部 goroutine 栈内存）观察：")
	tracking = true
	recurse(0)
	tracking = false
	prev := int64(-1)
	for i, kb := range growth {
		d := i * 64
		note := ""
		if prev > 0 && kb > prev {
			note = fmt.Sprintf("  ← 扩容 +%d KB", kb-prev)
		}
		fmt.Printf("  depth=%3d  StackInuse=%4d KB%s\n", d, kb, note)
		prev = kb
	}
	fmt.Println("  → 栈不够用时 runtime 自动换成更大的栈，全程用户无感知")
}

// ---------- 第 4 节：多 goroutine 各自独立栈 ----------

//go:noinline
func recursePark(depth int, stay <-chan struct{}, report func(int64)) {
	var buf [4096]byte
	buf[0] = byte(depth) + 1
	sink += uint64(buf[0])
	if depth == 300 {
		report(stackKB()) // 所有 goroutine 同时到最深帧 → 上报即总峰值
		<-stay
		return
	}
	recursePark(depth+1, stay, report)
}

func section4() {
	fmt.Println("\n========== 4. N 个并发深递归：栈按 N 倍增长 ==========")
	base := stackKB()
	fmt.Printf("  基线（无深递归时）StackInuse = %d KB\n", base)
	for _, n := range []int{1, 2, 4} {
		stay := make(chan struct{})
		reports := make(chan int64, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				recursePark(0, stay, func(kb int64) { reports <- kb })
			}()
		}
		var last int64
		for i := 0; i < n; i++ {
			last = <-reports
		}
		fmt.Printf("  %d 个并发深递归: StackInuse=%d KB（每个约 +%d KB）\n",
			n, last, (last-base)/int64(n))
		close(stay)
		wg.Wait()
	}
	fmt.Println("  → 每个 goroutine 各占一段独立栈，总量线性叠加")
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 每个 goroutine 有独立 goid 和一段独立栈（初始仅 ~2KB）")
	fmt.Println("2. 栈向下生长；帧不够时 runtime 整体复制翻倍扩容")
	fmt.Println("3. 栈扩容对用户完全透明，由 stackguard 阈值触发")
	fmt.Println("4. 栈小而可扩、成本低 → 可轻松开几十万 goroutine")
}

// ---------- JSON 状态快照 ----------

func jsonOut() {
	goids := map[string]interface{}{"main": goid()}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		goids["child"] = goid()
	}()
	wg.Wait()

	tracking = true
	recurse(0)
	tracking = false
	g := make([]map[string]interface{}, len(growth))
	for i, kb := range growth {
		g[i] = map[string]interface{}{"depth": i * 64, "kb": kb}
	}

	base := stackKB()
	gs := []map[string]interface{}{}
	for _, n := range []int{1, 2, 4} {
		stay := make(chan struct{})
		reports := make(chan int64, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				recursePark(0, stay, func(kb int64) { reports <- kb })
			}()
		}
		var last int64
		for i := 0; i < n; i++ {
			last = <-reports
		}
		gs = append(gs, map[string]interface{}{"n": n, "kb": last})
		close(stay)
		wg.Wait()
	}

	out := map[string]interface{}{
		"goid":       goids,
		"growth":     g,
		"baseline":   base,
		"goroutines": gs,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		jsonOut()
		return
	}
	section1()
	section2()
	section3()
	section4()
	summary()
}
