package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestActiveCloseTimeWait：验收①——服务端主动 Close 后，
// netstat 能观测到该端口的 TIME_WAIT 条目（存活 ~30s，不必等它消失）。
func TestActiveCloseTimeWait(t *testing.T) {
	const port = "19511"
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		c, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		// 被动方：读完服务端数据与 FIN（EOF）后自己再 Close。
		io.Copy(io.Discard, c)
		c.Close()
	}()

	srv, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	srv.Write([]byte("bye\n"))
	srv.Close() // 服务端主动关：第一个 FIN 发出 → 服务端进 TIME_WAIT
	<-clientDone

	// TIME_WAIT 在最终 ACK 后立即出现，轮询最多 5 秒等它上表。
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := exec.Command("netstat", "-an").Output()
		if err != nil {
			t.Fatalf("netstat: %v", err)
		}
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "."+port) && strings.Contains(line, "TIME_WAIT") {
				found = true
				t.Logf("观测到 TIME_WAIT：%s", strings.TrimSpace(line))
				break
			}
		}
		if found {
			return // 验收达成；不等它消失（实测存活见 README 的 -watch 记录）
		}
		if time.Now().After(deadline) {
			t.Fatalf("5 秒内未在 netstat 观测到 %s 的 TIME_WAIT", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestResetErrorType：验收②——客户端 SO_LINGER=0 后 Close 会发 RST，
// 服务端 Read 返回的正是 *net.OpError（ECONNRESET），而不是干净的 EOF。
func TestResetErrorType(t *testing.T) {
	const port = "19512"
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		fmt.Fprintf(c, "data before crash\n")
		// "不 Close 直接死亡"的可复现形式：linger=0 的 Close 发 RST 而非 FIN。
		if tcp := c.(*net.TCPConn); true {
			_ = tcp.SetLinger(0)
		}
		c.Close()
	}()

	srv, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer srv.Close()
	// 先把存量数据读干净（Read 有数据时返回 nil），下一读才会撞上 RST。
	buf := make([]byte, 64)
	var readErr error
	for readErr == nil {
		_, readErr = srv.Read(buf)
	}

	var opErr *net.OpError
	if !errors.As(readErr, &opErr) {
		t.Fatalf("Read err = %v (%T), want *net.OpError", readErr, readErr)
	}
	if !errors.Is(readErr, syscall.ECONNRESET) {
		t.Fatalf("Read err = %v, want ECONNRESET", readErr)
	}
	t.Logf("服务端读到的错误：%v（*net.OpError 包裹 ECONNRESET）", readErr)
	<-done
}

// TestGracefulFinEOF：对照组——正常 Close（发 FIN）时服务端读到的是干净的
// io.EOF，而不是错误。两条路一对比，RST 与 FIN 的区别就落地了。
func TestGracefulFinEOF(t *testing.T) {
	const port = "19513"
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		fmt.Fprintf(c, "polite goodbye\n")
		c.Close() // 正常关闭：发 FIN
	}()

	srv, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer srv.Close()
	r := bufio.NewReader(srv)
	if _, err := r.ReadString('\n'); err != nil {
		t.Fatalf("读存量数据: %v", err)
	}
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("FIN 后 Read = %v, want io.EOF", err)
	}
	<-done
}

// TestHalfClose：验收③——CloseWrite 半关闭后，对端仍能读全存量数据，
// 且还能反过来给本端发话；本端在半关闭状态下读对端新数据不受影响。
func TestHalfClose(t *testing.T) {
	const port = "19514"
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	clientDone := make(chan struct{})
	const msgs = 3
	go func() {
		defer close(clientDone)
		c, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer c.Close()
		for i := 0; i < msgs; i++ {
			fmt.Fprintf(c, "msg-%d\n", i)
		}
		tcp := c.(*net.TCPConn)
		if err := tcp.CloseWrite(); err != nil { // 半关闭：发 FIN，只关写方向
			t.Errorf("CloseWrite: %v", err)
			return
		}
		// 关键断言：FIN 之后仍能收到服务端的新消息。
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil || line != "server still talks\n" {
			t.Errorf("半关闭后收到 %q, err=%v", line, err)
		}
	}()

	srv, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer srv.Close()
	r := bufio.NewReader(srv)
	for i := 0; i < msgs; i++ { // FIN 前的数据一条不少
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("读消息 %d: %v", i, err)
		}
	}
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("应读到 FIN(EOF), got %v", err)
	}
	if _, err := srv.Write([]byte("server still talks\n")); err != nil {
		t.Fatalf("半关闭后服务端写失败: %v", err)
	}
	<-clientDone
}
