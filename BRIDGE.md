# Hotify 桥（gotify_pushkit_bridge.py）运行手册

> Gotify ↔ 华为 Push Kit 转发桥。订阅 Gotify `/stream`，把新消息经华为 Push Kit v3 推到鸿蒙锁屏。
> 架构/进度见 `task.md`，桥源码见 `gotify_pushkit_bridge.py`。
> 📌 **术语**：本文档的「桥」= App「设置」里的「Hotify 推送服务」（可选字段），同一个东西。

## 它干什么
```
副机 SmsForwarder ─▶ Gotify(存/流) ─▶ 【本桥】─▶ 华为 Push Kit v3 ─▶ 鸿蒙锁屏
```
桥干两件事：① 订阅 Gotify `/stream` 收新消息（+断线按 id 高水位**回补**漏的消息）② 经 Push Kit v3 推到已注册设备。另开 `POST /register` 接收 App 上报：push token 每次刷新；gotify 配置 **first-set wins**（首次上报锁定写回 yaml，之后再报忽略——防公网攻击者抢首注把后端改成他的 Gotify；要零赛跑就 yaml 预填）。

## 两种运行状态
- **只订阅模式**：`cloud_function_urls` 未配 → 订阅 Gotify `/stream`、断线回补都正常，**Push Kit 转发跳过**（日志 `⏭ 跳过推送`）。用于先验证 Gotify 链路。
- **完整模式**：`cloud_function_urls` 配了（默认 Hotify 托管函数，或自部署）→ 全链路推到鸿蒙锁屏。

## 前置
- Python 3.8+（本机已有）
- 依赖：`pip install websockets`（PyJWT/cryptography 已移除——桥不再签 JWT，云函数干）
- Gotify 可达（本机直连 `wss://<your-gotify-host>:<port>` 已验证通）
- 完整推送另需：`cloud_function_urls` 推送服务入口（默认 Hotify 托管，见 PushKit.md）、设备 push token（App 上报）、自分类权益/`TEST_MESSAGE`

## 运行（Windows / Git Bash）
```bash
cd hotify-bridge
export GOTIFY_HTTP_URL="https://<your-gotify-host>:<port>"   # 可省略，已是默认
export GOTIFY_CLIENT_TOKEN="你的client_token"           # 必填：能读消息/订阅流的 client token
python -u gotify_pushkit_bridge.py                       # -u 无缓冲，日志实时刷
```
正常启动应看到（实测）：
```
[注册接口] http://0.0.0.0:25238/register
[Gotify] 高水位初始化 = 2814（不回放历史）
[Gotify] 订阅 wss://<your-gotify-host>:<port>/stream?token=***
```
然后常驻：来新消息打印 `[Gotify][实时] id=... 已转发`（完整模式）或 `⏭ 跳过推送（cloud_function_urls 未配置）`（只订阅模式）。断线自动 5 秒重连 + 回补。

## 配置（bridge_config.yaml：动静结合 + 文件）
| 项 | 在哪 | 说明 |
|---|---|---|
| `gotify_url` / `gotify_token` | `bridge_config.yaml`（**首次 App 上报锁定** / yaml 预填）> env 兜底 | **必填**。Gotify 地址 + client token（读消息/订阅流）。首次 App 上报后锁定，之后再报桥忽略（防公网抢首注改后端）。见下方「client token 怎么拿」 |
| `register_port` | `bridge_config.yaml`（静态，部署者填） | `/register` 监听端口；**留空 → 默认 25238** |
| `tls_cert_file` / `tls_key_file` | `bridge_config.yaml`（静态，部署者填） | 填了 → `/register` 走 https；空 → 明文 http（仅 LAN/调试）。与 Gotify 同一张域名证书 |
| `cloud_function_urls` / `cloud_function_token` | `bridge_config.yaml` | 推送服务入口 URL（JSON 数组，可多个 fallback）/ AUTH_TOKEN。private 锁云函数、**不入桥**。留空=只订阅模式（跳过推送，不崩）。见 repourl.md |
| `push_tokens.json` | 自动生成 | App 上报的 push token 存这里，别手改 |
| `TEST_MESSAGE` | 源码常量 | 已设 `True`，调测期绕 MARKETING 频控（每项目 1000 条/天）；正式改 `False` |
| `NOTIFY_CATEGORY` | 源码常量 | 推送类目，须与申到的自分类权益一致（默认 `SUBSCRIPTION`） |

