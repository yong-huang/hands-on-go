# 17 · TLS 故障诊断手册：四种现场、双口径证据与一页决策树

> 生产 TLS 故障来来去去就四类：证书过期、SAN 错配、链不完整、时钟偏差。本篇把它们全部做成一键复现的现场，Go 客户端错误链与 openssl s_client 双口径取证，最后一页决策树把"报错 → 最可能原因 → 验证命令"串起来。附带两个分诊铁律：HostnameError 有两种文案，not-yet-valid 与 expired 同报一个 Reason——都只能按消息分诊。

## Background

TLS 故障的恼人之处不在修复而在定位：`tls: failed to verify certificate` 这一句话后面藏着至少四种不同的病。

处置方式各不相同——换证书、重签 SAN、补中间证书、校准时钟，改错方向等于白忙。

定位困难的根源有二。其一，Go 1.20+ 用 `tls.CertificateVerificationError` 包装了全部验证错误，不 `errors.As` 解包就只能看到外层套话。

其二，同类 x509 错误会复用同一个 Reason 常量——时钟回拨导致的"not yet valid"与真过期报同一个 `Expired`。

双口径取证是对策：Go 错误链给类型信息，`openssl s_client` 给独立实现的第二意见，两边对照基本可以锁定病因。

本篇把四个现场全部做成可一键复现的实验（不是讲道理，是造出来给你看），证据格式固定，决策树可抄进运维手册。

## What

TLS 诊断是一种基于错误分诊的排障方法：从验证失败的报错文案与错误链类型推断病因，用第二个工具交叉验证，再按病因处置。

一句话心智模型：**TLS 验证是三道关卡依次过——时间关（有效期）、身份关（SAN）、信任关（链回溯）——报错文案告诉你死在哪一关，但同关有多个死法，要再看第二句**。

但和关卡检查不同的是，死因的报告措辞在不同客户端并不一致：Go 说 "is not yet valid"，openssl 说 "certificate has expired"——这就是需要双口径的原因：单口径的文案差异会误导分诊。

四个现场与实测证据：

| 现场 | Go 错误链内层 | openssl 口径 | 处置 |
|---|---|---|---|
| 证书过期 | CertificateInvalidError，"is after" | certificate has expired | 换证书 + 自动轮换 |
| SAN 错配 | HostnameError，"is valid for X, not Y" | unable to verify the first certificate | 按服务地址重签 |
| 链不完整 | UnknownAuthorityError | unable to verify the first certificate | 服务端补中间证书 |
| 时钟偏差 | CertificateInvalidError，"is before" | unable to verify the first certificate | NTP 校准 |

## When to Use

这套分诊体系在三个时刻直接抄用。

- **告警响了"TLS handshake failed"时**：先跑本篇四现场对照报错文案，一分钟内锁定病类
- **服务证书轮换后**：新证书上线报错——多半是 SAN 或链，本篇实验 2/3 就是现场
- **写运维手册/值班文档时**：末尾的决策树即插即用，每条都带验证命令

何时不用：错误发生在 mTLS 客户端证书方向（`certificate required` 类，项目 16 的分诊）；握手更早阶段的失败（拨号不通、超时）——那是网络问题不是 TLS 问题，先 `netstat`/ping。

| 报错文案片段 | 病类 | 第一处置 |
|---|---|---|
| "is after ..."（expired 句式） | 证书过期 | 换证书 |
| "is valid for X, not Y" / "IP SANs" | SAN 错配 | 重签 |
| "unknown authority" | 链不完整/缺根 | 补中间证书 |
| "... is before ..."（not yet valid 句式） | 时钟偏差 | NTP |

## Quick Start

前置条件：Go 1.21+、openssl（本机已装）。运行：

```bash
cd net/17_tls_diagnostics
go run .
go test -race -count=3 .
```

真实输出（节选，时间戳随运行时刻变化）：

```text
== 故障 1：证书过期（NotAfter 在过去） ==
  [Go 客户端错误链]
    *tls.CertificateVerificationError: tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2026-10-05T21:16:45+08:00 is after 2026-10-05T12:16:45Z
    x509.CertificateInvalidError: x509: certificate has expired or is not yet valid: current time 2026-10-05T20:16:45+08:00 is after 2026-10-05T12:16:45Z
  [openssl s_client]
    Verification error: certificate has expired
  [处置] 换新证书；长期方案是自动化轮换（短有效期）

== 故障 4：客户端时钟偏差（回拨一年） ==
  [Go 客户端错误链]
    *tls.CertificateVerificationError: tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2025-10-05T21:16:45+08:00 is before 2026-10-05T12:16:45Z
  [openssl s_client]
    Verification error: unable to verify the first certificate
  [处置] 校准客户端系统时钟（NTP）；NotBefore 校验对时钟敏感

== 诊断决策树（报错 → 最可能原因 → 验证命令）==
  x509: certificate signed by unknown authority
      → 链不完整（最常见）或信任池缺根 → openssl s_client 看 Certificate chain 是否含中间；服务端补中间证书
```

