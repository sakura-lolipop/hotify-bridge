"""
gotify_pushkit_bridge.py
================================================================================
Gotify ↔ 华为 Push Kit(HarmonyOS NEXT v3) 转发桥。
链路：[发送方] -> Gotify --/stream--> 本桥 --Push Kit v3--> 鸿蒙(锁屏弹+图标)

配置（bridge_config.yaml，动静结合）：静态项（register_port/tls_cert_file/tls_key_file，部署者填）+
      动态项（gotify_url/gotify_token，App POST /register 上报持久化；空则 env GOTIFY_HTTP_URL/
      GOTIFY_CLIENT_TOKEN 兜底）。save 写回完整文件，故 App 改动态项时静态项原样保留。
      gotify 两项都没有 = waiting for app（开 /register 等上报），不拿占位符瞎连。

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
import shutil
import ssl
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
BRIDGE_CONFIG_FILE = "bridge_config.yaml"
# 一个文件装两类：静态项（部署者填，启动读）+ 动态项（App 运行时上报）。save 写回完整 _cfg，
# 故 App 改动态项（gotify）时静态项（port/tls）原样保留。Go 重写时解析同一份 YAML 即可。
_CFG_DEFAULTS = {
    # —— 动态项（App 上报 / env 兜底）——
    "gotify_url": "",          # Gotify 地址（智能模式：只填端口→http://127.0.0.1 同机）
    "gotify_token": "",        # Gotify client token（读消息/订阅流；机密）
    # —— 静态项（部署者填，启动读）——
    "gotify_url_local": "",    # 桥连 Gotify 的本地地址，【覆盖】gotify_url。同机填 https://127.0.0.1:端口（自动 skip-verify、免 hairpin）；空 → 用 gotify_url
    "tls_cert_file": "",       # 填了 → /register 走 https（公网上报 push token 必须）；空 → 明文 http
    "tls_key_file": "",        # 与 tls_cert_file 配对；与 Gotify 共用同一张域名证书（acme.sh/certbot 的 PEM）
}
_cfg = dict(_CFG_DEFAULTS)     # 运行时配置；init_config 填，RegisterHandler 改动态项，keep_subscribed 读


def load_bridge_config() -> dict:
    """宽松解析：每行 `key: value`，value = 第一个冒号后的整段（去外层可选引号 + 尾部 # 注释）。
    反斜杠/冒号/冒号后无空格/引号不配对——全容错。专治之前 YAML 的 \\U 转义、冒号必空格、引号配对那些坑。"""
    cfg = {}
    try:
        with open(BRIDGE_CONFIG_FILE, encoding="utf-8") as f:
            for line in f:
                stripped = line.strip()
                if not stripped or stripped.startswith("#") or ":" not in line:
                    continue
                key, _, val = line.partition(":")
                key = key.strip()
                val = val.split(" #", 1)[0].strip()        # 去尾部行内注释（空格+#）
                if len(val) >= 2 and val[0] == val[-1] and val[0] in "\"'":
                    val = val[1:-1]                          # 去外层可选引号（" 或 '）
                cfg[key] = val
    except FileNotFoundError:
        pass
    except Exception as e:
        print(f"[配置] ❌ 读取 {BRIDGE_CONFIG_FILE} 出错：{e}")
    return cfg


def save_bridge_config():
    """App 上报 gotify 后持久化：只替换 gotify_url / gotify_token 两行的值，其余（静态项 + # 注释）
    原样保留——不整文件重写，避免丢注释、避免"动"踩"静"。"""
    try:
        with open(BRIDGE_CONFIG_FILE, encoding="utf-8") as f:
            lines = f.readlines()
    except FileNotFoundError:
        lines = []
    out, wrote_url, wrote_tok = [], False, False
    for line in lines:
        key = line.lstrip()
        if key.startswith("gotify_url:"):
            out.append(f'gotify_url: {_cfg["gotify_url"]}\n'); wrote_url = True
        elif key.startswith("gotify_token:"):
            out.append(f'gotify_token: {_cfg["gotify_token"]}\n'); wrote_tok = True
        else:
            out.append(line)
    if not wrote_url:                                   # 文件里没这两行（罕见）→ 追加
        out.append(f'gotify_url: {_cfg["gotify_url"]}\n')
    if not wrote_tok:
        out.append(f'gotify_token: {_cfg["gotify_token"]}\n')
    with open(BRIDGE_CONFIG_FILE, "w", encoding="utf-8") as f:
        f.writelines(out)


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
    """启动时：bridge_config.yaml 不存在则从 example 复制一份（带注释）；再读配置覆盖默认值。"""
    if not os.path.exists(BRIDGE_CONFIG_FILE):
        _seed_config_file()
    _cfg.update(_CFG_DEFAULTS)                  # 默认值打底
    p = load_bridge_config()
    _cfg.update(p)                              # 文件覆盖（缺字段保留默认；tls 留空 = 意图 http）
    # gotify 动态项：文件 > env 兜底，统一过 normalize（端口→127.0.0.1）
    _cfg["gotify_url"] = normalize_gotify_addr(p.get("gotify_url") or os.environ.get("GOTIFY_HTTP_URL", ""))
    _cfg["gotify_token"] = p.get("gotify_token") or os.environ.get("GOTIFY_CLIENT_TOKEN", "")
    # gotify_url_local：部署者静态（同机覆盖），也过 normalize
    _cfg["gotify_url_local"] = normalize_gotify_addr(p.get("gotify_url_local") or "")
    _autodetect_local_gotify()   # 留空则自动探同机 Gotify，探到就填


def _seed_config_file():
    """首次启动：从 example 模板复制一份 bridge_config.yaml（带 # 注释），部署者直接改。"""
    example = "bridge_config.example.yaml"
    if os.path.exists(example):
        shutil.copyfile(example, BRIDGE_CONFIG_FILE)
        print(f"[配置] 未找到 {BRIDGE_CONFIG_FILE}，已从 {example} 复制一份（带注释）。")
    else:
        with open(BRIDGE_CONFIG_FILE, "w", encoding="utf-8") as f:
            for k, v in _CFG_DEFAULTS.items():
                f.write(f"{k}: {v}\n")
        print(f"[配置] 未找到 {BRIDGE_CONFIG_FILE}，已建一份默认配置（无 example，故无注释）。")
    print(f"[配置] ✏️ 请编辑 {BRIDGE_CONFIG_FILE} 填入 gotify / 证书路径，存盘后重启桥。")


