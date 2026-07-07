#!/usr/bin/env bash
# 交叉编译 Go 桥全平台（纯 Go，CGO_ENABLED=0 → 静态二进制，免运行时/免交叉工具链）
# 产物在 dist/：linux/{amd64,arm64} windows/amd64 darwin/{amd64,arm64}
# 用法：bash go/build-all.sh
set -e
cd "$(dirname "$0")"  # go/ 目录
mkdir -p dist

# -trimpath：去掉本地路径（二进制不含 C:\Users\... 等开发机路径）
# -ldflags="-s -w"：strip 调试符号 + DWARF，缩体积（~10MB→~7MB）
LDFLAGS="-s -w"

build() {
  local os=$1 arch=$2 out=$3
  echo "→ $os/$arch → $out"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags="$LDFLAGS" -o "$out" .
}

build linux   amd64 dist/gotify-bridge-linux-amd64
build linux   arm64 dist/gotify-bridge-linux-arm64
build windows amd64 dist/gotify-bridge-windows-amd64.exe
build darwin  amd64 dist/gotify-bridge-darwin-amd64
build darwin  arm64 dist/gotify-bridge-darwin-arm64

echo ""
echo "=== dist/ ==="
ls -lh dist/
