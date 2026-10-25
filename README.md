# hands-on-go：用 144 个能真实跑通的实验，吃透 Go 底层、并发与 Web 服务

不背概念，动手看真相。每个实验都是**一个可运行的 Go 程序 + 一份六段式技术博客 README**（
Background → What → When to Use → Quick Start → How It Works → Pitfalls & Q&A）：
clone 下来 → 每个实验 `go run` 即跑 → 对照 README 里的真实输出，亲眼验证每一个结论。
internals/patterns/concepts 三条线只用标准库；concurrency 线附 `-race` 测试与 goleak 泄漏门禁；
net 线聚焦协议与传输（独立模块，-race + goleak）；web 线用真实依赖共同生长出书签服务 marklet。

## 六条学习线

| 目录 | 定位 | 内容 |
|---|---|---|
| [`internals/`](internals/) | **语言底层**（30 篇） | slice/map/channel 的内存布局、GMP 调度、分配器与 GC、pprof/trace 工具链 |
| [`patterns/`](patterns/) | **手写组件与并发模式**（20 篇） | LRU/LFU、限流器、Worker Pool、分布式锁、迷你 Redis 等常用组件的完整实现 |
| [`concepts/`](concepts/) | **语言概念**（30 篇） | 变量与类型、流程控制、goroutine/channel/select、错误处理、context、反射——语言特性逐个击破 |
| [`concurrency/`](concurrency/) | **并发原理深水区**（23 篇） | happens-before 实证、sync 源码级复刻、GMP 调度深水区、channel 运行时、取消传播、并发度量——每个实验附 `-race` 测试 |
| [`net/`](net/) | **网络编程**（21 篇） | TCP/UDP 语义、协议设计与 fuzz、HTTP 深水区、TLS/PKI、gRPC/WebSocket——附 `-race` 与 goleak |
| [`web/`](web/) | **Web 服务实战**（20 篇 ✅） | marklet 书签服务共同生长：SQLite→PG 持久层、事务、连接池、缓存、认证、可观测、异步、Docker 交付 |

每个实验目录内（按线的差异见注）：

```text
NN_topic/
├── README.md          # 主教材：六段式（Background → What → When to Use → Quick Start → How It Works → Pitfalls & Q&A）
├── main.go / xxx.go   # 演示程序：每一步"在看什么"都有注释，部分支持 -json 输出状态快照
├── *_test.go          # concurrency/net/web 三线：-race 可过测试（web 另含 goleak 门禁）
├── viz.py             # （仅 internals/patterns/concepts 的数据实验）从 -json 快照绘制实测数据图
└── images/            # （同上三线）补充图：图源 JSON、交互 HTML、SVG/PNG，可本地打开
```

> concurrency/net/web 三线为独立 Go 模块（各自 go.mod），无 viz.py/images；
> web 线另有随实验生长的部署文件（Dockerfile、docker-compose.yml、脚本与 CI）。

> 🌐 交互图为补充材料（仅 internals/patterns/concepts 三线有 images/）：本地双击对应
> `images/*.html` 即可打开；开启 GitHub Pages 后也可在线访问，形如
> `https://<user>.github.io/<repo>/internals/01_slices/images/sharing.html`。

## 新增实验

想给系列加一篇？在对应线（六条线任一）末尾按 `NN_短名` 追加目录：
README 用与现有实验相同的六段式结构（Background → What → When to Use → Quick Start → How It Works →
Pitfalls & Q&A），真实输出块逐字摘自程序运行结果；可选地用 matplotlib 从 `-json` 快照绘制数据图到
`images/`，最后在本文件实验表加一行。
配套的学习清单与规划见 [docs/](docs/)（go_internals / go_patterns / go_concepts / go_concurrency /
go_net——21/21 已完成 / go_web——marklet 服务线，20/20 已完成）。

## 环境要求

