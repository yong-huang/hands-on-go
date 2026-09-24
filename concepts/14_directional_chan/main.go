package main

import "fmt"

// 14_directional_chan：单向 channel（<-chan 只读 / chan<- 只写）
//
// channel 本身是双向的，但好的 API 只暴露"需要的那个方向"：
//   - 生产者参数声明为 chan<- int：只能发送，想接收直接编译错
//   - 消费者参数声明为 <-chan int：只能接收，想发送直接编译错
// 约束发生在编译期：双向 chan 传参时隐式转换单向，函数内部无法"越权"。
//
// 运行：go run main.go

// producer 只写端：参数类型 chan<- int，箭头指向 chan 表示"只能往里发"。
// ch <- 1 合法；试图 <-ch 接收则编译报错（见函数末尾注释）。
func producer(ch chan<- int) {
	for i := 1; i <= 5; i++ {
		ch <- i * 10 // 只允许发送
	}
	close(ch) // 发送方负责关闭，是 chan<- 的特权也是职责
	// v := <-ch // 编译错：invalid operation: cannot receive from send-only channel ch (variable of type chan<- int)
}

// consumer 只读端：参数类型 <-chan int，箭头在 chan 左侧表示"只能从里收"。
// <-ch 合法；试图 ch <- v 发送则编译报错（见循环后注释）。
func consumer(ch <-chan int) {
	for v := range ch { // channel 被 close 且排空后循环自动退出
		fmt.Printf("收到: %d\n", v)
	}
	// ch <- 1 // 编译错：invalid operation: cannot send to receive-only channel ch (variable of type <-chan int)
}

func main() {
	// main 持有双向 chan（没有箭头），是唯一"全权"持有者
	ch := make(chan int)

	done := make(chan struct{})
	go func() {
		consumer(ch) // 双向 → 只读：隐式转换 <-chan int，编译期完成
		close(done)
	}()

	go producer(ch) // 双向 → 只写：隐式转换 chan<- int，编译期完成

	<-done // 等消费者收尾，避免 main 提前退出
	fmt.Println("channel 已关闭，消费者退出")

	// 转换是单向的：单向不能转回双向，也不能反向转换。
	// var ro <-chan int = ch // OK；var both chan int = ro // 编译错：类型不匹配
}
