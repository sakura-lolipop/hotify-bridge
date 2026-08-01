# Docker 部署 hotify-bridge

> 本文档专讲怎么用 Docker / NAS 跑起 hotify-bridge（Gotify → 华为 Push Kit 转发桥）。运行原理、配置项全集见 [README](./README.md) / [BRIDGE.md](./BRIDGE.md)。
>
> App「设置」里的"部署指引"链的就是这篇。打不开是 GitHub 被墙——用 Gitee 镜像：`gitee.com/sakura-lolipop/hotify-bridge/blob/main/docker.md`。

## 🐳 docker compose（推荐 · SSH-light）

最省心、最不碰 SSH。配置走 **docker-compose.yml 的 environment 两行**（直接编辑文件填值,CLI+GUI 都生效,不依赖 .env);NAS 上还能在 Container Manager 图形界面粘 compose,**全程不开 SSH**。

1. 拿 `docker-compose.yml`（仓里:[Gitee](https://gitee.com/sakura-lolipop/hotify-bridge)）。
2. 编辑 `docker-compose.yml` 的 `environment:` 两行,填 Gotify 地址 + token（注释里有地址怎么填）:
   ```yaml
   environment:
     GOTIFY_HTTP_URL: https://gotify.你域名.com   # 或 http://gotify容器名:端口 / http://host.docker.internal:端口
     GOTIFY_CLIENT_TOKEN: 你的gotify_client_token
   ```
3. `docker compose up -d`。

完事——桥读 env 自动配好 gotify;`cloud_function_urls` 不用填（v1.1.2+ 自动从 Gitee fetch）。改配置改 compose 这两行再 `up -d`。**全程不 SSH 编辑任何配置文件。**

> NAS GUI:Container Manager「项目」粘 `docker-compose.yml` 内容（environment 两行已填好）,不开 SSH。

## ⚡ 一键脚本 install.sh（可选，SSH 一条命令）

SSH 用熟、想一条命令搞定的：拉 `install.sh` + bash 跑，问俩问题自动起。**会 SSH 跑远程脚本才用这个**——谨慎的（不想 SSH 跑脚本）走上面的 compose。

```bash
curl -fsSL https://gitee.com/sakura-lolipop/hotify-bridge/raw/main/install.sh -o install.sh && bash install.sh
```

> 下面是手动 / 细节，compose 或脚本跑通的可跳过。

## 镜像（多架构 amd64+arm64，已实测）

`docker pull` 会按你的 CPU 自动选架构（Intel 服务器/AMD NAS 拉 amd64，树莓派/ARM NAS 拉 arm64）。两个 registry 二选一：

| Registry | 镜像地址 | 用谁 |
|---|---|---|
| **阿里云 ACR（国内推荐）** | `crpi-gi2hyqoir87c0lus.cn-hangzhou.personal.cr.aliyuncs.com/sakura-lolipop/hotify-bridge` | 国内（ghcr.io 被墙拉不动） |
| GHCR（海外） | `ghcr.io/sakura-lolipop/hotify-bridge` | 海外 / 能连 ghcr.io |

> 国内用户认准 **ACR** 那个长地址。GHCR 在国内基本拉不动。

## 快速开始

```bash
# 国内（ACR）：
docker pull crpi-gi2hyqoir87c0lus.cn-hangzhou.personal.cr.aliyuncs.com/sakura-lolipop/hotify-bridge:latest

docker run -d --name hotify-bridge --restart unless-stopped \
  -p 8080:8080 -v "$PWD/data:/data" \
  crpi-gi2hyqoir87c0lus.cn-hangzhou.personal.cr.aliyuncs.com/sakura-lolipop/hotify-bridge:latest
```

首启会在 `./data/bridge_config.yaml` 自动生成带注释的配置 → 编辑填 `gotify_token` + `cloud_function_urls` → `docker restart hotify-bridge`。

## docker-compose 补充

走 compose 看本文顶部「docker compose」节（`.env` 流程，免 SSH 编辑）。仓库自带的 [`docker-compose.yml`](./docker-compose.yml) 默认拉国内 ACR + 带 `environment:` 段（从 `.env` 读 gotify 配置）。

## 🔄 更新到新版（compose）

新版发布后，compose 用户更新就两条命令——数据卷 `./data` 保留，配置/token/水位都不丢，**不用重配**：

```bash
docker compose pull      # 拉最新 :latest 镜像
docker compose up -d     # 用新镜像重建容器（停旧换新）
```

- `./data` 卷（`bridge_config.yaml` / `push_tokens.json` / `last_msg_id`）跨重建保留 → 配置不动、设备 token 不掉、**高水位不丢（更新不会触发"全量重放"）**。
- 想固定某版本：把 `docker-compose.yml` 里镜像 tag 从 `:latest` 改成 `:1.1.2`（具体版本），再 `pull` + `up -d`。
- 清旧镜像（可选）：`docker image prune -f`。

## 配置（编辑挂载目录里的 `bridge_config.yaml`）

- **必填**：`gotify_token`（Gotify **CLIENT** token，WebUI→CLIENTS 建，不是 app token）+ `cloud_function_urls`（用 Hotify 托管默认即可，留空也行）。
- 其余有默认值或自动探测。完整字段见 [README 配置节](./README.md#-配置)。

## ⚠️ Gotify 地址（容器里和裸机不一样——最易踩的坑）

桥把「只填端口」当同机 `127.0.0.1`，但**容器里 `127.0.0.1` 是容器自己**，够不到 Gotify。所以 `bridge_config.yaml` 的 `gotify_url` 按你的拓扑填：

| Gotify 在哪 | 怎么填 `gotify_url` |
|---|---|
| 同机**另一容器** | 把桥和 Gotify 放同一 docker network，填 `http://<gotify 容器名>:<端口>`（如 `http://gotify:80`） |
| **宿主机**（非容器） | compose 放开 `extra_hosts: ["host.docker.internal:host-gateway"]`，填 `http://host.docker.internal:<端口>` |
| **远程 / 域名** | 直接填完整 `https://你的域名:<端口>`，无需特殊处理 |

> 别用"只填端口"的智能模式——那是给裸机同机 Gotify 的，容器里连不上。

## NAS 图形界面（Synology Container Manager / QNAP Container Station / Unraid）

1. 镜像：拉国内 ACR 地址（上面那个长的）。
2. 端口映射：宿主 `8080` → 容器 `8080`。
3. 卷映射：容器的 `/data` → 一个宿主目录（如 `/docker/hotify-bridge/data`）。**这步必须有**，否则配置和设备 token 随容器删丢。
4. 起来后，去那个宿主目录改 `bridge_config.yaml`，重启容器生效。

## 公网 HTTPS（公网部署必看）

`/register` 默认明文 HTTP——公网上报 push token 会裸奔。二选一：
- **反代终结 TLS**（推荐）：Caddy / Traefik / Nginx 在前面跑 HTTPS，反代到容器 `8080`。手机走 HTTPS 上报。
- **容器内 TLS**：`bridge_config.yaml` 配 `tls_cert_file` / `tls_key_file`，证书文件**挂进容器**（路径填容器内路径，如 `-v ./certs:/certs` 再填 `/certs/cert.pem`）。

## 架构说明

OCI image index（多架构清单），amd64 + arm64 两个条目都实测存在。NAS / 树莓派 / 服务器 `docker pull :latest` 自动拿对应架构，无需手动指定 `--platform`。

## 排错

- **拉不动 GHCR**：国内换 ACR 地址（上表）。
- **首启后 `/register` 要等 ~6s**：冷启动 fetch 一次 cloud_function_urls.txt（v1.1.2+ 走 **Gitee 首选 → ghproxy → raw 兜底**），稍等即监听 8080。
- **Gotify 连不上**：99% 是上面那个"容器 127.0.0.1"坑——按拓扑表填 `gotify_url`。
- 更多见 [BRIDGE.md](./BRIDGE.md)。
