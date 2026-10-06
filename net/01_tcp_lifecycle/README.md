# 01 · TCP 连接的一生：把状态机的每一跳对到代码行上

> 教科书讲"三次握手、四次挥手"，本篇回答的是它们落在 Go 代码的哪一行：握手藏在内核的 Accept 里，挥手从 Close 开始，TIME_WAIT 归主动关的一方。本篇用 netstat 亲眼观测全部状态流转，并实测 macOS 上 TIME_WAIT 的存活时长（约 31 秒）。

## Background

没有 socket 抽象之前，写网络程序意味着直接与操作系统内核的网络栈打交道：自己管理文件描述符、自己解析内核返回的错误码、自己应付字节的到达时机。

1983 年的 BSD socket API 把这些收拢成 `socket/bind/listen/accept/read/write/close` 一组调用，此后所有语言的网络库都是它的后裔。

但抽象带来了新的困惑：连接是内核里的一个状态机，代码里只有一个不透明的 `net.Conn`。线上最常见的一类问题——"连接为什么挂住""为什么突然 RST""端口为什么绑不上"——答案都在状态机里，却不在代码里。抓包工具（tcpdump/Wireshark）能看但要 sudo、要懂协议头，对排第一行错误来说太重。

Go 的 `net` 包做了两层事：把 BSD socket 封装成 `net.TCPListener`/`net.TCPConn`，再把阻塞读写的 fd 挂到运行时的网络轮询器上（这一层的调度行为见 concurrency/13）。本篇用最小代码 + `netstat`，把被抽象藏起来的状态机摆回眼前。

## What

TCP 连接是一种由四元组（源 IP、源端口、目的 IP、目的端口）标识的、两端各自维护状态的可靠字节流通道。它的行为由一个有限状态机决定，代码里的每个调用只是推动状态转移的事件。

一句话心智模型：**连接是一根两端各带独立阀门的水管——`Close` 是把整根管子拆掉，`CloseWrite` 是只关自己这头的写阀，水龙头关掉后管里存的水（已发送未读完的数据）照样能流到对端**。

但和真实水管不同的是，两端的"关闭"需要协商：先拆的一方要等对方也拆完，还要多等一个 2MSL（约 30 秒）的观察期才能彻底消失——这就是 TIME_WAIT。

核心状态与代码的对应：

| 状态 | 谁处于此态 | 代码里的对应 |
|---|---|---|
| LISTEN | 服务端 | `net.Listen` 之后 |
| ESTABLISHED | 双方 | `Accept` 返回 / `Dial` 返回之后 |
| FIN_WAIT/TIME_WAIT | 主动关的一方 | 第一个调用 `Close` 的一方 |
| CLOSE_WAIT | 被动关的一方 | Read 到 EOF 之后还没 Close 的时间窗 |

## When to Use

直接使用 `net.TCPListener`/`net.TCPConn`（而不是更高层封装）适合这些时刻。

- **自定义应用层协议时**：IM、游戏、IoT 网关——消息边界、心跳、编解码都由你定义（第二、三阶段的前置能力）
- **写代理与网关时**：需要同时扮演服务端与客户端、改写字节流（项目 13 的地基）
- **排查连接类故障时**：能读懂状态机，才能从一条 netstat 输出判断问题在谁身上

何时不用：业务就是 HTTP 请求响应时，`net/http` 已经把连接管理、超时、协议细节做对，直接用它（第三阶段）；需要跨语言的强类型 RPC 时，gRPC 更省力（第五阶段）。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| net.TCPConn（本篇） | 完全控制字节流与生命周期 | 自定义协议/代理/学习 |
| net/http | 自动管连接、协议固定 | 请求-响应型业务 |
| gRPC | 强类型、流式、生态完整 | 微服务间调用 |

## Quick Start

前置条件：Go 1.21+，macOS/Linux（`netstat` 观测）。运行：

```bash
cd net/01_tcp_lifecycle
go run .            # 快速演示（4 步）
go run . -watch     # 追加第 5 步：实测 TIME_WAIT 存活时长（约 30 秒）
```

快速演示的真实输出（客户端端口等细节每次运行不同）：

```text
== 第 1 步：Listen 与 Accept——三次握手藏在内核里 ==
Listen 已返回（bind+listen 完成），等待 Accept：127.0.0.1:19501
此刻握手还没发生——它由客户端 connect 触发、内核代劳，Accept 只取结果。
Accept 完成（握手已由内核做完）。收到: hi from client A
服务端已主动 Close（第一个 FIN 发出）——稍后 netstat 里它应显示 TIME_WAIT

== 第 2 步：半关闭（CloseWrite）——FIN 只关写方向 ==
  [A 客户端] 收到: hello from server
  [A 客户端] 收到: goodbye from server
  [A 客户端] 第三次 Read 返回 EOF —— 这就是对端 Close 发来的 FIN
B 的 FIN 已到（读到 EOF=true），FIN 前的数据完整收到 17 字节
  [B 客户端] 半关闭后仍收到: "server still talks to half-closed B\n" (err=<nil>)

== 第 3 步：netstat 看内核状态——谁主动关，谁进 TIME_WAIT ==
(实测发现：macOS 的 lsof 看不到 TIME_WAIT——它没有属主 fd；netstat 直接读内核连接表)
  tcp4       0      0  127.0.0.1.19501        127.0.0.1.61144        TIME_WAIT
  tcp4       0      0  127.0.0.1.61145        127.0.0.1.19501        TIME_WAIT

== 第 4 步：SO_REUSEADDR——TIME_WAIT 不挡重听 ==
关闭后立即重听同一端口成功——Go 的 ListenConfig 默认设置 SO_REUSEADDR；
若没有它，bind 会撞上刚才的 TIME_WAIT 报 address already in use。
```

