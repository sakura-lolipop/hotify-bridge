# bug.md — hotify-bridge bug 记录

> 用户反馈 + 调查 + 修复。✅ 已发版；🔧 已修待发；🐛 待查。

---

## 🐛 #4 push 失败 → 消息永久丢（缓修，待做推送可靠性队列）

- **来源**：对抗审查（Go agent，2026-07-27）。
- **症状**：`forward`（`subscriber.go`）先 CAS 推进水位 + 落盘，**然后**才 `sendToHuawei`。若云函数抽风（504/部署中）/ 无设备 / 全员取消订阅 → push 全失败，水位已落盘 → 重启不补 → 这条消息**永久丢**（云函数短暂故障期间的消息全没）。
- **根因**：水位语义是"已尝试"非"已送达"；push 失败后水位仍推进，无重试机制。
- **修法（待做）**：pending 推送队列——push 失败的消息入队重试，水位只在 push 成功后推进。~150 行 + 测试，功能级改动。**Push Kit 的 notifyId(=msg id) 幂等（覆盖式），未来加重试不会重发**——缓修不欠债。
- **状态**：🐛 缓修（记限制；等做"推送可靠性"特性时一起）。

---

## 🐛 #3 换 Gotify 实例 → 水位 stale 永久静默丢（缓修，边缘）

- **来源**：对抗审查（Go agent，2026-07-27）。
- **症状**：从 Gotify-A（id 已到 999）切到 Gotify-B（id 从 1 开始），落盘水位 999 仍生效 → B 所有 id≤999 的消息被 `forward` CAS 去重吞 → **永久不推**（跟 #1 全量重放是镜像问题：一个重放、一个永漏）。
- **根因**：`initLastID` 优先用落盘水位，不区分 Gotify 实例；换实例后旧水位 stale。
- **修法**：把 `gotify_url` 和水位一起持久化（或单独文件）；`initLastID` 时 url 变了 → 重置水位。~30 行。
- **状态**：🐛 缓修（触发极罕见——只有迁移 Gotify 实例才踩；真有用户迁移时再修）。

---

## ✅ #2 cloud_function_urls 空配 → fetch 全挂 → 收不到推送（v1.1.2 已发）

- **报告**：PandaSoos（2026-07-26）—— "Docker 部署时 cloud_function_urls 这个地址不明白；ai 没填之前确实收不到，填了就有弹窗；docker 的预期行为没有获取 urls"。
- **症状**：Docker 部署、`cloud_function_urls` 留空（靠桥启动 fetch）→ fetch 没拿到 → 推送入口空 → **收不到通知**，手填 URL 才行。
- **根因**：fetch 源只有 `ghproxy.com` + `raw.githubusercontent.com`，**俩都对国内不稳**（raw 被墙、ghproxy 第三方免费代理常抽风）；首次部署无 cache，全挂 → `cloud_function_urls` 空 → 跳过推送。（注：netlify 推送服务本身用户能通——填了就推成功——问题只在 fetch 那一步。）
- **修复**（commit 已推 main）：
  - `cfTxtSources` 加 **Gitee raw**（`gitee.com/sakura-lolipop/hotify-bridge/raw/main/cloud_function_urls.txt`，国内直连最稳）作**首选**，ghproxy/raw 退为兜底。
  - 抽 `fetchCfTxtOnce` helper 统一多源尝试；冷启动 `fetchCfURLsFromTxt` 加 **2 次重试 + cache 兜底**；`refreshCfURLs`（后台每 h + 启动立即一次）复用 helper，瞬时挂下轮自愈。
  - **保住自动更新**（cloud_function_urls.txt 改了桥跟上）且**可靠**（Gitee 通则秒成）——比预填死值好。
  - 测试：`TestFetchCfURLsFromTxt` + `TestFetchCfTxtOnceFallback`（首源 500→用次源）。`go test` 全过。
- **状态**：✅ v1.1.2 已发（2026-07-27：GitHub Release + GHCR/ACR 镜像 + Gitee 二进制全齐）。
- **教训**：又犯了"开发机能 fetch 成功就以为用户也行"（同 GHCR 那次 dev≠用户）。终端用户网络（GFW）下 ghproxy/raw 不稳，得有国内可达源（Gitee）兜底。

---

## ✅ #1 重启后回补全量重放（v1.1.2 已发；v1.1.1 曾短暂发布后被取代）

- **报告**：用户反馈"有时候（重启/重连）会把全部消息再推送一遍"。
- **症状**：桥**重启撞 Gotify 不可达** → 首次回补把最近 100 条全推一遍（用户看是"全部消息又推了一次"）。
- **根因**：`lastMsgID`（高水位）只在内存、不落盘；启动撞 Gotify 不可达 → `initLastID()` 失败、水位留 0 → `backfill()` 取最近 100 条、`id>0` 全中 → 全量重放。（纯运行中重连不受影响——内存水位还在。）
- **修复**（v1.1.1）：
  - **水位落盘**：`forward` 每推一条写 `last_msg_id`；`initLastID` 启动优先读落盘值续传 → 重启只补真漏的。
  - **`backfill` 兜底**：水位=0（无落盘值 且 `initLastID` 没成）→ 设到本批最大 id、一条不推（宁可漏断档，绝不重放刷屏）。
  - 测试：`TestInitLastIDRestoresPersisted` + `TestBackfillGuardZeroWatermark`（注册假设备+计数云函数，验 0 重放）。
- **状态**：✅ v1.1.2 已发（2026-07-27；v1.1.1 曾短暂发布后被取代移除，此修复合并进 v1.1.2）。
