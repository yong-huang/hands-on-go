# 10 · sync.WaitGroup：一个 64 位 state 字 + 一个 sema 的等待艺术

> `main` 怎么精确等到一组 goroutine 全部收工？`sync.WaitGroup` 的答案是极简的：没有锁、没有队列，只有**一个 64 位原子 state 字**（高 32 位 counter、低 32 位 waiters）加**一个 4 字节 sema 信号量**。本实验用 `atomic.Uint64.Load` 直接读出每一步操作后的真实计数，看清 Add/Done/Wait 各自碰哪个半边、归零那一瞬如何唤醒所有等待者。

## 1. 为什么需要它

等一组并发任务收工，手写方案要么忙轮询浪费 CPU，要么 channel 计数容易漏。WaitGroup 把"计数 + 等待 + 唤醒"封装成三个动作：`Add` 报数、`Done` 销账、`Wait` 睡到清零。但它最容易用错——`Add` 与 `Wait` 并发、计数不清零、Done 多次——出问题都是"永久卡住"级别的。**理解 state 字的位布局，就理解了哪些用法安全、哪些是未定义**。

## 2. 总览：核心机制一图看懂

![WaitGroup 的 state 字位布局：高 32 位 counter 与低 32 位 waiters](images/state.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/10_waitgroup/images/state.html)
> （或本地打开 [`images/state.html`](images/state.html)）。

**心智模型一句话**：WaitGroup 把两个计数打包进一个 64 位字——Add/Done 只改高半边 counter，Wait 只改低半边 waiters，两边互不冲突，任何读都是一次原子 Load，锁都不需要；counter 归零那刻由 sema 逐个唤醒睡眠的等待者。

## 3. 快速开始

```bash
cd internals/10_waitgroup
go run main.go          # 文本演示：Add/Done 步进 → Wait 阻塞 → 时间线采样
go run main.go -json    # 输出 JSON 快照（steps 与完整 timeline 数组）
```

预期输出（节选）：

```text
New()        counter=0  waiters=0   (新建：两个计数都是 0)
Add(2)       counter=2  waiters=0   (要等 2 个任务)
Add(1)       counter=3  waiters=0   (再加 1 个，counter=3)
Done()       counter=2  waiters=0   (-1，还剩 2 个)
Add(2)       counter=4  waiters=0   (中途还能再涨，counter=4)
Done()×4     counter=0  waiters=0   (全部清完，counter=0)
任务进行中: counter=1 waiters=1     ← Wait 期间 waiters +1
Done() 后  : counter=0 waiters=0   ← 清零那刻 sema 唤醒
t=  3786µs  counter=3 waiters=2
t= 15070µs  counter=2 waiters=3   ← waiters 顶满不动，等归零
```

每一步"在看什么"都写在 `main.go` 的注释里；`-json` 快照给出与文本演示一致的机器可读数值。

## 4. 核心概念

### 4.1 内存布局：两个计数挤进一个字

`WaitGroup = { noCopy 0B, state atomic.Uint64 8B, sema uint32 4B }`。state 高 32 位是 counter（还有多少 Add 增量未 Done），低 32 位是 waiters（阻塞在 Wait 上的 goroutine 数）。解码就是标准库的读法：

```go
st := (*atomic.Uint64)(unsafe.Pointer(wg))
w := st.Load()
Counter = int32(w >> 32)    // 高 32 位
Waiters = int32(uint32(w))  // 低 32 位
```

实测步进（第 1 节）：New 0/0 → Add(2) 2/0 → Add(1) 3/0 → Done 2/0 → Add(2) 4/0（中途还能涨）→ Done×4 0/0。counter 全程住在高 32 位。

### 4.2 Wait：把等待者挂到低 32 位并睡在 sema 上

有人 `Wait()` 且 counter 未归零时，waiters 低 32 位 +1（实测 counter=1 waiters=1），然后这个 goroutine 通过 `runtime_Semacquire` 睡在 sema 上——不烧 CPU。

### 4.3 时间线：递减到 0 的唤醒瞬间

让 3 个 waiter 错峰（每 2ms 一个）涌入，主 goroutine 每 8ms 调一次 Done，每 500µs 采样：

![WaitGroup 时间线：Add(3)、三次 Done 与归零唤醒](images/timeline.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/internals/10_waitgroup/images/timeline.html)
> （或本地打开 [`images/timeline.html`](images/timeline.html)）。

