package main

// 项目 19：系统调用与阻塞 I/O —— 观察 M（OS 线程）增长
//
// Go 里 G 阻塞在真实系统调用上时，对应 M 会被"脱钩"（handoff）给调度器：
// M 陷在 syscall 里回不来，调度器只能另找/新建 M 去跑其余 G →
// 于是 M 数量会超过 GOMAXPROCS(=P)。这正是"网络 I/O 不走线程蹲点"
// 反例：真实阻塞 I/O（这里有 flock 文件锁）会真的耗线程。
//
// 演示：主协程把文件锁死，8 个 worker 阻塞在 flock 上，同时记录
//   G 数（runtime.NumGoroutine）与 M 数（threadcreate profile：进程
//   创建过的 OS 线程数，玩后端对比差值 = 因阻塞新建的线程）。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

type snapshot struct {
	Phase      string `json:"phase"`
	Goroutines int    `json:"goroutines"`
	Threads    int    `json:"threads"`
	ThreadDiff int    `json:"threadDiff"`
	Waiting    int    `json:"waiting"` // 仍卡在锁上的 worker 数
}

func threadCount() int {
	p := pprof.Lookup("threadcreate")
	if p == nil {
		return 0
	}
	return p.Count()
}

func snap(phase string, waiting int) snapshot {
	return snapshot{Phase: phase, Goroutines: runtime.NumGoroutine(),
		Threads: threadCount(), Waiting: waiting}
}

func worker(fd uintptr, started chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	started <- struct{}{}
	// 阻塞在真实 flock 系统调用，直到拿到锁
	for {
		err := syscall.Flock(int(fd), syscall.LOCK_EX)
		if err == nil {
			runtime.Gosched() // 假装干点活
			syscall.Close(int(fd))
			return
		}
		if err != syscall.EINTR {
			return // 其余错误直接放弃
		}
	}
}

func exp() ([]snapshot, []snapshot) {
	runtime.GOMAXPROCS(2)

	tmp, err := os.CreateTemp("", "hd_lock_*.lock")
	if err != nil {
		panic(err)
	}
	defer os.Remove(tmp.Name())

	master, err := os.OpenFile(tmp.Name(), os.O_RDWR, 0)
	if err != nil {
		panic(err)
	}
	// 主协程先锁死文件
	if err := syscall.Flock(int(master.Fd()), syscall.LOCK_EX); err != nil {
		panic(err)
	}

	const K = 8
	hist := []snapshot{snap("baseline", 0)}
	hist = append(hist, snap("start", 0))

	fds := make([]uintptr, K)
	var wg sync.WaitGroup
	started := make(chan struct{}, K)
	for i := 0; i < K; i++ {
		f, err := os.OpenFile(tmp.Name(), os.O_RDWR, 0)
		if err != nil {
			panic(err)
		}
		fds[i] = f.Fd()
		wg.Add(1)
		go worker(fds[i], started, &wg)
	}
	// 等所有 worker 都开始阻塞在 flock
	for i := 0; i < K; i++ {
		<-started
	}
	time.Sleep(120 * time.Millisecond)
	hist = append(hist, snap("blocked-8", 8))
	time.Sleep(180 * time.Millisecond)
	hist = append(hist, snap("blocked+", 8))

	// 释放锁 → worker 依次拿到锁退出
	syscall.Close(int(master.Fd()))
	wg.Wait()
	hist = append(hist, snap("after-release", 0))

	base := hist[0].Threads
	for i := range hist {
		hist[i].ThreadDiff = hist[i].Threads - base
	}
	return hist[:1], hist // 只用 4 个关键点，也留全量给 JSON
}

func jdump(st []snapshot) {
	b, _ := json.MarshalIndent(st, "", "  ")
	if !silent {
		pr("%s\n", b)
	} else {
		fmt.Println(string(b))
	}
}

func textOut(st []snapshot) {
	pr(" 阶段            G数   M(累计线程)  新增线程  卡锁worker\n")
	for _, s := range st {
		pr(" %-14s %4d %7d %10d %9d\n",
			s.Phase, s.Goroutines, s.Threads, s.ThreadDiff, s.Waiting)
	}
	pr("\nGOMAXPROCS=2，但 8 个 worker 同时阻塞在 flock → M 撑到 %d\n",
		st[len(st)-2].Threads)
}

func summary(st []snapshot) {
	pr("\n========== 结论 ==========\n")
	pr("1. 真实阻塞 syscall（flock/read 文件/连接 DB）时，M 必须蹲在 syscall 里\n")
	pr("   线程不归还 → 调度器新建 M 去跑别的 G → M 超过 GOMAXPROCS\n")
	pr("2. 网络 I/O 走的却是非阻塞 fd + netpoller(kqueue/epoll)，G 阻塞而 M 不阻塞\n")
	pr("3. 所以「并发连接百万」靠的是『不阻塞』，不是靠线程\n")
	pr("4. 排查思路：GOMAXPROCS 定了而线程还在涨 → 有人在打真实阻塞 syscall\n")
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	hd, st := exp()
	_ = hd
	textOut(st)
	summary(st)
	jdump(st)
}