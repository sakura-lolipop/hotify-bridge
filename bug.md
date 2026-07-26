# bug.md — hotify-bridge bug 记录

> 用户反馈 + 调查 + 修复。✅ 已发版；🔧 已修待发；🐛 待查。

---

## 🔧 #2 cloud_function_urls 空配 → fetch 全挂 → 收不到推送（待发 v1.1.2）

- **报告**：PandaSoos（2026-07-26）—— "Docker 部署时 cloud_function_urls 这个地址不明白；ai 没填之前确实收不到，填了就有弹窗；docker 的预期行为没有获取 urls"。
- **症状**：Docker 部署、`cloud_function_urls` 留空（靠桥启动 fetch）→ fetch 没拿到 → 推送入口空 → **收不到通知**，手填 URL 才行。
- **根因**：fetch 源只有 `ghproxy.com` + `raw.githubusercontent.com`，**俩都对国内不稳**（raw 被墙、ghproxy 第三方免费代理常抽风）；首次部署无 cache，全挂 → `cloud_function_urls` 空 → 跳过推送。（注：netlify 推送服务本身用户能通——填了就推成功——问题只在 fetch 那一步。）
- **修复**（commit 已推 main）：
  - `cfTxtSources` 加 **Gitee raw**（`gitee.com/sakura-lolipop/hotify-bridge/raw/main/cloud_function_urls.txt`，国内直连最稳）作**首选**，ghproxy/raw 退为兜底。
  - 抽 `fetchCfTxtOnce` helper 统一多源尝试；冷启动 `fetchCfURLsFromTxt` 加 **2 次重试 + cache 兜底**；`refreshCfURLs`（后台每 h + 启动立即一次）复用 helper，瞬时挂下轮自愈。
  - **保住自动更新**（cloud_function_urls.txt 改了桥跟上）且**可靠**（Gitee 通则秒成）——比预填死值好。
  - 测试：`TestFetchCfURLsFromTxt` + `TestFetchCfTxtOnceFallback`（首源 500→用次源）。`go test` 全过。
- **状态**：🔧 已提交 main，待发 **v1.1.2**。
- **教训**：又犯了"开发机能 fetch 成功就以为用户也行"（同 GHCR 那次 dev≠用户）。终端用户网络（GFW）下 ghproxy/raw 不稳，得有国内可达源（Gitee）兜底。

---

## ✅ #1 重启后回补全量重放（v1.1.1 已发）

- **报告**：用户反馈"有时候（重启/重连）会把全部消息再推送一遍"。
- **症状**：桥**重启撞 Gotify 不可达** → 首次回补把最近 100 条全推一遍（用户看是"全部消息又推了一次"）。
- **根因**：`lastMsgID`（高水位）只在内存、不落盘；启动撞 Gotify 不可达 → `initLastID()` 失败、水位留 0 → `backfill()` 取最近 100 条、`id>0` 全中 → 全量重放。（纯运行中重连不受影响——内存水位还在。）
- **修复**（v1.1.1）：
  - **水位落盘**：`forward` 每推一条写 `last_msg_id`；`initLastID` 启动优先读落盘值续传 → 重启只补真漏的。
  - **`backfill` 兜底**：水位=0（无落盘值 且 `initLastID` 没成）→ 设到本批最大 id、一条不推（宁可漏断档，绝不重放刷屏）。
  - 测试：`TestInitLastIDRestoresPersisted` + `TestBackfillGuardZeroWatermark`（注册假设备+计数云函数，验 0 重放）。
- **状态**：✅ v1.1.1 已发（2026-07-26）。
