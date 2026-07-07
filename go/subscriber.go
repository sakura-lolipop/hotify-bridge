package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ──────────────────────────── Gotify 消息（★ hazard 1：ts 当不透明字符串透传）────────────────────────────
// Date 绝不用 time.Time——RFC3339Nano 削尾零会让 App 的 m.date===ts 精确匹配静默失配（点通知不滚）。
// Date 原样从 /stream 读、原样塞进 clickAction.data.ts，端到端不解析。
type GotifyMessage struct {
	ID       int             `json:"id"`
	Date     string          `json:"date"`     // ★ 不透明字符串（ISO8601+纳秒，如 2026-07-07T13:33:16.6428496+08:00）
	Title    string          `json:"title"`
	Message  string          `json:"message"`
	Priority int             `json:"priority"`
	Extras   json.RawMessage `json:"extras"` // RawMessage 保留原样（CP3 push 时 marshal 进 data 字符串）
}

// lastMsgID — 已转发消息最高 id（高水位：去重 + 回补边界）。live /stream 与 backfill 共享单变量（hazard 10，
// 防重连双发）。atomic.Int64 CAS 更新。
var lastMsgID atomic.Int64

// ──────────────────────────── 连 Gotify 的地址 + TLS（镜像 Python _gotify_connect_url / _gotify_ssl_ctx）────────────────────────────

// gotifyConnectURL — gotify_url_local（部署者填，同机覆盖）> gotify_url（App 上报的域名）。
func gotifyConnectURL() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	if cfg.GotifyURLLocal != "" {
		return cfg.GotifyURLLocal
	}
	return cfg.GotifyURL
}

// buildWSURL — 显式 scheme swap（hazard 16：不用 strings.Replace(base,"http","ws")，那个对含 "http" 的 host 有 latent bug）。
// https://→wss://，http://→ws://，加 /stream?token=<urlencoded>。
func buildWSURL(base, token string) string {
	var ws string
	switch {
	case strings.HasPrefix(base, "https://"):
		ws = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		ws = "ws://" + strings.TrimPrefix(base, "http://")
	default:
		ws = base // normalize 后不该到这
	}
	return ws + "/stream?token=" + url.QueryEscape(token)
}

// isPrivateIP — 私网 IP（hazard 8：127/10.x/172.16-31/192.168/localhost/::1）→ 跳 TLS 主机名校验。
// ★ Go net.IP.IsPrivate() 不含 loopback（127.0.0.0/8）！故 127.0.0.1/localhost/::1 显式判必须，非风格。
func isPrivateIP(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // 域名（非 IP）→ 公网，不跳
	}
	return ip.IsPrivate() // 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
}

// gotifyTLSConfig — 连 Gotify 的 TLS 配置（按 gotifyConnectURL 判）。
// 明文 http/ws → nil（无 TLS）；https/wss + 私网 IP → InsecureSkipVerify（同机/LAN 证书主机名对不上，私网可信）；
// https/wss + 公网域名 → nil（默认全验，= Python create_default_context）。
func gotifyTLSConfig() *tls.Config {
	u := gotifyConnectURL()
	if !strings.HasPrefix(u, "https://") {
		return nil // ws/http → 无 TLS
	}
	if isPrivateIP(extractHost(u)) {
		return &tls.Config{InsecureSkipVerify: true}
	}
	return nil // 公网 → 默认全验
}

