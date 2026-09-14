package main

// 项目 21：CGO 与内存管理 —— C 内存的罗斯账
//
// 三种模式隔离运行（每进程独立，RSS 才可比）：
//   leak  64 × C.malloc(1MB)，永不释放 → RSS +64MB，GC 三遍纹丝不动
//   free  64 × C.malloc(1MB)，用完 C.free → RSS 回到基线（泄漏消失）
//   go    64 × make([]byte,1MB) 靠 GC 回收 → 堆涨又落，RSS 平稳
//
// 运行：go run main.go                   文本：三种模式全跑
//       go run main.go -mode leak -json 输出单模式 JSON 快照
//
// 核心事实：C.malloc 的内存在 C 的世界里，Go 的 GC 完全看不见。

/*
#include <stdlib.h>
#include <string.h>
#include <mach/mach.h>

void* alloc_buf(int size, unsigned char magic) {
	void* p = malloc((size_t)size);
	if (!p) return 0;
	memset(p, magic, (size_t)size);
	return p;
}

unsigned char peek(const void* p, int i) {
	return ((const unsigned char*)p)[i];
}

// 当前进程常驻内存（mach task_info），字节
long cur_rss(void) {
	struct mach_task_basic_info info;
	mach_msg_type_number_t count = MACH_TASK_BASIC_INFO_COUNT;
	if (task_info(mach_task_self(), MACH_TASK_BASIC_INFO,
	              (task_info_t)&info, &count) != KERN_SUCCESS) {
		return -1;
	}
	return (long)info.resident_size;
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"unsafe"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

type sample struct {
	Mode      string `json:"mode"`
	RSSBefore int64  `json:"rssBefore"` // 分配完成后、GC 前
	RSSAfter  int64  `json:"rssAfter"`  // 跑满 GC 后（C 内存是否归还）
	GoHeapMax uint64 `json:"goHeapMax"` // 过程中堆峰值（GC 会不会管）
	Allocs    uint64 `json:"allocs"`    // 该模式新增分配次数
}

const SIZE = 1 << 20 // 1MB
const N = 64

func curRSS() int64 { return int64(C.cur_rss()) }

func readMem() (heap uint64, mallocs uint64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapInuse, ms.Mallocs
}

func gc3() {
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
}

func runMode(mode string) sample {
	heapIn, allocsIn := readMem()
	heapMax := heapIn

	switch mode {
	case "leak":
		var bufs []unsafe.Pointer
		for i := 0; i < N; i++ {
			p := C.alloc_buf(SIZE, C.uchar(0xAB))
			if p == nil {
				panic("malloc 失败")
			}
			C.peek(p, 0)
			bufs = append(bufs, unsafe.Pointer(p))
		}
		_ = bufs // 故意不释放：模拟 C 泄漏
	case "free":
		for i := 0; i < N; i++ {
			p := C.alloc_buf(SIZE, C.uchar(0xAB))
			if p == nil {
				panic("malloc 失败")
			}
			C.peek(p, 0)
			C.free(p) // 正确归还
		}
	case "go":
		var ga []*[]byte
		for i := 0; i < N; i++ {
			b := make([]byte, SIZE)
			b[0] = 0xAB
			ga = append(ga, &b)
			h, _ := readMem()
			if h > heapMax {
				heapMax = h
			}
		}
		ga = nil // 释放引用，交给 GC
	}

	s := sample{Mode: mode, RSSBefore: curRSS(), GoHeapMax: byteMB(heapMax)}
	gc3()
	s.RSSAfter = curRSS()
	_, mallocsOut := readMem()
	s.Allocs = mallocsOut - allocsIn
	return s
}

func byteMB(b uint64) uint64 { return b / 1024 / 1024 }

func mb(i int64) uint64 {
	if i < 0 {
		return 0
	}
	return uint64(i) / 1024 / 1024
}

func textAll(modes []sample) {
	pr(" 模式   分配后RSS   GC后RSS   进程堆峰值   新分配数\n")
	for _, s := range modes {
		pr(" %-6s %10d %9d %12d %9d\n",
			s.Mode, mb(s.RSSBefore), mb(s.RSSAfter), s.GoHeapMax, s.Allocs)
	}
	pr("\n单位 MB。RSS = 进程常驻内存（mach task_info）；三模式各自独立进程跑。\n")
}

func jdump(s sample) {
	b, _ := json.MarshalIndent(s, "", "  ")
	if !silent {
		pr("%s\n", b)
	} else {
		fmt.Println(string(b))
	}
}

func summary(modes []sample) {
	var leak, free, goMode sample
	for _, m := range modes {
		switch m.Mode {
		case "leak":
			leak = m
		case "free":
			free = m
		case "go":
			goMode = m
		}
	}
	pr("\n========== 结论 ==========\n")
	pr("1. leak：分配后 RSS %d MB，GC 三遍后仍 %d MB —— C 内存 GC 看不见\n",
		mb(leak.RSSBefore), mb(leak.RSSAfter))
	pr("2. free：同样 64MB，正确 C.free 后 RSS %d MB ≈ 基线 —— 泄漏消失\n",
		mb(free.RSSAfter))
	pr("3. go：GC 管的堆峰值 %d MB，回收后回落；RSS 平稳 — 分配活由 GC 干\n",
		goMode.GoHeapMax)
	pr("4. 红线：Go 指针不能无期限借给 C（checkptr 拦）；C 指针给 Go 随便用\n")
}

// isoSample：用独立子进程跑单一模式，得到可横向对比的隔离采样。
// 重执行自身编译产物（而非 `go run main.go`），这样从任意工作目录按文件路径运行都不受影响。
func isoSample(mode string) sample {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	out, err := exec.Command(exe, "-mode", mode, "-json").Output()
	if err != nil {
		panic(err)
	}
	var s sample
	if err := json.Unmarshal(bytes.TrimSpace(out), &s); err != nil {
		panic(err)
	}
	return s
}

func main() {
	mode := flag.String("mode", "", "leak|free|go（-json 时传单一模式）")
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	if *jsonFlag && *mode != "" {
		jdump(runMode(*mode))
		return
	}
	// 文本模式：三个子进程各跑一遍，RSS 隔离对比（C.free 的内存不还 OS，
	// 同进程内叠加会掩盖「free 更省」的事实）
	modes := make([]sample, 0, 3)
	for _, m := range []string{"leak", "free", "go"} {
		modes = append(modes, isoSample(m))
	}
	textAll(modes)
	summary(modes)
}