怎么看：waiters 错峰爬到 3 后一直顶着不动（没到唤醒条件大家都睡着）；counter 阶梯 3→2→1→0；**counter=0 与 waiters=3 同框的那一瞬，semrelease 把 3 个睡眠者全部叫醒，waiters 瞬间归零**——"全部完成后一起放行"的底层来源。

### 4.4 归零即质变

唤醒条件是 counter **等于 0** 而非"小于等于 0"：从 1 到 0 的那次 Done 触发对每位等待者的 `runtime_Semrelease`。这也是为什么 Done 必须与 Add 严格配对——减成负数直接 panic。

## 5. 关键字段与坑清单

| 字段/操作 | 含义 | 为什么重要 |
|---|---|---|
| `state 高 32 位` | counter：未完成任务的 Add 增量 | Add/Done 的工作区，归零即放行 |
| `state 低 32 位` | waiters：阻塞在 Wait 上的人数 | 归零唤醒后立即回 0 |
| `sema uint32` | 睡眠/唤醒信号量 | 绝对计数在 state，入睡叫醒在 sema |
| `Add(n)` / `Done()` | counter ±n（Done 即 Add(-1)） | 只碰高半边，乱序安全 |
| `Wait()` | waiters+1 并睡到归零 | 只碰低半边，可跨 goroutine 调用 |

坑清单：

- **Add 与 Wait 并发是未定义**：官方语义要求 Add 的额度在 Wait 前确定；标准库对"Wait 之后再加"有 panic 检查
- **计数不清零 = 永久泄漏**：少一次 Done，所有 Wait 永远睡着；把 Done 写成 `defer wg.Done()` 是肌肉记忆级防御
- **counter 减成负数直接 panic**（negative WaitGroup counter）：Done 不能多
- **readWG 只是瞬时快照**：任何一次 Load 都是"某一瞬间"，跨操作的因果要靠实验的时间设计（错峰、定时 Done）保证
- **waiters 爬升太快拍不到**：并发涌入第 5µs 就满 3；给实验加 2ms 错峰才能拍到 0→1→2→3 阶梯

## 6. 文件结构

```text
internals/10_waitgroup/
├── README.md                        # 本文件：原理 + 运行指引 + 深入要点
├── main.go                          # 主教材：state 解码 + Add/Done/Wait 时间线
└── images/
    ├── state.architecture.json      # 图源：state 字位布局（Typed JSON IR）
    ├── state.html                   # 交互版：可缩放/高亮连线的自包含页面
    ├── state.svg                    # README 内嵌双主题矢量图
    ├── timeline.sequence.json       # 图源：Add/Done/Wait 时序（Typed JSON IR）
    ├── timeline.html                # 交互版
    └── timeline.svg                 # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：WaitGroup 内部是怎么实现的？有锁吗？**
A：没有锁。一个 64 位原子 state 字：高 32 位 counter、低 32 位 waiters，加一个 sema 信号量。Add/Done 只 CAS 高半边，Wait 只改低半边并睡在 sema 上，读状态一次原子 Load 即可。

**Q：为什么 Done 可以在任意 goroutine 里调用？**
A：Done 就是 `Add(-1)`，只对高 32 位做一次原子减，与谁执行无关；归零的唤醒由那次 Done 顺带触发（发现 waiters>0 就逐个 semrelease）。

**Q：counter 归零时发生了什么？**
A：从 1 到 0 是质变：runtime 检查 waiters>0，对每个睡眠者做 `runtime_Semrelease`，waiters 瞬间清零、所有 Wait 同时返回。实验采样恰好抓到 counter=0 与 waiters=3 同框的一瞬。

**Q：WaitGroup 用错有哪些典型死法？**
A：少 Done → 计数永不清零，Wait 永久阻塞（goroutine 泄漏）；多 Done → counter 为负直接 panic；Wait 之后还 Add → 未定义行为。额度应在 Wait 前用 Add 确认。

**Q：WaitGroup 和 channel 等待相比怎么选？**
A：只等完成、不要结果用 WaitGroup 最省（无锁无队列）；要传结果或做流式处理用 channel；需要超时/取消语义则选 context + channel。

## 8. 总结

WaitGroup 的全部语义都能从"一个字两个计数 + 一个信号量"推出：**Add/Done 只动高 32 位，Wait 只动低 32 位，归零那一瞬 sema 送走所有睡眠者**。三个 API 各管一边、互不加锁，这就是它又轻又容易用错的原因——边界全在调用方手里。

下一篇 [11_pool](../11_pool/) 依然围绕"复用"：`sync.Pool` 怎么按 P 分桶缓存临时对象，又怎么在两轮 GC 之间悄悄清空。
