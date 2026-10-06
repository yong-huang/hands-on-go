// 项目 8：单连接多路复用——请求 id 关联响应，量化队头阻塞。
//
// 项目 7 的两版协议都是一问一答：请求发一条、响应读一条，连接上同时
// 只挂着一个未决请求。本篇引入多路复用（multiplexing）：
//
//	客户端：每个请求带唯一 seq，发出后记进 pending 表，N 个请求同时在途；
//	        唯一的读循环收到响应按 seq 查表，唤醒对应等待者（乱序天然支持）
//	服务端：每请求一个 goroutine 处理，可注入 sleep 模拟慢请求，
//	        响应按完成顺序乱序返回
//
// 帧格式（头 7 字节 + 载荷）：[kind 1B][seq 4B][len 2B][payload]
//
// 三个实验（127.0.0.1:19571）：
//
//	第 1 步：100 并发请求经单连接乱序返回，按 seq 全部正确配对（守恒）
//	第 2 步：队头阻塞量化——seq=0 注入 200ms，多路复用 vs 停等的总耗时
//	第 3 步：断线不悬挂——连接关闭后全部 in-flight 请求立即得到错误
//
// 运行方式：go run .。
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const addr = "127.0.0.1:19571"

const (
	KindReq  byte = 0x01
	KindResp byte = 0x02
	HeadSize      = 7 // kind 1 + seq 4 + len 2
)

var ErrClosed = errors.New("connection closed")

// frameEncode：[kind 1B][seq 4B][len 2B][payload]
func frameEncode(kind byte, seq int32, payload string) []byte {
	b := make([]byte, HeadSize+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint32(b[1:], uint32(seq))
	binary.BigEndian.PutUint16(b[5:], uint16(len(payload)))
	copy(b[HeadSize:], payload)
	return b
}

// readFrame：从流里读一帧（半包由 ReadFull 自然处理）。
func readFrame(r io.Reader) (kind byte, seq int32, payload string, err error) {
	var head [HeadSize]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return
	}
	kind = head[0]
	seq = int32(binary.BigEndian.Uint32(head[1:]))
	n := int(binary.BigEndian.Uint16(head[5:]))
	body := make([]byte, n)
	if _, err = io.ReadFull(r, body); err != nil {
		return
	}
	return kind, seq, string(body), nil
}

// ---------- 服务端 ----------

// serveMux：每请求一个 goroutine；slowFor 按 payload 注入慢请求。
// 注意按 payload 而不是 seq：seq 由客户端并发分配，goroutine 编号与 seq
// 没有对应关系（开发实录：按 seq 判定曾让"慢的是谁"不可预期）。
func serveMux(conn net.Conn, slowFor func(payload string) time.Duration) {
	defer conn.Close()
	var wg sync.WaitGroup
	for {
		kind, seq, payload, err := readFrame(conn)
		if err != nil {
			break
		}
		if kind != KindReq {
			continue
		}
		wg.Add(1)
		go func(seq int32, payload string) {
			defer wg.Done()
			if d := slowFor(payload); d > 0 {
				time.Sleep(d)
			}
			resp := strings.ToUpper(payload)
			conn.Write(frameEncode(KindResp, seq, resp))
		}(seq, payload)
	}
	wg.Wait() // 半关闭后：处理完已接受的请求再退场
}

// ---------- 客户端：pending 表 + 读循环分发 ----------

type muxResult struct {
	payload string
	err     error
}

// MuxClient：单连接多路复用客户端。
type MuxClient struct {
	conn    net.Conn
	pending sync.Map // seq int32 -> chan muxResult
	nextSeq int32
	seqMu   sync.Mutex
	closed  chan struct{}
	once    sync.Once
}

func NewMuxClient(addr string) (*MuxClient, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	m := &MuxClient{conn: conn, closed: make(chan struct{})}
	go m.readLoop()
	return m, nil
}

