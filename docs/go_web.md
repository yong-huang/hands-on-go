# 🌐 marklet：Go Web 服务 20 小项目学习清单 · Todo List（完整系统型）

> 通过 20 个小项目（每项目 100~250 行代码）共同生长为一个真实可部署的书签/稍后读服务 **marklet**：SQLite 起步 → PostgreSQL/Redis 上生产形态 → argon2id+JWT 认证 → slog/pprof/OTel 可观测 → Docker Compose 交付
> 本机可跑、零云依赖；SQLite 用纯 Go 驱动，PostgreSQL/Redis 走 Docker（镜像已在本地，arm64 实测）；每项目附 `-race` 可过测试
> 完整系统型：全部项目给共享载体 marklet 贡献模块，每阶段末 ⛓️ 集成点接线，项目 20 🏁 终极串联（CI + 健康检查 + 压测）
> 预计周期：9~10 周（每天 1~1.5 小时）

## 🤖 AI 辅助提示词速查

| 场景 | 提示词 |
|:---|:---|
| **开始一个新项目** | `我要开始 Go Web 项目「[名称]」（marklet 服务的 [模块]）。请给我完整代码约 [行数] 行（go1.26，[依赖]），附 -race 可过测试与验收命令；服务只监听 127.0.0.1:19610~19639。只输出代码。` |
| **排障** | `marklet 的 [接口/迁移] 出现 [现象]，日志：[粘贴]。请分析根因并给修复。` |
| **架构评审** | `marklet 目前 [描述当前分层]。请评审 [缓存策略/认证流程/停机顺序] 的缺陷并给改进。` |

## 📊 总进度

进度：████████████████████ 20/20 (100%)

| 阶段 | 项目数 | 已完成 |
|:---|:---:|:---:|
| 第一阶段：服务地基与 SQLite 持久层 | 4 | 4 |
| 第二阶段：PostgreSQL 迁移与缓存 | 4 | 4 |
| 第三阶段：认证与安全 | 4 | 4 |
| 第四阶段：可观测性与强化 | 4 | 4 |
| 第五阶段：异步任务 | 2 | 2 |
| 第六阶段：交付 | 2 | 2 |
| **合计** | **20** | **20** |

---

## 🗂️ 第一阶段：服务地基与 SQLite 持久层（项目 1-4）

> **目标**：marklet 有了骨架与 SQLite 数据层——路由、配置、迁移、事务全部落盘

