// 项目 2：粘包拆包——TCP 是字节流，不是消息流。
//
// 发送方写 10 次，接收方可能 1 次读完，也可能 37 次读完；Read 的边界
// 由内核缓冲与调度决定，与 Write 的边界无关。本程序做三件事：
//
//	第 1 步：逐字节发 10 条消息，把服务端每一次 Read 的长度原样打印——
//	        亲眼看到"写 10 次 ≠ 读 10 次"（半包/粘包的真实形态）
//	第 2 步：三种帧解码器（定长/分隔符/长度前缀）各自从同样的逐字节流里
//	        完整还原 10/10 条消息
//	第 3 步：一次 Write 灌 100 条消息（多包场景），长度前缀解码 100/100
//
// 解码器只依赖 io.Reader（bufio.Reader），与 TCP 无关——所以测试可以用
// iotest.OneByteReader 在内存里确定性复现半包。
//
// 运行方式：go run .（本机 127.0.0.1:19521）。
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

const addr = "127.0.0.1:19521"

// ---------- 三种帧解码器 ----------

// ErrFrameTooLarge：帧超过上限——长度字段不可信时的自保护（项目 6 会 fuzz 它）。
var ErrFrameTooLarge = errors.New("frame too large")

// Decoder：从流里取出一条完整消息。返回 io.EOF 表示流已尽。
type Decoder interface {
	Next(r *bufio.Reader) ([]byte, error)
}

// FixedDecoder：定长帧——每帧恰好 size 字节，短消息右侧补零。
type FixedDecoder struct{ Size int }

func (d FixedDecoder) Next(r *bufio.Reader) ([]byte, error) {
	buf := make([]byte, d.Size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err // 不足一帧时 ReadFull 会把已读部分填好再返回 err
	}
	return buf, nil
}

// DelimDecoder：分隔符帧——读到 delim 为止；超长报 ErrFrameTooLarge。
type DelimDecoder struct {
	Delim byte
	Max   int
}

func (d DelimDecoder) Next(r *bufio.Reader) ([]byte, error) {
	var msg []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == d.Delim {
			return msg, nil
		}
		if len(msg) >= d.Max {
			return nil, ErrFrameTooLarge
		}
		msg = append(msg, b)
	}
}

// LengthPrefixDecoder：长度前缀帧——帧头 2 字节大端长度 + 载荷（主流方案）。
type LengthPrefixDecoder struct{ Max int }

func (d LengthPrefixDecoder) Next(r *bufio.Reader) ([]byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(head[:])
	if int(n) > d.Max {
		return nil, ErrFrameTooLarge
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// ---------- 编码器（demo 与测试共用） ----------

func fixedEncode(msg string, size int) []byte {
	b := make([]byte, size)
	copy(b, msg)
	return b
}
func delimEncode(msg string) []byte { return append([]byte(msg), '\n') }
func lpEncode(msg string) []byte {
	b := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(b, uint16(len(msg)))
	copy(b[2:], msg)
	return b
}

// ---------- 演示 ----------

func main() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()
	fmt.Printf("服务端就绪：%s\n\n", addr)

	const msgs = 10
	fmt.Println("== 第 1 步：半包实证——逐字节发 10 条消息，Read 边界完全乱套 ==")
	clientHalf := func(write func(c net.Conn)) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			fmt.Println("dial:", err)
			return
		}
		defer c.Close()
		write(c)
		// 半关闭：字节全发完了，让服务端读到 EOF 收场
		c.(*net.TCPConn).CloseWrite()
	}
	go clientHalf(func(c net.Conn) {
		for _, m := range []string{"alpha", "beta", "gamma", "delta", "epsilon",
			"zeta", "eta", "theta", "iota", "kappa"} {
			for i := 0; i < len(m); i++ {
				c.Write([]byte{m[i]}) // 一次只写 1 字节！
			}
		}
	})
	raw, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	buf := make([]byte, 64)
	total := 0
	reads := 0
	for {
		n, err := raw.Read(buf)
		reads++
		total += n
		if reads <= 8 || err != nil {
			fmt.Printf("  第 %2d 次 Read: %2d 字节 (err=%v)\n", reads, n, err)
		}
		if err != nil {
			break
		}
	}
	written := 0
	for _, m := range []string{"alpha", "beta", "gamma", "delta", "epsilon",
		"zeta", "eta", "theta", "iota", "kappa"} {
		written += len(m)
	}
	fmt.Printf("  ⇒ 写了 %d 次，却读了 %d 次、共 %d 字节——字节流没有消息边界。\n\n",
		written, reads, total)

	fmt.Println("== 第 2 步：三种解码器，同一条逐字节流，各自还原 10/10 ==")
	decoders := []struct {
		name string
		dec  Decoder
		enc  func(string) []byte
	}{
		{"定长(8B)", FixedDecoder{Size: 8}, func(s string) []byte { return fixedEncode(s, 8) }},
		{"分隔符(\\n)", DelimDecoder{Delim: '\n', Max: 64}, delimEncode},
		{"长度前缀(2B)", LengthPrefixDecoder{Max: 64}, lpEncode},
	}
	payloads := []string{"alpha", "beta", "gamma", "delta", "epsilon",
		"zeta", "eta", "theta", "iota", "kappa"}
	for _, dc := range decoders {
		go func(dec Decoder, enc func(string) []byte) {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				fmt.Println("dial:", err)
				return
			}
			defer c.Close()
			for _, m := range payloads {
				frame := enc(m)
				for i := 0; i < len(frame); i++ {
					c.Write([]byte{frame[i]}) // 依旧逐字节 write，制造半包
				}
			}
			c.(*net.TCPConn).CloseWrite()
		}(dc.dec, dc.enc)
		c, err := ln.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			return
		}
		r := bufio.NewReader(c)
		got := 0
		var first, last string
		for {
			msg, err := dc.dec.Next(r)
			if err != nil {
				break
			}
			got++
			if got == 1 {
				first = trimZero(string(msg))
			}
			last = trimZero(string(msg))
		}
		c.Close()
		fmt.Printf("  %-12s 还原 %d/%d 条  首条=%q 末条=%q\n", dc.name, got, msgs, first, last)
	}

	fmt.Println()
	fmt.Println("== 第 3 步：多包实证——一次 Write 灌 100 条，解码 100/100 ==")
	go func() {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			fmt.Println("dial:", err)
			return
		}
		defer c.Close()
		big := []byte{}
		for i := 0; i < 100; i++ {
			big = append(big, lpEncode(fmt.Sprintf("m%03d", i))...)
		}
		c.Write(big) // 一整块下去
		c.(*net.TCPConn).CloseWrite()
	}()
	c, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	r := bufio.NewReader(c)
	dec := LengthPrefixDecoder{Max: 64}
	got := 0
	first, last := "", ""
	for {
		msg, err := dec.Next(r)
		if err != nil {
			break
		}
		if got == 0 {
			first = string(msg)
		}
		last = string(msg)
		got++
	}
	c.Close()
	fmt.Printf("  1 次 Write → 长度前缀解码 %d/100 条  首条=%q 末条=%q\n", got, first, last)
	fmt.Println()
	fmt.Println("基准：go test -bench . -run '^$'（结果见 bench.txt 与 README）")
}

// trimZero：定长帧的右侧补零去掉，便于打印。
func trimZero(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return s[:i]
		}
	}
	return s
}
