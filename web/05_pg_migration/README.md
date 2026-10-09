# 05 · Docker 化 PostgreSQL：迁移移植与接口换轮子

> marklet 的存储层升级到生产形态：同一 `Bookmarks` 接口换上 PostgreSQL 实现（pgx 驱动），
> 迁移 SQL 移植成 PG 方言（AUTOINCREMENT → BIGSERIAL）。集成测试只写一遍、两个引擎都绿——
> 接口多态让"换数据库"变成换一个构造函数。

## Background

实验 02-04 的数据层一直跑在 SQLite 上：单文件、零部署，开发期很好用。但 SQLite 是单写者
引擎，写入吞吐有天花板，也没有网络访问能力——多实例部署时各写各的文件，数据直接分叉。

生产服务的标准选择是 PostgreSQL（下称 PG）：一个独立的数据库进程，多连接并发写、
客户端通过网络连接。代价是它不再"长在服务进程里"——需要一个单独的运行环境。
容器（把进程及其依赖打包成标准化运行单元的工具）正是为此而生：
`docker run` 一条命令起一个一次性的 PG，端口映射到本机 15432。

换引擎听起来像大手术，但如果数据层藏在一个接口后面，手术就只剩"给接口再写一个实现"。

## What

本篇把存储层抽成 `Bookmarks` 接口（增查改删 + 迁移 + 关闭六个方法），
SQLite 与 PG 各出一个实现，两者都走 `database/sql`（Go 标准库的数据库访问层），
只是驱动不同：SQLite 用 modernc 纯 Go 驱动，PG 用 pgx 的 stdlib 适配。

可以把接口想成电源插座：电器（业务代码与测试）只认插座形状，不关心墙里接的是火电还是水电。
但和插座不同的是，两个"电源"的方言差异真实存在——建表语句、自增主键、取回新 id 的方式
都要按方言各写一份，这些差异被刻意钉死在迁移定义里，不许悄悄趋同。

| 差异点 | SQLite | PostgreSQL |
|---|---|---|
| 自增主键 | `INTEGER PRIMARY KEY AUTOINCREMENT` | `BIGSERIAL PRIMARY KEY` |
| 取回新 id | `res.LastInsertId()` | `INSERT ... RETURNING id` |
| 时间列 | `TEXT`（RFC3339 字符串） | `TIMESTAMPTZ`（原生时间类型） |

## When to Use

- **多实例部署前**：只要服务会横向扩到两个以上实例，就必须把共享状态搬进 PG 这类网络数据库
- **测试矩阵**：一套集成用例跑两种实现，方言回归（写错 PG 语法）在 CI 里当场暴露
- **本地开发**：容器 PG + 本地 SQLite 并存，没网没容器也能跑纯 SQLite 的用例

何时不用：数据只在本机、写入并发低——SQLite 更省事；实验 02-04 的选择此时依然正确。

| 存储 | 并发写 | 部署 | marklet 用在哪 |
|---|---|---|---|
| SQLite | 单写者 | 零部署 | 开发、单机、测试 |
| PostgreSQL | 多写者 | 一个容器 | 生产、多实例 |

## Quick Start

前置条件：Docker 运行中、本地有 `postgres:15-alpine` 镜像；Go ≥ 1.22。
PG 容器用 `pg.sh` 管理（端口 15432，避开常用端口）。运行：

```bash
cd web/05_pg_migration
./pg.sh start
go run .
go test -race -count=3 .
```

真实输出（`go run .`，SQLite 用临时文件，PG 用容器里的共享库）：

```text
== 第 1 步：双实现并跑——同一串 CRUD，同一套接口 ==
  sqlite   → INSERT id=1 → GET "Go 官网" → UPDATE → DELETE，再查 ErrNotFound
  postgres → INSERT id=1 → GET "Go 官网" → UPDATE → DELETE，再查 ErrNotFound
  ⇒ 业务代码只认 Bookmarks 接口，换引擎零改动——这就是接口多态的实证。
== 第 2 步：方言差异——同一张表，两种建法 ==
  sqlite 主键列: id         INTEGER PRIMARY KEY AUTOINCREMENT,
  pg     主键列: id         BIGSERIAL PRIMARY KEY,
  ⇒ AUTOINCREMENT vs BIGSERIAL；回 id 一个走 LastInsertId，一个走 RETURNING。
== 第 3 步：PG 迁移幂等——清库重演一遍完整迁移 ==
  清库后首次：应用 2 个版本 [001_create_bookmarks 002_index_url]
  紧接着重跑：应用 0 个版本——幂等在 PG 上同样成立
```

