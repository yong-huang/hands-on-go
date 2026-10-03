# 🧵 Go 并发 23 小项目学习清单 · Todo List（原理深水区）

> 通过 23 个小项目（每项目 80~250 行代码）吃透 Go 并发的运行时原理：内存模型与 happens-before、sync 库源码级复刻、GMP 调度器深水区、channel 运行时、取消传播与并发度量
> 本机可跑、纯工具链免费；已完成的 internals/patterns 80 个实验视为前置，本清单只做深化与实证，不重复造已造过的轮子
> 知识体系型：阶段即能力等级，里程碑按阶段给能力命名；将来实现时按仓库惯例落 `concurrency/NN_topic/`（届时走 hands-on-series 流程）
> 预计周期：8~10 周（每天 1 小时）

## 🤖 AI 辅助提示词速查

| 场景 | 提示词 |
|:---|:---|
| **开始一个新项目** | `我要开始 Go 并发项目「[名称]」，目标是 [目标]。请给我完整 Go 代码约 [行数] 行（go1.26，纯标准库/x/sync），含 -race 可过的并发测试与验收命令。只输出代码。` |
| **排障** | `我的并发程序出现 [现象]，-race 输出/trace 截图：[粘贴]。请分析根因并给修复。` |
| **基准解读** | `这是我的 benchmark 输出：[粘贴]。请解释 ns/op 与 allocs/op 差异来源，并指出基准写法的缺陷（时钟/伪共享/编译器优化）。` |

## 📊 总进度

进度：███████████████████████ 23/23 (100%)

| 阶段 | 项目数 | 已完成 |
|:---|:---:|:---:|
| 第一阶段：内存模型基石 | 4 | 4 |
| 第二阶段：sync 库源码级 | 5 | 5 |
| 第三阶段：调度器 GMP 深水区 | 5 | 5 |
| 第四阶段：channel 运行时 | 3 | 3 |
| 第五阶段：取消与生命周期 | 3 | 3 |
| 第六阶段：度量与综合 | 3 | 3 |
| **合计** | **23** | **23** |

---

## 🗂️ 第一阶段：内存模型基石（项目 1-4）

> **目标**：能用 happens-before 推理任何一段并发代码的对错，而不是靠"加了锁应该没事"

