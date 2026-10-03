# 23 · 有界并发任务框架：提交、取消、panic 隔离与一条守恒律

> 这是并发系列的收官项目：把前 22 篇练出的原语组装成一个有界并发任务框架，并用一条守恒律验收它——**Submitted == Succeeded + Failed**，任何提交进来的任务都必须有一个结局计数，一个都不能丢。本篇跑通 10^4 任务、约 10% panic 的混压场景，全程 `-race` 无报告。

## Background

生产服务里"并发执行用户提交的任务"是最常见的骨架：批处理作业、消息消费、爬虫抓取都是它。最朴素的写法是每来一个任务就 `go func()`——不设上限。

无上限的 `go` 语句在高负载下撞墙：任务到达速率超过处理速率时，goroutine 无限增长，内存被栈和队列吃光，GC 压力拖垮整个进程，最终 OOM。前文实验里的结论在这里集体生效——goroutine 初始栈 2KB 看着小，乘以百万级就是 GB。

解法是给并发加边界：固定 worker 池 + 有界队列，队列满时让提交方感知压力（阻塞或拒绝）。这个"组件"还需补齐三件工程必答题：任务 panic 不能带崩 worker（panic 隔离）；停机时不能丢任务也不能吊死（取消与排空）；以及一条可测试的正确性标准——守恒律。

## What

有界并发任务框架是一种任务执行组件：固定数量 worker 从有界队列取任务执行，提交方可选阻塞或被拒，每个任务有确定的结局计数。

一句话心智模型：**框架是一台"任务过磅机"——任务从 Submitted 闸口进，从 Succeeded 或 Failed 两个出口出，闸口计数恒等于两出口计数之和**。

可以把框架想象成一条传送带（队列）配固定数量的工人（worker）：传送带只有 64 个槽位，满了搬运工就在入口站着等（阻塞）或把货退回（拒绝）。但和工厂不同的是，这里的"货"（任务）可能自己炸掉——panic 隔离就是把爆炸限制在单件货的包装里，工人照常上班。

核心机制三件套，每个都来自系列前文：

- worker 池 + 环形队列：流水线骨架，见 [17_generics_prims](../17_generics_prims/)
- 取消与超时传播：cancel 树 + WithTimeout，见 [18_mini_context](../18_mini_context/)
- 泄漏门禁：goleak 挂在测试出口，见 [20_goleak_gate](../20_goleak_gate/)

## When to Use

适合在"做什么事"的时候引入这个组件，而不是"它有什么功能"。

- **批量作业需要并发上限时**：抓取一万个 URL、给一批用户发通知，用固定 worker + 队列把并发度钉在资源可承受的范围
- **任务来源不可控时**：上游突发流量（压测里的突发负载形状，见 [22_load_shapes](../22_load_shapes/)），需要阻塞背压或拒绝降级来保护自身
- **任务可能失败甚至 panic 时**：第三方解析、动态插件逻辑，要求单任务故障不影响整体吞吐

何时不用：任务量小且形态稳定时，一个 errgroup（见 [19_errgroup](../19_errgroup/)）几行就够；需要结果按序汇聚时，pipeline（见 [17_generics_prims](../17_generics_prims/)）更直接；只有单一异步操作要取消时，context 就完成了。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 裸 goroutine | 无界，无背压 | 任务量确定且极小 |
| errgroup | 一次性任务组，无队列无指标 | 一批任务等全部完成 |
| 本框架 | 有界、背压可配、守恒计数 | 常驻服务承接持续任务流 |
| 消息队列中间件 | 跨进程持久化 | 需要崩溃恢复与多机分发 |

## Quick Start

前置条件：Go 1.26 及以上，模块依赖已就绪（goleak 仅测试用）。运行：

```bash
cd concurrency/23_task_framework
go run main.go
```

预期输出（失败任务的 id 与各项计数每次运行不同；总量与守恒关系固定）：

```text
== 第 1 步：启动框架（8 worker，队列 64，阻塞背压，单任务超时 200ms）==

== 第 2 步：提交 10000 个任务 ==
约 10% 的任务会 panic（rand 决定，id 每次不同）；少量任务睡 400ms 触发超时；
其余任务做 1ms 的伪工作。提交方与 worker 并发跑，总量固定。

== 第 3 步：Cancel + Stop（快速停机）==

== 第 4 步：指标与守恒律 ==
Submitted=10000 Succeeded=8903 Failed=1097 (Panicked=1017) Rejected=0
守恒律：Submitted(10000) == Succeeded(8903) + Failed(1097) → true
panic 占失败比：92.7%（其余为超时/取消）
```

诚实预期：每次运行 Panicked 在 1000 上下浮动（二项分布，10000×10% 的标准差约 30）；约每 500 个 id 一个的 400ms 慢任务贡献十几个超时失败；`-race` 下整体耗时明显变长属正常。测试验收（守恒 + 无泄漏）：

```bash
go test -race -count=5 -short .   # 快速回归（守恒测试缩为 1000 任务）
go test -race -count=50 -short .  # 清单验收口径
```

## How It Works

### 守恒律为什么恒成立

计数的时机是关键：任务**成功进入队列**才计 Submitted，被拒的只记 Rejected；worker 处理结束时任务必然落在 Succeeded 或 Failed 之一。

