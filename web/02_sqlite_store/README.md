# 02 · SQLite 持久层：手写迁移器与 WAL 并发

> marklet 的数据落盘：纯 Go 驱动 modernc.org/sqlite（无 CGO，即不依赖 C 工具链）、
> 40 行的 schema_migrations 版本表迁移器（幂等：重放已应用的版本零执行）、
> 参数化 CRUD、WAL 并发（50 goroutine × 20 次混合读写 1000 次零 SQLITE_BUSY）。

## Background

实验 01 的路由骨架还撑得起服务，但数据没有去处——放进进程内存的 map，进程一重启就失忆；
临时凑合的办法是每次启动直接 `DROP TABLE` 重建，反正数据也是测试数据。

真实服务不能这样。更麻烦的是表结构会变：给 bookmarks 加一列，所有环境里的库文件都要跟着改，
手工改迟早漂移——"我机器上的库和你机器上的库结构不一样"这类问题没有工具兜不住。
而一旦有并发，SQLite 默认配置下的写锁会让请求成片报 SQLITE_BUSY（数据库忙，锁冲突错误）。

两件事各有标准解法。表结构演进交给迁移器（migration，按版本号顺序执行结构变更、
把执行记录写进版本表的小工具）；并发交给 WAL 模式（Write-Ahead Logging，预写日志，
写操作先记日志、读操作不再被写阻塞）加 busy_timeout（撞锁时排队等待而非立即报错）。

## What

持久层是"把领域对象（这里是 Bookmark）落盘、并提供增查改删方法的那一层代码"。
本篇包含两块：迁移器（负责表结构按版本演进）与 Store（负责书签数据的 CRUD，即增查改删）。

可以把迁移器想成盖楼：每一版迁移是在既有楼层上加建一层，版本表就是施工日志，记录盖到了几层，
停工再开工从日志的下一层继续。但和盖楼不同的是，"加建"必须先查日志再动工——同一层盖两次
在数据库里是直接报错，幂等（同一操作重复执行结果不变）靠"先查版本表、未应用才执行"实现。

三个 PRAGMA（SQLite 的运行时开关语句）决定本篇的并发行为：

| PRAGMA | 作用 | 默认值 |
|---|---|---|
| `journal_mode(WAL)` | 写先落 `-wal` 日志文件，读不被写阻塞 | `delete`（回滚日志，写独占） |
| `busy_timeout(5000)` | 撞锁时排队最多 5 秒 | 0（立即报错） |
| `foreign_keys(1)` | 强制检查外键约束 | 0（不检查） |

## When to Use

SQLite 适合"单机、单写者、数据量 GB 级以内"的持久化，marklet 正是这个画像。

- **服务启动时自动迁移**：进程起来先跑 `Migrate()`，所有环境结构自动对齐
- **零运维部署**：数据库就是一个文件，随容器/二进制一起备份即可
- **本地开发与测试**：每个测试一个临时库文件，互不干扰

何时不用：多机共享写入（SQLite 是单写者，跨机协调不是它的任务）、高并发重写负载。
这正是实验 05 会把存储层抽成接口、换 PostgreSQL 实现的原因——接口已经留好了。

| 存储 | 部署成本 | 并发写 | 适用 |
|---|---|---|---|
| 内存 map | 零 | 有锁即可 | 原型/缓存 |
| SQLite（本篇） | 一个文件 | 单写者 + WAL 读并发 | 单机服务 |
| PostgreSQL（实验 05） | 一个进程 | 多写者 | 生产多实例 |

## Quick Start

前置条件：Go ≥ 1.22（go.mod 声明 go 1.26），无需数据库软件；`go run .` 纯 Go 即可，
`go test -race` 需 C 编译器（macOS 自带，缺失时报 cgo 错误）。运行：

```bash
cd web/02_sqlite_store
go run .
go test -race -count=3 .
```

真实输出（`go run .`，数据库文件落在临时目录、退出即清理）：

