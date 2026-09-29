# 17 · Mini Redis：用 ~250 行 Go 手写一个 KV 缓存服务器

> Redis 是后端绕不开的话题，理解它最好的方式就是自己写一个。本实验用纯标准库实现了一个基于 TCP 的 Mini Redis：`accept` 后每连接一个 goroutine，行式 RESP-like 文本协议解析，`switch` 命令分派，`map + sync.RWMutex` 的锁保护存储，外加 TTL 过期清理。一条命令从 socket 进到 socket 出的完整数据流，就是一台缓存服务器的全部骨架。

## Background

后端服务几乎都要用缓存（把热点数据放在内存里加速读取的组件），Redis 是这一层的事实标准。但它通常是个黑盒：`SET` 进去的数据存在哪、过期键怎么清理、上万个客户端同时连接怎么服务，调用方都看不到。

拆开这个黑盒，里面是写任何 TCP（可靠字节流传输协议）服务都绕不开的三个问题：连接怎么扛——成千上万个客户端同时接入；字节流怎么变成命令——网络收到的是字节，不是结构化命令；并发怎么不出错——多个执行流同时读写一块内存。三个问题在一台 KV（键值——按 key 存取 value）服务器里同时出现。

理解它们最直接的路径，是用 `net` + `bufio` + `sync` 三个标准库包手写一台最小 KV 服务器。

本实验的 Mini Redis 因此而来：支持 `SET / GET / DEL / EXPIRE / TTL / PING` 六个命令，实现惰性检查 + 每秒全量扫描的双层过期策略，全部代码只有一个 `mini_redis.go`。

## What

**Mini Redis** 是一台用纯 Go 标准库实现的 KV 缓存服务器：监听 TCP 端口接收文本命令，在内存的 map 里存取数据，并用 TTL（time to live，存活时间——键在指定秒数后自动失效）管理过期。它不含持久化与集群，目标是展示一台缓存服务器的最小骨架。

心智模型一句话：每条连接是一个独立 goroutine（Go 的轻量并发执行单元）跑 `读取 → 解析 → 分派 → 执行 → 回写` 的循环，所有 goroutine 汇聚到唯一一把 `RWMutex`（读写锁——读共享、写独占）保护的 `KVStore` 上——连接层并发、存储层串行化。

可以把这台服务器想象成一家只有单个金库的银行：客户各自排队、互不干扰（每连接一个 goroutine），但所有存取请求都汇聚到同一个金库（锁保护下的 map）——读操作允许多人同时查看（RLock），写操作必须独占（Lock）。但和银行不同的是：队伍数量再大，新增成本也几乎可以忽略，且所有客户共用这一个金库，没有分行。

## When to Use

这节回答"什么时候适合写/用这样一台迷你 KV 服务器，什么时候直接上 Redis"。

典型场景：

- **学习网络服务骨架**：连接模型、协议解析、锁保护存储在一个 ~250 行的程序里全部可见，是理解 Redis/Memcached 这类 KV 服务器的最小完备样本。
- **写自定义协议的 TCP 服务**：`accept` 循环 + 每连接 goroutine + 命令分派这条主路径可以直接复用，换掉的只是协议解析一段。
- **单机工具的内嵌存储**：进程内嵌一个轻量 KV，省去部署外部缓存服务。

何时不用：

- 生产缓存：直接用 Redis——本实现没有持久化、主从复制，过期清理是每秒全量扫描。
- value 含空格或二进制数据的场景：内联文本协议靠空格分词，表达不了这类数据。

| 方案 | 差异 | 什么时候选它 |
|------|------|------|
| 本实现的 Mini Redis | 内联文本协议、`map + RWMutex`、惰性 + 定期过期，约 250 行 | 学习连接/协议/并发三件事的最小骨架 |
| Redis | RESP 二进制协议、事件循环、持久化与复制 | 生产缓存与共享状态的默认选择 |
| Memcached | 同为内存 KV 缓存，多线程模型 | 只需简单键值缓存、无需持久化时 |

## Quick Start

前置条件：只需安装 Go，无任何第三方依赖。

```bash
cd patterns/17_mini_redis
go run mini_redis.go
```

程序内置 demo：随机端口起服务器，逐条发送命令打印响应。预期输出（节选，端口每次运行不同）：

