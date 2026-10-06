// 项目 5：二进制协议设计——魔数、版本、类型、长度前缀与 CRC32 一应俱全。
//
// 帧格式（全部大端，总长 = 8 + n + 4）：
//
//	偏移  大小  字段        说明
//	0     4B   Magic      魔数 0x4E455443（"NETC"）：错位/串线帧的第一道闸
//	4     1B   Version    协议版本 = 1：升级共存与拒绝旧实现
//	5     1B   Kind       消息类型：0x01 DATA / 0x02 PING / 0x03 PONG
//	6     2B   Length     载荷长度 n（大端）——项目 2 的长度前缀正式转正
//	8     nB   Payload    载荷
//	8+n   4B   CRC32      校验和，覆盖 Magic..Payload 全部字节
//
// 解码按"先便宜后昂贵"排序：结构检查 → 魔数 → 版本 → 长度上限 → CRC → 类型。
// 每一步失败都返回独立错误值，fuzz 与诊断都受益（项目 6 的靶子在这里）。
//
// 运行方式：go run .（127.0.0.1:19551，含 hexdump 讲解与 100 帧往返）。
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net"
	"time"
)

const (
	Magic      uint32 = 0x4E455443 // "NETC"
	Version    byte   = 1
	HeadSize          = 8 // Magic4 + Version1 + Kind1 + Length2
	CRCSize           = 4 // CRC32 尾
	MaxPayload        = 4096
)

type Kind byte

const (
	KindData Kind = 0x01
	KindPing Kind = 0x02
	KindPong Kind = 0x03
)

// 协议错误族：每类失败一个哨兵，调用方用 errors.Is 分诊。
var (
	ErrTruncated  = errors.New("frame truncated")
	ErrBadMagic   = errors.New("bad magic")
	ErrBadVersion = errors.New("bad version")
	ErrTooLarge   = errors.New("payload too large")
	ErrBadCRC     = errors.New("crc mismatch")
	ErrBadKind    = errors.New("unknown kind")
)

// Frame：一帧完整消息。
type Frame struct {
	Kind    Kind
	Payload []byte
}

// Encode：按帧格式编码，返回完整字节序列（Header + Payload + CRC32）。
func (f *Frame) Encode() ([]byte, error) {
	if len(f.Payload) > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooLarge, len(f.Payload), MaxPayload)
	}
	switch f.Kind {
	case KindData, KindPing, KindPong:
	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrBadKind, f.Kind)
	}
	total := HeadSize + len(f.Payload) + CRCSize
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:], Magic)
	buf[4] = Version
	buf[5] = byte(f.Kind)
	binary.BigEndian.PutUint16(buf[6:], uint16(len(f.Payload)))
	copy(buf[HeadSize:], f.Payload)
	binary.BigEndian.PutUint32(buf[HeadSize+len(f.Payload):],
		crc32.ChecksumIEEE(buf[:HeadSize+len(f.Payload)]))
	return buf, nil
}

// DecodeFrame：从完整字节切片解码（测试与 fuzz 的主入口）。
// 检查顺序刻意从便宜到昂贵：结构 → 魔数 → 版本 → 长度 → CRC → 类型。
func DecodeFrame(b []byte) (*Frame, error) {
	if len(b) < HeadSize+CRCSize {
		return nil, fmt.Errorf("%w: %d 字节", ErrTruncated, len(b))
	}
	if binary.BigEndian.Uint32(b[0:]) != Magic {
		return nil, ErrBadMagic
	}
	if b[4] != Version {
		return nil, fmt.Errorf("%w: %d", ErrBadVersion, b[4])
	}
	n := int(binary.BigEndian.Uint16(b[6:]))
	if n > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooLarge, n, MaxPayload)
	}
	if len(b) < HeadSize+n+CRCSize {
		return nil, fmt.Errorf("%w: 需 %d 字节，只有 %d", ErrTruncated, HeadSize+n+CRCSize, len(b))
	}
	want := binary.BigEndian.Uint32(b[HeadSize+n:])
	got := crc32.ChecksumIEEE(b[:HeadSize+n])
	if want != got {
		return nil, fmt.Errorf("%w: 帧内 0x%08x ≠ 实算 0x%08x", ErrBadCRC, want, got)
	}
	kind := b[5]
	switch Kind(kind) {
	case KindData, KindPing, KindPong:
	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrBadKind, kind)
	}
	payload := make([]byte, n)
	copy(payload, b[HeadSize:HeadSize+n])
	return &Frame{Kind: Kind(kind), Payload: payload}, nil
}

// hexdump：逐字段标注一帧的十六进制布局（教学主视图）。
func hexdump(raw []byte) {
	fields := []struct {
		off, size int
		name      string
	}{
		{0, 4, "Magic(NETC)"}, {4, 1, "Version"}, {5, 1, "Kind"},
		{6, 2, "Length"}, {HeadSize, len(raw) - HeadSize - CRCSize, "Payload"},
		{len(raw) - CRCSize, CRCSize, "CRC32"},
	}
	for _, fl := range fields {
		chunk := raw[fl.off : fl.off+fl.size]
		hex := ""
		for _, b := range chunk {
			hex += fmt.Sprintf("%02x ", b)
		}
		note := ""
		if fl.name == "Payload" {
			note = fmt.Sprintf("= %q", string(chunk))
		}
		fmt.Printf("  [%2d..%-2d] %-14s %s%s\n", fl.off, fl.off+fl.size-1, fl.name, hex, note)
	}
	fmt.Printf("  总长 %d 字节\n", len(raw))
}

