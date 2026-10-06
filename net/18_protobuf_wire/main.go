// 项目 18：protobuf wire format——手写 varint、zigzag 与 tag 编解码。
//
// protobuf 是 gRPC 的线格式（wire format），三种编码原语撑起全部数字编码：
//
//	varint ：变长整数——每字节低 7 位承载数据，最高位是"还有下一字节"
//	zigzag ：有符号数映射——0,−1,+1,−2,+2 → 0,1,2,3,4（负数不再占 10 字节）
//	tag    ：字段头——(field_number << 3) | wire_type，决定字段号与载荷形态
//
// 权威判据是与 protoc 生成码逐字节一致：本篇先手写编解码器，再用
// protoc 生成的同一消息结构对拍（负数、大数、嵌套各一例）。
//
// 前置：protoc 与 protoc-gen-go 已装（本机实测 protoc 29.3）；
// 生成命令：protoc --go_out=. message.proto（见 Makefile 注释）。
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"bytes"
	"fmt"
	"math"

	"google.golang.org/protobuf/proto"
)

// WireType：载荷形态（低 3 位）。
const (
	WireVarint = 0 // int32/int64/uint32/uint64/bool/enum（varint）
	WireI64    = 1 // fixed64/sfixed64/double（8 字节定长）
	WireLen    = 2 // string/bytes/嵌套消息（长度前缀）
	WireI32    = 5 // fixed32/sfixed32/float（4 字节定长）
)

// PutVarint：把 uint64 编码为 varint 追加到 buf，返回新 buf。
// 每字节承载数据的低 7 位，最高位 MSB=1 表示"后面还有"。
func PutVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80) // 低 7 位 + 续传位
		v >>= 7
	}
	return append(buf, byte(v)) // 最后一字节 MSB=0
}

// ReadVarint：从 buf 读一个 varint，返回值与消耗的字节数。
func ReadVarint(buf []byte) (uint64, int, error) {
	var v uint64
	for shift := 0; ; shift += 7 {
		if shift >= 64 {
			return 0, 0, fmt.Errorf("varint overflow（超过 10 字节）")
		}
		if len(buf) <= shift/7 {
			return 0, 0, fmt.Errorf("varint truncated")
		}
		b := buf[shift/7]
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, shift/7 + 1, nil
		}
	}
}

