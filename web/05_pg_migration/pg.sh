#!/usr/bin/env bash
# marklet 实验用 PostgreSQL：postgres:15-alpine（本地镜像零拉取），端口 15432。
# 用法：./pg.sh start | stop
set -euo pipefail

case "${1:-start}" in
  start)
    if docker ps --format '{{.Names}}' | grep -q '^marklet-pg$'; then
      echo "marklet-pg 已在运行（127.0.0.1:15432）"
      exit 0
    fi
    docker rm -f marklet-pg >/dev/null 2>&1 || true
    docker run -d --name marklet-pg -p 15432:5432 \
      -e POSTGRES_PASSWORD=marklet -e POSTGRES_USER=marklet -e POSTGRES_DB=marklet \
      postgres:15-alpine >/dev/null
    for _ in $(seq 1 30); do
      if docker exec marklet-pg pg_isready -U marklet >/dev/null 2>&1; then
        echo "marklet-pg 就绪（127.0.0.1:15432）"
        exit 0
      fi
      sleep 1
    done
    echo "marklet-pg 30s 内未就绪" >&2
    exit 1
    ;;
  stop)
    docker rm -f marklet-pg >/dev/null
    echo "marklet-pg 已移除"
    ;;
  *)
    echo "用法: $0 start|stop" >&2
    exit 1
    ;;
esac
