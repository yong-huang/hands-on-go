#!/usr/bin/env bash
# 本地 CI：fmt → vet → race 测试 → 构建。与 GitHub Actions 等价的五步门禁
#（goleak 内嵌在各包 TestMain，随 -race 一起执行）。
# 用法：./scripts/ci.sh（在 web/ 根执行）
set -uo pipefail
cd "$(dirname "$0")/../.."

fail=0
echo "=== 1/5 gofmt（格式不一致即失败）==="
unformatted=$(gofmt -l . )
if [ -n "$unformatted" ]; then echo "未格式化: $unformatted"; fail=1; else echo "OK"; fi

echo "=== 2/5 go vet ==="
go vet ./... || fail=1

echo "=== 3/5 go test -race（含 goleak 门禁）==="
go test -race -count=1 ./... || fail=1

echo "=== 4/5 go build（全部可执行）==="
go build -o /dev/null ./... || fail=1

echo "=== 5/5 摘要 ==="
if [ "$fail" = 0 ]; then echo "CI 全绿"; else echo "CI 失败"; exit 1; fi
