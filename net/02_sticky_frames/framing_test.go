package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"testing/iotest"
)

// 消息集：长度 3~7 不等，覆盖短帧与较长帧。
var payloads = []string{"alpha", "beta", "gamma", "delta", "epsilon",
	"zeta", "eta", "theta", "iota", "kappa"}

var decoders = []struct {
	name string
	dec  Decoder
	enc  func(string) []byte
}{
	{"定长(8B)", FixedDecoder{Size: 8}, func(s string) []byte { return fixedEncode(s, 8) }},
	{"分隔符(\\n)", DelimDecoder{Delim: '\n', Max: 64}, delimEncode},
	{"长度前缀(2B)", LengthPrefixDecoder{Max: 64}, lpEncode},
}

// streamBytes：把全部编码帧拼成一个字节流。
func streamBytes(enc func(string) []byte) []byte {
	var b []byte
	for _, m := range payloads {
		b = append(b, enc(m)...)
	}
	return b
}

// decodeAll：从 r 里用 dec 解到 EOF，返回全部消息。
func decodeAll(t *testing.T, dec Decoder, r io.Reader) []string {
	t.Helper()
	br := bufio.NewReader(r)
	var out []string
	for {
		msg, err := dec.Next(br)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("解码出错: %v", err)
		}
		out = append(out, trimZero(string(msg)))
	}
}

// TestHalfPacketOneByte：验收①——逐字节投递（iotest.OneByteReader 确定性复现
// 半包），三种解码器各自还原 10/10 且内容与原始消息一致。
func TestHalfPacketOneByte(t *testing.T) {
	for _, dc := range decoders {
		raw := streamBytes(dc.enc)
		got := decodeAll(t, dc.dec, iotest.OneByteReader(bytes.NewReader(raw)))
		if len(got) != len(payloads) {
			t.Fatalf("%s: 还原 %d/%d 条", dc.name, len(got), len(payloads))
		}
		for i := range payloads {
			if got[i] != payloads[i] {
				t.Fatalf("%s: 第 %d 条 = %q, want %q", dc.name, i, got[i], payloads[i])
			}
		}
	}
}

// TestHalfPacketRealTCP：验收①的真实网络版——客户端逐字节 write，
// 服务端经真实 TCP 收流后解码，三种解码器同样 10/10。
func TestHalfPacketRealTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:19522")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	for _, dc := range decoders {
		go func(enc func(string) []byte) {
			c, err := net.Dial("tcp", "127.0.0.1:19522")
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			defer c.Close()
			for _, b := range streamBytes(enc) {
				c.Write([]byte{b}) // 一次 1 字节
			}
			c.(*net.TCPConn).CloseWrite()
		}(dc.enc)

		c, err := ln.Accept()
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		got := decodeAll(t, dc.dec, c)
		c.Close()
		if len(got) != len(payloads) || strings.Join(got, ",") != strings.Join(payloads, ",") {
			t.Fatalf("%s: 真实 TCP 下还原异常：%v", dc.name, got)
		}
	}
}

// TestMultiPacketOneWrite：验收②——100 帧一次 Write（多包场景），
// 长度前缀解码 100/100 且顺序不变。
func TestMultiPacketOneWrite(t *testing.T) {
	var big []byte
	want := make([]string, 100)
	for i := 0; i < 100; i++ {
		want[i] = fmt.Sprintf("m%03d", i)
		big = append(big, lpEncode(want[i])...)
	}
	got := decodeAll(t, LengthPrefixDecoder{Max: 64}, bytes.NewReader(big))
	if len(got) != 100 {
		t.Fatalf("还原 %d/100 条", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条 = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestFrameTooLarge：长度字段不可信时的自保护——超限帧报 ErrFrameTooLarge。
func TestFrameTooLarge(t *testing.T) {
	raw := []byte{0xFF, 0xFF, 'x'} // 声明 65535 字节，实际只有 1 字节
	br := bufio.NewReader(bytes.NewReader(raw))
	_, err := (LengthPrefixDecoder{Max: 64}).Next(br)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

// ---------- 基准：三种解码器在同一流量下的开销 ----------

// benchStream：1000 条 61 字符消息按三种编码各生成一份流量（62~64B/帧，量级一致）。
func benchStream(enc func(string) []byte) []byte {
	var b []byte
	for i := 0; i < 1000; i++ {
		b = append(b, enc(strings.Repeat("x", 61))...)
	}
	return b
}

func benchDecode(b *testing.B, dec Decoder, stream []byte) {
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	br := bufio.NewReader(bytes.NewReader(stream))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for {
			_, err := dec.Next(br)
			if errors.Is(err, io.EOF) {
				br.Reset(bytes.NewReader(stream))
				continue
			}
			if err != nil {
				b.Fatal(err)
			}
			break
		}
	}
}

func BenchmarkFixed(b *testing.B) {
	benchDecode(b, FixedDecoder{Size: 64}, benchStream(func(s string) []byte { return fixedEncode(s, 64) }))
}

func BenchmarkDelimiter(b *testing.B) {
	benchDecode(b, DelimDecoder{Delim: '\n', Max: 128}, benchStream(delimEncode))
}

func BenchmarkLengthPrefix(b *testing.B) {
	benchDecode(b, LengthPrefixDecoder{Max: 128}, benchStream(lpEncode))
}