### [x] 项目 1：happens-before 探针——用 litmus 测试实证重排

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | Go 内存模型、happens-before、store buffer 重排、同步原语的可见性承诺 |
| **验收标准** | ① 无同步的 litmus（两线程互写 flag/data）在 `go run -race` 下报 DATA RACE；② 加上 channel 或 atomic 同步后 `go test -race -count=100` 零报告；③ 报告中逐条列出 Go 内存模型承诺的 5 类 happens-before 边并标注哪条救了哪个 litmus |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「happens-before 探针」。请给我完整 Go 代码约 120 行：实现 store buffer、load buffer、消息传递三个 litmus 测试（每个测试双 goroutine 百万次迭代），无同步版本、channel 修复版、atomic 修复版三套，附 go test -race 验收命令与预期输出。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 2：atomic 语义层级——SeqCst vs Acquire/Release

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | memory order、SeqCst、Acquire/Release 配对、atomic.Int32/Pointer（Go 1.19+） |
| **前置** | 项目 1 |
| **验收标准** | ① 消息传递 litmus 用 Release 写 + Acquire 读跑 `go test -race -count=100` 零违例；② 报告用一句话说清"为什么 Release/Acquire 足够建锁而 Store/Load 默认语义下也要能过"；③ `go doc sync/atomic` 列出的原子类型清单贴进项目 README |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「atomic 语义层级」。请给我完整 Go 代码约 150 行：用 go1.26 的 atomic.Int32/atomic.Pointer[ T ] 实现 Release/Acquire 消息传递 litmus 与一个自旋锁（CAS + Acquire/Release），对比 SeqCst 写法，附 -race 测试与两个版本的基准对比。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 3：false sharing 与缓存行填充

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | cache line、伪共享、字段对齐填充、多核计数扩展性 |
| **前置** | 项目 2 |
| **验收标准** | ① 8 goroutine 各自 hot 计数：无填充 vs 64 字节填充两版基准，填充版吞吐 ≥ 2 倍（写入 `bench.txt`，附 `go test -bench` 命令）；② 报告说明 Apple Silicon 上 cache line 为何仍按 64 填充以及"不足 2 倍时如何排查（GOMAXPROCS/频率漂移）" |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「false sharing 实测」。请给我完整 Go 代码约 120 行：无填充计数器数组与 [64]byte 填充版两套实现，go test 基准（b.RunParallel 8 并发），附防止编译器优化掉的 sink 写法与验收命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 4：data race 检测器——原理、边界与开销

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | ThreadSanitizer、vector clock、shadow memory、漏报场景、-race 开销 |
| **前置** | 项目 1 |
| **验收标准** | ① 构造一个 `-race` 检不出但确实是竞态的最小案例（如跨 goroutine 的无同步先后依赖），并在 README 解释基于 vector clock 的检测为何漏报；② 同一服务 -race 开/关两组基准，开销记录进 README（内存与 ns/op 都要）；③ `go test -race` 报告样例逐行注解 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「data race 检测器边界」。请给我完整 Go 代码约 150 行：一个 -race 能报的标准竞态、一个 -race 漏报的弱一致案例、一组开/关 -race 的基准，附逐行注释解释 ThreadSanitizer 为什么这样判。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 🗂️ 第二阶段：sync 库源码级（项目 5-9）

> **目标**：徒手复刻 sync 核心原语，通过 -race 高压测试——读过不如写过

### [x] 项目 5：mini Mutex——双模式简化复刻

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | 自旋窗口、饥饿/正常模式切换、sema 信号量、CAS 状态字 |
| **前置** | 项目 2 |
| **风险** | internals/08 已拆解过 state 位布局，本项目要求"不看源码先写、写完再对照 `src/sync/mutex.go` 差异表" |
| **验收标准** | ① 自写 Mutex 过 `go test -race -count=200` 的 100 goroutine 临界区互斥测试；② 与标准库 Lock/Unlock 基准对比入档；③ 差异表：列出与标准库至少 3 处简化及各自后果 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「mini Mutex」。请给我完整 Go 代码约 150 行：用 CAS 状态字 + semaphore 实现含饥饿模式切换的互斥锁（先不看标准库源码），附 100 goroutine 互斥测试、-race 压测命令和与 sync.Mutex 的基准对比。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 6：mini RWMutex——readerCount 协调

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~130 行 |
| **核心知识点** | 读写锁、readerCount 负数协议、写优先与写阻塞读、递归 RLock 死锁 |
| **前置** | 项目 5 |
| **验收标准** | ① 并发读 + 独占写过 `go test -race -count=200`；② 写者到达后新读阻塞的公平性测试断言通过；③ 复现"读锁内再取读锁+写者在等"死锁案例并解释 readerCount 协议为何如此 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「mini RWMutex」。请给我完整 Go 代码约 130 行：readerCount 负数协议的读写锁，含写者优先、写阻塞读、递归 RLock 死锁演示三个测试，附 -race 命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 7：mini Cond——通知列表

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | 条件变量、NotifyList、Wait 原子放锁、Broadcast vs Signal 丢失唤醒 |
| **前置** | 项目 5 |
| **验收标准** | ① 用 mini Cond 实现有界队列阻塞版，`go test -race -count=100` 断言队列清空且无 goroutine 残留；② 演示"先改条件后 Wait 不加锁导致的丢失唤醒"并给出正确顺序；③ 对比 channel 版队列的一句话说清何时选 Cond |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「mini Cond」。请给我完整 Go 代码约 120 行：基于 sync.Mutex + 通知列表的条件变量，实现有界队列生产者消费者，含丢失唤醒反例测试，附 -race 验收命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 8：sync.Map 源码级复盘——read/dirty/misses

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | read/dirty 双 map、amended 标记、misses 提升、entry 只读加速 |
| **前置** | 项目 6 |
| **风险** | internals/02 与 patterns/10 已覆盖 map 底层与分片方案；本项目聚焦 sync.Map 自身协议，画 read→dirty→promote 状态流转 |
| **验收标准** | ① 用文字+ASCII 画出 misses 达阈值时 promote 的完整流转并标注锁点；② 读多写少场景 `go test -bench` 对比 sync.Map vs RWMutex+map vs 分片（数字入档）；③ 构造一个 dirty 反复重建的性能悬崖用例并解释 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「sync.Map 源码级复盘」。请给我完整 Go 代码约 150 行：read/dirty/misses 协议的三场景基准（读多写少/写多/高频增删 key）、promote 流转测试（用内部计数断言提升发生）、dirty 重建悬崖用例，附 bench 命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 9：singleflight 手写——请求合并防击穿

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | call 去重、WaitGroup 共享结果、panic 传播、热点 key 缓存击穿 |
| **前置** | 项目 5 |
| **技术栈** | golang.org/x/sync/singleflight 对照（⚠️ 首次 go get 走 goproxy.cn，开工探活） |
| **验收标准** | ① 100 goroutine 同 key 并发请求，后端计数器断言只落 1 次；② 不同 key 互不阻塞断言；③ 与 x/sync/singleflight 行为 diff 表（至少含 panic 传播、结果共享两行） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「singleflight 手写」。请给我完整 Go 代码约 120 行：call 去重 + WaitGroup 共享结果的请求合并器，含同 key 合并断言、不同 key 并行断言、panic 不吞测试，附与 x/sync/singleflight 的对照说明。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 🗂️ 第三阶段：调度器 GMP 深水区（项目 10-14）

