package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"unsafe"
)

// =============================================================================
// 04_string：String 底层实现（只读字节数组）
//
// string 的底层是一个两字段的头（与 slice 相比没有 cap）：
//     type stringHeader struct {        // runtime/string.go
//         str unsafe.Pointer            // 指向一个只读的字节数组
//         len int                       // 字节数（注意是字节，不是字符）
//     }
// 这个布局是官方文档承诺的稳定结构（reflect.StringHeader），所以可以放心用
// unsafe 读取真实 string 的内部状态来学习（与 map/channel 不同）。
//
// 本程序演示四件事：
//   1. string 头长什么样：字面量 vs 运行时构造（数据在哪个内存区域）
//   2. 字符串切片 s[a:b]：共享底层字节数组，只改指针偏移 + len
//   3. string → []byte 转换：分配新内存拷贝（可修改）
//   4. []byte → string 转换：同样拷贝一份（保证不可变）
//
// 关键结论：string 不可变（只读），但底层字节数组可以被多个 string 共享；
// 任何"可变"需求（[]byte）都必须先拷贝，这就是转换产生内存分配的原因。
// =============================================================================

// stringHeader 与 runtime.stringStruct 布局一致（= reflect.StringHeader）
type stringHeader struct {
	Data uintptr
	Len  int
}

// sh 读取一个 string 的底层头
func sh(s string) *stringHeader {
	return (*stringHeader)(unsafe.Pointer(&s))
}

// jsonOut：输出 JSON 状态快照，供 Python viz.py 生成可视化 PNG
var jsonOut = flag.Bool("json", false, "输出 JSON 状态快照（供 viz.py）")

func main() {
	flag.Parse()
	if *jsonOut {
		fmt.Println(stringJSON())
		return
	}

	fmt.Println("========== 1. string 的底层结构 ==========")
	fmt.Println("string = { str 指针, len }，共 16 字节；指向的字节数组是只读的。")

	lit := "hello"
	fmt.Printf("  字面量     %-20q data=0x%x len=%d\n", lit, sh(lit).Data, sh(lit).Len)
	fmt.Println("  → 编译器把字面量放在程序的只读数据区（rodata）")

	runtimeMade := string([]byte{'h', 'e', 'l', 'l', 'o'})
	fmt.Printf("  运行时构造  %-20q data=0x%x len=%d\n", runtimeMade, sh(runtimeMade).Data, sh(runtimeMade).Len)
	fmt.Println("  → 运行时用 []byte 拼出的 string，数据在堆/栈上，与字面量地址不同")

	fmt.Println("\n========== 2. 字符串切片：共享底层字节 ==========")
	s := "gopher"
	sub := s[1:5] // "ophe"
	hs, hsub := sh(s), sh(sub)
	fmt.Printf("  s   = %-10q data=0x%x len=%d\n", s, hs.Data, hs.Len)
	fmt.Printf("  sub = %-10q data=0x%x len=%d\n", sub, hsub.Data, hsub.Len)
	fmt.Printf("  偏移：sub.data - s.data = %d 字节（不拷贝，只移动指针）\n", hsub.Data-hs.Data)
	fmt.Println("  结论：字符串切片 s[a:b] 不复制数据，两个 string 共享同一块只读字节数组")

	fmt.Println("\n========== 3. string → []byte：拷贝（可修改）==========")
	b := []byte(lit)
	fmt.Printf("  s.data = 0x%x    b[0] 地址 = %p\n", sh(lit).Data, &b[0])
	fmt.Printf("  两者不同 → 发生拷贝（%v）\n", uintptr(unsafe.Pointer(&b[0])) != uintptr(unsafe.Pointer(&lit)))
	b[0] = 'j'
	fmt.Printf("  修改 b[0]='j' 后：b=%q  s=%q（s 不受影响，因为底层是两份数据）\n", b, lit)
	fmt.Println("  → 必须拷贝的原因：string 只读，[]byte 可写，不能共用同一块内存")

	fmt.Println("\n========== 4. []byte → string：也拷贝 ==========")
	bs := []byte{'h', 'i'}
	t := string(bs)
	hbs := uintptr(unsafe.Pointer(&bs[0]))
	ht := sh(t).Data
	fmt.Printf("  bs[0] 地址 = %p     t.data = 0x%x\n", &bs[0], sh(t).Data)
	fmt.Printf("  两者不同 → 又发生一次拷贝（%v）\n", ht != hbs)
	bs[0] = 'X'
	fmt.Printf("  修改 bs[0]='X' 后：bs=%q  t=%q（t 不变）\n", bs, t)
	fmt.Println("  → 同样：为了 string 的不可变，[]byte→string 也拷贝")

	fmt.Println("\n========== 5. 不可变性（编译期保证）==========")
	fmt.Println("  下面这行代码编译不过（注释掉）:")
	fmt.Println("  // lit[0] = 'a'   // 编译错误：cannot assign to lit[0] (neither addressable nor a map index expression)")
	fmt.Println("  string 只读由语言层面强制，运行时无法原地修改")

	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. string = {ptr, len} 两字段头，共 16 字节；无 cap")
	fmt.Println("2. 底层字节数组只读；字面量在 rodata，运行时构造的在堆/栈")
	fmt.Println("3. 字符串切片 s[a:b] 共享底层数据，只改指针偏移和 len")
	fmt.Println("4. string→[]byte、[]byte→string 都拷贝（为保证只读语义）")
	fmt.Println("5. len 是字节数：中文字符一个占 3 字节，与 rune 数不同")
}

