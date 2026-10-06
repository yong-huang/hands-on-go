// 项目 4：在 UDP 上手写停等协议——可靠传输的最小闭环。
//
// 协议（应用层，2 种消息）：
//
//	DATA: [0][seq 4B][payload]   发送方：每条消息等 ACK(seq)，超时则原样重发
//	ACK : [1][seq 4B]            接收方：收到 DATA 就回 ACK(seq)；只交付
//	                             expected 序号，重复 DATA 重 ACK 但不重交付
//
// 停等 = 发一条、等一条：发送窗口为 1，因此接收端只需要一个 expected
// 计数器就能去重——这是所有可靠协议（TCP/QUIC）的种子形态，后续演化
// 只是"窗口变大 + 乱序重排"。
//
// 拓扑：sender → 双向有损 NAT（丢 30%，DATA 与 ACK 都过损链路）→ receiver。
// 两个来自真实世界的协议课都会在本篇出现：
//  1. 接收方交付完不能立刻关连接——最后的 ACK 可能丢，必须 linger 补 ACK
//     （TCP TIME_WAIT 的存在理由，在停等协议里亲眼看到）
//  2. 重复 DATA 不是错误，是 ACK 丢失的正常代价——去重靠 expected 序号
//
// 运行方式：go run .（200 条消息，约 5~10 秒）。
package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

const (
	proxyAddr  = "127.0.0.1:19541" // 双向有损 NAT
	serverAddr = "127.0.0.1:19542" // 接收端固定地址
	totalMsgs  = 200
	timeout    = 30 * time.Millisecond // ACK 等待超时
	maxRetries = 100
	lossRate   = 0.30
)

const (
	kindData = 0
	kindAck  = 1
)

func encodeMsg(kind byte, seq int32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(seq))
	copy(b[5:], payload)
	return b
}

func decodeMsg(b []byte) (kind byte, seq int32, payload []byte) {
	return b[0], int32(binary.BigEndian.Uint32(b[1:])), b[5:]
}

// ScenarioResult：一次停等传输的完整统计。
type ScenarioResult struct {
	Tx, Retrans  int     // 总发送次数 / 超时重传次数
	AppDelivered int64   // 应用层交付计数（应等于消息数）
	Dups         int     // 收到的重复 DATA 条数（被去重）
	Seqs         []int32 // 应用层实际交付的序号序列（应恰为 0..n-1）
}

func main() {
	r, err := runScenario(totalMsgs, lossRate, time.Now().UnixNano())
	if err != nil {
		fmt.Println("场景失败:", err)
		return
	}
	fmt.Println("== 停等协议穿越 30% 双向丢包链路 ==")
	fmt.Printf("发送端：消息 %d 条，总发送 %d 次，超时重传 %d 次\n", totalMsgs, r.Tx, r.Retrans)
	fmt.Printf("接收端：应用层收到 %d 条（恰一次交付）；收到重复 DATA %d 条（已去重，只补 ACK）\n",
		r.AppDelivered, r.Dups)
	fmt.Println("⇒ 零丢失、零重复交付：可靠性是协议熬出来的，不是链路给的。")
}

// runScenario：搭一条 client→有损 NAT→receiver 管道并跑完一次停等传输。
func runScenario(total int, loss float64, seed int64) (r ScenarioResult, err error) {
	stopNAT := newLossyNAT(proxyAddr, serverAddr, loss, seed)
	defer stopNAT()
	time.Sleep(50 * time.Millisecond)

	// 接收端：固定地址；应用层回调记账并留交付序号序列
	var mu sync.Mutex
	recvDone := make(chan error, 1)
	go func() {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19542})
		if err != nil {
			recvDone <- err
			return
		}
		defer conn.Close()
		_, u, e := RecvReliable(conn, total, func(seq int32, _ []byte) {
			mu.Lock()
			r.AppDelivered++
			r.Seqs = append(r.Seqs, seq)
			mu.Unlock()
		})
		mu.Lock()
		r.Dups = u
		mu.Unlock()
		recvDone <- e
	}()
	time.Sleep(50 * time.Millisecond)

	snd, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19541})
	if err != nil {
		return r, err
	}
	defer snd.Close()

	msgs := make([][]byte, total)
	for i := range msgs {
		msgs[i] = []byte(fmt.Sprintf("message-%03d", i))
	}
	r.Tx, r.Retrans, err = SendReliable(snd, msgs, timeout, maxRetries)
	if err != nil {
		return r, err
	}
	if err := <-recvDone; err != nil {
		return r, err
	}
	mu.Lock()
	r.AppDelivered = int64(len(r.Seqs))
	mu.Unlock()
	return r, nil
}

