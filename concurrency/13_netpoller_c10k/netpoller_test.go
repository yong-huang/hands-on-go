package main

import (
	"bufio"
	"fmt"
	"net"
	"sort"
	"testing"
	"time"
)

// TestEchoAndIdleLedger：小规模走通全链路——建 200 个空闲连接，
// 断言 echo 语义正确、STATS 报告的 goroutine 数 = 连接数 + 小常数。
// 真实 10K 规模的数字由 README 的实测记录承载，测试只保语义。
func TestEchoAndIdleLedger(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go acceptLoop(ln)

	const N = 200
	cs := make([]net.Conn, 0, N)
	defer func() {
		for _, c := range cs {
			c.Close()
		}
	}()
	for i := 0; i < N; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		cs = append(cs, c)
	}

	// 让服务端把 200 个 handle goroutine 全部拉起。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := statsRequest(t, cs[0])
		if s.conns >= N {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s := statsRequest(t, cs[0])
	if s.conns != N {
		t.Fatalf("conns=%d，期望 %d", s.conns, N)
	}
	// 每连接恰好一个 G：多出来的应是 accept/main 等个位数固定值。
	if over := s.goroutines - int(s.conns); over < 0 || over > 64 {
		t.Fatalf("goroutines-conns=%d，超出 [0,64] 的固定常数范围", over)
	}
	t.Logf("conns=%d goroutines=%d (差=%d)", s.conns, s.goroutines, s.goroutines-int(s.conns))

	// echo 语义：发什么回什么。
	r := bufio.NewReaderSize(cs[1], 256)
	msg := "hello netpoller\n"
	if _, err := cs[1].Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != msg {
		t.Fatalf("echo 不一致: %q", got)
	}
}

// TestEchoLatencyBounded：300 次串行 echo，p99 应远小于秒级
// （本地回环 + 空闲连接）。阈值放宽到 200ms 防 CI 抖动。
func TestEchoLatencyBounded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go acceptLoop(ln)

	const N = 50
	var cs []net.Conn
	defer func() {
		for _, c := range cs {
			c.Close()
		}
	}()
	for i := 0; i < N; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		cs = append(cs, c)
	}

	rtts := make([]int64, 0, 300)
	for i := 0; i < 300; i++ {
		c := cs[i%len(cs)]
		r := bufio.NewReaderSize(c, 256)
		s := time.Now()
		fmt.Fprintf(c, "PING %d\n", i)
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("read: %v", err)
		}
		rtts = append(rtts, int64(time.Since(s)))
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	p99 := rtts[297]
	t.Logf("echo p99 = %v", time.Duration(p99))
	if p99 > int64(200*time.Millisecond) {
		t.Fatalf("p99=%v 超过 200ms，回环路径异常", time.Duration(p99))
	}
}

func statsRequest(t *testing.T, c net.Conn) statsMsg {
	t.Helper()
	fmt.Fprintf(c, "STATS\n")
	r := bufio.NewReaderSize(c, 256)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("stats read: %v", err)
	}
	var s statsMsg
	if _, err := fmt.Sscanf(line, "STATS conns=%d goroutines=%d", &s.conns, &s.goroutines); err != nil {
		t.Fatalf("stats parse %q: %v", line, err)
	}
	return s
}
