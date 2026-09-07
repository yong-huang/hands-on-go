package main

// 项目 22：pprof 性能分析工具深入 —— 热点是哪来的、怎么优化
//
// 流程：
//   1. 造两个热点函数：hotCPU 逐字节校验和（慢）、hotCPUfast 按字折叠（快）
//   2. 用 runtime/pprof 写 cpu.pprof / heap.pprof 两个 profile 文件
//   3. 起 net/http/pprof 端点（:6160/debug/pprof/），本地验证 GET 200
//   4. viz.py 再用 go tool pprof -top 分析文件，把热点函数画出来
//       并展示"找到热点 → 优化"的完整闭环
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

var sink uint64

// ---- CPU 热点：校验和 ----
func hotCPU(buf []byte) uint64 {
	h := uint64(0x8123)
	for i := 0; i < len(buf); i++ {
		h = h*31 + uint64(buf[i])
	}
	return h
}

// hotCPUfast：一次吞 8 字节，循环次数降到 1/8
func hotCPUfast(buf []byte) uint64 {
	h := uint64(0x8123)
	n := len(buf) - len(buf)%8
	for i := 0; i < n; i += 8 {
		w := uint64(buf[i]) |
			uint64(buf[i+1])<<8 |
			uint64(buf[i+2])<<16 |
			uint64(buf[i+3])<<24 |
			uint64(buf[i+4])<<32 |
			uint64(buf[i+5])<<40 |
			uint64(buf[i+6])<<48 |
			uint64(buf[i+7])<<56
		h = h*31 + w
	}
	return h
}

func bench(fn func([]byte) uint64, buf []byte, iters int) time.Duration {
	start := time.Now()
	for i := 0; i < iters; i++ {
		sink = fn(buf)
	}
	return time.Since(start)
}

// ---- 内存热点：造对象垃圾 ----
type payload struct {
	a, b, c, d uint64
	next       *payload
}

func hotMemAlloc(n int) *payload {
	var head *payload
	for i := 0; i < n; i++ {
		p := &payload{a: uint64(i)}
		if i%2 == 0 { // 一半留着，一半立刻变垃圾
			p.next = head
			head = p
		}
	}
	return head
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}

	buf := make([]byte, 1<<20) // 1MB
	for i := range buf {
		buf[i] = byte(i ^ (i >> 3))
	}

	// ===== 1. CPU 热点 & 优化 =====
	cpuF, err := os.Create("cpu.pprof")
	if err != nil {
		panic(err)
	}
	defer cpuF.Close()
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		panic(err)
	}
	tSlow := bench(hotCPU, buf, 300)
	tFast := bench(hotCPUfast, buf, 300)
	pprof.StopCPUProfile()

	// ===== 2. 内存热点 & heap profile =====
	head := hotMemAlloc(300_000)

	// 先写 profile：此刻堆里既有保活对象也有待回收垃圾，才像真实现场
	heapF, err := os.Create("heap.pprof")
	if err != nil {
		panic(err)
	}
	pprof.Lookup("heap").WriteTo(heapF, 0)
	heapF.Close()
	heapBefore := memSnapshot()

	// 再触发 GC，展示回收后真正的存活数
	runtime.GC()
	ms := memSnapshot()
	// 此处再 KeepAlive：作为 head 的最后一次使用，保证 GC 期间 head 必活
	runtime.KeepAlive(head)

	// ===== 3. net/http/pprof 端点自检 =====
	go func() {
		_ = http.ListenAndServe("127.0.0.1:6160", nil)
	}()
	httpOK := pingPprof()

	res := map[string]interface{}{
		"cpuSlowNs":    tSlow.Nanoseconds() / 300,
		"cpuFastNs":    tFast.Nanoseconds() / 300,
		"speedup":      float64(tSlow) / float64(tFast),
		"leakObjects":  heapBefore.HeapObjects,
		"leakHeapMB":   heapBefore.HeapInuse / 1048576,
		"aliveAfterGC": ms.HeapObjects,
		"retainedMB":   ms.HeapInuse / 1048576,
		"httpOK":       httpOK,
		"cpuProfile":   "cpu.pprof",
		"heapProfile":  "heap.pprof",
	}
	if !silent {
		pr("== CPU 热点（1MB 校验和 ×300 轮，ns/op） ====\n")
		pr("  hotCPU(逐字节)   : %d\n", res["cpuSlowNs"])
		pr("  hotCPUfast(按字) : %d\n", res["cpuFastNs"])
		pr("  加速比：%.1fx\n\n", res["speedup"])
		pr("== 内存热点（30 万对象，一半保活） ====\n")
		pr("  heap profile 写入时：%d 对象 / %d MB（含待回收垃圾）\n",
			heapBefore.HeapObjects, heapBefore.HeapInuse/1048576)
		pr("  GC 后真正存活   ：%d 对象 / %d MB\n\n",
			ms.HeapObjects, ms.HeapInuse/1048576)
		pr("== net/http/pprof =====\n")
		pr("  端点 :6160/debug/pprof/ 自检 → %v\n", httpOK)
		pr("  分析：go tool pprof -top cpu.pprof / -inuse_space heap.pprof\n\n")
		b, _ := json.MarshalIndent(res, "", "  ")
		pr("--- JSON 快照 ---\n%s\n", b)
	} else {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	}
}

func memSnapshot() runtime.MemStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms
}

func pingPprof() bool {
	for i := 0; i < 10; i++ {
		resp, err := http.Get("http://127.0.0.1:6160/debug/pprof/")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp.StatusCode == 200
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}