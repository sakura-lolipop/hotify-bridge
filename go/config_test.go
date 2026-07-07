package main

import (
	"os"
	"testing"
)

// TestSeedConfigFileOutput — 看 Go seed 首次生成的 bridge_config.yaml（含注释字段）。
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
	t.Logf("Go seed 生成的 bridge_config.yaml：\n──────\n%s────——", string(data))
}
