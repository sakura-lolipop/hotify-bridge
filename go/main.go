package main

import "fmt"

// ──────────────────────────── 入口（CP0：仅配置加载 + banner + dump，无 goroutine）────────────────────────────
// CP1 接入 go startRegisterServer(ctx)，CP2 接入 go keepSubscribed(ctx)。
// CP0 先验宽松解析器：用现有 bridge_config.yaml 跑，输出与 Python load_bridge_config() 逐 key 对齐。
func main() {
	initConfig()

	// banner（对齐 Python __main__ 打印）
	cfgMu.RLock()
	gu, gt := cfg.GotifyURL, cfg.GotifyToken
	cfURLs := cfg.CloudFunctionURLs
	cfgMu.RUnlock()
	if gu != "" && gt != "" {
		fmt.Printf("[Hotify 推送服务] 已有 Gotify 配置：%s\n", gu)
	} else {
		fmt.Println("[Hotify 推送服务] 无 Gotify 配置，等待 App 上报（开 /register 等）")
	}
	if len(cfURLs) > 0 {
		s := fmt.Sprintf("[Hotify 推送服务] 推送入口：%s", cfURLs[0])
		if len(cfURLs) > 1 {
			s += fmt.Sprintf("（+%d 个备用）", len(cfURLs)-1)
		}
		fmt.Println(s)
	} else {
		fmt.Println("[Hotify 推送服务] ⚠️ cloud_function_urls 未配置，Push Kit 转发将跳过（在 bridge_config.yaml 填）")
	}

	// CP0 dump：验宽松解析器对齐 Python（含 Windows 反斜杠路径 / JSON 数组 / 默认值）
	cfgMu.RLock()
	fmt.Printf("[CP0-dump] gotify_url=%q\n", cfg.GotifyURL)
	fmt.Printf("[CP0-dump] gotify_token=%q (len=%d, mask=%s)\n", cfg.GotifyToken, len(cfg.GotifyToken), mask(cfg.GotifyToken))
	fmt.Printf("[CP0-dump] gotify_url_local=%q\n", cfg.GotifyURLLocal)
	fmt.Printf("[CP0-dump] register_port=%q (空→默认 %d)\n", cfg.RegisterPort, registerPortDefault)
	fmt.Printf("[CP0-dump] tls_cert_file=%q\n", cfg.TLSCertFile)
	fmt.Printf("[CP0-dump] tls_key_file=%q\n", cfg.TLSKeyFile)
	fmt.Printf("[CP0-dump] gotify_config_path=%q\n", cfg.GotifyConfigPath)
	fmt.Printf("[CP0-dump] subscribe_label=%q\n", cfg.SubscribeLabel)
	fmt.Printf("[CP0-dump] cloud_function_urls=%q\n", cfg.CloudFunctionURLs)
	fmt.Printf("[CP0-dump] cloud_function_token=%q\n", cfg.CloudFunctionToken)
	cfgMu.RUnlock()
}
