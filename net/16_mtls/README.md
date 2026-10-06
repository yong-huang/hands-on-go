# 16 · mTLS 双向认证：服务端也要验你的身份

> 单向 TLS 只回答"服务器是谁"；mTLS 把同一套证书机制镜像到另一边——客户端也持叶子证书，服务端用 `RequireAndVerifyClientCert` 验证它。本篇实测三种客户端的命运：无证书与假 CA 证书在握手里被拒（客户端侧感知为 `remote error: tls: certificate required`），正确证书通过且服务端提取对端 CN 回话确认。

## Background

项目 15 建好了 CA 链，客户端验证服务端身份。但内网 zero-trust 语境下这是半个方案：能连上网关端口的任何进程都能冒充合法调用方——网络边界不再是信任边界，服务必须自己验证"对面是谁"。

mTLS（mutual TLS）的机制与单向 TLS 完全对称：客户端也持一张由 CA 签发的证书（ExtKeyUsage=ClientAuth），握手时出示给服务端。

服务端配置 `ClientAuth: RequireAndVerifyClientCert` 与 `ClientCAs`（信任哪个 CA 签的客户端证书），验证逻辑与项目 15 的服务端验证一字不差，只是方向相反。

两个方向各一条链，本篇复用同一个 CA（生产中客户端链与服务端链通常分开，按用途分权）。

配置只有核心一行，但 TLS 1.3 的拒绝时机有一个坑：**服务端拒绝客户端证书时，客户端的本地握手仍然成功**——服务端发 `certificate_required` 警报后关连接，客户端要到第一次读才知道被拒。本篇实测了这个行为并给出感知方法。

## What

mTLS 是双向的 TLS 身份验证：握手时双方各出示证书链，各自验证对方——服务端验客户端（ClientCAs 信任池），客户端验服务端（RootCAs 信任池）。加密照旧，区别是"我是谁"由单方声明变成双方证明。

一句话心智模型：**单向 TLS 是酒店查验访客（你核实前台是真前台）；mTLS 是核销对暗号——前台核实你的工牌（ClientAuth），你也核实前台（RootCAs），双方都验完才开门**。

但和暗号不同的是，双方身份的验证都是纯数学的证书链回溯（项目 15 的机制），不共享任何秘密——私钥从不出门，验证只依赖证书与签名。

三种客户端在 `RequireAndVerifyClientCert` 下的实测命运：

| 客户端 | 握手结果 | 服务端视角 |
|---|---|---|
| 无证书 | ❌ `certificate required` | client didn't provide a certificate |
| 正确 CA 签发（CN=alice） | ✅ 服务端回话含 CN | PeerCertificates[0].CN = alice |
| 错误 CA 签发（CN=mallory） | ❌ `certificate required` | 同上——CA 不在 ClientCAs，等效无证书 |

## When to Use

mTLS 是内网服务间认证的标配方案。

- **zero-trust 服务网格**：服务与服务的每次连接双向证明身份——Istio/Linkerd 的底层就是每 Pod 一张客户端证书的 mTLS
- **管理面与数据面隔离**：Kubernetes apiserver 对组件的认证、etcd 集群节点互认，全是 mTLS
- **设备/agent 接入**：IoT 设备出厂烧录客户端证书，服务端按 CN 授权

何时不用：面向公网用户的服务——给每个用户发证书不现实，Web 用 token/OAuth2；只需要加密不需要互认的场景，单向 TLS 更简单。

| 认证方式 | 身份载体 | 吊销 | 适用 |
|---|---|---|---|
| 单向 TLS | 服务端证书 | 证书体系 | 公网加密 |
| mTLS（本篇） | 双方证书 | 证书体系 | 内网服务互认 |
| Token/OAuth2 | 共享令牌 | 令牌过期/黑名单 | 面向用户 |

## Quick Start

前置条件：Go 1.21+。运行：

```bash
cd net/16_mtls
go run .
go test -race -count=3 .
```

真实输出：

```text
== 第 1 步：无证书客户端 → 握手失败 ==
  [server] 握手失败: tls: client didn't provide a certificate
  握手失败（正确行为）：remote error: tls: certificate required

== 第 2 步：持有正确客户端证书 → 通过，对端 CN 被服务端验证 ==
  服务端回话: "hello alice (CN verified)\n"

== 第 3 步：错误 CA 签发的客户端证书 → 握手失败 ==
  [server] 握手失败: tls: client didn't provide a certificate
  握手失败（正确行为）：remote error: tls: certificate required
```

