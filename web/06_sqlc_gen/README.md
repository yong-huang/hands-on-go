# 06 · sqlc 代码生成：SQL 即源码，类型即防线

> 同一个 PG、同一个 `Bookmarks` 接口：手写 SQL 与 sqlc 生成码各实现一遍、同一套测试都绿。
> schema 变更时的对照是本篇核心：手写漏改是运行时错误（测试实录），SQL 与 schema 漂移
> 在 `sqlc generate` 当场报错（输出实录），字段改名则直接 Go 编译报错——三层防线逐级提前。

## Background

手写数据访问层的状态：SQL 以字符串散落在 Go 代码里，列名拼错、参数对不上、Scan
类型不匹配——全躲过编译器，第一次运行才炸。更痛的是 schema 变更：给表加一列，
要人肉搜索所有 INSERT/SELECT/Scan，漏掉的那处报的是运行时异常。

sqlc 的思路是把 SQL 当源码：从 schema（表结构声明）与 queries（带名字的 SQL 语句）
生成强类型的 Go 代码——参数是结构体、返回是结构体，类型在生成期就定型。
生成码提交进仓库，业务代码不再手写任何 SQL 字符串。

## What

sqlc 是一个代码生成器：读 `schema/schema.sql`（表结构）与 `queries/*.sql`（命名查询），
产出 `gen/` 目录三个文件（模型、查询实现、连接封装）。生成的 `Queries` 结构体上，
每个命名查询对应一个方法。

可以把 sqlc 想成"SQL 的编译器"：schema 是类型声明，queries 是源代码，gen/ 是目标码，
schema 与 SQL 对不上时"编译"（generate）当场报错。但和编译器不同的是，它只覆盖查询
——不生成迁移、不管理 schema 演进，表结构怎么变仍归实验 02 那套迁移器管。

marklet 的两条实现路线共用一个接口：

