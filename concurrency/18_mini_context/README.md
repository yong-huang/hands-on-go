# 18 · mini context：cancel 树传播与超时级联的复刻

> 复刻 context 的 cancelCtx/timerCtx/valueCtx 三件套：children 登记表、递归取消、close(done) 广播与 Value 链回溯，把"子随父亡、子不弑父、先到先得"三条协议钉进 -race 测试。读完能说清取消是怎么沿树走的、在哪几处会出竞态窗口。

## Background

goroutine 生命周期的老问题是"开关传不到底"：一个请求链上有五层
goroutine，想在中途放弃，就得给每层函数签名加一个退出 channel，
每层自己约定收尾顺序。层数一深，总有某层忘了转发退出信号，下游
goroutine 就此无家可归。

context（Go 1.7 进入标准库的取消传播原语）把退出信号组织成一棵树：
每个节点只回答"该停了吗"，取消沿树自动向下传播，请求级元数据也沿
树携带。它的实现没有并发魔法，核心是一张登记表加一次 channel 关闭
广播。

本篇徒手复刻三件套（先写后对照源码，同
[15_mini_channel](../15_mini_channel/) 的路线），把协议的三个不变式
钉进测试。

## What

**定义**：mini context 是树形取消传播库，Context 接口四方法与标准库
同形，取消原因只有 Canceled 与 DeadlineExceeded 两种。

| 部件 | 职责 |
|---|---|
| `cancelCtx` | done 通道 + children 登记表 + err；cancel 关闭广播并递归 |
| `timerCtx` | 内嵌 cancelCtx 加计时器，到点自动调用 cancel |
| `valueCtx` | 单键值对，Value 沿父链逐层回溯，就近命中 |
| `propagateCancel` | 登记协议：认得父就进 children，不认得起 watcher |

可以把取消树想成多米诺骨牌：推倒第一张（关根的 done）整行倒下。
但和多米诺不同的是，每张牌也能自己倒下（子先取消）——倒下即退出
队伍，不会去推后面的牌，这就是隔离。

## When to Use

- 想回答"取消是怎么从父走到第五层叶子"时，实现一遍胜过读十遍
- 阅读 src/context 源码前的预热：登记、广播、回溯三协议就是全部
- 需要观察取消耗时的实验：内部结构可断言，标准库不可透视

什么时候必须用标准库：

- 一切生产代码：Cause 体系、AfterFunc、与 net/http 的深度整合
- 需要跨包传递取消语义：标准库类型是事实契约，自制类型无人认领

| 方案 | 差异 | 什么时候选它 |
|---|---|---|
| 标准库 context | 全功能，多年实战淬炼 | 默认选择 |
| mini context（本篇） | 三件套核心协议，无衍生 API | 学习与实验 |
| 自制退出 channel 逐层传 | 无树形复用，签名逐层污染 | 不建议，超过两层必漏 |

## Quick Start

前置条件：go 1.26，纯标准库。运行分步演示：

```bash
go run ./18_mini_context/
```

真实输出（第 2 步耗时每次不同，量级为百微秒）：

```text
== 第 1 步：取消树的三原则 ==
子随父亡（cancel 沿 children 递归）；子不弑父（只摘自己的登记）；
先到先得（重复 cancel 幂等）。

== 第 2 步：5 层树取消根，全部叶子 Done 的耗时 ==
cancel 后 10 个叶子全部 Done：79.833µs（每次不同，验收线 < 10ms）

== 第 3 步：超时级联——父超时，子自动取消且继承原因 ==
子 Done 后 Err = deadline exceeded（固定）

== 第 4 步：子先取消——父与兄弟不受影响 ==
30ms 后父与兄弟均未 Done，son.Err = context canceled（固定）

== 第 5 步：Value 链就近命中 ==
req = 42（固定）
user = alice（固定，跨层回溯）
missing = <nil>（固定，无此键返回 nil）
```

验收命令（十条测试见 context_test.go，实测记录）：

```bash
go test -race -count=10 ./18_mini_context/   # ok  2.597s
```

## How It Works

cancel 是五步动作：查 err 非空则幂等返回；置取消原因；`close(done)`
广播；沿 children 递归取消整棵子树；登记表置 nil。持锁递归安全，因为
锁序永远是父到子，全库没有反向获取路径。

第 2 步实测 10 个叶子 79.833µs 全部 Done，比 10ms 验收线快约两个
数量级——广播本身只有一次 close 加十次唤醒。

propagateCancel 处理三个窗口：父已取消（登记时复查，孩子生来即取消
态）；父是本库节点（登记进 children map）；父是外部实现（起 watcher
goroutine）。

watcher 的 select 同时盯 parent.Done 与 child.done，两头谁先响都退出。
"先登记、后返回"与 [07_mini_cond](../07_mini_cond/) 的先领票再入睡是
同一不变式：登记完成前，通知不允许发出。

Deadline 有一个标准库同款短路：父的截止时间更早时直接退化为
WithCancel——父先到点自然把子带走，子自己的计时器没有存在意义。

主动取消的 CancelFunc 里先 `timer.Stop()` 再 cancel：省掉计时器一次
无用到点唤醒。Err 归属由 cancel 的先到先得幂等性决定，与两个调用的
先后无关（TestActiveCancelBeatsDeadline 断言主动取消落在 Canceled）。

**与标准库的差异表**（盲写后对照 Go 1.26 src/context/context.go）：

| # | 差异点 | 标准库做法 | 后果 |
|---|---|---|---|
| 1 | 无 Cause 体系 | WithCancelCause/Cause（1.20+）携带任意取消原因 | 只有 Canceled/DeadlineExceeded 两种错误可辨 |
| 2 | done 构造即建 | 1.21+ 惰性创建，首次 Done/cancel 才分配 | 每个 ctx 多一次 chan 分配，语义无差 |
| 3 | DeadlineExceeded 是普通 error | 标准库版实现 net.Error（Timeout()=true） | 不能被 net 栈识别为超时错误 |
| 4 | 无 AfterFunc/registerOnCancel | 1.21+ 的 AfterFunc 及多 cancel 联结 | 回调式收尾要自己用 goroutine 包 |

## Pitfalls & Q&A

**坑 1：子 cancel 回父链摘自己的登记。** 摘除要拿父的锁，与父 cancel
持锁递归形成父子锁环。本实现的登记生命周期完全归父管——父取消后把
children 置 nil，子侧永不触碰父，锁序因此保持单向。

**坑 2：拿到 CancelFunc 却不调用。** 计时器会攥着整棵子树直到到点，
期间的 goroutine 与内存都无法回收（标准库有 lostcancel vet 检查兜底）。
修法：`defer cancel()` 作为固定搭配。

**坑 3：watcher 忘了同时盯 child.done。** 子先取消后 watcher 仍挂在
parent.Done 上不退出，引用与 goroutine 双泄漏。select 必须两端都等。

**Q：为什么用 close(done) 而不是给等待者逐个发信号？** 等待者数量
未知且随时增减，close 是天然的幂等广播：一次操作唤醒全部，后来者
读已关闭通道立即返回。这与 [15_mini_channel](../15_mini_channel/) 里
Close 的广播是同一机制。

**Q：cancel 持锁递归为什么不会栈爆？** 树深受调用方结构约束，cancel
递归深度等于树深；标准库同样递归。若担心超深树，可改成显式栈迭代，
协议不变。
