# 12 · ConnPool 连接池：空闲队列 + 信号量限流

> 连接的创建与销毁远贵于复用：TCP 三次握手加认证动辄几十毫秒，fd 和内存却是实打实的占着。连接池用「带缓冲 channel 做空闲队列 + Mutex 保护 openCount 做限流」这两个最朴素的 Go 原语，实现了 Get/Put 两个接口背后的全部机制：优先复用、按需新建、超限拒绝、空闲超时驱逐。本实验用不到 200 行标准库代码把它写出来并跑给你看。

## 1. 为什么需要它

三种获取连接的方式对比：

| 方案 | 优点 | 缺点 |
|------|------|------|
| **每次新建** | 实现简单 | 延迟高（TCP 握手 + 认证）、资源浪费、容易耗尽 fd |
| **全局单连接** | 延迟最低 | 无法并发、成为性能瓶颈 |
| **Connection Pool** | 复用连接、并发安全、资源可控 | 需要管理生命周期、防止泄漏 |

连接池本质是**对象池模式**：对象用完归还复用而不是销毁，但对「连接」还要多管两件事——总量上限（保护下游）和坏连接识别（对端可能早就把空闲连接关了）。

## 2. 总览：核心机制一图看懂

![ConnPool：空闲队列 + 信号量限流](images/connpool.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/interview/12_conn_pool/images/connpool.html)
> （或本地打开 [`images/connpool.html`](images/connpool.html)）。

**心智模型一句话**：`idle chan *conn` 是空闲队列，`mu + openCount` 是信号量——Get 先非阻塞取空闲、取不到再看配额建新的，Put 归还入队、队列满或连接超时就丢弃。

## 3. 快速开始

```bash
cd interview/12_conn_pool
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

三个阶段分别演示：并发 Get/Put 的复用、把 maxOpen 取满后再 Get 被拒绝、空闲超时——注意最后一个细节：**超时后 `Len()` 仍显示 idle=5**，因为驱逐发生在下一次 Get 时（惰性清理），不是后台定时扫描。

## 4. 核心概念

### 4.1 带缓冲 channel 做空闲队列

```go
idle chan *conn   // make(chan *conn, maxOpen)
```

buffered channel 天然适合做空闲队列：`select` + `default` 实现非阻塞获取；队列满时 Put 走 default 分支自动丢弃多余连接。不需要自己维护 slice + 锁。

### 4.2 Mutex + openCount 做信号量限流

```go
mu        sync.Mutex
openCount int       // 当前打开的连接数（idle + 使用中）
maxOpen   int       // 0 表示不限制
```

openCount 必须在锁保护下修改，否则并发 Get 会竞态超限。取满后再 Get 直接返回 `conn pool: max open connections reached`——本实现选择「快速失败」，生产级实现（如 `database/sql`）通常选择「阻塞等待归还」。

### 4.3 Get：先复用，再新建

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

关键点是 `default` 分支保证不会阻塞在 idle channel 上；命中过期连接时先 `openCount--`（这条连接等于被关闭了），再转入新建路径。

### 4.4 Put：归还与健康检查式的丢弃

Put 把连接包回 `&conn{resource: c, createdAt: time.Now()}` 放回队列；队列满则丢弃并 `openCount--`。「健康检查」在本实现里体现为**时间维度的体检**：createdAt 是归还时刻，下一次 Get 时超龄即丢弃——这防止了对端早已关闭的 stale connection 被复用。生产级连接池还会加 Ping 检查（如 `SELECT 1`）和 MaxLifetime。

## 5. 关键字段与坑清单

| 字段/表达式 | 含义 | 为什么重要 |
|---|---|---|
| `idle chan *conn` | 空闲队列，容量 = maxOpen | select+default 提供非阻塞取/放 |
| `openCount int` | 存活连接总数（idle + 在用） | 信号量：≥maxOpen 时 Get 拒绝 |
| `createdAt` | **归 Put 时**打的时间戳 | 超时从「归还」起算，不是从创建起算 |
| `idleTimeout` | 空闲最长存活时间 | 驱逐是惰性的，发生在下一次 Get |
| `Close()` | `close(idle)` 后 range 排干 | 连接是 type-erased 的，真正的 Close 需调用方管理 |

坑清单：

- **驱逐不及时**：demo 第三阶段超时后 `idle` 仍是 5，别误以为有后台 goroutine 在扫描
- **openCount 漏减**：过期连接分支必须先 `openCount--`，否则配额被「幽灵连接」永久占用
- **factory 失败要回滚**：`createNew` 失败时 `openCount--`，否则一次失败就永久少一个配额
- **`maxOpen=0` 语义**：代码里表示不限制（`p.maxOpen > 0 && ...`），不是「不允许连接」
- **泄漏**：Get 后忘记 Put，openCount 只增不减，最终全部 Get 返回 error；用 `defer pool.Put(c)` 兜底

## 6. 文件结构

```text
interview/12_conn_pool/
├── README.md                          # 本文件：原理 + 运行指引 + 深入要点
├── conn_pool.go                       # 主教材：池核心（Get/Put/Close）+ 三阶段 demo
└── images/
    ├── connpool.architecture.json     # 图源：空闲队列 + 信号量限流架构（Typed JSON IR）
    ├── connpool.html                  # 交互版：可缩放/高亮连线的自包含页面
    └── connpool.svg                   # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：maxOpen 达到后应该怎么办？本实现是什么策略？**
A：本实现快速失败，直接返回 error（demo 输出 `max open connections reached` 可证）。生产级常用**阻塞等待**：select 带超时等 idle channel 归还，即「Get 空则建、建不了则等」；还有 LRU 驱逐、动态扩容等策略。

**Q：空闲连接的健康检查怎么做？**
A：本实现只做时间维度——Put 时记 createdAt，Get 时超 `idleTimeout` 即丢弃重建，防止 stale connection。生产级还要加：主动 Ping（`SELECT 1`）、MaxLifetime（连接总寿命）、以及后台定时清理（本实现是惰性驱逐）。

**Q：连接泄漏怎么检测和预防？**
A：泄漏 = Get 后未 Put，openCount 只增不减直到 maxOpen 后全部失败。检测：监控 openCount 接近 maxOpen 且 idle=0；预防：`defer Put`、给连接加使用超时、Get 时带上调用栈便于归因。

**Q：为什么 idleTimeout 从 Put 时刻起算？**
A：归还时刻才是「开始空闲」的时刻；若从创建起算，一个高频复用的健康连接会被误杀。对应地，连接的「总寿命」要用另一个字段 MaxLifetime 单独管。

**Q：和 sync.Pool 的区别？**
A：sync.Pool 由 GC 驱动、两拍清空，对象何时被回收不可控，且存进去的连接没人负责 close——连接是稀缺资源需要显式上限与健康检查，必须用带 maxOpen/idleTimeout 语义的自定义池；sync.Pool 只适合临时对象（buffer 等）复用。

## 8. 总结

ConnPool = **buffered channel（空闲队列）+ Mutex/openCount（信号量）+ factory（新建）**：Get 先非阻塞复用、无空闲看配额、超限拒绝；Put 归还入队、满则丢弃；超时连接惰性驱逐。整套机制没有任何花哨原语，难点全在边界分支——漏减 openCount、忘记回滚 factory 失败，都是面试官爱挖的地方。

下一篇 [13_consistent_hash](../13_consistent_hash/) 看分布式场景的另一个必考点：哈希环如何用虚拟节点让「摘除一台机器」只影响相邻弧段的流量。
