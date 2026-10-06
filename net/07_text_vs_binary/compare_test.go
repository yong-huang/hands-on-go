package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// benchProtocol：total=b.N 对 SET+GET 串行往返，返回总耗时。
// 一问一答严格配对：响应一条条读，读错位即失败（基准不允许错位漂移）。
func benchProtocol(b *testing.B, addr string, binary bool) time.Duration {
	sock, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer sock.Close()
	r := bufio.NewReader(sock)
	value := strings.Repeat("v", 64)
	start := time.Now()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("k%d", i)
		if binary {
			sock.Write(frameEncode(KindSet, key+" "+value))
			readFrame(r)
			sock.Write(frameEncode(KindGet, key))
			readFrame(r)
		} else {
			sock.Write([]byte("SET " + key + " " + value + "\r\n"))
			readLine(r) // +OK
			sock.Write([]byte("GET " + key + "\r\n"))
			line := readLine(r) // $64
			n := 0
			fmt.Sscanf(line, "$%d", &n)
			if n > 0 {
				io.ReadFull(r, make([]byte, n+2)) // 值行 + CRLF
			}
		}
	}
	return time.Since(start)
}

// readLine：读一行 CRLF 结尾的文本。
func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// readFrame：读一个 NETC 帧。
func readFrame(r *bufio.Reader) {
	head := make([]byte, HeadSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return
	}
	n := int(binary.BigEndian.Uint16(head[6:]))
	io.ReadFull(r, make([]byte, n+CRCSize))
}

// BenchmarkTextProtocol / BenchmarkBinaryProtocol：同流量对比。
// 报告 ns/op（每对 SET+GET 的均摊延迟）与 ops/s。
func setupServersB(b testing.TB) (string, string) {
	b.Helper()
	lt, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen text: %v", err)
	}
	lb, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen binary: %v", err)
	}
	b.Cleanup(func() { lt.Close(); lb.Close() })
	go acceptLoop(lt, func(c net.Conn) { serveText(c, NewKV()) })
	go acceptLoop(lb, func(c net.Conn) { serveBinary(c, NewKV()) })
	return lt.Addr().String(), lb.Addr().String()
}

func BenchmarkText(b *testing.B) {
	textAddr, _ := setupServersB(b)
	el := benchProtocol(b, textAddr, false)
	b.ReportMetric(float64(b.N*2)/el.Seconds(), "ops/s")
}

func BenchmarkBinary(b *testing.B) {
	_, binAddr := setupServersB(b)
	el := benchProtocol(b, binAddr, true)
	b.ReportMetric(float64(b.N*2)/el.Seconds(), "ops/s")
}

// TestSameSemantics：验收①的自动化面——两协议对同一命令序列给出等价语义。
func TestSameSemantics(t *testing.T) {
	textAddr, binAddr := setupServersB(t)

	// 文本版
	c1, err := net.Dial("tcp", textAddr)
	if err != nil {
		t.Fatalf("dial text: %v", err)
	}
	defer c1.Close()
	r1 := bufio.NewReader(c1)
	c1.Write([]byte("SET a hello\r\nGET a\r\nDEL a\r\nGET a\r\n"))
	if got := readLine(r1); got != "+OK" {
		t.Fatalf("text SET = %q", got)
	}
	if got := readLine(r1); got != "$5" {
		t.Fatalf("text GET 头 = %q, want $5", got)
	}
	if got := readLine(r1); got != "hello" {
		t.Fatalf("text GET 值 = %q", got)
	}
	if got := readLine(r1); got != ":1" {
		t.Fatalf("text DEL = %q", got)
	}
	if got := readLine(r1); got != "$-1" {
		t.Fatalf("text GET 删除后 = %q, want $-1", got)
	}

	// 二进制版：SET → GET 回读值应等于写入值
	c2, err := net.Dial("tcp", binAddr)
	if err != nil {
		t.Fatalf("dial bin: %v", err)
	}
	defer c2.Close()
	c2.Write(frameEncode(KindSet, "a hello"))
	r2 := bufio.NewReader(c2)
	readFrame(r2)
	c2.Write(frameEncode(KindGet, "a"))
	head := make([]byte, HeadSize)
	io.ReadFull(r2, head)
	n := int(binary.BigEndian.Uint16(head[6:]))
	body := make([]byte, n+CRCSize)
	io.ReadFull(r2, body)
	_, val, err := frameDecode(append(append([]byte{}, head...), body...))
	if err != nil || val != "hello" {
		t.Fatalf("bin GET = (%q, %v)", val, err)
	}

}
