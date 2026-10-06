package main

import (
	"bytes"
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestVarintBoundaries：varint 的边界值编码——127 单字节、128 两字节、
// MaxUint64 十字节（往返一致）。
func TestVarintBoundaries(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 40, math.MaxUint64} {
		buf := PutVarint(nil, v)
		got, n, err := ReadVarint(buf)
		if err != nil || got != v {
			t.Fatalf("varint(%d) → % x，往返=%d (n=%d, err=%v)", v, buf, got, n, err)
		}
		if n != len(buf) {
			t.Fatalf("varint(%d) 消耗 %d 字节, want %d", v, n, len(buf))
		}
	}
	if got := PutVarint(nil, 127); len(got) != 1 {
		t.Fatalf("127 编码 %d 字节, want 1", len(got))
	}
	if got := PutVarint(nil, 128); len(got) != 2 || got[0] != 0x80 || got[1] != 0x01 {
		t.Fatalf("128 编码 = % x, want 80 01", got)
	}
	if got := PutVarint(nil, math.MaxUint64); len(got) != 10 {
		t.Fatalf("MaxUint64 编码 %d 字节, want 10", len(got))
	}
}

// TestZigzagHandCalculated：验收②——zigzag 对 −1/1/−2^31 的手算断言。
// zigzag(v) = (v << 1) ^ (v >> 63)：0→0、−1→1、1→2、−2→3、−2^31→2^31−1。
func TestZigzagHandCalculated(t *testing.T) {
	cases := []struct {
		in   int64
		want uint64
	}{
		{0, 0},
		{-1, 1},
		{1, 2},
		{-2, 3},
		{2, 4},
		{math.MinInt32, math.MaxUint32}, // −2^31 → 2^32−1（zigzag 全域均匀）
		{math.MaxInt64, math.MaxUint64 - 1},
		{math.MinInt64, math.MaxUint64},
	}
	for _, c := range cases {
		if got := ZigzagEncode(c.in); got != c.want {
			t.Errorf("ZigzagEncode(%d) = %d, want %d", c.in, got, c.want)
		}
		if back := ZigzagDecode(c.want); back != c.in {
			t.Errorf("ZigzagDecode(%d) = %d, want %d（往返）", c.want, back, c.in)
		}
	}
	// 核心命题：负数经 zigzag + varint 后不再占 10 字节（int64 负数裸 varint 恒 10 字节）
	neg := PutVarint(nil, ZigzagEncode(-1))
	if len(neg) != 1 {
		t.Fatalf("zigzag(−1) 的 varint 占 %d 字节, want 1", len(neg))
	}
	raw := PutVarint(nil, uint64(^uint64(0))) // 不 zigzag 的负数（补码全 1 = MaxUint64）
	if len(raw) != 10 {
		t.Fatalf("裸 int64(−1) 的 varint 占 %d 字节, want 10", len(raw))
	}
}

// TestFieldNumberTagSizes：验收③——字段号影响 tag 字节数：
// 1~15 → 1 字节；16~2047 → 2 字节；2048+ → 3 字节。
func TestFieldNumberTagSizes(t *testing.T) {
	cases := []struct {
		field int
		wantN int
	}{
		{1, 1}, {15, 1},
		{16, 2}, {2047, 2},
		{2048, 3},
	}
	for _, c := range cases {
		buf := PutTag(nil, c.field, WireVarint)
		if len(buf) != c.wantN {
			t.Errorf("字段号 %d 的 tag = % x（%d 字节）, want %d 字节",
				c.field, buf, len(buf), c.wantN)
		}
		// 往返
		fn, wt, n, err := ReadTag(buf)
		if err != nil || fn != c.field || wt != WireVarint || n != len(buf) {
			t.Errorf("ReadTag(% x) = (%d, %d, %d, %v)", buf, fn, wt, n, err)
		}
	}
}

// TestByteEqualWithProtoc：验收①——负数、大数、嵌套三例，
// 手写编码与 protoc 生成码逐字节一致。
func TestByteEqualWithProtoc(t *testing.T) {
	cases := []Handwritten{
		{A: -1, B: 2, S: "neg", Inner: -3},
		{A: 1 << 62, B: math.MaxUint64, S: "big numbers", Inner: 1 << 30},
		{A: 42, B: 7, S: "nested", Inner: -100},
		{}, // 全零值：官方输出空字节（proto3 默认不序列化零值），手写必须同样处理
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
			t.Fatalf("用例 %d proto.Marshal: %v", i, err)
		}
		if !bytes.Equal(mine, theirs) {
			t.Fatalf("用例 %d 不一致：\n  手写 % x\n  官方 % x", i, mine, theirs)
		}
	}
}

// TestZeroValueRules：proto3 零值语义的两条规则（对拍实录）：
//
//	标量零值省略（官方与手写全零 TestMessage 均无字段 1/2/3 字节）；
//	非 nil 的 message 字段恒编码——哪怕内部全零（官方输出 22 00：tag+长度 0）。
func TestZeroValueRules(t *testing.T) {
	hw := Handwritten{} // 全零标量 + Inner 恒编码
	mine := hw.Encode()

	pb := &TestMessage{Inner: &Inner{}}
	theirs, err := proto.Marshal(pb)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	if !bytes.Equal(mine, theirs) {
		t.Fatalf("全零消息不一致：手写 % x vs 官方 % x", mine, theirs)
	}
	if want := []byte{0x22, 0x00}; !bytes.Equal(mine, want) {
		t.Fatalf("全零消息 = % x, want 22 00（仅 message 字段的存在性标记）", mine)
	}
}
