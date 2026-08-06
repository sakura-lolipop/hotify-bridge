# task.md — hotify-bridge 当前任务 + 写代码必读

> 历史里程碑看 [task-archive.md](./task-archive.md)。本文件保持精简：>300 行或里程碑完成时拆，历史归 task-archive.md。

## 当前状态
**v1.1.5 已发版**（tag v1.1.5 双推,CI 跑 go-release + docker-release）。本版累积:cooldown 退避熔断 + Python 归档(Go 唯一主线)+ 屎山快修 9 条 + urls 重排(push.hotify.love 主)+ **发布规范 C.1-C.6**(去 .env 怪文件→compose 直填 GUI 友好 / 砍 Python release / body 去重 / Gitee checklist / version 注入 / extra_hosts)。**Gitee 二进制待手动 `scripts/gitee-upload.sh v1.1.5`**。

v1.1.2 曾发版(去重重放 + cloud_function_urls Gitee-fetch);v1.1.1 短暂发布后移除。

**+ 2026-08-01（本地待 push + 待发版）**：
- Go 加 **cooldown 熔断**（退避 60→180→540→900s，cap 15min；翻 bug.md YAGNI 拒绝案）—— commit `acfe2a4`
- **Python 版归档** `archive/gotify_pushkit_bridge.py`（Go 唯一主线，不再 Python fallback）
- 屎山快修 9 条（#1 gotifyConnectURL 单一真相 / #2 WaitGroup / #6 fmt→log / #7 死参 / #10 别名 / #11 parseStrArray / #12 trimMatchingQuotes / #15 readSnippet / #3 pushHTTPTimeout）—— commit `a9ebca0`，go build+test 过
- 剩 cleanup（magic #9/#13/#3-4处 + 重构 #4/#5/#8）—— 桥定型，**留着不管（YAGNI）**

## 下一步 / 待办
- [x] **v1.1.1（去重重放修复）** ✅ 已发。
- [x] **v1.1.2** ✅ 已发（cloud_function_urls Gitee-fetch 修复 + 去重重放修复；GitHub Release + GHCR/ACR 镜像 + Gitee 二进制全齐）。
- [ ] **App「部署指引」外链改指 Gitee**：HotifyNEXT App 仓里那条链接，从 GitHub README 改成 `gitee.com/sakura-lolipop/hotify-bridge/blob/main/docker.md`（国内可达）。不在本仓改。
- [ ] **GHCR 翻 Public（可选/低优先）**：仅海外用户需要；Hotify 国内为主、ACR 已覆盖，可跳过，或干脆删 `docker-release.yml` 的 ghcr job 只留 ACR。
- [~] ~~Gitee 令牌轮换~~（用户选择不轮换——令牌在对话露过几次、风险已知，不再催）。
- [ ] **下次发版流程**（备忘）：改代码 → `git push origin`（dual-push 自动 GitHub+Gitee）→ 打 `v*` tag（触发 go-release/docker-release）→ **★ 必跑 `GITEE_TOKEN=新令牌 bash scripts/gitee-upload.sh vX.Y.Z` 传 Gitee 二进制**（CI 境外传 Gitee 挂死,只能本地;忘跑 → 国内用户只 GitHub Releases 拉不动）。

> 上面"下一步"是我按这轮收尾推断的清单，实际路线你改。

## 写代码必读（conventions）
- **Go 主线在 `go/`**（**Python 版已归档 `archive/gotify_pushkit_bridge.py`**，Go 唯一主线）。改桥逻辑改 `go/*.go`，跑 `cd go && go test ./...`。交叉编译 `bash go/build-all.sh`。
- **dual-push**：`origin` 配了双 push URL（GitHub + Gitee），`git push origin` 一次推两边。新克隆的副本没这配置——记得手动两边推或重配（`git remote set-url --add --push origin <gitee>`）。
- **发版 = 打 `v*` tag**：触发 `.github/workflows/{go-release,release,docker-release}.yml`。**Gitee 二进制不在 CI**（境外传不动）——手动 `scripts/gitee-upload.sh`。
- **国内分发铁律**：终端用户在国内（GFW），GitHub 系全墙。文档→Gitee，镜像→ACR，别依赖 ghcr/GitHub Releases/raw。运行时资源 fetch（`cloud_function_urls.txt`）：**Gitee raw 首选 → ghproxy → raw 兜底 + 重试**（见 `autodetect.go` `cfTxtSources`；纯 ghproxy/raw 对国内不稳，PandaSoos 踩过）。
- **README 两版同步**：改 `README.md`（小白）要同步 `README_FULL.md`（详细），别让两份漂移。
- **专项文档**：`docker.md`（Docker 部署）、`gitee.md`（Gitee 镜像）、`dualpush.md`（双推方案 + 镜像构建，维护者参考）、`BRIDGE.md`（运行手册深入）。各自独立维护。
- **机密**：`bridge_config.yaml` / `push_tokens.json` / `private.md` / 任何令牌**绝不入库**（`.gitignore` 已挡；提交前扫一眼 `git status`）。

---

## 2026-08-06 4xx 分流（cfGone）对齐 NEXT（commit `586802f` 双推 GitHub+Gitee）

配合 CF 改纯透传：`postToPushService` + enum 加 `statusCfGone`（401/404/200非JSON→recordCfFail 熔断），`statusCfDown`（400/413→只 fallback 不计 fail，CF 不背发送方锅）。`sendToHuawei` tail `if attemptStatus != statusCfDown { recordCfFail }`（retry 用尽/cfGone→熔断；cfDown→不熔断）。对齐 NEXT harmony.go 同款分流。build/vet/test 绿。详见 docs/PushKitAPI.md。
