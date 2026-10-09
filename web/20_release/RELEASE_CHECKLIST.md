# marklet 发布清单（Release Checklist）

> 每次发布前逐项勾选。前三节是自动化门禁（ci.sh / verify.sh / loadtest 覆盖），
> 后两节必须人工过一遍——自动化能拦的是回归，拦不住的是决策。

## 1. 代码质量（ci.sh 自动）
- [ ] `gofmt -l .` 零输出（格式统一）
- [ ] `go vet ./...` 零告警
- [ ] `go test -race ./...` 全绿（含 goleak 泄漏门禁）
- [ ] `go build ./...` 全部可执行

## 2. 交付物（verify.sh 自动）
- [ ] `docker compose up` 一条命令起栈，/healthz 通过
- [ ] 容器内 `whoami` 为非 root（marklet）
- [ ] 运行镜像 ≤ 50MB（多阶段构建生效；实测 28MB）
- [ ] `down → up` 后数据仍在（卷持久化）
- [ ] `stop_grace_period` 与排空预算对齐（实验 18 的 5s + 余量）

## 3. 性能基线（loadtest 实测，与基线对比）
- [ ] 100 并发 × 60s：成功率 ≥ 99.9%
- [ ] p99 不劣于基线的 1.5 倍（基线见 README 性能小节，重测后更新）
- [ ] 吞吐不劣于基线的 0.7 倍
- [ ] 压测期间错误日志无新增类别（13 的 JSON 日志 grep）

## 4. 安全（人工，逐条对照实验 09-12/16）
- [ ] 无新增 SQL 拼接（全部参数化）
- [ ] 新端点都有 owner 过滤（IDOR）与认证中间件覆盖
- [ ] 敏感字段未进日志（LogSafe 出口 + grep 抽查）
- [ ] 限流维度/阈值经确认（变更需注明理由）
- [ ] JWT 密钥来自配置而非硬编码；算法白名单仍在

## 5. 发布与回滚（人工）
- [ ] 镜像 tag 与 git commit 对应（不可变交付物）
- [ ] 迁移可重复执行（幂等），且与 schema.sql 对齐（实验 06 纪律）
- [ ] 回滚方案：上一版镜像 + 数据卷兼容（迁移需向后兼容一版）
- [ ] 停机预算已告知编排层（K8s terminationGracePeriod / compose stop_grace_period）
- [ ] 发布窗口内有人盯 14 的指标面板（p99 与错误率）
