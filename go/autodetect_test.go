package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseGotifyConfig — 极简 YAML 解析（缩进跟踪 section 路径）。
func TestParseGotifyConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")
	content := `server:
  port: 443
  ssl:
    enabled: true
    certfile: "/etc/gotify/cert.cer"
    keyfile: "/etc/gotify/cert.key"
    letsencrypt:
      enabled: false
  stream:
    allowedorigins:
      - ".*"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := parseGotifyConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Port != 443 {
		t.Errorf("Port=%d（期望 443）", info.Port)
	}
	if !info.SSLEnabled {
		t.Error("SSLEnabled=false（期望 true）")
	}
	if info.CertFile != "/etc/gotify/cert.cer" {
		t.Errorf("CertFile=%q（期望 /etc/gotify/cert.cer，引号该剥）", info.CertFile)
	}
	if info.KeyFile != "/etc/gotify/cert.key" {
		t.Errorf("KeyFile=%q", info.KeyFile)
	}
	if info.LEEnabled {
		t.Error("LEEnabled=true（期望 false）")
	}
	t.Logf("✓ parseGotifyConfig：port=443 ssl=true cert=%s key=%s le=false", info.CertFile, info.KeyFile)
}

// TestParseCfTxt — 一行一个 URL，跳过空行 + # 注释。
func TestParseCfTxt(t *testing.T) {
	content := `# Hotify 推送服务入口
https://a.com/api/push
https://b.com/api/push

# 备用
https://c.com/api/push
`
	urls := parseCfTxt(content)
	want := []string{"https://a.com/api/push", "https://b.com/api/push", "https://c.com/api/push"}
	if len(urls) != len(want) {
		t.Fatalf("parseCfTxt 得 %v（期望 %v）", urls, want)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Errorf("urls[%d]=%q（期望 %q）", i, urls[i], want[i])
		}
	}
	t.Logf("✓ parseCfTxt：%v", urls)
}

// TestIsPrivateIP — ★ hazard 8：私网判定（Go IsPrivate 不含 loopback，故显式判 127/localhost/::1）。
func TestIsPrivateIP(t *testing.T) {
	privates := []string{"127.0.0.1", "localhost", "::1", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1"}
	for _, h := range privates {
		if !isPrivateIP(h) {
			t.Errorf("isPrivateIP(%q)=false（期望 true）", h)
		}
	}
	publics := []string{"172.32.0.1", "8.8.8.8", "1.1.1.1", "example.com", ""}
	for _, h := range publics {
		if isPrivateIP(h) {
			t.Errorf("isPrivateIP(%q)=true（期望 false：172.32 超出 16-31 / 公网 IP / 域名 / 空）", h)
		}
	}
	t.Logf("✓ hazard 8：私网判定（127/10/172.16-31/192.168/localhost/::1=true；172.32/公网/域名=false）")
}
