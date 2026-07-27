# syntax=docker/dockerfile:1
# hotify-bridge 容器镜像 —— Go 桥（订阅 Gotify → 华为 Push Kit 转发）。
#
# 多阶段 + buildx 多架构：
#   - builder 用 --platform=$BUILDPLATFORM 永远在 runner 原生架构（amd64）跑，靠 GOOS/GOARCH 交叉编译目标架构
#     → 不用 QEMU 模拟跑 Go 工具链，多架构构建快（纯 Go CGO_ENABLED=0 静态产物）。
#   - runtime 用 alpine：带 ca-certificates（出站 HTTPS/wss 连 Gotify + 推送服务要校验证书）+ tzdata（日志时区）。
#
# 状态文件语义（关键）：桥在【CWD】读/写 bridge_config.yaml / push_tokens.json / subscribe_status.json。
#   WORKDIR 设 /data → 这 3 个文件落在 /data → 用 -v ./data:/data 挂卷持久化（首启自动生成 bridge_config.yaml）。
# 别改 WORKDIR 到只读路径；别把配置 COPY 进镜像（镜像应无菌，配置由挂载卷提供，避免 baked-in token）。

# ── builder：原生跑，交叉编译目标架构 ──
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder
# buildx 自动注入 TARGETOS / TARGETARCH（声明后即可用）
ARG TARGETOS=linux TARGETARCH=amd64   # 默认值：本地非 buildx 的 docker build 也成立（buildx 自动覆盖）
WORKDIR /src
# 只 COPY go/ 源码（go.mod / go.sum / *.go）；.dockerignore 已挡掉 go/dist、go/bridge_config.yaml、*.exe
COPY go/ ./
# GOPROXY：proxy.golang.org 国内被墙（实测 connection refused），用 goproxy.cn（七牛，全球可达 + 国内快），direct 兜底。
# 可 build-arg 覆盖：docker build --build-arg GOPROXY=https://proxy.golang.org,direct .
ARG GOPROXY=https://goproxy.cn,direct
# 参数同 go/build-all.sh：CGO_ENABLED=0 静态、-trimpath 去开发机路径、-ldflags="-s -w" strip 调试符号
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/hotify-bridge .

# ── runtime：最小 + CA 证书 ──
FROM alpine:3.20
# ca-certificates：Go 静态二进制不内嵌 CA 包，读系统 /etc/ssl/certs（连 Gotify wss + 推送服务 https 必需）
# tzdata：日志时间用本地时区（可选但便宜）
RUN apk add --no-cache ca-certificates tzdata
# 桥的 CWD = /data → 3 个状态文件落这里 → 挂卷持久化
WORKDIR /data
COPY --from=builder /out/hotify-bridge /usr/local/bin/hotify-bridge
# /register 监听端口；register_port 留空 → 默认 8080（容器内一般不改，靠映射宿主端口）
EXPOSE 8080
ENTRYPOINT ["hotify-bridge"]