// =============================================================================
// JSON 状态快照：供 Python viz.py 生成可视化 PNG（-json 标志）
// =============================================================================

// stringJSON 复刻文本演示的全过程，输出 JSON 快照
func stringJSON() string {
	type head struct {
		Addr string `json:"addr"`
		Len  int    `json:"len"`
		Text string `json:"text"`
	}
	type share struct {
		Base   head `json:"base"`
		Sub    head `json:"sub"`
		Offset int  `json:"offset"`
	}
	type conv struct {
		SAddr  string `json:"s_addr"`
		BAddr  string `json:"b_addr"`
		Copied bool   `json:"copied"`
		B      string `json:"b"`
		S      string `json:"s"`
	}

	lit := "hello"
	runtimeMade := string([]byte{'h', 'e', 'l', 'l', 'o'})

	s := "gopher"
	sub := s[1:5]
	offset := int(sh(sub).Data - sh(s).Data)

	b := []byte(lit)
	bAddr := uintptr(unsafe.Pointer(&b[0]))
	sAddr := uintptr(unsafe.Pointer(&lit))
	b[0] = 'j'

	bs := []byte{'h', 'i'}
	t := string(bs)
	copied2 := sh(t).Data != uintptr(unsafe.Pointer(&bs[0]))
	bs[0] = 'X'

	out := struct {
		Header struct {
			Literal  head `json:"literal"`
			Computed head `json:"computed"`
		} `json:"header"`
		Sharing share `json:"sharing"`
		SToB    conv  `json:"s_to_b"`
		BToS    conv  `json:"b_to_s"`
	}{
		Header: struct {
			Literal  head `json:"literal"`
			Computed head `json:"computed"`
		}{
			Literal:  head{fmt.Sprintf("0x%x", sAddr), len(lit), lit},
			Computed: head{fmt.Sprintf("0x%x", sh(runtimeMade).Data), len(runtimeMade), runtimeMade},
		},
		Sharing: share{
			Base:   head{fmt.Sprintf("0x%x", sh(s).Data), len(s), s},
			Sub:    head{fmt.Sprintf("0x%x", sh(sub).Data), len(sub), sub},
			Offset: offset,
		},
		SToB: conv{
			SAddr:  fmt.Sprintf("0x%x", sAddr),
			BAddr:  fmt.Sprintf("0x%x", bAddr),
			Copied: bAddr != sAddr,
			B:      string(b),
			S:      lit,
		},
		BToS: conv{
			SAddr:  fmt.Sprintf("0x%x", sh(t).Data),
			BAddr:  fmt.Sprintf("0x%x", uintptr(unsafe.Pointer(&bs[0]))),
			Copied: copied2,
			B:      string(bs),
			S:      t,
		},
	}
	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(buf)
}
