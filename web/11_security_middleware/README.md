# 11 · 安全中间件全家桶：安全头、Bearer、CSRF 与请求体上限

> marklet 的入口防线四层同框：安全响应头（CSP/XFO/nosniff/HSTS）、Bearer 认证
> （401 统一语义）、CSRF 双提交（写请求双证齐全才放行）、请求体上限（2MB 阈值，
> 超限 413 先于业务执行）。逐层实测：无令牌与伪造令牌都进不来，缺 CSRF 头 403，
> 2MB+1 字节被拒之门外。

## Background

实验 10 的 JWT 解决了"你是谁"，但入口还有一群不请自来的问题：浏览器被诱导
提交跨站表单（CSRF）、页面被 iframe 套壳收集点击（点击劫持）、超大请求体
把内存吃满（资源耗尽）、响应被浏览器错误地当 HTML 执行（内容嗅探）。

这些问题没有一个是业务代码的 bug，却都能让业务代码背锅——所以解法必须
长在入口上：中间件。四层各司其职，任何一层失守都有另一层兜着；
组装顺序本身就是安全语义（认证必须在业务前，体积限制必须在读取前）。

## What

四个中间件，按"看到请求的先后"组装：

| 层 | 职责 | 失败语义 |
|---|---|---|
| SecureHeaders | 给响应挂浏览器防御指令（CSP 等 5 头） | 无失败，恒定注入 |
| Bearer | JWT 验签 + 算法白名单（实验 10 的纪律平移） | 401 + WWW-Authenticate |
| CSRFProtect | 写请求校验 X-CSRF-Token == csrf Cookie | 403 |
| MaxBody | `http.MaxBytesReader` 封顶请求体 | 413 |

CSRF 双提交是把令牌放两处：Cookie 里一份（自动随请求携带），自定义头里一份
（攻击者的跨站请求补不出来——同源策略挡住了它读 Cookie 的能力）。服务端只认
"两处相等"的写请求。恒定时间比较让"逐字节猜令牌"失去计时反馈。

## When to Use

- **任何对外暴露的 API**：安全头与体积限制是出厂默认，与业务无关
- **浏览器在场的写操作**（Cookie 自动携带的世界）：CSRF 双提交是标配
- **Bearer 保护的资源**：401 的统一语义（WWW-Authenticate 头）是 HTTP 协议要求，
  也是客户端库判断"该去刷新令牌"的依据

何时不用：纯服务间调用（无浏览器即无 CSRF 问题）；内部探针端点
（/healthz 豁免认证与 CSRF，否则负载均衡会把服务判死）。

| 层 | 对抗的攻击 | 缺失后果 |
|---|---|---|
| SecureHeaders | 嗅探/劫持/混合内容 | 浏览器替攻击者开路 |
| Bearer | 未认证访问 | 裸奔 |
| CSRF | 跨站伪造写请求 | 用户的登录态被借用 |
| MaxBody | 超大请求体耗尽资源 | OOM 风疹一遍 |

## Quick Start

前置条件：Go ≥ 1.22；无外部服务（令牌签发与校验都在进程内）。运行：

```bash
cd web/11_security_middleware
go run .
go test -race -count=3 .
```

真实输出（`go run .`，服务 127.0.0.1:19620，跑完自动退出）：