```text
== 第 1 步：开库——journal_mode 变为 wal ==
  PRAGMA journal_mode → wal（默认是 delete，WAL 才能写不阻塞读）
== 第 2 步：迁移连跑两次——第二次零执行 ==
  第一次迁移：执行 2 个版本 [001_create_bookmarks 002_index_url]
  第二次迁移：执行 0 个版本（已应用的直接跳过——这就是幂等）
== 第 3 步：CRUD 一轮 ==
  INSERT → id=1，GET 回读：context 包公式（https://go.dev/blog/context?it's= quoted）
  LIST → 1 条
  DELETE → 已删除=true，再 GET → err=bookmark not found
== 第 4 步：WAL 并发——50 goroutine × 20 次混合读写 ==
  完成 1000 次操作：错误 0 个，其中 SQLITE_BUSY 0 个，耗时 1855ms
  ⇒ busy_timeout 让写锁竞争变成「排队等」而不是「立刻炸」，WAL 让读写互不阻塞。
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁——残留 goroutine 即失败）：

```text
ok  	marklet/02_sqlite_store	5.372s
```

诚实预期：第 4 步耗时随机器波动（1~4s 都正常），数值本身不重要，重要的是错误数为 0；
modernc 纯 Go 驱动比 C 驱动（mattn/go-sqlite3）慢数倍，换吞吐是实验 20 的事。

## How It Works

### 版本表迁移器：幂等从哪来

迁移器的全部状态就是一张 `schema_migrations` 表（版本号、名字、执行时间）。
每次迁移先查表：已应用的版本直接跳过，未应用的按序执行——幂等不需要额外状态。

每个版本在一个事务（transaction，一组要么全成功要么全回滚的操作）里同时执行
DDL（Data Definition Language，`CREATE TABLE` 这类改表结构的语句）和版本记录写入。
SQLite 支持事务性 DDL，建表失败会整体回滚，不会留下"表建了一半、版本却没记录"的中间态。

### PRAGMA 三连：WAL 与 busy_timeout

`journal_mode(WAL)` 把写流程从"改主库文件"变成"先追加写 `-wal` 日志"，读者继续读主文件，
读写从此互不阻塞。代价是库文件旁边多出 `-wal`/`-shm` 两个伴生文件，由 SQLite 自动维护。

`busy_timeout(5000)` 解决写与写之间仍存在的单写者竞争：第二个写者撞锁时排队等锁（最多 5 秒），
而不是立刻把 `SQLITE_BUSY` 抛给业务层。第 4 步的 1000 次混合读写零错误就是这两项的合成结果。

三个 PRAGMA 都写在 DSN（Data Source Name，连接字符串）里——连接池里每条新连接都会自动执行
它们，不存在"某条连接忘了开 WAL"的配置漂移。

### 预编译语句与参数化：注入防线的正确位置

CRUD 一律用 `?` 占位符，值经驱动参数化传入：单引号、分号都只是普通字符，
SQL 注入（把输入拼进 SQL 文本改变语句语义的攻击）从机制上不存在。拼接 SQL 字符串
再手工转义是错误路线，转义表永远追不完输入的花样。

`Prepare` 把 SQL 预编译成可复用的语句，热路径省去重复解析。一个本项目实录的顺序约束：
`Prepare` 必须在 `Migrate` 之后——表还不存在时预编译直接报 `no such table`，
所以本篇把预编译放在迁移完成后的收尾步骤里。

## Pitfalls & Q&A

- **先 Prepare 后 Migrate**：现象是开库即报 `no such table: bookmarks`（本项目实录）。
  原因：预编译会校验表存在。解法：预编译挪到迁移完成之后
- **用 `:memory:` 库测 WAL**：现象是 `journal_mode` 查回来还是 `memory`，并发测试形同虚设。
  原因：WAL 是文件日志格式，内存库没有文件可写。解法：测试用 `t.TempDir()` 里的临时文件库
- **`defer tx.Rollback()` 写在循环里**：现象是回滚堆到函数返回才执行，已提交的还会报
  `ErrTxDone` 噪音。解法：循环体内出错路径显式调用 `tx.Rollback()`（本篇实现）
- **忘开 `foreign_keys(1)`**：现象是引用了不存在外键的行照插不误。原因：SQLite 出于兼容
  默认关闭外键检查。解法：DSN 里显式打开

**Q：为什么不用 goose、golang-migrate 这类现成迁移工具？**
A：marklet 只有一张表、两条迁移，一张版本表刚好讲清迁移器的全部机制——查版本、跳过、
事务内执行、记版本。第三方工具解决的是多数据库方言、回滚脚本、跨环境编排这类规模问题，
机制与手写版完全同源。

**Q：WAL 的代价是什么？**
A：三个：库目录多出 `-wal`/`-shm` 伴生文件；写入达到阈值会触发 checkpoint（把日志合并回
主文件）造成偶发写延迟；跨进程网络文件系统上 WAL 不可靠——它假设所有访问者在同一台机器。

**Q：created_at 为什么用 TEXT 而不是 INTEGER 时间戳？**
A：RFC3339（文本时间戳）的字典序就是时间序，`ORDER BY created_at` 语义正确且人眼可读，
`sqlite3` 命令行里直接能看懂。高频范围查询时再换 INTEGER（Unix 纳秒），那属于存储优化。

**Q：并发插入时 LastInsertId 会不会拿到别人的 id？**
A：不会。id 分配与读取在同一次 `Exec` 内完成，SQLite 在连接内保证 `last_insert_rowid`
指向本连接最近一次插入。AUTOINCREMENT 只增不复用，删最大行也不会让新插入重领旧 id。