> **目标**：从 trace 与实测数据解读调度器的每个行为，而不是背 GMP 名词

### [x] 项目 10：G 状态机追踪——go tool trace 解读

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | _Grunnable/_Grunning/_Gwaiting/_Gsyscall、runtime/trace、trace viewer |
| **前置** | internals/18、28（已做） |
| **验收标准** | ① 生成 trace 后 `go tool trace` 截取一个 G 的完整状态流转并按时间标注四次状态变化；② 报告列出 trace 里看到的 5 类事件（goroutine/网络/系统调用/ GC/用户任务）各举一例；③ 重复 3 次 trace，G 状态时序差异入档 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「G 状态机追踪」。请给我完整 Go 代码约 150 行：用 runtime/trace 采集混合负载（CPU 自旋 + channel 等待 + HTTP 模拟阻塞），附 trace 解读步骤与要截取的关键视图说明。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 11：work-stealing 实证——runnext/本地队列/全局队列

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | 本地队列 256 上限、runnext 插队、全局队列 1/61 概率、steal 半数 |
| **前置** | 项目 10 |
| **验收标准** | ① GOMAXPROCS=1/2/8 三档下任务完成顺序实验表（谁先谁后与预测对照）；② 用 `GODEBUG=schedtrace=100` 输出佐证本地队列与全局队列的分工；③ 报告解释 runnext 插队现象及一次实测证据 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「work-stealing 实证」。请给我完整 Go 代码约 150 行：设计能区分本地队列/runnext/全局队列行为的实验（大量短任务 + 精心安排的 go 语句顺序），GOMAXPROCS 三档对比，附 GODEBUG=schedtrace 采集步骤与解读模板。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 12：抢占演进——协作式到信号抢占

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | 协作式抢占点、stack guard、sysmon 10ms、signal 抢占（Go 1.14+） |
| **前置** | 项目 10 |
| **⚠️ 风险** | 严格版需 go1.13 旧工具链（golang.org/dl 下载，需可达 go.dev，开工探活）；fallback：用 GODEBUG=schedtrace + tight loop 在 go1.26 上观察 10ms 级让出，1.13 前行为以官方 release notes 引证 |
| **验收标准** | ① tight loop + GOMAXPROCS=1 下后台 goroutine 首次执行延迟实测 < 20ms 并记录；② fallback 路径也要完成：schedtrace 输出解读入档；③ 报告说清"异步抢占信号是什么、由谁发、打断在何处" |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「抢占演进」。请给我完整 Go 代码约 120 行：GOMAXPROCS=1 下 tight loop 阻塞后台 goroutine 的实验，测量后台任务首跑延迟（go1.26 信号抢占版），附 GODEBUG=schedtrace 采集与解读、go1.13 对照方案说明。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 13：netpoller——阻塞 socket 的 park/unpark 与 C10K

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **核心知识点** | netpoller、gopark/goready、epoll/kqueue 封装、goroutine-per-connection 内存账 |
| **前置** | internals/19（已做） |
| **验收标准** | ① echo server 维持 10000 个空闲连接，RSS < 100MB 且 goroutine 数 = 连接数 + 固定值（命令入档）；② 消息到达→echo 回包的 p99 延迟测量脚本与结果；③ 报告画出一个连接从 accept 到 data-ready 的 park/unpark 时序 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「netpoller 与 C10K」。请给我完整 Go 代码约 200 行：goroutine-per-connection echo server + 10k 连接压测客户端（建立空闲连接后分批发消息测延迟），附 RSS/goroutine 数采集命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 14：调度延迟测量——p99 分布

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | 调度延迟、trace 事件提取、尾延迟统计 |
| **前置** | 项目 10 |
| **验收标准** | ① 从 trace 提取 1000 次 goroutine 的 Runnable→Running 延迟，输出 p50/p95/p99 报表；② 空闲 vs 满载（GOMAXPROCS 个 CPU 密集 goroutine）两组分布对比；③ 报告解释 p99 差异的三个来源（队列排队/GC/抢占） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「调度延迟测量」。请给我完整 Go 代码约 150 行：用 runtime/trace + trace 解析（go tool trace 原始事件或 trace 包）提取 goroutine 调度延迟并输出分位数报表，空闲/满载双场景，附提取脚本。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 🗂️ 第四阶段：channel 运行时（项目 15-17）

