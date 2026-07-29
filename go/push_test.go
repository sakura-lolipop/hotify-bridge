package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mockResp — mock 云函数返回：statusCode（200=正常返 code；502/401 等=返状态码）。
type mockResp struct {
	statusCode int
	code       string // 200 时的 Push Kit code
}

// mockCloudFn — 模拟云函数：捕获 POST body + headers，按队列返响应。
type mockCloudFn struct {
	server    *httptest.Server
	mu        sync.Mutex
	bodies    []string
	headers   []http.Header
	responses []mockResp
	calls     int
}

func newMockCloudFn(t *testing.T, responses []mockResp) *mockCloudFn {
	t.Helper()
	m := &mockCloudFn{responses: responses}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/push", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.bodies = append(m.bodies, string(body))
		m.headers = append(m.headers, r.Header.Clone())
		resp := mockResp{statusCode: 200, code: "80000000"}
		if m.calls < len(m.responses) {
			resp = m.responses[m.calls]
		}
		m.calls++
		m.mu.Unlock()
		if resp.statusCode != 200 {
			w.WriteHeader(resp.statusCode)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"code":"%s","msg":"ok"}`, resp.code)))
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockCloudFn) lastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.bodies) == 0 {
		return ""
	}
	return m.bodies[len(m.bodies)-1]
}
func (m *mockCloudFn) lastHeaders() http.Header {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.headers) == 0 {
		return nil
	}
	return m.headers[len(m.headers)-1]
}
func (m *mockCloudFn) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// setCfg — 测试用设 cfg（加锁）。
func setCfg(t *testing.T, urls []string, cfToken, subLabel string) {
	t.Helper()
	cfgMu.Lock()
	cfg = Config{CloudFunctionURLs: urls, CloudFunctionToken: cfToken, SubscribeLabel: subLabel}
	cfgMu.Unlock()
}

// TestPushBodyAndTs — ★ hazard 1/3/4/5：POST body 形状 + ts 不透明透传。
func TestPushBodyAndTs(t *testing.T) {
	dir := chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80000000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "cf-tok-123", "false") // subscribe_label=false 不加前缀
	saveTokens(map[string]string{"dev1": "push-tok-A"})

	ts := "2026-07-07T13:33:16.6428496+08:00" // ★ ISO8601+纳秒（App 精确匹配的 key）
	sendToHuawei("标题", "正文", 4, json.RawMessage(`{"k":"v"}`), ts, 42)

	body := mock.lastBody()
	if body == "" {
		t.Fatal("未收到 POST")
	}
	var got struct {
		Token        string `json:"token"`
		Notification struct {
			Category    string `json:"category"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			ClickAction struct {
				ActionType int               `json:"actionType"`
				Data       map[string]string `json:"data"`
			} `json:"clickAction"`
			NotifyID int `json:"notifyId"`
		} `json:"notification"`
		Data        string `json:"data"`
		TestMessage bool   `json:"testMessage"`
	}
	if json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("body 非 JSON: %s", body)
	}
	// ★ hazard 1：ts 原值透传（7 位小数全在，未遭 time.Time 重格式化）
	if got.Notification.ClickAction.Data["ts"] != ts {
		t.Fatalf("★ hazard 1 失败：ts=%q（期望 %q，须不透明透传）", got.Notification.ClickAction.Data["ts"], ts)
	}
	if got.Notification.ClickAction.ActionType != 0 {
		t.Fatalf("actionType=%d（期望 0）", got.Notification.ClickAction.ActionType)
	}
	if got.Notification.Category != "SUBSCRIPTION" {
		t.Fatalf("category=%q（期望 SUBSCRIPTION）", got.Notification.Category)
	}
	if got.Notification.Title != "标题" {
		t.Fatalf("title=%q（期望「标题」，subscribe_label=false 不加前缀）", got.Notification.Title)
	}
	if got.Notification.NotifyID != 42 {
		t.Fatalf("notifyId=%d（期望 42）", got.Notification.NotifyID)
	}
	if got.Token != "push-tok-A" {
		t.Fatalf("token=%q", got.Token)
	}
	// ★ hazard 3：data 是 JSON 字符串（非对象）
	var dataInner map[string]any
	if err := json.Unmarshal([]byte(got.Data), &dataInner); err != nil {
		t.Fatalf("★ hazard 3 失败：data 不是合法 JSON 字符串: %q（err %v）", got.Data, err)
	}
	if dataInner["k"] != "v" {
		t.Fatalf("data 内容错: %v", dataInner)
	}
	// ★ hazard 4：Auth 头带（cfToken 非空）
	if ah := mock.lastHeaders().Get("Authorization"); ah != "Bearer cf-tok-123" {
		t.Fatalf("★ hazard 4 失败：Authorization=%q（期望 Bearer cf-tok-123）", ah)
	}
	t.Logf("✓ hazard 1/3/4/5：ts 透传 + data 字符串 + Auth 带 + notifyId=42")
	_ = dir
}

