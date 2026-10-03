# 04 · data race 检测器：原理、边界与开销

> `go test -race` 能抓住绝大多数数据竞态，但它既不是全部、也不免费。本篇拆开 ThreadSanitizer 的判定模型，构造一个稳定复现的漏报案例，逐行注解一份真实报告，最后把开关 -race 的时间与内存开销实测入档。

## Background

在没有竞态检测器的年代，并发 bug 的发现方式只有两种：code review 与线上偶发故障。

前者的覆盖率取决于 reviewer 对内存模型的理解；后者最痛——一次竞态可能让计数
差 1，也可能让支付重复扣款，平均几千次运行才发作一次，日志里没有堆栈。

Go 官方博客给过一个数字：一个存在数年的 Chrome 缓存 bug，靠人工只发现 1 次，
TSan（ThreadSanitizer，Google 的竞态检测器）的压力测试一次抓到 108 次。

Go 1.1 起把 TSan 集成进工具链：`go` 命令加 `-race` 参数即可启用，代价是程序
变慢、变吃内存。要放心用进 CI，需要知道它凭什么判定、何时沉默、代价几何。

## What

**定义**：`-race` 是编译期插桩加运行时检测的组合。编译器在每次内存访问前
插入对检测器的调用；检测器在影子内存（shadow memory，按应用内存地址一一
对应的元数据区）里为每个 8 字节单元记录"谁、读还是写、在什么逻辑时刻"。

"逻辑时刻"由 vector clock（向量时钟，每个线程维护一个"我知道其他线程各自
进行到哪"的计数向量）刻画。两个访问一写一读或两写、来自不同线程、且时钟
无法排出先后——三个条件同时成立，报告 DATA RACE。

**心智模型**：可以把影子内存想成每个房间门口的访客登记本，进来一个人记一行
"谁、读还是写、第几号时钟"。但和登记本不同的是它只有 4 行：写满后新记录
挤掉最旧的，被挤掉的人从此查无此人——这正是本篇漏报案例的机关所在。

## When to Use

什么时候开 `-race`：

- CI 的全量测试回归：竞态测试的价值在"经常跑"，不在"跑一次"
- 排查偶发故障：线上问题若怀疑竞态，先在能复现的最小负载下开 -race 跑
- 新写的并发组件：高压测试（如 `-count=200`）加 -race 是最低成本的正确性门禁

什么时候不该开：

- 生产服务常开：本机实测时间开销约 7.5 倍、内存约 4 倍（见 How It Works 的表）
- 性能基准：插桩后的 ns/op 不能与正常构建的数字混着比

同类手段对比：

| 手段 | 差异 | 什么时候选它 |
|---|---|---|
| `-race` | 只报发生过的交错，漏报静默 | 测试与回归，检出率远超人工 |
| code review | 依赖推理，可发现未发生的隐患 | 与 -race 互补，不可替代 |
| 压测 | 触发真实负载下的交错 | 搭配 -race 使用，否则只看吞吐 |

## Quick Start

前置条件：Go 工具链自带 -race，纯标准库，无需安装。

```bash
go run main.go        # 正常构建：两组竞态都静默
go run -race main.go  # 第 2 步报 WARNING；第 3 步三组全静默（漏报）
```

`go run main.go` 的一次真实输出（节选）：

```text
== 第 2 步：标准竞态——两个 goroutine 无同步对同一变量自增 ==
（已运行。go run -race main.go 时，上方应有 WARNING: DATA RACE，
  条数随交错而定，一至两条；不带 -race 时丢失的自增无声无息）

== 第 3 步：漏报构造——真竞态，但访问记录被逐出 ==
T1 读 x 后与外界失联；16 个垫写者写同一 8 字节单元里的 y
--- 第 1 组（若到下一组标题前没有 WARNING，即为漏报）---
--- 第 3 组（若到下一组标题前没有 WARNING，即为漏报）---
（固定）若 go run -race main.go 的第 3 步三组全静默，漏报稳定复现。
```

`go run -race main.go` 时第 2 步打印 `WARNING: DATA RACE` 与两段访问来源栈，
末行汇总 `Found 2 data race(s)`；第 3 步三组保持静默——那就是漏报。

