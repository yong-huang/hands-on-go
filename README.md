# hands-on-go：用 50 个能真实跑通的实验，吃透 Go 底层与并发模式

不背八股，动手看真相。每个实验都是**一个可运行的 Go 程序 + 一份编号章节的 README + 可交互的架构图**：
clone 下来 → `go run main.go` → 对照 README 与图，亲眼验证每一个结论。所有实验只用标准库、
不依赖外部服务，每张图都由图源 JSON（Typed IR）生成并通过质量门禁，README 里的结论都有对应的验证命令。

## 两条学习线

| 目录 | 定位 | 内容 |
|---|---|---|
| [`internals/`](internals/) | **语言底层**（30 篇） | slice/map/channel 的内存布局、GMP 调度、分配器与 GC、pprof/trace 工具链 |
| [`interview/`](interview/) | **面试手写**（20 篇） | LRU/LFU、限流器、Worker Pool、分布式锁、迷你 Redis 等高频手写题的完整实现 |

每个实验目录内：

```text
NN_topic/
├── README.md          # 主教材：8 个编号章节（为什么 → 一图看懂 → 快速开始 → 核心概念 → 坑清单 → 文件结构 → 面试要点 → 总结）
├── main.go / xxx.go   # 演示程序：每一步"在看什么"都有注释，部分支持 -json 输出状态快照
├── viz.py             # （仅数据实验）从 -json 快照绘制实测数据图，输出到 images/
└── images/
    ├── xxx.<type>.json  # 图源：Typed JSON IR（架构/流程/时序/数据流/状态机）
    ├── xxx.html         # 交互版：自包含单文件，可缩放、高亮连线、切换明暗主题
    └── xxx.svg          # README 内嵌的双主题矢量图
```

> 🌐 交互图支持 **GitHub Pages 在线打开**（开启 Pages 后形如
> `https://<user>.github.io/<repo>/internals/01_slices/images/sharing.html`，各 README 内有直达链接），
> 也可直接本地双击对应 `images/*.html` 打开。

## 新增实验

想给系列加一篇？照 [PROJECT_TEMPLATE.md](PROJECT_TEMPLATE.md) 执行：取号规则、README 八节骨架、
Archify 图三件套管线、数据图约定与收尾自检清单都在里面。

## 环境要求

- Go ≥ 1.21（实验在 go1.26 实测通过）；纯标准库，无第三方依赖
- 可选：python3 + matplotlib（仅重新生成实测数据图时需要；仓库已含生成好的 PNG）
- 操作系统不限；涉及地址/内存布局的输出数值每次运行都会变，不变的是关系与结论

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

### interview · 面试手写（20 篇）

| # | 实验 | 一句话 |
|---|---|---|
| 01 | [lru_cache](interview/01_lru_cache/) | 哈希表 + 双向链表的 O(1) LRU |
| 02 | [lfu_cache](interview/02_lfu_cache/) | 频次桶 + 双向链表的 O(1) LFU |
| 03 | [read4k](interview/03_read4k/) | 用 4K 缓冲实现多次调用的 ReadN |
| 04 | [trie](interview/04_trie/) | 前缀树与自动补全 |
| 05 | [bloom_filter](interview/05_bloom_filter/) | k 个哈希 + 位数组的存在性判断 |
| 06 | [ring_buffer](interview/06_ring_buffer/) | 固定内存的环形队列与回绕 |
| 07 | [token_bucket](interview/07_token_bucket/) | 恒速补充、容量上限的令牌桶 |
| 08 | [worker_pool](interview/08_worker_pool/) | 固定 worker 的任务池与背压 |
| 09 | [producer_consumer](interview/09_producer_consumer/) | 缓冲 channel 上的生产者消费者 |
| 10 | [shard_map](interview/10_shard_map/) | 分片降低锁竞争的并发 Map |
| 11 | [pubsub](interview/11_pubsub/) | topic 扇出的发布订阅 Broker |
| 12 | [conn_pool](interview/12_conn_pool/) | 连接复用：空闲队列 + 信号量 + 健康检查 |
| 13 | [consistent_hash](interview/13_consistent_hash/) | 哈希环与虚拟节点 |
| 14 | [dist_lock](interview/14_dist_lock/) | Redis 锁：TTL 过期与误删防护 |
| 15 | [http_router](interview/15_http_router/) | 基于方法树参数匹配的迷你路由 |
| 16 | [graceful_shutdown](interview/16_graceful_shutdown/) | 信号驱动的优雅关闭时间线 |
| 17 | [mini_redis](interview/17_mini_redis/) | 手写 RESP 协议的 KV 服务器 |
| 18 | [rate_limiter](interview/18_rate_limiter/) | 滑动窗口限流中间件 |
| 19 | [task_scheduler](interview/19_task_scheduler/) | 优先队列定时任务调度器 |
| 20 | [concurrent_lru](interview/20_concurrent_lru/) | 分片并发的 LRU 缓存 |

## 建议学习路线

1. **数据结构底层**（internals 01→06）：slice/map/channel/string/interface 的内存真相，后续一切的地基
2. **并发原语**（internals 07→12，再 18→19）：goroutine 栈、锁/原子/WaitGroup/Pool/Once、GMP 与 syscall
3. **内存与运行时**（internals 13→17、20→21、30）：逃逸、分配器、GC、对齐、信号与 CGO，最后手写迷你运行时自检
4. **工程化与性能**（internals 22→29）：pprof/trace/inline/defer/reflect/泄漏检测，学会用工具说话
5. **面试手写**（interview 01→20）：每题限时手写，写完对照 README 的架构图复盘复杂度与并发安全

建议顺序：internals 1~4 段先行，interview 穿插练手；每个实验都动手 `go run`，别只看图。

## 图三件套与质量门禁

所有架构/流程/时序图由图源 JSON 生成（作者写语义，工具管布局）：showcase 质量门禁 9 项全过
（标签净空、无穿越、可读性等）→ 交互页四视口零溢出 → 导出双主题 SVG → 像素级居中校正。
数据类图（基准柱状图、内存曲线）保留 matplotlib 从 `-json` 快照实测绘制，图里的数值全部来自真实程序输出。
