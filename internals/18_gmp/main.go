package main

// 项目 18：GMP 调度模拟
//
// Go 的调度模型：G（goroutine）挂在 M（OS 线程）上跑，M 由 P（处理器）
// 配属；新 G 优先进 P 的本地运行队列，满了才进全局队列；某 P 空闲时
// 先从全局队列取，没有就"偷"别的 P 的本地队列（work stealing）。
//
// 本项目不碰真实 runtime，而是写一个迷你调度器，把同样的三条规则
// 建模出来：本地队列(局部性) + 全局队列(公平) + work stealing(负载均衡)，
// 并记录每步调度事件，验证 stealing 真的会发生。
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

const (
	P = 4       // 处理器数
	NS = 12     // 并发 goroutine 数
	LOCAL_CAP = 3 // 本地队列容量：超了溢到全局（真实 runq 上限 256）
)

type event struct {
	Step int    `json:"step"`
	P    int    `json:"p"`
	G    int    `json:"g"`
	Src  string `json:"src"` // local | global | steal
}

type sim struct {
	local [][]int // 每 P 的本地队列（LIFO，尾部取）
	global []int  // 全局队列（FIFO，头部取）
	count []int   // 每个 G 剩余任务
	events []event
	steals  int
	globals int
}

func newSim() *sim {
	s := &sim{local: make([][]int, P)}
	// 负载不均衡：G0-3 各有 8 个任务，G4-11 各有 2 个 —— 才能逼出 steal
	// 每个 G 生成后进"当前 P"的本地队列（模拟 go 语句在发起者的 P 上排队）
	heavy := []int{8, 8, 8}
	for g := 0; g < NS; g++ {
		n := 2
		p := g % P
		if g < len(heavy) {
			n = heavy[g]
			p = 0 // 三个重 G 全挤在 P0，制造不均衡
		}
		s.count = append(s.count, n)
		s.local[p] = append(s.local[p], g)
	}
	return s
}

func (s *sim) pushGlobal(g int) { s.global = append(s.global, g) }

func (s *sim) run() {
	step := 0
	done := 0
	maxStep := 100000

	for done < NS {
		step++
		if step > maxStep {
			fmt.Fprintln(os.Stderr, "!!! 超过步数上限，可能死循环")
			break
		}
		for p := 0; p < P; p++ {
			if done >= NS {
				break
			}
			// 1) 本地队列优先
			src := "local"
			var g int
			if n := len(s.local[p]); n > 0 {
				g = s.local[p][n-1]
				s.local[p] = s.local[p][:n-1]
			} else if len(s.global) > 0 {
				// 2) 本地空了取全局（FIFO）
				g = s.global[0]
				s.global = s.global[1:]
				src = "global"
				s.globals++
			} else {
				// 3) work stealing：偷最忙的邻居，取它的队尾
				victim := -1
				for q := 0; q < P; q++ {
					if q == p {
						continue
					}
					if len(s.local[q]) > 0 && (victim == -1 || len(s.local[q]) > len(s.local[victim])) {
						victim = q
					}
				}
				if victim == -1 {
					continue // 全局空、别人也空 → 这轮这 P 闲着
				}
				v := len(s.local[victim])
				g = s.local[victim][v-1]
				s.local[victim] = s.local[victim][:v-1]
				src = "steal"
				s.steals++
			}
			s.events = append(s.events, event{Step: step, P: p, G: g, Src: src})
			// 任务执行：还剩任务就回本地队列，否则 G 结束
			s.count[g]--
			if s.count[g] > 0 {
				s.local[p] = append(s.local[p], g)
				// 本地队列超容量：队头一半溢到全局（FIFO 公平兜底）
				for len(s.local[p]) > LOCAL_CAP {
					s.pushGlobal(s.local[p][0])
					s.local[p] = s.local[p][1:]
				}
			} else {
				done++
			}
		}
	}

	// 统计每源调度次数
	bySrc := map[string]int{}
	for _, e := range s.events {
		bySrc[e.Src]++
	}
	if !silent {
		pr("== 调度结果：%d 个 G（负载不均），%d 步 =====\n", NS, step)
		pr("   local 取（局部性）  : %d\n", bySrc["local"])
		pr("   global 取（公平）  : %d\n", s.globals)
		pr("   steal 取（负载均衡）: %d\n", s.steals)
		pr("   比例 local:global:steal = %d:%d:%d\n", bySrc["local"], s.globals, s.steals)
	}

	// 输出 JSON
	res := map[string]interface{}{
		"p": P, "ns": NS, "steps": step,
		"bySrc": bySrc, "events": s.events,
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if !silent {
		pr("\n--- JSON 快照 ---\n%s\n", b)
	} else {
		fmt.Println(string(b))
	}
}

// summary 文本结论
func summary() {
	pr("\n========== 结论 ==========\n")
	pr("1. 本地队列是主线：新任务直接进当前 P，不走锁，CPU 缓存友好\n")
	pr("2. 全局队列是安全网：本地满了才溢出；空闲 P 先来这里补\n")
	pr("3. work stealing 兜底：没人喂就偷邻居的，谁忙偷谁\n")
	pr("4. 所以 G 的切换成本 ≈ 出队+入队（无系统调用），百万级并发才可行\n")
	pr("真实 runtime 的差别：偷取取的是对端队头（半），且 1/64 概率查全局，防饿死\n")
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
	}
	s := newSim()
	s.run()
	summary()
}