// 项目 20：手写 WebSocket（RFC6455）——握手、帧编解码、心跳与关闭握手。
//
// 不用任何第三方 ws 库，纯标准库实现服务端核心三件：
//
//	握手（HTTP Upgrade）：Sec-WebSocket-Accept = base64(SHA1(key + GUID))
//	帧编解码            ：FIN/opcode/mask/len(7|16|64 位)，客户端帧必须掩码
//	心跳与关闭          ：Ping/Pong 保活；Close 帧双向确认后断开
//
// 验收三测（go test）：
//
//	① Accept key 对 RFC 示例值断言
//	② 掩码帧 round-trip 1000 条随机 payload
//	③ 心跳超时未 Pong 断连 + Close 握手双方都收到 Close 帧
//
// 运行方式：go run .（127.0.0.1:19597，等价于测试的完整流程演示）。
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const addr = "127.0.0.1:19597"

// wsGUID：RFC6455 规定的魔术字符串（固定值，任何实现都必须用它）。
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// AcceptKey：计算 Sec-WebSocket-Accept 响应值。
func AcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Opcodes（RFC6455 §5.2）。
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// Frame：解码后的 WebSocket 帧。
type Frame struct {
	FIN     bool
	Opcode  byte
	Payload []byte
}

// maskPayload：异或掩码（掩码密钥 4 字节循环异或载荷）——客户端→服务端帧强制。
func maskPayload(payload []byte, key [4]byte) []byte {
	out := make([]byte, len(payload))
	for i := range payload {
		out[i] = payload[i] ^ key[i%4]
	}
	return out
}

// EncodeFrame：服务端→客户端帧不掩码（RFC6455 仅强制客户端帧掩码）。
// 长度编码三档：≤125 直接放 7 位；≤65535 用 16 位扩展；更大用 64 位扩展。
func EncodeFrame(opcode byte, payload []byte) []byte {
	var head []byte
	head = append(head, 0x80|opcode) // FIN=1 + opcode
	n := len(payload)
	switch {
	case n <= 125:
		head = append(head, byte(n))
	case n <= 65535:
		head = append(head, 126, 0, 0)
		binary.BigEndian.PutUint16(head[len(head)-2:], uint16(n))
	default:
		head = append(head, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(head[len(head)-8:], uint64(n))
	}
	return append(head, payload...)
}

// EncodeClientFrame：客户端帧——额外带 4 字节掩码密钥并掩码载荷。
func EncodeClientFrame(opcode byte, payload []byte, key [4]byte) []byte {
	// 开发实录：先 EncodeFrame（含载荷）再追加掩码载荷会把载荷写两遍，
	// 逐字节对拍当场抓出——帧头必须单独编码，载荷只出现一次。
	head := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n <= 125:
		head = append(head, byte(n)|0x80) // 长度 + mask 位
	case n <= 65535:
		head = append(head, byte(126)|0x80, 0, 0)
		binary.BigEndian.PutUint16(head[len(head)-2:], uint16(n))
	default:
		head = append(head, byte(127)|0x80, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(head[len(head)-8:], uint64(n))
	}
	frame := append(head, key[:]...)
	frame = append(frame, maskPayload(payload, key)...)
	return frame
}

// ErrConnClosed：对端正常关闭（Close 帧已处理）。
var ErrConnClosed = errors.New("connection closed by peer")

// ReadFrame：从流里读一帧（含掩码解码；服务端只收客户端帧）。
// 分片（FIN=0 + Continuation）由上层聚合，本层只处理单帧。
func ReadFrame(r *bufio.Reader) (*Frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, err
	}
	f := &Frame{FIN: head[0]&0x80 != 0, Opcode: head[0] & 0x0f}
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if masked {
		payload = maskPayload(payload, maskKey)
	}
	f.Payload = payload
	return f, nil
}

// handshake：读 HTTP Upgrade 请求，校验，写 101 响应。返回接受到的 key。
func handshake(conn net.Conn) (string, error) {
	req, err := httpReadRequest(bufio.NewReader(conn))
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(req.Method, "GET") ||
		!strings.Contains(strings.ToLower(req.Get("Upgrade")), "websocket") {
		return "", fmt.Errorf("不是 WebSocket 升级请求")
	}
	key := req.Get("Sec-Websocket-Key")
	if key == "" {
		return "", fmt.Errorf("缺少 Sec-WebSocket-Key")
	}
	accept := AcceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		return "", err
	}
	return key, nil
}

// request：握手请求所需的最小 HTTP 头集合。
type request struct {
	Method string
	Header map[string]string
}

// Get：不区分大小写取头（HTTP 头名不区分大小写）。
func (r *request) Get(name string) string {
	return r.Header[strings.ToLower(name)]
}

// httpReadRequest：逐行读 HTTP 请求头（握手只需 Method 与少数 Header）。
func httpReadRequest(r *bufio.Reader) (*request, error) {
	req := &request{Header: map[string]string{}}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return nil, fmt.Errorf("bad request line: %q", line)
	}
	req.Method = parts[0]
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return req, nil // 空行 = 头结束
		}
		if i := strings.Index(line, ":"); i > 0 {
			req.Header[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
		}
	}
}

// connWrapper：带缓冲读的 net.Conn（握手读掉的缓冲字节不能丢）。
type connWrapper struct {
	net.Conn
	reader *bufio.Reader
}

func (w connWrapper) Read(p []byte) (int, error) { return w.reader.Read(p) }

// serverEcho：完成握手后进入消息循环——Ping 回 Pong，Text 回显，Close 确认。
// pongTimeout：客户端须在 pongTimeout 内有活动（Pong 或数据），否则断连。
func serverEcho(conn net.Conn, pongTimeout time.Duration) {
	br := bufio.NewReader(conn)
	for {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		f, err := ReadFrame(br)
		if err != nil {
			return
		}
		switch f.Opcode {
		case OpPing:
			conn.Write(EncodeFrame(OpPong, f.Payload))
		case OpClose:
			// 关闭握手：回一个 Close 帧后断开
			conn.Write(EncodeFrame(OpClose, f.Payload))
			return
		case OpText, OpBinary:
			conn.Write(EncodeFrame(f.Opcode, f.Payload)) // echo
		case OpPong:
			// 保活确认，无需处理
		}
	}
}

// 客户端拨号（测试用）：TCP + Upgrade 握手，返回升级后的连接。
func dialWS(addr, key string) (net.Conn, error) {
	tcp, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	req := "GET /ws HTTP/1.1\r\nHost: " + addr + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := tcp.Write([]byte(req)); err != nil {
		tcp.Close()
		return nil, err
	}
	// 读 101 响应头
	br := bufio.NewReader(tcp)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			tcp.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	return connWrapper{Conn: tcp, reader: br}, nil
}

// 便于测试与演示的包装。
func main() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	fmt.Printf("WebSocket 服务端就绪：%s（心跳超时 2s，echo 模式）\n", addr)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			key, err := handshake(c)
			if err != nil {
				fmt.Println("  握手失败:", err)
				c.Close()
				return
			}
			fmt.Printf("  握手成功：Accept-Key=%s\n", key)
			serverEcho(c, 2*time.Second)
			fmt.Println("  连接关闭")
		}(c)
	}
}
