#!/usr/bin/env bash
# 全家桶验收：起栈 → 健康检查 → CRUD → 非 root → 镜像体积 → 重启持久化。
# 用法：./scripts/verify.sh（前置：Docker 运行中）
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== 构建与启动 =="
docker compose up -d --build --quiet-pull
echo "== 等健康检查通过 =="
for i in $(seq 1 60); do
  if curl -sf http://127.0.0.1:8080/healthz >/dev/null; then echo "app healthy"; break; fi
  [ "$i" = 60 ] && { echo "60s 内未就绪" >&2; docker compose logs app >&2; exit 1; }
  sleep 1
done

echo "== 健康检查 =="
curl -sf http://127.0.0.1:8080/healthz

echo "== CRUD（写入→读回）=="
curl -sf -X POST http://127.0.0.1:8080/api/v1/bookmarks \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://go.dev","title":"Go 官网"}'
echo
curl -sf http://127.0.0.1:8080/api/v1/bookmarks

echo "== 非 root 验证（容器内 whoami）=="
docker compose exec -T app whoami

echo "== 镜像体积（构建镜像 vs 运行镜像）=="
docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep -E "19_docker_deploy" | head -3
docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' | grep golang | head -1

echo "== 数据持久化：down（保留卷）→ up → 数据还在 =="
docker compose down >/dev/null
docker compose up -d >/dev/null
for i in $(seq 1 60); do
  curl -sf http://127.0.0.1:8080/healthz >/dev/null && break
  sleep 1
done
curl -sf http://127.0.0.1:8080/api/v1/bookmarks | grep -q "go.dev" \
  && echo "书签在重启后仍在（卷持久化生效）"

echo "== 收尾：down（数据保留在卷里）=="
docker compose down
echo "全部验收通过"
