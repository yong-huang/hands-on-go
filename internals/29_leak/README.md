# 29 · 内存泄漏检测：Go 没有"真泄漏"，只有"该回收却收不回"

> Go 有 GC，但照样有"泄漏"——准确说是**该被回收的对象被一个还活着的引用钉住**：一个永久阻塞的 goroutine、一块被 16 字节小切片拖住的 100MB 数组、5 个忘了 Close 的文件。读完本篇，你能亲手造出这三种泄漏，并用 `runtime.NumGoroutine`、`MemStats`、`runtime/pprof` 三件取证工具让它们现形。

## Background

GC（垃圾回收器）只回收**不可达**的对象——即从存活变量出发、顺着引用再也走不到的对象。这是自动内存管理的根基，也埋下了另一种隐患：只要引用链上有一个"永不满足的阻塞点"或一个被遗忘的引用，内存和 goroutine 就永远下不去。

更麻烦的是取证维度被拆开了：`heap` 指标、goroutine 数、fd（文件描述符，内核为每个打开文件维护的句柄）各自只暴露其中一种泄漏。单看一个指标会完全漏判——fd 泄漏时堆纹丝不动，goroutine 泄漏时堆也可能不变。

本实验的程序演示三件事：造出三种典型泄漏、逐阶段快照对比、写 profile 文本快照拿到"现场"。

## What

**定义**：Go 语境下的内存泄漏不是指针丢失（C 语言那种），而是**该回收的对象被一个还活着的引用钉住**——典型形态有三种：永久阻塞的 goroutine、小切片对大底层数组的背景引用、未关闭的系统资源。

**心智模型**：可以把 GC 想象成一位只收"没有任何线牵着"的废弃物的回收员——只要物件上还拴着一根线（引用链可达），它一律不收。但和人类回收员不同的是：GC 不会来问你"这根线还要不要"，线在就收走，剪线的责任全在你。

**机制一句话**：goroutine 泄漏 = 协程永久阻塞在无人接的 channel（goroutine 间传递数据的管道）上，阻塞点让"协程 + 栈 + 引用对象"整条引用链保持可达，GC 只能标记存活、无法回收。

## When to Use

### 典型场景

- 服务内存持续上涨不回落：先确认是"涨了不跌"（泄漏）还是正常波动，再分维度取证。
- goroutine 数只增不减：常见于超时控制缺失、channel 没人关闭——长驻服务里协程数是健康指标。
- 大对象处理后内存不降：检查是否传出去一个指向大底层数组的子切片。

### 何时不用

- 堆内存周期性波动但 GC 后能回落：这是正常行为，不是泄漏。
- 一次性短命程序：进程退出即全部释放，取证成本不划算。

### 同类方案对比

| 指标/工具 | 差异 | 什么时候选它 |
|---|---|---|
| `runtime.NumGoroutine()` | goroutine 维度计数 | 协程数只增不减的怀疑 |
| `MemStats.HeapAlloc`（GC 后读） | 瞬时堆字节数 | 确认"涨了不跌"的现象 |
| pprof heap/goroutine profile | 按分配点或栈归因 | 定位"谁分配的/卡在哪" |
| `lsof -p <pid>` | 内核侧 fd 计数 | fd 泄漏（堆和协程都看不见） |

## Quick Start

前置条件：无第三方依赖。运行：

```bash
cd internals/29_leak
go run main.go          # 文本演示：四个阶段快照 + profile 路径
go run main.go -json    # 输出 JSON 快照（phases/leakedChans/leakedFiles）
```

预期输出（节选；HeapAlloc 每次运行有零点几 MB 浮动，profile 路径每次不同）：

```text
基线                     goroutine=1   HeapAlloc=   0.3MB
20 个 goroutine 阻塞      goroutine=21  HeapAlloc=   0.3MB
保留 100MB 小切片引用        goroutine=21  HeapAlloc= 100.3MB
5 个未关闭文件               goroutine=21  HeapAlloc= 100.3MB

goroutine profile: /var/folders/.../leak_*.prof_goroutine.pprof
heap profile: /var/folders/.../leak_*.prof_heap.pprof
```

诚实预期：四个阶段对应三种泄漏——goroutine 数跳到 21 后不回落、堆跳到 100.3MB 后不回落；第三种（fd）在这两个指标里都看不见，它不在输出里，要靠 `lsof` 取证。

## How It Works

### 泄漏一：goroutine 卡在没人接的 channel

```go
ch := make(chan struct{})   // 无缓冲，也永远没人发送/关闭
go func() { <-ch }()        // main.go:65，永久阻塞
```

