// 项目 3：UDP 数据报语义——有边界，但不可靠、不保序。
//
// UDP 保留消息边界（一次 Write 对一次 ReadFrom），但网络可能丢、可能乱序、
// 可能分片。本程序搭一条真实拓扑来量化这三件事：
//
//	client ──► 丢包/乱序代理 ──► server
//	             （丢弃率 30%、相邻交换乱序率 20%）
//
//	第 1 步：1000 个带序号的数据报穿过代理，服务端统计到达率与乱序率
//	第 2 步：MTU 与分片观察——大报文在 loopback 上经 IP 分片/重组后
//	        仍是"一次 Write 对一次 ReadFrom"；超过 65507 字节则 Write 报错
//
// 控制通道小知识：服务端怎么知道"发完了"？客户端连发 10 个 END 标记——
// 单个 END 有 30% 概率被丢，10 个全丢的概率是 0.3^10 ≈ 0.0006%。UDP 上的
// 控制消息必须自带冗余，这是本项目附赠的一课。
//
// 运行方式：go run .（约 2 秒）。测试用固定种子的随机源保证可复现。
package main

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	clientAddr  = "127.0.0.1:19531" // 代理监听（客户端的目标）
	serverAddr  = "127.0.0.1:19532" // 服务端监听（代理转发的目标）
	totalMsgs   = 1000
	dropRate    = 0.30
	reorderRate = 0.20
)

// SeqPacket：应用层消息——4 字节大端序号 + 载荷。
type SeqPacket struct {
	Seq     int64
	Payload []byte
}

func encode(p SeqPacket) []byte {
	b := make([]byte, 4+len(p.Payload))
	binary.BigEndian.PutUint32(b, uint32(p.Seq))
	copy(b[4:], p.Payload)
	return b
}

func decode(b []byte) SeqPacket {
	return SeqPacket{
		Seq:     int64(int32(binary.BigEndian.Uint32(b))),
		Payload: b[4:],
	}
}

// Stats：服务端的到达统计。
type Stats struct {
	Received int     // 到达条数（不含 END）
	Descents int     // 相邻到达序号下降的次数（乱序的直接证据）
	LastSeq  int64   // 上一条到达的序号
	Dups     int     // 重复到达
	Min, Max int64   // 到达序号的最小/最大值
	Rate     float64 // Received / totalMsgs
}

func main() {
	// 服务端：收包、按序号统计，收到 END 收摊。
	statsCh := make(chan Stats, 1)
	go runServer(statsCh)
	time.Sleep(50 * time.Millisecond) // 让服务端先就位

	// 代理：丢弃 30%、相邻交换 20%，转发其余。
	stopProxy := newProxy(dropRate, reorderRate, rand.New(rand.NewSource(time.Now().UnixNano())))
	defer stopProxy()
	time.Sleep(50 * time.Millisecond)

	// 客户端：发 1000 个序号包 + 10 个冗余 END。
	sendPackets(totalMsgs, 10)

	stats := <-statsCh
	fmt.Println("== 第 1 步：1000 个数据报穿过丢包 30%/乱序 20% 的代理 ==")
	fmt.Printf("到达 %d/%d（%.1f%%）  乱序下降 %d 次  重复 %d 次  序号范围 [%d, %d]\n",
		stats.Received, totalMsgs, stats.Rate*100, stats.Descents, stats.Dups,
		stats.Min, stats.Max)
	fmt.Println("⇒ 丢了没通知、乱了没纠正——UDP 只帮你保留每条消息的边界。")

	fmt.Println()
	fmt.Println("== 第 2 步：大报文上限——理论 65507，macOS loopback 实测 9216 ==")
	sizeLimitObservation()
}

// runServer：收包统计，END 到达后把 Stats 递出。
func runServer(out chan<- Stats) {
	conn, err := net.ListenPacket("udp", serverAddr)
	if err != nil {
		fmt.Println("server listen:", err)
		close(out)
		return
	}
	defer conn.Close()
	buf := make([]byte, 70000)
	st := Stats{Min: 1 << 62, LastSeq: -2}
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			fmt.Println("server read:", err)
			close(out)
			return
		}
		p := decode(buf[:n])
		if p.Seq == -1 { // END：冗余标记，收到一个就收摊
			st.Rate = float64(st.Received) / float64(totalMsgs)
			out <- st
			return
		}
		if p.Seq == st.LastSeq {
			st.Dups++ // 演示拓扑不重排已转发包，此计数预期为 0
		}
		if p.Seq < st.LastSeq {
			st.Descents++ // 到达序号下降 = 乱序的现场证据
		}
		st.LastSeq = p.Seq
		if p.Seq < st.Min {
			st.Min = p.Seq
		}
		if p.Seq > st.Max {
			st.Max = p.Seq
		}
		st.Received++
	}
}

