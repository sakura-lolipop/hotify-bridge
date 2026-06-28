"""
gotify_pushkit_bridge.py
================================================================================
Gotify ↔ 华为 Push Kit(HarmonyOS NEXT v3) 转发桥。
链路：[发送方] -> Gotify --/stream--> 本桥 --Push Kit v3--> 鸿蒙(锁屏弹+图标)

配置来源（优先级）：App 上报（POST /register 带 gotify_url+gotify_token，持久化到
      bridge_config.json）> 环境变量 GOTIFY_HTTP_URL/GOTIFY_CLIENT_TOKEN（headless 兜底）。
      都没有 = waiting for app（开 /register 等 App 上报），不拿占位符瞎连。

鉴权：服务账号 JWT 直接当 Bearer（官方 push-jwt-token，不换 access_token）。
图标：不设 notification.image —— 通知小图标默认就是 Hotify 自己的应用图标（即 logo）。
      image 字段是可选「大图标」：曾取 Gotify 来源 app 图标，但 URL 拼错 + 华为拉不到
      （报 Get image failed, url is invalid），且转发场景下来源恒为 SmsForwarder 无意义，已移除。

依赖：pip install websockets PyJWT cryptography
运行：python -u gotify_pushkit_bridge.py
================================================================================
"""

import asyncio
import json
import os
import sys
import time
import threading
import urllib.request
import urllib.parse
import urllib.error
from http.server import BaseHTTPRequestHandler, HTTPServer

import websockets
import jwt as pyjwt  # PyJWT；PS256 签名需 cryptography

# Windows 控制台默认 GBK，emoji/特殊符号会 UnicodeEncodeError；强制 UTF-8。
try:
    sys.stdout.reconfigure(encoding='utf-8', errors='replace')
    sys.stderr.reconfigure(encoding='utf-8', errors='replace')
except Exception:
    pass

# ──────────────────────────── Gotify 配置（App 上报 > env）────────────────────────────
BRIDGE_CONFIG_FILE = "bridge_config.json"        # App 上报的 gotify 配置持久化（含 client token = 机密）
_cfg = {"gotify_url": "", "gotify_token": ""}    # 运行时配置；RegisterHandler 写，keep_subscribed 读


def load_bridge_config() -> dict:
    try:
        with open(BRIDGE_CONFIG_FILE, encoding="utf-8") as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def save_bridge_config():
    with open(BRIDGE_CONFIG_FILE, "w", encoding="utf-8") as f:
        json.dump(_cfg, f, ensure_ascii=False, indent=2)


def normalize_gotify_addr(raw: str) -> str:
    """智能模式：只输端口号→http://127.0.0.1:端口（Gotify 与桥同机部署，最快、免 TLS 证书）；
    否则按完整地址（远程主机）。没带协议的补 http://。"""
    raw = (raw or "").strip()
    if not raw:
        return ""
    if raw.isdigit():                       # 纯端口 → 同机，走 127.0.0.1
        return f"http://127.0.0.1:{raw}"
    if "://" not in raw:                    # 没带协议 → 补 http://
        return f"http://{raw}"
    return raw


def init_config():
    """启动时：持久化(App 上报过的) > 环境变量兜底。都没有则留空 = waiting for app。"""
    p = load_bridge_config()
    _cfg["gotify_url"] = normalize_gotify_addr(p.get("gotify_url") or os.environ.get("GOTIFY_HTTP_URL", ""))
    _cfg["gotify_token"] = p.get("gotify_token") or os.environ.get("GOTIFY_CLIENT_TOKEN", "")


# ──────────────────────── 华为服务账号 + 推送 ────────────────────────
SERVICE_ACCOUNT_FILE = "private.json"   # AGC 项目设置→常规→服务账号 下载
NOTIFY_CATEGORY = "ACCOUNT"             # 须与申到的自分类权益类目一致
TEST_MESSAGE    = True                  # 调测期绕频控；正式改 False

PUSH_TOKENS_FILE = "push_tokens.json"
REGISTER_PORT    = 25238

PUSH_URL  = "https://push-api.cloud.huawei.com/v3/{project_id}/messages:send"
TOKEN_URI = "https://oauth-login.cloud.huawei.com/oauth2/v3/token"