```text
== 第 1 步：Bearer——三连 ==
  无令牌   → 401（WWW-Authenticate="Bearer realm=\"marklet\""）
  伪造令牌 → 401
  有效令牌 → 200 {"user":"alice"}
== 第 2 步：CSRF 双提交——写请求必须双证齐全 ==
  令牌已发放（cookie=7f4d614c8f10e103...）
  缺 CSRF 头 → 403 csrf check failed
  双证齐全   → 200 {"user":"alice","received":12}
== 第 3 步：安全响应头——逐项断言在测试里，这里打印 ==
  Content-Security-Policy      = default-src 'none'
  X-Frame-Options              = DENY
  X-Content-Type-Options       = nosniff
  Strict-Transport-Security    = max-age=63072000; includeSubDomains
  Referrer-Policy              = no-referrer
== 第 4 步：请求体上限——2MB 阈值 ==
  恰好 2MB   → 200 {"user":"alice","received":2097152}
  2MB+1 字节 → 413 body too large
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁）：

```text
ok  	marklet/11_security_middleware	1.572s
```

诚实预期：CSRF 令牌每次运行随机；令牌发放走单一出口 /api/v1/csrf
（Cookie 与响应体各发一份会造成双提交的两半对不上——本篇实测踩中后修复）。

## How It Works

### 组装顺序即安全语义

`use(mux, SecureHeaders, Bearer, CSRFProtect, MaxBody)` 的执行序是
安全头 → 认证 → CSRF → 体积 → 业务。认证放 CSRF 之后，未认证请求先消耗
校验资源；体积限制放最后，超大请求体穿过全部中间件才被拒。/healthz 全放行。

### CSRF 双提交的成立条件

三件事缺一不可：Cookie 只随同源请求自动携带；攻击者的页面读不到本域 Cookie
（同源策略）；所以攻击者能发请求但补不出令牌头。服务端校验"头 == Cookie"用
恒定时间比较，逐字节猜令牌得不到计时反馈。令牌发放必须单一出口——本篇实测
踩中"中间件与 handler 各发一份"导致的对不上。

### MaxBody 在读取路径上拦人

`http.MaxBytesReader` 包装 r.Body，读超限即返回错误并通知 ResponseWriter
标 413。业务 handler `io.ReadAll` 的那一刻就是闸门生效的时刻——内存里永远
只有至多 2MB+缓冲的请求体，"先全读进内存再判断大小"的经典错误从结构上不存在。

### 401 的统一语义

认证失败统一 401 + `WWW-Authenticate: Bearer ...`，过期、伪造、缺失在响应
里无法区分——这条纪律与实验 09 的登录文案同源：认证端点的差异信息是攻击者的
路标，客户端只需要"该重新拿令牌了"这一个信号。

## Pitfalls & Q&A

- **CSRF 令牌两个出口**：现象是客户端拿到的 Cookie 值与响应体令牌不同，全部
  写请求 403（本篇实测）。原因：中间件与 handler 各自生成了随机令牌。
  解法：发放收口到单一端点
- **豁免清单写在业务里**：现象是 healthz 被认证拦住、负载均衡判死服务。
  原因：豁免是中间件的职责，散在 handler 里无法审计。解法：豁免路径集中
  在中间件层显式声明
- **CSRF Cookie 设了 HttpOnly**：现象是前端拿不到令牌值，所有写请求 403。
  原因：双提交模式要求客户端可读 Cookie。解法：HttpOnly 不设，
  SameSite=Lax 照设
- **先全读再判大小**：现象是内存随请求体膨胀。原因：`io.ReadAll` 无上限。
  解法：MaxBytesReader 包住 Body，读取本身带闸

**Q：CSRF 和 CORS 是什么关系？**
A：两件正交的事。CSRF 防的是"别人网站替用户发请求"（攻击方向：外→内）；
CORS 是"本站允许谁跨域读响应"（授权方向：内→外）。配好 CORS 不解决 CSRF——
简单跨站请求根本不受 CORS 预检约束。双提交是 CSRF 的防御，CORS 是另一扇门锁。

**Q：HSTS 会不会把开发环境锁死？**
A：会。浏览器记下 max-age 后两年内拒绝 HTTP 访问该域。解法：HSTS 只在生产
域注入（按环境开关中间件的这一条头），或开发用完全不同的域名。

**Q：为什么 413 而不是 400？**
A：语义即契约：413（Payload Too Large）告诉客户端"重试无益，缩小再试"；
400 表示请求本身格式有问题。MaxBytesReader 触发的超限明确属于前者——
状态码是 API 与客户端的对话语言，说准比说全重要。

**Q：这四层和实验 12 的全站认证是什么关系？**
A：本篇是入口的通用防线，实验 12 把身份贯穿到数据——书签按 owner 隔离、
越权访问返回 404。入口层回答"你是谁"，数据层回答"这数据是不是你的"，
两层都过才算安全闭环。
