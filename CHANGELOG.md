# 更新日志 / Changelog

桥（hotify-bridge）的 notable 变化。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/)。

## [Unreleased] — 开发中（2026-06-28）

### Added
- **`/register` TLS**：`tls_cert_file` / `tls_key_file` 填证书路径 → `/register` 走 https；留空 → 明文 http（启动 `⚠️ 降级明文 http` 提醒）。与 Gotify 共用同一张域名证书。
- **`register_port` 可配置**：留空 → 默认 25238（启动 `⚠️ 用默认端口` 提醒）；填了用填的。
- **`gotify_url_local`（同机覆盖）**：桥和 Gotify 同机时填 `https://127.0.0.1:<端口>`，桥走 localhost 连 Gotify（覆盖 App 上报的域名），免 NAT hairpin / 代理绕路。
- **自动探测同机 Gotify**：`gotify_url_local` 留空时，桥从 App 上报的域名取端口，自动探 `https://127.0.0.1:<端口>/version`，Gotify 应答就自动填 `gotify_url_local`（重试 3 次应对瞬时波动；成功才标记 done，失败不永久放弃）。
- **localhost TLS 跳过证书校验**：连 `127.0.0.1` / `localhost` 的 TLS Gotify 时自动跳过主机名校验（域名证书在 127.0.0.1 上对不上，本地回环可信）。
- **首次启动自动建配置**：没有 `bridge_config.yaml` 时，自动从 `bridge_config.example.yaml` 复制一份（带注释），部署者直接改。
- **双语 README**：默认中文（`README.md`），英文版 `README.en.md`，顶部互相链接。

### Changed
- **配置格式：JSON/YAML → 宽松文本**。每行 `键: 值`，值是冒号后的整段——反斜杠、冒号、冒号后无空格、引号不配对全容错。**去掉 pyyaml 依赖**（deps 回到 `websockets PyJWT cryptography`）。
- **App 上报持久化改成定点更新**：只替换 `gotify_url` / `gotify_token` 两行，其余（静态项 + `#` 注释）原样保留——不再整文件重写丢注释。

### Fixed
- Windows 反斜杠路径（`C:\Users\...`）在双引号 YAML 里被当 `\U` 转义、整个配置解析失败 → 改宽松解析器，反斜杠原样、不再炸。
- 配置解析失败不再静默吞错（打 `❌ 解析失败` + 行号 + 修正提示）。
- **失效 token 清理不再误删好 token**：旧逻辑"非成功码 / HTTPError 一律删"，会把与 token 死活无关的错（鉴权失败 802x、权益未开 80300002、消息超长 80300008、系统错 81xxxxx）也当死 token 删——鉴权闪一下能把全量 token 一锅端。改为：① **白名单删除**，仅 `80100000`（illegal_tokens）/ `80300007`（所有 token 无效）两码才删（据华为官方码表钉死，≈ APNs `Unregistered`）；② **HTTPError 改为保留**；③ **全局闸门**——本轮 0 台成功（疑系统性故障）则一台都不删，防 app 包名配错时全台返 `80300007` 被误触发全锅端。其余码一律保留 + 日志。

---

## 初始版本 — 已上线 GitHub
Gotify ↔ 华为 Push Kit 转发桥首发：订阅 Gotify `/stream`、断线按 id 高水位回补（不重不漏）、`POST /register` 收 App 上报的 push token + Gotify 配置、服务账号 JWT 直接当 Bearer（官方 push-jwt-token，非 client_id/secret）、脊柱 / 完整双模式、失效 token 自动清理（bark 式）。
