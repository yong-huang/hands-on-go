package main

// 项目 29：内存泄漏检测工具
//
// 三种常见"泄漏"（Go 没有真泄漏，都是"该回收却没回收"）：
//   1. goroutine 泄漏：协程永远阻塞在没人接的 channel 上
//   2. 大数组背景引用：拿住大 []byte 的一个小切片，整块 100MB 退不了堆
//   3. 未关闭资源：文件句柄（fd 泄漏）
//
// 取证手段：runtime/pprof 的 goroutine/heap 快照 + MemStats 前后对照。
//
// 运行：go run main.go [-json]（profile 文件写入系统临时目录）

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

var silent bool

// sink 防止编译器做死代码消除（只写不读的分配会被整个删掉）
var sink byte

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

type phase struct {
	Label   string  `json:"label"`
	Goroutines int  `json:"goroutines"`
	HeapAllocMB float64 `json:"heapAllocMB"`
}

func snapshot(label string) phase {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return phase{
		Label: label, Goroutines: runtime.NumGoroutine(),
		HeapAllocMB: float64(m.HeapAlloc) / (1 << 20),
	}
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	silent = *jsonFlag

	var phases []phase
	phases = append(phases, snapshot("基线"))

	// ---- 泄漏 1：goroutine 卡在没人收的 channel ----
	blockers := make([]chan struct{}, 20)
	for i := range blockers {
		ch := make(chan struct{})
		blockers[i] = ch
		go func() {
			<-ch // 永远等不到
		}()
	}
	time.Sleep(50 * time.Millisecond)
	phases = append(phases, snapshot("20 个 goroutine 阻塞"))

	// ---- 泄漏 2：100MB 大数组被小切片背景引用 ----
	big := make([]byte, 100<<20) // 100MB
	for i := range big {
		big[i] = byte(i)
	}
	sink = big[7]           // 真读一次，逼编译器保留分配
	runtime.KeepAlive(big)  // 直到这里之前 big 都不能被回收
	keep := big[0:16]       // 只留 16 字节引用，背后 100MB 出不去
	sink = keep[0]          // 真读 keep，防止切片引用被 DCE
	phases = append(phases, snapshot("保留 100MB 小切片引用"))

	// ---- 泄漏 3：未关闭文件（fd） ----
	files := []*os.File{}
	for i := 0; i < 5; i++ {
		f, err := os.Open("/usr/share/dict/words")
		if err == nil {
			files = append(files, f) // 故意不 Close
		}
	}
	phases = append(phases, snapshot("5 个未关闭文件"))

	// KeepAlive 在"最后一次使用"之后才让出：撑住所有快照阶段
	runtime.KeepAlive(keep)

	// ---- 取证：写 goroutine/heap profile ----
	tmp, _ := os.CreateTemp("", "leak_*.prof")
	base := tmp.Name()
	tmp.Close()
	os.Remove(base)
	gpath, hpath := base+"_goroutine.pprof", base+"_heap.pprof"
	writeProfile(gpath, "goroutine")
	writeProfile(hpath, "heap")

	_ = blockers
	_ = files

	if !silent {
		for _, p := range phases {
			pr("%-22s goroutine=%-3d HeapAlloc=%6.1fMB\n",
				p.Label, p.Goroutines, p.HeapAllocMB)
		}
		pr("\ngoroutine profile: %s\nheap profile: %s\n", gpath, hpath)
	}
	res := map[string]interface{}{
		"phases":        phases,
		"goroutineProf": gpath,
		"heapProf":      hpath,
		"leakedChans":   len(blockers),
		"leakedFiles":   len(files),
	}
	if *jsonFlag {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	}
}

func writeProfile(path, name string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	if p := pprof.Lookup(name); p != nil {
		p.WriteTo(f, 1) // 1 = 文本格式（0 是 protobuf）
	}
}