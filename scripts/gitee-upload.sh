#!/usr/bin/env bash
# scripts/gitee-upload.sh — 本地传 Go 二进制到 Gitee Release（国内机→Gitee 国内，快、不挂）。
#
# 为什么本地而不是 CI：CI runner 在境外，传 6MB 二进制到 Gitee 跨太平洋又慢又 stall（实测挂死过）；
#   本地（国内）直传秒级、稳。每次发版后跑一次即可。
#
# 用法：
#   GITEE_TOKEN=你的私人令牌 bash scripts/gitee-upload.sh [tag]
#   没传 tag 就用最近一个 git tag。
#   令牌：gitee.com → 头像 → 设置 → 私人令牌 → 生成（勾 projects）。别提交 git。
#
# 依赖：curl、git（必）；jq（可选，有更准，没有 grep 兜底）；Go 1.22+（首次编译二进制用）。
set -e
cd "$(git rev-parse --show-toplevel)"   # 切到仓库根，无论从哪调用

TOKEN="${GITEE_TOKEN:?❌ 请先 export GITEE_TOKEN=你的gitee私人令牌}"
TAG="${1:-$(git describe --tags --abbrev=0 2>/dev/null)}"
[ -n "$TAG" ] || { echo "❌ 没指定 tag，也没 git tag 可用"; exit 1; }
OWNER="sakura-lolipop"; REPO="hotify-bridge"
API="https://gitee.com/api/v5/repos/$OWNER/$REPO"

# 按 tag 取 release id（有 jq 用 jq 精确匹配；没 jq 取列表第一个——适合"目标 tag 是最新一个"的常见情形）
get_rid_by_tag() {
  local resp; resp=$(curl -sSL --max-time 30 "$API/releases?access_token=$TOKEN" 2>/dev/null)
  if command -v jq >/dev/null 2>&1; then
    echo "$resp" | jq -r ".[] | select(.tag_name==\"$TAG\") | .id" | head -1
  else
    echo "$resp" | grep -oE '"id":[0-9]+' | head -1 | cut -d: -f2
  fi
}

echo "=== 确保 go/dist/ 有 5 个二进制 ==="
ls go/dist/gotify-bridge-linux-amd64 >/dev/null 2>&1 || (cd go && bash build-all.sh)

echo "=== 删旧 release（tag=$TAG，若有）==="
OLD=$(get_rid_by_tag)
if [ -n "$OLD" ]; then
  echo "删旧 release id=$OLD"
  curl -fsSL --max-time 30 -X DELETE "$API/releases/$OLD?access_token=$TOKEN" -o /dev/null 2>/dev/null || echo "  (删除非 2xx，继续)"
  sleep 2   # 给 Gitee 传播时间，免得立刻建撞"该标签已存在发行版"
fi

echo "=== 建新 release ==="
RID=$(curl -fsSL --max-time 30 -X POST "$API/releases" -H "Content-Type: application/json" \
  -d "{\"access_token\":\"$TOKEN\",\"tag_name\":\"$TAG\",\"name\":\"hotify-bridge $TAG\",\"body\":\"Go 二进制（GitHub Releases 国内拉不动，此处镜像）。免 Docker 用户下对应平台包直接跑；Docker 部署看 docker.md / README。\",\"target_commitish\":\"main\"}" \
  2>/dev/null | grep -oE '"id":[0-9]+' | head -1 | cut -d: -f2)
echo "新 release id=$RID"
[ -n "$RID" ] || { echo "❌ 建 release 失败（Gitee 可能抽风，重试一次）"; exit 1; }

echo "=== 传 5 个二进制（国内直连）==="
for f in gotify-bridge-linux-amd64 gotify-bridge-linux-arm64 gotify-bridge-windows-amd64.exe gotify-bridge-darwin-amd64 gotify-bridge-darwin-arm64; do
  printf '  → %-36s ' "$f"
  curl -fsSL --max-time 180 --retry 3 --retry-delay 5 -X POST "$API/releases/$RID/attach_files" \
    -F "access_token=$TOKEN" -F "file=@go/dist/$f" -o /dev/null -w 'http %{http_code}\n' 2>/dev/null || echo "❌ 失败"
done

echo ""
echo "✅ $TAG 传完：https://gitee.com/$OWNER/$REPO/releases/tag/$TAG"