// TestPushNotifyIdOmitempty — ★ hazard 5：notifyID=0 → body 无 notifyId 字段。
func TestPushNotifyIdOmitempty(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80000000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "2026-07-07T13:33:16.6428496+08:00", 0)

	if strings.Contains(mock.lastBody(), "notifyId") {
		t.Fatalf("★ hazard 5 失败：notifyID=0 时 body 不该含 notifyId：%s", mock.lastBody())
	}
	t.Logf("✓ hazard 5：notifyID=0 → body 无 notifyId 字段（omitempty）")
}

// TestPushAuthOmitted — ★ hazard 4：cfToken 空 → 不发 Authorization 头。
func TestPushAuthOmitted(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80000000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "", "false") // 空 token
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if ah := mock.lastHeaders().Get("Authorization"); ah != "" {
		t.Fatalf("★ hazard 4 失败：cfToken 空时不该发 Auth，得 %q", ah)
	}
	t.Logf("✓ hazard 4：cfToken 空 → 不发 Authorization（空 Bearer 过不了云函数精确匹配）")
}

// TestPushDeliveredRetainsToken — code 80000000 → token 保留。
func TestPushDeliveredRetainsToken(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80000000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if _, ok := loadTokens()["dev1"]; !ok {
		t.Fatal("delivered 后 token 被误删")
	}
	t.Logf("✓ delivered：token 保留")
}

// TestPushDeadTokenRemoved — code 80100000 → token 删除（delivered>0 闸门放行）。
func TestPushDeadTokenRemoved(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80000000"}, {200, "80100000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A", "dev2": "B"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	// Go map 迭代序随机，不假定哪个设备拿 80000000 / 80100000；只验：删 1 留 1。
	toks := loadTokens()
	if len(toks) != 1 {
		t.Fatalf("该删 1 个死 token（80100000）、留 1 个（delivered），剩 %d 个（%v）", len(toks), toks)
	}
	t.Logf("✓ dead token：1 个（80100000）已删，1 个（delivered）保留：剩 %v", toks)
}

// TestPushGlobalGate — ★ hazard 11：全设备 80300007 + delivered=0 → 一台都不删（防系统性故障灭全量）。
func TestPushGlobalGate(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{200, "80300007"}, {200, "80300007"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A", "dev2": "B"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	toks := loadTokens()
	if len(toks) != 2 {
		t.Fatalf("★ hazard 11 失败：本轮 0 成功，死 token 不该删，但剩 %d 个（dev1=%v dev2=%v）", len(toks), toks["dev1"], toks["dev2"])
	}
	t.Logf("✓ hazard 11：全 80300007 + 0 delivered → 保留全部（防全锅端）")
}

// TestPushRetryThenDelivered — 502→重试→80000000。callCount=2。
func TestPushRetryThenDelivered(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{502, ""}, {200, "80000000"}})
	setCfg(t, []string{mock.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if cc := mock.callCount(); cc != 2 {
		t.Fatalf("retry 路径 callCount=%d（期望 2：502 重试 + 80000000）", cc)
	}
	t.Logf("✓ retry：502→重试→80000000（callCount=2）")
}

// TestPushFallback — URL1 502×3 用尽 → fallback URL2 80000000。callCount=4。
func TestPushFallback(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{502, ""}, {502, ""}, {502, ""}, {200, "80000000"}})
	// 同一 mock URL 列两次（测 fallback 逻辑：URL1 重试用尽→试 URL2）
	url := mock.server.URL + "/api/push"
	setCfg(t, []string{url, url}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if cc := mock.callCount(); cc != 4 {
		t.Fatalf("fallback callCount=%d（期望 4：URL1 502×3 + URL2 80000000）", cc)
	}
	t.Logf("✓ fallback：URL1 502×3 用尽 → URL2 80000000（callCount=4）")
}

