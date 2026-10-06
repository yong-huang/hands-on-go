package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Registry：后端注册表——按名字增删，查询返回地址列表。
type Registry struct {
	mu       sync.Mutex
	backends map[string]string // name → addr
}

func NewRegistry() *Registry {
	return &Registry{backends: map[string]string{}}
}

func (r *Registry) Register(name, addr string) {
	r.mu.Lock()
	r.backends[name] = addr
	r.mu.Unlock()
}

func (r *Registry) Addresses() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.backends))
	for _, a := range r.backends {
		out = append(out, a)
	}
	return out
}

// Handler：业务回调——payload 进、payload 出。
type Handler func(payload []byte) ([]byte, error)

// Server：mTLS RPC 服务端——每连接一个 goroutine，每请求一个 goroutine。
type Server struct {
	handler Handler
	ln      net.Listener
	wg      sync.WaitGroup
	mu      sync.Mutex
	conns   map[net.Conn]bool
	closed  bool
}

func NewServer(addr string, tlsCfg *tls.Config, handler Handler) (*Server, error) {
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	return &Server{handler: handler, ln: ln, conns: make(map[net.Conn]bool)}, nil
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve：接受连接直到 Close。
func (s *Server) Serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					fmt.Fprintf(ioDiscard, "serveConn panic: %v\n", rec)
				}
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				conn.Close()
			}()
			serveConn(conn, s.handler)
		}()
	}
}

// Close：优雅停机。
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.ln.Close()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// serveConn：读循环——每帧一个请求，goroutine 处理并回写响应。
func serveConn(conn net.Conn, handler Handler) {
	r := bufio.NewReader(conn)
	for {
		kind, reqID, payload, err := readFrame(r)
		if err != nil {
			return
		}
		if kind != KindReq {
			continue
		}
		go func(reqID uint64, payload []byte) {
			defer func() {
				if rec := recover(); rec != nil {
					errPayload := []byte(fmt.Sprintf("ERR: panic: %v", rec))
					conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
					conn.Write(frameEncode(KindResp, reqID, errPayload))
				}
			}()
			resp, err := handler(payload)
			if err != nil {
				resp = []byte("ERR:" + err.Error())
			}
			conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
			conn.Write(frameEncode(KindResp, reqID, resp))
		}(reqID, payload)
	}
}

// ---------- 客户端 ----------

// pending：一次在途请求的完成通知。
type pending struct {
	ch chan pendingResult
}

type pendingResult struct {
	payload []byte
	err     error
}

// Client：mTLS 多路复用客户端——pending 表 + 唯一读循环。
type Client struct {
	conn    net.Conn
	pending sync.Map // reqID → chan pendingResult
	nextSeq atomic.Uint64
	closed  chan struct{}
	once    sync.Once
}

func NewClient(addr string, tlsCfg *tls.Config) (*Client, error) {
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConnFail, err)
	}
	c := &Client{conn: conn, closed: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Call：同步调用——发送请求并等待同 reqID 的响应。
func (c *Client) Call(ctx context.Context, payload []byte, timeout time.Duration) ([]byte, error) {
	seq := c.nextSeq.Add(1)

	ch := make(chan pendingResult, 1)
	c.pending.Store(seq, ch)

	c.conn.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := c.conn.Write(frameEncode(KindReq, seq, payload)); err != nil {
		c.pending.Delete(seq)
		return nil, fmt.Errorf("%w: %v", ErrConnFail, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case pr := <-ch:
		return pr.payload, pr.err
	case <-ctx.Done():
		c.pending.Delete(seq)
		return nil, ctx.Err()
	case <-timer.C:
		c.pending.Delete(seq)
		return nil, fmt.Errorf("%w: no response in %v", ErrTimeout, timeout)
	}
}

// readLoop：唯一读 goroutine——解析帧按 reqID 分发到 pending 表。
func (c *Client) readLoop() {
	var acc []byte
	buf := make([]byte, 65536)
	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			c.failAll(err)
			return
		}
		acc = append(acc, buf[:n]...)
		for len(acc) >= HeadSize {
			plen := int(beUint32(acc[9:13]))
			if plen > MaxPayload || len(acc) < HeadSize+plen {
				break
			}
			frame := acc[:HeadSize+plen]
			acc = acc[HeadSize+plen:]
			kind, seq, payload, derr := decodeFrame(frame)
			if derr != nil || kind != KindResp {
				continue
			}
			if ch, ok := c.pending.Load(seq); ok {
				ch.(chan pendingResult) <- pendingResult{payload: payload}
				c.pending.Delete(seq)
			}
		}
	}
}

// failAll：连接断开——所有 in-flight 请求立即报错。
func (c *Client) failAll(err error) {
	c.once.Do(func() {
		c.pending.Range(func(k, v any) bool {
			v.(chan pendingResult) <- pendingResult{err: err}
			c.pending.Delete(k)
			return true
		})
		close(c.closed)
	})
}

// Close：关闭连接。
func (c *Client) Close() error { return c.conn.Close() }
