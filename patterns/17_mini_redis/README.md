# 17 · Mini Redis：用 ~250 行 Go 手写一个 KV 缓存服务器

> Redis 是后端绕不开的话题，理解它最好的方式就是自己写一个。本实验用纯标准库实现了一个基于 TCP 的 Mini Redis：`accept` 后每连接一个 goroutine，行式 RESP-like 文本协议解析，`switch` 命令分派，`map + sync.RWMutex` 的锁保护存储，外加 TTL 过期清理。一条命令从 socket 进到 socket 出的完整数据流，就是一台缓存服务器的全部骨架。

## 1. 为什么需要它

写网络服务时最常见的三个考点——连接模型、协议解析、并发安全——在 Redis 这类 KV 服务器里同时出现：成千上万个客户端连接怎么扛？字节流怎么变成命令？多个 goroutine 同时读写一个 map 怎么不出事？

本实验用 `net` + `bufio` + `sync` 三个标准库包回答这三个问题，支持 `SET / GET / DEL / EXPIRE / TTL / PING` 六个命令，并实现了惰性检查 + 每秒全量扫描的双层过期策略。代码只有一个 `mini_redis.go`，每一行都经得起追问："为什么这么写"。

## 2. 总览：核心机制一图看懂

![Mini Redis 命令数据流：accept → goroutine → 解析 → 分派 → 加锁存储 → 回写](images/miniredis.svg)

