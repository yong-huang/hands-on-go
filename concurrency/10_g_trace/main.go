// 项目 10：G 状态机追踪——用 runtime/trace 录制混合负载，观察一个 G 的
// _Grunnable / _Grunning / _Gwaiting / _Gsyscall 四态流转。
//
// 运行：go run ./10_g_trace/
// 输出：当前目录 g_states.out（trace 文件），并打印 go tool trace 解读步骤。
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/trace"
	"sync"
	"sync/atomic"
	"time"
)

// sink 防止编译器把自旋循环整体优化掉（结果被最后打印）。
var sink atomic.Uint64

func main() {
	out := flag.String("out", "g_states.out", "trace 输出文件路径")
	flag.Parse()

	// 步骤 0：开启录制。在看什么：trace.Start 之后，每个 G 的
	// 创建/就绪/运行/阻塞/退出事件都会被写进这个文件。
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "创建 trace 文件失败:", err)
		os.Exit(1)
	}
	if err := trace.Start(f); err != nil {
		fmt.Fprintln(os.Stderr, "trace.Start 失败:", err)
		os.Exit(1)
	}

	t0 := time.Now()
	runWorkload()
	trace.Stop()
	wall := time.Since(t0)

	fmt.Printf("== 混合负载录制完成，墙钟耗时 %v ==\n", wall.Round(time.Millisecond))
	fmt.Printf("trace 文件: %s（%d 字节）\n", *out, mustSize(*out))
	fmt.Println("sink =", sink.Load(), "（自旋结果，证明循环没被优化掉）")
	fmt.Println()
	fmt.Println("解读步骤：")
	fmt.Printf("  1. go tool trace %s\n", *out)
	fmt.Println("  2. 浏览器打开后截取这些视图：")
	fmt.Println("     - view trace：主时间线，按 P 分行，看 G 的状态色条与就绪间隔")
	fmt.Println("     - goroutine analysis：按函数分组，点开单个 G 看运行/等待时长与原因")
	fmt.Println("     - user-defined tasks：应有 spin / pingpong / http 三个任务")
	fmt.Println("     - GC 视图：确认 runtime.GC() 对应的标记阶段与 STW（stop-the-world）")
	fmt.Println("  3. 在 view trace 里找一个跑 runOneHTTP 的 G：")
	fmt.Println("     就绪→运行→阻塞读网络→就绪→运行到结束，构成四次状态变化")
}

// runWorkload 依次跑三类负载，每类登记成一个用户任务，
// 让 trace viewer 的 task 视图能直接定位到这段区间。
func runWorkload() {
	// 负载 A：纯 CPU。在看什么：两个 G 长时间停在 _Grunning，
	// GOMAXPROCS 小于核数时能看到它们排队（_Grunnable）等待 P。
	ctxA, taskA := trace.NewTask(context.Background(), "spin")
	var wg sync.WaitGroup
	trace.Log(ctxA, "plan", fmt.Sprintf("%d goroutines x 40ms spin", 2))
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer trace.StartRegion(ctxA, "cpu-burn").End()
			spin(40 * time.Millisecond)
		}(i)
	}
	wg.Wait()
	taskA.End()

	// 负载 B：channel 交接。在看什么：两个 G 反复 _Gwaiting ↔ _Grunning，
	// 这是 trace 里最典型的一对乒乓状态。
	ctxB, taskB := trace.NewTask(context.Background(), "pingpong")
	trace.Log(ctxB, "plan", "500 round trips over unbuffered chan")
	pingpong(500)
	taskB.End()

	// 负载 C：真实网络请求。在看什么：客户端 G 进入 _Gwaiting（等 netpoller），
	// 服务端 G 在 _Gsyscall 里完成 accept/read/write——内核侧的等待。
	ctxC, taskC := trace.NewTask(context.Background(), "http")
	trace.Log(ctxC, "plan", "3 requests to a local server")
	for i := 0; i < 3; i++ {
		runOneHTTP()
	}
	taskC.End()

	// 步骤 4：手动触发一次 GC。在看什么：trace 的 GC 视图会出现标记阶段，
	// 后台标记 G（GC dedicated workers）以 _Grunning 出现一小段。
	runtime.GC()
}

// spin 忙等 dur，把自旋次数累加进 sink。
func spin(dur time.Duration) {
	var n uint64
	end := time.Now().Add(dur)
	for time.Now().Before(end) {
		n++
	}
	sink.Add(n)
}

// pingpong 让两个 goroutine 通过无缓冲 channel 交接 n 次。
// 在看什么：main G 在这里第一次进入 _Gwaiting（等第一个应答）。
func pingpong(n int) {
	ch := make(chan int)
	go func() {
		for v := range ch {
			ch <- v + 1
		}
	}()
	x := 0
	for i := 0; i < n; i++ {
		ch <- x
		x = <-ch
	}
	close(ch)
}

// runOneHTTP 向本地临时 HTTP 服务发一次请求。
// 在看什么：客户端 G 阻塞在连接读上（等 netpoller），服务端 G 阻塞在 syscall。
func runOneHTTP() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen 失败:", err)
		os.Exit(1)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond) // 模拟后端处理；期间连接读 G 处于等网络状态
		fmt.Fprint(w, "ok")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	resp, err := http.Get(fmt.Sprintf("http://%s/", ln.Addr()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
}

func mustSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return st.Size()
}
