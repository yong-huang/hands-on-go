package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"unsafe"
)

// =============================================================================
// 01_slices：Slice 底层实现
//
// slice 在内存里只是一个三字段的头（header）：
//     type sliceHeader struct { Data uintptr; Len int; Cap int }
// 这个布局是 Go 文档承诺的稳定结构（不随版本变化），所以可以放心用 unsafe
// 读取真实 slice 的内部状态来学习，这也是与 map 不同之处。
//
// 本程序演示四件事：
//   1. slice 头长什么样（nil slice / make 出来的 slice）
//   2. append 的扩容机制（growslice 的规则 + 内存 class 对齐）
//   3. 多个 slice 共享同一块底层数组（内存别名陷阱）
//   4. append 超出容量 → 重新分配，旧数组与其他 slice 分离
// =============================================================================

// 20260813: slice 本质是一个"数组视图"——它自己不持有数据，只持有指向数组的指针和两个长度描述。

// sliceHeader 与 runtime.slice 布局一致
type sliceHeader struct {
	Data uintptr
	Len  int
	Cap  int
}

// peek 打印某个 slice 的底层信息（data 指针 + len + cap）
func peek(name string, s []int) {
	h := (*sliceHeader)(unsafe.Pointer(&s))
	fmt.Printf("  %-28s data=%p  len=%-3d cap=%-4d (data addr 0x%x)\n",
		name, s, h.Len, h.Cap, h.Data)
}