# ──────────────────────────── 运行态缓存 ────────────────────────────
_sa = None               # 服务账号配置
_jwt_token = None        # 缓存 JWT（官方 push-jwt-token：JWT 直接当 Bearer，不换 access_token；见 get_bearer_token）
_jwt_exp = 0             # JWT 到期时间戳（exp=签发+3600s）


def load_service_account():
    """加载华为服务账号 private.json。缺失则告警但不退出——桥仍可订阅 Gotify /stream，
    仅 Push Kit 转发跳过（send_to_huawei 见 _sa is None 即 return）。"""
    global _sa
    if not os.path.exists(SERVICE_ACCOUNT_FILE):
        print(f"[PushKit] ⚠️ 未找到 {SERVICE_ACCOUNT_FILE}（华为服务账号 RSA 私钥）。")
        print(f"[PushKit]    桥会照常订阅 Gotify /stream，但 Push Kit 转发将跳过。")
        print(f"[PushKit]    要完整推送：AGC 建项目→开通 Push Kit→下载服务账号 private.json 放本目录。")
        return
    with open(SERVICE_ACCOUNT_FILE, "r", encoding="utf-8") as f:
        _sa = json.load(f)
    print(f"[PushKit] 加载服务账号 project_id={_sa.get('project_id')}")


def _gen_jwt() -> str:
    """服务账号 JWT = 鉴权令牌本身（官方 push-jwt-token：JWT 直接当 Bearer，无"换 access_token"步）。
    Header {kid:key_id, typ:JWT, alg:PS256}；Payload {iss:sub_account, aud:TOKEN_URI, iat, exp=iat+3600}。
    照官方 5 语言示例，无 sub claim。"""
    now = int(time.time())
    payload = {"iss": _sa["sub_account"], "aud": TOKEN_URI, "iat": now, "exp": now + 3600}
    return pyjwt.encode(payload, _sa["private_key"], algorithm="PS256",
                        headers={"kid": _sa["key_id"], "typ": "JWT"})


def get_bearer_token() -> str:
    """官方 push-jwt-token：JWT 本身就是 Bearer 令牌，直接放 Authorization（不调 oauth2/v3/token 换 access_token——
    那端点要 client_id 会报 1102，且官方文档无此步；旧 get_access_token 走错路了）。缓存 JWT、临近 exp(1h) 重签。"""
    global _jwt_token, _jwt_exp
    if _jwt_token and time.time() < _jwt_exp - 60:
        return _jwt_token
    _jwt_token = _gen_jwt()
    _jwt_exp = time.time() + 3600
    print("[PushKit] JWT 鉴权令牌已生成（1h 有效，直接当 Bearer）")
    return _jwt_token


# ──────────────────────────── 设备 token 注册表 + App 配置上报 ────────────────────────────