// extractHost — URL 取 hostname（Python urlparse().hostname）。
func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// recentMessages — GET /message（最新在前 desc）。无配置返回 nil。timeout 10s。
func recentMessages(limit int) []GotifyMessage {
	base := gotifyConnectURL()
	cfgMu.RLock()
	tok := cfg.GotifyToken
	cfgMu.RUnlock()
	if base == "" || tok == "" {
		return nil
	}
	u := fmt.Sprintf("%s/message?token=%s&limit=%d", base, url.QueryEscape(tok), limit)
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: gotifyTLSConfig()},
	}
	resp, err := client.Get(u)
	if err != nil {
		log.Printf("[Gotify] 取历史失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	var result struct {
		Messages []GotifyMessage `json:"messages"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil
	}
	return result.Messages
}

// ──────────────────────────── 高水位 + 转发 + 回补（镜像 Python init_last_id / _forward / backfill）────────────────────────────

// initLastID — 把高水位设到当前最新 id（不回放历史，只推此后新消息）。
func initLastID() {
	msgs := recentMessages(1)
	if len(msgs) > 0 {
		lastMsgID.Store(int64(msgs[0].ID))
		log.Printf("[Gotify] 从最新消息（id=%d）开始，只转发之后的新消息，历史不补推", msgs[0].ID)
	}
}

// forward — 转发单条，按 id 去重（高水位 CAS，hazard 10）。/stream 与回补共用 → 天然不重不漏。
func forward(msg GotifyMessage, tag string) {
	mid := int64(msg.ID)
	for {
		cur := lastMsgID.Load()
		if mid <= cur {
			return // 去重
		}
		if lastMsgID.CompareAndSwap(cur, mid) {
			break // 抢到更新
		}
	}
	// TODO CP3：sendToHuawei(msg.Title, msg.Message, msg.Priority, msg.Extras, ts=msg.Date, notifyID=msg.ID)
	log.Printf("[Gotify][%s] id=%d 已转发", tag, mid)
}

// backfill — 重连后回补断开期间漏的消息：取最近 100 条，筛 id>高水位，升序补推（去重）。
func backfill() {
	msgs := recentMessages(100)
	cur := lastMsgID.Load()
	var missed []GotifyMessage
	for _, m := range msgs {
		if int64(m.ID) > cur {
			missed = append(missed, m)
		}
	}
	sort.Slice(missed, func(i, j int) bool { return missed[i].ID < missed[j].ID })
	for _, m := range missed {
		forward(m, "回补")
	}
	if len(missed) >= 100 {
		log.Print("[Gotify] ⚠️ 断开期间漏 ≥100 条，超出最新 100 条的部分未回补（历史仍可在 App GET /message 看）")
	}
}

// ──────────────────────────── 订阅 Gotify /stream（镜像 Python subscribe_gotify）────────────────────────────
// ★ hazard 2：gorilla Dialer.DialContext 传空 http.Header{} —— 不主动加 Origin（源码核），
//   Gotify isAllowedOrigin 空 Origin=放行 → 与 Python websockets（不发 Origin）字节级一致。
// ★ hazard 9：20s ping ticker goroutine（对齐 Python ping_interval=20）；gorilla ReadMessage 自动回 pong。
func subscribeGotify(ctx context.Context) error {
	cfgMu.RLock()
	tok := cfg.GotifyToken
	cfgMu.RUnlock()
	base := gotifyConnectURL()
	if base == "" || tok == "" {
		return fmt.Errorf("无 Gotify 配置")
	}
	wsURL := buildWSURL(base, tok)
	tlsCfg := gotifyTLSConfig()
	if tlsCfg != nil && tlsCfg.InsecureSkipVerify {
		log.Print("[Gotify] ℹ️ localhost TLS：跳过证书主机名校验（同机回环）")
	}
	log.Printf("[Gotify] 订阅 %s", strings.Replace(wsURL, tok, "***", 1))

	dialer := websocket.Dialer{
		HandshakeTimeout: 45 * time.Second,
		TLSClientConfig:  tlsCfg,
	}
	// ★ 空 http.Header{} —— gorilla 不加 Origin（绝不 header.Set("Origin",...)）
	conn, _, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return err
	}
	defer conn.Close()

	// ctx 取消 → close conn 中断阻塞的 ReadMessage
	go func() { <-ctx.Done(); _ = conn.Close() }()

	// 先回补断开期间漏的消息（去重）；阻塞在此 goroutine，回补完才消费实时流（对齐 Python to_thread(backfill) 顺序）
	backfill()

	// 20s ping ticker（ReadMessage 自动回服务端 pong；这是客户端主动 ping，belt-and-suspenders）
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					return
				}
			}
		}
	}()
	defer close(pingDone)

	// 消费实时流（forward 去重，不重发）
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var msg GotifyMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue // 非 JSON 帧（pong/control 已由 gorilla 处理），跳过
		}
		forward(msg, "实时")
	}
}

// ──────────────────────────── 订阅主循环（镜像 Python keep_subscribed）────────────────────────────
// 没 Gotify 配置 = waiting for app（轮询等 App 上报）；有就订阅；配置变了重设高水位。断开 5s 重连。
func keepSubscribed(ctx context.Context) {
	lastSig := ""
	for {
		if ctx.Err() != nil {
			return
		}
		cfgMu.RLock()
		base := cfg.GotifyURLLocal
		if base == "" {
			base = cfg.GotifyURL
		}
		tok := cfg.GotifyToken
		cfgMu.RUnlock()
		sig := base + "\x00" + tok

		if base == "" || tok == "" {
			if lastSig != "waiting" {
				log.Print("[Hotify 推送服务] ⏳ 等待 App 上报 Gotify 配置。在 App「设置」填 Gotify 地址 + client token 并保存，本服务会自动接上订阅。")
				lastSig = "waiting"
			}
			if sleepCtx(ctx, 5*time.Second) {
				return
			}
			continue
		}
		if sig != lastSig { // 首次 / App 刚改了配置 → 重设高水位（不回放历史）
			initLastID()
			lastSig = sig
		}
		if err := subscribeGotify(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[Gotify] 断开: %v，5秒后重连（重连后会回补漏的消息）...", err)
			if sleepCtx(ctx, 5*time.Second) {
				return
			}
		}
	}
}

// sleepCtx — 可被 ctx 取消的 sleep。返回 true = ctx 已取消（调用方应退出）。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}
