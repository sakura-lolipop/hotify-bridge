package main

import (
	"os"
	"testing"
)

// chdirTemp — load/save* 用 CWD 固定文件名，测试 chdir 到 temp dir 隔离（不碰真实 push_tokens.json）。
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}

// TestTokensRoundTrip — save→load 语义等价；单 key 字节格式对齐 Python（json.dump indent=2 无尾换行）。
// 真实 push_tokens.json 当前单设备，此场景字节同。多设备 Go 按 key 排序（Python 保插入序），
// 但 push_tokens.json 是本地文件非线契约，排序差异无功能影响。
func TestTokensRoundTrip(t *testing.T) {
	chdirTemp(t)
	orig := map[string]string{"ddff9c84": "token-AAAA"}
	saveTokens(orig)
	got := loadTokens()
	if len(got) != 1 || got["ddff9c84"] != "token-AAAA" {
		t.Fatalf("round-trip mismatch: got %v", got)
	}
	data, err := os.ReadFile(pushTokensFile)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "ddff9c84": "token-AAAA"
}` // Python json.dump(indent=2) 无尾换行
	if string(data) != want {
		t.Fatalf("byte format mismatch:\nwant=%q\ngot =%q", want, string(data))
	}
}

// TestSubscribeStatusRoundTrip — save→load 语义等价（多 key，Go 排序，仅验语义不验字节序）。
func TestSubscribeStatusRoundTrip(t *testing.T) {
	chdirTemp(t)
	orig := map[string]bool{"ddff9c84": true, "dev2": false}
	saveSubscribeStatus(orig)
	got := loadSubscribeStatus()
	if len(got) != 2 || !got["ddff9c84"] || got["dev2"] {
		t.Fatalf("round-trip mismatch: got %v", got)
	}
}

// TestTokensLoadMissing — 文件不存在返回空 map（对齐 Python FileNotFoundError→{}）。
func TestTokensLoadMissing(t *testing.T) {
	chdirTemp(t)
	got := loadTokens()
	if len(got) != 0 {
		t.Fatalf("missing file should give empty map, got %v", got)
	}
}
