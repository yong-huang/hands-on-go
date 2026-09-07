package main

// 项目 8：Mutex 底层——state 字段、自旋与饥饿模式
//
// sync.Mutex 的内存布局是 { state int32, sema uint32 }，十余年未变，
// 可直接用 unsafe 读出真实的 state 字并解码（无需 linkname）：
//
//   state 位布局（32 bit）：
//     bit 0 : locked   —— 锁是否被持有
//     bit 1 : woken    —— 是否已唤醒一个等待者
//     bit 2 : starving —— 是否处于饥饿模式
//     bit 3+ : waiters —— 排队等待的 goroutine 数量
//
// 实验：
//   1. 基本位操作（Lock/Unlock、waiters 计数）
//   2. 自旋：持锁很短 → 竞争者自旋即可拿到，waiters 不增加
//   3. 饥饿：持续竞争下某等待者累计等待 > 1ms → starving 位置位
//
// 运行：go run main.go        文本演示
//       go run main.go -json 输出 JSON 状态快照，供 viz.py 画图

import (
	"encoding/json"
	"flag"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// 与 sync.Mutex 前 8 字节一致的影子结构
type muShadow struct {
	state int32
	sema  uint32
}

// 读 state 字（原子，避免数据竞争）
func readState(m *sync.Mutex) int32 {
	s := (*muShadow)(unsafe.Pointer(m))
	return atomic.LoadInt32(&s.state)
}

type bits struct {
	Locked   bool
	Woken    bool
	Starving bool
	Waiters  int32
}

func decode(s int32) bits {
	return bits{
		Locked:   s&1 != 0,
		Woken:    s&2 != 0,
		Starving: s&4 != 0,
		Waiters:  s >> 3,
	}
}

func (b bits) String() string {
	return fmt.Sprintf("locked=%v woken=%v starving=%v waiters=%d",
		b.Locked, b.Woken, b.Starving, b.Waiters)
}

// ---------- 实验 1：state 基本位 ----------

func exp1() {
	fmt.Println("========== 1. state 字段：locked / woken / starving / waiters ==========")
	var m sync.Mutex
	fmt.Printf("  新建未锁: state=%d → %v\n", readState(&m), decode(readState(&m)))
	m.Lock()
	fmt.Printf("  加锁后  : state=%d → %v\n", readState(&m), decode(readState(&m)))
	fmt.Println("            bit0=1（locked），其余为 0")

	// 3 个等待者阻塞排队
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m.Lock()
			m.Unlock()
		}()
	}
	close(start)
	time.Sleep(20 * time.Millisecond) // 等 3 个竞争者入队
	fmt.Printf("  持锁时有 3 人等待: state=%d → %v\n", readState(&m), decode(readState(&m)))
	fmt.Println("            waiters=3（bits3+ 计数），waiters = state >> 3")
	m.Unlock()
	wg.Wait()
	fmt.Printf("  全部放行后: state=%d → %v\n", readState(&m), decode(readState(&m)))
}

// ---------- 实验 2：自旋（窗口只有几微秒） ----------

// 压出 8 个竞争者抢锁，在几微秒内连续采样 waiters 的爬升
func spinRamp() []int64 {
	var m sync.Mutex
	m.Lock()
	var wg sync.WaitGroup
	start := make(chan struct{})
	const contenders = 8
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m.Lock()
			m.Unlock()
		}()
	}
	close(start)
	ramp := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		time.Sleep(2 * time.Microsecond)
		ramp = append(ramp, int64(decode(readState(&m)).Waiters))
	}
	m.Unlock()
	wg.Wait()
	return ramp
}

func exp2() {
	fmt.Println("\n========== 2. 自旋：窗口只有几微秒 ==========")
	prev := runtime.GOMAXPROCS(2) // 2 个 P 时观察竞争最清晰
	defer runtime.GOMAXPROCS(prev)
	ramp := spinRamp()
	for i, w := range ramp {
		fmt.Printf("  持锁后 t≈%2dµs  waiters=%d\n", (i+1)*2, w)
	}
	fmt.Println("  → 竞争者先自旋 ~2µs（空转轮询 CAS），失败立刻 park 入队")
	fmt.Println("    Go 的自旋只有几次迭代：宁可快速入队，也不长空转浪费 CPU")
	fmt.Println("    自旋的意义：临界区极短时（<1µs）免一次上下文切换")
}