> 🌐 **交互版**：[在线打开（GitHub Pages）](https://<user>.github.io/<repo>/patterns/17_mini_redis/images/miniredis.html)
> （或本地打开 [`images/miniredis.html`](images/miniredis.html)）。

**心智模型一句话**：每条连接是一个独立 goroutine 跑 `读取 → 解析 → 分派 → 执行 → 回写` 的循环，所有 goroutine 汇聚到唯一一把 `RWMutex` 保护的 `KVStore` 上——连接层并发、存储层串行化。

## 3. 快速开始

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

注意三个细节：TTL 10 秒的键 2 秒后剩 7 而不是 8（`EXPIRE` 到首次 `TTL` 之间已消耗 1 秒）；无过期时间的键返回 `-1`、不存在的键返回 `-2`，与真 Redis 语义一致；错误响应也模仿了 Redis 的报错文案。

## 4. 核心概念

### 4.1 连接模型：goroutine per connection

```go
for {
    conn, err := listener.Accept()
    if err != nil { continue }
    go handleConn(conn, store)  // 每个连接一个协程
}
```

`Accept` 阻塞等新连接，来一个就 `go` 出一个 goroutine。goroutine 初始栈仅 2KB，万元级并发连接（C10K）的调度成本远低于线程模型；任何一条连接阻塞在读上，都不影响其他连接。

### 4.2 RESP-like 协议解析

真 Redis 用二进制化的 RESP 协议（`+OK\r\n`、`$3\r\nfoo\r\n`、`*2\r\n…` 五种类型，支持嵌套）。本项目简化为内联文本协议：`SET name Alice\r\n` 进、`OK\r\n` 出。解析三步：

```go
line, _ := reader.ReadString('\n')     // 1. 读一行（bufio 缓冲）
line = strings.TrimRight(line, "\r\n") // 2. 去行尾
parts := strings.Fields(line)          // 3. 空格分词 → switch 分派
```

`handleConn` 是一个 `for` 循环：解析出 `parts` 后交给 `executeCommand`，把响应 `fmt.Fprintf(conn, "%s\r\n", resp)` 写回，然后继续读下一条命令——一条连接可以复用多次请求。

### 4.3 命令分派与 KVStore

`executeCommand` 先 `strings.ToUpper` 归一化命令名，`switch` 分派到 `KVStore` 的方法，并做参数个数校验（`SET` 必须 3 段、`GET` 必须 2 段）。`KVStore` 用两个 map 平行存储：

```go
type KVStore struct {
    mu     sync.RWMutex
    data   map[string]string    // 键值数据
    expiry map[string]time.Time // 键 → 过期时刻
    stopCh chan struct{}        // 通知清理协程退出
}
```

### 4.4 TTL 过期：惰性 + 定期双层

TTL 不是递减计数器，而是存"过期时刻"，剩余时间用 `time.Until(exp)` 现算。过期靠两层机制兜底：

- **惰性检查**：`Get`/`TTL` 读到键时先查 `isExpired`，过期即视为不存在（`TTL` 对已过期键返回 `-2`）；
- **定期清理**：后台 goroutine 每 1 秒 `removeExpired()` 全量扫描 `expiry` map，加写锁删除过期键，通过 `stopCh` 优雅退出。

`SET` 会 `delete(s.expiry, key)`：重设一个键就重置它的 TTL，与 Redis 语义一致。

## 5. 关键字段与坑清单

| 字段/操作 | 锁级别 | 为什么 |
|---|---|---|
| `Get` / `TTL` | `RLock` | 只读，多个读可并发 |
| `Set` / `Del` / `Expire` | `Lock` | 写 map 必须独占 |
| `removeExpired` | `Lock` | 遍历中 delete，写锁防并发遍历数据竞争 |
| `executeCommand` 存在性回查 | `RLock` | GET 返回空串时区分"值为空"与"键不存在" |

坑清单：

- **遍历 map 时 delete 必须持写锁**：Go 允许遍历中 delete 当前键，但并发的其他写者会让遍历不安全，`removeExpired` 全程持 `Lock`
- **`SET` 记得清 expiry**：只写 `data` 不删 `expiry`，键会带着旧 TTL 意外消失
- **`(nil)` 判定要二次回查**：`Get` 返回 `""` 有两种可能（值为空串 / 键不存在或已过期），demo 里靠再加一次 `RLock` 查存在性来区分
- **`TTL` 三态返回**：`-2` 不存在或已过期、`-1` 存在但无过期时间、其余为剩余秒数——照抄 Redis 语义才不会被追问
- **demo 先 `net.Listen(":0")` 拿随机端口再 `Close` 让 Start 重监听**，存在极小的端口被抢窗口，生产代码应把 listener 直接传给服务器

## 6. 文件结构

```text
patterns/17_mini_redis/
├── README.md                      # 本文件：原理 + 运行指引 + 深入要点
├── mini_redis.go                  # 全部实现：KVStore → 命令 → 协议解析 → 服务器 → demo
└── images/
    ├── miniredis.dataflow.json    # 图源：命令数据流五阶段管线（Typed JSON IR）
    ├── miniredis.html             # 交互版：可缩放/高亮连线的自包含页面
    └── miniredis.svg              # README 内嵌双主题矢量图
```

## 7. 深入要点

**Q：真正的 RESP 协议长什么样？你的简化版差在哪？**
A：RESP 五种类型——Simple String（`+OK\r\n`）、Error（`-ERR …\r\n`）、Integer（`:1\r\n`）、Bulk String（`$3\r\nfoo\r\n`）、Array（`*2\r\n…`），类型前缀字节 + 长度声明，支持嵌套，客户端还能一次 pipeline 发多条。本项目的内联文本协议靠空格分词，无法表达带空格的 value 和二进制安全的数据，但解析模型（读一行 → 分词 → 分派 → 回写）与真实现同构。

**Q：C10K 问题，为什么 Go 一个连接一个 goroutine 就行？**
A：goroutine 初始 2KB 栈、按需增长，由 runtime 调度而非操作系统线程；阻塞在 `ReadString` 上只是挂起当前 goroutine，线程继续跑别的连接。一万个连接 ≈ 一万个 2KB 栈 + 少量线程，而线程模型一万个 1MB 栈直接爆内存。对比方案是 epoll 事件循环（如 Redis 本身单线程 IO 多路复用）。

**Q：多个连接同时读写 store，怎么保证并发安全？为什么用 RWMutex 不用 Mutex/sync.Map？**
A：所有命令最终都在 `KVStore.mu` 保护下操作两个 map，读命令 `RLock`、写命令 `Lock`，读多写少场景下 RLock 可并行、吞吐更高。`sync.Map` 适合 key 稳定、读多写少的简单场景，但本项目要遍历清理过期键、做"读+删"复合操作，`map + RWMutex` 更灵活可控；真正的高并发 KV 会再按 key 哈希分 shard 降低锁竞争。

**Q：手写这个服务器最容易出错的点是什么？**
A：① 遍历 `expiry` 删除过期键时忘了全程持写锁，与写命令并发会出数据竞争；② `SET` 覆盖键后忘记 `delete(s.expiry, key)`，键带着旧 TTL "复活后暴毙"；③ `handleConn` 忘了 `defer conn.Close()` 导致 fd 泄漏；④ `Accept` 出错时若直接 `return` 整个服务器下线，应记录后 `continue`。

**Q：如果要加持久化/集群怎么扩展？**
A：持久化两条路——RDB 式快照（加锁后把 `data`/`expiry` 序列化到文件）或 AOF 式追加日志（把每条写命令按协议格式落盘、重启重放）。集群先做一致性哈希分片（可参考 [Project 13](../13_consistent_hash/)），再做主从复制：主节点执行写命令后把命令流传播给从节点重放。

## 8. 总结

一条命令的生命周期——`accept` 交 goroutine、行解析成 `parts`、`switch` 分派、锁下操作两个 map、格式化写回 socket——就是 Redis/Memcached 这类 KV 服务器的最小完备骨架。剩下的工程化（RESP 二进制协议、分片锁、抽样过期、持久化）都是在这条主路径上做加法。

下一篇 [18_rate_limiter](../18_rate_limiter/) 看滑动窗口限流器：时间戳日志怎么把"每分钟最多 N 次"变成 O(1) 判定。