中间没有任何"既不算成功也不算失败"的出口——panic 被 recover 转成 error 计入 Failed（Panicked 是它的子集计数），超时与取消同样计入 Failed。

你在输出里看到的 `→ true`，就是 `go test` 里那条 `s != ok+fail` 断言的运行时版本。

### 停机顺序：Stop 的三步不能乱

```go
func (f *Framework) Stop() {
    f.stopping.Store(true) // 1. 关闸：新 Submit 一律拒绝
    f.pending.Wait()       // 2. 等已受理任务全部有结局（含排队中的）
    close(f.queue)         // 3. 此刻不可能再有发送方，close 才安全
    f.workers.Wait()
}
```

第 3 步是并发编程里最经典的 close 竞争点：向已关闭通道发送会 panic，所以 close 的前提是"确认没有发送方存在"。

pending.Wait 保证了这一点——每个提交方在入队前就 `pending.Add(1)`，无论它最终排队、被拒还是阻塞中取消，都会配对 Done。

快速停机先 `Cancel()` 再 `Stop()`：运行中的协作式任务看到 ctx.Done 立即以失败结局退出，排空随之加快，守恒律不受影响。

### panic 隔离与超时的协作式边界

```go
done := make(chan error, 1) // 带缓冲：放弃等待后 wrapper 也能安全收尾
go func() {
    defer func() {
        if r := recover(); r != nil {
            f.metrics.Panicked.Add(1)
            done <- fmt.Errorf("task %d panic: %v", t.id, r)
        }
    }()
    done <- t.fn(taskCtx)
}()
select {
case err = <-done:
case <-taskCtx.Done():
    err = fmt.Errorf("task %d 未在期限内完成: %w", t.id, taskCtx.Err())
}
```

panic 在 wrapper goroutine 里 recover，worker 本体毫发无损。

超时是**协作式**的：到点后框架放弃等待并计失败，但不遵守 ctx 的 Fn 会在后台继续跑到自然结束——结果写进带缓冲的 done 通道被丢弃，不阻塞也不泄漏。

这就是 Go 并发没有抢占式取消的现实：超时的语义是"不再等"，不是"强制杀"。

## Pitfalls & Q&A

- **串行提交 + 阻塞背压 = 提交方自我阻塞**：现象是测试里 20 个 Submit 串行发出，第 11 个开始卡住 50 秒。原因：队列与 worker 全满，Block 策略让提交方等槽位，而前面的任务各睡 10 秒——背压真实生效，是调用方的节奏错了。解法：并发提交，或按吞吐估算队列与 worker 配比（本次开发实录，已写进 TestCancelFastStop 注释）
- **把 timeout 当成"强制杀"**：现象是超时任务"结束后"资源仍在消耗。原因：协作式取消只对看 ctx 的任务生效。解法：任务体内用 `select { case <-ctx.Done() ... }` 周期检查；真杀不死的外部调用靠底层连接超时兜底
- **Stop 时 close 队列早于排空**：现象是偶发 `send on closed channel` panic。原因：close 后仍有提交方赢得 select 的发送分支。解法：按上文三步顺序，用 pending.Wait 把"无发送方"变成不变式，而不是靠时序运气
- **守恒断言写成 `Submitted == Succeeded + Failed + Panicked`**：现象是测试偶发失败。原因：Panicked 是 Failed 的子集计数，再加一遍就重复统计。解法：断言用三元守恒，Panicked 只做构成分析

**Q：阻塞和拒绝两种背压怎么选？**
A：看上游能否承受等待。内部调用链上游是自己人（能睡、能重试），选 Block，天然削峰；上游是用户请求（不能吊死），选 Reject 并返回限流语义的错误，把降级决策交回去——这和 [22_load_shapes](../22_load_shapes/) 里突发负载的结论一致。

**Q：任务优先级、去重、持久化为什么不做？**
A：它们分别属于调度器（堆/时间轮）、singleflight（见 [09_singleflight](../09_singleflight/)）与消息队列的领地。本框架刻意停在"有界执行"这一层——识别组件的边界，与实现它同样重要。

**Q：这条守恒律在分布式下还成立吗？**
A：单机内存态下它是精确的；一旦任务跨进程传递，"提交成功"与"必然有结局"之间就隔了网络与磁盘，需要确认语义与对账机制补齐。守恒律的价值恰恰在把"哪些丢不得"变成了可断言的机器检查。

**Q：本系列 23 篇是如何串起来的？**

| 能力 | 来自 |
|---|---|
| worker 池与流水线骨架 | [17_generics_prims](../17_generics_prims/) |
| 取消/超时（协作式） | [18_mini_context](../18_mini_context/) |
| panic 转错误、失败联动 | [19_errgroup](../19_errgroup/) |
| 原子计数与内存可见性 | [02_atomic_orders](../02_atomic_orders/) |
| 伪共享与分片计数 | [03_false_sharing](../03_false_sharing/)、[21_counter_bench](../21_counter_bench/) |
| 背压与负载形状 | [22_load_shapes](../22_load_shapes/) |
| 泄漏门禁 | [20_goleak_gate](../20_goleak_gate/) |
| 去重与请求合并 | [09_singleflight](../09_singleflight/) |
