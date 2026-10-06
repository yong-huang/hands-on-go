package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// startCluster：起双后端 + mTLS 客户端配置，返回地址列表、TLS 配置与清理。
func startCluster(t testing.TB) (addrs []string, cfg *tls.Config, cleanup func()) {
	t.Helper()
	ca, err := newCA("test CA")
	if err != nil {
		t.Fatalf("newCA: %v", err)
	}
	srvCert, _ := ca.sign("server", 1, false)
	cliCert, err := ca.sign("client", 2, true)
	if err != nil {
		t.Fatalf("sign client: %v", err)
	}
	handler := func(payload []byte) ([]byte, error) {
		return []byte("resp:" + string(payload)), nil
	}
	var servers []*Server
	var addrList []string
	for i := 0; i < 2; i++ {
		s, err := NewServer("127.0.0.1:0", serverTLS(ca, srvCert), handler)
		if err != nil {
			t.Fatalf("server %d: %v", i, err)
		}
		addrList = append(addrList, s.Addr())
		servers = append(servers, s)
		go s.Serve()
	}
	cfg = clientTLS(ca, cliCert)
	return addrList, cfg, func() {
		for _, s := range servers {
			s.Close()
		}
	}
}

// TestSuccessRateAndNoLeak：核心验收——50 并发 × 20 请求 = 1000 次 mTLS 调用，
// 成功率 ≥99.5%，goleak 门禁绿。-short 缩为 100 请求。
func TestSuccessRateAndNoLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("完整版跑 1000 请求")
	}
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	addrs, cfg, cleanup := startCluster(t)
	defer cleanup()

	var okCount, failCount atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < 50; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			c, err := NewClient(addrs[w%len(addrs)], cfg)
			if err != nil {
				failCount.Add(1)
				return
			}
			defer c.Close()
			for i := 0; i < 20; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := c.Call(ctx, []byte(fmt.Sprintf("w%d-r%d", w, i)), 3*time.Second)
				cancel()
				if err == nil && strings.HasPrefix(string(resp), "resp:") {
					okCount.Add(1)
				} else {
					failCount.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	got := okCount.Load()
	lost := failCount.Load()
	total := got + lost
	rate := float64(got) / float64(total) * 100
	t.Logf("成功 %d / %d（%.1f%%）", got, total, rate)
	if rate < 99.5 {
		t.Fatalf("成功率 %.1f%% < 99.5%%", rate)
	}
}

// TestShortSuccessRate：-short 快速回归（100 次调用）。
func TestShortSuccessRate(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	addrs, cfg, cleanup := startCluster(t)
	defer cleanup()

	var okCount atomic.Int64
	const total = 100
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := NewClient(addrs[i%len(addrs)], cfg)
			if err != nil {
				return
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			resp, err := c.Call(ctx, []byte("ping"), 2*time.Second)
			if err == nil && strings.HasPrefix(string(resp), "resp:") {
				okCount.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := okCount.Load(); got < int64(total)*995/1000 {
		t.Fatalf("成功 %d/%d < 99.5%%", got, total)
	}
}

// TestMTLSRequired：无客户端证书时 Call 失败——mTLS 生效。
// 注意：TLS 1.3 的 Dial 可能成功（客户端握手先完成），
// 但第一次 Call 时服务端会拒绝并关连接。
func TestMTLSRequired(t *testing.T) {
	addr, cfg, cleanup := startCluster(t)
	defer cleanup()

	// 有证书：Call 成功
	c, err := NewClient(addr[0], cfg)
	if err != nil {
		t.Fatalf("mTLS dial: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, []byte("ok"), 2*time.Second); err != nil {
		t.Fatalf("有证书 Call 失败: %v", err)
	}

	// 无证书：连接可能建立，但 Call 必然失败
	bad, err := NewClient(addr[0], &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return // 拨号即失败也算通过
	}
	defer bad.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if _, err := bad.Call(ctx2, []byte("test"), 3*time.Second); err == nil {
		t.Fatal("无证书 Call 竟成功——mTLS 未生效")
	}
}