测试验收（`-race` 下连跑 3 轮全绿，四现场各一个错误分诊断言 + 决策树覆盖断言）：

```bash
go test -race -count=3 .
```

## How It Works

### 双口径为什么必要：同一现场、两种措辞

故障 2（SAN 错配）的双口径对比是最好的教具：同一现场，Go 报 "is valid for X, not Y"（直指病因）；openssl 报 "unable to verify the first certificate"——措辞含糊，两类病合并成一个文案。

只看 openssl 口径会误判成"链问题"去补中间证书，方向就错了。

反过来，故障 4 的时钟偏差在 openssl 口径下完全没有独立信号。

结论：**Go 错误链定病类，openssl 做交叉确认**，顺序不能反。

### 同一个 Reason 的两种死法：is after vs is before

x509 对"过期"与"未生效"都返回 `CertificateInvalidError` 且 `Reason=Expired`——类型层面无法区分。

分诊只能看消息里的时间比较方向：`current time ... is after ...` 是真过期（服务端证书老了），`current time ... is before ...` 是未生效（客户端时钟回拨，证书在"未来"）。

本篇用 `Time` 回调把客户端时钟拨回一年制造后者的现场——这是 Go 标准库提供的唯一不改系统时钟的实验方法。

### 错误链的解包路径

四个现场的 Go 错误都是两层：外层 `*tls.CertificateVerificationError`（1.20+ 新增的包装，含原始证书与验证选项），内层才是 x509 的具体类型。

诊断代码必须 `errors.As` 穿透到内层分诊——只 match 外层会让所有失败变成同一个"verification failed"。

本篇 `goProbe` 把整条错误链按层打印，现场报告可直接粘贴给同事。

## Pitfalls & Q&A

- **只 match 外层错误类型**：现象是所有 TLS 失败都进了同一个分支。原因：Go 1.20+ 的 CertificateVerificationError 包装层。解法：errors.As 解包到 x509 内层再分诊（本篇 goProbe 的链式打印）
- **按 Reason 区分过期与未生效**：现象是时钟偏差被误诊为"服务端证书过期"，换了证书也没用。原因：两者 Reason 同为 Expired。解法：看消息里 `is after`（真过期）还是 `is before`（未生效/时钟回拨）——本项目实录
- **信任 openssl 的措辞**：现象是把 SAN 错配当链问题，补了中间证书无效。原因：openssl 的 "unable to verify the first certificate" 是多因一果的合并文案。解法：Go 错误链定病类、openssl 交叉验证，顺序不能反
- **时钟偏差只查服务端**：现象是服务端时钟正确但客户端仍报 not yet valid。原因：证书校验用的是**验证方**（客户端）的时钟。解法：两端 date 都比对，客户端偏差同样致命

**Q：为什么"链不完整"是最常见的现场？**
A：因为它出现在每一次常规变更里：换新证书时运维只替换了叶子文件、忘了中间证书文件。服务器自己能用（它有完整链做双向验证时），客户端却全部失败——典型的"服务端看起来正常"的陷阱。预防：部署脚本校验服务端链的完整性（叶子 Issuer 必须能在自己发送的链里找到）。

**Q：时钟偏差为什么会发生在服务器上？**
A：NTP 失效、虚拟机暂停后恢复、闰秒处理 bug 都可能。证书的 NotBefore/NotAfter 是绝对时间戳，两端时钟差超过证书有效期边界就会失败。这就是为什么 TLS 实现都强烈建议 NTP——时间不齐，密码学时间戳全部失真。

**Q：决策树怎么扩展到 mTLS 方向？**
A：客户端证书方向的故障（`certificate required` / `bad certificate`）分诊逻辑对称：无证书查 ClientAuth 档位、假证书查 ClientCAs 信任池、过期/错配同本篇（角色换成客户端证书）。项目 16 的实验就是这条分支的现场复现。

**Q：四现场之外还有什么会失败？**
A：协议版本不匹配（服务端只开 TLS 1.1 被现代客户端拒）、密码套件不兼容（合规加固误配）、SNI 缺失（一个 IP 多证书时客户端不带 ServerName）。

它们与证书验证无关、发生在握手更早阶段，报错是 `protocol version not supported` 类——诊断路径完全不同，属于另一本手册。