- Go ≥ 1.21（实验在 go1.26 实测通过）；internals/patterns/concepts 纯标准库，无第三方依赖
- concurrency 系列：独立 Go 模块（`concurrency/go.mod`），依赖 go.uber.org/goleak 与 golang.org/x/sync（对照用），测试统一 `go test -race ./NN_xxx/`
- net 系列：独立 Go 模块（`net/go.mod`），依赖 grpc/protobuf/goleak，测试统一 `go test -race ./NN_xxx/`
- web 系列：独立 Go 模块（`web/go.mod`），依赖 modernc.org/sqlite、pgx、go-redis、
  OTel、golang-jwt、x/crypto 与 goleak；`-race` 测试需 C 编译器（macOS 自带）；
  服务端口纪律 127.0.0.1:19610~19639，PostgreSQL/Redis 走 Docker
  （postgres:15-alpine / redis:7-alpine，镜像已在本地）；
  交付实验（19）含 Dockerfile 与 docker-compose.yml，CI 门禁见 `web/20_release/scripts/ci.sh`
- 可选：python3 + matplotlib（仅重新生成实测数据图时需要；仓库已含生成好的 PNG）
- 操作系统不限；涉及地址/内存布局与性能计时的输出数值每次运行都会变，不变的是关系与结论

## 实验总目录

### internals · 语言底层（30 篇）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [slices](internals/01_slices/) | 三字段头、共享别名与 append 分离 |
| 02 | [map](internals/02_map/) | 哈希定位、tophash 预筛、溢出链与负载因子扩容 |
| 03 | [channel](internals/03_channel/) | 带锁环形队列 hchan 的收发、阻塞与回绕 |
| 04 | [string](internals/04_string/) | 两字段只读头、切片共享与 []byte 互转拷贝 |
| 05 | [interface](internals/05_interface/) | eface/iface 两字布局、itab 缓存与 nil 陷阱 |
| 06 | [array_vs_slice](internals/06_array_vs_slice/) | 值内联 vs 24 字节头的内存布局对比 |
| 07 | [goroutine](internals/07_goroutine/) | 可扩容独立栈：台阶式增长与栈隔离 |
| 08 | [mutex](internals/08_mutex/) | state 位布局、自旋窗口与饥饿模式 |
| 09 | [atomic](internals/09_atomic/) | CAS 重试循环与 release/acquire 内存屏障 |
| 10 | [waitgroup](internals/10_waitgroup/) | 一个 64 位 state 字 + 一个 sema 的事件线 |
| 11 | [pool](internals/11_pool/) | sync.Pool 的 per-P 分片与 GC 两拍清理 |
| 12 | [once](internals/12_once/) | done 原子位快路径 + 慢路径互斥锁 |
| 13 | [escape](internals/13_escape/) | 逃逸分析：编译器到底把变量放哪 |
| 14 | [allocator](internals/14_allocator/) | mcache/mcentral/mheap 三级分配 |
| 15 | [gc](internals/15_gc/) | 三色标记、写屏障与并发回收 |
| 16 | [alloc_bench](internals/16_alloc_bench/) | ns/op 与 allocs/op 的实测账本 |
| 17 | [align](internals/17_align/) | 内存对齐：字段顺序能省 1/3 内存 |
| 18 | [gmp](internals/18_gmp/) | G/M/P 调度：本地队列与 work stealing |
| 19 | [syscall](internals/19_syscall/) | 阻塞 syscall 时 M 怎么涨、P 怎么移交 |
| 20 | [signal](internals/20_signal/) | 信号处理与 panic 的栈展开 |
| 21 | [cgo](internals/21_cgo/) | C.malloc 的内存 GC 管不着：边界与归属 |
| 22 | [pprof](internals/22_pprof/) | CPU 热点定位与优化闭环 |
| 23 | [inline](internals/23_inline/) | 内联决策：`-gcflags=-m` 说了算 |
| 24 | [closure](internals/24_closure/) | 闭包捕获的是引用：变量逃逸到堆 |
| 25 | [defer](internals/25_defer/) | 逆序收拢、立即求值、几乎白送的开销 |
| 26 | [iface](internals/26_iface/) | 动态派发的真实开销：挡住了优化 |
| 27 | [reflect](internals/27_reflect/) | Type 与 Value：元数据到动态调用 |
| 28 | [trace](internals/28_trace/) | runtime/trace 看清 goroutine 卡在哪 |
| 29 | [leak](internals/29_leak/) | goroutine/内存泄漏的成因与检测 |
| 30 | [runtime](internals/30_runtime/) | 手写最小运行时：GMP + 两级分配 + 标记清扫 |