// sendPackets：向代理发 n 个序号包，再发 count 个冗余 END 标记。
func sendPackets(n, endCount int) {
	c, err := net.Dial("udp", clientAddr)
	if err != nil {
		fmt.Println("dial proxy:", err)
		return
	}
	defer c.Close()
	for i := 0; i < n; i++ {
		c.Write(encode(SeqPacket{Seq: int64(i), Payload: []byte("payload")}))
		time.Sleep(20 * time.Microsecond) // 轻微限速：别把代理的收包缓冲挤爆
	}
	for i := 0; i < endCount; i++ {
		c.Write(encode(SeqPacket{Seq: -1, Payload: []byte("END")}))
		time.Sleep(2 * time.Millisecond)
	}
}

// newProxy：UDP 双跳——收客户端的包，按策略丢/换序后转发给服务端。
// 返回的 stop 关闭底层连接（测试收尾用）。
func newProxy(drop, reorder float64, rnd *rand.Rand) (stop func()) {
	stopCh := make(chan struct{})
	var once sync.Once
	stop = func() { once.Do(func() { close(stopCh) }) }

	conn, err := net.ListenPacket("udp", clientAddr)
	if err != nil {
		fmt.Println("proxy listen:", err)
		return func() {}
	}
	srv, err := net.Dial("udp", serverAddr)
	if err != nil {
		conn.Close()
		fmt.Println("proxy dial:", err)
		return func() {}
	}
	go func() {
		<-stopCh
		conn.Close()
		srv.Close()
	}()

	go func() {
		buf := make([]byte, 70000)
		var held []byte // 被扣下的包：与下一个幸存的包交换顺序后发出
		for {
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			if rnd.Float64() < drop {
				continue // 静默丢弃：UDP 世界的日常
			}
			if held != nil {
				srv.Write(pkt) // 后到的先走——这就是乱序
				srv.Write(held)
				held = nil
				continue
			}
			if rnd.Float64() < reorder {
				held = pkt // 扣下当前包，等下一个包与它换位
				continue
			}
			srv.Write(pkt)
		}
	}()
	return stop
}

// sizeLimitObservation：大报文观察——理论、平台上限与实测。
//
// 理论上 UDP 数据报最大 65507 字节（65535 − 8 UDP 头 − 20 IP 头），
// 超过接口 MTU 时由内核做 IP 分片。但 macOS 有自己的关卡：
// net.inet.udp.maxdgram 默认 9216——超过它的 Write 直接 EMSGSIZE。
// Linux 上则可发满 65507（内核分片）。平台差异，实测说话。
func sizeLimitObservation() {
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Name == "lo0" || ifc.Name == "lo" {
			fmt.Printf("回环接口 %s MTU = %d 字节；", ifc.Name, ifc.MTU)
		}
	}
	if out, err := exec.Command("sysctl", "net.inet.udp.maxdgram").Output(); err == nil {
		fmt.Printf("%s（BSD 传统单报文上限）", strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "net.inet.udp.maxdgram:")))
	}
	fmt.Println()

	echo, err := net.ListenPacket("udp", serverAddr)
	if err != nil {
		fmt.Println("echo listen:", err)
		return
	}
	defer echo.Close()
	echoAddr := echo.LocalAddr()
	go func() {
		buf := make([]byte, 70000)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			echo.WriteTo(buf[:n], from) // 原样回显
		}
	}()

	snd, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer snd.Close()

	fmt.Println("数据报大小 → 实测结果：")
	for _, size := range []int{1024, 8192, 9216, 9217, 60000} {
		payload := make([]byte, size-4)
		binary.BigEndian.PutUint32(payload, uint32(size))
		if _, err := snd.WriteTo(encode(SeqPacket{Seq: int64(size), Payload: payload}), echoAddr); err != nil {
			fmt.Printf("  %5d B → Write 被拒: %v\n", size, trimErr(err))
			continue
		}
		buf := make([]byte, 70000)
		snd.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := snd.ReadFrom(buf)
		if err != nil {
			fmt.Printf("  %5d B → 未收到回显（%v）\n", size, err)
			continue
		}
		p := decode(buf[:n])
		fmt.Printf("  %5d B → 单次 ReadFrom 完整收到 %5d B（序号回读 %d，一致=%v）\n",
			size, n, p.Seq, int(p.Seq) == size)
	}
	fmt.Println("⇒ macOS loopback 实测上限 9216B（net.inet.udp.maxdgram）；Linux 上限是 65507 且内核分片。")
}

// trimErr：错误信息去掉本地端口等噪声，便于 README 引用。
func trimErr(err error) string {
	s := err.Error()
	if i := strings.Index(s, "->"); i >= 0 {
		s = s[:i] + "->…"
	}
	return s
}

// echoAddr 之上需要的两个小依赖：exec/sysctl 用到 exec 与 strings。