> **目标**：亲手实现 channel 与 select 的核心语义，读运行时源码如读自己的代码

### [x] 项目 15：mini channel——锁 + 环形缓冲 + 等待队列

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **核心知识点** | hchan 结构、sendx/recvx 回绕、sudog 等待队列、close 广播语义 |
| **前置** | 项目 6、7 |
| **风险** | concepts/12~16 已覆盖 channel 语法语义；本项目是运行时复刻，先写再对照 `src/runtime/chan.go` |
| **验收标准** | ① 收发/缓冲满阻塞/关闭三语义过 `go test -race -count=200`；② 关闭后接收立即返回零值断言、向已关闭发送 panic 断言；③ 差异表：与 runtime hchan 至少 3 处简化（如无 select 支持）及后果 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「mini channel」。请给我完整 Go 代码约 200 行：泛型 Chan[T]，锁 + 环形缓冲 + 发送/接收等待队列，实现阻塞收发、缓冲、close 广播，附三语义 -race 测试与 runtime/chan.go 差异对照说明。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 16：select 均匀性与 selectgo 加锁顺序

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | selectgo、两阶段加锁与锁排序防死锁、随机轮询起点、default 非阻塞 |
| **前置** | 项目 15 |
| **验收标准** | ① 双就绪通道 10^6 次 select，两路占比偏差 < 5%（统计脚本入档）；② 构造两 channel 交叉收发的潜在锁序死锁场景，解释 selectgo 排序加锁如何避免；③ select + default 轮询忙等的 CPU 占用实测 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「select 均匀性实证」。请给我完整 Go 代码约 150 行：双就绪通道 select 均匀性统计（10^6 次）、default 忙等 CPU 占用测量、锁序死锁场景讲解案例，附统计脚本与验收命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 17：泛型并发原语库——Chan[T]/Pipe[T]/Merge[T]

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **核心知识点** | 泛型约束、pipeline 组合、fan-in/fan-out 封装、组件库 API 设计 |
| **前置** | 项目 15、concepts/19~20（已做基础版） |
| **验收标准** | ① 组件库 `go test -race -count=100` 全绿；② demo 用 Pipe[T]+Merge[T] 组装三级流水线跑通并打印各阶段吞吐；③ README 给 API 文档与"何时不用"（数据量小直接手写 channel 更清晰） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「泛型并发原语库」。请给我完整 Go 代码约 200 行：Chan[T]/Pipe[T]/Merge[T]/Split[T] 四个泛型组件，含 -race 单测与三级流水线 demo，附 API 文档与误用边界说明。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 🗂️ 第五阶段：取消与生命周期（项目 18-20）