// ZigzagEncode：int64 → uint64，把符号抖动摊平（−1 变 1 而不是 2^64−1）。
func ZigzagEncode(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

// ZigzagDecode：uint64 → int64（zigzag 的逆变换，位运算自逆）。
func ZigzagDecode(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// PutTag：字段头 = (field_number << 3) | wire_type，同样按 varint 编码。
func PutTag(buf []byte, fieldNum int, wireType int) []byte {
	return PutVarint(buf, uint64(fieldNum)<<3|uint64(wireType))
}

// ReadTag：读字段头，返回字段号与 wire type。
func ReadTag(buf []byte) (fieldNum int, wireType int, consumed int, err error) {
	v, n, err := ReadVarint(buf)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(v >> 3), int(v & 7), n, nil
}

// Handwritten：手写的演示消息（与 message.proto 的 TestMessage 逐字段对应）。
//
//	message TestMessage {
//	  int64  a       = 1; // zigzag（sint64）
//	  uint64 b       = 2; // varint
//	  string s       = 3; // 长度前缀
//	  Inner  inner   = 4; // 嵌套（长度前缀）
//	}
//	message Inner { int32 x = 1; } // zigzag（sint32）
type Handwritten struct {
	A     int64
	B     uint64
	S     string
	Inner int32 // Inner.x
}

// Encode：手写编码——每个字段 = tag + 载荷，按字段号顺序。
// proto3 语义：零值字段不序列化（显式存在性），手写版对齐——
// 这是与 protoc 生成码逐字节对拍时抓到的第一个差异（开发实录）。
func (m *Handwritten) Encode() []byte {
	var buf []byte
	// 字段 1：sint64（zigzag + varint）——零值跳过
	if m.A != 0 {
		buf = PutTag(buf, 1, WireVarint)
		buf = PutVarint(buf, ZigzagEncode(m.A))
	}
	// 字段 2：uint64（varint）
	if m.B != 0 {
		buf = PutTag(buf, 2, WireVarint)
		buf = PutVarint(buf, m.B)
	}
	// 字段 3：string（tag + 长度 varint + 字节）——空串跳过
	if m.S != "" {
		buf = PutTag(buf, 3, WireLen)
		buf = PutVarint(buf, uint64(len(m.S)))
		buf = append(buf, m.S...)
	}
	// 字段 4：嵌套 Inner——message 字段与标量不同：只要"存在"（非 nil）就恒编码，
	// 哪怕内部全零（空载荷 = tag + 长度 0）。零值省略只对标量生效。
	// 对拍实录：官方对 Inner{X:0} 输出 22 00（tag + len=0）。
	if hwInner := m.Inner; true {
		var inner []byte
		if hwInner != 0 { // Inner.x 是标量：零值时载荷为空
			inner = PutTag(inner, 1, WireVarint)
			inner = PutVarint(inner, ZigzagEncode(int64(hwInner)))
		}
		buf = PutTag(buf, 4, WireLen)
		buf = PutVarint(buf, uint64(len(inner)))
		buf = append(buf, inner...)
	}
	return buf
}

func main() {
	fmt.Println("== 第 1 步：varint 逐字节解剖 ==")
	for _, v := range []uint64{0, 1, 127, 128, 300, math.MaxUint64} {
		buf := PutVarint(nil, v)
		fmt.Printf("  %-20d → % x（%d 字节）\n", v, buf, len(buf))
	}

	fmt.Println()
	fmt.Println("== 第 2 步：zigzag——负数不再占 10 字节 ==")
	fmt.Println("  值        zigzag 后        varint 字节数")
	for _, v := range []int64{0, -1, 1, -2, 2, math.MinInt64} {
		z := ZigzagEncode(v)
		buf := PutVarint(nil, z)
		fmt.Printf("  %-10d → %-16d %d 字节\n", v, z, len(buf))
	}
	fmt.Println("  ⇒ 不做 zigzag 的 int64 负数恒占 10 字节（补码全 1）；sint 系列专为负数设计。")

	fmt.Println()
	fmt.Println("== 第 3 步：tag 解剖——字段号决定头的大小 ==")
	for _, fn := range []int{1, 15, 16, 2047, 2048} {
		buf := PutTag(nil, fn, WireVarint)
		fmt.Printf("  字段号 %-5d → tag % x（%d 字节）\n", fn, buf, len(buf))
	}
	fmt.Println("  ⇒ 字段号 1~15 的 tag 只有 1 字节（3 位字段号+3 位类型挤进 7 位）——")
	fmt.Println("     把最高频字段编成 1~15 是 protobuf 的公开优化纪律。")

	fmt.Println()
	fmt.Println("== 第 4 步：手写 vs protoc 生成码——逐字节对拍 ==")
	demoAgainstProtoc()
}

// demoAgainstProtoc：三个对拍用例——负数、大数、嵌套。
// protoc 生成码（message.pb.go 的 TestMessage）是线格式的权威实现。
func demoAgainstProtoc() {
	cases := []Handwritten{
		{A: -1, B: 2, S: "neg", Inner: -3},
		{A: 1 << 62, B: math.MaxUint64, S: "big numbers", Inner: 1 << 30},
		{A: 42, B: 7, S: "nested", Inner: -100},
	}
	for i, hw := range cases {
		mine := hw.Encode()

		pb := &TestMessage{
			A:     hw.A,
			B:     hw.B,
			S:     hw.S,
			Inner: &Inner{X: hw.Inner},
		}
		theirs, err := proto.Marshal(pb)
		if err != nil {
			fmt.Println("  proto.Marshal:", err)
			return
		}
		same := bytes.Equal(mine, theirs)
		fmt.Printf("  用例 %d（%d 字节）逐字节一致=%v\n    手写 % x\n    官方 % x\n",
			i+1, len(mine), same, mine, theirs)
		if !same {
			fmt.Println("  ⇒ 不一致！请检查编码顺序与字段号")
			return
		}
	}
	fmt.Println("  ⇒ 三例全部逐字节一致：手写实现与 protoc 生成码等价。")
}