# ──────────────────────── 华为服务账号 + 推送 ────────────────────────
SERVICE_ACCOUNT_FILE = "private.json"   # AGC 项目设置→常规→服务账号 下载
NOTIFY_CATEGORY = "ACCOUNT"             # 须与申到的自分类权益类目一致
TEST_MESSAGE    = True                  # 调测期绕频控；正式改 False

PUSH_TOKENS_FILE = "push_tokens.json"
REGISTER_PORT    = 25238     # /register 监听端口（桥内部默认值；要改改这行，不入配置文件）

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
    gotify 配置持久化到 bridge_config.yaml，桥据此订阅；push token 存 push_tokens.json。"""
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
        if gurl:
            _autodetect_local_gotify()   # App 上报了域名 → 探同机 Gotify，探到自动走 localhost
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
    """App 上报接口。tls_cert_file/tls_key_file 填了 → https；留空 → 明文 http（仅 LAN/调试）。
    端口 = register_port（留空→默认 25238）。启动时对“用默认端口”和“降级 http”都显式 ⚠️ 告警。"""
    port_cfg = _cfg.get("register_port")
    port = int(port_cfg or REGISTER_PORT)
    cert = _cfg["tls_cert_file"]
    key = _cfg["tls_key_file"]
    httpd = HTTPServer(("0.0.0.0", port), RegisterHandler)
    if cert and key:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(certfile=cert, keyfile=key)
        httpd.socket = ctx.wrap_socket(httpd.socket, server_side=True)
        print(f"[注册接口] 模式=HTTPS  https://0.0.0.0:{port}/register  （证书：{cert}）")
    else:
        print(f"[注册接口] 模式=HTTP  http://0.0.0.0:{port}/register")
        print(f"[注册接口] ⚠️ 降级明文 http：tls_cert_file/tls_key_file 未配 → /register 走明文，"
              "公网上报 push token 会裸奔。仅 LAN/调试可接受；公网部署请配 TLS。")
    if not port_cfg:
        print(f"[注册接口] ⚠️ 用默认端口 {port}：register_port 留空 → 默认 {REGISTER_PORT}（要改请填 register_port）")
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
    # 死-token 白名单：仅这两个码语义 = "该 token 无效/投不出去"（≈ APNs Unregistered），其余码一律【保留】。
    #   80100000 部分 token 失败（单 token 推时 failure 的 illegal_tokens 就是它）/ 80300007 所有 token 无效。
    #   鉴权 802x、权益 80300002、消息超长 80300008、频控、系统错 81xxxxx 都跟 token 死活无关——误删会丢好 token
    #   （鉴权闪一下全台端最惨）。码来自华为官方码表，不拍脑袋。详见 CHANGELOG。
    DEAD_TOKEN_CODES = {"80100000", "80300007"}
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
                elif code in DEAD_TOKEN_CODES:
                    print(f"[PushKit] ✗ {dev_id} code={code} msg={resp.get('msg')} → 该 token 无效")
                    dead.append(dev_id)
                else:
                    # 类 B（鉴权 802x / 权益 80300002 / 消息超长 80300008 / 频控 / 系统错 81xxxxx）：与 token 死活无关 → 保留
                    print(f"[PushKit] ⚠️ {dev_id} code={code} msg={resp.get('msg')} → 保留（非死-token 码，疑系统/参数问题）")
        except urllib.error.HTTPError as e:
            # HTTP 层错（401/403/429/5xx）：系统性/鉴权/限流 → 保留（旧逻辑这里误删，最危险的一刀）
            print(f"[PushKit] ⚠️ {dev_id} HTTP {e.code}: {e.read().decode()[:120]} → 保留（HTTP 层错误，非死 token）")
        except Exception as e:
            print(f"[PushKit] ✗ {dev_id} {type(e).__name__}: {e}（网络？保留 token）")
    if dead:
        # 全局闸门：本轮【至少一台成功】才删。否则多半是系统性故障（鉴权/配置/权益/服务端），死码也可能被误触发
        # （如 app 包名配错时全台返 80300007）→ 一台都不删，防"全锅端"丢好 token。
        if delivered == 0:
            print(f"[PushKit] ⚠️ 本轮 0 台成功，疑系统性故障，保留全部 {len(dead)} 个疑似失效 token（不删）：{dead}")
            dead = []
        else:
            t = load_tokens()           # 重新读（期间可能有新 register），避免覆盖
            for d in dead:
                t.pop(d, None)
            save_tokens(t)
            print(f"[PushKit] 清理 {len(dead)} 个失效 token：{dead}")
    print(f"[PushKit] 推送完成：{delivered} 台成功" + (f"，{len(dead)} 失效已清" if dead else ""))


# ──────────────────────────── 订阅 Gotify（断线回补 + waiting for app）────────────────────────────

_last_msg_id = 0   # 已转发消息最高 id（高水位：去重 + 回补边界）


def _gotify_connect_url():
    """桥实际连 Gotify 的地址：gotify_url_local（部署者填，同机覆盖）> gotify_url（App 上报的域名）。"""
    return _cfg.get("gotify_url_local") or _cfg.get("gotify_url") or ""


_autodetect_done_for = None   # 已【成功】探到同机 Gotify 的 gotify_url（去重）；失败不标记→下次再探


def _autodetect_local_gotify():
    """gotify_url_local 留空时自动探同机 Gotify：从 gotify_url（域名）取端口，试 https://127.0.0.1:端口/version。
    Gotify 应答（带 version）→ 自动填 gotify_url_local。**连不上重试 3 次**（应对瞬时波动，波动掉了不白搞）；
    仍不行→用域名，下次 App 上报/启动再探。仅探成功才标记 done，故失败不会永久放弃。"""
    global _autodetect_done_for
    if _cfg.get("gotify_url_local") or _autodetect_done_for == _cfg.get("gotify_url"):
        return  # 已填 / 已成功探过这个域名
    base = _cfg.get("gotify_url", "")
    if not base:
        return
    parsed = urllib.parse.urlparse(base)
    if not parsed.port or (parsed.hostname or "") in ("127.0.0.1", "localhost", "::1"):
        _autodetect_done_for = base  # 没 port / 已 localhost：这 url 无需探
        return
    local = f"https://127.0.0.1:{parsed.port}"
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
    for attempt in range(3):                       # 重试 3 次，应对瞬时波动
        try:
            with urllib.request.urlopen(f"{local}/version", timeout=2, context=ctx) as r:
                data = json.loads(r.read().decode() or "{}")
                if r.status == 200 and data.get("version"):
                    _cfg["gotify_url_local"] = local
                    _autodetect_done_for = base                    # 成功才标记 done
                    print(f"[Gotify] 🔍 探测到同机 Gotify（{local} → {data.get('version')}），自动走 localhost（免域名/hairpin）。")
                    return
        except Exception:
            pass
        if attempt < 2:
            time.sleep(1)                           # 失败隔 1s 再试（最后一次不睡）
    # 3 次都没探到：不标记 done，下次 App 上报 / 启动有机会再探


def _gotify_ssl_ctx():
    """连 Gotify 的 SSL 上下文（按 _gotify_connect_url 判）。明文 http/ws → None；
    https/wss + localhost(127.0.0.1/::1) → 跳过证书校验（同机 TLS Gotify：域名证书在 127.0.0.1 上主机名对不上，
    本地回环可信故跳过，免 hairpin/代理）；https/wss + 域名 → 默认校验。urllib 传 context=、websockets 传 ssl=。"""
    url = _gotify_connect_url()
    if not url.startswith("https://"):
        return None
    host = urllib.parse.urlparse(url).hostname or ""
    if host in ("127.0.0.1", "localhost", "::1"):
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        return ctx
    return ssl.create_default_context()


def _recent_messages(limit=100):
    """GET /message（最新在前 desc）。无配置返回空。"""
    base = _gotify_connect_url(); tok = _cfg["gotify_token"]
    if not base or not tok:
        return []
    url = f"{base}/message?token={urllib.parse.quote(tok)}&limit={limit}"
    try:
        with urllib.request.urlopen(url, timeout=10, context=_gotify_ssl_ctx()) as r:
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
    base = _gotify_connect_url(); tok = _cfg["gotify_token"]
    ws_url = f"{base.replace('http', 'ws')}/stream?token={urllib.parse.quote(tok)}"
    ssl_ctx = _gotify_ssl_ctx()
    if ssl_ctx is not None and ssl_ctx.verify_mode == ssl.CERT_NONE:
        print("[Gotify] ℹ️ localhost TLS：跳过证书主机名校验（同机回环）")
    print(f"[Gotify] 订阅 {ws_url.replace(tok, '***')}")
    async with websockets.connect(ws_url, ping_interval=20, ssl=ssl_ctx) as ws:
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
        sig = (_gotify_connect_url(), _cfg.get("gotify_token"))
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
