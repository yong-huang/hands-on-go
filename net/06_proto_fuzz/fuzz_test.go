package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

// TestMalformedMatrix：验收①——十类畸形逐项喂解码器：
// 零 panic（panic 即测试失败），且每类被拒的错误都命中预期哨兵。
func TestMalformedMatrix(t *testing.T) {
	valid, err := (&Frame{Kind: KindData, Payload: []byte("payload-123")}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	crcValidUnknownKind := func() []byte {
		b := mutate(valid, func(b []byte) { b[5] = 0x7F })
		binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
		return b
	}()

	cases := []struct {
		name string
		in   []byte
		want string // classify 的期望输出
	}{
		{"空输入", nil, "TRUNCATED"},
		{"1 字节", []byte{0x4E}, "TRUNCATED"},
		{"冒充串线流", []byte("GET / HTTP/1.1\r\n\r\n"), "BAD_MAGIC"},
		{"未来版本", mutate(valid, func(b []byte) { b[4] = 9 }), "BAD_VERSION"},
		{"长度欺骗 0xFFFF", mutate(valid, func(b []byte) { binary.BigEndian.PutUint16(b[6:], 0xFFFF) }), "TOO_LARGE"},
		{"截断", valid[:len(valid)-2], "TRUNCATED"},
		{"载荷位翻转", mutate(valid, func(b []byte) { b[HeadSize+2] ^= 0x01 }), "BAD_CRC"},
		{"CRC 字段翻转", mutate(valid, func(b []byte) { b[len(b)-1] ^= 0x80 }), "BAD_CRC"},
		{"未知类型(CRC 合法)", crcValidUnknownKind, "BAD_KIND"},
		{"合法帧+尾巴", append(append([]byte(nil), valid...), 0x30), "TRAILING"},
		{"纯垃圾 4KB", bytes.Repeat([]byte{0xA5}, 4096), "BAD_MAGIC"},
	}
	for _, tc := range cases {
		// 分类必须命中且属于闭集：UNCLASSIFIED 出现即解码器缺陷
		_, err := DecodeFrame(tc.in)
		if got := classify(err); got != tc.want {
			t.Errorf("%s: classify = %s, want %s（err=%v）", tc.name, got, tc.want, err)
		}
		if got := classify(err); got == "UNCLASSIFIED" {
			t.Errorf("%s: 错误逃出了闭集", tc.name)
		}
	}
}

// TestResyncAfterCRCReject：坏帧（CRC 类）拒绝后，同流后续帧继续可解。
func TestResyncAfterCRCReject(t *testing.T) {
	valid, _ := (&Frame{Kind: KindData, Payload: []byte("payload-123")}).Encode()
	good2, _ := (&Frame{Kind: KindData, Payload: []byte("good-3")}).Encode()

	stream := mutate(valid, func(b []byte) { b[HeadSize+1] ^= 0x80 })
	stream = append(stream, good2...)

	first, err := DecodeFrame(stream[:len(valid)])
	if !errors.Is(err, ErrBadCRC) {
		t.Fatalf("坏帧 err = %v, want ErrBadCRC", err)
	}
	second, err := DecodeFrame(stream[len(valid):])
	if err != nil || string(second.Payload) != "good-3" {
		t.Fatalf("坏帧后的好帧解码 = (%v, %q)", err, second.Payload)
	}
	_ = first
}

// FuzzDecodeFrame：fuzz 靶场。三条不变式：
//  1. 永不 panic（panic = fuzz 找到 crasher，自动记入 testdata/fuzz/）
//  2. 错误分类是闭集（UNCLASSIFIED 出现即缺陷）
//  3. 解码成功的输入重编码后逐字节一致（编码规范唯一，无别名接受）
func FuzzDecodeFrame(f *testing.F) {
	// 种子语料：合法帧、各类攻击、边界值——fuzz 从这些起点发散
	seeds := [][]byte{
		nil,
		{0x4E},
		mustEncodeKind(KindData, "seed-valid"),
		mustEncodeKind(KindPing, ""),
		mutate(mustEncodeKind(KindData, "seed-crc"), func(b []byte) { b[len(b)-1] ^= 0xFF }),
		mutate(mustEncodeKind(KindData, "seed-len"), func(b []byte) { binary.BigEndian.PutUint16(b[6:], 0xFFFF) }),
		bytes.Repeat([]byte{0xA5}, 64),
		[]byte("GET / HTTP/1.1\r\n\r\n"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := DecodeFrame(data)
		if err != nil {
			if classify(err) == "UNCLASSIFIED" {
				t.Errorf("错误逃出闭集: %v", err)
			}
			return
		}
		// 不变式 3：成功解码 ⇒ 规范重编码逐字节一致
		re, err := frame.Encode()
		if err != nil {
			t.Fatalf("成功解码的帧重编码失败: %v", err)
		}
		if !bytes.Equal(re, data) {
			t.Fatalf("非规范接受：重编码 %d 字节 ≠ 输入 %d 字节", len(re), len(data))
		}
	})
}

func mustEncodeKind(kind Kind, s string) []byte {
	b, err := (&Frame{Kind: kind, Payload: []byte(s)}).Encode()
	if err != nil {
		panic(err)
	}
	return b
}