// bufioReader / bytesReader：语义别名，让调用处意图更清楚。
func bufioReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }
func bytesReader(b []byte) *bytes.Reader    { return bytes.NewReader(b) }

// ReadFrame：流式解码——先读 8B 帧头，按长度读载荷与 CRC（真 TCP 用它）。
func ReadFrame(r io.Reader) (*Frame, error) {
	head := make([]byte, HeadSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(head[0:]) != Magic {
		return nil, ErrBadMagic
	}
	if head[4] != Version {
		return nil, fmt.Errorf("%w: %d", ErrBadVersion, head[4])
	}
	n := int(binary.BigEndian.Uint16(head[6:]))
	if n > MaxPayload {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooLarge, n, MaxPayload)
	}
	full := make([]byte, HeadSize+n+CRCSize)
	copy(full, head)
	if _, err := io.ReadFull(r, full[HeadSize:]); err != nil {
		return nil, err
	}
	return DecodeFrame(full)
}

func main() {
	fmt.Println("== 第 1 步：一帧的字节长什么样（hexdump 逐字段讲解）==")
	f := &Frame{Kind: KindData, Payload: []byte("hello, NETC")}
	raw, err := f.Encode()
	if err != nil {
		fmt.Println("encode:", err)
		return
	}
	hexdump(raw)

	fmt.Println()
	fmt.Println("== 第 2 步：100 帧往返（DATA×50 + PING→PONG + 1 帧损坏注入）==")
	ln, err := net.Listen("tcp", "127.0.0.1:19551")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()

	// 服务端：解码计数直方图；收到 PING 回 PONG；坏帧拒绝但不断流。
	serverCounts := map[string]int{}
	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		r := bufioReader(conn)
		for {
			frame, err := ReadFrame(r)
			if err != nil {
				switch {
				case errors.Is(err, io.EOF):
					serverErr <- nil
					return
				case errors.Is(err, ErrBadCRC), errors.Is(err, ErrBadKind):
					// 帧头有效 ⇒ 帧长已知且已消费 ⇒ 可以在原流上重新同步
					serverCounts["BAD(被CRC/类型拒绝)"]++
					continue
				default:
					// 魔数/版本/长度坏了 ⇒ 边界不可知，只能断流
					serverErr <- err
					return
				}
			}
			switch Kind(frame.Kind) {
			case KindPing:
				serverCounts["PING"]++
				pong := &Frame{Kind: KindPong, Payload: []byte("pong")}
				if raw, err := pong.Encode(); err == nil {
					conn.Write(raw)
				}
			default:
				serverCounts["DATA"]++
			}
		}
	}()

	c, err := net.Dial("tcp", "127.0.0.1:19551")
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer c.Close()
	rnd := rand.New(rand.NewSource(7))

	send := 0
	for i := 0; i < 50; i++ { // 50 条随机 DATA
		payload := make([]byte, 1+rnd.Intn(64))
		rnd.Read(payload)
		raw, err := (&Frame{Kind: KindData, Payload: payload}).Encode()
		if err != nil {
			fmt.Println("encode:", err)
			return
		}
		c.Write(raw)
		send++
	}
	ping, _ := (&Frame{Kind: KindPing, Payload: []byte("ping")}).Encode()
	c.Write(ping)
	time.Sleep(100 * time.Millisecond)

	// 损坏注入：合法 DATA 帧翻转 1 个载荷位（bit flip），CRC 必须拦下它
	bad, _ := (&Frame{Kind: KindData, Payload: []byte("corrupt-me")}).Encode()
	bad[HeadSize+3] ^= 0x10
	c.Write(bad)
	good3 := 0
	for i := 0; i < 3; i++ { // 坏帧之后的好帧：长度前缀保证重新同步
		payload := []byte(fmt.Sprintf("after-bad-%d", i))
		raw, _ := (&Frame{Kind: KindData, Payload: payload}).Encode()
		c.Write(raw)
		good3++
	}
	c.(*net.TCPConn).CloseWrite() // 半关闭：服务端读到 EOF 收场

	// 客户端收 PONG
	buf := make([]byte, 256)
	c.SetReadDeadline(time.Now().Add(time.Second))
	if n, err := c.Read(buf); err == nil {
		if pong, err := ReadFrame(bytesReader(buf[:n])); err == nil && Kind(pong.Kind) == KindPong {
			serverCounts["PONG(客户端收到)"]++
		}
	}

	if err := <-serverErr; err != nil {
		fmt.Println("服务端解码失败:", err)
		return
	}
	fmt.Printf("服务端直方图：%v\n", serverCounts)
	fmt.Printf("客户端发送：DATA %d + PING 1 + 坏帧 1 + 坏帧后好帧 %d\n", send, good3)
	fmt.Println("⇒ CRC 把坏帧拦在应用之外，长度前缀让后续好帧无缝重新同步。")
}
