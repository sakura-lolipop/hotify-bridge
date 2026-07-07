package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
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

// TestSubscribeNoOriginHeader — ★ hazard 2（killer）：Go WS 升级请求不发 Origin。
// 同时验 initLastID + 订阅 + forward（lastMsgID 更新）。
func TestSubscribeNoOriginHeader(t *testing.T) {
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

	// 发实时消息 id=200，验 forward（initLastID 已设 lastMsgID=100，200>100 → 转发）
	mock.msgCh <- GotifyMessage{ID: 200, Date: "2026-07-07T13:33:16.6428496+08:00", Title: "实时"}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && lastMsgID.Load() != 200 {
		time.Sleep(20 * time.Millisecond)
	}
	if lastMsgID.Load() != 200 {
		t.Fatalf("forward 未执行：lastMsgID=%d（期望 200）", lastMsgID.Load())
	}
	t.Logf("✓ forward：实时消息 id=200 已收 + 去重（lastMsgID=%d）", lastMsgID.Load())
}

// TestBackfill — 回补：lastMsgID=100，/message 返回 [300, 200]（desc），backfill 升序补 200→300。
func TestBackfill(t *testing.T) {
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
