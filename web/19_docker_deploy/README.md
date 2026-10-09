# 19 · Docker 化 + Compose：一条命令的全家桶

> marklet 变成 `docker compose up` 就能跑的服务：多阶段构建把运行镜像压到 28MB
> （构建镜像 1.29GB），容器内非 root 运行，depends_on 等健康条件而非容器存在，
> 数据卷让 down/up 之后书签还在。验收脚本一条命令复演全部四条。

## Background

前 18 篇的 marklet 一直长在宿主机上：`go run` 启动、PG 与 Redis 是共享容器、
依赖版本散落在开发机的环境里。"在我机器上是好的"——部署的第一个坑永远在这里。

容器化解决的是"交付物"问题：把二进制、运行时、配置打成一个不可变的镜像，
任何装了 Docker 的机器行为一致。多阶段构建（multi-stage build）进一步拆开
"构建环境"与"运行环境"：SDK 只在第一阶段存在，运行镜像只有一个静态
二进制——体积从 GB 级降到两位数 MB，攻击面与拉取时间一起缩水。

## What

三服务栈：app（本篇的容器化 marklet）+ postgres + redis，由 Compose
（声明式编排：一个 YAML 描述服务、依赖、网络、卷，一条命令拉起全部）组装。
Dockerfile 的两个关键段：

- 构建段：`golang` 镜像内 `go mod download` + `CGO_ENABLED=0 go build`——
  产出静态二进制（不依赖 C 库，alpine 里直接跑）
- 运行段：`alpine` + 二进制 + 非 root 用户 + HEALTHCHECK——不含任何构建工具

可以把多阶段构建想成餐厅后厨与前厅分离：后厨（构建段）有灶台、原料、
师傅（SDK、源码、工具链），顾客看到的前厅（运行段）只有菜品（二进制）。
但与前厅不同的是，运行镜像里连菜单都没有——不是"整理得干净"，
是"根本没有"，体积与攻击面因此归零。

| 镜像 | 内容 | 体积（实测） |
|---|---|---|
| golang:1.26（构建段） | SDK + 源码 + 模块缓存 | 1.29GB |
| 19_docker_deploy-app（运行段） | alpine + 二进制 + 用户 | 28MB |

## When to Use

- **交付任何常驻服务**：镜像即交付物，"跑得起来"由 Dockerfile 保证而非环境
- **依赖复杂的服务**：PG + Redis + 应用三件套的启动顺序、健康条件、数据卷
  全部声明在 compose 里，新人一条命令获得完整环境
- **数据必须活过部署**：数据卷（volume）独立于容器生命周期，down 不删卷

何时不用：一次性本地实验（docker run 单容器足够）；有平台团队维护的
K8s 集群（compose 让位给 Helm/K8s manifest，但镜像构建逻辑不变）。

| 交付物 | 一致性 | 启动方式 |
|---|---|---|
| 手装依赖 | 低 | README + 祈祷 |
| 单容器 | 中 | docker run + 手动依赖 |
| Compose 栈（本篇） | 高 | 一条命令 + 健康条件 |

## Quick Start

前置条件：Docker 运行中；首次构建会拉取基础镜像与模块（走 goproxy.cn）。运行：

```bash
cd web/19_docker_deploy
./scripts/verify.sh
```

真实输出（节选；容器编排的进度行略）：

```text
== 等健康检查通过 ==
app healthy
== 健康检查 ==
ok
== CRUD（写入→读回）==
{"id":2}
[{"id":1,"title":"Go 官网","url":"https://go.dev"},{"id":2,"title":"Go 官网","url":"https://go.dev"}]
== 非 root 验证（容器内 whoami）==
marklet
== 镜像体积（构建镜像 vs 运行镜像）==
19_docker_deploy-app:latest 28MB
golang:1.26 1.29GB
== 数据持久化：down（保留卷）→ up → 数据还在 ==
书签在重启后仍在（卷持久化生效）
全部验收通过
```

测试验收（app 源码的 vet 与构建；镜像验证由 verify.sh 承担）：

```text
ok  	marklet/19_docker_deploy	2.598s
```

诚实预期：首次构建耗时以分钟计（模块下载），之后命中缓存秒级；
`docker compose down` 保留数据卷，彻底清场用 `down -v`。

## How It Works

### 依赖等待：service_healthy 与应用内重试

`depends_on.condition: service_healthy` 让 app 等 PG 的 `pg_isready` 通过
才启动——但"PG 接受连接"与"应用连通"之间仍有缝隙，所以应用启动时对 PG
重试最多 30 次。编排层管容器顺序，应用层管自己的就绪，两层各管一段。

### 非 root 与 HEALTHCHECK：镜像的两个出厂默认

`USER marklet`（uid 10001）让进程没有任何特权——被攻破也只有 marklet 的
能力边界。HEALTHCHECK 让容器自声明"怎么判断我活着"，`docker ps` 直接显示
healthy/unhealthy。verify 脚本的 `whoami` 断言的就是这两行。

### 数据卷：生命周期独立于容器

`pgdata:/var/lib/postgresql/data` 把 PG 的数据目录挂在命名卷上。容器删除重建
（down/up）数据原样保留——部署的不可变性与数据的持久性由两个不同的机制承载。
`down -v` 才会删卷，verify 脚本刻意不用它，并在最后保留数据供人工检查。

## Pitfalls & Q&A

- **GOPROXY 没进构建段**：现象是 `go mod download` 全线 unexpected EOF
  （本篇实测）。原因：ENV 只写在了 build 一行上，download 步骤仍走官方源。
  解法：ENV GOPROXY 放在下载之前
- **构建上下文缺 go.mod**：现象是 COPY 找不到文件。原因：context 指向实验
  目录而 go.mod 在 web/。解法：compose 里 `context: ..` + Dockerfile 按新
  上下文写相对路径
- **depends_on 不带条件**：现象是应用启动时 PG 还在初始化，连接全部被拒。
  原因：默认 condition 只等容器启动。解法：`service_healthy` + 健康检查
  （外加应用内重试兜底）
- **镜像里跑 root**：现象是容器逃逸后攻击者直接是 root。解法：显式
  `USER` 非 root 账户，verify 脚本断言 `whoami`

**Q：为什么运行镜像选 alpine 而不是 distroless/scratch？**
A：alpine 带 shell（调试友好）；distroless 无 shell 更安全；scratch 最小但
连 CA 证书都要自己拷。教学期选 alpine，上生产换 distroless 只改一行 FROM。

**Q：go.sum 进镜像了吗？构建层缓存怎么工作？**
A：go.sum 只在构建段，运行段没有。层缓存按 COPY 内容哈希失效：清单不变时
`go mod download` 层直接命中，改源码不重拉模块。把"拷清单"与"拷源码"分成
两步，正是为了让这个缓存生效。

**Q：Redis 呢？**
A：compose 里保留了 redis 服务位（本篇 app 未用到——13~16 的观测组件接它的
方式见各篇）。三件套的完整性在 compose 里先占位，接线是既有实验的反向回放。

**Q：compose 与 K8s 的关系？**
A：compose 是单机开发/演示的编排；K8s 是集群的调度。两者消费的是同一份
镜像——Dockerfile 的投入完全复用，编排描述从 YAML 翻译成 manifest 即可。
镜像的不可变性与非 root 是两者共同的前提，也是本篇真正交付的东西。