> **目标**：把 goroutine 的生杀大权管起来——取消传播、失败联动、泄漏防护成体系

### [x] 项目 18：mini context——cancel 树传播

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **核心知识点** | cancelCtx 树、propagateCancel、WithTimeout 计时器、Value 链查找 |
| **前置** | 项目 15 |
| **风险** | concepts/22 已覆盖 context 用法；本项目聚焦树结构与传播协议复刻 |
| **验收标准** | ① 5 层 cancel 树取消根节点，全部叶子 < 10ms 内 Done（测试断言）；② 父超时自动取消子断言；③ 子先取消不扩散到父的隔离断言；④ 与标准库行为 diff 表 ≥ 2 行 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「mini context」。请给我完整 Go 代码约 180 行：cancelCtx/timeoutCtx/ValueCtx 三件套复刻（children 集合 + propagateCancel），附 5 层树传播、超时级联、父子隔离三个 -race 测试。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 19：errgroup 手写与结构化并发

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | WaitGroup+once 组合、首错取消、SetLimit、结构化并发原则 |
| **前置** | 项目 18 |
| **技术栈** | golang.org/x/sync/errgroup 对照（⚠️ 同项目 9，首次拉取探活） |
| **验收标准** | ① N 任务一失败全组取消断言（其余任务收到 ctx 取消 < 10ms）；② SetLimit 并发上限断言（峰值并发 = limit）；③ 对照裸 goroutine 的"失败无人管、泄漏无人收"失控案例入档 |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「errgroup 手写」。请给我完整 Go 代码约 150 行：首错取消 + SetLimit 的 Group 实现，含失败联动、并发上限、panic 转错误三个 -race 测试，附裸 goroutine 失控对照案例。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 20：goleak 泄漏防护测试基建

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 |
| **核心知识点** | goleak.VerifyTestMain、goroutine 快照 diff、测试即泄漏门禁 |
| **前置** | 项目 19 |
| **技术栈** | go.uber.org/goleak（⚠️ 首次拉取探活，走 goproxy.cn） |
| **验收标准** | ① 给项目 15~19 的库统一接入 VerifyTestMain；② 故意写一个泄漏测试用例确认 goleak 能抓红，修复后转绿（前后输出入档）；③ README 记录接入模板与"哪些情况 goleak 会误报（后台 timer/GC goroutine）" |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「goleak 泄漏门禁」。请给我完整 Go 代码约 120 行：goleak.VerifyTestMain 接入模板、一个可被 goleak 抓红的泄漏用例及其修复版、误报场景说明，附验收命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 🗂️ 第六阶段：度量与综合（项目 21-23）

> **目标**：并发正确性之外拿到性能证据，并用全部所学交付一个综合框架