测试验收（`-race` 连跑 3 轮全绿；PG 用例在容器未启动时自动跳过并说明原因）：

```text
ok  	marklet/05_pg_migration	2.721s
```

诚实预期：PG 容器必须先 `./pg.sh start`，否则第 1 步直接报错（带启动提示）；
PG 是共享容器库，两次运行间数据会残留，测试用幂等保证、演示用清库重演来对付这一点。

## How It Works

### 接口：测试只写一遍的秘密

`Bookmarks` 接口的六个方法就是调用方的全部世界。集成测试 `exerciseStore` 的参数类型是
接口，SQLite 和 PG 各喂一次——用例里的每条断言对两个引擎各跑一遍。方言若悄悄坏掉
（比如某次重构把 PG 的 `$1` 写成 `?`），对应的实现当场变红。

### 方言差异被钉在迁移定义里

每个迁移版本带两列 SQL（`sqlite` 与 `pgsql`），运行时按实现的方言取用。差异集中在
三处：自增主键的写法、时间列类型、以及取回新 id 的方式——SQLite 用 `LastInsertId`，
PG 的标准做法是 `RETURNING` 子句，同一条 INSERT 把新 id 直接带回来。

测试 `TestDialectSQLReallyDiffers` 断言两个方言列不许相同——防止后人"统一"掉差异，
让方言 bug 失去暴露的机会。

### 幂等迁移在 PG 上同样成立

迁移器的机制（版本表 + 逐版本事务）与实验 02 完全同源，PG 方言只是换了 DDL 的写法。
共享容器库的特殊之处是"首次迁移"早已发生过——测试不假装库是全新的：幂等断言
（重跑零执行）随时可用；"从零全量应用"的语义由清库重演覆盖（`DROP TABLE` 后再迁移）。

## Pitfalls & Q&A

- **`sql.Open` 不报错却连不上**：现象是开库成功、第一条查询才炸。原因：`sql.Open`
  是惰性的，只构造连接池不建立连接。解法：开库后立即 `db.Ping()`（本篇 OpenPG 实录）
- **共享库上断言"首次全量迁移"**：现象是 `-count=3` 第二轮必挂。原因：容器库跨运行
  残留，第一轮已把版本表写满。解法：幂等断言（重跑零执行）随处可用，"首次"语义
  用清库制造（本篇 `TestPGMigrateFreshAndIdempotent`）
- **占位符写串方言**：现象是 PG 报语法错误。原因：PG 用 `$1, $2`，`?` 是 SQLite 的。
  解法：方言 SQL 分列存放，不做运行时替换
- **忘记 `sslmode=disable`**：现象是本地容器连接报 TLS 错误。原因：pgx 默认要求加密，
  容器没配证书。解法：本地开发 DSN 显式 `sslmode=disable`

**Q：为什么两个实现都走 database/sql，而不是 PG 直接用 pgx 原生 API？**
A：本篇的教学点是接口换实现，`database/sql` 是两者的最大公约数——测试代码、连接池
配置、错误语义完全共享。pgx 原生 API 的增益在 marklet 的查询复杂度下还没兑现；
接口在，随时可换。

**Q：TIMESTAMPTZ 和 TEXT 存时间，测试为什么两边都能过？**
A：接口约定 `Get` 返回 `time.Time`，两个实现各自负责把存储格式还原成时间——
SQLite 解析 RFC3339 字符串，PG 由驱动直接扫描。方言差异被接口吸收在实现内部，
这正是"实现负责翻译"的分工。

**Q：容器里的数据容器删了就没了，怎么算生产形态？**
A：本篇起的是"开发用"容器：数据卷、备份、监控都还没配——那是实验 19（Compose 三件套）
的事。本篇交付的是代码侧的迁移：存储层与业务解耦，随时可指向真正的生产库。

**Q：SQLite 版本会被删掉吗？**
A：不会。它继续承担三件事：单机部署模式、无 Docker 环境的测试回退、以及方言差异的
永久对照物。marklet 的发布形态（实验 19）会同时支持两种存储。
