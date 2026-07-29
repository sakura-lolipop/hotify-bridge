# bug.md — hotify-bridge bug 记录

> 用户反馈 + 调查 + 修复。✅ 已发版；🔧 已修待发；🐛 待查。

---

## 🔧 #5 CF1 配额耗尽 → 消息漏发/迟发（fallback 没分层；已修待发版）

- **来源**：设计复核（2026-07-29）——追查桥 fallback 路径时发现。
- **症状**：多 CF 配置下，CF1 配额耗尽（或部署挂 / 返 402 / 错误页）时：
  - CF1 返 5xx/429/挂起 → 每条消息都得先给 CF1 烧 3 次重试（~2-3s，hang 最坏 ~45s）才 fallback 到 CF2 = **持续迟发**；
  - CF1 返 4xx（402/403）或 200+错误页 → 旧逻辑把 4xx 和"200+Push Kit 错码"都归进 `statusSystemError` 终态、**不 fallback** → CF2 根本不被试 → **该条漏发**（且之后每条都漏，直到 CF1 自愈）。
- **根因**：`postToPushService` 把"CF 平台层故障（非 200）"和"Push Kit 语义拒（200 + 非 success 码）"混进同一个 `statusSystemError` 终态桶 → 前者本该 fallback 却没 fallback。CF 是 Push Kit 的代理，HTTP 状态码已区分了两层，旧代码把这信号扔了。
- **修法（2026-07-29，已实现）**：fallback 决策改成按**失败层**——
  - 加 `statusCfDown`（非 200·硬挂：4xx / 200+非JSON / URL 格式错）→ 立即 fallback、不重试；
  - `statusSystemError` 收窄为"200 + 非 success 码 / 本地 body 序列化错"→ 终态不 fallback（CF2 同果，省 consumption）；
  - 5xx/429/超时/网络 仍 `statusRetry`（重试 ≤3 再 fallback）。
  - 一条规则：**HTTP 200 = Push Kit 已应答 → 停（不 fallback）；HTTP 非 200 = CF 挂 → fallback**。零新状态（无熔断 / cooldown / health map）。
- **测试**：`TestPushNoFallbackOnPushKitError`（200+80300002 → URL2 零调用，省消费）+ `TestPushFallbackOnCfDown`（402 → 立即 fallback，callCount=2 无迟发税）+ `TestPushFallbackOnNonJSON200`（200+HTML → fallback）。`go test` 全过。
- **状态**：🔧 已修（本地，测试过）；⏳ 待发版（下次 tag 带上）。文档已同步 `docs/pushkit-delivery.md` §9。
- **注**：单 CF 配置不触发（无第二个 URL 可 fallback）；CF1 配额若返 5xx/429 仍重试 3 次（迟发税残留，量小 YAGNI 不加熔断）。

---

## 🐛 #4 push 失败 → 消息永久丢（缓修，待做推送可靠性队列）

- **来源**：对抗审查（Go agent，2026-07-27）。
- **症状**：`forward`（`subscriber.go`）先 CAS 推进水位 + 落盘，**然后**才 `sendToHuawei`。若云函数抽风（504/部署中）/ 无设备 / 全员取消订阅 → push 全失败，水位已落盘 → 重启不补 → 这条消息**永久丢**（云函数短暂故障期间的消息全没）。
- **根因**：水位语义是"已尝试"非"已送达"；push 失败后水位仍推进，无重试机制。
- **修法（待做）**：pending 推送队列——push 失败的消息入队重试，水位只在 push 成功后推进。~150 行 + 测试，功能级改动。**Push Kit 的 notifyId(=msg id) 幂等（覆盖式），未来加重试不会重发**——缓修不欠债。
- **状态**：🐛 缓修（记限制；等做"推送可靠性"特性时一起）。

---

## 🔧 #3 重装 Gotify → 水位 stale 永久静默丢（已修，待发版）

- **来源**：对抗审查（Go agent，2026-07-27）+ 用户真机触发（2026-07-27：降级 gotify 后"桥收不到、打不到锁屏"）。
- **症状**：重装/换 Gotify 实例（DB 重建、id 从小重新开始），落盘水位（旧实例高值）仍生效 → 新消息 id≤水位 全被 `forward` CAS 去重吞 → **永久不推**（跟 #1 全量重放是镜像问题：一个重放、一个永漏）。表现 = "桥 WS 绿（订阅正常）但就是不弹锁屏"。
- **根因**：水位语义是"该实例已转发到的最大 id"，但落盘水位不绑定实例身份 → 换实例后旧水位 stale。
- **原修法（废）**：把 `gotify_url` 和水位一起持久化，url 变了重置。**用户否**：重装 gotify 时 URL 通常不变（同域名/IP），url 作实例标识检测不到。
- **现修法（2026-07-27，已实现）**：改用 **id 倒退检测**（与 URL 无关）——Gotify 消息 id 正常单调递增永不倒退，mid < 水位 = DB 重建铁证。两处检测：
  - `forward`（`subscriber.go`）：每条消息必经，mid<水位 → 重置水位到 mid-1 让本条通过（**运行中自愈，不用重启桥、不用手动删文件**）。reset 到 mid-1 即便 =0 也安全（backfill 零水位 guard 兜底，不重放）。
  - `initLastID`（`subscriber.go`）：启动时拉 `recentMessages(1)` 对比落盘水位，落盘>当前 → 重置（重启场景）。
  - 不回放历史（宁可漏断档绝不重放，同 #1 哲学）。代价：initLastID 多一次 GET /message（仅启动/配置变更时，不频繁）。
- **测试**：`TestForwardDetectsIDRollback`（水位 300 + URL 不变 → id=5 自愈转发，pushCount=1）+ `TestInitLastIDDetectsRollback`（落盘 500 vs Gotify 当前 10 → 重置到 10）。`go test` 全过。
- **即时解法（旧版本也能用）**：用户当前就在踩、不想等发版 → 停桥 → 删 `last_msg_id` → 启桥（initLastID 重新取当前最新 id，绕过 stale）。
- **状态**：🔧 已修（本地，测试过）；⏳ 待发版（下次 tag 带上）。

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
