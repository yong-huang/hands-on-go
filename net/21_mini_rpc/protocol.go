// 项目 21（🏁 收官）：迷你 RPC 框架——协议、安全与元数据的合流。
//
// 复用本系列组件（复用关系见 README 表）：
//
//	帧协议   ：[kind 1B][reqID 8B][len 4B][payload]（08 多路复用的 seq 扩展为 8B reqID）
//	mTLS     ：服务器/客户端双向证书验证（16 的 ClientAuth 镜像）
//	超时/重试：请求级元数据（超时逐请求生效；幂等请求自动重试 1 次）
//	守恒统计 ：Submitted == Succeeded + Failed（23 系列纪律的最终形态）
//	泄漏门禁 ：goleak 挂在测试出口（20 的 VerifyNone 模板）
//
// 注册表与压测在本文件内闭环：Registry 维护后端列表；main 演示单机双后端。
//
// 运行方式：go run .；断言见 go test -race（-count=50 口径见 README）。
package main

import (
	"errors"
	"fmt"
	"io"
)

// ---------- 协议帧 ----------

const (
	KindReq    byte = 0x01
	KindResp   byte = 0x02
	HeadSize        = 13 // kind 1 + reqID 8 + len 4
	MaxPayload      = 1 << 20
)

func frameEncode(kind byte, reqID uint64, payload []byte) []byte {
	b := make([]byte, HeadSize+len(payload))
	b[0] = kind
	for i := 0; i < 8; i++ {
		b[1+i] = byte(reqID >> (56 - 8*i))
	}
	putU32(b[9:], uint32(len(payload)))
	copy(b[HeadSize:], payload)
	return b
}

// binaryBE：避开 math/binary 别名噪音的最小大端包装。
func binaryBE(buf []byte, v uint64) {
	for i := 0; i < 8; i++ {
		buf[i] = byte(v >> (56 - 8*i))
	}
}

func putU32(buf []byte, v uint32) {
	for i := 0; i < 4; i++ {
		buf[i] = byte(v >> (24 - 8*i))
	}
}

// readFrame：从流里读一帧（半包由 ReadFull 自然处理）。
func readFrame(r io.Reader) (kind byte, reqID uint64, payload []byte, err error) {
	head := make([]byte, HeadSize)
	if _, err = io.ReadFull(r, head); err != nil {
		return
	}
	kind = head[0]
	reqID = beUint64(head[1:9])
	n := beUint32(head[9:13])
	if n > MaxPayload {
		err = fmt.Errorf("payload too large: %d", n)
		return
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	return
}

func beUint64(b []byte) uint64 {
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ---------- 协议错误 ----------

var (
	ErrConnFail  = errors.New("connection failure")
	ErrTimeout   = errors.New("rpc timeout")
	ErrNoBackend = errors.New("no backend available")
)

// decodeFrame：从完整帧字节中提取 kind、reqID 与载荷。
func decodeFrame(b []byte) (byte, uint64, []byte, error) {
	if len(b) < HeadSize {
		return 0, 0, nil, io.ErrShortBuffer
	}
	return b[0], beUint64(b[1:9]), b[13:], nil
}
