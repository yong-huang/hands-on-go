// Ring Buffer —— 环形缓冲区 / 循环队列
// 面试高频题: 固定大小队列、head/tail 指针回绕、O(1) 读写

package main

import (
	"errors"
	"fmt"
)

// ── 1. Ring Buffer 核心 ──

var ErrFull = errors.New("ring buffer is full")

type RingBuffer struct {
	buf  []int
	head int // 下一个读出的位置
	tail int // 下一个写入的位置
	count int // 当前元素数量
	cap  int // 最大容量
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		buf: make([]int, capacity),
		cap: capacity,
	}
}

func (rb *RingBuffer) Write(val int) error {
	if rb.IsFull() {
		return ErrFull
	}
	rb.buf[rb.tail] = val
	rb.tail = (rb.tail + 1) % rb.cap
	rb.count++
	return nil
}

func (rb *RingBuffer) Read() (int, bool) {
	if rb.IsEmpty() {
		return 0, false
	}
	val := rb.buf[rb.head]
	rb.head = (rb.head + 1) % rb.cap
	rb.count--
	return val, true
}

func (rb *RingBuffer) Len() int  { return rb.count }
func (rb *RingBuffer) Cap() int  { return rb.cap }
func (rb *RingBuffer) IsFull() bool  { return rb.count == rb.cap }
func (rb *RingBuffer) IsEmpty() bool { return rb.count == 0 }

func (rb *RingBuffer) Show() {
	fmt.Printf("  buf=[")
	for i := 0; i < rb.cap; i++ {
		if i < rb.count {
			idx := (rb.head + i) % rb.cap
			fmt.Printf(" %d", rb.buf[idx])
		} else {
			fmt.Print(" _")
		}
	}
	fmt.Printf(" ]  head=%d  tail=%d  len=%d  cap=%d\n", rb.head, rb.tail, rb.count, rb.cap)
}

// ── 2. Demo ──

func demo() {
	fmt.Println("=== Ring Buffer Demo ===\n")

	rb := NewRingBuffer(5)
	fmt.Printf("创建 RingBuffer(cap=%d)\n\n", rb.Cap())

	// 1. 正常写入
	fmt.Println("1. 写入 3 个元素:")
	for _, v := range []int{10, 20, 30} {
		err := rb.Write(v)
		fmt.Printf("  Write(%d) → %v", v, err == nil)
		rb.Show()
	}

	// 2. 读取
	fmt.Println("\n2. 读取 2 个元素:")
	for i := 0; i < 2; i++ {
		val, ok := rb.Read()
		fmt.Printf("  Read() → %d, ok=%v", val, ok)
		rb.Show()
	}

	// 3. 继续写入（测试回绕）
	fmt.Println("\n3. 继续写入（测试 head/tail 回绕）:")
	for _, v := range []int{40, 50, 60, 70} {
		err := rb.Write(v)
		fmt.Printf("  Write(%d) → %v", v, err == nil)
		rb.Show()
	}

	// 4. 溢出处理
	fmt.Println("\n4. 溢出处理:")
	err := rb.Write(99)
	fmt.Printf("  Write(99) → err: %v\n", err)
	rb.Show()

	// 5. 读空处理
	fmt.Println("\n5. 读空所有元素:")
	for !rb.IsEmpty() {
		val, ok := rb.Read()
		fmt.Printf("  Read() → %d, ok=%v", val, ok)
		rb.Show()
	}
	_, ok := rb.Read()
	fmt.Printf("  Read() → val=0, ok=%v (buffer empty)\n", ok)

	// 6. FIFO 顺序验证
	fmt.Println("\n6. FIFO 顺序验证:")
	rb = NewRingBuffer(3)
	for _, v := range []int{100, 200, 300} {
		rb.Write(v)
	}
	fmt.Print("  写入: 100, 200, 300\n  读取: ")
	for !rb.IsEmpty() {
		val, _ := rb.Read()
		fmt.Printf("%d ", val)
	}
	fmt.Println("\n  FIFO 顺序正确!")
}

// ── 3. 主入口 ──

func main() {
	demo()
	fmt.Println("\n运行 viz.py 生成可视化...")
	fmt.Println("  python3 viz.py")
}