func main() {
	flag.Parse()
	if *jsonOut {
		fmt.Println(sliceJSON())
		return
	}

	fmt.Println("========== 1. slice 的底层结构 ==========")

	var s []int

	// 20260813:
	// 将切片变量 s 的地址转换为指向 sliceHeader 结构的指针。
	// - &s - 获取切片变量 s 的地址（注意：这里不是切片的底层数组地址，而是 slice header 本身的地址）
	// - unsafe.Pointer(&s) - 将这个地址转换为通用的 unsafe.Pointer 类型指针
	// - (*sliceHeader)(...) - 将通用指针强制转换为指向自定义的 sliceHeader 结构体的指针
	// - 解引用后访问字段 - 通过这个指针可以读取切片的三个内部字段：
	//   - Data - 底层数组的地址
	//   - Len - 切片长度
	//   - Cap - 切片容量
	// 这是 Go 语言中通过 unsafe 包"窥探"slice 内部实现的标准方式。

	h := (*sliceHeader)(unsafe.Pointer(&s))
	fmt.Printf("nil slice:   data=0x%x len=%d cap=%d（三个字段全为 0）\n", h.Data, h.Len, h.Cap)

	s = make([]int, 2, 4)
	s[0], s[1] = 1, 2
	peek("make([]int,2,4)", s)

	fmt.Println("\n说明：make([]int, 2, 4) 分配了容量 4 的底层数组，")
	fmt.Println("但 len 只开放前 2 个。data 是底层数组的起始地址。")

	fmt.Println("\n========== 2. append 的扩容机制 ==========")
	fmt.Println("growslice 规则（runtime/slice.go）：")
	fmt.Println("  1) 若追加后所需容量超过原 cap 的两倍 → 直接按所需容量分配")
	fmt.Println("  2) 否则：cap < 256 时翻倍；cap >= 256 时按 (cap+3*256)/4 递增")
	fmt.Println("  3) 最终按元素大小向上对齐到内存 size class（mallocgc）")
	fmt.Println("所以最终容量是\"规则 + 对齐\"的结果，具体数值随元素大小/版本略变。")

	// 小容量：严格翻倍
	fmt.Println("\n-- 小容量（<256，规则翻倍）--")
	s2 := make([]int, 16, 16)
	for i := 0; i < 16; i++ {
		s2[i] = i
	}
	s2 = append(s2, 100)
	fmt.Printf("  cap 16 -> cap %d（翻倍）\n", cap(s2))

	// 大容量：演示增长估算公式 vs 实际值
	fmt.Println("\n-- 各容量下\"增长估算\"与\"实际结果\"对比 --")
	for _, size := range []int{128, 256, 512, 1024, 2048} {
		ss := make([]int, size, size)
		for i := 0; i < size; i++ {
			ss[i] = i
		}
		estimate := growEstimate(size, size+1)
		ss = append(ss, 1)
		fmt.Printf("  cap %5d: 估算 %-5d  实际 %-5d\n", size, estimate, cap(ss))
	}
	fmt.Println("  （估算不含内存对齐；实际值 = 按 size class 向上取整后的结果）")

	fmt.Println("\n========== 3. 多个 slice 共享底层数组 ==========")

	// 20260813:
	// `s[a:b]` 不复制数据，只把 Data 指针偏移、Len/Cap 重新计算。程序从 original（cap=10）切出
	// sliceA=original[1:4] 与 sliceB=original[2:5]，打印它们相对 original 的偏移字节数与地址，
	// 然后把 sliceA[0] 改为 999——sliceB[0] 与 original[1] 一并变化，这就是"内存别名陷阱"。

	original := make([]int, 5, 10)
	for i := 0; i < 5; i++ {
		original[i] = i*10 + 1
	}
	peek("original", original)

	sliceA := original[1:4] // 指向 original[1], original[2], original[3]
	sliceB := original[2:5] // 指向 original[2], original[3], original[4]
	peek("sliceA = original[1:4]", sliceA)
	peek("sliceB = original[2:5]", sliceB)

	ha := (*sliceHeader)(unsafe.Pointer(&sliceA))
	hb := (*sliceHeader)(unsafe.Pointer(&sliceB))
	ho := (*sliceHeader)(unsafe.Pointer(&original))
	fmt.Printf("  地址关系：sliceA 偏移 original %d 字节，sliceB 偏移 %d 字节\n",
		ha.Data-ho.Data, hb.Data-ho.Data)

	fmt.Println("\n-- 修改 sliceA[0]（即 original[1]）--")
	fmt.Printf("  修改前: sliceA[0]=%d  sliceB[0]=%d  original[1]=%d\n",
		sliceA[0], sliceB[0], original[1])
	sliceA[0] = 999
	fmt.Printf("  修改后: sliceA[0]=%d  sliceB[0]=%d  original[1]=%d\n",
		sliceA[0], sliceB[0], original[1])
	fmt.Println("  结论：三个 slice 共用同一块数组，一处修改处处可见")

	fmt.Println("\n========== 4. append 超容量 → 重新分配 ==========")
	// 记录 sliceA 扩容前的底层数组地址（值拷贝到变量里，避免之后被覆盖）
	oldAddr := ha.Data
	fmt.Printf("  sliceA 扩容前: len=%d cap=%d data=0x%x\n", len(sliceA), cap(sliceA), oldAddr)

	for i := 0; i < 7; i++ { // len 3 -> 10，超过 cap 9，触发扩容
		sliceA = append(sliceA, i+100)
	}
	ha2 := (*sliceHeader)(unsafe.Pointer(&sliceA))
	fmt.Printf("  sliceA 扩容后: len=%d cap=%d data=0x%x\n", len(sliceA), cap(sliceA), ha2.Data)
	fmt.Printf("  data 地址: 0x%x -> 0x%x  %v\n", oldAddr, ha2.Data, oldAddr != ha2.Data)
	fmt.Printf("  original 地址: 0x%x（不变）\n", ho.Data)
	fmt.Printf("  sliceB 地址: 0x%x（不变）\n", hb.Data)
	fmt.Println("  结论：append 超容量时分配新数组并拷贝，旧数组归 original/sliceB 继续使用")
	fmt.Println("  （这是\"切片后 append 会悄悄分离\"的经典陷阱）")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. slice = {data, len, cap} 三个字段的头，本质是\"数组视图\"")
	fmt.Println("2. 扩容分两步：growslice 算目标容量 + 按 size class 对齐")
	fmt.Println("3. 切片（s[a:b]）不复制数据，只改 data 偏移和 len/cap")
	fmt.Println("4. append 超出 cap 会换新数组；之后与原来的 slice 不再共享")
	fmt.Println("5. unsafe 读取仅用于学习（sliceHeader 布局是官方承诺的稳定结构）")
}