### patterns · 手写组件与并发模式（20 篇）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [lru_cache](patterns/01_lru_cache/) | 哈希表 + 双向链表的 O(1) LRU |
| 02 | [lfu_cache](patterns/02_lfu_cache/) | 频次桶 + 双向链表的 O(1) LFU |
| 03 | [read4k](patterns/03_read4k/) | 用 4K 缓冲实现多次调用的 ReadN |
| 04 | [trie](patterns/04_trie/) | 前缀树与自动补全 |
| 05 | [bloom_filter](patterns/05_bloom_filter/) | k 个哈希 + 位数组的存在性判断 |
| 06 | [ring_buffer](patterns/06_ring_buffer/) | 固定内存的环形队列与回绕 |
| 07 | [token_bucket](patterns/07_token_bucket/) | 恒速补充、容量上限的令牌桶 |
| 08 | [worker_pool](patterns/08_worker_pool/) | 固定 worker 的任务池与背压 |
| 09 | [producer_consumer](patterns/09_producer_consumer/) | 缓冲 channel 上的生产者消费者 |
| 10 | [shard_map](patterns/10_shard_map/) | 分片降低锁竞争的并发 Map |
| 11 | [pubsub](patterns/11_pubsub/) | topic 扇出的发布订阅 Broker |
| 12 | [conn_pool](patterns/12_conn_pool/) | 连接复用：空闲队列 + 信号量 + 健康检查 |
| 13 | [consistent_hash](patterns/13_consistent_hash/) | 哈希环与虚拟节点 |
| 14 | [dist_lock](patterns/14_dist_lock/) | Redis 锁：TTL 过期与误删防护 |
| 15 | [http_router](patterns/15_http_router/) | 基于方法树参数匹配的迷你路由 |
| 16 | [graceful_shutdown](patterns/16_graceful_shutdown/) | 信号驱动的优雅关闭时间线 |
| 17 | [mini_redis](patterns/17_mini_redis/) | 手写 RESP 协议的 KV 服务器 |
| 18 | [rate_limiter](patterns/18_rate_limiter/) | 滑动窗口限流中间件 |
| 19 | [task_scheduler](patterns/19_task_scheduler/) | 优先队列定时任务调度器 |
| 20 | [concurrent_lru](patterns/20_concurrent_lru/) | 分片并发的 LRU 缓存 |

### concepts · 语言概念（30 篇）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [vars](concepts/01_vars/) | 变量、常量与类型推断 |
| 02 | [flow](concepts/02_flow/) | 流程控制：if/switch/for 三形态 |
| 03 | [arrays_maps](concepts/03_arrays_maps/) | 数组、切片与 map：词频统计 |
| 04 | [funcs_defer](concepts/04_funcs_defer/) | 函数、多返回值与 defer |
| 05 | [struct_iface](concepts/05_struct_iface/) | 结构体、方法与接口多态 |
| 06 | [goroutine_basics](concepts/06_goroutine_basics/) | goroutine 启动与 WaitGroup |
| 07 | [closure_trap](concepts/07_closure_trap/) | 循环变量闭包陷阱与修正 |
| 08 | [leak_detect](concepts/08_leak_detect/) | goroutine 泄漏场景与检测 |
| 09 | [runtime_info](concepts/09_runtime_info/) | 栈打印、Goexit 与 goid |
| 10 | [gomaxprocs](concepts/10_gomaxprocs/) | GOMAXPROCS 并行度实测 |
| 11 | [worker_pool](concepts/11_worker_pool/) | channel 版任务池 |
| 12 | [unbuffered_chan](concepts/12_unbuffered_chan/) | 无缓冲 channel 的同步握手 |
| 13 | [buffered_chan](concepts/13_buffered_chan/) | 有缓冲 channel 的三种状态 |
| 14 | [directional_chan](concepts/14_directional_chan/) | 单向 channel 的类型约束 |
| 15 | [select](concepts/15_select/) | 多路复用、超时与非阻塞 |
| 16 | [chan_close](concepts/16_chan_close/) | 关闭语义与广播退出 |
| 17 | [chan_semaphore](concepts/17_chan_semaphore/) | channel 信号量限流 |
| 18 | [producer_consumer](concepts/18_producer_consumer/) | 多生产多消费与关闭顺序 |
| 19 | [pipeline](concepts/19_pipeline/) | 三级流水线模式 |
| 20 | [fan_in_out](concepts/20_fan_in_out/) | 扇出与扇入合并 |
| 21 | [mutex_race](concepts/21_mutex_race/) | 竞态、锁与 atomic 对比 |
| 22 | [context](concepts/22_context/) | 取消传播与超时控制 |
| 23 | [error_basics](concepts/23_error_basics/) | error 接口与哨兵错误 |
| 24 | [error_chain](concepts/24_error_chain/) | 自定义 error 与错误链 |
| 25 | [panic_recover](concepts/25_panic_recover/) | panic 捕获与恢复 |
| 26 | [iface_assert](concepts/26_iface_assert/) | 接口组合与类型断言 |
| 27 | [reflect_basics](concepts/27_reflect_basics/) | reflect.Type 与 Value |
| 28 | [struct_tags](concepts/28_struct_tags/) | 结构体标签解析与校验 |
| 29 | [reflect_call](concepts/29_reflect_call/) | 反射创建实例与动态调用 |
| 30 | [custom_marshal](concepts/30_custom_marshal/) | 自定义 JSON 序列化 |

