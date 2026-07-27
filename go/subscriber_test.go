package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockGotify — 轻量 Gotify 模拟（/stream WS + /message 历史 + /version）。
// /stream 升级时记录 Origin 头（验 hazard 2：Go 桥不发 Origin，对齐 Python websockets）。
type mockGotify struct {
	server   *httptest.Server
	mu       sync.Mutex
	origin   string // 升级请求的 Origin 头（"" = 缺席）
	streamCh chan struct{}
	msgCh    chan GotifyMessage
	messages []GotifyMessage // /message 返回（desc，newest first）
}

func newMockGotify(t *testing.T, messages []GotifyMessage) *mockGotify {
	t.Helper()
	m := &mockGotify{
		streamCh: make(chan struct{}, 8),
		msgCh:    make(chan GotifyMessage, 8),
		messages: messages,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"mock"}`))
	})
	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": m.messages})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.origin = r.Header.Get("Origin")
		m.mu.Unlock()
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		m.streamCh <- struct{}{}
		for msg := range m.msgCh {
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	})
	m.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		m.server.Close()
		close(m.msgCh)
	})
	return m
}

func (m *mockGotify) originHeader() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.origin
}

// resetWMState — 测试间隔离：清高水位 + 删水位/设备持久化文件（防互相污染）。
// 必要：forward 现在会落盘 last_msg_id；不重置会让 initLastID 读到上一轮残留、串测试。
func resetWMState() {
	lastMsgID.Store(0)
	_ = os.Remove(lastMsgIDFile)
	_ = os.Remove(pushTokensFile)
}

// TestSubscribeNoOriginHeader — ★ hazard 2（killer）：Go WS 升级请求不发 Origin。
// 同时验 initLastID + 订阅 + forward（lastMsgID 更新 + 落盘）。
func TestSubscribeNoOriginHeader(t *testing.T) {
	resetWMState()
	hist := []GotifyMessage{{ID: 100, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "hist"}}
	mock := newMockGotify(t, hist)

	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "test-token"}
	cfgMu.Unlock()
	lastMsgID.Store(0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go keepSubscribed(ctx)

	select {
	case <-mock.streamCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: /stream 未连上")
	}

	// ★ hazard 2：Origin 必须缺席
	if origin := mock.originHeader(); origin != "" {
		t.Fatalf("★ hazard 2 失败：Go WS 升级请求发了 Origin=%q（应不发，对齐 Python websockets；Gotify 空 Origin=放行）", origin)
	}
	t.Logf("✓ hazard 2：Origin 缺席（Go 桥不发 Origin，与 Python websockets 字节级一致）")

	// 发实时消息 id=200，验 forward（initLastID 已设 lastMsgID=100，200>100 → 转发 + 落盘）
	mock.msgCh <- GotifyMessage{ID: 200, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "实时"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && lastMsgID.Load() != 200 {
		time.Sleep(20 * time.Millisecond)
	}
	if lastMsgID.Load() != 200 {
		t.Fatalf("forward 未执行：lastMsgID=%d（期望 200）", lastMsgID.Load())
	}
	if got := loadLastMsgID(); got != 200 {
		t.Fatalf("forward 后 last_msg_id 落盘应为 200，实际=%d", got)
	}
	t.Logf("✓ forward：实时消息 id=200 已收 + 去重 + 落盘（lastMsgID=%d）", lastMsgID.Load())
}

// TestBackfill — 回补：lastMsgID=100，/message 返回 [300, 200]（desc），backfill 升序补 200→300。
func TestBackfill(t *testing.T) {
	resetWMState()
	msgs := []GotifyMessage{
		{ID: 300, Date: "2026-07-07T13:33:17.0000000+08:00", Title: "m3"},
		{ID: 200, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "m2"},
	}
	mock := newMockGotify(t, msgs)
	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "test-token"}
	cfgMu.Unlock()
	lastMsgID.Store(100)

	backfill()

	if lastMsgID.Load() != 300 {
		t.Fatalf("backfill 后 lastMsgID=%d（期望 300，应升序补 200→300）", lastMsgID.Load())
	}
	t.Logf("✓ backfill：lastMsgID 100→%d（漏的 200、300 升序补推，去重正确）", lastMsgID.Load())
}

// TestInitLastID — 高水位设到最新 id（不回放历史）。
func TestInitLastID(t *testing.T) {
	resetWMState()
	msgs := []GotifyMessage{{ID: 999, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "newest"}}
	mock := newMockGotify(t, msgs)
	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "test-token"}
	cfgMu.Unlock()
	lastMsgID.Store(0)

	initLastID()

	if lastMsgID.Load() != 999 {
		t.Fatalf("initLastID 后 lastMsgID=%d（期望 999）", lastMsgID.Load())
	}
	t.Logf("✓ initLastID：lastMsgID→%d（不回放历史）", lastMsgID.Load())
}

// TestInitLastIDRestoresPersisted — 落盘水位优先：有 last_msg_id 时 initLastID 用它续传（不取最新、不回放）。
func TestInitLastIDRestoresPersisted(t *testing.T) {
	resetWMState()
	saveLastMsgID(500) // 模拟上一轮落盘水位
	msgs := []GotifyMessage{{ID: 999, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "newest"}}
	mock := newMockGotify(t, msgs) // 即使 Gotify 有 999，也应用落盘的 500
	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "test-token"}
	cfgMu.Unlock()
	lastMsgID.Store(0)

	initLastID()

	if lastMsgID.Load() != 500 {
		t.Fatalf("有落盘水位时应续传 500，实际=%d", lastMsgID.Load())
	}
	t.Logf("✓ initLastID 落盘优先：lastMsgID→%d（重启续传，不取最新 999）", lastMsgID.Load())
}

// TestBackfillGuardZeroWatermark — ★ 防"桥重启撞 Gotify 不可达 → initLastID 失败 → 水位=0 → 首次回补全量重放"bug：
// 水位=0 + 有历史时，backfill 不应重放，应把水位设到本批最大 id、一条不推。
// 注册一台假设备 + 计数云函数 → bug 重现会 push 2 条（200、300）；兜底后应 0 条。
func TestBackfillGuardZeroWatermark(t *testing.T) {
	resetWMState()
	msgs := []GotifyMessage{
		{ID: 300, Date: "2026-07-07T13:33:17.0000000+08:00", Title: "m3"},
		{ID: 200, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "m2"},
	}
	mock := newMockGotify(t, msgs)

	var pushCount int32
	cfMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCount, 1)
		_, _ = w.Write([]byte(`{"code":"80000000"}`))
	}))
	t.Cleanup(cfMock.Close)

	saveTokens(map[string]string{"dev1": "fake-push-token"}) // 注册一台设备，不然无设备直接跳过、测不出重放

	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "t", CloudFunctionURLs: []string{cfMock.URL}}
	cfgMu.Unlock()
	lastMsgID.Store(0)

	backfill()

	if lastMsgID.Load() != 300 {
		t.Fatalf("水位应设到本批最大 id=300，实际=%d", lastMsgID.Load())
	}
	if n := atomic.LoadInt32(&pushCount); n != 0 {
		t.Fatalf("★ 防 bug 失败：水位=0 时 backfill 重放了 %d 条（应 0，不重放历史）", n)
	}
	t.Logf("✓ 水位=0 兜底：lastMsgID→%d，未重放任何消息（pushCount=0）", lastMsgID.Load())
}

// TestForwardDetectsIDRollback — ★ bug.md #3 修法验证（运行中自愈）：水位 300（旧 Gotify 实例残留），
// Gotify 重装清库后 /stream 推来 id=5（< 水位）→ forward 必须检测到 id 倒退、重置水位、正常转发，
// 而不是被 mid<=cur 去重吞掉 → 静默丢。注册假设备 + 计数云函数验真推送了。
// 【关键】URL 全程不变——证明修法不依赖 url 作实例标识（用户点破的原 url 方案失效场景）。
func TestForwardDetectsIDRollback(t *testing.T) {
	resetWMState()
	var pushCount int32
	cfMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pushCount, 1)
		_, _ = w.Write([]byte(`{"code":"80000000"}`))
	}))
	t.Cleanup(cfMock.Close)
	saveTokens(map[string]string{"dev1": "fake-push-token"}) // 注册一台设备，否则无设备跳过推送测不出
	cfgMu.Lock()
	cfg = Config{CloudFunctionURLs: []string{cfMock.URL}}
	cfgMu.Unlock()
	lastMsgID.Store(300) // 旧 Gotify 实例残留水位

	forward(GotifyMessage{ID: 5, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "重装后新消息"}, "实时")

	if lastMsgID.Load() != 5 {
		t.Fatalf("id 倒退应重置水位并转发 id=5，实际 lastMsgID=%d（被去重吞了？bug #3 未修）", lastMsgID.Load())
	}
	if n := atomic.LoadInt32(&pushCount); n != 1 {
		t.Fatalf("应推送 1 次，实际 %d 次（id 倒退的消息没转发——bug #3 静默丢未修）", n)
	}
	t.Logf("✓ id 倒退运行中自愈：水位 300→%d，id=5 正常转发（pushCount=1，URL 不变也生效）", lastMsgID.Load())
}

// TestInitLastIDDetectsRollback — ★ bug.md #3 启动检测：落盘水位 500（旧实例），Gotify 当前最新 id=10
// （重装清库后）→ initLastID 检测到 落盘 > 当前 → 重置到 10（不续传 500、不回放历史）。
func TestInitLastIDDetectsRollback(t *testing.T) {
	resetWMState()
	saveLastMsgID(500) // 旧实例落盘水位
	msgs := []GotifyMessage{{ID: 10, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "重装后最新"}}
	mock := newMockGotify(t, msgs) // Gotify 当前最新 id=10
	cfgMu.Lock()
	cfg = Config{GotifyURL: mock.server.URL, GotifyToken: "t"}
	cfgMu.Unlock()
	lastMsgID.Store(0)

	initLastID()

	if lastMsgID.Load() != 10 {
		t.Fatalf("落盘 500 > Gotify 当前 10 应重置到 10，实际 lastMsgID=%d", lastMsgID.Load())
	}
	if got := loadLastMsgID(); got != 10 {
		t.Fatalf("重置后 last_msg_id 落盘应为 10，实际=%d", got)
	}
	t.Logf("✓ 启动检测 id 倒退：落盘 500 vs Gotify 当前 10 → 重置水位→%d（落盘同步，不续传旧 500）", lastMsgID.Load())
}
