# hotify-bridge

🌐 **中文** | [English](README.en.md) · 📄 [更新日志](CHANGELOG.md)

> Gotify → 华为 Push Kit 转发桥，服务于 **Hotify** —— HarmonyOS NEXT 通知转发客户端（App 上架后这里加应用市场链接）。
> 订阅 Gotify 消息流，把每条消息经华为 Push Kit 推到你的鸿蒙**锁屏**——即使 App 没开也能收到。

```
[发送方] → Gotify（存储 + /stream）→ 【本桥】→ 华为 Push Kit v3 → 鸿蒙锁屏
```

这是 Hotify 的**服务端**那一半。鸿蒙客户端 App 在另一个（闭源）仓库。本桥**可自托管**——你把它跑在自己的 Gotify 实例旁边，通知只经过你自己掌控的基础设施。

> 📌 **术语**：本文档里的「桥 / bridge」就是这个服务端程序；App「设置」里它叫**「Hotify 推送服务」**（可选字段）——同一个东西，两个叫法。

## ✨ 它干什么
- 订阅 Gotify 的 `/stream`（WebSocket）收实时消息。
- 把每条消息转发到华为 Push Kit v3（`POST /v3/{project_id}/messages:send`），作为锁屏通知。
- 断线自动重连 + **回补**断开期间漏掉的消息（按 id 高水位去重——不重不漏）。
- 开 `POST /register` 接收 App 上报：push token（每次刷新）+ Gotify 配置（**首次上报锁定**，之后忽略——防公网抢首注改后端）。
- 按 token 逐台投递，失效 token 自动清理（bark 式）。

