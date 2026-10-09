# 多阶段构建：构建镜像（SDK）与运行镜像（纯运行时）分离——
# 运行镜像不含源码、模块缓存与工具链，体积从 ~1GB 级降到两位数 MB。
FROM golang:1.26 AS build
WORKDIR /src
# Go 模块缓存优先：依赖清单不变时，源码变更不触发重新拉取
# 模块代理：容器内默认官方源（goproxy 未配置），显式指向可达源
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
# 构建上下文是 web/（compose 里指定），只拷贝本实验的 app 源码
COPY 19_docker_deploy/app ./app
# CGO_ENABLED=0：纯静态二进制，scratch/alpine 皆可跑
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w" -o /out/marklet ./app

FROM alpine:3.20
# 非 root：运行镜像里的进程没有任何特权
RUN adduser -D -u 10001 marklet
COPY --from=build /out/marklet /usr/local/bin/marklet
USER marklet
EXPOSE 8080
# 健康检查：容器编排不依赖外部探活脚本
HEALTHCHECK --interval=3s --timeout=2s --retries=10 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/marklet"]
