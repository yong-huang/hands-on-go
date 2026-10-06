// 项目 7：文本协议 vs 二进制协议——同一语义，两种封装，实测取舍。
//
// 同一个 KV 缓存语义（SET/GET/DEL/QUIT + 批量 PING）做两版协议：
//
//	文本版（RESP 风格，端口 19561）：可读可手敲，nc 直连即可调试；
//	  SET key value → +OK\r\n；GET key → $n\r\n<value>\r_n 或 $-1\r\n
//	二进制版（端口 19562）：项目 5 的 NETC 帧，Kind 即命令，载荷即参数行。
//
// 三个实测视角：
//
//	第 1 步：nc 交互实录（文本协议的可调试性是杀手锏）
//	第 2 步：同流量吞吐对比（10 万条 64B 值，回环 TCP）
//	第 3 步：解码开销对比（纯解码基准，把网络因素剔除）
//
// 运行方式：go run .   （两协议各起一个服务，跑完基准自动退出；
//
//	手动调试：另开终端 nc 127.0.0.1 19561）
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// ---------- KV 存储与命令语义（两协议共享） ----------

type KVStore struct {
	mu sync.RWMutex
	m  map[string]string
}

func NewKV() *KVStore { return &KVStore{m: map[string]string{}} }

func (kv *KVStore) Set(k, v string) {
	kv.mu.Lock()
	kv.m[k] = v
	kv.mu.Unlock()
}
func (kv *KVStore) Get(k string) (string, bool) {
	kv.mu.RLock()
	v, ok := kv.m[k]
	kv.mu.RUnlock()
	return v, ok
}
func (kv *KVStore) Del(k string) {
	kv.mu.Lock()
	delete(kv.m, k)
	kv.mu.Unlock()
}

// ---------- 文本协议（RESP 风格） ----------

// applyText：解析一行命令，返回响应字符串（含 CRLF）。
// 命令集：SET k v / GET k / DEL k / PING / QUIT。
func applyText(kv *KVStore, line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "-ERR empty\r\n"
	}
	switch strings.ToUpper(fields[0]) {
	case "SET":
		if len(fields) != 3 {
			return "-ERR usage: SET key value\r\n"
		}
		kv.Set(fields[1], fields[2])
		return "+OK\r\n"
	case "GET":
		if len(fields) != 2 {
			return "-ERR usage: GET key\r\n"
		}
		if v, ok := kv.Get(fields[1]); ok {
			return fmt.Sprintf("$%d\r\n%s\r\n", len(v), v) // bulk string
		}
		return "$-1\r\n" // nil
	case "DEL":
		if len(fields) != 2 {
			return "-ERR usage: DEL key\r\n"
		}
		kv.Del(fields[1])
		return ":1\r\n"
	case "PING":
		return "+PONG\r\n"
	case "QUIT":
		return "+BYE\r\n"
	default:
		return fmt.Sprintf("-ERR unknown command %q\r\n", fields[0])
	}
}

// serveText：文本协议服务端——行协议循环。
func serveText(conn net.Conn, kv *KVStore) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		resp := applyText(kv, line)
		conn.Write([]byte(resp))
		if strings.EqualFold(strings.TrimSpace(line), "QUIT") {
			return
		}
	}
}

// ---------- 二进制协议（NETC 帧，同项目 5） ----------

const (
	Magic      uint32 = 0x4E455443
	Version    byte   = 1
	HeadSize          = 8
	CRCSize           = 4
	MaxPayload        = 4096
)

type Kind byte

const (
	KindSet Kind = 0x01 // 载荷: "k v"（空格分隔，KV 语义够用）
	KindGet Kind = 0x02 // 载荷: "k"
	KindDel Kind = 0x03 // 载荷: "k"
	KindPng Kind = 0x04
)

var ErrBadFrame = errors.New("bad frame")

func frameEncode(kind Kind, payload string) []byte {
	total := HeadSize + len(payload) + CRCSize
	b := make([]byte, total)
	binary.BigEndian.PutUint32(b[0:], Magic)
	b[4] = Version
	b[5] = byte(kind)
	binary.BigEndian.PutUint16(b[6:], uint16(len(payload)))
	copy(b[HeadSize:], payload)
	binary.BigEndian.PutUint32(b[HeadSize+len(payload):],
		crc32.ChecksumIEEE(b[:HeadSize+len(payload)]))
	return b
}

