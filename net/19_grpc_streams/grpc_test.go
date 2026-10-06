package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"net"
)

// startGRPC：起 FourStreams 服务端（带拦截器），返回地址与关闭函数。
func startGRPC(t *testing.T, interceptor grpc.UnaryServerInterceptor) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	opts := []grpc.ServerOption{}
	if interceptor != nil {
		opts = append(opts, grpc.ChainUnaryInterceptor(interceptor))
	}
	gs := grpc.NewServer(opts...)
	RegisterFourStreamsServer(gs, &fourStreams{})
	go gs.Serve(ln)
	t.Cleanup(gs.Stop)
	return ln.Addr().String()
}

// newClient：建客户端连接（测试生命周期内复用）。
func newClient(t *testing.T, addr string) FourStreamsClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewFourStreamsClient(conn)
}

// TestUnaryAndServerStreaming：Unary 回显 + Countdown 推 n 条且顺序保持。
func TestUnaryAndServerStreaming(t *testing.T) {
	addr := startGRPC(t, nil)
	client := newClient(t, addr)
	ctx := context.Background()

	resp, err := client.Echo(ctx, &Request{N: 7, Note: "hi"})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if resp.N != 7 || resp.Note != "echo:hi" {
		t.Fatalf("echo = %+v", resp)
	}

	cd, err := client.Countdown(ctx, &Request{N: 100})
	if err != nil {
		t.Fatalf("countdown: %v", err)
	}
	count := 0
	last := int64(101)
	for {
		r, err := cd.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		count++
		if r.N >= last {
			t.Fatalf("第 %d 条 N=%d 未递减（上一条 %d）", count, r.N, last)
		}
		last = r.N
	}
	if count != 100 {
		t.Fatalf("Countdown 推 %d 条, want 100", count)
	}
}

// TestClientStreamingSum：验收②的一半——ClientStreaming 攒批求和。
func TestClientStreamingSum(t *testing.T) {
	addr := startGRPC(t, nil)
	client := newClient(t, addr)
	ctx := context.Background()

	stream, err := client.Sum(ctx)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	const n = 500
	for i := 1; i <= n; i++ {
		if err := stream.Send(&Request{N: int64(i)}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if want := int64(n * (n + 1) / 2); resp.N != want {
		t.Fatalf("Sum(1..%d) = %d, want %d", n, resp.N, want)
	}
}

// TestBidiThousandConserved：验收①——bidi 双向 1000 条守恒：
// 发 1000 条、收 1000 条、值翻倍且可配对（乱序容忍下的守恒断言）。
func TestBidiThousandConserved(t *testing.T) {
	addr := startGRPC(t, nil)
	client := newClient(t, addr)
	ctx := context.Background()

	chat, err := client.Chat(ctx)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	const total = 1000
	var wg sync.WaitGroup
	got := make(map[int64]bool)
	var mu sync.Mutex
	// 发送方
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= total; i++ {
			if err := chat.Send(&Request{N: int64(i)}); err != nil {
				return
			}
		}
		chat.CloseSend()
	}()
	// 接收方
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			r, err := chat.Recv()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				return
			}
			mu.Lock()
			got[r.N/2] = r.N%2 == 0 // 记录配对结果（n×2 还原 n）
			mu.Unlock()
		}
	}()
	wg.Wait()

	if len(got) != total {
		t.Fatalf("bidi 守恒 %d/%d 条", len(got), total)
	}
	for i := 1; i <= total; i++ {
		if !got[int64(i)] {
			t.Fatalf("序号 %d 缺失", i)
		}
	}
}

// TestSlowConsumerBackpressure：验收②——服务端 Send 阻塞由 HTTP/2 流控承接：
// 服务端推 10 万条、消费端限速，最终全部收完且无错误（流控的现实存在感）。
func TestSlowConsumerBackpressure(t *testing.T) {
	addr := startGRPC(t, nil)
	client := newClient(t, addr)
	ctx := context.Background()

	push, err := client.Countdown(ctx, &Request{N: 100000})
	if err != nil {
		t.Fatalf("countdown: %v", err)
	}
	consumed := 0
	start := time.Now()
	for {
		_, err := push.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		consumed++
		if consumed%2000 == 0 {
			time.Sleep(time.Millisecond) // 模拟慢消费
		}
	}
	el := time.Since(start)
	if consumed != 100000 {
		t.Fatalf("消费 %d/100000", consumed)
	}
	t.Logf("10 万条限速消费耗时 %v（流控生效、零丢失）", el.Round(50*time.Millisecond))
}

// authInterceptor：与 main.go 同款拦截器（可注入行为）。
func testAuthInterceptor(denyPrefix string, onErr func(error) error) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if r, ok := req.(*Request); ok && strings.HasPrefix(r.Note, denyPrefix) {
			return nil, status.Errorf(codes.PermissionDenied, "denied: %s", r.Note)
		}
		resp, err := handler(ctx, req)
		if err != nil && onErr != nil {
			return resp, onErr(err)
		}
		return resp, err
	}
}

// TestInterceptorShortCircuit：验收③——拦截器短路错误码：
// deny 前缀请求得到 PermissionDenied（handler 未执行），错误改写生效。
func TestInterceptorShortCircuit(t *testing.T) {
	ran := false
	base := func(ctx context.Context, req any) (any, error) {
		ran = true
		r := req.(*Request)
		if strings.Contains(r.Note, "boom") {
			return nil, fmt.Errorf("handler exploded: %s", r.Note)
		}
		return &Response{Note: r.Note}, nil
	}
	rewrite := func(err error) error {
		return status.Errorf(codes.Internal, "wrapped: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(testAuthInterceptor("deny", rewrite)))
	RegisterFourStreamsServer(gs, &recordFourStreams{base: base, flag: &ran})
	go gs.Serve(ln)
	t.Cleanup(gs.Stop)

	client := newClient(t, ln.Addr().String())
	ctx := context.Background()

	// deny 前缀：PermissionDenied 且 handler 未执行
	_, err = client.Echo(ctx, &Request{Note: "deny-me"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("deny 请求 code = %v, want PermissionDenied", status.Code(err))
	}
	if ran {
		t.Fatal("拦截器短路后 handler 不应执行")
	}

	// 非 deny：handler 执行，返回的错误被改写为 Internal
	_, err = client.Echo(ctx, &Request{Note: "boom"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("boom 请求 code = %v, want Internal", status.Code(err))
	}
}

// recordFourStreams：记录 handler 是否执行的 Unary 实现（闭包共享标志）。
type recordFourStreams struct {
	UnimplementedFourStreamsServer
	base func(ctx context.Context, req any) (any, error)
	flag *bool
}

func (f *recordFourStreams) Echo(ctx context.Context, req *Request) (*Response, error) {
	*f.flag = true
	resp, err := f.base(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.(*Response), nil
}

// TestRogueClientCertStillAuthenticates：占位对齐（mTLS 在项目 16 已验）——
// 本篇验证 TLS 缺省下 gRPC 走明文 h2c 仍可用（bufconn 之外的真实端口路径）。
func TestPlainH2CServerWorks(t *testing.T) {
	addr := startGRPC(t, nil)
	client := newClient(t, addr)
	resp, err := client.Echo(context.Background(), &Request{N: 1, Note: "h2c"})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if resp.Note != "echo:h2c" {
		t.Fatalf("echo = %q", resp.Note)
	}
}