def load_tokens():
    try:
        with open(PUSH_TOKENS_FILE, "r", encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return {}


def save_tokens(d):
    with open(PUSH_TOKENS_FILE, "w", encoding="utf-8") as f:
        json.dump(d, f, ensure_ascii=False, indent=2)


class RegisterHandler(BaseHTTPRequestHandler):
    """App 上报：POST /register
    body = {client, token:<push_token>(可选), gotify_url, gotify_token}。
    gotify 配置持久化到 bridge_config.json，桥据此订阅；push token 存 push_tokens.json。"""
    def do_POST(self):
        if self.path != "/register":
            self.send_response(404); self.end_headers(); return
        length = int(self.headers.get("Content-Length", 0))
        try:
            payload = json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError:
            self.send_response(400); self.end_headers(); return
        client = payload.get("client", "default")
        # 1) 华为 push token（可选——milestone 1 没 agconnect 时没有）
        push_token = payload.get("token") or payload.get("push_token")
        if push_token:
            tokens = load_tokens(); tokens[client] = push_token; save_tokens(tokens)
            print(f"[注册] {client} push token -> {push_token[:12]}...")
        # 2) Gotify 配置（App 上报 → 持久化 → 桥订阅用）
        gurl = normalize_gotify_addr(payload.get("gotify_url") or "")
        gtok = payload.get("gotify_token") or ""
        changed = False
        if gurl and gurl != _cfg["gotify_url"]:
            _cfg["gotify_url"] = gurl; changed = True
        if gtok and gtok != _cfg["gotify_token"]:
            _cfg["gotify_token"] = gtok; changed = True
        if changed:
            save_bridge_config()
            print(f"[注册] App 上报 Gotify 配置已保存：url={_cfg['gotify_url']} "
                  f"token={'***已设置***' if _cfg['gotify_token'] else '无'}")
        if not (push_token or changed):
            print(f"[注册] {client} 上报为空（无 token 无配置）")
        self.send_response(200); self.end_headers()
        self.wfile.write(b'{"ok":true}')

    def log_message(self, *args):
        pass


def start_register_server():
    httpd = HTTPServer(("0.0.0.0", REGISTER_PORT), RegisterHandler)
    print(f"[注册接口] http://0.0.0.0:{REGISTER_PORT}/register （等 App 上报）")
    httpd.serve_forever()


# ──────────────────────────── 推送到华为 (v3) ────────────────────────────

def send_to_huawei(title, message, priority=4, extras=None, appid=0):
    if _sa is None:
        print(f"[PushKit] ⏭ 跳过推送（private.json 未配置）：{title or '(无标题)'} | {(message or '')[:40]}")
        return
    devs = load_tokens()  # {device_id: push_token}
    if not devs:
        print("[PushKit] 还没注册设备，跳过"); return

    notification = {
        "category": NOTIFY_CATEGORY,
        "title": title or "Hotify",
        "body": message,
        "badge": {"addNum": 1},
        "clickAction": {"actionType": 0, "data": {"appid": appid}},
    }
    # 不设 notification.image：通知小图标默认用 Hotify 自己的应用图标（logo），无需来源 app 图标（见模块头注释）。

    # 逐 token 推（非批量）：① 能按单 token 拿返回码、清理失效 token（bark 式，device 卸载/重装后旧 token 不再当孤儿反复推）；
    # ② 多设备各自独立、互不影响。代价：N 台=N 次 API（自用几台无妨；JWT 缓存复用）。
    delivered, dead = 0, []
    for dev_id, tok in list(devs.items()):
        payload = {
            "target": {"token": [tok]},
            "payload": {
                "notification": notification,
                "data": json.dumps({"priority": priority, "extras": extras or {}}, ensure_ascii=False),
            },
            # pushOptions 须【顶层】（与 target/payload 平级），否则华为读不到 testMessage → 走 MARKETING 频控（每设备 2~5 条/天）。
            "pushOptions": {"testMessage": TEST_MESSAGE},
        }
        req = urllib.request.Request(PUSH_URL.format(project_id=_sa["project_id"]),
                                     data=json.dumps(payload).encode("utf-8"), method="POST")
        req.add_header("Content-Type", "application/json")
        req.add_header("Authorization", f"Bearer {get_bearer_token()}")
        req.add_header("push-type", "0")
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                resp = json.loads(r.read().decode("utf-8"))
                code = str(resp.get("code"))
                if code == "80000000":
                    delivered += 1
                    print(f"[PushKit] ✓ {dev_id} code=80000000")
                else:
                    # 非 success：多半该 token 失效 → 删掉。⚠️ 注意：华为对 dead token 也常返 80000000（接受但不投递），
                    # 那种检测不到、留着无害（同 bark 不主动 GC）。这里只清华为【明确拒】的。
                    print(f"[PushKit] ✗ {dev_id} code={code} msg={resp.get('msg')} → 删该 token")
                    dead.append(dev_id)
        except urllib.error.HTTPError as e:
            print(f"[PushKit] ✗ {dev_id} HTTP {e.code}: {e.read().decode()[:120]} → 删该 token")
            dead.append(dev_id)
        except Exception as e:
            print(f"[PushKit] ✗ {dev_id} {type(e).__name__}: {e}（网络？保留 token）")
    if dead:
        t = load_tokens()           # 重新读（期间可能有新 register），避免覆盖
        for d in dead:
            t.pop(d, None)
        save_tokens(t)
        print(f"[PushKit] 清理 {len(dead)} 个失效 token：{dead}")
    print(f"[PushKit] 推送完成：{delivered} 台成功" + (f"，{len(dead)} 失效已清" if dead else ""))


# ──────────────────────────── 订阅 Gotify（断线回补 + waiting for app）────────────────────────────

_last_msg_id = 0   # 已转发消息最高 id（高水位：去重 + 回补边界）


def _recent_messages(limit=100):
    """GET /message（最新在前 desc）。无配置返回空。"""
    base = _cfg["gotify_url"]; tok = _cfg["gotify_token"]
    if not base or not tok:
        return []
    url = f"{base}/message?token={urllib.parse.quote(tok)}&limit={limit}"
    try:
        with urllib.request.urlopen(url, timeout=10) as r:
            return (json.load(r) or {}).get("messages", [])
    except Exception as e:
        print(f"[Gotify] 取历史失败: {e}")
        return []


def init_last_id():
    """把高水位设到当前最新 id——不回放历史，只推此后新消息。"""
    global _last_msg_id
    msgs = _recent_messages(limit=1)
    if msgs:
        _last_msg_id = msgs[0].get("id", 0)
        print(f"[Gotify] 高水位初始化 = {_last_msg_id}（不回放历史）")


def _forward(msg, tag="实时"):
    """转发单条，按 id 去重（高水位）。/stream 与回补共用 → 天然不重不漏。"""
    global _last_msg_id
    mid = msg.get("id", 0)
    if mid <= _last_msg_id:
        return
    _last_msg_id = mid
    send_to_huawei(msg.get("title") or "", msg.get("message") or "",
                   msg.get("priority", 4), msg.get("extras"), msg.get("appid", 0))
    print(f"[Gotify][{tag}] id={mid} 已转发")


def backfill():
    """重连后回补断开期间漏的消息：取最近 100 条，筛 id>高水位，升序补推（去重）。"""
    msgs = _recent_messages(limit=100)
    missed = sorted((m for m in msgs if m.get("id", 0) > _last_msg_id), key=lambda m: m.get("id", 0))
    for m in missed:
        _forward(m, tag="回补")
    if len(missed) >= 100:
        print("[Gotify] ⚠️ 断开期间漏 ≥100 条，超出最新 100 条的部分未回补（历史仍可在 App GET /message 看）")


async def subscribe_gotify():
    base = _cfg["gotify_url"]; tok = _cfg["gotify_token"]
    ws_url = f"{base.replace('http', 'ws')}/stream?token={urllib.parse.quote(tok)}"
    print(f"[Gotify] 订阅 {ws_url.replace(tok, '***')}")
    async with websockets.connect(ws_url, ping_interval=20) as ws:
        await asyncio.to_thread(backfill)            # 先回补断开期间漏的消息（去重）
        async for raw in ws:                          # 再消费实时流（_forward 去重，不重发）
            try:
                msg = json.loads(raw)
            except json.JSONDecodeError:
                continue
            await asyncio.to_thread(_forward, msg)


async def keep_subscribed():
    """没 Gotify 配置 = waiting for app（轮询等 App 上报）；有就订阅；配置变了重设高水位。"""
    global _last_msg_id
    last_sig = None
    while True:
        sig = (_cfg.get("gotify_url"), _cfg.get("gotify_token"))
        if not sig[0] or not sig[1]:
            if last_sig != "waiting":
                print("[桥] ⏳ waiting for app：还没收到 Gotify 配置。"
                      "在 App「设置」填 Gotify 地址 + client token 并保存，桥会自动接上订阅。")
                last_sig = "waiting"
            await asyncio.sleep(5)
            continue
        if sig != last_sig:                          # 首次 / App 刚改了配置 → 重设高水位（不回放历史）
            init_last_id()
            last_sig = sig
        try:
            await subscribe_gotify()
        except Exception as e:
            print(f"[Gotify] 断开: {e}，5秒后重连（重连后会回补漏的消息）...")
            await asyncio.sleep(5)


# ──────────────────────────── 入口 ────────────────────────────

if __name__ == "__main__":
    init_config()           # 读持久化(App上报)/env；都没有则空 = waiting for app
    if _cfg["gotify_url"] and _cfg["gotify_token"]:
        print(f"[桥] 已有 Gotify 配置：{_cfg['gotify_url']}")
    else:
        print("[桥] 无 Gotify 配置，waiting for app（开 /register 等 App 上报）")
    load_service_account()
    threading.Thread(target=start_register_server, daemon=True).start()
    try:
        asyncio.run(keep_subscribed())
    except KeyboardInterrupt:
        print("\n退出。")
