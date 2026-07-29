package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSeedConfigFileOutput — seed 首次生成 bridge_config.yaml。L3 修：加 assert（不只 Logf）。
func TestSeedConfigFileOutput(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	cfg = cfgDefaults()
	seedConfigFile()

	data, err := os.ReadFile(bridgeConfigFile)
	if err != nil {
		t.Fatalf("seed 未生成 %s: %v", bridgeConfigFile, err)
	}
	s := string(data)
	if !strings.Contains(s, "gotify_token:") {
		t.Error("生成配置缺 gotify_token:")
	}
	if !strings.Contains(s, "# cloud_function_urls:") {
		t.Error("生成配置缺 cloud_function_urls（应为注释）")
	}
	if !strings.Contains(s, "仅供测试用") {
		t.Error("生成配置缺'仅供测试用'提示")
	}
}

// TestLoadBridgeConfig_cfURLs — cloud_function_urls 多格式解析（M4：锁回归）。
func TestLoadBridgeConfig_cfURLs(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		want   []string
		wantOK bool
	}{
		{
			name:   "续行（缩进 URL under empty key）",
			yaml:   "cloud_function_urls:\n  https://a.com/push\n  https://b.com/push\n",
			want:   []string{"https://a.com/push", "https://b.com/push"},
			wantOK: true,
		},
		{
			name:   "重复 key 行",
			yaml:   "cloud_function_urls: https://a.com/push\ncloud_function_urls: https://b.com/push\n",
			want:   []string{"https://a.com/push", "https://b.com/push"},
			wantOK: true,
		},
		{
			name:   "JSON 数组（旧格式兼容）",
			yaml:   `cloud_function_urls: ["https://a.com/push", "https://b.com/push"]` + "\n",
			want:   []string{"https://a.com/push", "https://b.com/push"},
			wantOK: true,
		},
		{
			name:   "单 URL 同行",
			yaml:   "cloud_function_urls: https://a.com/push\n",
			want:   []string{"https://a.com/push"},
			wantOK: true,
		},
		{
			name:   "注释掉（autodetect 回归）",
			yaml:   "# cloud_function_urls: https://a.com/push\n",
			want:   nil,
			wantOK: false,
		},
		{
			name:   "不写（autodetect 回归）",
			yaml:   "gotify_token: test\n",
			want:   nil,
			wantOK: false,
		},
		{
			name:   "URL 含 :// 和 userinfo 不被截断",
			yaml:   "cloud_function_urls:\n  https://user:pass@host:8443/path\n",
			want:   []string{"https://user:pass@host:8443/path"},
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.yaml")
			os.WriteFile(path, []byte(tt.yaml), 0644)
			p := loadBridgeConfig(path)
			urls, ok := p["cloud_function_urls"].([]string)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v (urls=%v)", ok, tt.wantOK, urls)
			}
			if tt.wantOK && !reflect.DeepEqual(urls, tt.want) {
				t.Errorf("urls=%v, want %v", urls, tt.want)
			}
		})
	}
}
