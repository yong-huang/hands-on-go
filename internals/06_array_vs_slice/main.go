package main

// 项目 6：Slice vs Array 内存布局对比
//
// 数组是"值"：N 个元素直接内联、连续排列，sizeof 就是 N*elemSize；
// slice 是"结构体"：变量本身只有 {Data, Len, Cap} 三个字段（24 字节），
// 真正的数据在它指向的底层数组上。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 状态快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"reflect"
	"unsafe"
)

func dumpBytes(ptr unsafe.Pointer, n int) {
	b := unsafe.Slice((*byte)(ptr), n)
	for i := 0; i < len(b); i++ {
		fmt.Printf("%02x ", b[i])
		if i%8 == 7 {
			fmt.Print("| ")
		}
	}
	fmt.Println()
}

func bytesHex(ptr unsafe.Pointer, n int) string {
	b := unsafe.Slice((*byte)(ptr), n)
	s := ""
	for i, x := range b {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("%02x", x)
	}
	return s
}

// ---------- 第 1 节：数组是内联的连续值 ----------

func section1(arr *[5]int) {
	fmt.Println("========== 1. 数组 [5]int 是「值」：元素内联、连续排列 ==========")
	fmt.Printf("  sizeof(arr) = %d 字节（5 个元素直接放在变量里，没有指针）\n", unsafe.Sizeof(*arr))
	for i := 0; i < 5; i++ {
		fmt.Printf("  &arr[%d] = 0x%x  value=%d\n", i,
			uintptr(unsafe.Pointer(&(*arr)[i])), (*arr)[i])
	}
	fmt.Printf("  &arr     = 0x%x（== &arr[0]，数组名就是首元素地址）\n",
		uintptr(unsafe.Pointer(arr)))
	fmt.Printf("  内存字节: ")
	dumpBytes(unsafe.Pointer(arr), int(unsafe.Sizeof(*arr)))
}

// ---------- 第 2 节：slice 是 {Data, Len, Cap} 结构体 ----------

func section2(sl *[]int) {
	fmt.Println("\n========== 2. slice 是结构体：{ Data, Len, Cap } ==========")
	h := (*reflect.SliceHeader)(unsafe.Pointer(sl))
	fmt.Printf("  sizeof(sl) = %d 字节（只有 header 的三个字段）\n", unsafe.Sizeof(*sl))
	fmt.Printf("  &sl     = 0x%x   （header 变量本身）\n", uintptr(unsafe.Pointer(sl)))
	fmt.Printf("  Data    = 0x%x   （指向底层数组）\n", h.Data)
	fmt.Printf("  Len     = %d        Cap = %d\n", h.Len, h.Cap)
	fmt.Printf("  &sl 与 Data 不同 → 变量里没有数据，数据在别处\n")
	fmt.Printf("  底层数组字节: ")
	dumpBytes(unsafe.Pointer(h.Data), h.Len*int(unsafe.Sizeof(int(0))))
}

// ---------- 第 3 节：本质区别与传参 ----------

func copyArr(a [5]int) {
	fmt.Printf("    函数内 &a = 0x%x（按值传入 → 拷贝整 40 字节）\n",
		uintptr(unsafe.Pointer(&a)))
}
func copySl(s []int) {
	h := (*reflect.SliceHeader)(unsafe.Pointer(&s))
	fmt.Printf("    函数内 &s = 0x%x（只拷 24 字节 header），s.Data = 0x%x（与外面共享底层数组）\n",
		uintptr(unsafe.Pointer(&s)), h.Data)
}

func section3(arr *[5]int, sl *[]int) {
	fmt.Println("\n========== 3. 本质区别与传参行为 ==========")
	h := (*reflect.SliceHeader)(unsafe.Pointer(sl))
	fmt.Printf("  数组: &arr = 0x%x == &arr[0]，无额外指针层\n", uintptr(unsafe.Pointer(arr)))
	fmt.Printf("  slice: &sl = 0x%x != Data 0x%x，多一层 header\n",
		uintptr(unsafe.Pointer(sl)), h.Data)
	fmt.Println("  传参：")
	copyArr(*arr)
	copySl(*sl)
	fmt.Println("  → 传数组 = 全量拷贝；传 slice = 只拷 header，数据共享")
}

