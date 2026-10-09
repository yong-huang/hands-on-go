# 09 · argon2id 密码与会话：注册、登录、轮换与登出

> marklet 有了真实用户：argon2id 存密码（同一密码两次哈希必然不同）、Redis 存会话
> （TTL 即过期）、登录即轮换 SID（会话固定攻击的终结点）。注册/登录/登出/当前用户
> 四个端点用真实 Cookie 走通全流程。

## Background

用户的密码不能明文存——数据库一旦泄露，明文等于把所有账号双手奉上。也不能用
MD5/SHA-256 这类通用哈希：它们计算太快，攻击者拿 GPU 每秒算几十亿次，
字典攻击形同虚设。密码哈希需要"故意慢 + 故意费内存"的专用算法。

登录状态同样有历史包袱：Cookie 加密用户名（可伪造）、URL 带会话 ID（可泄露）
均已淘汰。现代方案是服务端存会话、客户端只拿不可预测的随机 ID——但有个经典
绕过手法：会话固定攻击（预塞已知 SID 等受害者登录后沿用），防御是登录即轮换。

## What

本篇交付两个组件。密码层：argon2id（密码哈希竞赛冠军，内存困难型，当前事实
标准），存储用 PHC 字符串格式——参数、盐、哈希编成一个自描述字符串。会话层：
Redis 键 `sess:<SID>`，载荷是用户 ID 的 JSON，TTL 即过期时间，过期管理全交给
Redis，服务端没有任何扫描任务。

可以把会话想成寄存处的手牌：服务端保管真实状态，客户端只拿编号牌。编号必须
不可预测（否则报编号就能取走东西）；登录轮换则是"办正式手续换新牌、旧牌作废"——
攻击者预塞的旧牌从此一文不值。

| 组件 | 关键决策 | 理由 |
|---|---|---|
| 密码哈希 | argon2id，PHC 串自描述 | 内存困难抗 GPU；参数随串走，升级不迁移 |
| SID / 过期 | crypto/rand 128 位；Redis TTL | 安全性=猜不到；过期零扫描任务 |
| 登录 | 旧 SID 销毁 + 新 SID | 会话固定攻击的结构性防御 |

## When to Use

- **需要登录态的服务**：会话是默认答案，本篇的四个端点即最小完整集
- **密码重置/用户体系搭建**：argon2id + PHC 串可以直接搬走，参数写死默认值即可
- **安全评审**：盐随机性、轮换、统一失败文案、HttpOnly——本篇四条断言就是清单

何时不用：纯机器对机器调用（API key + HTTPS 足够）；无服务端状态时选 JWT（实验 10）。

| 方案 | 服务端状态 | 吊销 | 适用 |
|---|---|---|---|
| 服务端会话（本篇） | 有（Redis） | 删除即吊销，即时 | 需要登出/踢人的业务 |
| JWT（实验 10） | 无 | 难（黑名单=退回有状态） | 跨服务、无中心存储 |

## Quick Start

前置条件：Redis 容器（建议带 AOF 持久化，验收③需要重启存活）；Go ≥ 1.22。运行：

```bash
docker run -d --name marklet-redis -p 16379:6379 \
  redis:7-alpine redis-server --appendonly yes
cd web/09_auth_password_session
go run .
go test -race -count=3 .
```

真实输出（`go run .`，服务 127.0.0.1:19618，跑完自动退出）：

```text
== 第 1 步：盐随机性——同密码两次哈希必然不同 ==
  hash1 = $argon2id$v=19$m=32768,t=2,p=2$5egpbC6L6Z06fZT...
  hash2 = $argon2id$v=19$m=32768,t=2,p=2$onQpgoA1GEUBAyx...
  两次哈希相同 = false；各自验证通过 = true / true；错误密码 = false
== 第 2 步：弱密码策略——逐条拒绝 ==
  "12345678"     → 拒绝：未包含字母
  "abcdefgh"     → 拒绝：未包含数字
  "a1"           → 拒绝：长度不足 8 位
  "Passw0rd1"    → 通过
  "short1"       → 拒绝：长度不足 8 位
== 第 3 步：会话固定防御——登录轮换 ==
  登录前 SID ee9c7e23fe6d955b...，登录后 893766db342d25a6...——必然不同
  旧 SID 查询 → no session；新 SID → user=alice fixed=true
== 第 4 步：全流程走真服务（Cookie 全程贯通）==
  注册   → 201 {"status":"registered"}
  登录   → 200（Set-Cookie 签发会话 SID）
  me     → 200 {"login_at":"2026-10-09T00:59:00.821673Z","rotated":true,"user":"alice"}
  登出   → 200（服务端会话已删）
  me     → 401 {"error":"未登录"}（SID 已作废）
```

