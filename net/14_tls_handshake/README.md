# 14 · TLS 握手观察：自签服务、证书链与 ConnectionState 全景

> 自签一张证书、起一个 TLS 服务，然后从三个视角看同一次握手：Go 客户端的 ConnectionState（TLS 1.3、密码套件、对端证书链）、默认信任池的正确拒绝（x509.UnknownAuthorityError）、openssl s_client 的外部视角。附赠 KeyLog 文件——Wireshark 解密 TLS 流量的钥匙。

## Background

项目 11 里 TLS 只作为 h2 协商的载体一闪而过；本篇把 TLS 本身摊开。TLS 的作用人人知道——加密与身份验证——但"身份验证到底验证了什么"经常被答错：不是验证密码，而是验证**对端持有的证书能追溯到一张你信任的根证书**，且证书声明的域名/IP 与你访问的一致。

自己搭服务时最常撞上的就是这条验证链：自签证书不在系统根存储里，客户端报 certificate signed by unknown authority。多数人的第一反应是搜索"InsecureSkipVerify 一跳过就好了"——这等于关闭身份验证只留加密，连接确实通了，但也对任意中间人敞开了大门。

正确姿势是理解链条的每一环：证书是谁签的（自签/CA）、客户端凭什么信任（信任池/系统根存储）、SAN 必须包含目标地址、失败时的错误类型各自说明什么。本篇把这些全部做成可运行的实验。

## What

TLS 握手是一次"在不可信信道上建立可信连接"的协商：双方约定协议版本与密码套件，服务端出示证书链证明身份，并协商出只有两端知道的会话密钥。

一句话心智模型：**TLS 握手是海关过关——证书是护照（链路必须追溯到免签国：受信任的根 CA），SAN 是签证上的目的地（与你实际要去的一致），密码套件是通关通道的加密等级，KeyLog 是海关自己的留底（有它才能还原通关全程）**。

但和护照不同的是，证书链的验证是纯数学的：每一级证书用上一级的公钥验签，根证书的"信任"是你主动放进信任池的那个决定——自签实验里，把叶子加入信任池，拒绝就变成了通过，信任的边界一目了然。

握手完成后 `ConnectionState` 暴露全部协商结果：

| 字段 | 本篇实测值 | 含义 |
|---|---|---|
| Version | TLS 1.3 | 协商出的协议版本 |
| CipherSuite | TLS_AES_128_GCM_SHA256 | 密码套件 |
| PeerCertificates | CN=localhost, SAN 含 127.0.0.1 | 对端证书链（叶子在前） |
| HandshakeComplete | true | 握手完成标志 |

## When to Use

crypto/tls 的这些能力在三个时刻兑现。

- **内网服务间加密**：mTLS 前置篇（项目 16）——先会自签与信任池，再做双向认证
- **排查 TLS 故障**：知道 ConnectionState 里有什么，才能定位"握手失败"卡在哪一环（项目 17 的诊断手册）
- **需要解密自己的流量做调试**：KeyLog 文件 + Wireshark——只在测试环境用

何时不用：公网面向用户的服务直接用 `http.ListenAndServeTLS` 或网关终止 TLS；本篇的手工 `tls.Listen` 是学习与自定义协议的场景。

| 客户端配置 | 握手结果 | 用途 |
|---|---|---|
| RootCAs 加自签证书 | ✅ 通过 | 信任自签的正确姿势 |
| 默认（系统根存储） | ❌ UnknownAuthority | 正确的拒绝行为 |
| InsecureSkipVerify | ✅ 但无身份验证 | 仅调试，生产禁用 |

## Quick Start

前置条件：Go 1.21+、openssl（本机已装）。运行：

```bash
cd net/14_tls_handshake
go run .
go test -race -count=3 .
```

真实输出：

```text
== 第 1 步：信任池客户端——握手成功，ConnectionState 全景 ==
  握手完成=true  协议版本=TLS 1.3
  密码套件=TLS_AES_128_GCM_SHA256
  证书链[0]: CN="localhost"  有效期 2026-10-04 ~ 2026-10-06
  叶子 SAN: DNS=[localhost] IP=[127.0.0.1]

== 第 2 步：默认信任池客户端——自签被拒（正确行为）==
  错误类型：*tls.CertificateVerificationError
  错误内容：tls: failed to verify certificate: x509: certificate signed by unknown authority

== 第 3 步：InsecureSkipVerify——能连上，但这是危险开关 ==
  跳过验证握手成功=true——只用于调试，生产禁用

== 第 4 步：openssl s_client 外部观察 ==
  Verification error: self-signed certificate
  New, TLSv1.3, Cipher is TLS_AES_128_GCM_SHA256
  Protocol: TLSv1.3
```

