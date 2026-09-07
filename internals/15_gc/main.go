package main

// 项目 15：GC 三色标记与写屏障
//
// 现代 Go GC 是并发标记-清扫（三色抽象）：
//   - 白色：尚未确认可达（清扫时会被回收）
//   - 灰色：已知可达、但子指针还没扫完（工作队列）
//   - 黑色：扫描完成、已确认可达
// 并发 GC 的关键危险：mutator 在标记进行中向"已黑"对象塞一条指向"白"对象的引用，
// 若没有拦截就会让这个对象被误回收。社区把它叫"三色不变式"破坏。
// 写屏障（WB：Write Barrier）就是在堆写入时把新指向的白对象立刻染灰。
//
// 实验 1：用一个最小对象图仿真标记-清扫，对比"有/无写屏障"两种变异
// 实验 2：真实运行时：构造 200 万节点指针图，记录每轮 GC 停顿与堆回收量
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 实验 1：三色仿真 ----------

type node struct {
	name string
	refs []*node
}

// mark 模拟并发标记过程。mutate 在标记中途修改 B 的引用。
func markSim(writeBarrier bool) (alive map[string]bool, marks int) {
	// 对象图（初始）：
	//   root ──> A ──> B          （C 初始不可达，只有变异后经 B 才可达）
	A := &node{name: "A"}
	B := &node{name: "B"}
	C := &node{name: "C"}
	root := &node{name: "root", refs: []*node{A}}
	A.refs = []*node{B}

	white := map[*node]bool{A: true, B: true, C: true, root: true}
	grey := []*node{root}
	black := map[*node]bool{}
	marks = 0

	// 模拟标记推进；在 B 扫描完成后立刻执行变异（B 已黑，指针不会再被扫）
	for len(grey) > 0 {
		cur := grey[0]
		grey = grey[1:]
		black[cur] = true
		marks++
		// 先扫完 cur 的所有子引用
		for _, n := range cur.refs {
			if n != nil && white[n] {
				white[n] = false
				grey = append(grey, n)
			}
		}
		// 变异发生在 cur 扫描之后：Black B 新加指向 White C 的边
		if cur == B {
			B.refs = append(B.refs, C)
			if writeBarrier && white[C] { // 写屏障：立刻把新指向的白对象染灰
				white[C] = false
				grey = append(grey, C)
			}
		}
	}

	alive = map[string]bool{}
	for n := range black {
		alive[n.name] = true
	}
	return alive, marks
}

func exp1() map[string]interface{} {
	pr("========== 1. 三色标记：写屏障的救场 ==========\n")
	withBar, m1 := markSim(true)
	withoutBar, m2 := markSim(false)
	pr("   对象：root→A→B，标记进行中 B 新增指向 C 的引用\n")
	pr("   有写屏障 : 幸存 = %v（C 被染灰救下），标记步数=%d\n",
		keys(withBar), m1)
	pr("   无写屏障 : 幸存 = %v（C 丢失被误回收！），标记步数=%d\n",
		keys(withoutBar), m2)
	pr("  → 三色不变式被打破：黑色 B → 白色 C 的边必须靠写屏障拦截\n")
	return map[string]interface{}{
		"with_barrier":    keys(withBar),
		"without_barrier": keys(withoutBar),
		"steps_with":      m1,
		"steps_without":   m2,
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------- 实验 2：真实 GC ----------

type obj struct{ p *obj }

func buildGraph(n int) []*obj {
	objs := make([]*obj, n)
	for i := range objs {
		objs[i] = &obj{}
		if i > 0 {
			objs[i].p = objs[i-1]
		}
	}
	return objs
}

func exp2() map[string]interface{} {
	pr("\n========== 2. 真实 GC：停顿与堆回收量 ==========\n")
	// 先造一个大图，让 GC 有一片要扫的对象
	graph := buildGraph(2_000_000)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	objectsBefore := before.HeapObjects

	var pauses []float64 // 每轮停顿（毫秒）
	for i := 0; i < 6; i++ {
		t0 := time.Now()
		runtime.GC()
		pause := float64(time.Since(t0).Microseconds()) / 1000
		pauses = append(pauses, pause)
		// 每轮后丢一半图，制造清扫压力
		if i%2 == 0 && len(graph) > 1000 {
			graph = graph[:len(graph)/2]
		}
	}
	runtime.ReadMemStats(&after)
	objectsAfter := after.HeapObjects

	pr("   每轮 runtime.GC() 停顿(ms)：%v\n", pauses)
	pr("   HeapObjects：%d → %d（清扫回收）±\n", objectsBefore, objectsAfter)
	pr("  → 停顿是 STW 时间；清扫并发进行，堆对象数回落不影响停顿\n")
	return map[string]interface{}{
		"pauses_ms":        pauses,
		"heap_objects_before": objectsBefore,
		"heap_objects_after":  objectsAfter,
	}
}

func summary() {
	pr("\n========== 总结 ==========\n")
	pr("1. 白色=待定，灰色=待扫，黑色=确认：标记就是灰→黑的推进\n")
	pr("2. 并发变异会破坏三色不变式 → 写屏障在堆写入时把白对象染灰\n")
	pr("3. 停顿=STW 时间，标记与清扫都可并发；垃圾多才贵\n")
	pr("4. 堆对象数回落=清扫见效，但停顿看的是标记工作量，两者要分开看\n")

}

// ---------- JSON ----------

func jsonOut() {
	res := map[string]interface{}{
		"sim":   exp1(),
		"gc":    exp2(),
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		silent = true
		jsonOut()
		return
	}
	exp1()
	exp2()
	summary()
}