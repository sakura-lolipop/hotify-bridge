#!/usr/bin/env bash
# hotify-bridge 一键安装（Docker 版）—— 拉 ACR 镜像 + 问配置 + 预写配置 + 起容器，一条龙。
# 把"pull → run → 编辑 bridge_config.yaml → restart"四步压成跑脚本答两个问题。
#
# 用法（二选一）：
#   交互（推荐）：
#     curl -fsSL https://gitee.com/sakura-lolipop/hotify-bridge/raw/main/install.sh -o install.sh && bash install.sh
#   非交互（CI / 脚本调）：
#     GOTIFY_CLIENT_TOKEN=xxx GOTIFY_HTTP_URL=https://your.gotify:port bash install.sh
#     （HOTIFY_TOKEN / HOTIFY_GOTIFY_URL 老名字也认，向后兼容；跟 docker-compose 的 env 名对齐）
#
# 国内走阿里云 ACR（ghcr.io 被墙）。Gotify 寻址 / NAS / HTTPS 见 docker.md。
set -e

IMAGE="crpi-gi2hyqoir87c0lus.cn-hangzhou.personal.cr.aliyuncs.com/sakura-lolipop/hotify-bridge:latest"
NAME="hotify-bridge"
DATA_DIR="${HOTIFY_DATA_DIR:-$PWD/hotify-bridge-data}"
PORT_DEFAULT=8080

# ── 检查 docker ──
if ! command -v docker >/dev/null 2>&1; then
  echo "❌ 没检测到 docker。先装 Docker：https://docs.docker.com/engine/install/"
  exit 1
fi

echo "═════════════════ hotify-bridge 一键安装 ═════════════════"

# ── 收集配置：env 优先，否则交互问；非 tty 又没 env → 报错（curl|bash 那种管道拿不到输入）──
TOKEN="${GOTIFY_CLIENT_TOKEN:-${HOTIFY_TOKEN:-}}"
GURL="${GOTIFY_HTTP_URL:-${HOTIFY_GOTIFY_URL:-}}"
PORT="${HOTIFY_PORT:-$PORT_DEFAULT}"

if [ -z "$TOKEN" ]; then
  if [ -t 0 ]; then
    read -srp "Gotify CLIENT token（必填；Gotify WebUI → CLIENTS → 建 Client → 复制 Token）: " TOKEN; echo
  else
    echo "❌ 非交互模式需 GOTIFY_CLIENT_TOKEN（或老名字 HOTIFY_TOKEN）环境变量；或直接 bash install.sh 进交互。" >&2
    exit 1
  fi
fi
[ -n "$TOKEN" ] || { echo "❌ token 必填" >&2; exit 1; }

if [ -z "$GURL" ] && [ -t 0 ]; then
  read -rp "Gotify 地址 gotify_url [留空=等 App 上报；远程填 https://域名:端口；同机另一容器填 http://容器名:端口]: " GURL
fi
if [ -t 0 ] && [ -z "$HOTIFY_PORT" ]; then
  read -rp "宿主机 /register 端口 [回车默认 $PORT_DEFAULT]: " PORT_IN
  [ -n "$PORT_IN" ] && PORT="$PORT_IN"
fi

# ── 建数据目录 + 预写配置（首启即配好，免手动编辑+重启）──
mkdir -p "$DATA_DIR"
cat > "$DATA_DIR/bridge_config.yaml" <<EOF
# hotify-bridge 配置（install.sh 生成）。完整字段见 docker.md。
# 格式：每行 键: 值，值=冒号后整段。# 是注释。

gotify_url: $GURL
gotify_token: $TOKEN
gotify_url_local:
register_port:
gotify_config_path:
tls_cert_file:
tls_key_file:
subscribe_label: true
# 留空 = 自动从云端拉取托管推送服务（启动 cache-first fetch + 后台每 h 刷新）；自托管推送服务才手填
cloud_function_urls:
cloud_function_token: hotifypushkit
EOF
echo "✅ 配置已写入 $DATA_DIR/bridge_config.yaml"

# ── 拉镜像（国内 ACR；多架构自动按本机 CPU 选）──
echo "⬇️  拉镜像（阿里云 ACR）..."
docker pull "$IMAGE"

# ── 起容器（旧的同名容器先删）──
docker rm -f "$NAME" >/dev/null 2>&1 || true
# --add-host：让容器里 host.docker.internal 能寻址宿主机（Gotify 跑宿主机时填 http://host.docker.internal:端口）
docker run -d --name "$NAME" --restart unless-stopped \
  --add-host=host.docker.internal:host-gateway \
  -p "$PORT:8080" -v "$DATA_DIR:/data" "$IMAGE" >/dev/null

echo ""
echo "═════════════════ ✅ hotify-bridge 已启动 ═════════════════"
echo "  /register 地址：http://<本机IP>:$PORT/register   ← App 上报 push token 走这"
echo "  数据/配置目录：$DATA_DIR"
echo "  看日志：  docker logs -f $NAME"
echo "  改配置后：docker restart $NAME"
echo ""
echo "  ⚠️  Gotify 在【另一容器/宿主机】？容器里 127.0.0.1 连不到——"
echo "      改 \$DATA_DIR/bridge_config.yaml 的 gotify_url（见 docker.md「Gotify 寻址」），再 docker restart $NAME。"
