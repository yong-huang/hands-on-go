package main

// 项目 24：闭包与函数捕获 —— 闭包抓的是「引用」，不是一个拷贝
//
// 1. 经典坑：循环变量声明在循环外（老代码模式），goroutine 闭包捕获的是
//    &i（同一个地址）→ 全部看到终止值；修复：把 i 作为参数传进去。
//    注意：Go 1.22+ 用 := 在循环内声明的变量已是每迭代一份（见博客），
//    但"循环外声明 + 3 段式 for"仍共享同一个盒子——经典坑还在。
// 2. 闭包捕获 = 引用捕获：外层变量改了，闭包看到新值（不是快照）。
// 3. 逃逸：被捕获的变量移到堆上（"moved to heap"），因闭包可能活得比栈久。
//
// 运行：go run main.go [-json]

import (
	"encoding/json"
	"flag"
	"fmt"
	"sync"
	"unsafe"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

const N = 5

// captureLoop：循环变量声明在循环外（经典老代码），goroutine 捕获 &i
func captureLoop(addrs *[]uintptr, values *[]int) {
	var wg sync.WaitGroup
	var i int // 整个循环只有这一个盒子；Go 1.22+ 的 per-iteration 修复不覆盖它
	slots := make([]uintptr, N)
	vls := make([]int, N)
	var mu sync.Mutex
	next := 0
	for i = 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock() // 各 goroutine 写自己的槽位，避免共享 slice 并发 append 丢更新
			s := next
			next++
			// 捕获的是外层 i 的引用（&i），所有 goroutine 共享同一个地址
			slots[s] = uintptr(unsafe.Pointer(&i))
			vls[s] = i
			mu.Unlock()
		}()
	}
	wg.Wait()
	*addrs = slots
	*values = vls
}

// captureFixed：参数传值，每个闭包拿自己的那一份（按索引写槽位，无竞争）
func captureFixed(addrs *[]uintptr, values *[]int) {
	var wg sync.WaitGroup
	slots := make([]uintptr, N)
	vls := make([]int, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			slots[j] = uintptr(unsafe.Pointer(&j)) // 每迭代的 j 是独立拷贝
			vls[j] = j
		}(i)
	}
	wg.Wait()
	*addrs = slots
	*values = vls
}

// byRef：引用捕获演示——闭包创建后外层变更，闭包看到新值
func byRef() int {
	x := 10
	get := func() int { return x }
	x = 99
	return get()
}

// shareCapture：两个独立闭包，共享同一个被捕获变量（指向同一盒子）
func shareCapture() (uintptr, uintptr) {
	v := 1
	a := func() uintptr { return uintptr(unsafe.Pointer(&v)) }
	b := func() uintptr { return uintptr(unsafe.Pointer(&v)) }
	return a(), b()
}

func uniq(a []uintptr) int {
	seen := map[uintptr]bool{}
	for _, x := range a {
		seen[x] = true
	}
	return len(seen)
}

func jdump(v interface{}) {
	b, _ := json.MarshalIndent(v, "", "  ")
	if !silent {
		pr("%s\n", b)
	} else {
		fmt.Println(string(b))
	}
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}

	var loopAddrs, fixedAddrs []uintptr
	var loopVals, fixedVals []int
	captureLoop(&loopAddrs, &loopVals)
	captureFixed(&fixedAddrs, &fixedVals)

	byRefV := byRef()
	sa, sb := shareCapture()

	res := map[string]interface{}{
		"loopAddrs":         loopAddrs,
		"loopUniqueAddrs":   uniq(loopAddrs),
		"loopValues":        loopVals,
		"fixedAddrs":        fixedAddrs,
		"fixedUniqueAddrs":  uniq(fixedAddrs),
		"fixedValues":       fixedVals,
		"byRefSeen":         byRefV,
		"shareSameBox":      sa == sb,
	}

	if !silent {
		pr("== 1. 循环变量捕获（声明在循环外，整个循环一个盒子） ====\n")
		pr("   地址 %v → 唯一 %d 个（全共享 &i）\n",
			loopAddrs, uniq(loopAddrs))
		pr("   读到值 %v（大概率重复/终止值）\n\n", loopVals)
		pr("== 2. 参数传值修复 ==========================\n")
		pr("   地址 %v → 唯一 %d 个（各持一份）\n",
			fixedAddrs, uniq(fixedAddrs))
		pr("   读到值 %v（正确）\n\n", fixedVals)
		pr("== 3. 引用捕获 / 共享盒子 ===================\n")
		pr("   创建时 x=10 → 之后 x=99 → 闭包读到 %d\n", byRefV)
		pr("   两个闭包指向同一盒子：%t\n\n", sa == sb)
		pr("   go build -gcflags=-m 应看到 moved to heap\n")
		jdump(res)
	} else {
		jdump(res)
	}
}