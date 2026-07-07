package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ──────────────────────────── /register 响应（字段顺序对齐 Python）────────────────────────────
// json.Marshal struct 按声明序输出 → 与 Python json.dumps({"ok","device_known","gotify_set","ignored_gotify"}) 同序。
type registerResponse struct {
	OK            bool `json:"ok"`
	DeviceKnown   bool `json:"device_known"`
	GotifySet     bool `json:"gotify_set"`
	IgnoredGotify bool `json:"ignored_gotify"`
}

// ──────────────────────────── /register HTTP 服务（镜像 Python start_register_server / handle_register / _process_register）────────────────────────────
// net/http stdlib 取代 Python 手搓 HTTP 解析（删 _send_http_response + handle_register 手动 request-line/Content-Length 解析）。
// TLS 可选：tls_cert_file + tls_key_file 配了 → HTTPS；空 → 明文 HTTP（仅 LAN/调试）。

func startRegisterServer(ctx context.Context) error {
	cfgMu.RLock()
	portCfg := cfg.RegisterPort
	cert, key := cfg.TLSCertFile, cfg.TLSKeyFile
	cfgMu.RUnlock()

	port := portCfg
	if port == "" {
		port = strconv.Itoa(registerPortDefault)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/register", handleRegister) // 方法在 handleRegister 内判（非 POST → 404 {"ok":false}，对齐 Python）
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	if cert != "" && key != "" {
		log.Printf("[注册接口] 模式=HTTPS  https://0.0.0.0:%s/register  （证书：%s）", port, cert)
	} else {
		log.Printf("[注册接口] 模式=HTTP  http://0.0.0.0:%s/register", port)
		log.Printf("[注册接口] ⚠️ 降级明文 http：tls_cert_file/tls_key_file 未配 → /register 走明文，公网上报 push token 会裸奔。仅 LAN/调试可接受；公网部署请配 TLS。")
	}
	if portCfg == "" {
		log.Printf("[注册接口] ⚠️ 用默认端口 %s：register_port 留空 → 默认 %d（要改请填 register_port）", port, registerPortDefault)
	}

	// 优雅关停：ctx.Done → srv.Shutdown（中断 ListenAndServe，返回 http.ErrServerClosed）
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if cert != "" && key != "" {
		return srv.ListenAndServeTLS(cert, key)
	}
	return srv.ListenAndServe()
}

// handleRegister — POST /register：解析 JSON body → processRegister → JSON 响应。
// 非 POST → 404 {"ok":false}（对齐 Python）；body 非 JSON → 400 {"ok":false}。
func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false})
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, processRegister(payload))
}

// writeJSON — 写 JSON 响应（compact，无尾换行；对齐 Python json.dumps 无尾换行）。
func writeJSON(w http.ResponseWriter, code int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"ok":false}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

// processRegister — 镜像 Python _process_register。first-set-wins gotify 锁 + 持久化先于 200。
// 返回 registerResponse（device_known/gotify_set/ignored_gotify 给 App 反馈）。
//
// 模型 = first-set wins（照 SSH 主机指纹 TOFU + bark 式 device key）：
//   - push token：每次都注册/刷新（token 会变）；返 device_known=之前是否已登记。
//   - gotify 配置：桥【未配置】才收 App 上报 → 写回 yaml = 锁定；【已配置】后一律忽略（防公网抢首注改后端）。
func processRegister(payload map[string]any) registerResponse {
	client := getString(payload, "client")
	if client == "" {
		client = "default"
	}
	pushToken := getString(payload, "token")
	if pushToken == "" {
		pushToken = getString(payload, "push_token")
	}

	resp := registerResponse{OK: true}
	deviceKnown := false

	// 1) push token：每次写（token 会刷新；空 → 跳过）
	if pushToken != "" {
		tokens := loadTokens()
		_, deviceKnown = tokens[client]
		tokens[client] = pushToken
		saveTokens(tokens)
		known := "新设备"
		if deviceKnown {
			known = "已登记/刷新"
		}
		log.Printf("[注册] %s push token -> %s... (%s)", client, mask(pushToken), known)
	}

	// 2) subscribed（每次写，不锁定——首注锁定只管 gotify；走 push_token 同款"每次刷新"路径）
	if sub, ok := payload["subscribed"]; ok {
		status := loadSubscribeStatus()
		status[client] = toBool(sub)
		saveSubscribeStatus(status)
		s := "已取消"
		if status[client] {
			s = "订阅"
		}
		log.Printf("[注册] %s subscribed=%s", client, s)
	}

	// 3) gotify：first-set-wins（防公网抢首注改后端）
	gurl := normalizeGotifyAddr(getString(payload, "gotify_url"))
	gtok := getString(payload, "gotify_token")
	cfgMu.RLock()
	already := cfg.GotifyURL != "" && cfg.GotifyToken != ""
	cfgMu.RUnlock()
	if !already {
		if gurl != "" && gtok != "" {
			cfgMu.Lock()
			cfg.GotifyURL = gurl
			cfg.GotifyToken = gtok
			cfgMu.Unlock()
			saveBridgeConfig() // 持久化先于 200（crash-safe：save 后 crash → 重启已锁，App 重试得 ignored_gotify）
			resp.GotifySet = true
			log.Printf("[注册] 首次收到 App 的 Gotify 配置，已保存：url=%s token=***已设置***", gurl)
			// TODO CP4: go autodetectLocalGotify()（后台探同机 Gotify ~9s，不阻塞 200——HAP 8s connectTimeout）
		}
		// App 没带 gotify（纯 token 刷新）→ 桥仍 waiting，不动配置
	} else {
		if gurl != "" || gtok != "" { // 桥已配置 → App 的 gotify 一律忽略（防改后端）
			resp.IgnoredGotify = true
			log.Printf("[注册] Hotify 推送服务配置已存在，本次忽略。需要修改 Gotify 配置：手动修改 bridge_config.yaml 后重启 Hotify 推送服务")
		}
	}
	resp.DeviceKnown = deviceKnown

	if pushToken == "" && !resp.GotifySet && !resp.IgnoredGotify {
		log.Printf("[注册] %s 上报为空（无 token 无配置）", client)
	}
	return resp
}

// toBool — payload["subscribed"]（JSON true/false → Go bool）转 bool。
func toBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