// SendReliable：停等发送端。逐条发送，等 ACK(seq)；超时重发同一条。
// 返回总发送次数与重传次数（总发送 − 消息数）。
func SendReliable(conn *net.UDPConn, msgs [][]byte, ackTimeout time.Duration, retries int) (tx, retrans int, err error) {
	buf := make([]byte, 1500)
	for seq, msg := range msgs {
		ok := false
		for attempt := 0; attempt <= retries; attempt++ {
			conn.Write(encodeMsg(kindData, int32(seq), msg))
			tx++
			if attempt > 0 {
				retrans++
			}
			conn.SetReadDeadline(time.Now().Add(ackTimeout))
			n, rerr := conn.Read(buf)
			if rerr != nil { // 超时（或暂时出错）：重发
				if ne, okErr := rerr.(net.Error); okErr && ne.Timeout() {
					continue
				}
				return tx, retrans, rerr
			}
			kind, ackSeq, _ := decodeMsg(buf[:n])
			if kind == kindAck && ackSeq == int32(seq) {
				ok = true // 收到本条 ACK，下一条
				break
			}
			// 旧 ACK/错序 ACK：停等语义下直接当超时处理，继续等
			attempt--
			time.Sleep(time.Millisecond)
		}
		if !ok {
			return tx, retrans, fmt.Errorf("seq %d 重试 %d 次仍未收到 ACK", seq, retries)
		}
	}
	return tx, retrans, nil
}

// linger：全部交付后的收尾观察期。最后一条 ACK 可能丢失，发送方会重发
// 重复 DATA——接收方必须留在原地补 ACK，否则对方在无限重传。
// 这就是 TCP TIME_WAIT 的存在理由，在停等协议里亲眼看到它。
const linger = 800 * time.Millisecond

// RecvReliable：停等接收端。只交付 expected 序号；重复 DATA 重 ACK 不重交付。
// deliver 回调即"应用层"，每条消息恰好调用一次。
func RecvReliable(conn *net.UDPConn, total int, deliver func(seq int32, payload []byte)) (delivered, dups int, err error) {
	var expected int32
	buf := make([]byte, 1500)
	for delivered < total {
		n, peer, rerr := conn.ReadFromUDP(buf)
		if rerr != nil {
			return delivered, dups, rerr
		}
		kind, seq, payload := decodeMsg(buf[:n])
		if kind != kindData {
			continue
		}
		// 无论新旧，都回 ACK：重复 DATA 的重传方正等着这个 ACK 收工
		if _, werr := conn.WriteToUDP(encodeMsg(kindAck, seq, nil), peer); werr != nil {
			return delivered, dups, werr
		}
		switch {
		case seq == expected:
			deliver(seq, payload) // 应用层恰好一次
			delivered++
			expected++
		case seq < expected:
			dups++ // 重复 DATA 被序号去重：只补 ACK，不调 deliver
		default:
			// 停等语义下不该出现未来序号（窗口为 1）；记录而不交付
		}
	}
	// linger：交付完不立刻走，补发丢失的最后一个 ACK
	conn.SetReadDeadline(time.Now().Add(linger))
	for {
		n, peer, rerr := conn.ReadFromUDP(buf)
		if rerr != nil {
			break // 超时：对端已确认，安心退场
		}
		kind, seq, _ := decodeMsg(buf[:n])
		if kind == kindData {
			conn.WriteToUDP(encodeMsg(kindAck, seq, nil), peer)
			if seq < expected {
				dups++
			}
		}
	}
	return delivered, dups, nil
}

// newLossyNAT：双向有损代理。a 侧监听固定端口（发送方 dial 它），b 侧
// 连到接收端固定地址；两侧每个数据报独立掷骰子，丢了就无声无息。
// 两个方向各有独立的随机源——共享一个 rand.Rand 会构成数据竞争。
func newLossyNAT(aAddr, bAddr string, loss float64, seed int64) (stop func()) {
	stopCh := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }
	rndA := rand.New(rand.NewSource(seed))
	rndB := rand.New(rand.NewSource(seed + 1))

	a, err := net.ListenPacket("udp", aAddr)
	if err != nil {
		fmt.Println("nat listen:", err)
		return func() {}
	}
	b, err := net.Dial("udp", bAddr)
	if err != nil {
		a.Close()
		fmt.Println("nat dial:", err)
		return func() {}
	}
	go func() {
		<-stopCh
		a.Close()
		b.Close()
	}()

	// 两个方向各一条读循环，lastClient 记录发送方的最新地址（NAT 表的种子）。
	var mu sync.Mutex
	var lastClient net.Addr
	// a 侧：客户端的 DATA → 转发给 server
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := a.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			lastClient = from
			mu.Unlock()
			if rndA.Float64() < loss {
				continue // 静默丢弃：DATA 与 ACK 一视同仁
			}
			b.Write(buf[:n])
		}
	}()
	// b 侧：接收端的 ACK → 转发给最近一次的客户端
	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := b.Read(buf)
			if err != nil {
				return
			}
			if rndB.Float64() < loss {
				continue
			}
			mu.Lock()
			peer := lastClient
			mu.Unlock()
			if peer != nil {
				a.WriteTo(buf[:n], peer)
			}
		}
	}()
	return stop
}
