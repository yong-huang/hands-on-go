// Read4K → Read N Characters（多调用版本）
// 面试高频题: 缓冲区管理、跨调用状态保持、边界处理

package main

import (
	"fmt"
)

// ── 1. 模拟 Read4 ─

var fileData = "Hello, World! This is a test file for Read4K."
var filePos int

func Read4(buf4 []byte) int {
	n := 0
	for n < 4 && filePos < len(fileData) {
		buf4[n] = fileData[filePos]
		filePos++
		n++
	}
	return n
}

// ── 2. Reader（支持多次调用）──

type Reader struct {
	buf  [4]byte // 内部缓冲区
	pos  int     // buf 中下一个要读的字节
	size int     // buf 中有效字节数
	eof  bool    // 文件是否读完
}

func (r *Reader) Read(buf []byte, n int) int {
	total := 0
	for total < n {
		// 内部缓冲区有剩余，先读
		if r.pos < r.size {
			buf[total] = r.buf[r.pos]
			r.pos++
			total++
			continue
		}
		// 文件已读完
		if r.eof {
			break
		}
		// 调用 Read4 填充缓冲区
		r.size = Read4(r.buf[:])
		r.pos = 0
		if r.size == 0 {
			r.eof = true
			break
		}
	}
	return total
}

// ── 3. 主入口 ──

func main() {
	fmt.Println("=== Read4K → Read N Characters ===\n")

	// 测试 1: 单次读取
	fmt.Println("测试 1: 单次 Read(5)")
	filePos = 0
	r := &Reader{}
	buf := make([]byte, 5)
	n := r.Read(buf, 5)
	fmt.Printf("  读取 %d 字节: %q\n\n", n, string(buf[:n]))

	// 测试 2: 多次调用，跨缓冲区
	fmt.Println("测试 2: 多次 Read(3)")
	filePos = 0
	r = &Reader{}
	for i := 1; i <= 5; i++ {
		buf = make([]byte, 3)
		n = r.Read(buf, 3)
		fmt.Printf("  第%d次 Read(3): %d 字节 → %q\n", i, n, string(buf[:n]))
	}

	// 测试 3: 读取超过文件长度
	fmt.Println("\n测试 3: Read(100) 超过文件长度")
	filePos = 0
	r = &Reader{}
	buf = make([]byte, 100)
	n = r.Read(buf, 100)
	fmt.Printf("  读取 %d 字节: %q\n", n, string(buf[:n]))

	// 测试 4: 文件已读完再读
	fmt.Println("\n测试 4: 文件读完后再 Read(5)")
	n = r.Read(buf[:5], 5)
	fmt.Printf("  读取 %d 字节（应为 0）\n", n)
}
