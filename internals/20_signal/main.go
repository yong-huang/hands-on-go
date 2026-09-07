package main

// 项目 20：信号处理与 panic 恢复 —— 栈展开过程可视化
//
// 1. os/signal：注册 SIGINT/SIGTERM，把信号变成 channel 消息，
//    （这里为可重复演示，程序自己给自己发一个 SIGINT）
// 2. panic：层层 panic 时，defer 链像叠被子一样收回来（栈展开），
//    在第 2 层用 recover 兜住，并用 runtime.Caller / runtime.Stack
//    打印展开时的调用栈。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 信号 ----------

func startSignalWatcher(sigCh chan os.Signal) chan int {
	countCh := make(chan int, 4)
	go func() {
		n := 0
		for range sigCh {
			n++
			countCh <- n
		}
	}()
	return countCh
}

func selfSignal(when time.Duration) {
	time.AfterFunc(when, func() {
		syscall.Kill(os.Getpid(), syscall.SIGINT)
	})
}

// ---------- panic 栈展开 ----------

// register 返回一个闭包：注册 defer 的瞬间就记下函数名与深度，
// 展开时闭包只是把名字写入日志（避免 defer 在 panic 执行期 Caller 取到 gopanic）
func register(log *[]string) func() {
	fn, _, _, _ := runtime.Caller(1)
	name := short(runtime.FuncForPC(fn).Name())
	return func() {
		*log = append(*log, name)
	}
}

func short(fn string) string {
	out := ""
	for _, c := range fn {
		if c == '.' {
			out = ""
			continue
		}
		out += string(c)
	}
	return out
}

func evil(depth int, log *[]string) {
	defer register(log)()
	if depth == 0 {
		panic("boom: 最深一层 panic")
	}
	evil(depth-1, log)
}

// guard 函数在调用链中间层 recover，演示"展开到哪一层被接住"
func guard(depth int, log *[]string) (stack string, frameCount int, err interface{}) {
	defer func() {
		if r := recover(); r != nil {
			stack = string(runtime.Stack(nil, false))
			err = r
			frameCount = len(stackChain())
		}
	}()
	defer register(log)()
	evil(depth, log)
	return
}

// stackChain：用 CallersFrames 生成调用链（从当前帧往 main 方向）
func stackChain() []string {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	out := []string{}
	for {
		fr, more := frames.Next()
		out = append(out, short(fr.Function))
		if !more {
			break
		}
	}
	return out
}

func arr(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " -> "
		}
		out += x
	}
	return out
}

func exp() map[string]interface{} {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sigCount := startSignalWatcher(sigCh)
	selfSignal(300 * time.Millisecond)

	log := []string{}
	_, frames, err := guard(5, &log)

	// 等信号送达
	var sigN int
	select {
	case sigN = <-sigCount:
	case <-time.After(2 * time.Second):
	}

	trace := stackChain()

	if !silent {
		pr("== 信号 =====================\n")
		pr("PID=%d 自我 SIGINT → 收到 %d 次，channel 化处理\n\n", os.Getpid(), sigN)
		pr("== panic 栈展开 =============\n")
		pr("recover 时异常: %v\n", err)
		pr("展开路径（defer 逆序收拢）:\n")
		for i, s := range log {
			pr("  %d. %s\n", i+1, s)
		}
		pr("\nrecover 时刻调用栈共 %d 帧：%s\n\n", frames, arr(trace))
	}

	res := map[string]interface{}{
		"sigCount":  sigN,
		"recovered": err != nil,
		"frames":    frames,
		"path":      log,
		"trace":     trace,
	}
	if !silent {
		b, _ := json.MarshalIndent(res, "", "  ")
		pr("--- JSON 快照 ---\n%s\n", b)
	} else {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	}
	return res
}

func summary() {
	pr("\n========== 结论 ==========\n")
	pr("1. signal.Notify 把信号转成 channel：处理是异步的、可优雅退出\n")
	pr("2. panic 后先跑当前函数所有 defer（LIFO=栈展开），一路回退到 recover\n")
	pr("3. recover 只在自己 defer 里有效；没接住就 top-frame panic 崩溃\n")
	pr("4. runtime.Caller/Stack 是看「崩到哪」的两把刷子——生产环境抓崩溃也是这套\n")
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	exp()
	summary()
}