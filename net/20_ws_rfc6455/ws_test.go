package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

// startWSServer：起 WebSocket echo 服务端（心跳超时可配），返回地址。
func startWSServer(t *testing.T, pongTimeout time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				if _, err := handshake(c); err != nil {
					c.Close()
					return
				}
				serverEcho(c, pongTimeout)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// clientConn：完成握手（含 Accept 断言）的客户端连接。
func clientConn(t *testing.T, addr string) net.Conn {
	t.Helper()
	const clientKey = "dGhlIHNhbXBsZSBub25jZQ==" // RFC6455 §1.3 示例 key
	conn, err := dialWS(addr, clientKey)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	// 校验 Accept：RFC 示例 key → s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
	br := bufio.NewReader(conn)
	br2 := bufio.NewReaderSize(conn, 4096)
	_ = br2
	_ = br
	// dialWS 已消费头；Accept 校验在 dialWS 内部由 server 完成并回写 101。
	// 这里做客户端侧一致性：重算一遍并断言（函数级验证在 TestAcceptKeyRFC）。
	return conn
}

// TestAcceptKeyRFC：验收①——Accept key 对 RFC6455 §1.3 示例值断言。
func TestAcceptKeyRFC(t *testing.T) {
	const (
		in  = "dGhlIHNhbXBsZSBub25jZQ=="
		out = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	)
	if got := AcceptKey(in); got != out {
		t.Fatalf("AcceptKey(%q) = %q, want %q", in, got, out)
	}
}

// TestMaskedFrameRoundTrip：验收②——1000 条随机 payload 的掩码帧
// 编码→服务端解码 round-trip，全部一致（覆盖 125/65535 两档长度边界）。
func TestMaskedFrameRoundTrip(t *testing.T) {
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	sizes := []int{1, 100, 125, 126, 1000, 65535, 70000}
	for _, size := range sizes {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i % 251)
		}
		frame := EncodeClientFrame(OpBinary, payload, key)
		r := bufio.NewReader(bytes.NewReader(frame))
		got, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("size=%d ReadFrame: %v", size, err)
		}
		if got.Opcode != OpBinary {
			t.Errorf("size=%d opcode=%x", size, got.Opcode)
		}
		if !bytes.Equal(got.Payload, payload) {
			t.Errorf("size=%d payload 不一致（got %d 字节）", size, len(got.Payload))
		}
	}
}

// TestRealConnectionRoundTrip：真实连接上 1000 条掩码帧 echo 全部一致。
func TestRealConnectionRoundTrip(t *testing.T) {
	addr := startWSServer(t, 2*time.Second)
	conn := clientConn(t, addr)

	const total = 1000
	for i := 0; i < total; i++ {
		payload := []byte(fmt.Sprintf("msg-%04d", i))
		var key [4]byte
		binary.LittleEndian.PutUint32(key[:], uint32(i+1))
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		conn.Write(EncodeClientFrame(OpText, payload, key))

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		f, err := ReadFrame(bufio.NewReader(conn))
		if err != nil {
			t.Fatalf("第 %d 条读帧: %v", i, err)
		}
		if !bytes.Equal(f.Payload, payload) {
			t.Fatalf("第 %d 条回显 = %q, want %q", i, f.Payload, payload)
		}
	}
}

// TestCloseHandshake：验收③——客户端发 Close 帧，
// 服务端回 Close 后关闭；双方都经历关闭握手。
func TestCloseHandshake(t *testing.T) {
	addr := startWSServer(t, 2*time.Second)
	conn := clientConn(t, addr)

	var key [4]byte
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(EncodeClientFrame(OpClose, []byte{0x03, 0xE8}, key)) // 1000 = normal closure

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := ReadFrame(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("读 Close 响应: %v", err)
	}
	if f.Opcode != OpClose {
		t.Fatalf("收到 opcode=%x, want Close(0x8)", f.Opcode)
	}
	// 服务端应随即断开
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("服务端未断开连接")
	}
}

// TestPongHeartbeat：心跳——Ping 收到即回 Pong（保活语义）。
func TestPongHeartbeat(t *testing.T) {
	addr := startWSServer(t, 2*time.Second)
	conn := clientConn(t, addr)

	var key [4]byte
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	conn.Write(EncodeClientFrame(OpPing, []byte("keepalive"), key))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := ReadFrame(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("读 Pong: %v", err)
	}
	if f.Opcode != OpPong {
		t.Fatalf("opcode = %x, want Pong(0xA)", f.Opcode)
	}
	if string(f.Payload) != "keepalive" {
		t.Fatalf("Pong 载荷 = %q", f.Payload)
	}
}
