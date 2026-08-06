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

# 按 tag 取 release id。需 jq 精确匹配——**无 jq 时返空跳过删旧，绝不 grep-first 兜底**
# （grep-first 会拿列表第一个 release 的 id，可能误删别的 tag 的 release——实测踩过：传 v1.1.3 误删了 v1.1.2）。
get_rid_by_tag() {
  local resp; resp=$(curl -sSL --max-time 30 "$API/releases?access_token=$TOKEN" 2>/dev/null)
  if command -v jq >/dev/null 2>&1; then
    echo "$resp" | jq -r ".[] | select(.tag_name==\"$TAG\") | .id" | head -1
  else
    echo "⚠️ 无 jq：无法按 tag 精确定位 release，跳过删旧（grep-first 会误删别的 release）。装 jq（scoop/choco install jq）再跑可清旧。" >&2
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

echo "=== 建新 release（body 走 UTF-8 模板文件 + token 走 query，避 Windows curl inline 中文 cp936→400）==="
BODY_TMP="$(mktemp)"
sed "s/__TAG__/$TAG/g" "$(git rev-parse --show-toplevel)/scripts/gitee-release-body.tmpl.json" > "$BODY_TMP"
RID=""
for attempt in 1 2 3 4; do
  RID=$(curl -fsSL --max-time 30 -X POST "$API/releases?access_token=$TOKEN" -H "Content-Type: application/json" \
    -d @"$BODY_TMP" \
    2>/dev/null | grep -oE '"id":[0-9]+' | head -1 | cut -d: -f2)
  [ -n "$RID" ] && break
  # create 没返 id：可能前一次已建成（grep 漏了）/ 已存在 → 按 tag 找复用
  RID=$(get_rid_by_tag)
  [ -n "$RID" ] && { echo "（release 已存在，复用 id=$RID）"; break; }
  echo "  建 release 第 $attempt 次没拿到 id，重试..."; sleep 2
done
rm -f "$BODY_TMP"
echo "新 release id=$RID"
[ -n "$RID" ] || { echo "❌ 建 release 多次失败（Gitee 抽风）；过会儿重跑本脚本"; exit 1; }

echo "=== 传 5 个二进制（国内直连）==="
fails=0
for f in gotify-bridge-linux-amd64 gotify-bridge-linux-arm64 gotify-bridge-windows-amd64.exe gotify-bridge-darwin-amd64 gotify-bridge-darwin-arm64; do
  printf '  → %-36s ' "$f"
  if curl -fsSL --max-time 180 --retry 3 --retry-delay 5 -X POST "$API/releases/$RID/attach_files" \
    -F "access_token=$TOKEN" -F "file=@go/dist/$f" -o /dev/null -w 'http %{http_code}\n' 2>/dev/null; then :
  else echo "❌ 失败"; fails=$((fails+1)); fi
done

echo ""
if [ "$fails" -gt 0 ]; then
  echo "❌ $TAG：$fails 个二进制传失败，release 不完整（重跑本脚本会删旧重建补全）"
  exit 1
fi
echo "✅ $TAG 传完：https://gitee.com/$OWNER/$REPO/releases/tag/$TAG"
