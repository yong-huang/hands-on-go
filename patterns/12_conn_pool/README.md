# 12 · ConnPool 连接池：空闲队列 + 信号量限流

> 连接的创建与销毁远贵于复用：TCP 三次握手加认证动辄几十毫秒，fd 和内存却是实打实的占着。连接池用「带缓冲 channel 做空闲队列 + Mutex 保护 openCount 做限流」这两个最朴素的 Go 原语，实现了 Get/Put 背后的全部机制：优先复用、按需新建、超限拒绝、空闲超时驱逐。本篇用不到 200 行标准库代码实现它，并逐段讲清每个分支。

## Background

访问下游服务（数据库、RPC 服务）时，最直接的用法是每次操作新建一条连接。代价有两层：TCP 三次握手加认证动辄几十毫秒，延迟全部摊在业务路径上；fd（文件描述符，进程打开资源的句柄，系统有上限）和内存实打实被占着，高并发下容易耗尽。

反方向的做法是全局共用一条连接：延迟最低，但所有请求排队串行，单连接成为性能瓶颈。

两个极端都走不通，中间路线是用完归还、复用而非销毁——这就是对象池（用完归还复用而非销毁的资源管理模式）的思想。但对「连接」还要多管两件事：总量上限（保护下游不被打挂）和坏连接识别（对端可能早已悄悄关闭空闲连接）。

## What

**连接池（Connection Pool）**是一种对象池模式的并发资源管理结构：维护一组已建立的连接供并发使用，用完归还复用而不是销毁，并对总量设上限、对空闲连接设寿命。

心智模型一句话：`idle chan *conn` 是空闲队列，`mu + openCount` 是信号量——Get 先非阻塞取空闲、取不到再看配额建新的，Put 归还入队、队列满或连接超时就丢弃。

可以把连接池想象成公司楼下的共享自行车棚：车骑完还回车棚而不是扔掉，车棚容量有限（maxOpen），车棚满了后来的车不收（丢弃），长期没人骑的车定期清走（空闲超时）。但和自行车棚不同的是：这里的"车"可能在对端悄悄坏掉——服务端可能早已关闭这条空闲连接，所以取车时还要查一眼"停放时长"。

## When to Use

这节回答"什么服务值得自己管连接，什么场景不必"。

典型场景：

- **高频短操作访问下游**：每个请求都要查询数据库、发起 RPC——几十毫秒的握手加认证乘以请求量，就是实打实的延迟与开销。
- **下游有承载上限**：数据库的最大连接数有限，必须用 maxOpen 封顶，把并发压力挡在池这一层。
- **对端会断开空闲连接**：服务端通常有 idle 超时，池需要超时驱逐与坏连接识别来兜底。

何时不用：

- 一次性脚本、几十次请求：直接新建更简单。
- 临时对象复用（buffer、临时对象）：`sync.Pool` 更合适——它没有上限与健康检查语义（区别见 Pitfalls 的 Q）。
- 生产服务直接用现成实现：`database/sql` 等库自带连接池，手写主要用于理解原理。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 每次新建 | 实现简单；延迟高（TCP 握手 + 认证）、资源浪费、容易耗尽 fd | 一次性、极低频的调用 |
| 全局单连接 | 延迟最低；无法并发、成为性能瓶颈 | 严格串行的单点任务 |
| **Connection Pool** | 复用连接、并发安全、资源可控；需要管理生命周期、防止泄漏 | 常驻服务里高频访问下游 |

## Quick Start

前置条件：只需安装 Go，纯标准库实现。

```bash
cd patterns/12_conn_pool
go run conn_pool.go
```

预期输出（节选，goroutine 完成顺序每次运行可能不同）：

```text
ConnPool: maxOpen=5, idleTimeout=3s

  goroutine 3: acquired conn-1 (idle=0, open=3)
  goroutine 1: returned conn-2 (idle=1)
After phase 1: idle=3, open=3

  acquired conn-2 (open=3)
  acquired conn-5 (open=5)
  extra Get (should fail): conn pool: max open connections reached

After returning all: idle=5, open=5
Before idle timeout: idle=4
  Put back, waiting 3.5s for timeout...
After idle timeout: idle=5

Pool closed.
```

三个阶段分别演示：并发 Get/Put 的复用、把 maxOpen 取满后再 Get 被拒绝、空闲超时。注意最后一个细节：**超时后 `Len()` 仍显示 idle=5**，因为驱逐发生在下一次 Get 时（惰性清理），不是后台定时扫描。

池的两个核心原语只有十行：

```go
idle chan *conn   // make(chan *conn, maxOpen)
```

这段在做什么：buffered channel 天然适合做空闲队列——`select` + `default` 实现非阻塞获取；队列满时 Put 走 default 分支自动丢弃多余连接。不需要自己维护 slice + 锁。

