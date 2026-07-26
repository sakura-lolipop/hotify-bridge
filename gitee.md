# Gitee 镜像（国内可达）

> GitHub（代码 + Releases + ghcr 镜像 + raw）国内被墙，终端用户摸不到。**Gitee 是国内可达的镜像**，承载两样：① 代码镜像 ② Release 二进制镜像。Docker 部署看 [docker.md](./docker.md)。

## ① 代码镜像（自动）

`.github/workflows/mirror-to-gitee.yml`：GitHub 一 push（main / tag）→ 自动同步到 `gitee.com/sakura-lolipop/hotify-bridge`。
- 用 `GITEE_USER` / `GITEE_TOKEN` 两个 GitHub secret 鉴权。
- 国内用户 clone / 看 README / docker.md 都走 Gitee（`gitee.com/sakura-lolipop/hotify-bridge`）。

> 本机 push 也走 dual-push（`origin` 配了 GitHub + Gitee 双 push URL），`git push origin` 一次推两边。

## ② Release 二进制镜像（手动本地传）

**为什么手动不在 CI**：境外 CI runner 传 6MB 二进制到 Gitee 跨太平洋又慢又 stall（试过第三方 action，它 `requests` 没 timeout，TCP stall 永久挂、retry 救不了）。**本地（国内机）直传秒级、稳。**

每次发版后跑：

```bash
GITEE_TOKEN=你的私人令牌 bash scripts/gitee-upload.sh v1.1.0
```

脚本干的事：编 `go/dist/` → 删旧 release → 建新 → 传 5 个 Go 二进制（每个 curl 带 `--max-time`/`--retry`）。详见 [scripts/gitee-upload.sh](./scripts/gitee-upload.sh)。

## 国内用户怎么下

- **二进制**：`gitee.com/sakura-lolipop/hotify-bridge/releases` → 选 tag → 下对应平台包（`gotify-bridge-<os>-<arch>`）。
- **源码 / 文档**：`gitee.com/sakura-lolipop/hotify-bridge`（clone 或直接看 README / docker.md）。

## 令牌管理

- **存哪**：GitHub repo secret `GITEE_USER` + `GITEE_TOKEN`（mirror-to-gitee workflow 用）。
- **本地传用**：临时 `export GITEE_TOKEN=...`，传完可 `unset`。**别入 chat / commit / 截图**；露过立刻去 gitee→私人令牌删了重建。
- **令牌 vs 密码**：`git push` 用的凭证（系统钥匙串）可能是你的**账号密码**（Gitee 接受密码做 git 鉴权）；但 **release API 只认个人访问令牌、不认密码**（API 返 401）。所以本地传 release 必须用**个人令牌**（gitee.com → 头像 → 设置 → 私人令牌 → 生成，勾 `projects`）。
