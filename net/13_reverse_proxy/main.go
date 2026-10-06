// 项目 13：反向代理——手写迷你网关：轮询、被动健康检查与 XFF 链。
//
// 反向代理站在服务端前面，替后端集群接收请求：客户端 → 网关 → 后端。
// 本篇用 httputil.ReverseProxy 组装一个可用的小网关：
//
//	负载均衡：轮询（round-robin）在两个后端间均分流量
//	被动健康检查：转发失败（连接被拒等）的后端拉黑 1 秒，流量切到幸存者
//	X-Forwarded-For：网关把客户端地址追加进 XFF 头，后端可见完整代理链
//
// 帧外知识：Rewrite 钩子里选中的后端要"告诉"ErrorHandler（转发失败时
// 精准拉黑）——走 request context 传递，因为 ErrorHandler 拿到的是原始
// 入站请求，而两个钩子共享它的 context。
//
// 三个实验（网关 127.0.0.1:19597，后端临时端口，断言见 go test -race）：
//
//	第 1 步：1000 请求 → 两后端 50/50±5%
//	第 2 步：关闭后端 A → 1 秒内全部流量切到 B
//	第 3 步：两层网关串联 → 后端看到的 XFF 链长为 2
//
// 运行方式：go run .（起 A/B 后端与网关，自动跑一轮 200 请求的分布演示）。
package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const gatewayAddr = "127.0.0.1:19597"

// backend：一个可被拉黑的后端目标。
type backend struct {
	target    *url.URL
	name      string
	hits      atomic.Int64
	downUntil atomic.Int64 // 被动健康检查：失败后拉黑到该时刻（unix nano）
}

func (b *backend) available() bool { return time.Now().UnixNano() >= b.downUntil.Load() }

func (b *backend) markDown(d time.Duration) {
	b.downUntil.Store(time.Now().Add(d).UnixNano())
}

// Gateway：多后端轮询 + 被动健康检查的反向代理。
type Gateway struct {
	backends []*backend
	rr       atomic.Int64
	cooldown time.Duration
}

func NewGateway(cooldown time.Duration, targets ...string) (*Gateway, error) {
	g := &Gateway{cooldown: cooldown}
	for i, t := range targets {
		u, err := url.Parse(t)
		if err != nil {
			return nil, err
		}
		g.backends = append(g.backends, &backend{
			target: u,
			name:   fmt.Sprintf("backend-%c", 'A'+i),
		})
	}
	return g, nil
}

// pick：轮询选一个可用后端；全部拉黑时退化为轮询（给它们恢复机会）。
func (g *Gateway) pick() *backend {
	n := len(g.backends)
	for i := 0; i < n; i++ {
		b := g.backends[int(g.rr.Add(1))%n]
		if b.available() {
			return b
		}
	}
	return g.backends[int(g.rr.Add(1))%n]
}

// Handler：Rewrite 钩子选路 + SetXFF；ErrorHandler 拉黑失败后端。
func (g *Gateway) Handler() http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			b := g.pick()
			pr.SetURL(b.target)         // scheme/host 指向后端，路径保留
			pr.Out.Host = b.target.Host // Host 头同步
			// Rewrite 模式默认丢弃入站 XFF（防伪造：客户端可带假 XFF）。
			// 要传递代理链，必须按官方文档先拷贝入站链，SetXForwarded 再追加本跳。
			pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
			pr.SetXForwarded()                     // 追加客户端 IP 到 XFF，并设置 X-Forwarded-Host/Proto
			pr.Out.Header.Set("X-Backend", b.name) // 选路结果写入头，供 ErrorHandler 取用
			b.hits.Add(1)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if name := r.Header.Get("X-Backend"); name != "" {
				for _, b := range g.backends {
					if b.name == name {
						b.markDown(g.cooldown) // 被动健康检查：拉黑失败后端
						fmt.Printf("  [gateway] %s 转发失败（%v），拉黑 %s\n", name, err, g.cooldown)
						break
					}
				}
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

// startBackend：起一个回显自身名字与 XFF 链的后端，返回服务端（含 Addr）。
func startBackend(name string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		xff := r.Header.Get("X-Forwarded-For")
		w.Header().Set("X-Backend-Name", name)
		fmt.Fprintf(w, "%s|xff_entries=%d", name, len(splitXFF(xff)))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv.Addr = ln.Addr().String()
	go srv.Serve(ln)
	return srv
}

// splitXFF：按逗号拆 XFF 链并去空白。
func splitXFF(xff string) []string {
	var out []string
	for _, part := range splitAndTrim(xff) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitAndTrim(s string) []string { return strings.Split(s, ",") }

func trimSpace(s string) string { return strings.TrimSpace(s) }

func main() {
	a := startBackend("A")
	bSrv := startBackend("B")
	gw, err := NewGateway(1*time.Second, "http://"+a.Addr, "http://"+bSrv.Addr)
	if err != nil {
		fmt.Println(err)
		return
	}
	ln, err := net.Listen("tcp", gatewayAddr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	gwSrv := &http.Server{Handler: gw.Handler(), ReadHeaderTimeout: 2 * time.Second}
	go gwSrv.Serve(ln)
	time.Sleep(100 * time.Millisecond)

	fmt.Println("== 200 请求穿过网关（轮询两后端）==")
	c := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < 200; i++ {
		if resp, err := c.Get("http://" + gatewayAddr + "/"); err == nil {
			resp.Body.Close()
		}
	}
	for _, b := range gw.backends {
		fmt.Printf("  %s: %d 请求\n", b.name, b.hits.Load())
	}
	fmt.Println("（1000 请求分布断言、故障切换、XFF 链：见 go test -race）")
	gwSrv.Close()
	a.Close()
	bSrv.Close()
}
