# 20 · goleak 门禁：goroutine 快照 diff 当泄漏防护测试基建

> 把 go.uber.org/goleak 的 VerifyTestMain 接成包级门禁：测试跑完做一次 goroutine 快照比对，谁漏收谁抓红。本篇含抓红→修复→转绿的完整现场、误报场景与豁免手法，以及可复制到其他库的三行接入模板。

## Background

goroutine 泄漏的老检查法是收尾时打印 `runtime.NumGoroutine()`，和启动
时的基线比大小。计数能看出"多了几个"，却答不出"多的是谁、卡在哪一行"。

这个差距在真实排障里是致命的：泄漏点藏在几十个后台任务里，没有调用栈
就只能靠二分加打印去猜。而泄漏本身有个特点——它不是运行期错误，-race
完全不管，测试照绿，只有进程寿命被悄悄吃掉。

goleak（Uber 开源的泄漏防护库）的答案是让测试自己当门禁：每个测试包
结束时自动做一次全量 goroutine 快照比对，发现来历不明的 goroutine 就
报错并贴出完整调用栈。

## What

**定义**：goleak 是"goroutine 快照 diff"式测试断言库——比对测试前后
的存活 goroutine 集合，多出来的即泄漏嫌疑犯。

| 部件 | 职责 |
|---|---|
| `VerifyTestMain(m)` | 包级门禁：`m.Run()` 之后全包比对一次 |
| `VerifyNone(t)` | 单测级断言：此刻必须是"干净快照" |
| `Find(...)` | 返回 error 而不置败，适合自定义处理 |
| `IgnoreTopFunction / IgnoreAnyFunction` | 按函数名豁免指定栈 |
| `IgnoreCurrent()` | 把此刻全部 goroutine 记入豁免名册 |

可以把 goleak 想象成散场后的查场员：演出结束（测试跑完）清点还坐在
座位上的观众。但和查场员不同的是，它有白名单——testing 框架自己的
工作人员不算观众，白名单外的都要求出示离场理由。

## When to Use

- 给并发库测试挂常驻门禁：Chan/Mutex/context/errgroup 这类拥有后台
  goroutine 的库，泄漏测试比功能测试更容易失守
- 修一个已知泄漏后防止复发：门禁比人工 review 可靠
- CI 里替代"跑完看 NumGoroutine 日志"的手工习惯

何时不用：纯计算函数的测试包没有 goroutine，挂门禁只是空转；依赖
第三方 SDK 且 SDK 有合法常驻 goroutine 时，豁免名单会比断言还长。

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| `NumGoroutine` 计数 | 只知数量不知身份 | 粗排查、压测脚本 |
| goleak 包级门禁 | 自动、带调用栈、零侵入 | 并发库测试标配 |
| goleak 单测断言 | 精确到某测试退出点 | 关键生命周期用例 |
| pprof 快照人工分析 | 全量信息但靠人盯 | 事后深挖，不当门禁 |

## Quick Start

前置条件：go 1.26 与 `go.uber.org/goleak`（本模块 go.mod 已接入）。
运行分步演示：

```bash
go run ./20_goleak_gate/
```

真实输出（goroutine 基线每次不同，差值固定）：

```text
== 第 1 步：goleak 三步原理 ==
快照(runtime.Stack 全量抓栈) → 过滤(testing/syscall/signal/trace 白名单) → 比对(剩余即泄漏)。
VerifyTestMain 把比对挂在 m.Run() 之后：任何测试漏收的 goroutine 都在包级被抓红。

== 第 2 步：泄漏版 worker——起了没地方收 ==
NumGoroutine：1 → 2（+1，且没有任何句柄能收回它）

== 第 3 步：修复版 worker——退出权交给 ctx ==
NumGoroutine：2 → 3 → cancel 后回到 2（基线每次不同，回落是固定的）

== 第 4 步：把门禁挂上——三条命令 ==
抓红演示：GOLEAK_RED=1 go test -run TestVerifyNoneCatchesLeak ./20_goleak_gate/
误报演示：GOLEAK_RED=1 go test -run TestFalsePositiveAndIgnore ./20_goleak_gate/
门禁常规：go test -race ./20_goleak_gate/   （TestMain 已挂 VerifyTestMain）
```

