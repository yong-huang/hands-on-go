package main

// 项目 28：调度器 Trace 分析
//
// 用 runtime/trace 录一段包含三类 goroutine 的轨迹，再喂给 go tool trace：
//   1. CPU 密集任务：忙循环算质数
//   2. 模拟 I/O 任务：读文件 + 睡眠（阻塞型）
//   3. 管道任务：生产者/消费者通过 chan 交接
//
// 同时用自建埋点把每类任务的"开始/结束墙钟时间"记下来，
// 可视化画成 goroutine 生命周期条（调度视图）。
//
// 运行：go run main.go [-json]（trace 文件写入系统临时目录）

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/trace"
	"sync"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// 事件埋点：{kind, id, phase, tsMs}
type ev struct {
	Kind  string  `json:"kind"`
	ID    int     `json:"id"`
	Phase string  `json:"phase"` // start / end
	TsMs  float64 `json:"tsMs"`
}

var (
	evMu   sync.Mutex
	events []ev
	nowMs  = func() float64 { return float64(time.Now().UnixNano()) / 1e6 }
)

func mark(kind string, id int, phase string) {
	evMu.Lock()
	defer evMu.Unlock()
	events = append(events, ev{kind, id, phase, nowMs()})
}

// 1) CPU 密集：算质数
func cpuTask(id int, iters int) {
	mark("cpu", id, "start")
	defer mark("cpu", id, "end")
	for i := 0; i < iters; i++ {
		isPrime(1000 + i%4000)
	}
}

func isPrime(n int) bool {
	for d := 2; d*d <= n; d++ {
		if n%d == 0 {
			return false
		}
	}
	return true
}

// 2) 模拟 I/O：读文件 + 睡眠（把 CPU 让出去）
func ioTask(id int) {
	mark("io", id, "start")
	defer mark("io", id, "end")
	f, _ := os.Open("/usr/share/dict/words")
	if f != nil {
		buf := make([]byte, 4096)
		for i := 0; i < 6; i++ {
			f.Read(buf) // 真正的系统调用阻塞
		}
		f.Close()
	}
	time.Sleep(40 * time.Millisecond) // 显式让出
}

// 3) 管道任务：生产者/消费者交接
func producer(id int, n int, ch chan int) {
	mark("chan", id, "start")
	defer mark("chan", id, "end")
	for i := 0; i < n; i++ {
		ch <- i
	}
}

func consumer(id int, n int, ch chan int, wg *sync.WaitGroup) {
	defer wg.Done()
	mark("chan", id, "start")
	defer mark("chan", id, "end")
	for i := 0; i < n; i++ {
		<-ch
	}
}

func run() (map[string]interface{}, string) {
	tmp, _ := os.CreateTemp("", "trace*.out")
	tracePath := tmp.Name()
	tmp.Close()

	f, err := os.Create(tracePath)
	if err != nil {
		panic(err)
	}
	if err := trace.Start(f); err != nil {
		panic(err)
	}

	const N = 4 // 每类任务 goroutine 数

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cpuTask(id, 400)
		}(i)
	}

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ioTask(id)
		}(i)
	}

	ch := make(chan int)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			producer(id, 20, ch)
		}(i)
		wg.Add(1)
		go func(id int) {
			consumer(id, 20, ch, &wg)
		}(i)
	}

	wg.Wait()
	time.Sleep(10 * time.Millisecond)
	trace.Stop()
	f.Close()

	pr("trace 文件：%s\n", tracePath)
	pr("goroutine 事件数：%d（4 类 × 8 个 goroutine × 2 个相位）\n", len(events))

	res := map[string]interface{}{
		"tracePath": tracePath,
		"events":    events,
		"GOMAXPROCS": runtime.GOMAXPROCS(0),
	}
	return res, tracePath
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	silent = *jsonFlag
	res, path := run()
	if jsonFlag := *jsonFlag; jsonFlag {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	} else {
		fmt.Printf("提示：go tool trace %s（浏览器交互式）\n", path)
	}
}