// 项目 10：http.Transport 连接池——keep-alive、调参与连接泄漏。
//
// http.Client 背后的 Transport 维护一个连接池：请求结束后连接不关闭，
// 放回池里给后续请求复用（keep-alive）。池子有两个关键参数：
//
//	MaxIdleConnsPerHost：每个 host 最多保留几条**空闲**连接（默认 2！）
//	MaxIdleConns        ：全局空闲上限（默认 100）
//
// 池满时的策略是"用完即关"——并发突刺建起的连接，响应一回来就被裁撤，
// 下一波突刺重新握手。本篇用服务端 ConnState 计数实测三件事：
//
//	第 1 步：默认参数——1000 请求/50 并发，服务端实际建连次数（远多于 50）
//	第 2 步：MaxIdleConnsPerHost=100——同样的流量，建连数 = 初始并发数
//	第 3 步：响应体未读尽——Transport 无法复用该连接，逐条重建（泄漏形态）
//
// 排查工具：netstat -an | grep 19591，观察 ESTABLISHED 的实时条数。
//
// 运行方式：go run .；基准与断言见 go test。
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const addr = "127.0.0.1:19591" // 本篇专用端口

// counters：服务端视角的连接账本（ConnState 钩子填充）。
type counters struct {
	newConns, closedConns, requests atomic.Int64
	delayNs                         atomic.Int64 // handler 处理延迟（稳态旋钮）
}

func newServer(addr string, c *counters) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		if d := c.delayNs.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(strings.Repeat("B", 64*1024))) // 64KB：超过 256B 的自动排空阈值
	})
	s := &http.Server{Addr: addr, Handler: mux,
		ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	s.ConnState = func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			c.newConns.Add(1)
		case http.StateClosed:
			c.closedConns.Add(1)
		}
	}
	return s
}

// burst：concurrency 个 worker 共享 client 向 addr 发 total 个请求。
func burst(client *http.Client, addr string, total, concurrency int) {
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := client.Get("http://" + addr + "/health")
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body) // 读尽：给连接池复用的机会
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
}

func main() {
	cnt := &counters{}
	srv := newServer(addr, cnt)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	go srv.Serve(ln)
	defer srv.Close()
	time.Sleep(100 * time.Millisecond)

	fmt.Println("== 第 1 步：默认 Transport（MaxIdleConnsPerHost=2）==")
	n0, c0, r0 := cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	burst(&http.Client{Timeout: 5 * time.Second}, addr, 1000, 50)
	n1, c1, r1 := cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	fmt.Printf("  1000 请求（50 并发）→ 服务端建连 %d 次，关闭 %d 次，请求数 %d\n",
		n1-n0, c1-c0, r1-r0)
	fmt.Println("  ⇒ 50 条并发连接用完即裁，池里只留 2 条空闲——下一波突刺重新握手。")

	fmt.Println()
	fmt.Println("== 第 2 步：调优 Transport（MaxIdleConnsPerHost=100）==")
	tuned := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100, // 默认 2 → 100
			IdleConnTimeout:       30 * time.Second,
			MaxConnsPerHost:       0, // 不限制并发建连
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
	n0, c0, r0 = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	burst(tuned, addr, 1000, 50)
	n1, c1, r1 = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	fmt.Printf("  1000 请求（50 并发）→ 服务端建连 %d 次，关闭 %d 次，请求数 %d\n",
		n1-n0, c1-c0, r1-r0)
	fmt.Println("  ⇒ 突刺建起的 50 条连接全部进池保留，后续请求零握手复用。")

	fmt.Println()
	fmt.Println("== 第 3 步：响应体未读尽 → 连接被弃（泄漏形态）==")
	leakClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: 100},
	}
	n0, _, _ = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	for i := 0; i < 10; i++ {
		resp, err := leakClient.Get("http://" + addr + "/big") // 64KB
		if err != nil {
			fmt.Println("get:", err)
			return
		}
		_ = resp // Body 未读未关：引用被丢弃，连接无法回池
	}
	time.Sleep(300 * time.Millisecond)
	n1, c1, _ = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	fmt.Printf("  10 次未读尽的 /big 请求 → 建连 %d 次、关闭 %d 次（每条连接用完即弃）\n",
		n1-n0, c1-c0)

	drainClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: 100},
	}
	n0, _, _ = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	for i := 0; i < 10; i++ {
		resp, err := drainClient.Get("http://" + addr + "/big")
		if err != nil {
			fmt.Println("get:", err)
			return
		}
		io.Copy(io.Discard, resp.Body) // 读尽 → 连接回池
		resp.Body.Close()
	}
	time.Sleep(300 * time.Millisecond)
	n1, c1, _ = cnt.newConns.Load(), cnt.closedConns.Load(), cnt.requests.Load()
	fmt.Printf("  10 次读尽再关闭的同样请求 → 建连 %d 次、关闭 %d 次（单连接全程复用）\n",
		n1-n0, c1-c0)
	fmt.Println("  ⇒ 泄漏不是忘了 Close 才叫泄漏：Body 未排干，Transport 只能弃连接重建。")

	fmt.Println()
	fmt.Println("== 排查工具 ==")
	fmt.Println("  watch -n 1 'netstat -an | grep 19591 | grep ESTABLISHED | wc -l'")
	fmt.Println("  （基准与断言：go test -race -count=3 .）")
}
