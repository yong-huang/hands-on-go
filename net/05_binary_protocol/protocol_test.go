package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/rand"
	"net"
	"testing"
	"time"
)

// TestRoundTrip1000：验收①——1000 条随机消息（类型与载荷长度随机）
// 编码 → 解码，全部字段全等。
func TestRoundTrip1000(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	kinds := []Kind{KindData, KindPing, KindPong}
	for i := 0; i < 1000; i++ {
		n := rnd.Intn(300) // 载荷 0~299 字节，含空载荷
		payload := make([]byte, n)
		rnd.Read(payload)
		in := &Frame{Kind: kinds[rnd.Intn(len(kinds))], Payload: payload}

		raw, err := in.Encode()
		if err != nil {
			t.Fatalf("第 %d 条 Encode: %v", i, err)
		}
		out, err := DecodeFrame(raw)
		if err != nil {
			t.Fatalf("第 %d 条 Decode: %v", i, err)
		}
		if out.Kind != in.Kind || !bytes.Equal(out.Payload, in.Payload) {
			t.Fatalf("第 %d 条不一致: kind %x/%x, payload %d/%d 字节",
				i, out.Kind, in.Kind, len(out.Payload), len(in.Payload))
		}
	}
}

// TestByteOrderExplicit：验收②——手算期望字节序列。
// 长度字段 258 的大端编码是 0x01 0x02（高位在前）；小端写法 0x02 0x01 是
// 字节序陷阱的反面教材，解码必须拒绝。
func TestByteOrderExplicit(t *testing.T) {
	payload := make([]byte, 258) // 258 = 0x0102
	payload[0], payload[1] = 0xAA, 0xBB
	raw, err := (&Frame{Kind: KindData, Payload: payload}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// 帧头逐字节断言：Magic 大端 + Version + Kind + Length 大端
	wantHead := []byte{0x4E, 0x45, 0x54, 0x43, 0x01, 0x01, 0x01, 0x02}
	if !bytes.Equal(raw[:8], wantHead) {
		t.Fatalf("帧头 = % x, want % x", raw[:8], wantHead)
	}
	if raw[8] != 0xAA || raw[9] != 0xBB {
		t.Fatalf("载荷前两字节 = % x, want aa bb", raw[8:10])
	}
	// CRC 位置与长度：尾 4 字节即 CRC32(前 8+258 字节)
	wantCRC := crc32.ChecksumIEEE(raw[:8+258])
	if binary.BigEndian.Uint32(raw[8+258:]) != wantCRC {
		t.Fatal("CRC 字段与实算不符")
	}

	// 反面教材：把长度字段按小端改写（0x02 0x01 = 513），解码必须拒绝
	le := append([]byte(nil), raw...)
	le[6], le[7] = 0x02, 0x01
	le = le[:8+258+4]
	if _, err := DecodeFrame(le); err == nil {
		t.Fatal("小端长度帧被接受——字节序陷阱成真了")
	}
}

// TestCRCCorruption：验收③——损坏帧按失败原因分诊。
func TestCRCCorruption(t *testing.T) {
	raw, err := (&Frame{Kind: KindData, Payload: []byte("payload-123")}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	cases := []struct {
		name string
		mut  func(b []byte) []byte
		want error
	}{
		{"翻转载荷 1 位", func(b []byte) []byte { b[HeadSize+2] ^= 0x01; return b }, ErrBadCRC},
		{"翻转 CRC 1 位", func(b []byte) []byte { b[len(b)-1] ^= 0x01; return b }, ErrBadCRC},
		{"截断尾部", func(b []byte) []byte { return b[:len(b)-2] }, ErrTruncated},
		{"魔数错位", func(b []byte) []byte { b[0] ^= 0xFF; return b }, ErrBadMagic},
		{"版本未知", func(b []byte) []byte { b[4] = 9; return b }, ErrBadVersion},
		{"长度超限", func(b []byte) []byte {
			binary.BigEndian.PutUint16(b[6:], 0xFFFF)
			return append(b, make([]byte, 10)...)
		}, ErrTooLarge},
	}
	for _, tc := range cases {
		b := append([]byte(nil), raw...)
		if _, err := DecodeFrame(tc.mut(b)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestBadKind：未知类型在编码与解码两端都被拒绝。
func TestBadKind(t *testing.T) {
	if _, err := (&Frame{Kind: Kind(0x7F), Payload: []byte("x")}).Encode(); !errors.Is(err, ErrBadKind) {
		t.Fatalf("Encode err = %v, want ErrBadKind", err)
	}
	// CRC 合法但类型未知——模拟"新版对端发来我们不认识的消息"，
	// 这正是 Kind 白名单存在的意义（CRC 检查放行，类型检查拦截）。
	bad := append([]byte(nil), mustEncode(t, KindData, "x")...)
	bad[5] = 0x7F
	binary.BigEndian.PutUint32(bad[len(bad)-4:], crc32.ChecksumIEEE(bad[:len(bad)-4]))
	if _, err := DecodeFrame(bad); !errors.Is(err, ErrBadKind) {
		t.Fatalf("Decode err = %v, want ErrBadKind", err)
	}
}

// TestStreamHalfFrame：流式 ReadFrame 在半包下工作——帧分两次写入，
// 每次各得一半字节，解码仍完整。
func TestStreamHalfFrame(t *testing.T) {
	raw := mustEncode(t, KindPing, "half-frame-test")
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go func() {
		a.Write(raw[:len(raw)/2]) // 前半
		time.Sleep(20 * time.Millisecond)
		a.Write(raw[len(raw)/2:]) // 后半
	}()

	frame, err := ReadFrame(bufio.NewReader(b))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if string(frame.Payload) != "half-frame-test" {
		t.Fatalf("payload = %q", frame.Payload)
	}
}

// ---------- 小工具 ----------

func mustEncode(t *testing.T, kind Kind, s string) []byte {
	t.Helper()
	raw, err := (&Frame{Kind: kind, Payload: []byte(s)}).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return raw
}