`-watch` 的第 5 步实测（本机 macOS，MSL=15 秒）：

```text
== 第 5 步：TIME_WAIT 存活时长实测（-watch，约 30 秒）==
已由主动关闭方进入 TIME_WAIT，开始每秒轮询 netstat …
TIME_WAIT 已消失，实测存活约 31 秒
```

测试验收（`-race` 下连跑 5 轮全绿）：

```bash
go test -race -count=5 .
```

## How It Works

### 握手在内核：Accept 的真实语义

`net.Listen` 完成的是 bind + listen：端口进入 LISTEN 态，内核开始替你完成三次握手——完成的连接放进"就绪队列"。

`Accept` 只是从队列里取出一条已建立（ESTABLISHED）的连接。所以演示里"Listen 已返回"之后、客户端 `Dial` 之前，握手确实一次都没发生；握手次数不取决于 Accept 调用的快慢，没取走的连接会在队列里排队（长度即 backlog）。

### 挥手从 Close 开始：两条不对称的路径

谁先调 `Close`，谁发出第一个 FIN，谁就是"主动关"的一方——FIN_WAIT → TIME_WAIT 是它的专属路径；被动方 Read 到 EOF（`io.EOF`）后调 `Close`，走 CLOSE_WAIT → LAST_ACK，直接消失。

两条路在输出里都出现了：A 场景服务端主动关，netstat 显示 `.19501` 在 TIME_WAIT；B 场景客户端先 `CloseWrite`（半关闭也算先发 FIN），netstat 显示客户端端口在 TIME_WAIT。

半关闭是这条规则最实用的推论：`CloseWrite` 发出 FIN 只声明"我写完了"，读方向仍然打开——服务端在收到 FIN 后照样把回话发完，客户端也照样收到（输出第 2 步最后一行）。

### TIME_WAIT 为什么存在，为什么是 30 秒

TIME_WAIT 只留给主动关的一方，原因有二：其一，它发出的最后一个 ACK 可能丢，对方重发 FIN 时它必须有能力重答 ACK；其二，让这条四元组在网络里"冷却"，避免相同四元组的新连接收到旧连接的迟到报文。

时长是 2 倍 MSL（报文最大存活时间）：macOS 默认 `net.inet.tcp.msl = 15000`，即 2×15 秒——与实测的约 31 秒吻合（轮询间隔带来 ±1 秒）。

还有一个观测层的坑值得单独立碑：**macOS 的 `lsof` 看不到 TIME_WAIT**。TIME_WAIT 的 socket 不属于任何进程、没有文件描述符，而 lsof 的视角是"进程打开了什么"；`netstat` 直接读内核连接表，所以能看见。本篇演示与测试全部用 netstat。

## Pitfalls & Q&A

- **以为 lsof 能看 TIME_WAIT**：现象是 `lsof -i :端口` 只看到 LISTEN。原因如上——TIME_WAIT 无属主 fd。解法：用 `netstat -an | grep 端口`（本篇实测替换，清单验收措辞已随之更正）
- **Read 读到一半的错误**：现象是服务端第一次 `Read` 返回 nil（读到了数据），以为没有异常。原因：客户端 RST 到达前，先到达的是它发出的存量数据。解法：循环读到出错——下一读才是 `*net.OpError`（ECONNRESET），本项目测试实录
- **"不 Close 直接退出"怎么稳定复现**：进程退出时内核会替它补发 FIN，那是优雅关闭。解法：`SetLinger(0)` 后 Close——跳过挥手直接 RST，这才是"当场死亡"，也是验收②的可复现形式
- **测试串行跑挂在自己的 TIME_WAIT 上**：现象是重跑测试报 address already in use。原因：TIME_WAIT 占着端口。解法：Go 默认 SO_REUSEADDR 已兜底 listen；测试用不同端口分段（本项目 19511~19514）

**Q：TIME_WAIT 堆积成灾怎么办？**
A：它只占内核内存（每条约 1KB 量级），不占应用 fd；数万条无碍。

真正的解法依次是：连接由客户端先关改为服务端保持长连接复用（把 TIME_WAIT 挪到客户端侧的临时端口，天然分散）、调大临时端口范围、最后才是内核参数（macOS 无需、Linux 有 `tcp_tw_reuse`）。

**Q：CLOSE_WAIT 堆积说明什么？**
A：说明对端已关（你 Read 到了 EOF），而你的代码一直没调 Close——几乎总是应用 bug（连接被遗忘在某张表里）。TIME_WAIT 在主动方是正常的，CLOSE_WAIT 在被动方长期存在是病的，这条区别是排障的第一判断。

**Q：RST 什么时候会出现？**
A：常见四种：`SetLinger(0)` 后 Close；向已被对端关闭的连接继续写（第一次写触发对端 RST）；进程死亡被内核清理时残留待发数据；中间设备（防火墙/NAT）主动拆连。前两种本篇代码可复现，后两种出现在真实网络里。

**Q：半关闭有什么真实用途？**
A：HTTP/1.0 短连接"发完请求就 CloseWrite，服务端读到 EOF 知道请求结束再回整个响应"；`shutdown -w` 式的管道收尾；以及流式上传结束的信号。它的本质是给字节流补一个"我说完了"的边界——这正是项目 2 要解决的粘包问题的原型。
