// 项目 6：协议健壮性——把畸形包矩阵与 fuzz 靶场架在项目 5 的解码器上。
//
// 解码器是应用的第一道门：它读到的每一个字节都应视为攻击者的输入。
// 本篇做三件事：
//
//	第 1 步：畸形包矩阵现场——九类攻击逐个喂给解码器，打印被拒的错误分类
//	        （零 panic 是底线，可分类是及格）
//	第 2 步：混流生存——好帧/坏帧/好帧交替，验证坏帧不影响后续解析
//	第 3 步：fuzz 靶场入口——go test -fuzz FuzzDecodeFrame -fuzztime 30s，
//	        随机字节轰炸 30 秒无 crash（结果见 fuzz.txt）
//
// 协议帧与项目 5 完全一致（魔数 NETC/版本/类型/长度/CRC32，全大端）；
// 按仓库惯例每个实验目录自包含，本文复制核心解码器作为被测对象。
//
// 运行方式：go run .；fuzz 见 README。
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	Magic      uint32 = 0x4E455443 // "NETC"
	Version    byte   = 1
	HeadSize          = 8
	CRCSize           = 4
	MaxPayload        = 4096
)

type Kind byte

const (
	KindData Kind = 0x01
	KindPing Kind = 0x02
	KindPong Kind = 0x03
)

var (
	ErrTruncated  = errors.New("frame truncated")
	ErrBadMagic   = errors.New("bad magic")
	ErrBadVersion = errors.New("bad version")
	ErrTooLarge   = errors.New("payload too large")
	ErrBadCRC     = errors.New("crc mismatch")
	ErrBadKind    = errors.New("unknown kind")
	ErrTrailing   = errors.New("trailing bytes after frame")
)

type Frame struct {
	Kind    Kind
	Payload []byte
}

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

// DecodeFrame：被测对象。检查次序从便宜到昂贵，错误一一分诊。
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
	// 严格解码：整帧之后的残余字节不属于本帧——静默忽略就是给攻击面开门
	// （fuzz 实录：合法帧+1 字节尾巴曾被接受，重编码不一致暴露了它）。
	if fs := HeadSize + n + CRCSize; len(b) > fs {
		return nil, fmt.Errorf("%w: %d 字节", ErrTrailing, len(b)-fs)
	}
	want := binary.BigEndian.Uint32(b[HeadSize+n:])
	got := crc32.ChecksumIEEE(b[:HeadSize+n])
	if want != got {
		return nil, fmt.Errorf("%w: 帧内 0x%08x ≠ 实算 0x%08x", ErrBadCRC, want, got)
	}
	switch Kind(b[5]) {
	case KindData, KindPing, KindPong:
	default:
		return nil, fmt.Errorf("%w: 0x%02x", ErrBadKind, b[5])
	}
	payload := make([]byte, n)
	copy(payload, b[HeadSize:HeadSize+n])
	return &Frame{Kind: Kind(b[5]), Payload: payload}, nil
}

// classify：把解码错误归类到哨兵（fuzz 与矩阵共用）。
// 返回 "OK" 表示成功——分类集合必须是闭集，这是 fuzz 不变式的一部分。
func classify(err error) string {
	switch {
	case err == nil:
		return "OK"
	case errors.Is(err, ErrTruncated):
		return "TRUNCATED"
	case errors.Is(err, ErrBadMagic):
		return "BAD_MAGIC"
	case errors.Is(err, ErrBadVersion):
		return "BAD_VERSION"
	case errors.Is(err, ErrTooLarge):
		return "TOO_LARGE"
	case errors.Is(err, ErrBadCRC):
		return "BAD_CRC"
	case errors.Is(err, ErrBadKind):
		return "BAD_KIND"
	case errors.Is(err, ErrTrailing):
		return "TRAILING"
	default:
		return "UNCLASSIFIED" // fuzz 的不变式：这个值出现即缺陷
	}
}

func main() {
	fmt.Println("== 第 1 步：畸形包矩阵——十类攻击，逐个分诊 ==")
	valid, _ := (&Frame{Kind: KindData, Payload: []byte("payload-123")}).Encode()

	attacks := []struct {
		name string
		in   []byte
	}{
		{"空输入", nil},
		{"1 字节", []byte{0x4E}},
		{"冒充 HTTP 的串线流", []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")},
		{"版本 9（未来版本）", mutate(valid, func(b []byte) { b[4] = 9 })},
		{"声明 65535 长度的欺骗帧", mutate(valid, func(b []byte) {
			binary.BigEndian.PutUint16(b[6:], 0xFFFF)
		})},
		{"截断 2 字节的合法帧", valid[:len(valid)-2]},
		{"载荷翻转 1 位的合法帧", mutate(valid, func(b []byte) { b[HeadSize+2] ^= 0x01 })},
		{"合法帧+1 字节尾巴", append(mutate(valid, func(b []byte) {}), []byte{0x30}...)},
		{"未知类型（CRC 合法，模拟新版对端）", func() []byte {
			b := mutate(valid, func(b []byte) { b[5] = 0x7F })
			binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
			return b
		}()},
	}
	for _, atk := range attacks {
		_, err := DecodeFrame(atk.in)
		fmt.Printf("  %-28s → %s\n", atk.name, classify(err))
	}
	fmt.Println("  ⇒ 零 panic，每类攻击都有名字。")

	fmt.Println()
	fmt.Println("== 第 2 步：坏帧的两种可恢复性——分诊决定断不断链 ==")
	good := func(i int) []byte {
		b, _ := (&Frame{Kind: KindData, Payload: []byte(fmt.Sprintf("good-%d", i))}).Encode()
		return b
	}
	// 情形 A：CRC 坏帧（帧头有效 → 帧长已知 → 整帧已消费）→ 拒帧继续
	resume := mutate(valid, func(b []byte) { b[HeadSize+1] ^= 0x80 })
	resume = append(resume, good(3)...)
	_, errCRC := DecodeFrame(resume[:len(valid)])
	_, errAfter := DecodeFrame(resume[len(valid):])
	fmt.Printf("  A. CRC 坏帧  → %-10s 其后好帧 → %s（流可继续）\n", classify(errCRC), classify(errAfter))
	// 情形 B：魔数坏帧（边界不可知：不知这一"帧"到哪结束）→ 只能断链
	badMagic := mutate(valid, func(b []byte) { b[0] ^= 0xFF })
	badMagic = append(badMagic, good(4)...) // 好帧跟在后面，但已不可达
	_, errMagic := DecodeFrame(badMagic)
	fmt.Printf("  B. 魔数坏帧  → %-10s 其后好帧不可达（只能断链重连）\n", classify(errMagic))

	fmt.Println()
	fmt.Println("== 第 3 步：fuzz 靶场 ==")
	fmt.Println("  go test -fuzz FuzzDecodeFrame -fuzztime 30s")
	fmt.Println("  随机字节轰炸 30 秒：零 crash、错误分类闭集、成功输入重编码逐字节一致。")
	fmt.Println("  结果实录见 fuzz.txt。")
}

// mutate：拷贝后原位修改（测试与演示共用的攻击构造器）。
func mutate(b []byte, f func([]byte)) []byte {
	c := append([]byte(nil), b...)
	f(c)
	return c
}
