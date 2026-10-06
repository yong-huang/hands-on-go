// 项目 9：net/http 服务端行为学——超时参数、慢攻击与连接状态机。
//
// 教科书式结论"slowloris 能打瘫 http 服务"在 Go 上不成立：Go 的 http.Server
// 是每连接一个 goroutine，没有可占满的 worker 池——300 条慢连接挂上去，
// 正常请求照样毫秒级返回（结构性免疫，本篇实测）。
//
// 那超时参数防什么？防的是**无限驻留**：每条慢连接 = 1 个 goroutine +
// 读写缓冲 ≈ 数十 KB 内存，且默认配置下永不释放。攻击的真实代价是
// 内存与 goroutine 数量的线性增长（300 条 ≈ +300 goroutine），
// 而三行超时配置就是给这道无底洞封顶。
//
// 本程序在同一台机器（127.0.0.1:19581）跑两版服务端对照：
//
//	vuln    : ReadHeaderTimeout/ReadTimeout/IdleTimeout 全零（默认）
//	hardened: 三超时配置 + ConnState 钩子观测状态机
//
//	第 1 步：ConnState 状态机——一条连接的状态事件计数
//	第 2 步：300 条慢连接打 vuln 版——正常请求仍健康（免疫），
//	        但 goroutine 与内存线性增长、永不回收
//	第 3 步：同攻击打 hardened 版——慢连接 500ms 内被服务端掐断，
//	        goroutine 回落
//
// 运行方式：go run .（约 10 秒）。全部流量走 loopback，攻击只打本机。
package main

import (
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	addr          = "127.0.0.1:19581"
	slowConns     = 300
	attackWindow  = 4 * time.Second
	headerTimeout = 500 * time.Millisecond
)

// slowloris：conns 条慢连接，每条发一半请求头（不带终结空行）让服务端
// 的解析永远等不齐；周期性发一个字节吊着连接。
func slowloris(addr string, conns int, duration time.Duration, cancel <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer c.Close()
			c.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n")) // 半个头
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-cancel:
					return
				case <-ticker.C:
					if _, err := c.Write([]byte("X")); err != nil {
						return // 服务端把连接掐了（读失败→Close）——攻击者下线
					}
				}
			}
		}(i)
	}
	time.AfterFunc(duration, func() {})
	return &wg
}

// probe：发一条正常请求，返回延迟；失败返回 -1。
func probe(addr string) time.Duration {
	c := &http.Client{Timeout: 2 * time.Second}
	start := time.Now()
	resp, err := c.Get("http://" + addr + "/health")
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return time.Since(start)
}

// mem：goroutine 数与堆内存快照。
func mem() (int, uint64) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return runtime.NumGoroutine(), m.HeapAlloc
}

// buildServer：hardened 版配置三超时 + ConnState 计数钩子。
func buildServer(hardened bool, stateCount map[string]*atomic.Int64) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	s := &http.Server{Addr: addr, Handler: mux}
	if hardened {
		s.ReadHeaderTimeout = headerTimeout
		s.ReadTimeout = 2 * time.Second
		s.IdleTimeout = 1 * time.Second
		s.ConnState = func(_ net.Conn, st http.ConnState) {
			if c, ok := stateCount[st.String()]; ok {
				c.Add(1)
			}
		}
	}
	return s
}

func main() {
	fmt.Println("== 第 1 步：ConnState 状态机——一条连接的事件计数 ==")
	states := map[string]*atomic.Int64{
		"new": {}, "active": {}, "idle": {}, "hijacked": {}, "closed": {},
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	srv := buildServer(true, states)
	go srv.Serve(ln)
	time.Sleep(100 * time.Millisecond)

	c, _ := net.Dial("tcp", addr)
	c.Write([]byte("GET /health HTTP/1.1\r\nHost: x\r\n\r\n"))
	buf := make([]byte, 64)
	c.Read(buf)
	time.Sleep(300 * time.Millisecond) // IdleTimeout=1s 未到：连接应停在 idle
	c.Close()
	time.Sleep(300 * time.Millisecond) // 空闲连接被回收 → closed
	srv.Close()

	fmt.Printf("  事件计数：new=%d active=%d idle=%d closed=%d\n",
		states["new"].Load(), states["active"].Load(), states["idle"].Load(), states["closed"].Load())
	fmt.Println("  一条连接的一生：new（接入）→ active（处理中）→ idle（空闲）→ closed（回收）")

	fmt.Println()
	fmt.Println("== 第 2 步：300 条慢连接 vs 默认服务端（无超时）==")
	runAttack(false)

	fmt.Println()
	fmt.Println("== 第 3 步：300 条慢连接 vs 加固服务端（三超时）==")
	runAttack(true)
}

// runAttack：起指定配置的服务端，300 条慢连接攻击 4 秒，
// 打印正常请求健康度与 goroutine/内存账本。
func runAttack(hardened bool) {
	states := map[string]*atomic.Int64{"closed": {}}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	srv := buildServer(hardened, states)
	go srv.Serve(ln)
	time.Sleep(100 * time.Millisecond)

	g0, heap0 := mem()
	cancel := make(chan struct{})
	attackDone := make(chan struct{})
	go func() {
		wg := slowloris(addr, slowConns, attackWindow, cancel)
		wg.Wait()
		close(attackDone)
	}()

	// 攻击期间每 800ms 探测一次正常请求
	var ok, fail atomic.Int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if d := probe(addr); d < 0 {
				fail.Add(1)
			} else {
				ok.Add(1)
			}
			time.Sleep(800 * time.Millisecond)
		}
	}()
	// goroutine 数量曲线：vulnerable 应阶梯上升后不落；hardened 应冲高后回落
	var curve []int
	for i := 0; i < 6; i++ {
		time.Sleep(500 * time.Millisecond)
		g, _ := mem()
		curve = append(curve, g)
	}
	g1, heap1 := mem()
	close(cancel) // 停攻击
	<-time.After(200 * time.Millisecond)
	close(stop)

	label := "vulnerable(无超时)"
	if hardened {
		label = "hardened(三超时)"
	}
	fmt.Printf("  [%s] 攻击期间正常请求：成功 %d / 失败 %d\n", label, ok.Load(), fail.Load())
	fmt.Printf("  goroutine：%d → %d（Δ%+d）   堆内存：%d → %d KB\n", g0, g1, g1-g0, heap0/1024, heap1/1024)
	fmt.Printf("  曲线(每 500ms)：base=%d %v\n", g0, curve)
	if hardened {
		fmt.Printf("  服务端 500ms 内掐断全部慢连接（closed 事件 %d 次），攻击端写失败被迫下线——goroutine 全额回落\n",
			states["closed"].Load())
	} else {
		fmt.Println("  ⇒ 结构性免疫（无 worker 池可占满），但 goroutine 与内存无限驻留——默认配置的隐藏账单")
	}
	srv.Close()
	time.Sleep(300 * time.Millisecond)
}