// growEstimate 模拟 runtime.growslice 的容量计算（不含最终的内存 class 对齐）
// 参数含义：oldCap 原容量、requestedCap 追加后所需容量
func growEstimate(oldCap, requestedCap int) int {
	newcap := oldCap
	doublecap := newcap + newcap
	if requestedCap > doublecap {
		newcap = requestedCap
	} else {
		const threshold = 256
		if oldCap < threshold {
			newcap = doublecap
		} else {
			for 0 < newcap && newcap < requestedCap {
				newcap += (newcap + 3*threshold) / 4
			}
		}
	}
	return newcap
}

// jsonOut：输出 JSON 状态快照，供 Python viz.py 生成可视化 PNG
var jsonOut = flag.Bool("json", false, "输出 JSON 状态快照（供 viz.py）")

// sliceJSON 复刻文本演示的全过程，输出 JSON 快照
func sliceJSON() string {
	type view struct {
		Name string `json:"name"`
		Lo   int    `json:"lo"`
		Hi   int    `json:"hi"`
	}
	type sharing struct {
		ArrayCap int    `json:"array_cap"`
		Values   []int  `json:"values"`
		Views    []view `json:"views"`
	}
	type keeper struct {
		Name string `json:"name"`
		Addr string `json:"addr"`
	}
	type realloc struct {
		OldAddr string   `json:"old_addr"`
		NewAddr string   `json:"new_addr"`
		OldLen  int      `json:"old_len"`
		OldCap  int      `json:"old_cap"`
		NewLen  int      `json:"new_len"`
		NewCap  int      `json:"new_cap"`
		OldVals []int    `json:"old_vals"`
		NewVals []int    `json:"new_vals"`
		Keepers []keeper `json:"keepers"`
	}

	// 复刻演示 3：三个 slice 共享底层数组
	original := make([]int, 5, 10)
	for i := 0; i < 5; i++ {
		original[i] = i*10 + 1
	}
	sliceA := original[1:4]
	sliceB := original[2:5]
	sliceA[0] = 999

	// 复刻演示 4：append 扩容（先把旧值拷出来）
	oldAddr := (*sliceHeader)(unsafe.Pointer(&sliceA)).Data
	oldVals := append([]int(nil), sliceA...)
	for i := 0; i < 7; i++ {
		sliceA = append(sliceA, i+100)
	}
	newAddr := (*sliceHeader)(unsafe.Pointer(&sliceA)).Data

	out := struct {
		Sharing sharing `json:"sharing"`
		Realloc realloc `json:"realloc"`
	}{
		Sharing: sharing{
			ArrayCap: cap(original),
			Values:   original[:5],
			Views: []view{
				{"original[0:5]", 0, 5},
				{"sliceA[1:4]", 1, 4},
				{"sliceB[2:5]", 2, 5},
			},
		},
		Realloc: realloc{
			OldAddr: fmt.Sprintf("0x%x", oldAddr),
			NewAddr: fmt.Sprintf("0x%x", newAddr),
			OldLen:  3, OldCap: 9, NewLen: len(sliceA), NewCap: cap(sliceA),
			OldVals: oldVals,
			NewVals: sliceA,
			Keepers: []keeper{
				{"original", fmt.Sprintf("0x%x", (*sliceHeader)(unsafe.Pointer(&original)).Data)},
				{"sliceB", fmt.Sprintf("0x%x", (*sliceHeader)(unsafe.Pointer(&sliceB)).Data)},
			},
		},
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}
