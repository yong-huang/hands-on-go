// 项目 13：netpoller 与 C10K——goroutine-per-connection echo server 的内存账与延迟。
//
// 两个角色，同一个二进制：
//
//	服务端：go run ./13_netpoller_c10k/ -role=serve -addr=127.0.0.1:9007
//	客户端：go run ./13_netpoller_c10k/ -role=connect -addr=127.0.0.1:9007 -n=10000
//
// 协议：按行文本。普通行原样回显；"STATS" 行返回连接数与 goroutine 数。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	conns int64 // 存活连接数（accept +1，close -1）
)

func main() {
	role := flag.String("role", "serve", "serve | connect")
	addr := flag.String("addr", "127.0.0.1:9007", "服务端地址")
	n := flag.Int("n", 10000, "connect 模式：要维持的空闲连接数")
	msgs := flag.Int("msgs", 2000, "connect 模式：延迟测量次数")
	flag.Parse()

	switch *role {
	case "serve":
		serve(*addr)
	case "connect":
		connect(*addr, *n, *msgs)
	default:
		fmt.Fprintln(os.Stderr, "未知 role:", *role)
		os.Exit(2)
	}
}

// serve：accept 循环 + 每连接一个 goroutine。
// 在看什么：每个 conn.Read 阻塞时，对应 G 被 gopark 挂到 netpoller
// （运行时对 epoll/kqueue 的封装），不占用任何线程——这是万级连接的可行性来源。
func serve(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	fmt.Printf("serving on %s (pid=%d, GOMAXPROCS=%d)\n", addr, os.Getpid(), runtime.GOMAXPROCS(0))
	acceptLoop(ln)
}

func acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			fmt.Fprintln(os.Stderr, "accept:", err)
			return
		}
		atomic.AddInt64(&conns, 1)
		go handle(c)
	}
}

func handle(c net.Conn) {
	defer func() {
		atomic.AddInt64(&conns, -1)
		c.Close()
	}()
	r := bufio.NewReaderSize(c, 256)
	for {
		// 阻塞点：无数据可读时本 G 进入 _Gwaiting（等 netpoller），
		// 底层线程被释放去跑别的 G。
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if line == "STATS\n" {
			fmt.Fprintf(c, "STATS conns=%d goroutines=%d\n",
				atomic.LoadInt64(&conns), runtime.NumGoroutine())
			continue
		}
		if _, err := c.Write([]byte(line)); err != nil {
			return
		}
	}
}

// connect：建立 n 个空闲连接，读一次空闲态账本，再测 echo 延迟。
// 诚实预期：建连耗时秒级；RTT 毫秒以下为主，p99 偶有毫秒级尖峰。
func connect(addr string, n, msgs int) {
	cs := make([]net.Conn, 0, n)
	defer func() {
		for _, c := range cs {
			c.Close()
		}
	}()

	t0 := time.Now()
	var mu sync.Mutex
	sem := make(chan struct{}, 256) // 并发建连上限，避免瞬时洪峰
	var fail int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c, err := net.Dial("tcp", addr)
			if err != nil {
				atomic.AddInt64(&fail, 1)
				return
			}
			mu.Lock()
			cs = append(cs, c)
			mu.Unlock()
		}()
	}
	wg.Wait()
	fmt.Printf("== 阶段 1：建连 %d 个，失败 %d，耗时 %v ==\n", len(cs), fail, time.Since(t0).Round(time.Millisecond))
	if len(cs) == 0 {
		os.Exit(1)
	}

	// 每条连接配一个固定读缓冲，避免响应跨 TCP 分段时读半行。
	readers := make([]*bufio.Reader, len(cs))
	for i, c := range cs {
		readers[i] = bufio.NewReaderSize(c, 256)
	}

	// 阶段 2：空闲态账本。goroutines - conns 应为一个小常数（accept G、
	// main G 等），说明每个连接恰好一个 G 且线程数不随连接数增长。
	stats := queryStats(cs[0], readers[0])
	fmt.Printf("== 阶段 2：空闲态账本 ==\n  %s\n  goroutines-conns=%d（诚实预期：个位数常数）\n",
		stats, stats.goroutines-int(stats.conns))

	// 阶段 3：在已建立的连接上做串行 echo，测 RTT 分位数。
	rtts := make([]time.Duration, 0, msgs)
	for i := 0; i < msgs; i++ {
		c, r := cs[i%len(cs)], readers[i%len(cs)]
		msg := fmt.Sprintf("PING %d\n", i)
		s := time.Now()
		if _, err := c.Write([]byte(msg)); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		if _, err := r.ReadString('\n'); err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		rtts = append(rtts, time.Since(s))
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	pct := func(p float64) time.Duration {
		idx := int(p*float64(len(rtts))) - 1
		if idx < 0 {
			idx = 0
		}
		return rtts[idx]
	}
	fmt.Printf("== 阶段 3：%d 次 echo 的 RTT 分位数 ==\n", msgs)
	fmt.Printf("  p50=%v p95=%v p99=%v max=%v（样本=%d）\n",
		pct(0.50).Round(time.Microsecond), pct(0.95).Round(time.Microsecond),
		pct(0.99).Round(time.Microsecond), rtts[len(rtts)-1].Round(time.Microsecond), len(rtts))
	fmt.Println("RSS 请在服务端进程上另测：ps -o rss= -p <serve 的 pid>（单位 KB）")
}

type statsMsg struct {
	conns      int64
	goroutines int
}

func (s statsMsg) String() string {
	return fmt.Sprintf("conns=%d goroutines=%d", s.conns, s.goroutines)
}

func queryStats(c net.Conn, r *bufio.Reader) statsMsg {
	fmt.Fprintf(c, "STATS\n")
	line, err := r.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "stats read:", err)
		os.Exit(1)
	}
	var s statsMsg
	if _, err := fmt.Sscanf(line, "STATS conns=%d goroutines=%d", &s.conns, &s.goroutines); err != nil {
		fmt.Fprintln(os.Stderr, "stats parse:", line)
		os.Exit(1)
	}
	return s
}
