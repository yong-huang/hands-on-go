package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// main：单机双后端 + mTLS 网关 + 1000 并发请求演示。
func main() {
	ca, err := newCA("mini-rpc CA")
	if err != nil {
		fmt.Println(err)
		return
	}
	srvCert, _ := ca.sign("server", 1, false)
	cliCert, _ := ca.sign("client", 2, true)

	handler := func(payload []byte) ([]byte, error) {
		return []byte("resp:" + string(payload)), nil
	}

	// 双后端
	srvA, err := NewServer("127.0.0.1:0", serverTLS(ca, srvCert), handler)
	if err != nil {
		fmt.Println(err)
		return
	}
	srvB, err := NewServer("127.0.0.1:0", serverTLS(ca, srvCert), handler)
	if err != nil {
		fmt.Println(err)
		return
	}
	go srvA.Serve()
	go srvB.Serve()

	// 客户端连接两个后端
	cliA, err := NewClient(srvA.Addr(), clientTLS(ca, cliCert))
	if err != nil {
		fmt.Println(err)
		return
	}
	cliB, err := NewClient(srvB.Addr(), clientTLS(ca, cliCert))
	if err != nil {
		fmt.Println(err)
		return
	}
	clients := []*Client{cliA, cliB}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
		srvA.Close()
		srvB.Close()
	}()

	fmt.Println("== 1000 请求经 mTLS 到双后端 ==")
	rr := 0
	var mu sync.Mutex
	var ok, fail int
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mu.Lock()
			c := clients[rr%len(clients)]
			rr++
			mu.Unlock()
			ctx, cancel := contextWithTimeout(3 * time.Second)
			defer cancel()
			resp, err := c.Call(ctx, []byte(fmt.Sprintf("req-%d", i)), 2*time.Second)
			mu.Lock()
			if err == nil {
				ok++
				_ = resp
			} else {
				fail++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	fmt.Printf("  成功 %d / 失败 %d / 总 %d\n", ok, fail, ok+fail)
	srvA.Close()
	srvB.Close()
}

// contextWithTimeout：简写。
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// ioDiscard：避免导入 io 只为 Discard。
var ioDiscard = discard{}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

var _ = os.Getenv
