# Hotify 桥（gotify_pushkit_bridge.py）运行手册

> Gotify ↔ 华为 Push Kit 转发桥。订阅 Gotify `/stream`，把新消息经华为 Push Kit v3 推到鸿蒙锁屏。
> 架构/进度见 `task.md`，桥源码见 `gotify_pushkit_bridge.py`。

## 它干什么
```
副机 SmsForwarder ─▶ Gotify(存/流) ─▶ 【本桥】─▶ 华为 Push Kit v3 ─▶ 鸿蒙锁屏
```
桥干两件事：① 订阅 Gotify `/stream` 收新消息（+断线按 id 高水位**回补**漏的消息）② 经 Push Kit v3 推到已注册设备。另开 `POST /register` 接收 App 上报的 push token。

## 两种运行状态
- **脊柱模式（现在就能跑）**：无 `private.json` → 订阅 Gotify `/stream`、断线回补都正常，**Push Kit 转发跳过**（日志 `⏭ 跳过推送`）。用于先验证 Gotify 链路。
- **完整模式（milestone 2）**：放 `private.json` → 全链路推到鸿蒙锁屏。

## 前置
- Python 3.8+（本机已有）
- 依赖：`pip install websockets PyJWT cryptography`（均已装）
- Gotify 可达（本机直连 `wss://<your-gotify-host>:<port>` 已验证通）
- 完整推送另需：`private.json`（华为服务账号）、设备 push token（App 上报）、自分类权益/`TEST_MESSAGE`

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
然后常驻：来新消息打印 `[Gotify][实时] id=... 已转发`（完整模式）或 `⏭ 跳过推送（private.json 未配置）`（脊柱模式）。断线自动 5 秒重连 + 回补。

## 配置（环境变量 / 文件）
| 项 | 怎么设 | 说明 |
|---|---|---|
| `GOTIFY_HTTP_URL` | 环境变量 | Gotify https 地址，默认 `https://<your-gotify-host>:<port>` |
| `GOTIFY_CLIENT_TOKEN` | 环境变量 | **必填**。client token（读消息/订阅流）。见下方「client token 怎么拿」 |
| `private.json` | 同目录文件 | 华为服务账号（AGC 下载，含 RSA 私钥）。缺失=脊柱模式（跳过推送，不崩） |
| `push_tokens.json` | 自动生成 | App 上报的 push token 存这里，别手改 |
| `TEST_MESSAGE` | 源码常量 | 已设 `True`，调测期绕 MARKETING 频控（每项目 1000 条/天）；正式改 `False` |
| `NOTIFY_CATEGORY` | 源码常量 | 推送类目，须与申到的自分类权益一致（默认 `ACCOUNT`） |

⚠️ token / `private.json` 私钥都是**机密**，靠环境变量注入 + 本地文件，**别提交 git**。

## 智能模式：Gotify 地址（端口 vs 完整地址）
桥连 Gotify 的地址（App「设置」的"Gotify 地址"，或 env / `bridge_config.json`）支持两种输入：
- **只输端口号**（纯数字，如 `25234` / `25233`）→ 桥认为 Gotify **与本桥同机部署**，自动连 `http://127.0.0.1:<端口>`（最快、免 TLS 证书）。
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
| 闪退/秒退 | 曾因 `private.json` 缺失开局崩（**已改成告警不退**）；或 GBK 编码打 emoji 崩（**已强制 stdout UTF-8**）。现在缺 `private.json` 只告警、照常订阅 |
| `UnicodeEncodeError 'gbk'` | 已修（脚本顶部 `sys.stdout.reconfigure(utf-8)`）。若仍现，用 `PYTHONUTF8=1 python -u ...` |
| 订阅立刻断 / 401 | `GOTIFY_CLIENT_TOKEN` 错（填成 app token）或失效 → 换 client token |
| 收到消息没推到手机 | ① 脊柱模式（无 `private.json`，正常跳过）② 没设备注册（看 `push_tokens.json` 是否有值）③ Push Kit 频控/自分类权益（看 Push Kit 返回 code，非 `80000000`=失败） |
| 断线 | 自动 5 秒重连 + 按高水位回补漏的消息（≥100 条会告警：超出的老消息去 App `GET /message` 看） |

## 推送模式 & 鉴权（已核实：服务账号 JWT，**非** client_id/secret）
- 鸿蒙 NEXT Push Kit 服务端**官方推荐 = 服务账号 + RSA 私钥 JWT**（[push-jwt-token](https://developer.huawei.com/consumer/cn/doc/harmonyos-guides/push-jwt-token)）。本桥即此法，别改。
- ⚠️ 别和 HMS Core（安卓）的 [`hms-push-serverdemo-python`](https://gitee.com/hms-core/hms-push-serverdemo-python) 混：那套是 `client_id/client_secret` + `/v1/{appId}/messages:send`，**鉴权和 API 版本都不同**。demo 只能参考消息体结构。
- 对比：

  | | HMS Core demo（安卓） | 本桥（鸿蒙 NEXT） |
  |---|---|---|
  | 凭据 | client_id + client_secret（字符串） | 服务账号 RSA 私钥（JSON） |
  | 换 token | id/secret 直换 | 私钥签 JWT → 换 token |
  | 推送 API | `/v1/{appId}/messages:send` | `/v3/{project_id}/messages:send` + `push-type:0` |

- **RSA 私钥（`private.json`）从哪来**：华为开发者联盟 → 你的项目 →「用户与权限」/「项目设置」里找「**服务账号（Service Account）**」→ 创建 → **下载 JSON 密钥文件**（含 `private_key`/`key_id`/`sub_account`/`project_id`）→ 存成 `private.json` 放桥目录。开 Push Kit 能力 ≠ 创建服务账号，这是单独一步。

## 完整推送（milestone 2）要做的
1. AGC 建项目（`com.yourname.hotify`）→ 开通 Push Kit
2. AGC→项目设置→常规→服务账号→新建→下载 `private.json` → 放本目录
3. 桥 `TEST_MESSAGE=True`（已设，调测期绕频控）
4. 申「自分类权益」改服务/通讯类（锁屏）+ payload `category` 对齐；自用可先靠 `TEST_MESSAGE` + 手机手动开「锁屏通知」
5. App 上报 push token（`POST 桥/register`）→ `push_tokens.json` 有值
6. 跑桥 → 副机发条消息 → 鸿蒙锁屏应弹

## 部署（远期）
- **本机调试**：终端常驻 `python -u gotify_pushkit_bridge.py`
- **持久化**：systemd / docker `restart=always` / Windows 任务计划或服务（防进程崩）
- **建议**：和 Gotify 一起部署到远程主机（<your-gotify-host>），桥走 localhost 连 Gotify，外网只暴露必要端口

---
*更新：2026-06-21。实测脊柱模式 OK（连真实 Gotify，id=2814，订阅 /stream 正常）。*
