package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ──────────────────────────── 推送常量（对齐 Python）────────────────────────────
const (
	notifyCategory    = "SUBSCRIPTION" // 通知类目，须与已开通自分类权益一致
	testMessage       = false          // 有自分类权益→False（服务/通讯类无频控）
	pushRetryLimit    = 3              // 502/超时重试次数（同 notifyId 幂等，Push Kit 原生覆盖防重）
	pushRetryInterval = 1 * time.Second // 固定间隔（量小 YAGNI，不指数退避）
)

// deadTokenCodes — 死-token 白名单（仅这两个码语义=token 无效，≈ APNs Unregistered）。
// 鉴权 802x/权益 80300002/超长 80300008/频控/系统错 81xxxxx 都跟 token 死活无关——误删丢好 token。
var deadTokenCodes = map[string]bool{"80100000": true, "80300007": true}

// pushStatus — 推送结果分类（对齐 Python _post_to_push_service 返回的 status 字符串）。
type pushStatus int

const (
	statusDelivered   pushStatus = iota // 80000000
	statusDead                          // 80100000/80300007
	statusSystemError                   // 其他 code / HTTP 5xx/401/400（保留 token）
	statusRetry                         // 502 / 网络异常（重试）
)

// ──────────────────────────── Push Kit notification / body 结构（★ 多 hazard 落点）────────────────────────────

// ClickAction — ★ hazard 1：data.ts 透传 msg.Date 字符串（绝不解析成 time.Time）。
// actionType 必须 0（1 要 action/uri → 80100003）。
type ClickAction struct {
	ActionType int               `json:"actionType"`
	Data       map[string]string `json:"data"` // {ts: <Gotify msg.Date 原值>}
}

