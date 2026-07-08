# 更新日志 / Changelog

桥（hotify-bridge）的 notable 变化。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/)。

## [Go 桥重写] — 2026-07-08

Python→Go 重写（CP0-CP5），`go/` 子目录（Python 留 repo 根作 fallback）。单 `main` 包 6 源文件 + 4 测试，唯一外部依赖 `gorilla/websocket`。配置/文件/线契约与 Python 完全通用（App/云函数/Gotify 察觉不到换语言）。

### Added
- **Go 桥（CP0-CP5）**：① `net/http` stdlib 取代 Python 手搓 HTTP 解析（删 `_send_http_response` + 手动 request-line/Content-Length 解析）；② goroutine + `sync`（Mutex/RWMutex/atomic.Int64）取代 asyncio + `to_thread`，高水位 `atomic.Int64` CAS 去重（live+回补共享单变量防重连双发）；③ `gotify_pushkit_bridge.py` 全功能 1:1 移植——/register（first-set-wins + `saveBridgeConfig` 持久化先于 200 + autodetect 后台 goroutine 不阻塞 HAP 8s）、订阅器（gorilla `Dialer.DialContext` 传空 `http.Header{}` **不发 Origin**、20s ping ticker、回补 100 条升序）、推送转发（`clickAction.data={ts}` 不透明字符串透传 + 云函数 fallback/重试 3 次 + 死 token 白名单 `{80100000,80300007}` + 全局闸门 `delivered==0` 不删）、0-config 自动探测（证书探 Gotify config/certs glob + 端口探 443→80→cfg→url + private-IP skip-verify + cf-txt ghproxy→直连→cache）。
- **交叉编译 `go/build-all.sh`**：`CGO_ENABLED=0` 出 5 平台静态二进制（linux amd64/arm64、windows amd64、darwin amd64/arm64，各 6.4-7.1MB），`-trimpath -ldflags="-s -w"` 缩体积。纯 Go 无 cgo，免交叉工具链。
- **mock 测试（4 文件）**：CP0 宽松解析器 + 文件往返；CP1 /register 10 payload Go-vs-Python parity（`jq -S` 全 MATCH）；CP2 WS Origin 缺席（killer）+ 高水位去重 + 回补；CP3 POST body（ts 透传/data 字符串/Auth 条件/notifyId omitempty/全局闸门/retry/fallback）；CP4 parseGotifyConfig/parseCfTxt/isPrivateIP。

### Fixed
- **Python `start_register_server` `ssl=ssl` → `ssl=ssl_ctx`（latent bug）**：传了 ssl **模块**（非 None）→ asyncio 当成开 SSL → 连进来调 `ssl.wrap_bio`（模块级，Python 3.12 已删）→ `AttributeError` 崩 /register。prod 跑 Python <3.12 没暴露（`ssl.wrap_bio` 模块级还在，将错就错）；升级 3.12+ 即崩。Go 重写 CP1 parity 测试（Python 3.13 环境）暴露此 bug，修后 10/10 对齐。

### Changed
- **BRIDGE.md 加「Go 版（推荐）vs Python（fallback）」章节**：源码/构建/依赖/运行/配置对比表 + Go 优势 + 线兼容已验 + Windows Defender 排除项提醒（新编 Go exe 可能被误杀）。
- **真机验收（CP5）**：订阅远程 Gotify（`wss://push.smgwy.com:25234`）→ 收消息 → 推云函数 → Push Kit `80000000` → 手机收通知 → **点通知 App 滚到对应消息**（★ hazard 1 ts 端到端 killer 验过：ts 从 Gotify→桥→云函数→Push Kit→App `m.date===ts` 精确反查全程没坏）+ 干净 UTF-8 中文不坏。Go 桥线兼容端到端 VERIFIED。
- **Go 默认 register_port 25238 → 8080（2026-07-08）**：公开发布版默认 8080（避开 Gotify 常占的 80/443，对部署者友好）；自用/测试在 `bridge_config.yaml` 显式 `register_port: 25238` 覆盖（一份代码 per 目录，无需两份源码）。Python fallback 默认仍 25238（未改）。
- **cloud_function_urls 自动检测（cache-first + 后台刷新，2026-07-08）**：留空 = 自动管理——启动 cache-first（有 cache 秒起不等网络 / 冷启动 fetch 建缓存）+ 后台立即 fetch + 每 1h 刷新 txt 热更新（加锁，对 push 安全）。云函数变动只改仓库 `cloud_function_urls.txt`，桥常驻免重启跟上。填了 = 手动 override（不走自动）。Python fallback 仍是启动单次 fetch（未改）。

---

## [Unreleased] — 开发中（2026-06-29）