// Call：发出请求并只等自己的响应（其余请求互不影响）。
func (m *MuxClient) Call(payload string) (string, error) {
	m.seqMu.Lock()
	seq := m.nextSeq
	m.nextSeq++
	m.seqMu.Unlock()

	ch := make(chan muxResult, 1)
	m.pending.Store(seq, ch)
	if _, err := m.conn.Write(frameEncode(KindReq, seq, payload)); err != nil {
		m.pending.Delete(seq)
		return "", err
	}
	select {
	case r := <-ch:
		return r.payload, r.err
	case <-m.closed:
		return "", ErrClosed
	}
}

// CallWithSentinel：同 Call，但请求帧写进连接后触发 sentinel.Done
// （实验 3 用来确认"50 个请求全部在途"）。
func (m *MuxClient) CallWithSentinel(payload string, sentinel *sync.WaitGroup) (string, error) {
	m.seqMu.Lock()
	seq := m.nextSeq
	m.nextSeq++
	m.seqMu.Unlock()

	ch := make(chan muxResult, 1)
	m.pending.Store(seq, ch)
	_, err := m.conn.Write(frameEncode(KindReq, seq, payload))
	sentinel.Done()
	if err != nil {
		m.pending.Delete(seq)
		return "", err
	}
	select {
	case r := <-ch:
		return r.payload, r.err
	case <-m.closed:
		return "", ErrClosed
	}
}

// readLoop：唯一的读 goroutine——响应按完成顺序到达，按 seq 分发即乱序支持。
func (m *MuxClient) readLoop() {
	var acc []byte
	buf := make([]byte, 65536)
	for {
		n, err := m.conn.Read(buf)
		if err != nil {
			m.failAll(err)
			return
		}
		acc = append(acc, buf[:n]...)
		for len(acc) >= HeadSize {
			plen := int(binary.BigEndian.Uint16(acc[5:HeadSize]))
			if len(acc) < HeadSize+plen {
				break // 半包：留在累积缓冲等下一次 Read
			}
			frame := acc[:HeadSize+plen]
			acc = acc[HeadSize+plen:]
			kind, seq, payload, derr := frameOf(frame)
			if derr != nil || kind != KindResp {
				continue
			}
			if ch, ok := m.pending.Load(seq); ok {
				ch.(chan muxResult) <- muxResult{payload: payload}
				m.pending.Delete(seq)
			}
		}
	}
}

// frameOf：完整帧 → (kind, seq, payload)。
func frameOf(b []byte) (byte, int32, string, error) {
	if len(b) < HeadSize {
		return 0, 0, "", io.ErrShortBuffer
	}
	return b[0], int32(binary.BigEndian.Uint32(b[1:])), string(b[HeadSize:]), nil
}

// failAll：连接断了——所有 in-flight 请求立即得到错误（不悬挂）。
func (m *MuxClient) failAll(err error) {
	m.once.Do(func() {
		m.pending.Range(func(k, v any) bool {
			v.(chan muxResult) <- muxResult{err: ErrClosed}
			m.pending.Delete(k)
			return true
		})
		close(m.closed)
	})
}

func (m *MuxClient) Close() error { return m.conn.Close() }

// ---------- 实验编排 ----------

// serverCtl：服务端注入策略的进程级旋钮（accept 时的 snapshot）。
var serverCtl = struct {
	mu   sync.Mutex
	slow func(string) time.Duration
}{slow: func(string) time.Duration { return 0 }}

func setSlow(f func(string) time.Duration) {
	serverCtl.mu.Lock()
	serverCtl.slow = f
	serverCtl.mu.Unlock()
}

// stats：一次并发实验的统计。
type stats struct {
	total, hits int
	outOfOrder  int
	elapsed     time.Duration
	respSeqs    []int32
}

