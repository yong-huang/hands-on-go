// 项目 1：TCP 连接的一生——把状态机的每一跳和 socket API 对上号。
//
// 教科书说"三次握手、四次挥手"，本程序回答它们落在代码的哪一行：
//
//	三次握手：内核在 Accept 返回之前悄悄完成——Accept 只是从"已完成队列"取人
//	数据阶段：Read/Write 就是中间的 ESTABLISHED
//	四次挥手：谁先 Close，谁发第一个 FIN；被动方 Read 到 EOF 后 Close 发第二个
//	TIME_WAIT：主动 Close 的一方要在 TIME_WAIT 停留 2MSL（macOS 默认 30 秒量级）
//
// 观测方法：程序自己调用 netstat -an 过滤本实验端口（lsof 看不到无属主的
// TIME_WAIT socket），把内核里的真实状态打印出来；
// -watch 再实测一次 TIME_WAIT 从出现到消失的存活时长。
//
// 运行方式：go run .（快速演示）；go run . -watch（额外约 30 秒的真实测量）。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"
)

const addr = "127.0.0.1:19501"
const port = "19501"

func main() {
	watch := flag.Bool("watch", false, "追加测量 TIME_WAIT 存活时长（约 30 秒）")
	flag.Parse()

	fmt.Println("== 第 1 步：Listen 与 Accept——三次握手藏在内核里 ==")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	fmt.Printf("Listen 已返回（bind+listen 完成），等待 Accept：%s\n", addr)
	fmt.Println("此刻握手还没发生——它由客户端 connect 触发、内核代劳，Accept 只取结果。")

	// 场景 A：服务端主动 Close（回两行话后关）——主动关的一方进 TIME_WAIT。
	clientA := func() {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			fmt.Println("  [A] dial:", err)
			return
		}
		defer c.Close()
		fmt.Fprintf(c, "hi from client A\n")
		r := bufio.NewReader(c)
		for i := 0; i < 2; i++ {
			line, err := r.ReadString('\n')
			if err != nil {
				fmt.Println("  [A 客户端] 读出错:", err)
				return
			}
			fmt.Printf("  [A 客户端] 收到: %s", line)
		}
		_, err = r.ReadString('\n')
		fmt.Printf("  [A 客户端] 第三次 Read 返回 %v —— 这就是对端 Close 发来的 FIN\n", err)
	}
	go clientA()
	a, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	line, _ := bufio.NewReader(a).ReadString('\n')
	fmt.Printf("Accept 完成（握手已由内核做完）。收到: %s", line)
	fmt.Fprintf(a, "hello from server\n")
	fmt.Fprintf(a, "goodbye from server\n")
	a.Close() // 服务端主动关：第一个 FIN 从这里发出
	fmt.Println("服务端已主动 Close（第一个 FIN 发出）——稍后 netstat 里它应显示 TIME_WAIT")

	// 场景 B：客户端半关闭（CloseWrite）——FIN 之后服务端仍能发话。
	fmt.Println()
	fmt.Println("== 第 2 步：半关闭（CloseWrite）——FIN 只关写方向 ==")
	clientB := func() {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			fmt.Println("  [B] dial:", err)
			return
		}
		defer c.Close()
		fmt.Fprintf(c, "hi from client B\n")
		c.(*net.TCPConn).CloseWrite() // 发 FIN：我再不写了，但还能读
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		fmt.Printf("  [B 客户端] 半关闭后仍收到: %q (err=%v)\n", line, err)
	}
	go clientB()
	b, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	n, err := io.Copy(io.Discard, bufio.NewReader(b)) // 读到 EOF：收全存量数据
	fmt.Printf("B 的 FIN 已到（读到 EOF=%v），FIN 前的数据完整收到 %d 字节\n", err == nil, n)
	fmt.Fprintf(b, "server still talks to half-closed B\n")
	time.Sleep(100 * time.Millisecond)
	b.Close()

	fmt.Println()
	fmt.Println("== 第 3 步：netstat 看内核状态——谁主动关，谁进 TIME_WAIT ==")
	fmt.Println("(实测发现：macOS 的 lsof 看不到 TIME_WAIT——它没有属主 fd；netstat 直接读内核连接表)")
	showStates()

	fmt.Println()
	fmt.Println("== 第 4 步：SO_REUSEADDR——TIME_WAIT 不挡重听 ==")
	ln.Close()
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("重听失败（不该发生）：", err)
		return
	}
	ln2.Close()
	fmt.Println("关闭后立即重听同一端口成功——Go 的 ListenConfig 默认设置 SO_REUSEADDR；")
	fmt.Println("若没有它，bind 会撞上刚才的 TIME_WAIT 报 address already in use。")

	if *watch {
		fmt.Println()
		fmt.Println("== 第 5 步：TIME_WAIT 存活时长实测（-watch，约 30 秒）==")
		measureTimeWait()
	}
}

// showStates：对本实验端口跑 netstat，只保留 TCP 状态行。
// 注意：TIME_WAIT 的 socket 没有属主进程与 fd，lsof 看不到它，netstat 能看到。
func showStates() {
	out, err := exec.Command("netstat", "-an").Output()
	if err != nil {
		fmt.Println("netstat 不可用：", err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "."+port) && strings.Contains(line, "TIME_WAIT") {
			fmt.Println("  " + line)
		}
	}
}

// measureTimeWait：再主动关一条连接，每秒轮询 netstat 直到 TIME_WAIT 消失，
// 打印实测存活时长（macOS 默认 2*MSL = 30 秒量级，以实测为准）。
func measureTimeWait() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()
	closed := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(closed)
			return
		}
		io.Copy(io.Discard, c)
		c.Close() // 服务端主动关 → 服务端进 TIME_WAIT
		close(closed)
	}()
	cl, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	fmt.Fprintf(cl, "bye\n")
	cl.Close()
	<-closed
	fmt.Println("已由主动关闭方进入 TIME_WAIT，开始每秒轮询 netstat …")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := time.Now()
	for {
		out, _ := exec.Command("netstat", "-an").Output()
		// 只看本实验端口的条目：整个系统的 TIME_WAIT 别管（其他应用的连接常年在）。
		gone := true
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "."+port) && strings.Contains(line, "TIME_WAIT") {
				gone = false
				break
			}
		}
		if gone {
			fmt.Printf("TIME_WAIT 已消失，实测存活约 %d 秒\n", int(time.Since(start).Seconds()))
			return
		}
		select {
		case <-ctx.Done():
			fmt.Println("90 秒内未观察到 TIME_WAIT 消失，请手动复测")
			return
		case <-time.After(time.Second):
		}
	}
}
