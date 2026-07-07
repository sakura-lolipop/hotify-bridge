package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// ──────────────────────────── 入口（CP1：initConfig + banner + go startRegisterServer）────────────────────────────
// CP2 接入 go keepSubscribed(ctx)。
func main() {
	initConfig()

	// banner（对齐 Python __main__ 打印）
	cfgMu.RLock()
	gu, gt := cfg.GotifyURL, cfg.GotifyToken
	cfURLs := cfg.CloudFunctionURLs
	cfgMu.RUnlock()
	if gu != "" && gt != "" {
		log.Printf("[Hotify 推送服务] 已有 Gotify 配置：%s", gu)
	} else {
		log.Println("[Hotify 推送服务] 无 Gotify 配置，等待 App 上报（开 /register 等）")
	}
	if len(cfURLs) > 0 {
		s := fmt.Sprintf("[Hotify 推送服务] 推送入口：%s", cfURLs[0])
		if len(cfURLs) > 1 {
			s += fmt.Sprintf("（+%d 个备用）", len(cfURLs)-1)
		}
		log.Println(s)
	} else {
		log.Println("[Hotify 推送服务] ⚠️ cloud_function_urls 未配置，Push Kit 转发将跳过（在 bridge_config.yaml 填）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := startRegisterServer(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("register server: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		keepSubscribed(ctx)
	}()
	wg.Wait()
}