### [x] 项目 1：marklet 骨架——路由、配置与优雅退出

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | `cmd/marklet`（入口）、`internal/config`、`internal/httpapi`（路由骨架） |
| **核心知识点** | ServeMux 方法路由、env 配置加载、优雅退出（SIGTERM→Shutdown→超时强杀）、/healthz 与 /readyz 分离 |
| **验收标准** | ① `kill -TERM` 后进程 2 秒内退出且日志打印 drain 完成；② /readyz 在 Shutdown 后立即返回 503 而 /healthz 仍 200；③ `go test -race` 全绿 |
| **前置** | 无（复用 net/12 中间件与 net/16 优雅退出知识） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「marklet 骨架」。请给我完整代码约 180 行：env 配置加载、ServeMux 路由（/healthz 存活、/readyz 就绪、/api/v1 占位）、SIGTERM 优雅退出（server.Shutdown 5s 超时 + readyz 先行摘除），附 -race 测试与验收命令。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 2：SQLite 持久层与手写迁移

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~220 行 |
| **产出模块** | `internal/store/sqlite`（书签表 CRUD）、`internal/store/migrate`（schema_migrations 版本表） |
| **核心知识点** | modernc.org/sqlite 纯 Go 驱动（无 CGO）、手写迁移器（版本表 + 顺序执行）、预编译语句、WAL 模式 |
| **验收标准** | ① 迁移器连跑 2 次幂等（第二次零执行）；② CRUD 集成测试 `go test -race` 全绿；③ WAL 开启后读写并发 1000 次无 SQLITE_BUSY |
| **⚠️ 风险** | modernc.org/sqlite 首次拉取 ⚠️ 开工探活（goproxy.cn）；fallback：mattn/go-sqlite3（需 CGO，macOS 默认可用） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「SQLite 持久层」。请给我完整代码约 220 行：modernc.org/sqlite 驱动 + WAL 模式、schema_migrations 版本表迁移器（幂等）、bookmarks 表 CRUD（参数化查询防注入），附迁移幂等断言与 -race 测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 3：连接池调参与并发实测

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **产出模块** | `internal/store/pool`（池参数配置与观测） |
| **核心知识点** | SetMaxOpenConns/SetMaxIdleConns/SetConnMaxLifetime、池耗尽的等待队列行为、SQLite 单写者约束、池指标导出（DBStats） |
| **验收标准** | ① MaxOpenConns=2 时 50 并发请求实测排队（p99 随并发上升曲线入档）；② DBStats.WaitCount 与请求数对得上；③ 对照实验：池参数默认 vs 调优的吞吐差入档 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「连接池调参」。请给我完整代码约 150 行：SQLite 连接池参数实验（默认 vs MaxOpenConns=2 vs 调优），50 并发压测量化排队延迟，导出 DBStats 指标，附对照表测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 4：⛓️ 事务与隔离级别实测 + CRUD 接线

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **产出模块** | `internal/store/tx`（事务助手）、httpapi 书签 CRUD 全量接线 |
| **核心知识点** | BeginTx/隔离级别（SQLite Serializable vs PG ReadCommitted 对比预研）、脏写/不可重复读最小复现、事务边界与 handler 的关系 |
| **验收标准** | ① 两个 goroutine 并发转账式操作，无事务版丢更新实测、事务版守恒断言；② CRUD 全链路（POST/GET/PUT/DELETE）httptest 集成测试全绿；③ 迁移在服务启动时自动执行 |
| **前置** | 项目 1~3 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「事务与隔离实测」。请给我完整代码约 200 行：转账式丢更新复现（无事务 vs BeginTx）、事务助手（WithTx 包装回滚/提交）、书签 CRUD 接入 httpapi（httptest 全链路测试），附 -race 命令。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 🗂️ 第二阶段：PostgreSQL 迁移与缓存（项目 5-8）

> **目标**：数据层升级到生产形态——PG 真库、sqlc 生成、Redis 缓存、一致性策略实测

