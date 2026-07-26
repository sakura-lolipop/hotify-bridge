# task-archive.md — hotify-bridge 历史里程碑快照

> 完成的里程碑存档。进行中的看 [task.md](./task.md)。

## v1.1.1（待发，已提交 main）— 修重启后回补全量重放 bug

### 背景
用户反馈：桥"有时候（重启/重连）会把全部消息再推送一遍"。

### 根因
`lastMsgID`（高水位）只在内存、不落盘。桥**启动撞 Gotify 不可达**时 `initLastID()` 失败（取不到最新消息）→ 水位留 0 → 首次 `backfill()` 取最近 100 条、`id>0` 全中 → **全量重放**。（纯运行中重连不受影响——内存水位还在，只补真漏的。）

### 修
- **水位落盘**：`forward` 每推一条写 `last_msg_id`；`initLastID` 启动优先读落盘值续传 → 重启只补真漏的，不回放。
- **`backfill` 兜底**：水位=0（无落盘值 且 `initLastID` 没成）→ 设到本批最大 id、一条不推（宁可漏断档，绝不重放刷屏）。
- 测试：`TestInitLastIDRestoresPersisted` + `TestBackfillGuardZeroWatermark`（注册假设备+计数云函数，验 0 重放）。`go test` 全过。

### 待办
打 `v1.1.1` tag 发版（触发 go-release/docker-release）+ 本地 `scripts/gitee-upload.sh v1.1.1` 传 Gitee。

---

## v1.1.0（2026-07-26）— Docker 化 + 国内分发 + 小白文档

### Done
- **Docker 化**：多阶段 `Dockerfile`（buildx amd64+arm64，`GOPROXY=goproxy.cn` 让国内 `docker build` 能编——proxy.golang.org 被墙）；`docker-compose.yml`；`.dockerignore`（防 `go/bridge_config.yaml` 真 token 进镜像）；`docker.md` 部署指南；`install.sh` 一键（Docker 包装，问配置+预写+起容器，加 host-gateway）。
- **国内镜像（阿里云 ACR）**：`docker-release.yml` 发版自动多架构双推 GHCR（海外）+ 阿里云 ACR 个人版（国内，域名是新版 `crpi-xxx.cn-<region>.personal.cr.aliyuncs.com`，非老 `registry.cn-xxx`）。ACR 多架构对外提供**实测 OK**（amd64+arm64 manifest）。
- **Gitee 镜像**：`mirror-to-gitee.yml` 自动 GitHub→Gitee 代码镜像（文档/clone 国内可达）；Release 二进制走 `scripts/gitee-upload.sh` **本地传**（境外 CI 传 Gitee 又慢又挂，改本地秒传）。
- **README 拆分**：`README.md` 小白一步步教程 + `README_FULL.md` 详细技术（顶部互链，**以后一起更新**）。平台段改"下 Go 二进制直接跑"为主（Docker 退为可选）。加 acme.sh HTTPS、分平台表。
- **发版 v1.1.0**：tag 触发 go-release（5 二进制）+ release（Python）+ docker-release（GHCR+ACR 镜像）三个 workflow。Gitee Release v1.1.0 本地传齐 5 个 Go 二进制（linux amd64/arm64、windows、darwin amd64/arm64）。

### 踩过的坑（别再踩）
- **`secrets.*` 不能用在 step/job 级 `if`** → GitHub 解析报 `Unrecognized named-value: 'secrets'`，整 workflow 0s 挂。条件隔离靠**拆独立 job**，不是 if-secrets。
- **ACR 个人版代码源构建只单架构**（多架构是企业版功能，官方文档坐实）；但能**接收外部 buildx 推的多架构 manifest 并对外提供**（实测）——构建 vs 存储服务两回事。
- **Gitee Release 从境外 CI 上传 `attach_files` 又慢又 stall**：第三方 `action-gitee-release` 的 `requests` 没 `timeout`，TCP stall 永久挂、retry 救不了（retry 只接异常不接挂起）。→ 本地（国内机）传，秒级稳。
- **`ghcr.io` / GitHub Releases / `raw.githubusercontent.com` 国内全被墙** → 文档走 Gitee，镜像走 ACR，运行时 fetch 走 ghproxy 兜底。
- **git 用的凭证（钥匙串）可能是账号密码**，Gitee release API 只认**个人访问令牌**不认密码（API 401）。本地传 release 必须用个人令牌。