```text
Mini Redis server listening on [::]:56889
=== Mini Redis Demo ===

> SET name Alice                 -> OK
> GET name                       -> "Alice"
> DEL lang                       -> (integer) 1
> GET lang                       -> (nil)
> EXPIRE session 10              -> (integer) 1
> TTL session                    -> (integer) 9
> TTL name                       -> (integer) -1
> TTL notexist                   -> (integer) -2
> TTL session                    -> (integer) 7      # 等 2 秒后再查
> PING                           -> PONG
> SET                            -> ERR wrong number of arguments for 'SET' command
> UNKNOWN_CMD                    -> ERR unknown command 'UNKNOWN_CMD'
```

注意三个细节：

- TTL 10 秒的键 2 秒后剩 7 而不是 8——`EXPIRE` 到首次 `TTL` 之间已消耗 1 秒；
- 无过期时间的键返回 `-1`、不存在的键返回 `-2`，与真 Redis 语义一致；
- 错误响应也模仿了 Redis 的报错文案。

## How It Works

按一条命令的生命周期走：`accept` 交 goroutine → 行解析 → `switch` 分派 → 锁下操作 → 格式化写回。

### 连接模型：goroutine per connection

```go
for {
    conn, err := listener.Accept()
    if err != nil { continue }
    go handleConn(conn, store)  // 每个连接一个协程
}
```

`Accept` 阻塞等新连接，来一个就 `go` 出一个 goroutine。goroutine 初始栈仅 2KB，万级并发连接（C10K 问题，即上万客户端同时接入）的调度成本远低于线程模型；任何一条连接阻塞在读上，都不影响其他连接。

### RESP-like 协议解析

真 Redis 用二进制化的 RESP 协议（`+OK\r\n`、`$3\r\nfoo\r\n`、`*2\r\n…` 五种类型，支持嵌套）。本项目简化为内联文本协议：`SET name Alice\r\n` 进、`OK\r\n` 出。解析三步：

```go
line, _ := reader.ReadString('\n')     // 1. 读一行（bufio 缓冲）
line = strings.TrimRight(line, "\r\n") // 2. 去行尾
parts := strings.Fields(line)          // 3. 空格分词 → switch 分派
```

`handleConn` 是一个 `for` 循环：解析出 `parts` 后交给 `executeCommand`，把响应 `fmt.Fprintf(conn, "%s\r\n", resp)` 写回，然后继续读下一条命令——一条连接可以复用多次请求。

demo 逐行打印的 `> 命令 -> 响应` 就是这个循环的真实轨迹。

### 命令分派与 KVStore

`executeCommand` 先 `strings.ToUpper` 归一化命令名，`switch` 分派到 `KVStore` 的方法，并做参数个数校验（`SET` 必须 3 段、`GET` 必须 2 段）。

你在输出里看到的 `ERR wrong number of arguments for 'SET' command`，就来自这个校验。`KVStore` 用两个 map 平行存储：

```go
type KVStore struct {
    mu     sync.RWMutex
    data   map[string]string    // 键值数据
    expiry map[string]time.Time // 键 → 过期时刻
    stopCh chan struct{}        // 通知清理协程退出
}
```

### TTL 过期：惰性 + 定期双层

TTL 不是递减计数器，而是存"过期时刻"，剩余时间用 `time.Until(exp)` 现算——输出里 `TTL session` 从 9 变 7，就是两次现算的结果。过期靠两层机制兜底：

- **惰性检查**：`Get`/`TTL` 读到键时先查 `isExpired`，过期即视为不存在（`TTL` 对已过期键返回 `-2`）；
- **定期清理**：后台 goroutine 每 1 秒 `removeExpired()` 全量扫描 `expiry` map，加写锁删除过期键，通过 `stopCh` 优雅退出。

`SET` 会 `delete(s.expiry, key)`：重设一个键就重置它的 TTL，与 Redis 语义一致。

### 锁策略速查

| 字段/操作 | 锁级别 | 为什么 |
|---|---|---|
| `Get` / `TTL` | `RLock` | 只读，多个读可并发 |
| `Set` / `Del` / `Expire` | `Lock` | 写 map 必须独占 |
| `removeExpired` | `Lock` | 遍历中 delete，写锁防并发遍历数据竞争 |
| `executeCommand` 存在性回查 | `RLock` | GET 返回空串时区分"值为空"与"键不存在" |