func main() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			serverCtl.mu.Lock()
			slow := serverCtl.slow
			serverCtl.mu.Unlock()
			go serveMux(c, slow)
		}
	}()

	fmt.Println("== 第 1 步：100 并发请求经单连接乱序返回，按 seq 全部正确配对 ==")
	setSlow(nil2)
	r1 := runConcurrent(100)
	fmt.Printf("  配对 %d/%d，耗时 %v，乱序返回 %d 次\n",
		r1.hits, r1.total, r1.elapsed.Round(time.Millisecond), r1.outOfOrder)

	fmt.Println()
	fmt.Println("== 第 2 步：队头阻塞量化——慢请求（SLOW）注入 200ms，看快请求何时完成 ==")
	setSlow(slowSleeper("SLOW"))
	r2 := runConcurrent(100)
	fmt.Printf("  多路复用：总耗时 %v——99 个快请求毫秒级完成，SLOW 的 200ms 与它们并行\n",
		r2.elapsed.Round(time.Millisecond))
	sw, swLast := runStopWait(100)
	fmt.Printf("  停等对照：总耗时 %v，最后一条请求在 %v 才发出——被前面 99 次串行往返逐个压后\n",
		sw.Round(time.Millisecond), swLast.Round(time.Millisecond))
	fmt.Println("  ⇒ 若把 SLOW 换成一条大响应或网络拥塞，停等的逐个压后就是队头阻塞；")
	fmt.Println("     多路复用让快慢请求共享一条连接而不共享命运（HTTP/2 的核心动机）。")

	fmt.Println()
	fmt.Println("== 第 3 步：断线不悬挂——关闭后 in-flight 全部立即报错 ==")
	setSlow(nil2)
	m, _ := NewMuxClient(addr)
	var wg, sent sync.WaitGroup
	sent.Add(50)
	firstErr := make(chan time.Duration, 1)
	start := time.Now()
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.CallWithSentinel("orphan", &sent); errors.Is(err, ErrClosed) {
				select {
				case firstErr <- time.Since(start):
				default:
				}
			}
		}()
	}
	sent.Wait() // 50 个请求全部写入连接（真正 in-flight）
	m.Close()
	wg.Wait()
	select {
	case d := <-firstErr:
		fmt.Printf("  50 个 in-flight 请求在关闭后 %v 内全部收到 ErrClosed\n", d.Round(time.Millisecond))
	default:
		fmt.Println("  未观察到 ErrClosed（不应发生）")
	}
}

func nil2(string) time.Duration { return 0 }

// slowSleeper：对指定 payload 的请求注入 200ms（实验 2 的"那一个慢请求"）。
func slowSleeper(target string) func(string) time.Duration {
	return func(p string) time.Duration {
		if p == target {
			return 200 * time.Millisecond
		}
		return 0
	}
}

// runConcurrent：total 个请求并发发出，统计配对与乱序。
func runConcurrent(total int) stats {
	m, err := NewMuxClient(addr)
	if err != nil {
		return stats{total: total}
	}
	defer m.Close()

	st := stats{total: total}
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := m.Call(fmt.Sprintf("req-%d", i))
			mu.Lock()
			defer mu.Unlock()
			if err == nil && resp == strings.ToUpper(fmt.Sprintf("req-%d", i)) {
				st.hits++
				if len(st.respSeqs) > 0 && st.respSeqs[len(st.respSeqs)-1] > int32(i) {
					st.outOfOrder++
				}
				st.respSeqs = append(st.respSeqs, int32(i))
			}
		}(i)
	}
	wg.Wait()
	st.elapsed = time.Since(start)
	return st
}

// runStopWait：停等对照——同流量下发一条等一条。返回（总耗时, 最后一条
// 请求的发出时刻）——停等的队头阻塞就体现在它被前面 99 次串行往返压后。
func runStopWait(total int) (elapsed, lastSent time.Duration) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return 0, 0
	}
	defer conn.Close()
	start := time.Now()
	for i := 0; i < total; i++ {
		conn.Write(frameEncode(KindReq, int32(i), fmt.Sprintf("req-%d", i)))
		lastSent = time.Since(start)
		_, _, _, err := readFrame(conn)
		if err != nil {
			break
		}
	}
	return time.Since(start), lastSent
}
