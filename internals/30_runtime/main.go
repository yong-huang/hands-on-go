package main

// 项目 30：编写一个最小的 Go 运行时模拟
//
// 用 Go 模拟 Go 运行时三大件（真·套娃）：
//   1. GMP 调度器：P 各自带本地队列，全局队列 + 工作窃取（work stealing）
//   2. 内存分配器：arena 分 size class，mcache（每 P 缓存）→ mcentral（每类上锁）
//   3. 标记-清扫 GC：任务跑完后从 roots 标记，未标记对象回收到 central
//
// "M" 用真 goroutine 充当，"G" 是带状态的闭包任务。GC 在完成数越过阈值时触发。
//
// 运行：go run main.go [-json]

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 内存分配器（mcache → mcentral） ----------

const NClass = 5

var classSizes = []int{8, 16, 32, 64, 128}
var classPools = []int{64, 48, 32, 24, 16}

type Object struct {
	id     int
	class  int
	inUse  bool
	marked bool
}

type mcentral struct {
	free [NClass][]*Object
	mu   sync.Mutex
}

type mcache struct {
	free [NClass][]*Object
}

type Allocator struct {
	mu        sync.Mutex
	central   *mcentral
	inUse     []*Object
	allocs    int64
	frees     int64
	liveBytes int64
	gcRuns    int
	collectBytes int64
}

func newAllocator() *Allocator {
	a := &Allocator{central: &mcentral{}}
	id := 0
	for c := 0; c < NClass; c++ {
		for i := 0; i < classPools[c]; i++ {
			a.central.free[c] = append(a.central.free[c], &Object{id: id, class: c})
			id++
		}
	}
	return a
}

func (mc *mcache) pop(c int) *Object {
	if n := len(mc.free[c]); n > 0 {
		o := mc.free[c][n-1]
		mc.free[c] = mc.free[c][:n-1]
		return o
	}
	return nil
}

func (mc *mcache) refill(a *Allocator, c int) {
	a.central.mu.Lock()
	pool := a.central.free[c]
	batch := 8
	if len(pool) < batch {
		batch = len(pool)
	}
	mc.free[c] = append(mc.free[c], pool[:batch]...)
	a.central.free[c] = pool[batch:]
	a.central.mu.Unlock()
}

func (a *Allocator) alloc(c int, mc *mcache) *Object {
	a.mu.Lock()
	a.allocs++
	a.mu.Unlock()
	o := mc.pop(c)
	if o == nil {
		mc.refill(a, c)
		o = mc.pop(c)
		if o == nil {
			return nil
		}
	}
	o.inUse = true
	o.marked = false
	a.mu.Lock()
	a.inUse = append(a.inUse, o)
	a.liveBytes += int64(classSizes[c])
	a.mu.Unlock()
	return o
}

func (a *Allocator) gc(roots []*Object) {
	a.mu.Lock()
	a.gcRuns++
	for _, o := range a.inUse {
		o.marked = false
	}
	stack := append([]*Object(nil), roots...)
	for len(stack) > 0 {
		o := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if o == nil || o.marked || !o.inUse {
			continue
		}
		o.marked = true
	}
	survivors := a.inUse[:0]
	var collect int64
	for _, o := range a.inUse {
		if o.marked {
			survivors = append(survivors, o)
			continue
		}
		collect += int64(classSizes[o.class])
		o.inUse = false
		a.central.mu.Lock()
		a.central.free[o.class] = append(a.central.free[o.class], o)
		a.central.mu.Unlock()
	}
	a.inUse = survivors
	a.liveBytes -= collect
	a.collectBytes += collect
	a.frees++
	a.mu.Unlock()
}

// ---------- GMP 调度器 ----------

type GTask struct {
	id    int
	state string
	f     func(*P)
}

type P struct {
	id    int
	local []*GTask
	cache *mcache
}

type Mev struct {
	Ev string  `json:"ev"`
	Ts float64 `json:"ts"`
}

type Scheduler struct {
	global    []*GTask
	ps        []*P
	mu        sync.Mutex
	taskCount int
	steals    int
	maxLocal  int
	done      int
	lastGC    int
	events    []*[]Mev
}

func newScheduler(nP int) *Scheduler {
	s := &Scheduler{}
	for i := 0; i < nP; i++ {
		s.ps = append(s.ps, &P{id: i, cache: &mcache{}})
		s.events = append(s.events, &[]Mev{})
	}
	return s
}

