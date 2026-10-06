package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// setup：起服务端（注入策略可配），返回地址与收尾。
func setup(t *testing.T, slow func(string) time.Duration) string {
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
			go serveMux(c, slow)
		}
	}()
	return ln.Addr().String()
}

// TestConcurrentMuxPairing：验收①——100 并发请求经单连接乱序返回，
// 按 seq 全部正确配对（守恒：响应内容与请求一一对应）。
func TestConcurrentMuxPairing(t *testing.T) {
	addr := setup(t, nil2)
	m, err := NewMuxClient(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer m.Close()

	const total = 100
	var mu sync.Mutex
	hits, ooo := 0, 0
	var seqs []int32
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := m.Call(strings.Repeat("x", 100) + fmt.Sprintf("-%d", i))
			mu.Lock()
			defer mu.Unlock()
			if err == nil && strings.HasSuffix(resp, fmt.Sprintf("-%d", i)) {
				hits++
				if len(seqs) > 0 && seqs[len(seqs)-1] > int32(i) {
					ooo++
				}
				seqs = append(seqs, int32(i))
			}
		}(i)
	}
	wg.Wait()
	if hits != total {
		t.Fatalf("配对 %d/%d", hits, total)
	}
	if ooo == 0 {
		t.Log("本轮未观测到乱序（调度相关，不判失败）")
	}
}

// TestHeadOfLineBlocking：验收②——seq=0 注入 200ms 时，
// 多路复用下其余 99 个请求的总耗时 ≈200ms（不被拖住）；
// 且全部完成时间与慢请求几乎同时（快请求先回）。
func TestHeadOfLineBlocking(t *testing.T) {
	addr := setup(t, slowSleeper("SLOW"))
	m, err := NewMuxClient(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer m.Close()

	var wg sync.WaitGroup
	start := time.Now()
	// 不变式：payload != SLOW 的 99 个快请求，自身往返 ≤50ms（自身 RTT 量级）。
	// 慢请求按 payload 标识（seq 由并发分配，与 goroutine 编号无对应——开发实录）。
	var latMu sync.Mutex
	slowestFastLatency := time.Duration(0)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := "fast"
			if i == 0 {
				payload = "SLOW"
			}
			issued := time.Now()
			_, err := m.Call(payload)
			lat := time.Since(issued)
			if err != nil {
				t.Errorf("Call(%s): %v", payload, err)
				return
			}
			if payload == "fast" {
				latMu.Lock()
				if lat > slowestFastLatency {
					slowestFastLatency = lat
				}
				latMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	el := time.Since(start)
	if el < 190*time.Millisecond || el > 600*time.Millisecond {
		t.Fatalf("总耗时 %v, want ≈200ms（慢请求）±调度余量", el.Round(time.Millisecond))
	}
	if slowestFastLatency > 50*time.Millisecond {
		t.Fatalf("快请求自身往返最慢 %v——远超自身 RTT，被慢请求拖住，多路复用失效",
			slowestFastLatency.Round(time.Millisecond))
	}
}

// TestClosedNotHanging：验收③——连接关闭后，in-flight 请求全部立即
// 得到 ErrClosed（不悬挂）。sentinel 确认全部请求已发出再关。
func TestClosedNotHanging(t *testing.T) {
	addr := setup(t, func(string) time.Duration { return time.Hour }) // 响应永不回
	m, err := NewMuxClient(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	var wg, sent sync.WaitGroup
	const total = 50
	sent.Add(total)
	errs := make([]error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = m.CallWithSentinel("orphan", &sent)
		}(i)
	}
	sent.Wait() // 全部在途
	closeStart := time.Now()
	m.Close()
	wg.Wait()
	closeElapsed := time.Since(closeStart)

	for i, err := range errs {
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("请求 %d err = %v, want ErrClosed", i, err)
		}
	}
	if closeElapsed > 100*time.Millisecond {
		t.Fatalf("关闭后 %v 才全部返回——存在悬挂", closeElapsed.Round(time.Millisecond))
	}
}