验收③的实录——重启 Redis，会话凭 AOF（追加持久化文件，重启后重放写命令）存活：

```text
$ docker exec marklet-redis redis-cli SET sess:restart-proof '{"user_id":"alice"}' EX 3600
OK
$ docker restart marklet-redis
$ docker exec marklet-redis redis-cli GET sess:restart-proof
{"user_id":"alice"}
$ docker exec marklet-redis redis-cli TTL sess:restart-proof
3591
```

测试验收（`-race` 连跑 3 轮全绿，含 goleak 门禁）：

```text
ok  	marklet/09_auth_password_session	16.822s
```

诚实预期：用户表是内存 map（实验 12 才接 PG）；TTL 恢复依赖 AOF 重放精度，秒级误差正常。

## How It Works

### 盐：让每个哈希都是孤本

盐是每密码 16 字节的 crypto/rand 随机数，编进 PHC 串。作用不是保密（明文可见），
而是让"同一密码 → 不同哈希"——预计算表（彩虹表）失效，攻击者只能逐条爆破。
验证时从串解析参数与盐重算，恒定时间比较（`subtle.ConstantTimeCompare`，
耗时与差异位置无关，杜绝计时侧信道）。

### 登录轮换：会话固定的结构性防御

攻击三步：拿到一个合法 SID → 诱导受害者浏览器带着它登录 → 服务端沿用该 SID，
攻击者冒充受害者。防御不需要识别攻击，只需一条铁律：登录成功的瞬间销毁旧 SID、
签发全新 SID（login handler 的前 3 行），攻击者手里的旧牌当场作废。

### 过期即删除：Redis TTL 承包生命周期

会话写入带 24 小时 TTL，登出即 DEL，过期管理零代码（Redis 惰性+定期删除）。
AOF 让重启不丢会话：写命令追加落盘、重启重放，TTL 一并恢复。

## Pitfalls & Q&A

- **盐用 math/rand 生成**：序列由种子决定可预测。解法：crypto/rand（本篇实现）
- **失败文案区分"用户不存在"**：等于帮攻击者枚举用户名。解法：统一文案 + no-user 也做一次哈希拉平耗时
- **登录不轮换 SID**：现象是会话固定攻击全盘成立。解法：登录即销毁旧 SID、
  签发新 SID（有测试断言 newSID != oldSID）
- **会话 Cookie 缺 HttpOnly**：现象是任意 XSS 脚本能读走 SID。
  解法：SetCookie 时显式 HttpOnly + SameSite=Lax（测试断言）

**Q：为什么内存困难对密码哈希如此重要？**
A：SHA-256 快到 GPU 每秒数十亿次，字典攻击成本趋近零；argon2id 把单次哈希的
内存需求拉到几十 MB，GPU 的并行优势被内存带宽掐死，爆破回到"真金白银"。

**Q：会话和 JWT 怎么选？**
A：看"吊销"是不是硬需求。会话吊销 = 删一个 key，即时生效；JWT 无状态但吊销
要黑名单——黑名单就是状态，等于退回会话。marklet 要登出，会话是本篇答案，
JWT 的取舍在实验 10 展开。

**Q：用户表为什么是内存 map？**
A：刻意的教学切片——本篇增量是哈希与会话，用户表持久化归实验 12（owner_id
落 PG）。每个实验只引入一层新概念，是本系列的编排纪律。

**Q：参数 m=32MB、t=2 会不会太弱？**
A：RFC 9106 首推 m=2GiB，但登录会秒级耗时。32MB/2 轮在 2026 年仍是合理档位；
PHC 串自描述让未来调档零迁移——老哈希在下次登录时透明重哈希。