### [x] 项目 5：Docker 化 PostgreSQL + 迁移移植

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | `deploy/postgres`（docker run 脚本）、`internal/store/pg`（PG 实现同一 Store 接口）、迁移 SQL 的 PG 方言版 |
| **核心知识点** | lib/pq 或 pgx 驱动选型、Docker 端口映射（15432）、PG 与 SQLite 方言差异（AUTOINCREMENT vs BIGSERIAL）、接口多实现切换 |
| **验收标准** | ① `docker run` 起 pg:15-alpine（本地镜像），健康检查脚本就绪；② 同一套集成测试跑 SQLite 与 PG 两个实现都绿（接口多态的价值实证）；③ 迁移在 PG 上幂等 |
| **⚠️ 风险** | psql 客户端未装——用 `docker exec` 交互；postgres:15-alpine 镜像已在本地 ✅ |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「PostgreSQL 迁移」。请给我完整代码约 180 行：pgx 驱动实现与 SQLite 相同的 Store 接口、PG 方言迁移 SQL、docker run 脚本（postgres:15-alpine，端口 15432，健康检查），附双实现共享集成测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 6：sqlc 代码生成 vs 手写 SQL

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 + 生成代码 |
| **产出模块** | `internal/store/sqlc`（生成）、`sqlc.yaml`、queries/*.sql |
| **核心知识点** | sqlc 工作流（SQL 即源码）、生成码与手写 Store 的接口适配、编译期类型安全 vs 运行时拼接 |
| **验收标准** | ① `sqlc generate` 产出可用代码并通过与手写版相同的集成测试；② 一处 schema 变更实操：手写版漏改的注入点 vs sqlc 编译期报错对照入档 |
| **⚠️ 风险** | sqlc CLI 未装——`brew install sqlc` ⚠️ 开工探活；fallback：手写版继续 + README 记录 sqlc 输出样例 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「sqlc 实践」。请给我 sqlc.yaml、queries/bookmarks.sql 与适配层代码约 150 行：把项目 5 的手写查询改为 sqlc 生成，接口对齐现有 Store，附 schema 变更实验（手写 vs 生成）对照说明。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 7：Redis 缓存层 + singleflight 防击穿

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **产出模块** | `internal/cache/redis`（cache-aside 实现）、GET 热点路径接入 |
| **核心知识点** | go-redis 客户端、cache-aside 模式、TTL 设计、缓存穿透（查不存在）与击穿（热点过期）——singleflight 合并回源（复用 concurrency/09） |
| **验收标准** | ① 命中率统计实测（预热后 ≥90%）；② 1000 并发查同一冷 key，回源次数 =1（singleflight 断言）；③ 穿透防护：不存在的 key 用空值缓存，回源 QPS 实测归零 |
| **⚠️ 风险** | redis:7-alpine 镜像已在本地 ✅；go-redis 模块首次拉取走 goproxy.cn（预期可达） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「Redis 缓存」。请给我完整代码约 200 行：cache-aside 读路径（miss 回源+回填+TTL）、singleflight 合并回源、空值缓存防穿透、命中率计数器，附击穿/穿透两个场景的断言测试（Redis 用 docker redis:7-alpine，端口 16379）。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 8：⛓️ 缓存一致性策略实测

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | 写路径缓存失效策略（先更 DB 后删缓存）、不一致窗口观测工具 |
| **核心知识点** | cache-aside 的写序（先库后删）、延迟双删、不一致窗口的实测测量、读旧数据的概率量化 |
| **验收标准** | ① 并发读写交错实验：读旧数据窗口实测记录（次数/时长）；② 先删后更 vs 先更后删两种策略的坏例复现；③ 最终策略下 10 万次读写交错零脏读断言 |
| **前置** | 项目 4、7 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「缓存一致性」。请给我完整代码约 180 行：并发读写交错实验框架（统计读旧窗口）、先删后更/先更后删两种策略的坏例复现、最终采用先更后删+TTL 兜底，附 10 万次交错零脏读断言。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 🗂️ 第三阶段：认证与安全（项目 9-12）

> **目标**：marklet 有了真实用户——argon2id 密码、Redis 会话、JWT、安全中间件

### [x] 项目 9：密码哈希与 Redis 会话

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~220 行 |
| **产出模块** | `internal/auth/password`（argon2id）、`internal/auth/session`（Redis 会话存储）、注册/登录/登出接口 |
| **核心知识点** | argon2id 参数（内存/时间/并行度）、恒定时间比较、session 固定攻击防御（登录后换 ID）、Redis TTL 即会话过期 |
| **验收标准** | ① 同一密码两次哈希结果不同（盐生效）且验证通过；② 登录后会话 ID 变化（固定攻击防御断言）；③ Redis 重启后会话仍有效（持久化配置）或按设计失效；④ 弱密码被拒绝的用例表 |
| **⚠️ 风险** | golang.org/x/crypto 首次拉取 ⚠️ 开工探活（goproxy.cn，预期可达） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「密码与会话」。请给我完整代码约 220 行：argon2id 哈希（参数可配）、Redis 会话存储（TTL、登录后换 ID）、注册/登录/登出/当前用户四个接口，附盐随机性、会话固定、弱密码三个断言测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 10：JWT 签发、验证与陷阱复现

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | `internal/auth/jwt`（HS256 签发/验证 + 过期/刷新） |
| **核心知识点** | HS256 结构（header.payload.signature）、exp/nbf/iss 声明、alg=none 攻击与弱密钥爆破的复现式讲解、JWT vs Session 的取舍 |
| **验收标准** | ① 过期 token 被拒（exp 断言）；② 篡改 payload 后签名校验失败断言；③ alg=none token 被拒（库默认安全实证）；④ 刷新流程测试通过 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「JWT」。请给我完整代码约 180 行：golang-jwt HS256 签发/验证（exp/iss 声明）、刷新端点、alg=none 与篡改 payload 两个陷阱的复现测试（都应被拒），附 -race 命令。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 11：安全中间件全家桶

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | `internal/httpapi/middleware`（auth/CSRF/安全头/请求体限制） |
| **核心知识点** | Bearer 中间件、CSRF 双提交 cookie、安全响应头（CSP/X-Frame-Options/HSTS）、http.MaxBytesReader 请求体上限、Constant-Time 比较 |
| **验收标准** | ① 无 token 访问受保护路由 401、伪造 token 401；② 缺 CSRF token 的写请求 403；③ 安全头逐项断言（响应头测试）；④ 2MB 请求体被 413 拒绝 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「安全中间件」。请给我完整代码约 180 行：Bearer 认证中间件、CSRF 双提交、安全头中间件（CSP/XFO/HSTS/XCTO）、MaxBytesReader，附 401/403/安全头/413 四组断言测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 12：⛓️ 全站认证打通

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **产出模块** | 路由分组重构（公开/受保护两组）、书签归属用户（owner_id 外键）、越权访问测试 |
| **核心知识点** | 路由分组与中间件作用域、数据按用户隔离（WHERE owner_id）、水平越权（IDOR）防御测试 |
| **验收标准** | ① 用户 A 无法 GET/PUT/DELETE 用户 B 的书签（IDOR 断言 404 而非 403——不泄露存在性）；② 未登录全量 401；③ 全量集成测试（注册→登录→建→查→改→删→登出）跑通 |
| **前置** | 项目 9~11 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「认证打通」。请给我完整代码约 150 行：路由分组（公开/受保护）、bookmarks 加 owner_id 隔离、IDOR 防御（跨用户访问 404），附注册登录建查改删全流程 httptest 测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 🗂️ 第四阶段：可观测性与强化（项目 13-16）

> **目标**：marklet 从"能跑"到"看得见"——日志、指标、追踪、限流四位一体

### [x] 项目 13：slog 结构化日志 + 请求 ID 贯通

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **产出模块** | `internal/obs/logging`（slog 封装）、请求 ID 中间件 |
| **核心知识点** | slog JSON handler 与字段约定、X-Request-Id 生成/透传/注入 context、日志级别与环境切换、敏感字段脱敏（密码/token 永不落日志） |
| **验收标准** | ① 每条日志都含 request_id 且跨中间件/handler 一致（断言）；② 客户端传入 X-Request-Id 时透传（便于关联）；③ 日志中 grep 不到密码与 token（脱敏断言） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「结构化日志」。请给我完整代码约 150 行：slog JSON 封装、请求 ID 中间件（生成/透传/context 注入）、敏感字段脱敏助手，附一致性/透传/脱敏三组断言测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 14：/metrics 与 pprof 端点

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **产出模块** | `internal/obs/metrics`（Prometheus 文本格式手写）、pprof 挂载与隔离 |
| **核心知识点** | Prometheus 文本格式（counter/gauge/histogram）、RED 指标（Rate/Errors/Duration）、pprof 只挂内部端口、直方图桶设计 |
| **验收标准** | ① /metrics 输出可被 `promtool check metrics`（或手工解析）接受；② 请求延迟直方图分位数与实测一致（对照组）；③ pprof 在独立端口（19639），主端口不可达 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「指标端点」。请给我完整代码约 150 行：手写 Prometheus 文本格式（RED 三指标 + histogram）、pprof 挂独立内部端口、附格式合法性与分位数断言测试。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 15：OpenTelemetry trace 贯通

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行 |
| **产出模块** | `internal/obs/tracing`（tracer 初始化、HTTP/DB span）、trace 输出到 stdout（无 collector 依赖） |
| **核心知识点** | trace/span/context 传播、span 属性与事件、采样策略、W3C traceparent 透传 |
| **验收标准** | ① 一次请求产生 父 span(HTTP) + 子 span(DB) 层级关系断言；② 外部传入 traceparent 时延续同一 trace（透传断言）；③ 采样率 10% 实测（100 请求 ~10 条 trace） |
| **⚠️ 风险** | OTel SDK 首次拉取 ⚠️ 开工探活（goproxy.cn，依赖较多预期可达）；fallback：slog 打 key-value trace 字段 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「OTel 追踪」。请给我完整代码约 200 行：OTel 初始化（stdout exporter）、HTTP 中间件建父 span、DB 层建子 span、W3C traceparent 透传、可配采样率，附 span 层级/透传/采样三组断言。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 16：⛓️ 限流 + 全链路观测验收

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~180 行 |
| **产出模块** | `internal/httpapi/ratelimit`（Redis 滑动窗口，复用 patterns/18 思想）、观测四件套集成验收 |
| **核心知识点** | Redis 滑动窗口限流（INCR+EXPIRE vs ZSET）、按用户维度限流、429 语义与 Retry-After、观测四件套（log/metric/trace/limit）协同 |
| **验收标准** | ① 100 请求/10s 限流下，第 101 起实测 429 且带 Retry-After；② 超限请求在日志/指标/trace 中均可定位（关联断言）；③ 全链路：一条请求的 request_id 在日志、metrics label、trace 三处一致 |
| **前置** | 项目 13~15、patterns/18（滑动窗口思想） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「限流与观测验收」。请给我完整代码约 180 行：Redis 滑动窗口限流中间件（按用户、429+Retry-After）、观测四件套集成断言（request_id 三处一致），附压测脚本验证限流边界。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 🗂️ 第五阶段：异步任务（项目 17-18）

> **目标**：marklet 有了后台肌肉——链接预取队列与优雅排空

### [x] 项目 17：后台任务队列——链接预取 worker

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~220 行 |
| **产出模块** | `internal/jobs`（内存队列 + worker 池）、书签创建时异步抓取页面标题 |
| **核心知识点** | 生产者-消费者（channel + errgroup worker，复用 concurrency/19）、任务重试与死信、抓取超时与 SSRF 防护（禁内网地址）、任务可见性（队列长度指标） |
| **验收标准** | ① 创建书签后 3 秒内标题被抓取并落库（异步断言）；② worker 崩溃模拟（panic 恢复）任务不丢；③ SSRF 用例：提交 http://127.0.0.1/ 被拒绝 |
| **前置** | 项目 12（书签已归属用户） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「后台任务」。请给我完整代码约 220 行：channel 队列 + errgroup worker 池、抓取 URL 标题（超时 3s、SSRF 防护禁内网）、失败重试 2 次进死信、队列长度指标，附异步落库/崩溃不丢/SSRF 拒绝三组断言。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 18：优雅停机与任务排空

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~150 行 |
| **产出模块** | 停机编排器（HTTP 停止接新 → 队列排空 → 关闭 DB/Redis） |
| **核心知识点** | 停机顺序（先摘流量再排空）、排空超时与强杀兜底、pg/redis 连接的关闭次序、停机期间任务状态可见 |
| **验收标准** | ① SIGTERM 时 50 个在途任务全部完成或进死信（零丢失断言）；② 排空超时 5s 后强制退出且有日志；③ 停机顺序日志可审计（摘流量→排空→关连接的时序） |
| **前置** | 项目 17、1（优雅退出的完整版） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「优雅停机」。请给我完整代码约 150 行：停机编排器（readyz 摘除 → HTTP Shutdown → 队列排空 5s 超时 → 关闭连接池），附 50 在途任务零丢失与停机时序断言。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 🗂️ 第六阶段：交付（项目 19-20）

> **目标**：marklet 变成一个 `docker compose up` 就能跑、CI 会拦坏代码的真服务

### [x] 项目 19：Docker 化 + Compose 三件套

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~120 行 Dockerfile/compose + 50 行 Go 调整 |
| **产出模块** | `Dockerfile`（多阶段构建，distroless/alpine 运行镜像）、`docker-compose.yml`（app+pg+redis 三服务）、非 root 运行 |
| **核心知识点** | 多阶段构建（构建镜像与运行镜像分离）、非 root 用户、健康检查配置、compose 依赖顺序（depends_on + 健康条件）、环境变量注入 |
| **验收标准** | ① `docker compose up` 一条命令起全家桶，/healthz 通过；② 运行容器内 `whoami` 非 root；③ 镜像体积记录（多阶段前后对比入档）；④ `docker compose down && up` 数据卷持久（书签还在） |
| **⚠️ 风险** | 镜像构建需拉 golang/alpine 基础镜像 ⚠️ 开工探活（走国内镜像源） |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「Docker 化」。请给我多阶段 Dockerfile（非 root、健康检查）、docker-compose.yml（app+postgres:15-alpine+redis:7-alpine，数据卷持久、depends_on 健康条件）、以及容器内配置注入的调整，附四条验收的验证脚本。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

### [x] 项目 20：🏁 CI + 健康检查 + 全链路压测

| 项目信息 | 详情 |
|:---|:---|
| **行数** | ~200 行（workflow + 压测脚本 + 修复） |
| **产出模块** | `.github/workflows/ci.yml`（vet/race/goleak/build）、`scripts/loadtest.sh`、压测发现的性能修复 |
| **核心知识点** | CI 门禁设计（哪些步骤挡合并）、全链路压测（100 并发 × 60s）、p99 基线与回归判定、发布清单（checklist） |
| **验收标准** | ① CI 五步（fmt/vet/race/goleak/build）本地等价脚本全绿；② 100 并发 60s 压测：成功率 ≥99.9%、p99 基线入档、连跑 3 次不回归；③ 压测发现至少 1 个瓶颈并修复（前后对比入档）；④ 发布 checklist 文档落盘 |
| **前置** | 项目 19 全部 |
| **⚠️ 风险** | GitHub Actions 远程跑不通时以本地等价脚本 `scripts/ci.sh` 为验收口径 |

**🤖 开始提示词**：
> `我要开始 Go Web 项目「CI 与压测」（marklet 收官）。请给我 ci.yml（fmt/vet/race/goleak/build 五步）、本地等价 scripts/ci.sh、100 并发 60 秒压测脚本（成功率与 p99 输出），附压测基线记录模板与发布 checklist。只输出代码。`

**完成日期**：2026-10-09
**踩坑记录**：________

---

## 📅 周计划

| 周次 | 内容 | 项目数 |
|:---|:---|:---:|
| **第 1 周** | 项目 1-2（骨架 + SQLite 迁移） | 2 |
| **第 2 周** | 项目 3-4（连接池 + 事务接线）⛓️ | 2 |
| **第 3 周** | 项目 5-6（PG 迁移 + sqlc） | 2 |
| **第 4 周** | 项目 7-8（Redis 缓存 + 一致性）⛓️ | 2 |
| **第 5 周** | 项目 9-10（密码会话 + JWT） | 2 |
| **第 6 周** | 项目 11-12（安全中间件）⛓️ | 2 |
| **第 7 周** | 项目 13-14（日志 + 指标） | 2 |
| **第 8 周** | 项目 15-16（OTel + 限流验收）⛓️ | 2 |
| **第 9 周** | 项目 17-18（任务队列 + 排空） | 2 |
| **第 10 周** | 项目 19-20（Docker + 🏁 CI 压测） | 2 |

## 🏆 里程碑

- [x] **完成项目 1-4** → 数据层工匠：SQLite 持久层、迁移、事务、池调参全部落盘
- [x] **完成项目 5-8** → 生产存储：PG/Redis 双真库上线，缓存一致性有实测数据
- [x] **完成项目 9-12** → 安全守门人：argon2id/JWT/CSRF/IDOR 防御全链路打通
- [x] **完成项目 13-16** → 可观测工程师：log/metric/trace/limit 四件套协同
- [x] **完成项目 17-18** → 异步驾驭者：后台队列零丢失、优雅排空
- [x] **完成项目 19-20** → 🏁 交付工程师：一条命令起全家桶，CI 门禁与压测基线齐备
- [x] **全部完成** → marklet 毕业：从空目录到可部署、可观测、有认证的真实服务

## 📝 每日日志

| 日期 | 项目 | 耗时 | 收获 | 踩坑 |
|:---|:---|:---:|:---|:---|
| | | | | |

## 🔧 环境配置

```bash
# 0. 已就绪（实测）：
#    - go1.26.5 darwin/arm64；GOPROXY=https://goproxy.cn,direct
#    - Docker 29.4.0 运行中（k8s 环境共存正常）
#    - postgres:15-alpine、redis:7-alpine 镜像已在本地（arm64，零拉取）
#    - sqlite3 3.51.0、curl、jq
#    - goleak v1.3.0（concurrency/net 已用）
# 1. 按需安装：
#    - modernc.org/sqlite（纯 Go 驱动，项目 2 ⚠️ 首拉探活；fallback mattn/go-sqlite3+CGO）
#    - sqlc CLI（项目 6 ⚠️ brew install sqlc 探活；fallback 手写 SQL 继续）
#    - golang.org/x/crypto（argon2id，项目 9 ⚠️ 首拉探活）
#    - go-redis/v9（项目 7）、pgx/v5（项目 5）、OTel（项目 15）⚠️ 首拉探活
# 2. 端口纪律：
#    - marklet 服务 127.0.0.1:19610~19639；pprof 内部端口 19639
#    - PostgreSQL 15432 / Redis 16379（避开本机常用端口）
# 3. 每项目统一检查：
go version && gofmt -l . && go vet ./... && go test -race ./...
```

## ⚠️ 与已有清单的关系

| 已有清单 | 关系 |
|:---|:---|
| docs/go_net.md（21/21 已完成） | **HTTP 地基前置**：net/12 中间件与优雅退出、net/09 超时行为学直接复用思想；认证中间件是 net/12 洋葱模型的业务化；net/16 的 mTLS 手法在项目 9 会话层有镜像。marklet 聚焦"有状态服务"（DB/缓存/认证/任务），net 线聚焦"协议与传输"——不重复 |
| docs/go_concurrency.md（23/23 已完成） | **组件直接复用**：singleflight（项目 7 防击穿）、errgroup（项目 17 worker 池）、goleak 门禁（全部测试）、atomic 计数 |
| docs/go_patterns.md（20/20 已完成） | **模式来源**：token bucket/滑动窗口（项目 16 限流）、bloom filter 思想（项目 7 穿透防护提及）、mini_redis 的 RESP 知识（项目 7 的 Redis 使用背景） |
| docs/go_internals.md（30/30 已完成） | **底层参照**：pprof（项目 14）、escape/alloc（压测优化项目 20） |
| docs/go_concurrency.md / go_net.md 的测试纪律 | **全套继承**：每个项目都要求 `go test -race` 全绿 + goleak（涉协程的项目） |

## ⚠️ 工具可用性速查表

| 能力 | 用于项目 | 实测结论 |
|:---|:---|:---|
| Docker + postgres:15-alpine | 5、19 | ✅ 镜像已在本地（arm64），Docker 29.4.0 运行中 |
| Docker + redis:7-alpine | 7、8、9、16 | ✅ 镜像已在本地（redis:7 与 8.6 两版） |
| sqlite3 CLI + modernc 驱动 | 2~4 | ✅ CLI 3.51.0 已装；modernc 驱动 ⚠️ 首拉探活 |
| sqlc CLI | 6 | ❌ 未装（brew install sqlc ⚠️ 探活；fallback 手写继续） |
| psql / redis-cli 宿主客户端 | 5、7 | ❌ 未装——用 `docker exec <容器> psql/redis-cli` 替代 |
| goproxy.cn 拉新模块 | 2/5/7/9/15 | ✅ 本仓库已多次实测可达（x/sync、goleak、grpc 均成功） |
| GitHub Actions 远程 | 20 | ⚠️ 以本地 scripts/ci.sh 为验收口径，远程为加分项 |