### [x] 项目 21：计数器三路基准——mutex/atomic/分片填充

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **核心知识点** | 争用扩展性、分片计数、缓存行填充（项目 3 复用）、基准方法论 |
| **前置** | 项目 2、3 |
| **验收标准** | ① 1/2/4/8/16 并发五档基准，三实现 × 五档矩阵表入档；② 结论句回答"多少并发以下 mutex 反而最快"；③ 基准附 b.ReportAllocs 且跑 `go test -bench -count=5` 取中位数（脚本入档） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「计数器三路基准」。请给我完整 Go 代码约 150 行：Mutex/atomic.AddInt64/分片填充计数三实现，1~16 并发五档基准矩阵，附 -count=5 中位数统计脚本与结论模板。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 22：并发压测方法论——-race 与负载形状

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **核心知识点** | 均匀/突发/阶跃负载、-race 全量回归、压测指标定义、伪共享排查 |
| **前置** | 项目 4、17 |
| **验收标准** | ① 对项目 17 的组件库跑三种负载形状（恒定/突发 10x/阶跃爬坡），p99 对比表入档；② `go test -race -count=100` 全绿记录；③ README 沉淀一份"并发组件验收清单"（正确性/竞争/泄漏/尾延迟四项各一条命令） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「并发压测方法论」。请给我完整 Go 代码约 180 行：三种负载形状发生器 + 对项目 17 组件库的压测 harness，输出 p50/p99 表格，附 -race 全量回归命令与验收清单模板。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

### [x] 项目 23：🏁 收官——有界并发任务框架

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~250 行 |
| **核心知识点** | 任务提交/取消/超时、panic 隔离、背压策略、指标暴露、全项目综合 |
| **前置** | 项目 5~22 全部 |
| **验收标准** | ① 提交 10^4 任务、随机 10% panic：完成数 = 成功数 + 失败数严格守恒断言，无 goroutine 泄漏（goleak 绿）；② 全流程 `go test -race -count=50` 零报告；③ README 汇总本清单 23 项目的复用关系图（哪个组件来自哪个项目） |

**🤖 开始提示词**：
> `我要开始 Go 并发项目「有界并发任务框架」（收官综合）。请给我完整 Go 代码约 250 行：提交/取消/超时/panic 隔离/背压（阻塞或拒绝可配）/计数指标的任务框架，复用我写过的 mini Mutex、errgroup、goleak 思路，附守恒断言与 -race 验收命令。只输出代码。`

**完成日期**：2026-10-03
**踩坑记录**：________

---

## 📅 周计划

| 周次 | 内容 | 项目数 |
|:---|:---|:---:|
| **第 1 周** | 项目 1-3（内存模型入门：litmus/atomic/伪共享） | 3 |
| **第 2 周** | 项目 4-6（race 检测器 + mini Mutex/RWMutex） | 3 |
| **第 3 周** | 项目 7-9（mini Cond + sync.Map + singleflight） | 3 |
| **第 4 周** | 项目 10-12（G 状态机/work-stealing/抢占） | 3 |
| **第 5 周** | 项目 13-15（netpoller/调度延迟/mini channel） | 3 |
| **第 6 周** | 项目 16-18（select/泛型原语/mini context） | 3 |
| **第 7 周** | 项目 19-21（errgroup/goleak/计数器基准） | 3 |
| **第 8~10 周** | 项目 22-23 + 全清单复盘（压测方法论 + 收官框架） | 2 |

## 🏆 里程碑

- [ ] **完成项目 1-4** → 内存模型推理者：能用 happens-before 判定任意并发代码对错
- [ ] **完成项目 5-9** → 原语造轮者：徒手复刻 sync 核心五件套并通过 -race 高压
- [ ] **完成项目 10-14** → 调度器译码员：能从 trace 读出 G 的一生并解释每个调度行为
- [ ] **完成项目 15-17** → channel 造轮者：能实现 channel/select 并指出运行时对应路径
- [ ] **完成项目 18-20** → 生命周期管理者：取消传播、失败联动、泄漏门禁成体系
- [ ] **完成项目 21-23** → 并发度量工程师：能测能压，交付综合任务框架
- [ ] **全部完成** → Go 并发原理深水区毕业：从内存模型到调度器到工程框架全链路贯通

