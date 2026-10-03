// 项目 4：data race 检测器——原理、边界与开销
//
// Go 的竞态检测器是 ThreadSanitizer（TSan）的集成版：每次内存访问都会查
// 一张影子内存（shadow memory，按地址记录最近访问者的元数据表），用
// vector clock（每个线程一个"我知道谁干到哪了"的时钟向量）判定两次访问
// 是否缺少 happens-before 边。它能抓住绝大多数竞态，但不是全部。
//
// 运行方式：
//
//	go run main.go        // 两组竞态都静默通过
//	go run -race main.go  // 第 2 步报 WARNING；第 3 步连续三组零报告（漏报）
//
// 输出分两类：标注（固定）的行每次运行相同；标注（每次不同）的行受调度
// 影响。验证开销用 go test -bench（见 README）。
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// sinkStatus：承接 T1 的读结果，避免编译器删掉那次读；
// atomic 存储不与 main 建立被读取的同步边，保持竞态纯净。
var sinkStatus atomic.Int64

func main() {
	fmt.Println("== 第 1 步：检测器在记什么 ==")
	fmt.Println("每次内存访问：TSan 查影子内存里这个地址的最近访问记录（读写各留几个槽位）。")
	fmt.Println("判定：两次访问一写一读/写、来自不同线程、且 vector clock 无先后关系 => 报告。")
	fmt.Println()

	fmt.Println("== 第 2 步：标准竞态——两个 goroutine 无同步对同一变量自增 ==")
	detectedRace()
	fmt.Println("（已运行。go run -race main.go 时，上方应有 WARNING: DATA RACE，")
	fmt.Println("  条数随交错而定，一至两条；不带 -race 时丢失的自增无声无息）")
	fmt.Println()

	fmt.Println("== 第 3 步：漏报构造——真竞态，但访问记录被逐出 ==")
	fmt.Println("T1 读 x 后与外界失联；16 个垫写者写同一 8 字节单元里的 y")
	fmt.Println("（x 的隔壁 4 字节，字节不重叠 => 与谁都不构成竞态）；")
	fmt.Println("T2 等垫写者全部完成再写 x。T1 的读与 T2 的写之间没有")
	fmt.Println("任何 happens-before 链 => 真竞态；但影子单元容量有限，")
	fmt.Println("T1 那条读记录已被垫写者逐出 => T2 写入时无可比对记录。")
	for i := 1; i <= 3; i++ {
		fmt.Printf("--- 第 %d 组（若到下一组标题前没有 WARNING，即为漏报）---\n", i)
		missedRace()
	}
	fmt.Println("（固定）若 go run -race main.go 的第 3 步三组全静默，漏报稳定复现。")
	fmt.Println()

	fmt.Println("== 第 4 步：开销测量入口 ==")
	fmt.Println("ns/op 与 allocs/op：go test -bench . -run '^$' -benchmem")
	fmt.Println("带 -race 的对照组： go test -race -bench . -run '^$' -benchmem")
	fmt.Println("内存（RSS）：先 go test -c -o svc.test . 编出测试二进制（go test 直接")
	fmt.Println("测会把工具链开销算进去），再 /usr/bin/time -l ./svc.test -test.run TestServiceLoad；")
	fmt.Println("带 -race 时用 go test -c -race -o svc_race.test . 编出的二进制同样测量。")
	fmt.Println("结果已入 README 的开销表。")
}

// detectedRace：教科书式竞态。两个 goroutine 对普通 int 各自增 1000 次，
// 无任何同步。带 -race 必报，不带 -race 大概率"看起来没问题"。
func detectedRace() {
	var n int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			n++ // 读-改-写三步，与另一个 goroutine 的同名操作交错
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			n++
		}
	}()
	wg.Wait()
}

// missedRace：-race 检不出、但按内存模型定义确实存在的竞态。
// 构造分三层，缺一不可：
//
//  1. T1 只做一次普通读 x，此后与外界再无任何同步——它与 T2 的写之间
//     没有 happens-before 链，按内存模型定义就是真竞态；
//  2. 影子内存按 8 字节单元记录访问，单元容量有限（TSan v3 每单元 4 条，
//     随版本可能变化），槽位整体复用。垫写者写的是同单元里的 y（x 的
//     隔壁 4 字节）：字节不重叠，与 T1、T2 都不构成竞态，但每条写都要
//     占一个槽，16 条来自不同线程的写把 T1 那条读记录彻底逐出。
//     两个细节缺一不可：垫写者之间用 channel 链式交接（否则它们彼此
//     就是无同步的写-写竞态，会被如实报出）；必须用"写"而不能用"读"
//     来垫——单元满员时新读记录直接被丢弃，不逐出旧记录（第一稿用
//     垫读者，-count=10 就翻车一次，T1 的记录原封不动）。
//  3. T2 等全部垫写者完成（WaitGroup）再写 x——它与垫写者时钟有序，
//     与 T1 没有。写入时单元里只剩垫写者的记录，全部字节不重叠且有序，
//     于是静默。
func missedRace() {
	w := new(struct{ x, y int32 }) // new 给 8 字节对象 8 字节对齐：x、y 落在同一影子单元
	var wg sync.WaitGroup
	done := make(chan struct{})

	go func() { // T1：读 x，然后与外界彻底失联
		sinkStatus.Store(int64(w.x)) // 访问 A：普通读 x，记入影子内存
	}()
	time.Sleep(2 * time.Millisecond) // 时序垫片：让 A 先落影子；睡眠不产生任何同步边

	prev := make(chan struct{}) // 垫写者的链式交接通道
	close(prev)                 // 预关闭：第一个垫写者不必等待
	for i := 0; i < padWriters; i++ {
		next := make(chan struct{})
		wg.Add(1)
		go func(v int32, prev, next chan struct{}) { // 垫写者：写 y（同单元、字节不重叠）
			defer wg.Done()
			<-prev  // 等上一个垫写者写完：close 同步先于观测关闭的 receive
			w.y = v // 访问 C_i：普通写，与谁都不构成竞态，但逐出单元里的旧记录
			close(next)
		}(int32(i), prev, next)
		prev = next
	}
	go func() { // T2：与垫写者 HB 有序；与 T1 无任何链
		defer close(done)
		wg.Wait()
		w.x = 42 // 访问 B：与 A 无序 => 真竞态；但 A 的记录已被逐出 => 不报
	}()
	<-done
}

// padWriters：垫写者数量。每单元 4 条记录意味着 4 条不同线程的写即可
// 完全换血；这里给 4 倍冗余，检测器实现变化时构造依然成立。
const padWriters = 16