## 🔗 与 Gotify 协作
本桥 **Go 重写（CP0-CP5，纯 Go 静态二进制，免运行时依赖）**，Python 版（`gotify_pushkit_bridge.py`）留作 fallback。对接 [Gotify](https://github.com/gotify/server) 服务端（MIT），**不打包** Gotify——请单独运行 Gotify。Hotify 复用 Gotify 的协议、存储和流；只是最后一公里投递从 FCM 换成了华为 Push Kit。

## 📋 前置
- **Go 桥（主线）**：预编译二进制（免依赖）或 Go 1.22+ 源码编译
- 一个运行中的 [Gotify](https://github.com/gotify/server) 服务端（自托管）
- 推送服务入口 `cloud_function_urls`：默认用 Hotify 托管函数（无需自建）；自部署见 `CloudFuction/PushKit.md`（private 锁云函数、不入桥）
- **Python fallback（可选）**：Python 3.8+ + `pip install websockets`（跑 `gotify_pushkit_bridge.py`；Go 不可用时备选）

## 🚀 快速开始
```bash
# 方式 A（推荐）：下载预编译二进制（Releases，免依赖）
#   gotify-bridge-<os>-<arch> → 运行：
./gotify-bridge   # 首启自动生成 bridge_config.yaml（默认值 + 注释）

# 方式 B：源码编译（Go 1.22+）
git clone <this-repo> hotify-bridge && cd hotify-bridge/go
go build -o gotify-bridge .          # 单平台；或 bash build-all.sh 出全平台 dist/
./gotify-bridge                       # 首启自动生成 bridge_config.yaml

# 1) Gotify CLIENT token（读消息 / 订阅 /stream）
#    Gotify WebUI → CLIENTS → Create Client → 复制 Token
#    （不是 app token——那个只能"发"消息）
# 2) 推送服务入口 cloud_function_urls：用 Hotify 托管函数（默认）或填自托管的
#    （private 锁云函数、不入桥——见 repourl.md / CloudFuction/PushKit.md）

# → 编辑 bridge_config.yaml：必填 gotify_token + cloud_function_urls，重启

# Python fallback（Go 不可用）：pip install websockets && python -u gotify_pushkit_bridge.py
```

## ⚙️ 配置
Gotify 配置 = **first-set wins**（照 SSH 主机指纹 TOFU）：桥【未配置】时收 App 首次 `POST /register` 上报（持久化到 `bridge_config.yaml` = 锁定），【已配置】后 App 再发的 gotify 一律忽略——**防公网攻击者抢首注把后端改成他的 Gotify**。要零赛跑：在 yaml 直接预填 gotify（桥启动即锁）。env 仅启动兜底；push token 则每次都刷新。

| 位置 | 键 | 说明 |
|---|---|---|
| `bridge_config.yaml` | `gotify_url`、`gotify_token`（**首次 App 上报锁定** / 或 yaml 预填）**+** `gotify_url_local`、`register_port`、`tls_*`（静态）**+** `cloud_function_urls`、`cloud_function_token`（推送服务入口） | 首启自动生成（无 example 模板）。**gitignore——别提交真 token。** `register_port` 空 → 默认 8080；`tls_*` 空 → `/register` 走明文 http；`cloud_function_urls` 空 → 跳过推送（只订阅 Gotify）。 |
| 环境变量 | `GOTIFY_HTTP_URL`、`GOTIFY_CLIENT_TOKEN` | 仅动态 gotify 字段的 headless 兜底 |
| （private 已移出桥） | — | private 锁在**云函数**（Netlify），桥不含 → 桥可开源。见 `repourl.md` / `CloudFuction/PushKit.md`。 |
| `push_tokens.json` | 设备 push token | App 上报自动管理。gitignore。 |

- **Gotify 地址智能模式**：`gotify_url` 只填端口（纯数字）→ 桥认为 Gotify 同机，连 `http://127.0.0.1:<端口>`（最快、免 TLS）。填完整地址 → 远程 Gotify（wss/https，需有效证书）。
- **`gotify_url_local`（同机覆盖）**：桥和 Gotify 同机、但 App 上报的是域名时，在此填 `https://127.0.0.1:<端口>`，桥**用它连**（覆盖域名）+ 自动跳过证书校验，免 NAT hairpin。留空 → 桥用 `gotify_url`。

## 🔧 两种运行模式
- **只订阅模式**（`cloud_function_urls` 未配）：订阅 Gotify `/stream` + 回补照常，但**跳过 Push Kit 投递**（日志 `⏭ 跳过推送`）。先验证 Gotify 链路用。
- **完整模式**（`cloud_function_urls` 配了）：端到端 → 鸿蒙锁屏。

## 🚢 生产拓扑
把 **Gotify + 桥放一台主机**；各自 serve HTTPS，**共用同一张证书**（各自端口）。手机走 HTTPS 触达两者；桥也走 HTTPS 连 Gotify——同一域名，证书校验通过。

```
   手机  ──https──▶ Gotify   https://你的域名:<你的端口>
         ──https──▶ 桥        https://你的域名:25238   (/register——上报 push token)
   桥    ──https──▶ Gotify   https://你的域名:<你的端口>   (同证书同域名)
```

- **一张证书喂两个服务。** Gotify 在 `config.yml`（`ssl.enabled` + 证书/私钥路径）加载；桥用 `bridge_config.yaml` 的 `tls_cert_file` / `tls_key_file` 指**同一份文件**。证书签一次，两边都指过去。
- **桥没配证书 → 走明文 http**（仅 LAN/调试）。任何公网部署都得配——否则手机上报的 push token 走明文。
- **桥的"Gotify 地址"** = 完整 HTTPS URL（`https://你的域名:<你的端口>`），别用"只填端口"的智能模式（那假设 Gotify 是明文 http，TLS 开了连不上）。
- **桥和 Gotify 同机？** 填 `gotify_url_local: https://127.0.0.1:<端口>`，桥走 localhost 直连（跳过证书校验），绕开公网域名 / NAT hairpin。

## 📖 更多
完整运行手册、故障排查、Push Kit 鉴权深入：见 [`BRIDGE.md`](./BRIDGE.md)。

## 📄 许可证
MIT。本桥是原创代码；与 [Gotify](https://github.com/gotify/server)（MIT，© 其作者）互操作，但不包含 Gotify 源码。
