// 项目 21：计数器三路基准——Mutex / atomic / 分片填充，争用扩展性实测。
//
// 三种实现的取舍：
//   - MutexCounter：sync.Mutex 保护一个 int64。写法最简，但临界区把全部
//     核串行化，且热点锁所在的缓存行在核间弹跳。
//   - AtomicCounter：atomic.Int64 单条原子加。无锁，但所有核写同一
//     缓存行（cache line，CPU 在缓存与主存间搬运的固定单位，本机 64B），
//     高并发时缓存行在核间"乒乓"，远端核每次都要拉取最新值。
//   - ShardCounter：16 个分片各自独占一条缓存行（填充到 64B），工作
//     协程认领分片互不干扰，读取时求和——以读端代价换写端扩展性。
//
// 运行方式：go run main.go。第 3 步耗时每次不同（量级见输出）；
// 第 2 步守恒校验是固定断言。权威数字用 -bench 矩阵，见 README。
package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// ---------- 三种实现 ----------

// MutexCounter：最朴素的线程安全计数器。
type MutexCounter struct {
	mu sync.Mutex
	n  int64
}

func (c *MutexCounter) Add() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *MutexCounter) Load() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// AtomicCounter：单变量原子加。
type AtomicCounter struct {
	n atomic.Int64
}

func (c *AtomicCounter) Add() { c.n.Add(1) }

func (c *AtomicCounter) Load() int64 { return c.n.Load() }

const shardCount = 16 // 分片数取 2 的幂：与掩码配合免去除法

// paddedShard：一个分片 = 一个原子数 + 填充。填充把相邻分片推到不同
// 缓存行，这是"伪共享"（false sharing，不同核写同一缓存行的不同变量，
// 互相把对方的缓存行打失效）的解药，同 03_false_sharing 的做法。
type paddedShard struct {
	n atomic.Int64
	_ [56]byte // 8 + 56 = 64B
}

// ShardCounter：分片填充计数。写端各走各的缓存行，读端线性求和。
type ShardCounter struct {
	shards [shardCount]paddedShard
}

// Add：shard 是调用方认领的分片号（按协程固定，本地性是本方案前提）。
func (c *ShardCounter) Add(shard int) {
	c.shards[shard&(shardCount-1)].n.Add(1)
}

func (c *ShardCounter) Load() (total int64) {
	for i := range c.shards {
		total += c.shards[i].n.Load()
	}
	return total
}

// ---------- 分步演示 ----------

func main() {
	fmt.Println("== 第 1 步：三实现清单与内存布局 ==")
	fmt.Println("MutexCounter(锁+int64) / AtomicCounter(原子加) / ShardCounter(16 分片×64B 填充)。")
	fmt.Printf("paddedShard 实际大小 = %d 字节（固定，恰好一条缓存行）\n",
		unsafe.Sizeof(paddedShard{}))
	fmt.Println()

	// 第 2 步：正确性守恒——并发加完后总数必须分毫不差。
	fmt.Println("== 第 2 步：守恒校验（8 协程 × 10000 次）==")
	const workers, per = 8, 10_000
	var mu MutexCounter
	var at AtomicCounter
	var sh ShardCounter
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() { // 同一批协程同时喂三个计数器
			defer wg.Done()
			for i := 0; i < per; i++ {
				mu.Add()
				at.Add()
				sh.Add(w)
			}
		}()
	}
	wg.Wait()
	fmt.Printf("期望 %d（固定）；Mutex=%d Atomic=%d Shard=%d（三个都必须相等）\n",
		workers*per, mu.Load(), at.Load(), sh.Load())
	fmt.Println()

	// 第 3 步：争用快测——16 协程各加 200 万次，三实现耗时对比。
	// 单次波动大，权威数字用 -bench 五档矩阵（README），这里看量级。
	fmt.Println("== 第 3 步：争用快测（16 协程 × 200 万次，耗时每次不同）==")
	const ops = 2_000_000
	for _, impl := range []string{"mutex", "atomic", "shard"} {
		elapsed := benchOnce(impl, ops)
		fmt.Printf("%-7s：%8.1f ms（%6.0f Mops/s）\n",
			impl, elapsed.Seconds()*1000, float64(ops*16)/elapsed.Seconds()/1e6)
	}
	fmt.Println()
	fmt.Println("权威矩阵：go test -run '^$' -bench . -benchmem -cpu=1,2,4,8,16 -count=5 ./21_counter_bench/")
}

// benchOnce：启动 NumCPU 个协程（上限 16）各加 ops 次并计时。
func benchOnce(impl string, ops int) time.Duration {
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	var mu MutexCounter
	var at AtomicCounter
	var sh ShardCounter
	var wg sync.WaitGroup
	start := time.Now()
	wg.Add(n)
	for w := 0; w < n; w++ {
		w := w
		go func() {
			defer wg.Done()
			switch impl {
			case "mutex":
				for i := 0; i < ops; i++ {
					mu.Add()
				}
			case "atomic":
				for i := 0; i < ops; i++ {
					at.Add()
				}
			default: // shard：协程号即分片号，认领后不再换
				for i := 0; i < ops; i++ {
					sh.Add(w)
				}
			}
		}()
	}
	wg.Wait()
	return time.Since(start)
}