抓红现场（`GOLEAK_RED=1 go test -race -run TestVerifyNoneCatchesLeak
-v`，泄漏测试默认跳过以防门禁常红）——报错直接点名泄漏函数与出处行：

```text
    goleak_test.go:41: found unexpected goroutines:
        [Goroutine 20 in state chan receive, with concurrency/20_goleak_gate.leakyWorker on top of the stack:
        concurrency/20_goleak_gate.leakyWorker(...)
        	.../20_goleak_gate/main.go:27
        created by concurrency/20_goleak_gate.StartLeakyWorker in goroutine 19
        	.../20_goleak_gate/main.go:33 +0x94
        ]
--- FAIL: TestVerifyNoneCatchesLeak (0.45s)
```

修复方式见 main.go 的 StartWorker：把退出权交给 ctx。转绿记录：

```bash
go test -race ./20_goleak_gate/   # ok  1.231s
```

## How It Works

VerifyTestMain 的接入只需三行，模板适用于任何库的测试包（15_mini_channel、
17_generics_prims、18_mini_context、19_errgroup 均可直接套用）：

```go
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m) // 测试全部通过后做一次全包快照比对
}
```

快照比对的内部是 `runtime.Stack(buf, true)` 全量抓栈，解析出每个
goroutine 的栈帧集合，先过四类默认白名单（testing 框架、CGo syscall、
os/signal、runtime.ReadTrace），再跳过当前 goroutine 自身，剩下的
逐个报错。

你在抓红现场看到的 `state chan receive` 与函数名，就来自
这份解析结果。

Find 并非只查一次：它按指数退避重试至多 20 次（单次间隔上限 100ms，
总宽限约 2 秒），吸收"测试已返回、goroutine 正在收尾"的时序竞态。
宽限之后仍在的，才定性为泄漏。

VerifyTestMain 有一个容易忽略的前提：`m.Run()` 返回非 0（有测试失败）
时它直接跳过泄漏检查。所以关键生命周期用例应在函数内显式加
`defer goleak.VerifyNone(t)`，不能只依赖包级兜底。

## Pitfalls & Q&A

**坑 1：VerifyNone 与 t.Parallel 不兼容。** 并行测试的 goroutine 混在
同一份快照里，无法归属到具体测试，他人正常的后台任务会算到你头上。
并行包用 VerifyTestMain 兜底，单测级断言留给串行用例。

**坑 2：豁免被当修复。** `IgnoreAnyFunction("...backgroundWorker")`
能让红转绿，但 goroutine 依旧在漏。

豁免只能用于"确属合法常驻"的栈，
且名单要随代码 review——误报现场与豁免后的对照见
`GOLEAK_RED=1 go test -run TestFalsePositiveAndIgnore -v` 的输出。

**Q：后台 timer 或 GC goroutine 会造成误报吗？** 分两类。runtime 的
常驻线程（GC 标记、扫除、定时器）多数不占用户 goroutine 或已被白名单
吸收。

已知例外是 `runtime.gcBgMarkWorker`，它不在默认白名单且出现时机
随 GC 波动，会偶发抓红，解法是
`goleak.IgnoreTopFunction("runtime.gcBgMarkWorker")`。

真正高频的误报源
是第三方库的合法常驻 goroutine（net/http 空闲连接读循环、连接池），即
本篇演示的 backgroundWorker 形态。

**Q：为什么 19_errgroup 敢说"Wait 返回即组内清零"？** 因为它的
联结器是 WaitGroup，`wg.Wait()` 返回即全部任务已退出，这一承诺正好
是 goleak 快照比对能通过的前提；反例见该篇第 5 步的裸 goroutine 对照。
