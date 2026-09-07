package main

// 项目 14：内存分配器——mcache / mcentral / mheap 三级结构
//
// Go 为 67 个 size class 建了三级分配网：
//   - mcache  ：每个 P 的私有小仓，命中即取，无锁
//   - mcentral：每 size class 一个中心，跨 P 借 span 要换锁
//   - mheap   ：全局堆，管 span 复用；>32KB 大对象直接在这分配（绕过前两级）
//
// 分配路径判定：
//   <16B   且不含指针 → tiny allocator，多个小对象挤进同一个 16B 块（合并）
//   16B~32KB          → 按 size class 先从 mcache 现拿，miss 再逐级上溯
//   >32KB             → 直通 mheap，每个对象独占一个 span
//
// 本项目用地址分布 + HeapInuse 增量实证三条路径的差异：
//   tiny：地址挤在同一个 16B 块（偏移 <16）
//   small：地址按 64B 间距密集排列（同 span 空间复用）
//   large：地址按 ~64KB span 大步分离（独占）
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"unsafe"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

func heapInuse() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// ---------- A：tiny（无指针 <16B）合并进 16B 块 ----------

type byteObj struct{ _ byte }

func allocTiny(n int) []*byteObj {
	ptrs := make([]*byteObj, 0, n)
	for i := 0; i < n; i++ {
		v := byteObj{}
		ptrs = append(ptrs, &v)
	}
	return ptrs
}

func expA() []int64 {
	ptrs := allocTiny(24)
	base := uintptr(unsafe.Pointer(ptrs[0]))
	var offsets []int64
	for _, p := range ptrs {
		offsets = append(offsets, int64(uintptr(unsafe.Pointer(p))-base))
	}
	within := 0
	for _, o := range offsets {
		if o < 16 {
			within++
		}
	}
	pr("========== A. tiny：24 个 1B 对象 ==========\n")
	pr("   相对首个对象偏移(bytes)：%v\n", offsets)
	pr("   → %d/24 落在头一个 16B 块里（tiny allocator 合并分配，无指针对象不会各占各的）\n", within)
	return offsets
}

// ----------- B：small（16B~32KB）按 size class 走 mcache ----------

type smallObj struct{ buf [64]byte }

func allocSmall(n int) []*smallObj {
	ptrs := make([]*smallObj, 0, n)
	for i := 0; i < n; i++ {
		v := smallObj{}
		ptrs = append(ptrs, &v)
	}
	return ptrs
}

type smallRes struct {
	Gaps      []int64 `json:"gaps"`
	InuseUp   uint64  `json:"heap_inuse_delta"`
}

func expB() smallRes {
	inuse0 := heapInuse()
	ptrs := allocSmall(256)
	inuse1 := heapInuse()

	// 排好序再算相邻地址间隔，展示同 span 内的紧凑排布
	var addrs []uintptr
	for _, p := range ptrs {
		addrs = append(addrs, uintptr(unsafe.Pointer(p)))
	}
	// 简单选择排序（n=256 足够）
	for i := 0; i < len(addrs)-1; i++ {
		for j := i + 1; j < len(addrs); j++ {
			if addrs[j] < addrs[i] {
				addrs[i], addrs[j] = addrs[j], addrs[i]
			}
		}
	}
	var gaps []int64
	for i := 1; i < len(addrs); i++ {
		gaps = append(gaps, int64(addrs[i]-addrs[i-1]))
	}

	pr("========== B. small：64B 对象 ==========\n")
	pr("   排序后相邻地址间隔：%v ...\n", gaps[:10])
	pr("   → 间隔基本是 64B（size class）的倍数：同 span 里紧凑排列；HeapInuse +%d B\n",
		inuse1-inuse0)
	return smallRes{Gaps: gaps, InuseUp: inuse1 - inuse0}
}

// ----------- C：large（>32KB）直通 mheap ----------

type bigObj [6000]int64 // 48KB

func allocBig(n int) []*bigObj {
	ptrs := make([]*bigObj, 0, n)
	for i := 0; i < n; i++ {
		v := bigObj{}
		v[0] = int64(i)
		ptrs = append(ptrs, &v)
	}
	return ptrs
}

type largeRes struct {
	Deltas  []int64 `json:"deltas"`   // 相邻对象地址间隔
	Aligns  []int64 `json:"aligns"`   // 页面内偏移（%4096），期望对齐 0
	InuseUp uint64  `json:"heap_inuse_delta"`
}

func expC() largeRes {
	inuse0 := heapInuse()
	objs := allocBig(4)
	inuse1 := heapInuse()

	var addrs []uintptr
	for _, o := range objs {
		addrs = append(addrs, uintptr(unsafe.Pointer(o)))
	}
	// 排序
	for i := 0; i < len(addrs)-1; i++ {
		for j := i + 1; j < len(addrs); j++ {
			if addrs[j] < addrs[i] {
				addrs[i], addrs[j] = addrs[j], addrs[i]
			}
		}
	}
	var deltas, aligns []int64
	for i := 1; i < len(addrs); i++ {
		deltas = append(deltas, int64(addrs[i]-addrs[i-1]))
	}
	for _, a := range addrs {
		aligns = append(aligns, int64(a&0xfff))
	}
	pr("========== C. large：48KB 对象 ==========\n")
	pr("   相邻地址间隔(bytes)：%v\n", deltas)
	pr("   页内偏移（期望对齐到页首，≈0）：%v\n", aligns)
	pr("   → 每个大对象独占一个 span（相邻间隔=对象大小 48KB），地址页对齐（偏移=0）；HeapInuse +%d B\n",
		inuse1-inuse0)
	return largeRes{Deltas: deltas, Aligns: aligns, InuseUp: inuse1 - inuse0}
}

func summary() {
	pr("\n========== 总结 ==========\n")
	pr("1. tiny：无指针小对象合并进 16B 块，分配次数远小于对象数\n")
	pr("2. small：按 size class 从 mcache 无锁现取，miss 才逐级上溯到 mcentral/mheap\n")
	pr("3. large：直通 mheap、独占 span、页对齐，绕过两级缓存\n")
	pr("4. 设计哲学：无锁优先 → 中心加锁 → 全局协调，代价逐级上升，绝不让常用路径碰锁\n")

}

func jsonOut() {
	res := map[string]interface{}{
		"tiny_offsets": expA(),
		"small":        expB(),
		"large":        expC(),
	}
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
	expA()
	expB()
	expC()
	summary()
}