`<-ch` 在无缓冲 channel 上等待一个永不到来的发送方，goroutine 卡死在 `runtime.gopark`。20 个这样的协程把 `NumGoroutine` 从 1 顶到 21——就是你在输出里看到的第二个阶段——且每个协程的栈和它引用的对象全部随之钉死。

解除方式三选一：谁发送谁负责 `close(ch)`，接收方用 `v, ok := <-ch` 读到零值（类型的默认值）后退出；`make(chan T, N)` 带缓冲让发送不阻塞；`select { <-ctx.Done() }` 监听 context 的取消信号，保证可控退出。

### 泄漏二：小切片背景引用大数组

`keep := big[0:16]` 只留 16 字节的切片头（ptr/len/cap 三个字段），但它和 `big` 共享同一块 100MB 底层数组——`big` 变量没了，`keep` 还握着整块内存。切片头小，**它指向的东西大**。

输出第三阶段的 `HeapAlloc=100.3MB` 就是这块数组退不了堆：GC 前后都一样，因为引用链可达。

### 泄漏三：未关闭资源（fd）

5 个 `os.Open` 后故意不 `Close` 的文件不进堆、不进 goroutine 数——前两项指标全然不见，得靠 `lsof -p <pid>` 数文件描述符。这是第三种泄漏维度。

Go 侧唯一线索是 `os.File` 的 finalizer（对象被回收前运行的钩子）会在 GC 时尝试关闭，但这依赖 GC 时机，不可靠，必须显式 `Close`。

### 取证数据怎么读

goroutine 数在"20 个 goroutine 阻塞"阶段从 1 跳到 21 并不再回落；profile 文本快照（`debug=1`）里，这 20 个协程全部指向 `main.main.func1`（main.go:65 的 `<-ch`）——"多少协程卡在哪个栈"一目了然。

堆在"保留 100MB 小切片引用"阶段跳到 100.3MB 后不再下降，与输出一致。

### 关键工具与字段速查

| 工具/字段 | 用法 | 为什么重要 |
|---|---|---|
| `runtime.NumGoroutine()` | 各阶段快照对比 | goroutine 泄漏最直接的计数证据 |
| `runtime.ReadMemStats(&m)` → `m.HeapAlloc` | `runtime.GC()` 后再读 | 强制 GC 排除"还没来得及收"的干扰 |
| `pprof.Lookup("goroutine").WriteTo(f, 1)` | debug=1 是文本 | 直接给出"多少协程卡在哪个栈" |
| `runtime.KeepAlive(x)` | 放在所有快照**之后** | 只保活到调用点之前，位置错了测量全废 |

## Pitfalls & Q&A

**坑 1：编译器 DCE（死代码消除，删除不影响结果的代码）删掉"只写不读"的 100MB。** 现象：堆统计全是 0。原因：`big[i] = byte(i)` 后无人读，整个分配被优化消失。解法：`sink = big[7]` 真读一次，再用全局/引用钉住。

**坑 2：`KeepAlive(keep)` 放在快照之前。** 现象：末尾快照时 HeapAlloc 掉回 0.3MB。原因：保活只到调用点为止，之后引用已被判定死亡。解法：`KeepAlive` 必须放在最后一个快照之后。

**坑 3：把 `WriteTo(f, 0)` 的输出当文本读。** 现象：解析报 `UnicodeDecodeError`。原因：debug=0 是 gzip 压缩的 protobuf（二进制格式），不是文本。解法：读文本快照必须 `debug=1`。

**坑 4：直接渲染 profile 文本栈。** 现象：排版错乱、混入十六进制串。原因：文本栈含 tab 和 `0x…` 地址前缀。解法：`\s+` 归一为空格并剥掉地址前缀。

**坑 5：假设字典文件一定存在。** 现象：`/usr/share/dict/words` 不存在时 `os.Open` 直接失败，fd 泄漏根本不会发生。原因：泄漏依赖打开成功这个前提。解法：跑之前确认该文件存在（本机存在，实测 `leakedFiles=5`），或换自己创建的临时文件。

**Q：Go 有 GC，为什么还会内存泄漏？**
GC 只回收不可达对象。三种泄漏的共同点是"一个活着的引用"：阻塞的 goroutine 持有栈、小切片持有大底层数组、全局 map/缓存握着旧数据——引用链可达，GC 就永远不收。

机制已在 What 与 How It Works 展开，三种 channel 泄漏的解除方式也见"泄漏一"一节。

**Q：`m.HeapAlloc` 和 pprof heap profile 有什么区别？**
HeapAlloc 是瞬时堆字节数，适合前后对照看"涨了不跌"；heap profile 按分配点聚合，回答"谁分配的"。

定位泄漏先用前者确认现象、再用后者归因，goroutine 泄漏则直接看 goroutine profile。