```go
mu        sync.Mutex
openCount int       // 当前打开的连接数（idle + 使用中）
maxOpen   int       // 0 表示不限制
```

这段在做什么：`openCount` 是配额计数（信号量），必须在锁保护下修改，否则并发 Get 会竞态超限。取满后再 Get 直接返回 `conn pool: max open connections reached`——本实现选择「快速失败」，生产级实现（如 `database/sql`）通常选择「阻塞等待归还」。

## How It Works

按一次借还的路径走：Get 先取空闲 → 没有则按配额新建 → 用完 Put 归还 → 超时连接在下一次 Get 时被丢弃。

### Get：先复用，再新建

```go
select {
case c := <-p.idle:
    if time.Since(c.createdAt) > p.idleTimeout {
        // 过期连接：openCount-- 后改走新建
        return p.createNew()
    }
    return c.resource, nil
default: // 队列空，不阻塞
}
```

这段在做什么：`default` 分支保证不会阻塞在 idle channel 上；命中过期连接时先 `openCount--`（这条连接等于被关闭了），再转入新建路径。输出里 `acquired conn-1 (idle=0, open=3)` 的两个计数，就分别对应空闲队列长度与配额占用。

### Put：归还与健康检查式的丢弃

Put 把连接包回 `&conn{resource: c, createdAt: time.Now()}` 放回队列；队列满则丢弃并 `openCount--`。

「健康检查」在本实现里体现为**时间维度的体检**：createdAt 是归还时刻，下一次 Get 时超龄即丢弃——这防止了对端早已关闭的 stale connection（陈旧连接）被复用。生产级连接池还会加 Ping 检查（如 `SELECT 1`）、MaxLifetime 和后台定时清理；本实现的驱逐是惰性的。

### 关键字段一览

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `idle chan *conn` | 空闲队列，容量 = maxOpen | select+default 提供非阻塞取/放 |
| `openCount int` | 存活连接总数（idle + 在用） | 信号量：≥maxOpen 时 Get 拒绝 |
| `createdAt` | **归 Put 时**打的时间戳 | 超时从「归还」起算，不是从创建起算 |
| `idleTimeout` | 空闲最长存活时间 | 驱逐是惰性的，发生在下一次 Get |
| `Close()` | `close(idle)` 后 range 排干 | 池把连接存成 `interface{}`，具体类型被擦除（type-erased），池不知道怎么真正关闭它，Close 需调用方管理 |

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **驱逐不及时**：现象是 demo 第三阶段超时后 `idle` 仍是 5。原因：驱逐是惰性的，发生在下一次 Get。解法：别误以为有后台 goroutine 在扫描；需要及时性就加后台定时清理。
- **openCount 漏减**：现象是配额被「幽灵连接」永久占用。原因：过期连接分支忘了 `openCount--`。解法：丢弃连接的每个分支都配对减计数。
- **factory（即新建连接的 createNew 回调）失败要回滚**：现象是一次失败就永久少一个配额。原因：`createNew` 失败时没做 `openCount--`。解法：失败路径回滚计数。
- **`maxOpen=0` 语义误解**：现象是把 0 当「不允许连接」。原因：代码里 0 表示不限制（`p.maxOpen > 0 && ...`）。解法：把哨兵值语义（用特定取值代表特殊含义，此处 0=不限制）写进文档。
- **连接泄漏**：现象是最终全部 Get 返回 error。原因：Get 后忘记 Put，openCount 只增不减。解法：见下方 Q 的检测与预防清单。

**Q：maxOpen 达到后应该怎么办？本实现是什么策略？**
A：本实现快速失败，直接返回 error（demo 输出 `max open connections reached` 可证）。

生产级常用**阻塞等待**：select 带超时等 idle channel 归还，即「Get 空则建、建不了则等」；还有 LRU 驱逐、动态扩容等策略。

**Q：连接泄漏怎么检测和预防？**
A：泄漏 = Get 后未 Put，openCount 只增不减直到 maxOpen 后全部失败。检测：监控 openCount 接近 maxOpen 且 idle=0；预防：`defer Put`、给连接加使用超时、Get 时带上调用栈便于归因。

**Q：为什么 idleTimeout 从 Put 时刻起算？**
A：归还时刻才是「开始空闲」的时刻；若从创建起算，一个高频复用的健康连接会被误杀。对应地，连接的「总寿命」要用另一个字段 MaxLifetime 单独管。

**Q：和 sync.Pool 的区别？**
A：sync.Pool 由 GC 驱动、两拍清空，对象何时被回收不可控，且存进去的连接没人负责 close——连接是稀缺资源需要显式上限与健康检查，必须用带 maxOpen/idleTimeout 语义的自定义池；sync.Pool 只适合临时对象（buffer 等）复用。