## How It Works

先看一份真实报告。用测试标签产出（普通 `go test ./...` 不含此用例）：

```bash
go test -race -tags race_demo -run TestDetectedRaceSample ./04_race_detector/
```

报告（节选，路径缩短）与逐行注解：

```text
WARNING: DATA RACE
Read at 0x00c0001941e8 by goroutine 9:
  concurrency/04_race_detector.detectedRace.func2()
      .../main.go:75 +0x88
Previous write at 0x00c0001941e8 by goroutine 8:
  concurrency/04_race_detector.detectedRace.func1()
      .../main.go:69 +0x98
Goroutine 9 (running) created at:
  concurrency/04_race_detector.detectedRace()
      .../main.go:72 +0x194
==================
```

- 第 1 行：报告头，一个竞态一份；同一处竞态可能因访问方向不同而出多份
- `Read at ...` / `Previous write ...`：后到的访问与先到的访问，永远成对报告；地址是变量 n
- 两个缩进的源码位置：两个访问各自的发生处，对应 `n++` 的读半步与写半步
- `Goroutine 9 (running) created at` 及其后：创建点栈，用来回答"谁起的这个 goroutine"

漏报案例的机理（代码见 main.go 的 missedRace，`-count=200` 稳定复现）：
T1 普通读 x 后与外界失联，与 T2 的普通写 x 之间没有任何 happens-before 链，
按内存模型定义这是真竞态。

但影子单元只装 4 条记录，16 个垫写者把 T1 那条记录挤出单元——垫写者写的是
同单元里字节不重叠的 y（彼此 channel 链式交接），与谁都不构成竞态。

于是 T2 写 x 时单元里只剩垫写者的记录——字节不重叠、时钟有序，判定条件
不成立，报告静默。vector clock 的比较本身是精确的：漏报不是时钟算错，而是
唯一能证明 T1 存在的那条记录已被逐出，时钟无从比起。

开销实测（Apple M5 Pro，18 逻辑核；时间取 `-count=3` 中位数）：

| 指标 | 不带 -race | 带 -race | 倍数 |
|---|---|---|---|
| BenchmarkService ns/op | 49.09 | 370.4 | 7.5x |
| TestServiceLoad 峰值 RSS | 10.8 MB | 42.9 MB | 4.0x |

测量命令：时间 `go test -bench BenchmarkService -run '^$' -count=3`，取中位数。

内存先 `go test -c -o svc.test .` 编出二进制，再 `/usr/bin/time -l
./svc.test -test.run TestServiceLoad` 测峰值 RSS；两条命令各跑开/关两遍。

RSS 必须测二进制而非 `go test` 进程：后者含工具链开销，实测 91 MB 对
102 MB 几乎无差，会得出"-race 不吃内存"的错误结论。

## Pitfalls & Q&A

**坑 1：用"垫读者"逐出旧记录。** 第一稿让 12 个 goroutine 读 x 来挤掉 T1
的读记录，`-count=10` 就翻车：单元满员时新读记录直接被丢弃、不逐出旧记录，
T1 的记录原封不动地留在槽里等着被判。逐出必须靠"写"，本项目改用垫写者。

**坑 2：垫写者彼此构成新竞态。** 16 个 goroutine 无同步地写同一个 y，
-race 如实报出——构造漏报案例时引入的新访问本身必须无竞态。解法是让垫写者
用 channel 链式交接，相邻两次写之间有明确的先后。

**坑 3：把 -race 当完备性证明。** 检测器只报告"测试执行期间实际发生的"
交错。没跑到的代码路径、被逐出影子记录的访问（本篇案例），它都保持沉默。
零报告的含义是"这段测试负载下未观察到竞态"，不是"不存在竞态"。

**Q：有了 GC 的安全网，竞态最多脏一个变量，值得开 -race 吗？** 值得。竞态
破坏的是 happens-before 链，后果不限于脏读：可见性失败能让循环永不退出，
`map` 并发写会直接 panic。检测器的开销买的是测试期的确定性证据，按秒计。