测试验收（`-race` 下连跑 3 轮全绿）：

```bash
go test -race -count=3 .
```

## How It Works

### ClientAuth 策略的四个档位

ClientAuth 策略有四档，`RequireAndVerifyClientCert` 是最严格档（要求出示证书且验证链）：

- `NoClientCert`：不要求客户端证书（单向 TLS）
- `RequestClientCert`：请求出示但不验证
- `RequireAnyClientCert`：要求出示但不验链——等于自报家门
- `RequireAndVerifyClientCert`：要求 + 验链，mTLS 标配

验证用的信任锚是 `ClientCAs`——注意它与客户端的 `RootCAs` 是**两个方向独立的池**：服务端验证客户端链用 ClientCAs，客户端验证服务端链用 RootCAs，互不混用（生产中两边常用不同 CA，分权即在此）。

### TLS 1.3 的拒绝时机：客户端要读一次才知道

开发实录：第一版用 `tls.Dial` 后断言错误，无证书与假证书客户端都"握手成功"。

真相：TLS 1.3 的握手设计里，服务端对客户端证书的拒绝以 `certificate_required` 警报 + 关闭连接实现，而客户端侧的 `Handshake()` 在发出 Finished 消息后就返回成功——**被拒的证据在服务端发回的警报里，客户端要读到它才知情**。

修复：Dial + Handshake 之后立即 `Read` 一次，实测得到 `remote error: tls: certificate required`。这也解释了服务端日志：TLS 1.3 下假证书与无证书在服务端报错相同，分诊要靠客户端侧错误。

### 对端 CN：认证之后的授权输入

握手通过后，服务端从 `ConnectionState().PeerCertificates[0]` 拿到客户端叶子证书，CN 即客户端身份（alice/bob）。本篇把它回话给客户端确认。

生产中的下一步是**授权**——按 CN/Organization 查 ACL 决定能调哪些接口。注意认证（你是 alice）与授权（alice 能干什么）是两步，mTLS 只做前者。

## Pitfalls & Q&A

- **以为 tls.Dial 报错才算被拒**：现象是无证书客户端"握手成功"。原因：TLS 1.3 的拒绝在服务端侧警报里，客户端本地握手照常成功。解法：Dial + Handshake 后立即读一次（本项目实录）
- **ClientCAs 与 RootCAs 混为一谈**：现象是客户端用服务端 CA 签的证书被拒。原因：两个池管两个方向。解法：明确"服务端验证客户端用 ClientCAs，客户端验证服务端用 RootCAs"，两边可以（通常应该）是不同 CA
- **客户端证书忘配 ExtKeyUsage=ClientAuth**：现象是拿服务端证书当客户端证书被拒。原因：证书的 EKU 声明用途，ServerAuth 证书过不了 ClientAuth 验证。解法：签发时按用途分开模板（本篇两个模板函数）
- **把 CN 当唯一身份**：现象是重名用户越权。原因：CN 只是可读名，同一 CA 可以签两张同名证书。解法：授权以证书指纹（SHA-256）或序列号为主键，CN 作展示

**Q：mTLS 的证书轮换怎么做才不断服？**
A：证书"先进后出"：新证书先部署、新旧并存在线一个周期、旧证书再下线。Go 的 `tls.Config` 支持 `GetCertificate`/`GetClientCertificate` 回调——回调里返回当前有效证书即可热切换，无需重启进程。配合短有效期（项目 15 的 24h~7d）实现自动轮换。

**Q：双向验证对性能影响多大？**
A：增加一次客户端证书的验证（每条连接一次，不是每请求）。ECDSA P-256 验签微秒级，长连接场景可忽略；高频短连接场景收益来自连接复用（项目 10/11 的池与 h2 多路复用都直接适用）。

**Q：服务网格里的 mTLS 是不是也是这套？**
A：机制相同，工程形态不同：网格由 sidecar 代理持有证书并做 mTLS，业务进程对加密无感知；证书由网格 CA 短周期自动签发轮换。理解了本篇的 ClientAuth/ClientCAs/双向链，再看 Istio 的 PeerAuthentication 配置就是纯语义层。

**Q：这条链路离项目 21 迷你 RPC 还缺什么？**
A：就认证而言已经齐了——项目 21 把本篇的 `tls.Config` 直接装到自研协议的 listener/dialer 上，服务端从 PeerCertificates 取 CN 做调用方鉴权。剩下的三块（超时重试元数据、服务注册表、压测）见收官篇。