⚠️ `gotify_token` 是**机密**，靠 bridge_config.yaml（gitignore）/ 环境变量，**别提交 git**。private 锁云函数（Netlify env），桥不含 → 桥可开源。

## 智能模式：Gotify 地址（端口 vs 完整地址）
桥连 Gotify 的地址（App「设置」的"Gotify 地址"，或 env / `bridge_config.yaml`）支持两种输入：
- **只输端口号**（纯数字，即你的 Gotify 端口）→ 桥认为 Gotify **与本桥同机部署**，自动连 `http://127.0.0.1:<端口>`（最快、免 TLS 证书）。
- **完整地址**（如 `https://<your-gotify-host>:<port>`）→ **远程 Gotify**，按原样连（wss/https，需有效证书）。
- 没带协议的（如 `<your-gotify-host>:<port>`）→ 自动补 `http://`。

> 同机部署建议：Gotify + 桥放一台主机，"Gotify 地址"只填端口，桥走 127.0.0.1；
> App 自己的历史列表则用该主机的可达地址（域名/IP:端口）。若后续 App 改为只走桥（桥代理历史），App 只需填"桥地址"+端口。

## client token 怎么拿（关键，别拿错）
Gotify WebUI（`https://<your-gotify-host>`）→ 登录 → **CLIENTS** 页 → Create Client → 复制 Token。
- ✅ **Client token**：读消息 / 订阅 `/stream` 用 ← 桥和 App 都用这个
- ❌ App token：SmsForwarder **发**消息用，不能订阅流，**别填这里**

