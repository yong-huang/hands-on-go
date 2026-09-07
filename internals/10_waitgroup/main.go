package main

// 项目 10：WaitGroup 底层——state 与 sema 信号量
//
// sync.WaitGroup 的内存布局（十几年未变，Go 1.20 后用 atomic.Uint64 包装）：
//   { noCopy 0B | state atomic.Uint64 (8B) | sema uint32 (4B) }
//
//   一个 64 位的 state 字把两个计数打包在一起，原子更新只需读一次：
//       高 32 位（state >> 32）：counter  —— 还有多少个 Add 增量未 Done
//       低 32 位（state 低位）  ：waiters  —— 阻塞在 Wait() 上的 goroutine 数量
//   旁边的 sema：等待者睡眠 / 唤醒的信号量（runtime_Semacquire / Semrelease）
//
// 实验：
//   1. Add / Done 对 state 高 32 位 counter 的变化
//   2. Wait() 阻塞 → waiters 低位增加；Done 清到 0 → 全部唤醒并归零
//   3. 时间线：3 个 waiters 爬升 + counter 递减到 0 的那一瞬
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 状态快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// WaitGroup 的 state 字段我们不需要 unsafe —— atomic.Uint64 本身提供
// Load 接口拿到同一份原始值，再由自己解码（这正是标准库内部的解码方式）。
//
//   counter（int32）= int32(state >> 32)    高 32 位
//   waiters（int32）= int32(uint32(state))  低 32 位
//
// 用 unsafe + atomic.LoadUint64 读也可以，但直接走 API 更稳、更贴近标准库。
// sync/atomic.Uint64 的底层正好就是 8 字节的 uint64，内存对齐保证安全。

type wgState struct {
	Counter int32 `json:"counter"`
	Waiters int32 `json:"waiters"`
}

// 从 WaitGroup 里读 state 并解码出 counter / waiters
//
// sync.WaitGroup 的布局是 { noCopy 0B | state atomic.Uint64 | sema uint32 }，
// state 恰好是第一个非零大小字段、8 字节对齐首地址，因此把整个 WaitGroup
// 指针按 *atomic.Uint64 解读就能拿到原始 64 位状态字（这只是读，不影响原子性）。
func readWG(wg *sync.WaitGroup) wgState {
	st := (*atomic.Uint64)(unsafe.Pointer(wg))
	w := st.Load()
	return wgState{Counter: int32(w >> 32), Waiters: int32(uint32(w))}
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		jsonOut()
		return
	}
	exp1(false)
	exp2()
	timeline(false)
	summary()
}

// ---------- 实验 1：Add / Done 改变 state ----------

type step struct {
	Op   string `json:"op"`   // 触发的操作
	Cs   int32  `json:"cs"`   // 之后 counter
	Ws   int32  `json:"ws"`   // 之后 waiters
	Desc string `json:"desc"`
}

func exp1(silent bool) []step {
	if !silent {
		fmt.Println("========== 1. Add / Done：counter 住在 state 的高 32 位 ==========")
	}
	var wg sync.WaitGroup
	steps := []step{}
	record := func(op string, desc string) {
		s := readWG(&wg)
		steps = append(steps, step{Op: op, Cs: s.Counter, Ws: s.Waiters, Desc: desc})
		if !silent {
			fmt.Printf("   %-12s counter=%-2d waiters=%d   (%s)\n", op, s.Counter, s.Waiters, desc)
		}
	}

	record("New()", "新建：两个计数都是 0")
	wg.Add(2)
	record("Add(2)", "要等 2 个任务")
	wg.Add(1)
	record("Add(1)", "再加 1 个，counter=3")
	wg.Done()
	record("Done()", "-1，还剩 2 个")
	wg.Add(2)
	record("Add(2)", "中途还能再涨，counter=4")
	for i := 0; i < 4; i++ {
		wg.Done()
	}
	record("Done()×4", "全部清完，counter=0")

	if !silent {
		fmt.Println("  → counter 累加存活于高 32 位；waiters 为 0（无人 Wait）")
	}
	return steps
}