### concurrency · 并发原理深水区（23 篇，附 -race 测试）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [hb_litmus](concurrency/01_hb_litmus/) | happens-before 探针：litmus 实证重排与同步的可见性承诺 |
| 02 | [atomic_orders](concurrency/02_atomic_orders/) | atomic 语义层级：SeqCst 与 Acquire/Release 配对 |
| 03 | [false_sharing](concurrency/03_false_sharing/) | 伪共享实测：缓存行填充拉开数量级差距 |
| 04 | [race_detector](concurrency/04_race_detector/) | race 检测器：vector clock、漏报边界与开销实测 |
| 05 | [mini_mutex](concurrency/05_mini_mutex/) | 盲写 mini Mutex：双模式状态机并对照源码 |
| 06 | [mini_rwmutex](concurrency/06_mini_rwmutex/) | mini RWMutex：readerCount 负数协议与写优先 |
| 07 | [mini_cond](concurrency/07_mini_cond/) | mini Cond：通知列表与丢失唤醒反例 |
| 08 | [syncmap_internals](concurrency/08_syncmap_internals/) | sync.Map 源码级：read/dirty 协议与 HashTrieMap 现状 |
| 09 | [singleflight](concurrency/09_singleflight/) | singleflight 手写：请求合并、panic 传播与 x/sync 对照 |
| 10 | [g_trace](concurrency/10_g_trace/) | G 状态机追踪：go tool trace 解读 G 的一生 |
| 11 | [work_stealing](concurrency/11_work_stealing/) | work-stealing 实证：runnext、本地队列与全局队列分工 |
| 12 | [preemption](concurrency/12_preemption/) | 抢占演进：协作式到信号抢占的 10ms 边界 |
| 13 | [netpoller_c10k](concurrency/13_netpoller_c10k/) | netpoller 与 C10K：万级连接的 park/unpark 与内存账 |
| 14 | [sched_latency](concurrency/14_sched_latency/) | 调度延迟：runtime/metrics 的 p50/p99 双口径测量 |
| 15 | [mini_channel](concurrency/15_mini_channel/) | mini channel：锁+环形缓冲+等待队列的运行时复刻 |
| 16 | [select_fairness](concurrency/16_select_fairness/) | select 均匀性：selectgo 随机轮询与两阶段锁排序 |
| 17 | [generics_prims](concurrency/17_generics_prims/) | 泛型并发原语库：Chan/Pipe/Merge/Split |
| 18 | [mini_context](concurrency/18_mini_context/) | mini context：cancel 树传播与超时级联 |
| 19 | [errgroup](concurrency/19_errgroup/) | errgroup 手写：首错取消、SetLimit 与结构化并发 |
| 20 | [goleak_gate](concurrency/20_goleak_gate/) | goleak 泄漏门禁：goroutine 快照 diff 挂上测试 |
| 21 | [counter_bench](concurrency/21_counter_bench/) | 计数器三路基准：mutex/atomic/分片填充五档矩阵 |
| 22 | [load_shapes](concurrency/22_load_shapes/) | 压测方法论：三种负载形状与并发组件验收清单 |
| 23 | [task_framework](concurrency/23_task_framework/) | 收官框架：守恒律验收的有界并发任务框架 |