## 常见问题
| 现象 | 原因 / 处理 |
|---|---|
| 闪退/秒退 | GBK 编码打 emoji 崩（**已强制 stdout UTF-8**）——用 `PYTHONUTF8=1 python -u ...`。（private.json 相关旧崩已随直连 Push Kit 移除） |
| `UnicodeEncodeError 'gbk'` | 已修（脚本顶部 `sys.stdout.reconfigure(utf-8)`）。若仍现，用 `PYTHONUTF8=1 python -u ...` |
| 订阅立刻断 / 401 | `GOTIFY_CLIENT_TOKEN` 错（填成 app token）或失效 → 换 client token |
| 收到消息没推到手机 | ① 只订阅模式（`cloud_function_urls` 未配，正常跳过）② 没设备注册（看 `push_tokens.json` 是否有值）③ Push Kit 频控/自分类权益（看 Push Kit 返回 code，非 `80000000`=失败） |
| 断线 | 自动 5 秒重连 + 按高水位回补漏的消息（≥100 条会告警：超出的老消息去 App `GET /message` 看） |
| App 状态条显示「实时（8s刷新）」而非「实时」 | App 的 WebSocket 直连被 **Gotify 自带的 Origin 校验**挡了——ArkTS 原生客户端会发非同源 Origin，Gotify 默认返 403，App 自动降级 8s 轮询兜底（**消息照收**，只是从秒到变 8s 内）。要即时推送：Gotify `config.yml` 设 `server.stream.allowedorigins: ['.*']`（合法正则，**不是 `*`**）+ `server.cors.alloworigins`，重启 Gotify。桥走 Python websockets 不发 Origin 不受影响；只有 App 的 ArkTS 客户端中招。详见 [gotify#372](https://github.com/gotify/server/issues/372)/[#580](https://github.com/gotify/server/issues/580) |

## 推送：桥调云函数，不直连 Push Kit（2026-07-07 架构变更）
桥**不再持 `private.json`、不再签 JWT、不再直连 Push Kit**——改 HTTP POST 一个无状态云函数，函数持 private 干这些（private 锁云函数 → 桥可开源）。
- **云函数**（Netlify，`hotifypushkit.netlify.app` 或 custom domain `hotify.lovesweet.online`）：签 PS256 JWT + 调 Push Kit v3 + 转发 code。纯协议管道（透传调用方 notification 对象，不构造）。详见 `CloudFuction/PushKit.md`。
- **桥**：`send_to_huawei` 遍历 `cloud_function_urls` POST（fallback），body = `{token, notification, data, testMessage}`，拿 code 按码表分类（Delivered/DeadToken/SystemError）。
- **鉴权（旧桥直连时的核实，现归云函数）**：鸿蒙 NEXT Push Kit = 服务账号 RSA 私钥签 PS256 JWT（**非** client_id/secret），JWT 直当 Bearer 调 `/v3/{project_id}/messages:send` + `push-type:0`。别和 HMS Core（安卓）的 `client_id/secret + /v1/{appId}/messages:send` 混。详见 task.md #2/#17 + PushKit.md §5。


## 完整推送（已闭环，2026-07-07）
桥端云函数链路已通（实测 80000000 × 2 设备）。清单：
1. ✅ 云函数部署（Netlify，持 private 签 JWT 调 Push Kit）——见 `CloudFuction/PushKit.md` §9
2. ✅ 桥配 `cloud_function_urls`（默认 Hotify 托管函数）+ `cloud_function_token`
3. 桥 `TEST_MESSAGE=True`（调测期绕频控，正式改 False）
4. 申「自分类权益」改服务/通讯类（锁屏）+ payload `category` 对齐；自用可先靠 `TEST_MESSAGE` + 手机手动开「锁屏通知」
5. App 上报 push token（`POST 桥/register`）→ `push_tokens.json` 有值
6. 跑桥 → 副机发条消息 → 鸿蒙锁屏弹


## 部署（生产拓扑）
**桥和 Gotify 同机，各自 serve HTTPS，共用同一张证书**（各自端口）。手机走 https 触达两者；桥也走 https 连 Gotify——同一张证书、同一域名，校验通过。

```
  手机 ──https──▶ Gotify   https://你的域名:<端口>
        ──https──▶ 桥       https://你的域名:25238  (/register，上报 push token)
  桥   ──https──▶ Gotify   https://你的域名:<端口>   (同证书同域名)
```

- **一张证书喂两个服务**：Gotify 在 `config.yml`（`ssl.enabled` + 证书/私钥路径）加载；桥在 `bridge_config.yaml` 的 `tls_cert_file` / `tls_key_file` 填**同一份证书文件**。证书签一次（acme.sh / certbot / Let's Encrypt），两边都指过去。
- **桥没填证书 → 退化成明文 http**（仅 LAN/调试）。任何公网部署都得填——否则手机上报的 push token 走明文，蜂窝/外网下裸奔。
- **桥里填的"Gotify 地址"用完整 https**（如 `https://你的域名:<端口>`），别用"只填端口"的智能模式——那假设 Gotify 是明文 http，TLS 开了连不上。
- **本机调试**：终端常驻 `python -u gotify_pushkit_bridge.py`。
- **持久化**：systemd / docker `restart=always` / Windows 任务计划或服务（防进程崩）。

---
*2026-07-07：**架构变更**——private 移出桥、锁云函数（Netlify）；桥改 HTTP POST 云函数，不再签 JWT / 直连 Push Kit（实测 80000000 × 2 设备）。详见 `CloudFuction/PushKit.md` + `task.md` #17。*
*更新：2026-06-29。`/register` 加固：gotify 配置改 **first-set wins**（首次 App 上报锁定写回 yaml，之后再报忽略，防公网抢首注改后端；要零赛跑就 yaml 预填）；push token 每次刷新；响应加 `device_known`/`ignored_gotify` 给 App 反馈。*
*2026-06-28：脊柱模式实测 OK（连真实 Gotify，id=2814，订阅 /stream 正常）；FAQ「实时(8s刷新)」降级 = Gotify Origin 校验（`allowedorigins` 修法）。*