// ---------- 实验 2：Wait() 阻塞与释放 ----------

func exp2() {
	fmt.Println("\n========== 2. Wait()：waiters 住在低 32 位 ==========")
	var wg sync.WaitGroup
	wg.Add(1)

	woken := make(chan time.Time, 1)
	go func() {
		wg.Wait()
		woken <- time.Now()
	}()

	time.Sleep(10 * time.Millisecond) // 等 waiter 挂上
	before := readWG(&wg)
	fmt.Printf("   任务进行中: counter=%d waiters=%d\n", before.Counter, before.Waiters)
	fmt.Println("   → waiters 低 32 位 +1：一个 goroutine 在 Wait() 上睡觉")

	wg.Done()
	select {
	case <-woken:
		after := readWG(&wg)
		fmt.Printf("   Done() 后  : counter=%d waiters=%d\n", after.Counter, after.Waiters)
		fmt.Println("   → counter 清零那刻把睡眠的 waiter 用 sema 唤醒，等待者归零")
	case <-time.After(200 * time.Millisecond):
		fmt.Println("   [ERR] waiter 没有被唤醒")
	}
}

// ---------- 实验 3：时间线（waiters 爬升 + counter 递减） ----------

type tick struct {
	T_us int64 `json:"t_us"`
	S    wgState
}

func timeline(silent bool) []tick {
	if !silent {
		fmt.Println("\n========== 3. 时间线：3 个 waiter 涌入 + counter 递减到 0 ==========")
	}
	wg := &sync.WaitGroup{}
	wg.Add(3)

	now := time.Now()
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		go func(idx int) {
			time.Sleep(time.Duration(idx) * 2 * time.Millisecond) // 错峰入场，方便看爬升
			<-start
			wg.Wait() // 挂到 waiters，直到 counter 归零被唤醒
		}(i)
	}

	samples := []tick{}
	run := make(chan struct{})
	go func() { // 采样循环
		for time.Since(now) < 30*time.Millisecond {
			samples = append(samples, tick{T_us: time.Since(now).Microseconds(), S: readWG(wg)})
			time.Sleep(500 * time.Microsecond)
		}
		close(run)
	}()

	close(start) // 3 个 waiter 一起涌上来
	time.Sleep(12 * time.Millisecond)
	wg.Done() // +12ms
	time.Sleep(8 * time.Millisecond)
	wg.Done() // +20ms
	time.Sleep(8 * time.Millisecond)
	wg.Done() // +28ms：清零 → 全部唤醒
	<-run

	if !silent {
		for i := 0; i < len(samples); i += 6 { // 抽样打印
			line := fmt.Sprintf("   t=%6dµs  counter=%d waiters=%d", samples[i].T_us,
				samples[i].S.Counter, samples[i].S.Waiters)
			if !silent && samples[i].S.Waiters == 3 && samples[i].S.Counter == 0 {
				line += "   ← 清零，唤醒所有 waiter"
			}
			fmt.Println(line)
		}
		fmt.Println("  → 早期 waiters 从 0 爬到 3；counter 逐次 -1 直到 0 那刻全部放行")
	}
	return samples
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. state 一个字打包两个计数：高32位 counter，低32位 waiters")
	fmt.Println("2. Add/Done 只碰 counter；Wait 只碰 waiters —— 互不干扰，一次原子读")
	fmt.Println("3. counter 归零那刻：对每位 waiter 做 runtime_Semrelease 唤醒")
	fmt.Println("4. 因此 Done 乱序安全、Wait 可跨 goroutine 调用；计数不清零就永久卡住")
}

// ---------- JSON 状态快照 ----------

func jsonOut() {
	steps := exp1(true) // 复用实验 1 的数据
	tl := timeline(true)
	out := map[string]interface{}{
		"steps":    steps,
		"timeline": tl,
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}