你在输出里看到的 `GET lang -> (nil)`，就靠表里最后一行的存在性回查区分出"键不存在"。

## Pitfalls & Q&A

坑清单（每条：现象 + 原因 + 解法）：

- **遍历 map 时 delete 必须持写锁**：现象是并发下偶发数据竞争告警。原因：Go 允许遍历中 delete 当前键，但并发的其他写者会让遍历不安全。解法：`removeExpired` 全程持 `Lock`。
- **`SET` 记得清 expiry**：现象是重写的键带着旧 TTL 意外消失。原因：只写 `data` 不删 `expiry`。解法：`SET` 时执行 `delete(s.expiry, key)`。
- **`(nil)` 判定要二次回查**：现象是值为空串与键不存在无法区分。原因：`Get` 返回 `""` 有两种可能（值为空串 / 键不存在或已过期）。解法：像 demo 一样再加一次 `RLock` 查存在性。
- **`TTL` 三态返回**：现象是三种取值混为一谈。原因：语义没有对齐 Redis。解法：照抄 Redis 语义客户端才有一致行为——`-2` 不存在或已过期、`-1` 存在但无过期时间、其余为剩余秒数。
- **demo 的随机端口窗口**：现象是极小概率启动失败。原因：demo 先 `net.Listen(":0")` 拿随机端口再 `Close` 让 Start 重监听，存在端口被抢窗口。解法：生产代码应把 listener 直接传给服务器。
- **连接 fd 泄漏**：现象是长时间运行后文件描述符（fd——进程打开文件的句柄，数量有上限）耗尽。原因：`handleConn` 忘了 `defer conn.Close()`。解法：进入函数立即 `defer conn.Close()`。
- **`Accept` 出错整个服务器下线**：现象是偶发网络错误导致服务器退出。原因：`Accept` 出错时直接 `return`。解法：记录错误后 `continue`。

**Q：真正的 RESP 协议长什么样？你的简化版差在哪？**
A：RESP 五种类型——

- Simple String（`+OK\r\n`）
- Error（`-ERR …\r\n`）
- Integer（`:1\r\n`）
- Bulk String（`$3\r\nfoo\r\n`）
- Array（`*2\r\n…`）

类型前缀字节 + 长度声明，支持嵌套，客户端还能一次 pipeline 发多条。

本项目的内联文本协议靠空格分词，无法表达带空格的 value 和二进制安全的数据，但解析模型（读一行 → 分词 → 分派 → 回写）与真实现同构。

**Q：C10K 问题，为什么 Go 一个连接一个 goroutine 就行？**
A：goroutine 初始 2KB 栈、按需增长，由 runtime 调度而非操作系统线程；阻塞在 `ReadString` 上只是挂起当前 goroutine，线程继续跑别的连接。

一万个连接 ≈ 一万个 2KB 栈 + 少量线程，而线程模型一万个 1MB 栈直接爆内存。

对比方案是 epoll 事件循环（epoll 是 Linux 提供的网络事件通知机制，让单个线程同时监听上万连接的可读/可写事件；Redis 的单线程模型正建立在其上）。

**Q：多个连接同时读写 store，怎么保证并发安全？为什么用 RWMutex 不用 Mutex/sync.Map？**
A：所有命令最终都在 `KVStore.mu` 保护下操作两个 map，读命令 `RLock`、写命令 `Lock`，读多写少场景下 RLock 可并行、吞吐更高。

`sync.Map` 适合 key 稳定、读多写少的简单场景，但本项目要遍历清理过期键、做"读+删"复合操作，`map + RWMutex` 更灵活可控；真正的高并发 KV 会再按 key 哈希分 shard 降低锁竞争。

**Q：如果要加持久化/集群怎么扩展？**
A：持久化两条路——RDB 式快照（加锁后把 `data`/`expiry` 序列化到文件）或 AOF 式追加日志（把每条写命令按协议格式落盘、重启重放）。

集群先做一致性哈希分片（可参考 [Project 13](../13_consistent_hash/)），再做主从复制：主节点执行写命令后把命令流传播给从节点重放。
