package main

// 项目 17：内存对齐与 struct 优化
//
// 结构体大小由"字段对齐"决定：每个字段的偏移必须是其自身对齐值的倍数，
// 编译器会在字段间/结构体尾部插填充（padding）。字段排得差，单纯一个
// "声明顺序"就能让结构体从 2 个字变成 3 个字。
//
// 本项目用 unsafe.Sizeof / Alignof / Offsetof 打印几种排序的实际大小
// 与字段偏移，肉眼看出对齐规则和填充在哪。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"unsafe"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 结构体定义 ----------

// 三个字段：两个 int64（8B 对齐）+ 一个 int8（1B 对齐）
type badOrder struct {
	a int8
	b int64
	c int8
}

type goodOrder struct {
	b int64
	a int8
	c int8
}

// 混合类型
type mixed struct {
	p *int
	f float64
	b bool
	s string
}

// ---------- 字段表 ----------

type field struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   uintptr `json:"size"`
	Align  uintptr `json:"align"`
	Offset uintptr `json:"offset"`
}

type structInfo struct {
	Name   string  `json:"name"`
	Size   uintptr `json:"size"`
	Align  uintptr `json:"align"`
	Fields []field `json:"fields"`
}

func describe(name string, size, align uintptr, fields []field) structInfo {
	return structInfo{Name: name, Size: size, Align: align, Fields: fields}
}

func exp() []structInfo {
	bad := new(badOrder)
	good := new(goodOrder)
	mx := new(mixed)

	infos := []structInfo{
		describe("badOrder(a int8, b int64, c int8)",
			unsafe.Sizeof(*bad), unsafe.Alignof(*bad), []field{
				{"a", "int8", unsafe.Sizeof(bad.a), unsafe.Alignof(bad.a), unsafe.Offsetof(bad.a)},
				{"b", "int64", unsafe.Sizeof(bad.b), unsafe.Alignof(bad.b), unsafe.Offsetof(bad.b)},
				{"c", "int8", unsafe.Sizeof(bad.c), unsafe.Alignof(bad.c), unsafe.Offsetof(bad.c)},
			}),
		describe("goodOrder(b int64, a int8, c int8)",
			unsafe.Sizeof(*good), unsafe.Alignof(*good), []field{
				{"b", "int64", unsafe.Sizeof(good.b), unsafe.Alignof(good.b), unsafe.Offsetof(good.b)},
				{"a", "int8", unsafe.Sizeof(good.a), unsafe.Alignof(good.a), unsafe.Offsetof(good.a)},
				{"c", "int8", unsafe.Sizeof(good.c), unsafe.Alignof(good.c), unsafe.Offsetof(good.c)},
			}),
		describe("mixed(p *int, f float64, b bool, s string)",
			unsafe.Sizeof(*mx), unsafe.Alignof(*mx), []field{
				{"p", "*int", unsafe.Sizeof(mx.p), unsafe.Alignof(mx.p), unsafe.Offsetof(mx.p)},
				{"f", "float64", unsafe.Sizeof(mx.f), unsafe.Alignof(mx.f), unsafe.Offsetof(mx.f)},
				{"b", "bool", unsafe.Sizeof(mx.b), unsafe.Alignof(mx.b), unsafe.Offsetof(mx.b)},
				{"s", "string", unsafe.Sizeof(mx.s), unsafe.Alignof(mx.s), unsafe.Offsetof(mx.s)},
			}),
	}

	for _, info := range infos {
		if !silent {
			pr("== %s : 大小=%d 对齐=%d\n", info.Name, info.Size, info.Align)
			sum := uintptr(0)
			for _, f := range info.Fields {
				pad := int(f.Offset - sum)
				pr("     %-2s %-9s 偏移=%-2d (pad=%d)\n", f.Name, f.Type, f.Offset, pad)
				sum = f.Offset + f.Size
			}
			pr("\n")
		}
	}
	return infos
}

func summary() {
	pr("========== 结论 ==========\n")
	pr("int64×2 + int8×2 字节：排成 8B 后跟 1B+1B，只要 16B；int8 插中间要三块 = 24B\n")
	pr("规则：字段偏移 = 自身对齐值的倍数；结构体大小 = 最大成员对齐值的倍数\n")
	pr("优化口诀：大字段放前 / 等宽排布，或用字段顺序交换把填充挤到尾部\n")
	pr("   badOrder 24B vs goodOrder 16B —— 只变顺序，省 1/3\n")
}

func jsonOut() {
	res := map[string]interface{}{"structs": exp()}
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