## 📝 每日日志

| 日期 | 项目 | 耗时 | 收获 | 踩坑 |
|:---|:---|:---:|:---|:---|
| | | | | |

## 🔧 环境配置

```bash
# 0. 已就绪（实测）：
#    - go1.26.5 darwin/arm64（go version 验证过）
#    - GOPROXY=https://goproxy.cn,direct（国内可达）
#    - go tool trace / pprof / -race（工具链内置，internals 系列已反复使用）
#    - dlv、gopls（~/go/bin 已装）
# 1. 按需安装：
#    - golang.org/x/sync v0.23.0（✅ 已拉取：项目 9/19 对照用）
#    - go.uber.org/goleak v1.3.0（✅ 已拉取：项目 20 泄漏门禁）
#    - go1.13 旧工具链：❌ darwin/arm64 无官方二进制（go1.16 起才支持），项目 12 已按 fallback 路线完成
#      （GODEBUG=asyncpreemptoff=1 对照 + release notes 引证，见 concurrency/12_preemption/README.md）
# 2. 每项目开工前统一检查：
go version && go vet ./... && go test -race ./...
```

## ⚠️ 与已有清单的关系

| 已有清单 | 关系 |
|:---|:---|
| docs/go_internals.md（30/30 已完成） | **前置已掌握**：07 goroutine 栈、08 mutex 状态机、09 atomic CAS、10 waitgroup、11 pool、12 once、18 GMP 基础、19 syscall、20 signal、29 leak、30 runtime 均已实验过。本清单不重复这些主题，全部走"源码级复刻 + 实证测量"的深化路线（如 mutex：internals 拆过 state 位，项目 5 要求盲写复刻） |
| docs/go_patterns.md（20/20 已完成） | **应用层已掌握**：worker_pool、producer_consumer、shard_map、conn_pool 等组件已手写过。本清单组件只补未覆盖的 singleflight（9）、errgroup（19）、泛型原语库（17） |
| docs/go_concepts.md（0/30，刚建） | **语法层并行线**：其 06~22 为并发语法用法。本清单默认读者已会用 channel/context/WaitGroup，语法不熟时先做该清单对应篇，或两线并行（概念篇当天做，本清单对应深化项目隔天做） |

## 📚 来源

- [Go Memory Model（官方）](https://go.dev/ref/mem)
- [runtime 源码：chan.go / proc.go / select.go / runtime2.go](https://github.com/golang/go/tree/master/src/runtime)
- [sync 源码：mutex.go / rwmutex.go / cond.go / map.go](https://github.com/golang/go/tree/master/src/sync)
- [Scheduling in Go（Ardan Labs 系列参考）](https://www.ardanlabs.com/blog/2018/08/scheduling-in-go-part1.html)

## ⚠️ 工具可用性速查表

| 能力 | 用于项目 | 实测结论 |
|:---|:---|:---|
| go tool trace / pprof | 10、13、14 | ✅ 工具链内置，internals/22、28 已反复验证 |
| -race（TSan） | 1、4、5~23 | ✅ 已验证（darwin/arm64），本仓库多实验使用过 |
| GODEBUG=schedtrace / gctrace | 11、12 | ✅ 内置能力 |
| golang.org/x/sync（singleflight/errgroup） | 9、19 | ✅ 已拉取 v0.23.0，实测对照通过 |
| go.uber.org/goleak | 20 | ✅ 已拉取 v1.3.0，门禁实测通过 |
| go1.13 旧工具链下载 | 12 | ❌ darwin/arm64 无官方二进制；项目 12 已走 fallback 完成验收 |
