package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

// Service：开销基准用的"真实一点"的并发服务。
// 请求进来：读多写少（每 3 个请求写 1 个），RWMutex 保护 map。
// 本身是正确同步的，-race 下应零报告——它是测量检测器开销的载体。
type Service struct {
	mu   sync.RWMutex
	data map[int]int
}

func NewService() *Service {
	return &Service{data: make(map[int]int, 1024)}
}

// Handle：处理一个请求。key 为 3 的倍数时写入，否则读取。
func (s *Service) Handle(req int) int {
	if req%3 == 0 {
		s.mu.Lock()
		s.data[req] = req * 2
		s.mu.Unlock()
		return req * 2
	}
	s.mu.RLock()
	v := s.data[req]
	s.mu.RUnlock()
	return v
}

// BenchmarkService：并发负载基准。不带 -race 与带 -race 各跑一次，
// ns/op 的比值就是检测器的时间开销（结果入 README）。
func BenchmarkService(b *testing.B) {
	s := NewService()
	var seq atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		base := int(seq.Add(1)) * 7 // 每个 worker 错开 key 空间
		for i := 0; pb.Next(); i++ {
			s.Handle(base + i)
		}
	})
}

// TestServiceLoad：固定负载，专供 /usr/bin/time 测 RSS（内存开销入 README）。
// 顺带断言结果正确性：写入过的 key 读回必须是写入值。
func TestServiceLoad(t *testing.T) {
	s := NewService()
	const n = 200_000
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < n/8; i++ {
				req := base*1_000_000 + i
				s.Handle(req)
			}
		}(w)
	}
	wg.Wait()
	if got := s.Handle(3); got != 6 { // 3 是写 key：曾以 6 写入
		t.Fatalf("Handle(3)=%d, want 6", got)
	}
}

// TestMissedRaceConstructionNoReport：漏报构造的可执行化。
// -race 下本测试应当零报告通过；若未来 Go 升级修复了这类漏报，
// 本测试会转红——那本身就是一条值得记录的检测器演进证据。
func TestMissedRaceConstructionNoReport(t *testing.T) {
	for i := 0; i < 3; i++ {
		missedRace()
	}
}

// TestServiceUnderRaceClean：服务在 -race 高压下零报告、结果守恒。
func TestServiceUnderRaceClean(t *testing.T) {
	s := NewService()
	const perW, workers = 5000, 8
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perW; i++ {
				req := base*100_000 + i
				if v := s.Handle(req); req%3 == 0 && v != req*2 {
					t.Errorf("Handle(%d)=%d, want %d", req, v, req*2)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	s.mu.RLock()
	size := len(s.data)
	s.mu.RUnlock()
	if size != workers*perW/3 {
		t.Fatalf("map 大小 %d, want %d", size, workers*perW/3)
	}
}