### net · 网络编程（21 篇，附 -race 测试与 goleak 门禁）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [tcp_lifecycle](net/01_tcp_lifecycle/) | TCP 状态机的每一跳对到代码行 |
| 02 | [sticky_frames](net/02_sticky_frames/) | 粘包拆包：TCP 是字节流不是消息流 |
| 03 | [udp_datagram](net/03_udp_datagram/) | UDP：有边界，但不可靠、不保序 |
| 04 | [stop_wait](net/04_stop_wait/) | 在 UDP 上手写停等协议 |
| 05 | [binary_protocol](net/05_binary_protocol/) | 魔数、版本、长度与 CRC 的四道闸 |
| 06 | [proto_fuzz](net/06_proto_fuzz/) | 畸形包矩阵与 fuzz 靶场 |
| 07 | [text_vs_binary](net/07_text_vs_binary/) | 文本协议的调试性 vs 二进制的解析开销 |
| 08 | [mux_pipeline](net/08_mux_pipeline/) | 单连接多路复用与队头阻塞量化 |
| 09 | [http_server_internals](net/09_http_server_internals/) | net/http 超时参数、慢攻击与连接状态机 |
| 10 | [transport_pool](net/10_transport_pool/) | http.Transport 连接池与连接泄漏 |
| 11 | [http2_mux](net/11_http2_mux/) | HTTP/2 一条连接扛住全部并发 |
| 12 | [middleware_ctx](net/12_middleware_ctx/) | 中间件洋葱、断连感知与 panic 兜底 |
| 13 | [reverse_proxy](net/13_reverse_proxy/) | 迷你网关：轮询、健康检查与 XFF 链 |
| 14 | [tls_handshake](net/14_tls_handshake/) | TLS 握手观察与证书链全景 |
| 15 | [ca_chain](net/15_ca_chain/) | 自建 CA：根、中间、叶子三级信任 |
| 16 | [mtls](net/16_mtls/) | mTLS 双向认证 |
| 17 | [tls_diagnostics](net/17_tls_diagnostics/) | TLS 故障诊断：四现场、双口径、决策树 |
| 18 | [protobuf_wire](net/18_protobuf_wire/) | 手写 varint、zigzag 与 tag |
| 19 | [grpc_streams](net/19_grpc_streams/) | gRPC 四种流式 |
| 20 | [ws_rfc6455](net/20_ws_rfc6455/) | 手写 WebSocket：握手、帧、心跳 |
| 21 | [mini_rpc](net/21_mini_rpc/) | 迷你 RPC 框架：协议、安全与元数据合流 |

