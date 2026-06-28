# hotify-bridge

🌐 **中文** | [English](README.en.md) · 📄 [更新日志](CHANGELOG.md)

> Gotify → 华为 Push Kit 转发桥，服务于 **[Hotify](#)** —— 一个 HarmonyOS NEXT 通知转发客户端。
> 订阅 Gotify 消息流，把每条消息经华为 Push Kit 推到你的鸿蒙**锁屏**——即使 App 没开也能收到。

```
[发送方] → Gotify（存储 + /stream）→ 【本桥】→ 华为 Push Kit v3 → 鸿蒙锁屏
```

这是 Hotify 的**服务端**那一半。鸿蒙客户端 App 在另一个（闭源）仓库。本桥**可自托管**——你把它跑在自己的 Gotify 实例旁边，通知只经过你自己掌控的基础设施。

## ✨ 它干什么
- 订阅 Gotify 的 `/stream`（WebSocket）收实时消息。
- 把每条消息转发到华为 Push Kit v3（`POST /v3/{project_id}/messages:send`），作为锁屏通知。
- 断线自动重连 + **回补**断开期间漏掉的消息（按 id 高水位去重——不重不漏）。
- 开 `POST /register` 接收 App 上报的 push token + Gotify 配置。
- 按 token 逐台投递，失效 token 自动清理（bark 式）。

## 🔗 与 Gotify 协作
本桥是原创 Python 代码，对接 [Gotify](https://github.com/gotify/server) 服务端（MIT）。它**不打包** Gotify——请单独运行 Gotify。Hotify 复用 Gotify 的协议、存储和流；只是最后一公里投递从 FCM 换成了华为 Push Kit。

## 📋 前置
- Python 3.8+
- 一个运行中的 [Gotify](https://github.com/gotify/server) 服务端（自托管）
- 一个开了 **Push Kit** 的华为 AGC 项目 + **服务账号**密钥（`private.json`，RSA）——见 [push-jwt-token](https://developer.huawei.com/consumer/cn/doc/harmonyos-guides/push-jwt-token)
- Python 依赖：`pip install websockets PyJWT cryptography`

## 🚀 快速开始
```bash
git clone <this-repo> hotify-bridge && cd hotify-bridge
pip install websockets PyJWT cryptography

# 1) Gotify CLIENT token（读消息 / 订阅 /stream）
#    Gotify WebUI → CLIENTS → Create Client → 复制 Token
#    （不是 app token——那个只能"发"消息）
# 2) 华为服务账号密钥 → 存成 private.json
#    华为开发者联盟 → 你的项目 → 服务账号 → 创建 → 下载 JSON

cp bridge_config.example.yaml bridge_config.yaml   # 然后填入你的值
python -u gotify_pushkit_bridge.py
```

## ⚙️ 配置
Gotify 配置读取优先级：**App 上报**（`POST /register`，持久化到 `bridge_config.yaml`）> **环境变量** > 无（`waiting for app`）。

| 位置 | 键 | 说明 |
|---|---|---|
| `bridge_config.yaml` | `gotify_url`、`gotify_token`（动态，App 上报）**+** `gotify_url_local`、`register_port`、`tls_cert_file`、`tls_key_file`（静态，部署者填） | 从 `.example` 复制。**gitignore——别提交真 token。** `register_port` 空 → 默认 25238；`tls_*` 空 → `/register` 走明文 http。 |
| 环境变量 | `GOTIFY_HTTP_URL`、`GOTIFY_CLIENT_TOKEN` | 仅动态 gotify 字段的 headless 兜底 |
| `private.json` | 华为服务账号（RSA 私钥） | AGC 下载。**gitignore。** 缺失 = "脊柱模式"（只订阅，跳过 Push Kit）。 |
| `push_tokens.json` | 设备 push token | App 上报自动管理。gitignore。 |

- **Gotify 地址智能模式**：`gotify_url` 只填端口（纯数字）→ 桥认为 Gotify 同机，连 `http://127.0.0.1:<端口>`（最快、免 TLS）。填完整地址 → 远程 Gotify（wss/https，需有效证书）。
- **`gotify_url_local`（同机覆盖）**：桥和 Gotify 同机、但 App 上报的是域名时，在此填 `https://127.0.0.1:<端口>`，桥**用它连**（覆盖域名）+ 自动跳过证书校验，免 NAT hairpin。留空 → 桥用 `gotify_url`。

## 🔧 两种运行模式
- **脊柱模式**（无 `private.json`）：订阅 Gotify `/stream` + 回补照常，但**跳过 Push Kit 投递**（日志 `⏭ skip`）。先验证 Gotify 链路用。
- **完整模式**（有 `private.json`）：端到端 → 鸿蒙锁屏。

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