### Added
- **0-config 自动探测（2026-07-07）**：① 证书从 Gotify `config.yml`（`ssl.certfile`/`ssl.keyfile`）或 `<config_dir>/certs/` glob（*.cer/*.pem/*.crt + *.key）自动发现，5 种情况显式 print；② Gotify 端口探 443(HTTPS)→80(HTTP)→config-port→gotify_url-port（**不信任 config 80**——Gotify 302→443 不再降级；127.0.0.1 始终探避免 hairpin NAT）；③ private-IP（127/10.x/172.16-31/192.168）跳过 TLS 主机名校验（LAN/同机可信）；④ `cloud_function_urls` 从 `cloud_function_urls.txt` fetch（ghproxy.com 优先→直连→本地 cache，一行一 URL，零配置外部管理）。
- **`cloud_function_urls.txt` 外挂（2026-07-07）**：一行一个推送服务入口 URL，桥启动 fetch。改这里 → 桥下次启动拉新（不动桥代码）。仓库内已填默认 Hotify 托管函数。
- **端口策略 per-config（2026-07-07；默认值 2026-07-08 改）**：默认 8080（正式，`register_port` 留空即用，避开 Gotify 常占的 80/443）；测试目录 `bridge_config.yaml` 显式设 `register_port: 25238`。一份代码两份配置（per 目录，无需两份源码）。详见 `BRIDGE.md`「端口策略」。
- **`/register` TLS**：`tls_cert_file` / `tls_key_file` 填证书路径 → `/register` 走 https；留空 → 明文 http（启动 `⚠️ 降级明文 http` 提醒）。与 Gotify 共用同一张域名证书。
- **`register_port` 可配置**：留空 → 默认 8080（启动 `⚠️ 用默认端口` 提醒）；填了用填的。
- **`gotify_url_local`（同机覆盖）**：桥和 Gotify 同机时填 `https://127.0.0.1:<端口>`，桥走 localhost 连 Gotify（覆盖 App 上报的域名），免 NAT hairpin / 代理绕路。
- **自动探测同机 Gotify**：`gotify_url_local` 留空时，桥从 App 上报的域名取端口，自动探 `https://127.0.0.1:<端口>/version`，Gotify 应答就自动填 `gotify_url_local`（重试 3 次应对瞬时波动；成功才标记 done，失败不永久放弃）。
- **localhost TLS 跳过证书校验**：连 `127.0.0.1` / `localhost` 的 TLS Gotify 时自动跳过主机名校验（域名证书在 127.0.0.1 上对不上，本地回环可信）。
- **首次启动自动建配置**：没有 `bridge_config.yaml` 时，自动从 `_CFG_DEFAULTS` 生成一份（带头部注释），部署者直接改（无 example 模板文件——注释内嵌生成逻辑）。
- **双语 README**：默认中文（`README.md`），英文版 `README.en.md`，顶部互相链接。
- **`/register` 响应字段**：返 `device_known`（UUID 是否已登记过）/ `gotify_set`（本次是否首注成功）/ `ignored_gotify`（桥已配置、本次 gotify 被忽略），App 据此给反馈。
- **订阅类字样标注**：转发时给标题加 `订阅:` 前缀（符合华为 Push Kit"订阅"类消息分类的标注要求，见 push-apply-right；如 `订阅:短信验证码`）。新配置 `subscribe_label`（默认 `true`=标注；`false`=不标注）。
- **订阅总开关（per-device 订阅/取消）**：`/register` 收 `subscribed` 字段，按设备存 `subscribe_status.json`；转发时 `subscribed=false` 的设备跳过不推（未记录默认订阅，不破坏老设备）。`subscribed` 走 push_token 同款"每次刷新、不锁定"路径（首注锁定只管 gotify），反复订阅/取消都生效。配合 App 端订阅开关，符合华为"订阅"类消息订阅/取消订阅流程。

### Changed
- **点通知投递改 `clickAction.data={ts}`（2026-07-07）**：ts=msg.date（Gotify ISO8601+纳秒时间戳）。Push Kit 点通知把 `data` 拍平进 `want.parameters` 顶层（`want.parameters['ts']`），**非** `want.parameters.data`。`payload.data`（顶层）点通知不投递、`checkId` 值被忽略——**clickAction.data 是唯一点击投递机制**（真机 hilog 实测）。详见 `docs/pushkit-delivery.md`。
- **`NOTIFY_CATEGORY` 改 `SUBSCRIPTION`**：订阅类(SUBSCRIPTION)自分类权益审核通过后，与已开通类目保持一致（华为要求配置 category=SUBSCRIPTION）。之前 `ACCOUNT` 为未开通时的占位（携带未开通权益的 category 会归资讯营销）。
- **配置格式：JSON/YAML → 宽松文本**。每行 `键: 值`，值是冒号后的整段——反斜杠、冒号、冒号后无空格、引号不配对全容错。**去掉 pyyaml 依赖**（deps 回到 `websockets PyJWT cryptography`）。
- **App 上报持久化改成定点更新**：只替换 `gotify_url` / `gotify_token` 两行，其余（静态项 + `#` 注释）原样保留——不再整文件重写丢注释。
- **日志口语化 + 「桥」→「Hotify 推送服务」统一**：「高水位初始化」→「从最新消息开始只推新消息」、「首注 Gotify 已锁定」→「首次收到 App 的 Gotify 配置」、`[桥]` 标签 → `[Hotify 推送服务]` 等，去掉开发术语。README/BRIDGE 加术语映射（桥 = Hotify 推送服务）。
- **register server 重构：HTTPServer（同步单线程）→ asyncio.start_server（async，整合主 loop）**：根治长跑后收不到上报。旧架构 register 在独立 daemon 线程跑 HTTPServer（单线程、accept backlog 默认 5、连接异常可能 fd 泄漏），任一偶发慢（磁盘 IO 抖动 / GC / 首注 `_autodetect_local_gotify` 同步探同机 Gotify ~9s）→ do_POST >8s → HAP `connectTimeout:8000` 超时断开 → HAP 多调用点 + 重试短时多次连 → backlog 满 OS 丢连接 → 桥 `accept` 不到 → **收不到上报**（正反馈自维持，重启才行）。推送不受影响（keep_subscribed 在主线程 async loop，独立 websockets + urllib，不碰 25238）。新架构 register 搬进主 async loop（`asyncio.gather(register, keep_subscribed)`），每连接独立协程互不阻塞，无单线程瓶颈 / backlog 满 / fd 泄漏。配套：文件读写加 `_file_lock`（防 async loop + to_thread 跨线程 race 半写）、`_autodetect` 首注改后台线程（不阻塞 200 响应）。

### Fixed
- Windows 反斜杠路径（`C:\Users\...`）在双引号 YAML 里被当 `\U` 转义、整个配置解析失败 → 改宽松解析器，反斜杠原样、不再炸。
- 配置解析失败不再静默吞错（打 `❌ 解析失败` + 行号 + 修正提示）。
- **失效 token 清理不再误删好 token**：旧逻辑"非成功码 / HTTPError 一律删"，会把与 token 死活无关的错（鉴权失败 802x、权益未开 80300002、消息超长 80300008、系统错 81xxxxx）也当死 token 删——鉴权闪一下能把全量 token 一锅端。改为：① **白名单删除**，仅 `80100000`（illegal_tokens）/ `80300007`（所有 token 无效）两码才删（据华为官方码表钉死，≈ APNs `Unregistered`）；② **HTTPError 改为保留**；③ **全局闸门**——本轮 0 台成功（疑系统性故障）则一台都不删，防 app 包名配错时全台返 `80300007` 被误触发全锅端。其余码一律保留 + 日志。

### Removed
- **`server_id` 字段（2026-07-07）**：`/register` 不再收/存 `server_id`，`_CFG_DEFAULTS` / `save_bridge_config` / seed 模板全删该行。原 source_id 设计（桥存 per-device 人工状态、多设备全局覆盖）弃用 → 改 `ts` 无状态反查（自然键 msg.date）。两端 dead code 清理。

### Security
- **`/register` 防公网抢首注改后端**：gotify 配置首次 App 上报后**锁定**（写回 yaml 持久化），之后再报一律**忽略**——堵住"公网攻击者抢先 `POST /register`、把桥的后端改成自己的 Gotify、借你的配额推垃圾通知"。要零赛跑可在 `bridge_config.yaml` 预填 gotify（桥启动即锁）。详见 `gotify_pushkit_bridge.py` `_process_register`。

### ⏳ 待测（register server 重构后验证）
- **长跑后是否还卡死**：async 多协程，预期不再单线程阻塞 / backlog 满。需远程跑数小时 + 反复 register 验（旧 bug 长跑后才现，新代码待同条件压测确认根治）。
- **是否引入新 bug**：async handler 手动 HTTP 解析（request line / Content-Length / body）/ `_file_lock` 是否死锁 / `_autodetect` 后台线程——首次部署需验 register 全流程（HAP 点对勾 → 200 + push token 写入 + 订阅开关工作）+ 推送照常 + TLS 模式（若配证书，验 `asyncio.start_server` 的 `ssl=` 参数生效）。

---

## 初始版本 — 已上线 GitHub
Gotify ↔ 华为 Push Kit 转发桥首发：订阅 Gotify `/stream`、断线按 id 高水位回补（不重不漏）、`POST /register` 收 App 上报的 push token + Gotify 配置、服务账号 JWT 直接当 Bearer（官方 push-jwt-token，非 client_id/secret）、脊柱 / 完整双模式、失效 token 自动清理（bark 式）。