| 路线 | 驱动 | SQL 来源 | 漏改的暴露时机 |
|---|---|---|---|
| 手写（实验 05 移植） | database/sql | 代码里的字符串 | 运行时 |
| 生成码（本篇） | pgx 原生 | queries/*.sql | generate / 编译期 |

## When to Use

- **查询集相对固定**的业务服务：增查改删 + 报表查询，sqlc 覆盖率接近 100%
- **schema 活跃演进**的项目：改表后重新 generate，所有受影响调用点被机械找出
- **多方言**共存：同一套查询可配 sqlite 与 postgresql 两个引擎分别生成

何时不用：查询需要运行时动态拼装（多维筛选、搜索引擎类）；团队已深度绑定 ORM
且没有类型安全痛点。

| 方案 | 类型安全 | 动态查询 | 心智负担 |
|---|---|---|---|
| 手写 SQL 字符串 | 无（运行时暴露） | 灵活 | 每处查询自己背 |
| ORM | 弱（反射+tag） | 强 | 隐藏 SQL，调优难 |
| sqlc（本篇） | 强（生成期+编译期） | 弱（预写命名查询） | 一份 schema 一份 queries |

## Quick Start

前置条件：sqlc CLI（`brew install sqlc`）；PG 容器（`../05_pg_migration/pg.sh start`）。
生成码已提交在 `gen/`，重生成是显式动作。运行：

```bash
cd web/06_sqlc_gen
sqlc generate   # schema/queries 有改动时执行；产出 gen/ 三个文件
go run .
go test -race -count=3 .
```

真实输出（`go run .`；共享容器库的 id 每次运行都会递增）：

```text
== 第 1 步：双路并跑——手写 SQL 与 sqlc 生成码，同一接口同一结果 ==
  手写   → id=27 → "Go 官网" → "Go 官网（改）" → 删除
  生成码 → id=28 → "Go 官网" → "Go 官网（改）" → 删除
  ⇒ 结果一致；差异不在运行时，在 schema 变更那一刻由谁兜底。
== 第 2 步：生成码速览——签名即文档，类型即约束 ==
  func (q *Queries) CreateBookmark(ctx context.Context, arg CreateBookmarkParams) (Bookmark, error) {
  type CreateBookmarkParams struct {
  	Url   string
  	Title string
  }
  type Bookmark struct {
  	ID        int64
  	Url       string
  	Title     string
  	CreatedAt time.Time
  }
== 第 3 步：schema 变更时——三层防线的对照 ==
  手写漏改：加一列 NOT NULL 后 INSERT 照旧 → 运行时才被 PG 拒绝（测试 TestSchemaChangeWorkflow 实录）
  生成码①：改列名后 SQL 与 schema 漂移 → sqlc generate 当场报错（README 有实录输出）
  生成码②：列名/类型真变了 → 生成码同步改名，调用点直接 Go 编译报错
```

schema 漂移的 generate 报错实录（把 `title` 改名 `label` 后执行 `sqlc generate`）：

```text
# package db
queries/bookmarks.sql:9:1: column "title" does not exist
queries/bookmarks.sql:12:12: column "title" does not exist
queries/bookmarks.sql:15:1: column "title" does not exist
queries/bookmarks.sql:18:29: column "title" does not exist
```

测试验收（`-race` 连跑 3 轮全绿；PG 容器未启动时自动跳过并说明原因）：

```text
ok  	marklet/06_sqlc_gen	2.276s
```

诚实预期：第 1 步的 id 数字每次运行都不同（共享库序列递增），重要的是两路行为一致；
`TestSchemaChangeWorkflow` 会真实地给共享库加列再删列，属于演示性副作用。

## How It Works

### 工作流：三份文件，一个方向

`sqlc.yaml` 声明引擎、输入输出目录与生成配置。生成方向单向：schema + queries → gen/，
手改 gen/ 下次 generate 就被覆盖。适配层 `sqlcStore` 把生成的 `Queries` 包装成
`Bookmarks` 接口，调用方无感。

### 两路的真实差异在哪里

运行结果完全一致（第 1 步），差异在三处：生成码的参数是结构体，拼错字段名是编译
错误，手写是位置参数、拼错顺序只有运行时知道；生成码跑在 pgx 原生连接上，少一层
`database/sql` 间接；两边的 ErrNoRows 是不同的值，适配层统一翻译成 `ErrNotFound`。

### schema 变更：三层防线逐级提前

手写漏改加列，到运行时才被 PG 拒绝（测试实录）；SQL 与 schema 漂移，`sqlc generate`
当场失败并列出全部出错位置（实录一屏四处）；列真改了名，生成模型同步改名，引用
旧字段的调用点 Go 编译报错。防线越靠前，修复越便宜。

## Pitfalls & Q&A

- **$1/$2 占位符与参数顺序**：现象是运行时报看不懂的 `unable to encode` 编码错误（实录）。
  原因：手写版参数传反，编译器不查占位符语义。解法：生成码的结构体参数从机制上消灭它
- **TIMESTAMPTZ 生成 pgtype.Timestamptz**：现象是生成码类型与手写接口对不上。
  原因：sqlc 默认用 pgtype 表示 PG 时间。解法：yaml 里 override 映射为 `time.Time`
  （本篇配置实录；注意 db_type 要写 `timestamptz` 而非带前缀全名）
- **keyed 结构体字面量缺字段不报错**：现象是新加的 NOT NULL 列被零值静默插入。
  原因：Go 的 keyed literal 允许缺字段。解法：不要指望编译器兜底一切——
  generate 报错 + code review 才是这道防线的本体
- **两份 schema 漂移**：现象是 `schema/schema.sql` 与迁移器建出的表渐渐不一致。
  原因：生成用的 schema 与执行用的迁移 DDL 是两份文件。解法：纪律——改库必改
  schema.sql，作为迁移 PR 的一部分

**Q：生成码里不还是 Query + Scan 那一套吗，省了什么？**
A：省的不是那几行 Scan，是"每次手写都要人肉对齐列名、顺序、类型"的过程。
生成码的 Scan 是机械正确的——它由 schema 推导，schema 变了它跟着变，永不错位。

**Q：sqlcStore 为什么还留着一个 *sql.DB？**
A：只为复用迁移器。生成码只管查询，不管 schema 演进——表结构怎么变、版本怎么记，
仍然是实验 02 那套版本表迁移器的职责。两套机制各守一层，谁也不越界。

**Q：生成的代码提交进仓库，不怕脏吗？**
A：提交是刻意设计：diff 可 review、CI 不装 sqlc 也能编译；"重新生成"是显式动作，
什么改动产生了什么代码一目了然。
