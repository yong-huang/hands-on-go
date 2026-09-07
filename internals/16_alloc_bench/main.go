package main

// 项目 16：对象分配性能调优
//
// 等价于 go test -bench=. -benchmem 的三组对照（这里用 main.go 内建计时：
//   ns/op    —— 每次操作耗时
//   allocs/op —— 每次操作堆分配次数（MemStats.Mallocs 增量）
//
// 对照 1：new(T) vs &T{}              —— 结果应完全一样（同一操作）
// 对照 2：切片 append 动态扩容 vs make 预分配容量
// 对照 3：sync.Pool 复用 vs 每次 fresh(new)
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"sync"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

type Payload struct{ data [32]byte }

var sink interface{}

// bench 跑 nOps 次 f()，返回 ns/op 与 allocs/op
func bench(name string, nOps int, f func()) (float64, float64) {
	runtime.GC()
	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	t0 := time.Now()
	for i := 0; i < nOps; i++ {
		f()
	}
	dt := time.Since(t0)
	runtime.ReadMemStats(&m2)
	ns := float64(dt.Nanoseconds()) / float64(nOps)
	alloc := float64(m2.Mallocs-m1.Mallocs) / float64(nOps)
	return ns, alloc
}

func exp() []map[string]interface{} {
	var res []map[string]interface{}

	// 对照 1：new vs &T{}
	newFn := func() {
		p := new(Payload)
		sink = p
	}
	structFn := func() {
		p := &Payload{}
		sink = p
	}
	ns1, a1 := bench("new(Payload)", 500_000, newFn)
	ns2, a2 := bench("&Payload{}", 500_000, structFn)
	res = append(res, mk("new vs &struct{}", "new", ns1, a1),
		mk("new vs &struct{}", "&struct{}", ns2, a2))
	pr("对照 1: new(T) vs &T{}（语义完全相同，耗时/分配一致）\n")

	// 对照 2：append 扩容 vs 预分配
	appendFn := func() {
		var s []int
		for i := 0; i < 256; i++ {
			s = append(s, i)
		}
		sink = s
	}
	preFn := func() {
		s := make([]int, 0, 256)
		for i := 0; i < 256; i++ {
			s = append(s, i)
		}
		sink = s
	}
	ns3, a3 := bench("append 动态扩容", 200_000, appendFn)
	ns4, a4 := bench("make 预分配(256)", 200_000, preFn)
	res = append(res, mk("append vs 预分配", "append 扩容", ns3, a3),
		mk("append vs 预分配", "make 预分配", ns4, a4))
	pr("对照 2: 预分配避免扩容搬移，更省分配\n")

	// 对照 3：sync.Pool vs fresh
	var pool = sync.Pool{New: func() interface{} { return new(Payload) }}
	poolFn := func() {
		p := pool.Get().(*Payload)
		p.data[0] = 1
		pool.Put(p)
	}
	freshFn := func() {
		p := new(Payload)
		p.data[0] = 1
		sink = p
	}
	ns5, a5 := bench("sync.Pool 复用", 200_000, poolFn)
	ns6, a6 := bench("每次 fresh", 200_000, freshFn)
	res = append(res, mk("Pool vs fresh", "Pool 复用", ns5, a5),
		mk("Pool vs fresh", "每次 fresh", ns6, a6))
	pr("对照 3: Pool 复用基本零分配\n")

	for _, r := range res {
		if !silent {
			pr("   %-14s %-16s %8.2f ns/op  %6.2f allocs/op\n",
				r["group"], r["name"], r["ns"], r["allocs"])
		}
	}
	return res
}

func mk(g, n string, ns, a float64) map[string]interface{} {
	return map[string]interface{}{"group": g, "name": n, "ns": ns, "allocs": a}
}

func summary() {
	pr("\n========== 总结 ==========\n")
	pr("1. new(T) 与 &T{} 是同一操作，写法差异不产生性能差异\n")
	pr("2. 预分配容量：避免反复扩容搬移，分配次数陡降\n")
	pr("3. sync.Pool 复用：把分配次数压到 ~0，吞吐翻倍\n")
}

func jsonOut() {
	res := map[string]interface{}{"bench": exp()}
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
	exp()
	summary()
}