// ---------- 第 4 节：切片产生新 header，只移指针 ----------

func section4() {
	fmt.Println("\n========== 4. 切片 a[1:4]：产生新 header，不复制数据 ==========")
	a := [5]int{1, 2, 3, 4, 5}
	sA := a[:]
	sB := a[1:4]
	hA := (*reflect.SliceHeader)(unsafe.Pointer(&sA))
	hB := (*reflect.SliceHeader)(unsafe.Pointer(&sB))
	fmt.Printf("  sA = a[:]     Data=0x%x  Len=%d  Cap=%d\n", hA.Data, hA.Len, hA.Cap)
	fmt.Printf("  sB = a[1:4]   Data=0x%x  Len=%d  Cap=%d\n", hB.Data, hB.Len, hB.Cap)
	fmt.Printf("  偏移 = Data(sB) - Data(sA) = %d 字节（= 1 个元素）\n", hB.Data-hA.Data)
	fmt.Printf("  修改 sB[0] 会影响 sA：sA = %v  sB = %v（同一块底层数组）\n", sA, sB)
	sB[0] = 99
	fmt.Printf("  改后: sA = %v  sB = %v（内存别名）\n", sA, sB)
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. 数组 = 连续的内联值，sizeof = N*elemSize；数组名即首元素地址")
	fmt.Println("2. slice = { Data, Len, Cap } 三个字段的结构体（24 字节）")
	fmt.Println("3. 传数组全量拷贝；传 slice 只拷 header，底层数组共享")
	fmt.Println("4. 切片产生新 header，Data 只是偏移移动，不复制数据")
}

// ---------- JSON 状态快照 ----------

func jsonOut() {
	arr := [5]int{1, 2, 3, 4, 5}
	sl := []int{1, 2, 3, 4, 5}
	h := (*reflect.SliceHeader)(unsafe.Pointer(&sl))
	elems := make([]map[string]interface{}, 5)
	for i := 0; i < 5; i++ {
		elems[i] = map[string]interface{}{
			"i":    i,
			"addr": fmt.Sprintf("0x%x", uintptr(unsafe.Pointer(&arr[i]))),
			"val":  fmt.Sprintf("%d", arr[i]),
		}
	}
	a := [5]int{1, 2, 3, 4, 5}
	sA := a[:]
	sB := a[1:4]
	hA := (*reflect.SliceHeader)(unsafe.Pointer(&sA))
	hB := (*reflect.SliceHeader)(unsafe.Pointer(&sB))
	out := map[string]interface{}{
		"array": map[string]interface{}{
			"sizeof":    unsafe.Sizeof(arr),
			"n":         5,
			"elem_size": unsafe.Sizeof(arr[0]),
			"addr":      fmt.Sprintf("0x%x", uintptr(unsafe.Pointer(&arr))),
			"elems":     elems,
			"bytes":     bytesHex(unsafe.Pointer(&arr), int(unsafe.Sizeof(arr))),
		},
		"slice": map[string]interface{}{
			"sizeof":     unsafe.Sizeof(sl),
			"sl_addr":    fmt.Sprintf("0x%x", uintptr(unsafe.Pointer(&sl))),
			"data":       fmt.Sprintf("0x%x", h.Data),
			"len":        h.Len,
			"cap":        h.Cap,
			"data_bytes": bytesHex(unsafe.Pointer(h.Data), h.Len*int(unsafe.Sizeof(int(0)))),
		},
		"sharing": map[string]interface{}{
			"base_data": fmt.Sprintf("0x%x", hA.Data),
			"base_len":  hA.Len,
			"base_cap":  hA.Cap,
			"sub_data":  fmt.Sprintf("0x%x", hB.Data),
			"sub_len":   hB.Len,
			"sub_cap":   hB.Cap,
			"offset":    hB.Data - hA.Data,
			"elem_size": unsafe.Sizeof(int(0)),
		},
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
	arr := [5]int{1, 2, 3, 4, 5}
	sl := []int{1, 2, 3, 4, 5}
	section1(&arr)
	section2(&sl)
	section3(&arr, &sl)
	section4()
	summary()
}