// ---------- 实验 3：饥饿模式 ----------

type sample struct {
	T_us     int64 `json:"t_us"`
	Locked   bool  `json:"locked"`
	Woken    bool  `json:"woken"`
	Starving bool  `json:"starving"`
	Waiters  int32 `json:"waiters"`
}

var workSink uint64

// work 模拟临界区里的实际工作（us 微秒）
func work(us int) {
	end := time.Now().Add(time.Duration(us) * time.Microsecond)
	for time.Now().Before(end) {
		workSink++
	}
}

func exp3(silent bool) []sample {
	if !silent {
		fmt.Println("\n========== 3. 饥饿模式：持续竞争 → starving 位 ==========")
	}
	prev := runtime.GOMAXPROCS(2) // 只有 2 个 P，竞争激烈到必然饥饿
	defer runtime.GOMAXPROCS(prev)

	var m sync.Mutex
	stop := make(chan struct{})
	var wg sync.WaitGroup
	start := make(chan struct{})
	const workers = 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.Lock()
				work(60) // 60µs 临界区，让竞争持续
				m.Unlock()
			}
		}()
	}
	close(start)

	samples := []sample{}
	begin := time.Now()
	for {
		time.Sleep(2 * time.Millisecond)
		elapsed := time.Since(begin)
		if elapsed > 30*time.Millisecond {
			break
		}
		b := decode(readState(&m))
		samples = append(samples, sample{
			T_us: elapsed.Microseconds(), Locked: b.Locked,
			Woken: b.Woken, Starving: b.Starving, Waiters: b.Waiters,
		})
	}

	if !silent {
		first := -1
		for _, s := range samples {
			mark := ""
			if s.Starving && first < 0 {
				first = 1
				mark = "  ← 首次进入饥饿模式"
			}
			fmt.Printf("  t=%7dµs  locked=%v  starving=%v  waiters=%d%s\n",
				s.T_us, s.Locked, s.Starving, s.Waiters, mark)
		}
		fmt.Println("  → 等待者累计等待 >1ms 后 mutex 转入饥饿模式（FIFO 直通，防饿死）")
		fmt.Println("    被服务后就复位，高竞争下会反复进出饥饿模式")
	}
	return samples
}

// ---------- 总结 ----------

func summary() {
	fmt.Println("\n========== 总结 ==========")
	fmt.Println("1. state 字：locked(bit0) | woken(bit1) | starving(bit2) | waiters(bits3+)")
	fmt.Println("2. 自旋：窗口仅几微秒（空转轮询 CAS），失败立刻 park 入队")
	fmt.Println("3. 饥饿模式：入队者累计等待 >1ms 触发，unlock 直接交接给最老的等待者")
	fmt.Println("4. 两种模式配合：正常模式吞吐高，饥饿模式保公平、防饿死")
}

// ---------- JSON 状态快照 ----------

func jsonOut() {
	prev := runtime.GOMAXPROCS(2)
	ramp := spinRamp()
	runtime.GOMAXPROCS(prev)
	out := map[string]interface{}{
		"exp1": map[string]interface{}{
			"fresh": func() string {
				m := &sync.Mutex{}
				return decode(readState(m)).String()
			}(),
			"locked": func() string {
				m := &sync.Mutex{}
				m.Lock()
				return decode(readState(m)).String()
			}(),
			"waiters": func() string {
				m := &sync.Mutex{}
				m.Lock()
				var wg sync.WaitGroup
				start := make(chan struct{})
				for i := 0; i < 3; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						m.Lock()
						m.Unlock()
					}()
				}
				close(start)
				time.Sleep(10 * time.Millisecond)
				s := decode(readState(m)).String()
				m.Unlock()
				wg.Wait()
				return s
			}(),
		},
		"spin":       ramp,
		"starvation": exp3(true),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func main() {
	jsonFlag := flag.Bool("json", false, "输出 JSON 快照")
	flag.Parse()
	if *jsonFlag {
		jsonOut()
		return
	}
	exp1()
	exp2()
	exp3(false)
	summary()
}
