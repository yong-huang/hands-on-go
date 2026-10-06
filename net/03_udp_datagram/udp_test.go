package main

import (
	"errors"
	"math/rand"
	"net"
	"syscall"
	"testing"
	"time"
)

// runPipeline：按给定策略搭一条 client→proxy→server 管道，
// 发 n 个包后等 END 触发统计收拢。固定种子 ⇒ 结果可复现。
func runPipeline(t *testing.T, drop, reorder float64, seed int64, n int) Stats {
	t.Helper()
	statsCh := make(chan Stats, 1)
	go runServer(statsCh)
	time.Sleep(50 * time.Millisecond)

	stop := newProxy(drop, reorder, rand.New(rand.NewSource(seed)))
	defer stop()
	time.Sleep(50 * time.Millisecond)

	sendPackets(n, 10)

	select {
	case st := <-statsCh:
		return st
	case <-time.After(10 * time.Second):
		t.Fatal("10 秒内未收到 END 统计")
		return Stats{}
	}
}

// TestDropRate：验收①——30% 丢包代理下，到达率落在 60%~80% 区间。
// 2000 个包的二项分布均值 70%、标准差 ≈1%，区间极宽，必然命中。
func TestDropRate(t *testing.T) {
	st := runPipeline(t, 0.30, 0, 42, 2000)
	rate := float64(st.Received) / 2000 // 服务端的 Rate 按 demo 常量算，测试自算
	t.Logf("到达 %d/2000（%.1f%%）", st.Received, rate*100)
	if rate < 0.60 || rate > 0.80 {
		t.Fatalf("到达率 %.1f%% 超出 [60%%, 80%%] 验收区间", rate*100)
	}
	if st.Dups != 0 {
		t.Fatalf("无重排拓扑下不应有重复：Dups = %d", st.Dups)
	}
}

// TestReorder：验收②——20% 乱序代理下，到达序号出现下降（乱序的现场证据）。
func TestReorder(t *testing.T) {
	st := runPipeline(t, 0, 0.20, 7, 2000)
	t.Logf("到达 %d/2000，乱序下降 %d 次", st.Received, st.Descents)
	if st.Descents == 0 {
		t.Fatal("20% 乱序代理下 Descents = 0，乱序注入未生效")
	}
	if st.Min < 0 || st.Max >= 2000 {
		t.Fatalf("到达序号越界：[%d, %d]", st.Min, st.Max)
	}
}

// TestDropAndReorderTogether：两者叠加，丢包与乱序同时可观测。
func TestDropAndReorderTogether(t *testing.T) {
	st := runPipeline(t, 0.30, 0.20, 99, 2000)
	rate := float64(st.Received) / 2000
	t.Logf("到达 %.1f%%，乱序下降 %d 次", rate*100, st.Descents)
	if rate < 0.60 || rate > 0.80 {
		t.Fatalf("到达率 %.1f%% 超出验收区间", rate*100)
	}
	if st.Descents == 0 {
		t.Fatal("乱序注入未生效")
	}
}

// TestSizeLimit：验收③——macOS loopback 单报文上限实测。
// 9216B（net.inet.udp.maxdgram）完整往返；9217B 写被拒且错误为 EMSGSIZE。
func TestSizeLimit(t *testing.T) {
	echo, err := net.ListenPacket("udp", "127.0.0.1:19532")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 70000)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			echo.WriteTo(buf[:n], from)
		}
	}()

	snd, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer snd.Close()

	// 上限值：完整往返，一次 ReadFrom 收全。
	payload := make([]byte, 9216-4)
	snd.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := snd.WriteTo(encode(SeqPacket{Seq: 9216, Payload: payload}), echo.LocalAddr()); err != nil {
		t.Fatalf("9216B WriteTo = %v, want 成功", err)
	}
	buf := make([]byte, 70000)
	snd.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := snd.ReadFrom(buf)
	if err != nil || n != 9216 {
		t.Fatalf("9216B 回显 = (%d, %v), want (9216, nil)", n, err)
	}

	// 上限 +1：内核拒绝，*net.OpError 包裹 EMSGSIZE。
	payload2 := make([]byte, 9217-4)
	_, err = snd.WriteTo(encode(SeqPacket{Seq: 9217, Payload: payload2}), echo.LocalAddr())
	var opErr *net.OpError
	if !errors.As(err, &opErr) || !errors.Is(err, syscall.EMSGSIZE) {
		t.Fatalf("9217B WriteTo = %v, want *net.OpError(EMSGSIZE)", err)
	}
}