// frameDecode：全量解码（内部流量可信端到端，矩阵/fuzz 见项目 6）。
func frameDecode(b []byte) (Kind, string, error) {
	if len(b) < HeadSize+CRCSize || binary.BigEndian.Uint32(b[0:]) != Magic || b[4] != Version {
		return 0, "", ErrBadFrame
	}
	n := int(binary.BigEndian.Uint16(b[6:]))
	if len(b) < HeadSize+n+CRCSize {
		return 0, "", ErrBadFrame
	}
	if crc32.ChecksumIEEE(b[:HeadSize+n]) != binary.BigEndian.Uint32(b[HeadSize+n:]) {
		return 0, "", ErrBadFrame
	}
	return Kind(b[5]), string(b[HeadSize : HeadSize+n]), nil
}

// serveBinary：二进制协议服务端——帧循环。
func serveBinary(conn net.Conn, kv *KVStore) {
	defer conn.Close()
	head := make([]byte, HeadSize)
	for {
		if _, err := io.ReadFull(conn, head); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(head[6:]))
		body := make([]byte, n+CRCSize)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		full := append(append([]byte{}, head...), body...)
		kind, payload, err := frameDecode(full)
		if err != nil {
			return
		}
		fields := strings.Fields(payload)
		var resp []byte
		switch kind {
		case KindSet:
			if len(fields) == 2 {
				kv.Set(fields[0], fields[1])
			}
			resp = frameEncode(KindSet, "OK")
		case KindGet:
			v, _ := kv.Get(payload)
			resp = frameEncode(KindGet, v)
		case KindDel:
			kv.Del(payload)
			resp = frameEncode(KindDel, "OK")
		case KindPng:
			resp = frameEncode(KindPng, "PONG")
		default:
			return
		}
		conn.Write(resp)
	}
}

func main() {
	fmt.Println("== 第 1 步：两协议服务端就绪（文本 :19561 / 二进制 :19562）==")
	kv := NewKV()
	lnText, _ := net.Listen("tcp", "127.0.0.1:19561")
	lnBin, _ := net.Listen("tcp", "127.0.0.1:19562")
	defer lnText.Close()
	defer lnBin.Close()
	go acceptLoop(lnText, func(c net.Conn) { serveText(c, kv) })
	go acceptLoop(lnBin, func(c net.Conn) { serveBinary(c, kv) })
	time.Sleep(100 * time.Millisecond)

	fmt.Println()
	fmt.Println("== 第 2 步：nc 交互实录（文本协议的可调试性）==")
	demoNC(lnText, kv)

	fmt.Println()
	fmt.Println("== 第 3 步：吞吐基准 ==")
	fmt.Println("  go test -bench . -benchtime=2s   （标准基准，结果实录见 bench.txt 与 README）")
}

// acceptLoop：单监听多连接的接受循环。
func acceptLoop(ln net.Listener, serve func(net.Conn)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go serve(c)
	}
}

// demoNC：程序内模拟一次"nc 会话"，逐条打印请求/响应（与手工 nc 等价；
// 真实调试：另开终端 nc 127.0.0.1 19561，逐条手敲同样的命令）。
func demoNC(ln net.Listener, kv *KVStore) {
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveText(c, kv)
	}()
	c, err := net.Dial("tcp", "127.0.0.1:19561")
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	sessions := []string{"SET user:1 alice", "GET user:1", "GET user:2", "DEL user:1", "GET user:1", "PING", "QUIT"}
	r := bufio.NewReader(c)
	for _, cmd := range sessions {
		fmt.Printf("  > %s\n", cmd)
		c.Write([]byte(cmd + "\r\n"))
		line, _ := r.ReadString('\n')
		if strings.HasPrefix(line, "$") && line != "$-1\r\n" {
			// bulk string：长度行之后还有值行，一并读出
			val, _ := r.ReadString('\n')
			fmt.Printf("  < %s  %s", strings.TrimRight(line, "\r\n"), val)
		} else {
			fmt.Printf("  < %s", line)
		}
	}
	<-time.After(200 * time.Millisecond)
	c.Close()
}