// Notification — Push Kit notification 对象（云函数原样透传不解释）。
// ★ hazard 5：NotifyID omitempty——0 省略（Push Kit 自动分配），非 0（Gotify msgId）出现（重试同 id → 原生覆盖防重）。
type Notification struct {
	Category    string      `json:"category"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	ClickAction ClickAction `json:"clickAction"`
	NotifyID    int         `json:"notifyId,omitempty"`
}

// PushRequestBody — 桥→云函数 POST body。
// ★ hazard 3：Data 是 JSON 字符串非对象（json.Marshal(extras) 嵌入；对象型被 Push Kit 拒）。
type PushRequestBody struct {
	Token        string      `json:"token"`
	Notification Notification `json:"notification"`
	Data         string      `json:"data"`
	TestMessage  bool        `json:"testMessage"`
}

// ──────────────────────────── 单次 POST（镜像 Python _post_to_push_service）────────────────────────────
// 返回 (status, code_str, msg)。status ∈ {delivered, dead, system_error, retry}。
func postToPushService(url, cfToken string, body *PushRequestBody) (pushStatus, string, string) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return statusSystemError, "", "body 序列化失败: " + err.Error()
	}
	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		// URL 缺 scheme 等 → SystemError 不重试（防 ValueError 窜）
		return statusSystemError, "", "URL 格式错（检查 cloud_function_urls 带 https://）: " + err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	if cfToken != "" { // ★ hazard 4：空 token 不发 Auth（发空 "Bearer " 过不了云函数精确匹配）
		req.Header.Set("Authorization", "Bearer "+cfToken)
	}
	client := &http.Client{Timeout: 15 * time.Second} // 15s：云函数内部 10s 调 Push Kit + 余量
	resp, err := client.Do(req)
	if err != nil {
		return statusRetry, "", fmt.Sprintf("%T: %v", err, err) // 网络异常/超时 → 重试
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readSnippet(resp.Body, 160)
		switch resp.StatusCode {
		case 502:
			return statusRetry, "", "HTTP 502 " + snippet // Push Kit HTTP 错/超时 → 重试
		case 401:
			return statusSystemError, "", "HTTP 401 unauthorized（cloud_function_token 配错？）" + snippet
		case 400:
			return statusSystemError, "", "HTTP 400 bad request " + snippet
		default:
			return statusSystemError, "", fmt.Sprintf("HTTP %d %s", resp.StatusCode, snippet)
		}
	}

	// HTTP 200：解析 Push Kit 原始 code（★ hazard 6：code 防御性兼容 string/int，对齐 Python str(resp.get("code"))）
	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Code any    `json:"code"` // any → 类型断言（string 或 float64）
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return statusSystemError, "", "HTTP 200 但 body 非 JSON：" + truncate(string(respBody), 160)
	}
	code := anyToCodeStr(result.Code)
	if code == "80000000" {
		return statusDelivered, code, result.Msg
	}
	if deadTokenCodes[code] {
		return statusDead, code, result.Msg
	}
	return statusSystemError, code, fmt.Sprintf("code=%s msg=%s", code, result.Msg)
}

// anyToCodeStr — code 字段（string 或 JSON number→float64）转字符串（对齐 Python str()）。
// float64 须 FormatFloat 'f' 否则 80000000→"8e+07"（科学计数法比不等）。
func anyToCodeStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// ──────────────────────────── 转发一条消息到所有设备（镜像 Python send_to_huawei）────────────────────────────
// 流程（docs/pushkit-delivery.md）：① 构造 notification（clickAction.data={ts}）② 逐设备遍历 cloud_function_urls
// fallback + 每 URL 重试 ≤3（同 notifyId 幂等）③ 全局闸门：本轮 delivered==0 则不删任何死 token（hazard 11）。
func sendToHuawei(title, message string, priority int, extras json.RawMessage, ts string, notifyID int) {
	_ = priority // notification 不含 priority（Push Kit 不需要；保留签名对齐 Python）
	cfgMu.RLock()
	urls := cfg.CloudFunctionURLs
	cfToken := cfg.CloudFunctionToken
	subLabel := cfg.SubscribeLabel
	cfgMu.RUnlock()

	if len(urls) == 0 {
		log.Printf("[PushKit] ⏭ 跳过推送（cloud_function_urls 未配置）：%s | %s", orVal(title, "(无标题)"), truncate(message, 40))
		return
	}
	devs := loadTokens()
	if len(devs) == 0 {
		log.Print("[PushKit] 还没注册设备，跳过")
		return
	}

	// subscribe_label 前缀（★ hazard 14：{true,1,yes,on} 小写，不用 strconv.ParseBool——它拒 yes/on）
	if subscribeLabelEnabled(subLabel) {
		if title != "" {
			title = "订阅:" + title
		} else {
			message = strings.TrimSpace("订阅:" + message)
		}
	}

	notifyIDInt := notifyID // 0 → omitempty 省略；Gotify msgId 非 0 → 出现（重试同 id 幂等）
	subStatus := loadSubscribeStatus()

	// data 字符串（★ hazard 3）：extras → map → json.Marshal → string。nil/空 → "{}"。
	var dataObj map[string]any
	if len(extras) > 0 {
		_ = json.Unmarshal(extras, &dataObj)
	}
	if dataObj == nil {
		dataObj = map[string]any{}
	}
	extrasBytes, _ := json.Marshal(dataObj)
	dataStr := string(extrasBytes)

	delivered := 0
	var dead []string

	for devID, tok := range devs {
		// 订阅状态过滤：显式 False = 取消订阅 → 跳过。未记录 → 默认订阅（不破坏老设备/首装未上报）。
		if v, ok := subStatus[devID]; ok && !v {
			continue
		}

		notification := Notification{
			Category: notifyCategory,
			Title:     orVal(title, "Hotify"),
			Body:      message,
			ClickAction: ClickAction{
				ActionType: 0, // 必须 0（1 要 action/uri → 80100003）
				Data:       map[string]string{"ts": ts}, // ★ hazard 1：ts 原值透传
			},
			NotifyID: notifyIDInt,
		}
		body := &PushRequestBody{
			Token:        tok,
			Notification: notification,
			Data:         dataStr, // ★ hazard 3：JSON 字符串
			TestMessage:  testMessage,
		}

		// 遍历 URLs（fallback）：retry 用尽才试下一个 URL；delivered/dead/system_error 终态即出。
		finalStatus, finalMsg := statusRetry, ""
		for _, u := range urls {
			attemptStatus, attemptMsg := statusRetry, ""
			for attempt := 1; attempt <= pushRetryLimit; attempt++ {
				st, code, msg := postToPushService(u, cfToken, body)
				attemptStatus, attemptMsg = st, msg
				switch st {
				case statusDelivered:
					log.Printf("[PushKit] ✓ %s code=80000000  (url=%s)", devID, u)
				case statusDead:
					log.Printf("[PushKit] ✗ %s code=%s msg=%s → 该 token 无效  (url=%s)", devID, code, msg, u)
				case statusSystemError:
					log.Printf("[PushKit] ⚠️ %s %s → 保留（非死-token，疑系统/参数问题）  (url=%s)", devID, msg, u)
				case statusRetry:
					if attempt < pushRetryLimit {
						log.Printf("[PushKit] ↻ %s %s → 重试 %d/%d  (url=%s)", devID, msg, attempt+1, pushRetryLimit, u)
						time.Sleep(pushRetryInterval)
					}
				}
				if st == statusDelivered || st == statusDead || st == statusSystemError {
					break // 终态 → 出重试循环
				}
			}
			finalStatus, finalMsg = attemptStatus, attemptMsg
			if attemptStatus == statusDelivered || attemptStatus == statusDead || attemptStatus == statusSystemError {
				break // 终态 → 不 fallback 下一个 URL
			}
			// retry 用尽 3 次 → 试下一个 URL（fallback）
		}

		switch finalStatus {
		case statusDelivered:
			delivered++
		case statusDead:
			dead = append(dead, devID)
		case statusRetry:
			log.Printf("[PushKit] ✗ %s 所有 URL 重试用尽仍失败 → 保留 token（下次再推）：%s", devID, finalMsg)
			// system_error 已在上面打印过
		}
	}

	// 死 token 清理 + 全局闸门（★ hazard 11）
	if len(dead) > 0 {
		if delivered == 0 {
			log.Printf("[PushKit] ⚠️ 本轮 0 台成功，疑系统性故障，保留全部 %d 个疑似失效 token（不删）：%v", len(dead), dead)
			dead = dead[:0] // 一台都不删（防 app 包名配错全台返 80300007 被误触发全锅端）
		} else {
			t := loadTokens() // 重新读（期间可能有新 register），避免覆盖
			for _, d := range dead {
				delete(t, d)
			}
			saveTokens(t)
			log.Printf("[PushKit] 清理 %d 个失效 token：%v", len(dead), dead)
		}
	}
	suffix := ""
	if len(dead) > 0 {
		suffix = fmt.Sprintf("，%d 失效已清", len(dead))
	}
	log.Printf("[PushKit] 推送完成：%d 台成功%s", delivered, suffix)
}

// subscribeLabelEnabled — ★ hazard 14：真值集 {true,1,yes,on}（小写）。不用 strconv.ParseBool（它拒 yes/on）。
func subscribeLabelEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// readSnippet — 读响应 body 前 n 字符（错误诊断用）。
func readSnippet(r io.Reader, n int) string {
	buf := make([]byte, n)
	m, _ := r.Read(buf)
	return string(buf[:m])
}

// truncate — rune 安全截断（中文字符不劈）。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// orVal — s 非空返 s，否则返 def（对齐 Python title or "Hotify"）。
func orVal(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
