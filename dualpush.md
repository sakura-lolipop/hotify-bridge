# dualpush.md — 双推方案 + 镜像构建方法

> hotify-bridge **维护者参考**：代码怎么同时推 GitHub + Gitee、Docker 镜像怎么构建发布。
> 用户部署看 [README](./README.md) / [docker.md](./docker.md)；Gitee 二进制镜像看 [gitee.md](./gitee.md)。

---

## 1. 代码双推（GitHub + Gitee 同步）

### 为什么
终端用户在国内（GFW），GitHub 被墙——代码 / 文档 / release 国内只能从 Gitee 拉。只推 GitHub = 国内用户拿旧的 / 空的。**双推保两边同步**。

### 方案：origin 配双 push URL
`origin` remote 配两个 push URL（GitHub + Gitee），`git push origin <分支|tag>` 一次推两边。

**配置（本机一次性）**：
```bash
cd hotify-bridge
git remote set-url --add --push origin https://github.com/sakura-lolipop/hotify-bridge.git
git remote set-url --add --push origin https://gitee.com/sakura-lolipop/hotify-bridge.git
# 验证：git remote -v 应看到 origin 有 2 行 push（github + gitee）
```

### 关键纪律
- **dual-push 是本机 origin 的本地配置，不在 git 里** → 新克隆的副本没有。新机器上要么重配（上面两条 `set-url`），要么手动分别推 github / gitee。
- **commit + tag 都双推**：`git push origin main`、`git push origin v1.1.3` 都自动双发。
- **推前核**：`git remote -v` 看 origin 是不是 2 行 push。掉一条 = 只推了一边。
- **github 偶发 reset**：`git push` 偶尔 `Connection reset`（GFW 抖），重试 2-3 次通常成；重试后核两边都成（github 成了 gitee 没成的情况要单独补 gitee）。

### CI 兜底：`.github/workflows/mirror-to-gitee.yml`
本机 dual-push 之外的兜底（别的机器 push / GitHub 网页改 / PR 合并）：
- **main push** → 镜像 main 到 Gitee（`if: github.ref == 'refs/heads/main'`）。
- **tag push** → 镜像 tags（`if: startsWith(github.ref, 'refs/tags/')`）——**不在 tag 上强推 main**（tag 的 HEAD 不是 main tip，强推会回退 Gitee main）。
- 用 `GITEE_USER` / `GITEE_TOKEN` secret。
- `workflow_dispatch` → 镜像 main（手动补同步）。

> 本机 dual-push + CI mirror 对本机 push 是**幂等重确认**（gitee 结果不变，仅多一次 run）。

---

## 2. Docker 镜像构建 + 发布

### 镜像（多架构 amd64+arm64）
两个 registry：

| Registry | 地址 | 受众 | 鉴权 |
|---|---|---|---|
| **GHCR** | `ghcr.io/sakura-lolipop/hotify-bridge` | 海外 | `GITHUB_TOKEN`（CI 自带，免配） |
| **阿里云 ACR** | `crpi-gi2hyqoir87c0lus.cn-hangzhou.personal.cr.aliyuncs.com/sakura-lolipop/hotify-bridge` | 国内（ghcr 被墙） | `ACR_USERNAME` / `ACR_PASSWORD`（repo secret） |

tag 规则（`docker/metadata-action`）：`v1.1.3` → `:1.1.3` / `:1.1` / `:latest` / `:sha-xxxxxxx`。

### Dockerfile（多阶段 + buildx 多架构）
- **builder**（`--platform=$BUILDPLATFORM golang:1.22-alpine`）：永远在 runner 原生架构跑，`GOOS=$TARGETOS GOARCH=$TARGETARCH` 交叉编译目标架构（**免 QEMU 编 Go，多架构不慢**）。`GOPROXY=goproxy.cn`（`proxy.golang.org` 国内被墙，`goproxy.cn` 国内可达 + 全球可达）。
- **runtime**（`alpine:3.20`）：`ca-certificates`（出站 HTTPS/wss 连 Gotify + 推送服务要校验证书）+ `tzdata`。`WORKDIR /data`（状态文件落挂载点）。`EXPOSE 8080`。
- `ARG TARGETOS=linux TARGETARCH=amd64`（**默认值**：本地非 buildx 的 `docker build .` 也成立；buildx 自动覆盖注入目标架构）。

### CI：`.github/workflows/docker-release.yml`
打 `v*` tag → 两个**并行** job：
- **GHCR job**：buildx 多架构构建 → 推 GHCR（`push: github.event_name == 'push'`）。
- **ACR job**：同款 buildx 构建 → 推 ACR（`push` 同样 event-gated）。ACR 凭证不对时 ACR job 自己红，**GHCR 不受影响**（独立 job 隔离）。
- **`workflow_dispatch`**：两边都**只构建不推**（纯烟测；要发版打 tag，免污染国内 `:latest`）。

### ACR 个人版要点（实测）
- 域名是**新版** `crpi-<rand>.cn-<region>.personal.cr.aliyuncs.com`（非老版 `registry.cn-xxx.aliyuncs.com`）。
- **代码源构建只单架构**（多架构是企业版功能）；但能**接收外部 buildx 推的多架构 manifest 并对外提供**（实测 amd64+arm64 都在）——所以构建在 CI（buildx）做，ACR 只当仓库。
- 仓库必须**先在控制台建好**（个人版不自动建仓，CI 首推会被拒）。
- GHCR 首推默认 **private**；要海外用户拉，到 GitHub → Packages → `hotify-bridge` → Package settings 改 Public（ACR 建仓时直接选公开）。

### 本地构建（不靠 CI）
```bash
# 单架构（默认 linux/amd64，ARG 默认值兜底，普通 docker build 就行）：
docker build -t hotify-bridge:dev .

# 多架构（需 buildx）：
docker buildx build --platform linux/amd64,linux/arm64 -t hotify-bridge:dev .
```

### 用户更新（compose）
```bash
docker compose pull && docker compose up -d
```
`./data` 卷保留配置 / 设备 token / 水位（`last_msg_id`）→ **不重配、更新不触发全量重放**（水位落盘了）。

---

## 相关
- 发版流程总览：[task.md](./task.md)「写代码必读 / 下次发版流程」。
- Gitee Release 二进制镜像（**CI 不传，本地 `scripts/gitee-upload.sh`**）：[gitee.md](./gitee.md)。
- 踩过的坑（secrets 不能进 if / action 无 timeout / cf-fetch 国内不稳 / Gitee 境外 CI 上传挂死 等）：[bug.md](./bug.md)。