// TestPushNoFallbackOnPushKitError — ★ 省消费核心：URL1 返 200+Push Kit 错码（CF 健康、Push Kit 拒）
// → 不 fallback URL2（CF2 会同果）。URL1 只调 1 次（终态不重试），URL2 零调用，token 保留。
func TestPushNoFallbackOnPushKitError(t *testing.T) {
	chdirTemp(t)
	mock1 := newMockCloudFn(t, []mockResp{{200, "80300002"}}) // URL1：Push Kit 权益拒绝，CF 本身健康
	mock2 := newMockCloudFn(t, []mockResp{{200, "80000000"}}) // URL2：不该被调
	setCfg(t, []string{mock1.server.URL + "/api/push", mock2.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if cc := mock2.callCount(); cc != 0 {
		t.Fatalf("★ 省消费失败：200+Push Kit 错码不该 fallback，但 URL2 被调了 %d 次", cc)
	}
	if cc := mock1.callCount(); cc != 1 {
		t.Fatalf("URL1 该调 1 次（终态不重试不 fallback），实际 %d", cc)
	}
	if _, ok := loadTokens()["dev1"]; !ok {
		t.Fatal("200+Push Kit 错码（system_error）不该删 token")
	}
	t.Logf("✓ 省消费：URL1 返 200+80300002（Push Kit 拒）→ 不 fallback URL2（CF2 同果，零浪费）")
}

// TestPushFallbackOnCfDown — ★ CF 平台层故障：URL1 返 402（配额耗尽）→ 不重试、立即 fallback URL2。
// 关键：4xx 不重试（不会 1s 自愈），URL1 只调 1 次（非 3 次）→ 无迟发税，总 callCount=2。
func TestPushFallbackOnCfDown(t *testing.T) {
	chdirTemp(t)
	mock := newMockCloudFn(t, []mockResp{{402, ""}, {200, "80000000"}})
	url := mock.server.URL + "/api/push" // 同一 mock 当 URL1+URL2
	setCfg(t, []string{url, url}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if cc := mock.callCount(); cc != 2 {
		t.Fatalf("4xx fallback callCount=%d（期望 2：URL1 402×1 不重试 + URL2 80000000）", cc)
	}
	t.Logf("✓ CF 平台层故障：URL1 402 → 不重试立即 fallback URL2（callCount=2，无迟发税）")
}

// TestPushFallbackOnNonJSON200 — URL1 返 200 但 body 非 JSON（CF 异常/错误页）→ cfDown → fallback URL2。
func TestPushFallbackOnNonJSON200(t *testing.T) {
	chdirTemp(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>quota exceeded error page</html>"))
	}))
	t.Cleanup(bad.Close)
	mock2 := newMockCloudFn(t, []mockResp{{200, "80000000"}})
	setCfg(t, []string{bad.URL, mock2.server.URL + "/api/push"}, "tok", "false")
	saveTokens(map[string]string{"dev1": "A"})

	sendToHuawei("t", "m", 4, nil, "ts", 1)

	if cc := mock2.callCount(); cc != 1 {
		t.Fatalf("200+非JSON 该 fallback URL2，URL2 调用 %d 次（期望 1）", cc)
	}
	t.Logf("✓ CF 异常：URL1 200+非JSON（错误页）→ cfDown fallback URL2")
}