测试验收（`-race` 下连跑 3 轮全绿）：

```bash
go test -race -count=3 .
```

## How It Works

### 信任的边界：信任池就是根

同一个自签证书，加入 `RootCAs` 池的客户端握手通过，默认客户端报 UnknownAuthority——两个行为的分界线只有一行 `roots.AddCert(leaf)`。

这演示了 PKI 的本质：**信任不是证书的属性，是验证方主动授予的决定**。生产里"把公司根 CA 装进系统"与这里的 `AddCert` 是同一件事，只是授予范围不同。

注意错误被包装成 `*tls.CertificateVerificationError`（Go 1.20+），要用 `errors.As` 穿透到内层的 `x509.UnknownAuthorityError` 分诊。

### SAN：比 CN 更重要的身份声明

证书的 CN（CommonName）字段已被现代验证流程弃用，**SAN（Subject Alternative Name）才是身份匹配的依据**：客户端校验"我访问的地址 ∈ 证书 SAN 列表"。

自签时忘配 SAN 是最常见的翻车点——证书链验证通过但主机名不匹配，报 x509.HostnameError。本篇生成证书时显式写入 `IPAddresses: 127.0.0.1` 与 `DNSNames: localhost`，缺一不可。

### KeyLog：Wireshark 解密的钥匙

`KeyLogWriter` 让 TLS 把每次握手的会话密钥按 NSS 格式写出来（行首 `CLIENT_HANDSHAKE_TRAFFIC_SECRET`）。

把这份文件喂给 Wireshark 的 TLS 配置，就能解密你自己的流量——调试协议实现的神器，也是"加密不等于抓包看不见"的正确打开方式：只有持有密钥的一方才能解密，密钥文件等同于密码，绝不进版本库。

## Pitfalls & Q&A

- **自签证书忘配 SAN**：现象是信任池加了证书仍然握手失败。原因：验证流程校验 SAN 而非 CN，证书里没有目标地址就报 HostnameError。解法：生成时显式写入 `IPAddresses` 与 `DNSNames`（本篇两者都有）
- **InsecureSkipVerify 当默认配置**：现象是"能连上就先用着"，中间人攻击面全开。原因：该开关关闭的是身份验证（谁都能冒充），只留加密。解法：只在本地调试用，生产用信任池或系统 CA 签发的证书
- **错误类型直接断言外层**：现象是 `err.(*x509.UnknownAuthorityError)` 断言失败。原因：Go 1.20+ 用 `tls.CertificateVerificationError` 包装了底层 x509 错误。解法：`errors.As` 沿错误链解包（本项目测试实录）
- **CN 当主机名用**：现象是换了 CN 但 SAN 正确时验证通过、CN 正确 SAN 缺失时失败。原因：现代客户端只看 SAN。解法：证书身份一律写 SAN，CN 只当显示名

**Q：TLS 1.3 和 1.2 的握手差在哪？**
A：1.3 把往返从 2-RTT 压到 1-RTT（会话复用 0-RTT），并删掉了全部老旧密码套件——本篇协商结果固定是 TLS_AES_128_GCM_SHA256 这类 AEAD 套件。对应用层的可感知差异：握手更快、更难被降级攻击。

**Q：自签证书和 CA 签发的证书安全性差在哪？**
A：加密强度完全一样，差在信任分发：自签需要把证书逐个塞进每个客户端的信任池（本项目做法），CA 体系用系统预置的根存储免去分发。内网 mTLS 自建 CA 是标准做法（项目 15）。

**Q：KeyLog 文件安全吗？**
A：它包含会话密钥——拿到它就能解密对应连接的全部流量。只写入内存缓冲或临时文件、调试完即删；绝不能在生产服务开启 KeyLogWriter。

**Q：下一步怎么从"观察"走到"建设"？**
A：项目 15 自建三级 CA（根→中间→叶子），把本篇的"信任池加一张证书"扩展成"信任一条链"；项目 16 再升级成双向 mTLS。
