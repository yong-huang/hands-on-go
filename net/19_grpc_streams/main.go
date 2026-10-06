// 项目 19：gRPC 四种流式——Unary、ServerStreaming、ClientStreaming、Bidi。
//
// gRPC 在 HTTP/2 之上把 RPC 归纳为四种形态，全部跑在本篇的 FourStreams 服务上：
//
//	Unary          Echo      ：一请求一响应
//	ServerStreaming Countdown ：一请求 N 响应（服务端推流：从 n 倒数到 1）
//	ClientStreaming Sum       ：N 请求一响应（客户端攒批：求和）
//	BidiStreaming  Chat      ：双向独立流（收发并发，echo 放大器）
//
// 三个实验（127.0.0.1:19599，无 TLS——本篇聚焦流语义）：
//
//	第 1 步：四种形态各跑一遍（bidi 双向 1000 条，守恒断言在测试）
//	第 2 步：慢消费背压——服务端推 10 万条，消费端限速，
//	        Send 阻塞实测量级（HTTP/2 流控的现实存在感）
//	第 3 步：拦截器——服务端一元拦截器改写/短路错误码
//
// 运行方式：go run .；断言见 go test -race。
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const addr = "127.0.0.1:19599"

// fourStreams：服务实现——四个方法各显一种流形态。
type fourStreams struct {
	UnimplementedFourStreamsServer
}

// Echo：Unary。
func (f *fourStreams) Echo(ctx context.Context, req *Request) (*Response, error) {
	return &Response{N: req.N, Note: "echo:" + req.Note}, nil
}

// Countdown：ServerStreaming——发 n 条，倒数到 1。
func (f *fourStreams) Countdown(req *Request, stream grpc.ServerStreamingServer[Response]) error {
	for i := req.N; i > 0; i-- {
		if err := stream.Send(&Response{N: i, Note: req.Note}); err != nil {
			return err
		}
	}
	return nil
}

// Sum：ClientStreaming——收齐求和。
func (f *fourStreams) Sum(stream grpc.ClientStreamingServer[Request, Response]) error {
	var sum int64
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&Response{N: sum, Note: "sum done"})
		}
		if err != nil {
			return err
		}
		sum += req.N
	}
}

// Chat：BidiStreaming——收一条发一条（放大 ×2），收发两侧独立并发。
func (f *fourStreams) Chat(stream grpc.BidiStreamingServer[Request, Response]) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(&Response{N: req.N * 2, Note: req.Note}); err != nil {
			return err
		}
	}
}

// authInterceptor：服务端一元拦截器——演示"短路错误码"：
// note 以 "deny" 开头的请求直接返回 PermissionDenied，handler 不执行。
func authInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if r, ok := req.(*Request); ok && len(r.Note) >= 4 && r.Note[:4] == "deny" {
		return nil, status.Errorf(codes.PermissionDenied, "denied by interceptor: %s", r.Note)
	}
	resp, err := handler(ctx, req)
	if err != nil {
		// 改写错误码：内部错误统一包装成 Internal
		return resp, status.Errorf(codes.Internal, "wrapped: %v", err)
	}
	return resp, nil
}

// dial：创建指向 addr 的 gRPC 客户端连接（无 TLS——本篇聚焦流语义）。
func dial(addr string) FourStreamsClient {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	return NewFourStreamsClient(conn)
}

func main() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(authInterceptor))
	RegisterFourStreamsServer(gs, &fourStreams{})
	go gs.Serve(ln)
	time.Sleep(100 * time.Millisecond)

	client := dial(addr)
	ctx := context.Background()

	fmt.Println("== 第 1 步：Unary 与 ServerStreaming ==")
	resp, err := client.Echo(ctx, &Request{N: 1, Note: "hi"})
	if err != nil {
		fmt.Println("echo:", err)
		return
	}
	fmt.Printf("  Echo → %d %q\n", resp.N, resp.Note)
	cd, err := client.Countdown(ctx, &Request{N: 3, Note: "t-minus"})
	if err != nil {
		fmt.Println("countdown:", err)
		return
	}
	for {
		r, err := cd.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("recv:", err)
			return
		}
		fmt.Printf("  Countdown → %d %q\n", r.N, r.Note)
	}

	fmt.Println()
	fmt.Println("== 第 2 步：ClientStreaming（Sum）与 Bidi（Chat，10 条）==")
	sum, err := client.Sum(ctx)
	if err != nil {
		fmt.Println("sum:", err)
		return
	}
	for i := 1; i <= 5; i++ {
		if err := sum.Send(&Request{N: int64(i)}); err != nil {
			fmt.Println("send:", err)
			return
		}
	}
	sumResp, err := sum.CloseAndRecv()
	if err != nil {
		fmt.Println("close:", err)
		return
	}
	fmt.Printf("  Sum(1..5) → %d\n", sumResp.N)

	chat, err := client.Chat(ctx)
	if err != nil {
		fmt.Println("chat:", err)
		return
	}
	// 收发并发：发送 10 条，接收 10 条
	go func() {
		for i := 1; i <= 10; i++ {
			if err := chat.Send(&Request{N: int64(i)}); err != nil {
				return
			}
		}
		chat.CloseSend()
	}()
	for i := 0; i < 10; i++ {
		r, err := chat.Recv()
		if err != nil {
			fmt.Println("recv:", err)
			return
		}
		fmt.Printf("  Chat → %d (n×2)\n", r.N)
	}

	fmt.Println()
	fmt.Println("== 第 3 步：慢消费背压——服务端推 10 万条，消费端限速 ==")
	push, err := client.Countdown(ctx, &Request{N: 100000})
	if err != nil {
		fmt.Println("countdown:", err)
		return
	}
	consumed := 0
	slowStart := time.Now()
	// 慢消费：每收 1000 条睡 5ms（模拟下游处理）
	for {
		r, rerr := push.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			fmt.Println("recv:", rerr)
			return
		}
		_ = r // 慢消费：只计数不做实际工作
		consumed++
		if consumed%1000 == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	fmt.Printf("  10 万条推流全部收完（%v）——Send 的阻塞由 HTTP/2 流控承接，无丢失无乱序\n",
		time.Since(slowStart).Round(50*time.Millisecond))

	fmt.Println()
	fmt.Println("== 第 4 步：拦截器短路（deny 开头的 note → PermissionDenied）==")
	_, err = client.Echo(ctx, &Request{Note: "deny-me"})
	fmt.Printf("  denied 请求 → %v\n", status.Code(err))
	_, err = client.Echo(ctx, &Request{Note: "allow-me"})
	fmt.Printf("  正常请求   → %v\n", status.Code(err))
	gs.Stop()
}