func (s *Scheduler) addTask(f func(*P)) {
	g := &GTask{id: s.taskCount, state: "runnable", f: f}
	s.taskCount++
	s.mu.Lock()
	if s.taskCount%3 == 0 { // 约 1/3 进全局队列（负载均衡）
		s.global = append(s.global, g)
	} else { // 其余按轮询塞进各 P 的本地队列
		p := s.ps[s.taskCount%len(s.ps)]
		p.local = append(p.local, g)
		if len(p.local) > s.maxLocal {
			s.maxLocal = len(p.local)
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) steal(p *P) *GTask {
	s.mu.Lock()
	if n := len(s.global); n > 0 {
		g := s.global[n-1]
		s.global = s.global[:n-1]
		s.mu.Unlock()
		s.steals++
		return g
	}
	s.mu.Unlock()
	for _, q := range s.ps {
		if q == p || len(q.local) == 0 {
			continue
		}
		n := len(q.local)
		g := q.local[n-1]
		q.local = q.local[:n-1]
		s.steals++
		return g
	}
	return nil
}

// m：一个"M" = 真 goroutine，绑一个 P
func (s *Scheduler) m(mID int, p *P, alloc *Allocator, roots *[]*Object,
	rootsMu *sync.Mutex, gcThreshold int, done chan<- struct{}) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(mID)))
	now := func() float64 { return float64(time.Now().UnixNano()) / 1e6 }
	evs := s.events[mID]
	cur := ""
	emit := func(ev string) {
		if ev == cur {
			return
		}
		*evs = append(*evs, Mev{ev, now()})
		cur = ev
	}
	for {
		var g *GTask
		if n := len(p.local); n > 0 {
			g = p.local[n-1]
			p.local = p.local[:n-1]
			emit("run")
		} else {
			g = s.steal(p)
			if g == nil {
				emit("wait")
				time.Sleep(time.Duration(rng.Intn(500)) * time.Microsecond)
				continue
			}
			emit("steal")
		}
		g.state = "running"
		g.f(p)
		g.state = "done"
		s.mu.Lock()
		s.done++
		if s.done%gcThreshold == 0 && s.done != s.lastGC {
			s.lastGC = s.done
			rootsCopy := append([]*Object(nil), (*roots)...)
			s.mu.Unlock()
			alloc.gc(rootsCopy)
			s.mu.Lock()
		}
		finished := s.done == s.taskCount
		s.mu.Unlock()
		if finished {
			emit("idle")
			done <- struct{}{}
			return
		}
	}
}

// ---------- 业务任务（G）：分配对象 + 小计算 ----------

func (s *Scheduler) spawnWorkers(alloc *Allocator, allocs int, keep int,
	roots *[]*Object, rootsMu *sync.Mutex) {
	keepLeft := keep
	for i := 0; i < 12; i++ {
		i := i
		s.addTask(func(p *P) {
			n := allocs / 12
			for j := 0; j < n; j++ {
				c := i % NClass
				o := alloc.alloc(c, p.cache)
				// 约 3/4 是临时对象（会挂掉），其余进 roots 存活
				if j%4 == 3 && keepLeft > 0 {
					rootsMu.Lock()
					if keepLeft > 0 {
						*roots = append(*roots, o)
						keepLeft--
					}
					rootsMu.Unlock()
				}
			}
			acc := 0
			for j := 0; j < 200; j++ {
				acc += (i*31 + j) % 7
			}
			_ = acc
		})
	}
}

// ---------- 主流程 ----------

func run() map[string]interface{} {
	const nP = 4
	alloc := newAllocator()
	s := newScheduler(nP)
	roots := []*Object{}
	var rootsMu sync.Mutex

	s.spawnWorkers(alloc, 1200, 15, &roots, &rootsMu)

	// 4 个"M"（真 goroutine）跑调度循环，任务完成数过阈值触发 GC
	done := make(chan struct{})
	for mID, p := range s.ps {
		go s.m(mID, p, alloc, &roots, &rootsMu, 4, done)
	}
	<-done

	res := map[string]interface{}{
		"scheduler": map[string]interface{}{
			"gmp": nP, "taskCount": s.taskCount, "done": s.done,
			"steals": s.steals, "maxLocalQueue": s.maxLocal,
		},
		"allocator": map[string]interface{}{
			"allocs": alloc.allocs, "gcRuns": alloc.gcRuns,
			"liveBytes": alloc.liveBytes, "collectedBytes": alloc.collectBytes,
			"frees": alloc.frees, "keptRoots": len(roots),
		},
		"events": s.events,
	}
	return res
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	silent = *jsonFlag
	res := run()
	if *jsonFlag {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	} else {
		sched := res["scheduler"].(map[string]interface{})
		alloc := res["allocator"].(map[string]interface{})
		pr("== 调度器 ================\n")
		pr("  GMP=%v  任务=%v  完成=%v  窃取=%v  本地队列峰值=%v\n",
			sched["gmp"], sched["taskCount"], sched["done"],
			sched["steals"], sched["maxLocalQueue"])
		pr("== 分配器 + GC ===========\n")
		pr("  分配=%v  回收对象批=%v  GC 次数=%v  存活字节=%v  回收字节=%v  根对象=%v\n",
			alloc["allocs"], alloc["frees"], alloc["gcRuns"],
			alloc["liveBytes"], alloc["collectedBytes"], alloc["keptRoots"])
	}
}