### web · Web 服务实战（marklet，规划 20 篇·已完成 4 篇）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [skeleton](web/01_skeleton/) | 方法路由、双探针分离与 SIGTERM 优雅退出 |
| 02 | [sqlite_store](web/02_sqlite_store/) | 版本表迁移器、WAL 并发与参数化 CRUD |
| 03 | [conn_pool](web/03_conn_pool/) | 池三旋钮实测对照，WaitCount 对账 |
| 04 | [tx_crud](web/04_tx_crud/) | BEGIN IMMEDIATE 防丢更新，WithTx 与 CRUD 接线 |
| 05 | [pg_migration](web/05_pg_migration/) | 同接口换 PG 实现，方言迁移幂等 |
| 06 | [sqlc_gen](web/06_sqlc_gen/) | SQL 即源码：生成码与手写的三层防线对照 |
| 07 | [redis_cache](web/07_redis_cache/) | 恰好一次回源防击穿，空值缓存防穿透 |
| 08 | [cache_consistency](web/08_cache_consistency/) | 两种坏例复现与锁内失效的零脏读 |
| 09 | [auth_password_session](web/09_auth_password_session/) | argon2id 落库、Redis 会话与登录轮换 |
| 10 | [jwt](web/10_jwt/) | HS256 签发验证与三个陷阱复现 |
| 11 | [security_middleware](web/11_security_middleware/) | 安全头、Bearer、CSRF 双提交与请求体上限 |
| 12 | [auth_wiring](web/12_auth_wiring/) | owner 过滤防 IDOR：身份贯穿到 WHERE 子句 |
| 13 | [obs_logging](web/13_obs_logging/) | 请求 ID 贯通、slog JSON 与字段脱敏 |
| 14 | [obs_metrics](web/14_obs_metrics/) | 手写 exposition、直方图分位数对照、pprof 隔离 |
| 15 | [obs_tracing](web/15_obs_tracing/) | 父子 span、W3C 透传与 10% 采样 |
| 16 | [obs_ratelimit](web/16_obs_ratelimit/) | ZSET 滑动窗口 Lua 原子限流，四件套联动 |
| 17 | [jobs_queue](web/17_jobs_queue/) | worker 池、重试死信与 SSRF 防线 |
| 18 | [graceful_drain](web/18_graceful_drain/) | 四阶段停机编排：50 任务零丢失可审计 |
| 19 | [docker_deploy](web/19_docker_deploy/) | 多阶段构建 28MB 运行镜像，Compose 全家桶 |
| 20 | [release](web/20_release/) | 五步 CI、压测基线入档与发布清单 |

规划与学习路线见 [docs/go_web.md](docs/go_web.md)（20/20 已完成）。

## 建议学习路线

1. **数据结构底层**（internals 01→06）：slice/map/channel/string/interface 的内存真相，后续一切的地基
2. **并发原语**（internals 07→12，再 18→19）：goroutine 栈、锁/原子/WaitGroup/Pool/Once、GMP 与 syscall
3. **内存与运行时**（internals 13→17、20→21、30）：逃逸、分配器、GC、对齐、信号与 CGO，最后手写迷你运行时自检
4. **工程化与性能**（internals 22→29）：pprof/trace/inline/defer/reflect/泄漏检测，学会用工具说话
5. **手写组件**（patterns 01→20）：每篇亲手实现，写完对照 README 的坑清单与 Q&A 复盘复杂度与并发安全
6. **语言概念**（concepts 01→30）：按 6 个阶段顺序过一遍——基础语法、goroutine、channel、并发模式、错误处理、反射；与 internals 的同名主题（如 mutex、defer、reflect）互为"用法 ↔ 原理"对照
7. **并发原理深水区**（concurrency 01→23）：内存模型 → sync 源码级复刻 → GMP 调度 → channel 运行时 → 取消传播 → 度量与收官框架；适合已做完 internals 07~19 与 concepts 并发篇后进阶，每个实验先跑 `go test -race` 再读 README
8. **网络编程**（net 01→21）：Socket 语义 → 自定义协议与 fuzz → HTTP 深水区 → TLS/PKI → gRPC/WebSocket，收官迷你 RPC；与 web 线互为"协议传输 ↔ 有状态服务"
9. **Web 服务实战**（web 01→20 ✅）：marklet 书签服务共同生长——骨架与 SQLite 数据层 → PostgreSQL/缓存 → 认证安全 → 可观测性 → 异步任务 → Docker 交付；每篇先 `go run .` 看完整时间线，再 `go test -race` 过验收

建议顺序：concepts 打底，internals 1~4 段与 patterns 穿插对照，最后 concurrency 深水区收束；每个实验都动手 `go run`，别只看图。

## images/ 补充图与质量门禁

internals/patterns/concepts 三线的 `images/` 目录保留了此前生成的架构/流程/时序图
（图源 JSON + 交互 HTML + 双主题 SVG，由工具从图源生成并通过质量门禁），以及数据类图
（基准柱状图、内存曲线，matplotlib 从 `-json` 快照实测绘制，数值全部来自真实程序输出）。
README 正文不再内嵌这些图；需要可视化时按上文方式本地打开，或运行实验目录的 `viz.py`
重新生成数据图。concurrency/net/web 三线无 images/——web 线的实测数据以文字与
代码内计数器形式入档在各篇 README。
