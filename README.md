# hands-on-go：用 80 个能真实跑通的实验，吃透 Go 底层与并发模式

不背概念，动手看真相。每个实验都是**一个可运行的 Go 程序 + 一份编号章节的 README + 可交互的架构图**：
clone 下来 → 每个实验 `go run` 即跑 → 对照 README 与图，亲眼验证每一个结论。所有实验只用标准库、
不依赖外部服务，每张图都由图源 JSON（Typed IR）生成并通过质量门禁，README 里的结论都有对应的验证命令。

## 两条学习线

| 目录 | 定位 | 内容 |
|---|---|---|
| [`internals/`](internals/) | **语言底层**（30 篇） | slice/map/channel 的内存布局、GMP 调度、分配器与 GC、pprof/trace 工具链 |
| [`patterns/`](patterns/) | **手写组件与并发模式**（20 篇） | LRU/LFU、限流器、Worker Pool、分布式锁、迷你 Redis 等常用组件的完整实现 |
| [`concepts/`](concepts/) | **语言概念**（30 篇） | 变量与类型、流程控制、goroutine/channel/select、错误处理、context、反射——语言特性逐个击破 |

每个实验目录内：

```text
NN_topic/
├── README.md          # 主教材：8 个编号章节（为什么 → 一图看懂 → 快速开始 → 核心概念 → 坑清单 → 文件结构 → 深入要点 → 总结）
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

想给系列加一篇？在对应线（internals/patterns）末尾按 `NN_短名` 追加目录：
README 用与现有实验相同的 8 节编号结构，机制图在 `images/` 放图源 JSON + 交互 HTML + SVG 三件套，
实测数据图用 matplotlib 从 `-json` 快照绘制到 `images/`，最后在本文件实验表加一行。
配套的学习清单与规划见 [docs/](docs/)（go_internals / go_patterns）。

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

## 建议学习路线

1. **数据结构底层**（internals 01→06）：slice/map/channel/string/interface 的内存真相，后续一切的地基
2. **并发原语**（internals 07→12，再 18→19）：goroutine 栈、锁/原子/WaitGroup/Pool/Once、GMP 与 syscall
3. **内存与运行时**（internals 13→17、20→21、30）：逃逸、分配器、GC、对齐、信号与 CGO，最后手写迷你运行时自检
4. **工程化与性能**（internals 22→29）：pprof/trace/inline/defer/reflect/泄漏检测，学会用工具说话
5. **手写组件**（patterns 01→20）：每篇亲手实现，写完对照 README 的架构图复盘复杂度与并发安全
6. **语言概念**（concepts 01→30）：按 6 个阶段顺序过一遍——基础语法、goroutine、channel、并发模式、错误处理、反射；与 internals 的同名主题（如 mutex、defer、reflect）互为"用法 ↔ 原理"对照

建议顺序：concepts 打底，internals 1~4 段与 patterns 穿插对照；每个实验都动手 `go run`，别只看图。

## 图三件套与质量门禁

所有架构/流程/时序图由图源 JSON 生成（作者写语义，工具管布局）：showcase 质量门禁 9 项全过
（标签净空、无穿越、可读性等）→ 交互页四视口零溢出 → 导出双主题 SVG → 像素级居中校正。
数据类图（基准柱状图、内存曲线）保留 matplotlib 从 `-json` 快照实测绘制，图里的数值全部来自真实程序输出。
