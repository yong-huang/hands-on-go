package main

// 项目 11：sync.Pool 底层——per-P 分片、victim 缓存与 GC 清理
//
// sync.Pool 不是普通对象池，而是"低 GC 压力下的复用器"：
//   - 每个 P 一块 poolLocal（private 槽 + shared 无锁链条），Get/Put 无锁优先本 P
//   - 每次 GC 运行时都会回调 poolCleanup：把本 P 分片整体平移进 victim 缓存，
//     下一轮 GC 才把上一轮的 victim 丢弃 —— 两拍才彻底清空
// 因此"GC 后对象被清掉"是两阶段：第一轮 GC 后 Get 走 victim 还能捞回旧对象，
// 第二轮 GC 才真的没有。这也解释了 Pool 不能当跨 GC 边界的持久缓存。
//
// 实验：
//   1. 同一轮内的 Put/Get 复用：指针相同，New 不被调用
//   2. GC 两拍清理：单对象灌池，第 1 轮 GC 后 Get 还能捞回，第 2 轮才触发 New
//   3. 复用 vs 新建：同样工作量，复用把触发 GC 的次数压下来
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"sync"
)

type blob struct{ a, b, c int }

var silent bool

func pr(f string, a ...interface{}) {
	if !silent {
		fmt.Printf(f, a...)
	}
}

// ---------- 实验 1：同一轮内的复用 ----------

func exp1() map[string]interface{} {
	pr("========== 1. 同一轮：Put/Get 复用同一个对象 ==========\n")
	getsNew := 0
	pool := &sync.Pool{New: func() interface{} { getsNew++; return &blob{a: 42} }}

	// 取出→还回→再取：必须原样拿到同一个指针
	old := pool.Get().(*blob)
	pool.Put(old)
	same := pool.Get() == old

	// 连续 3 轮 Get/Put，看是否一直是同一对象
	var got []*blob
	rounds := 3
	for i := 0; i < rounds; i++ {
		b := pool.Get().(*blob)
		got = append(got, b)
		pool.Put(b)
	}
	allSame := true
	for i := 1; i < rounds; i++ {
		if got[i] != got[0] {
			allSame = false
		}
	}
	pr("   Get→Put→Get 拿到同一指针: %v（New 只被调了 %d 次）\n", same, getsNew)
	pr("   连续 %d 轮 Get/Put 拿到的都是同一对象 = %v\n", rounds, allSame)
	pr("  → 本 P 的 private 槽位复用；Get 先摸 private，空了才往上走\n")
	return map[string]interface{}{"gets_new": getsNew, "same": same, "all_same": allSame}
}

// ---------- 实验 2：GC 的两拍清理 ----------

type gcTurn struct {
	Turn         int  `json:"turn"`
	VictimHit    bool `json:"victim_hit"`   // 从 victim 缓存捞回旧对象
	NewAllocated bool `json:"new_allocated"` // 触发了 New
}

func exp2() []gcTurn {
	pr("\n========== 2. GC 的两拍：victim 缓存延迟清理 ==========\n")
	// 锁成单 P：victim 缓存按 P 分块，goroutine 在 GC 后可能漂到别的 P，
	// 单 P 才能确定本 P 一定拿到 seed（也算踩坑：多 P 下结果是概率性的）
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)

	newCount := 0
	pool := &sync.Pool{New: func() interface{} { newCount++; return &blob{a: 7} }}

	// 只灌单个对象，观察它被两轮 GC 逐级淘汰的过程
	seed := &blob{a: 100}
	pool.Put(seed)

	var turns []gcTurn
	gotSeed := false
	for turn := 1; turn <= 5; turn++ {
		runtime.GC()
		before := newCount
		b := pool.Get().(*blob)
		isNew := newCount > before
		// 取出来的对象不再放回（让它在后续 GC 里自然淘汰）
		poolHit := !isNew
		if b == seed {
			gotSeed = true
		}
		turns = append(turns, gcTurn{Turn: turn, VictimHit: poolHit, NewAllocated: isNew})
		pr("   GC×%d 后 Get → %s（blob.a=%d, 是 seed=%v）\n",
			turn, map[bool]string{true: "捞出旧对象", false: "触发 New"}[poolHit], b.a, b == seed)
		if isNew {
			break
		}
	}
	pr("  → 单 P 下确定：GC×1 后 victim 还能捞回 seed=%v；下一轮就只剩 New\n", gotSeed)
	return turns
}

// ---------- 实验 3：复用把 GC 压力打下来 ----------

type pressure struct {
	ReuseGC       uint32 `json:"reuse_gc"`
	RawGC         uint32 `json:"raw_gc"`
	RawAllocBytes uint64 `json:"raw_alloc_bytes"`
}

func exp3() pressure {
	const rounds = 3_000_000
	pr("\n========== 3. 复用 vs 新建：GC 次数与总分配量 ==========\n")

	// 3a. 用 Pool：循环里 Get+Put，几乎不产生垃圾
	var pool sync.Pool
	pool.New = func() interface{} { return &blob{a: 2} }
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	beforeGC := stats.NumGC
	for i := 0; i < rounds; i++ {
		b := pool.Get().(*blob)
		b.a = i
		pool.Put(b)
	}
	runtime.ReadMemStats(&stats)
	reuseGC := stats.NumGC - beforeGC

	// 3b. 裸 new：每个循环都分配一个新对象，全留给 GC 收。
	// 用 interface 装箱强制逃逸到堆（否则编译器可能栈上化或消除分配），
	// 每轮旧对象立刻变垃圾 → 堆增长到阈值触发一轮轮 GC
	var sink interface{}
	var pre, post runtime.MemStats
	runtime.ReadMemStats(&pre)
	beforeGC = pre.NumGC
	for i := 0; i < rounds; i++ {
		b := &blob{a: i, b: i, c: i}
		sink = b
	}
	_ = sink
	runtime.ReadMemStats(&post)
	rawGC := post.NumGC - beforeGC

	pr("   Pool 复用 %d 轮：GC 增量 %d 次\n", rounds, reuseGC)
	pr("   裸 new  %d 轮：GC 增量 %d 次，期间分配 %.1f MB\n",
		rounds, rawGC, float64(post.TotalAlloc-pre.TotalAlloc)/1e6)
	return pressure{ReuseGC: reuseGC, RawGC: rawGC, RawAllocBytes: post.TotalAlloc - pre.TotalAlloc}
}

// ---------- 总结 ----------

func summary() {
	pr("\n========== 总结 ==========\n")
	pr("1. Pool = 一撮 per-P 无锁分片（private/shared）+ victim 缓存\n")
	pr("2. 每次 GC 回调 poolCleanup：local → victim（1拍），victim → 丢弃（2拍）\n")
	pr("3. 因此 Pool 的对象只在短窗口内可复用，不能当持久缓存\n")
	pr("4. 收益：大对象循环复用得快、垃圾少、GC 触发次数下降\n")
}

// ---------- JSON ----------

func jsonOut() {
	res := map[string]interface{}{
		"exp1": exp1(),
		"exp2": exp2(),
		"exp3": exp3(),
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
	exp3()
	summary()
}