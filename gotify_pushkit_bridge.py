"""
gotify_pushkit_bridge.py
================================================================================
Gotify ↔ 华为 Push Kit(HarmonyOS NEXT v3) 转发桥。
链路：[发送方] -> Gotify --/stream--> 本桥 --Push Kit v3--> 鸿蒙(锁屏弹+图标)

配置（bridge_config.yaml）：静态项（register_port/tls_cert_file/tls_key_file，部署者填）+
      gotify_url/gotify_token（首次由 App POST /register 上报、写回持久化【锁定】；或部署者直接 yaml 预填）。
      模型 = first-set wins（照 SSH 主机指纹 TOFU）：桥【未配置】才收 App 的 gotify 上报（App 已先 check 验过），
      【已配置】后一律忽略——防公网攻击者抢首注把后端改成他的 Gotify。要零赛跑：yaml 预填 gotify 即启动即锁。
      gotify 两项都没有 = waiting for app（开 /register 等 App 首注），不拿占位符瞎连。

鉴权（桥侧）：无。桥不再直连 Push Kit，改 HTTP POST 推送服务（见 PushKit.md §10.1）。
      华为服务账号 private.json 锁在推送服务里（§1/§7），桥不含 private → 可开源。
      推送服务侧的 AUTH_TOKEN（可选）由 cloud_function_token 带头（§4.1/§8.1）。
图标：不设 notification.image —— 通知小图标默认就是 Hotify 自己的应用图标（即 logo）。
      image 字段是可选「大图标」：曾取 Gotify 来源 app 图标，但 URL 拼错 + 华为拉不到
      （报 Get image failed, url is invalid），且转发场景下来源恒为 SmsForwarder 无意义，已移除。

依赖：pip install websockets
运行：python -u gotify_pushkit_bridge.py
================================================================================
"""

import asyncio
import ipaddress
import json
import os
import ssl
import sys
import time
import threading
import urllib.request
import urllib.parse
import urllib.error
import websockets

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
    # —— gotify 项（首注锁定：App 首次 POST /register 上报后写回 yaml 锁定；或部署者 yaml 预填；env 仅启动兜底）——
    "gotify_url": "",          # Gotify 地址（智能模式：只填端口→http://127.0.0.1 同机）
    "gotify_token": "",        # Gotify client token（读消息/订阅流；机密）
    # —— 静态项（部署者填，启动读）——
    "gotify_url_local": "",    # 桥连 Gotify 的本地地址，【覆盖】gotify_url。同机填 https://127.0.0.1:端口（自动 skip-verify、免 hairpin）；空 → 用 gotify_url
    "tls_cert_file": "",       # 留空→启动自动探 Gotify config 读 ssl.certfile（0 配置）；手填=override
    "tls_key_file": "",        # 同上（ssl.keyfile）；手填=override
    "gotify_config_path": "",  # Gotify config.yml 路径（留空→自动探 ../gotify/config.yml 等同机路径）；手填=override
    # —— 订阅类字样标注（华为 Push Kit"订阅"类消息分类要求携带订阅类字样，见 push-apply-right）——
    "subscribe_label": "true", # 是否给转发标题加"订阅:"前缀。true=加；false=不加。仅桥端配置（不入 App）。
    # —— 推送服务入口（PushKit.md §10.1/§11：桥不再直连 Push Kit，改 HTTP POST 推送服务）——
    "cloud_function_urls": [],                                  # 默认空（URL 由 TXT 发现：查 _hotify.<gotify_host> 的 DNS TXT）；自托管在 bridge_config override
    "cloud_function_token": "hotifypushkit",                    # AUTH_TOKEN 默认写死（防爬虫，非防推送；自托管在 bridge_config.yaml override；见 §4.1/§8.1）
}
_cfg = dict(_CFG_DEFAULTS)     # 运行时配置；init_config 填，_process_register 改动态项，keep_subscribed 读
_file_lock = threading.Lock()   # 文件读写锁：register（async loop）+ 推送（to_thread）跨线程读写 tokens/subscribe_status/bridge_config，全量 load/save 并发要锁防半写


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
                if val[:1] == "[" and val[-1:] == "]":      # list 字面量 → json.loads
                    try:
                        val = json.loads(val)
                    except Exception:
                        pass                                # 解析失败保留原字符串
                elif isinstance(_CFG_DEFAULTS.get(key), list) and isinstance(val, str):
                    # 宽松归一化：剥外层 brackets/引号/空白（malformed 如 "url"] 也清）→ 单元素 list。多 URL 用 ["a","b"]（json.loads 路径）
                    val = val.strip('[]"\' \t')
                    val = [val] if val else []
                cfg[key] = val
    except FileNotFoundError:
        pass
    except Exception as e:
        print(f"[配置] ❌ 读取 {BRIDGE_CONFIG_FILE} 出错：{e}")
    return cfg


def save_bridge_config():
    """App 上报后持久化动态项：只替换 gotify_url / gotify_token 两行的值，其余（静态项 + # 注释）
    原样保留——不整文件重写，避免丢注释、避免"动"踩"静"。"""
    with _file_lock:
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
        if not wrote_url:                                   # 文件里没这行（罕见）→ 追加
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


def _parse_gotify_config(path):
    """极简解析 Gotify config.yml（只取 server.port + server.ssl.{enabled,certfile,keyfile,letsencrypt.enabled}）。
    不用 PyYAML（桥零额外依赖）——按缩进跟踪 section 路径，提取目标字段。"""
    result = {"port": None, "ssl_enabled": False, "certfile": "", "keyfile": "", "le_enabled": False}
    section = []   # [(name, indent), ...] 当前 section 路径
    with open(path, encoding="utf-8") as f:
        for line in f:
            stripped = line.rstrip("\n")
            if not stripped.strip() or stripped.lstrip().startswith("#"):
                continue
            indent = len(stripped) - len(stripped.lstrip())
            content = stripped.lstrip()
            while section and indent <= section[-1][1]:
                section.pop()
            if content.endswith(":"):
                section.append((content[:-1].strip(), indent))
                continue
            if ":" in content:
                key, val = content.split(":", 1)
                key = key.strip(); val = val.strip().strip('"').strip("'")
                path_str = ".".join(s[0] for s in section) + "." + key
                if path_str == "server.port":
                    try: result["port"] = int(val)
                    except ValueError: pass
                elif path_str == "server.ssl.enabled":
                    result["ssl_enabled"] = val.lower() == "true"
                elif path_str == "server.ssl.certfile":
                    result["certfile"] = val
                elif path_str == "server.ssl.keyfile":
                    result["keyfile"] = val
                elif path_str == "server.ssl.letsencrypt.enabled":
                    result["le_enabled"] = val.lower() == "true"
    return result


def _probe_gotify_config():
    """启动时探同机 Gotify config.yml → 读 port(hint)+ 证书 → 自动加载证书(/register HTTPS)。
    **不设 gotify_url_local**——端口可能不准(Gotify 可能 302 重定向 80→443,或自定义端口)→ 交给 _autodetect_local_gotify
    探 443/80/config-port。证书:config ssl.certfile/keyfile → 否则扫 <config_dir>/certs/(acme.sh 装这,即使 Gotify HTTP)。
    5 种情况显式打印。手动 bridge_config override 优先。"""
    cfg_path = _cfg.get("gotify_config_path") or ""
    candidates = ([cfg_path] if cfg_path else []) + ["../gotify/config.yml", "./gotify/config.yml", "../config.yml"]
    found = next((p for p in candidates if p and os.path.isfile(p)), None)
    if not found:
        print("[配置] ⚠ 未找到 Gotify config.yml（尝试了 ../gotify/config.yml 等）。/register 退 HTTP(LAN)。"
              "需 HTTPS:bridge_config 配 gotify_config_path 指向 Gotify config，或手填 tls_cert_file/tls_key_file。")
        return
    try:
        g = _parse_gotify_config(found)
    except Exception as e:
        print(f"[配置] ⚠ Gotify config 读取失败（{found}）:{e}。/register 退 HTTP(LAN)。")
        return
    # port + ssl hint（给 _autodetect 探测用；不直接设 gotify_url_local——config 端口可能不准）
    _cfg["gotify_config_port"] = g["port"]
    _cfg["gotify_config_ssl"] = g["ssl_enabled"]
    # 证书:config ssl.certfile/keyfile → 否则扫 <config_dir>/certs/(acme.sh 常装这，Gotify HTTP 时 config 无证书)
    certfile = g["certfile"]; keyfile = g["keyfile"]
    if not (certfile and keyfile):
        certs_dir = os.path.join(os.path.dirname(os.path.abspath(found)), "certs")
        if os.path.isdir(certs_dir):
            cfs = [f for f in os.listdir(certs_dir) if f.endswith((".cer", ".pem", ".crt"))]
            kfs = [f for f in os.listdir(certs_dir) if f.endswith(".key")]
            if cfs and kfs:
                certfile = os.path.join(certs_dir, cfs[0])
                keyfile = os.path.join(certs_dir, kfs[0])
    # 加载证书 + 打印
    if certfile and keyfile and os.path.isfile(certfile) and os.path.isfile(keyfile):
        if not _cfg.get("tls_cert_file"): _cfg["tls_cert_file"] = certfile
        if not _cfg.get("tls_key_file"): _cfg["tls_key_file"] = keyfile
        print(f"[配置] ✓ 自动加载证书: cert={certfile} → /register HTTPS（Gotify 端口由探测定）")
    elif g["le_enabled"]:
        print("[配置] ⚠ Gotify 用 Let's Encrypt（证书内部管理，私钥不可读）。/register 退 HTTP(LAN)。"
              "App 需在家庭 WiFi 注册一次。需公网 HTTPS:手填证书（acme.sh/certbot）或 Caddy 反代。")
    elif certfile or keyfile:
        print(f"[配置] ⚠ 证书文件不存在: cert={certfile} key={keyfile}。检查路径。/register 退 HTTP(LAN)。")
    else:
        print("[配置] 未找到证书（Gotify config 无 ssl + certs/ 无证书文件）。/register 退 HTTP(LAN)。"
              "需 HTTPS:手填 tls_cert_file/tls_key_file。")


def _is_private_ip(host):
    """私网 IP（127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16）→ 跳 TLS 验证（LAN/同机可信）。"""
    if host in ("127.0.0.1", "localhost", "::1"):
        return True
    try:
        return ipaddress.ip_address(host).is_private
    except ValueError:
        return False   # 域名（非 IP）→ 公网，不跳


# cloud_function_urls.txt fetch 源（ghproxy.com 优先国内加速 → 直连 fallback）
_CF_TXT_SOURCES = [
    "https://ghproxy.com/https://raw.githubusercontent.com/sakura-lolipop/hotify-bridge/main/cloud_function_urls.txt",
    "https://raw.githubusercontent.com/sakura-lolipop/hotify-bridge/main/cloud_function_urls.txt",
]
_CF_TXT_CACHE = "cloud_function_urls.cache.txt"


def _fetch_cf_urls_from_txt():
    """cloud_function_urls 空时 → fetch cloud_function_urls.txt（GitHub raw,ghproxy.com 优先国内加速）→ 按行解析 URL。
    拉到 → 缓存本地（全挂时用缓存,不空跑）。已配（bridge_config override）→ 跳过。"""
    if _cfg.get("cloud_function_urls"):
        return
    for src in _CF_TXT_SOURCES:
        try:
            with urllib.request.urlopen(src, timeout=8) as r:
                if r.status != 200:
                    continue
                content = r.read().decode("utf-8", errors="replace")
            urls = [ln.strip() for ln in content.splitlines() if ln.strip() and not ln.strip().startswith("#")]
            if urls:
                _cfg["cloud_function_urls"] = urls
                try:
                    with open(_CF_TXT_CACHE, "w", encoding="utf-8") as f:
                        f.write(content)
                except Exception:
                    pass
                tag = "ghproxy" if "ghproxy.com" in src else "直连"
                print(f"[配置] ✓ fetch cloud_function_urls.txt（{tag}）→ {urls}")
                return
        except Exception:
            pass
    # 全挂 → 用缓存
    try:
        with open(_CF_TXT_CACHE, encoding="utf-8") as f:
            content = f.read()
        urls = [ln.strip() for ln in content.splitlines() if ln.strip() and not ln.strip().startswith("#")]
        if urls:
            _cfg["cloud_function_urls"] = urls
            print(f"[配置] ⚠ fetch .txt 全挂,用缓存（{len(urls)} 个 URL）")
            return
    except Exception:
        pass
    print("[配置] ⚠ fetch cloud_function_urls.txt 失败（ghproxy + 直连都挂,无缓存）。请手填 cloud_function_urls")


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
    _probe_gotify_config()        # 探 Gotify config.yml → 自动证书 + gotify_url_local（0 配置；手动 override 优先）
    _autodetect_local_gotify()   # 留空则自动探同机 Gotify（/version probe，_probe 已填则跳过）
    _fetch_cf_urls_from_txt()    # cloud_function_urls 空 → fetch .txt（ghproxy.com 优先国内加速 → 直连 fallback → 缓存）


def _seed_config_file():
    """首次启动：生成带注释的 bridge_config.yaml（部署者直接改）。注释内嵌于此，无 example 模板文件。"""
    d = _CFG_DEFAULTS
    with open(BRIDGE_CONFIG_FILE, "w", encoding="utf-8") as f:
        f.write(f"""# bridge_config.yaml — Hotify 桥配置（首次自动生成，按需修改）
# 格式宽松：每行 `键: 值`，值 = 冒号后整段（反斜杠/冒号原样，引号可选）。# 开头是注释。
# 必填：gotify_token + cloud_function_urls。其余有默认值或自动探测。详见 BRIDGE.md / repourl.md

# Gotify 地址（App 视角）。完整地址 https://你的域名:端口（远程/域名）；或只填端口→同机明文。App 上报会覆盖。
gotify_url: {d['gotify_url']}

# Gotify client token（读消息 / 订阅流；机密，别提交 git）
gotify_token: {d['gotify_token']}

# 桥连 Gotify 的本地地址，覆盖上面的 gotify_url。同机 TLS Gotify 填 https://127.0.0.1:端口（自动跳过证书校验、免 hairpin）；留空→用 gotify_url
gotify_url_local: {d['gotify_url_local']}

# /register 监听端口。留空→默认 25238（启动 ⚠️ 提醒）；填了用填的
register_port:

# Gotify config.yml 路径（留空→自动探 ../gotify/config.yml 等同机路径）。桥启动读它自动加载证书 + Gotify 端口（0 配置）。
gotify_config_path: {d['gotify_config_path']}

# TLS 证书/私钥【文件路径】。留空→自动从 Gotify config 读 ssl.certfile/keyfile（0 配置）；手填=override（如 LE 或非同机）。
tls_cert_file: {d['tls_cert_file']}
tls_key_file: {d['tls_key_file']}

# 订阅类字样标注开关。true（默认）= 转发时给标题加"订阅:"前缀（如"订阅:短信验证码"）；false = 不加。
subscribe_label: {d['subscribe_label']}

# 推送服务入口（桥不直连 Push Kit，HTTP POST 推送服务；private 锁在服务里）。
# 默认 Hotify 托管函数（hotifypushkit.netlify.app/api/push），零配置直接用。自托管改这里（JSON 数组，可多个 fallback）。
cloud_function_urls: {d['cloud_function_urls']}

# 推送服务 AUTH_TOKEN（防爬虫，非防推送）。默认 hotifypushkit（managed）；自托管填你服务侧配的；留空=服务侧没开鉴权。
cloud_function_token: {d['cloud_function_token']}
""")
    print(f"[配置] 未找到 {BRIDGE_CONFIG_FILE}，已生成一份（带注释 + 默认值）。")
    print(f"[配置] ✏️ 请编辑 {BRIDGE_CONFIG_FILE}：必填 gotify_token + cloud_function_urls，存盘后重启。")


# ──────────────────────── 推送服务常量（转发 body 用）────────────────────────
NOTIFY_CATEGORY = "SUBSCRIPTION"   # 通知消息分类 category，须与已开通的自分类权益类目一致。
                              # 订阅类(SUBSCRIPTION)自分类权益 2026-07-02 已审核通过（华为要求配置 category=SUBSCRIPTION；未开通权益时携带该 category 值会归资讯营销）。
TEST_MESSAGE    = False                 # 初期没自分类权益时=True 绕频控（无权益会被 MARKETING 频控）；有权益（服务/通讯类无频控）→False

PUSH_TOKENS_FILE = "push_tokens.json"
SUBSCRIBE_STATUS_FILE = "subscribe_status.json"  # {device_id: bool}，App /register 上报的订阅状态（订阅总开关）
REGISTER_PORT    = 25238     # /register 默认端口（留空→此值；测试/正式各目录 bridge_config.yaml 显式设 register_port）

# 华为服务账号 private.json / JWT 签名 / PUSH_URL / TOKEN_URI 已移除——桥不再直连 Push Kit，
# 改 HTTP POST 推送服务（private 锁在推送服务里）。详见 PushKit.md §10.1。


# ──────────────────────────── 设备 token 注册表 + App 配置上报 ────────────────────────────

def load_tokens():
    with _file_lock:
        try:
            with open(PUSH_TOKENS_FILE, "r", encoding="utf-8") as f:
                return json.load(f)
        except FileNotFoundError:
            return {}


def save_tokens(d):
    with _file_lock:
        with open(PUSH_TOKENS_FILE, "w", encoding="utf-8") as f:
            json.dump(d, f, ensure_ascii=False, indent=2)


def load_subscribe_status() -> dict:
    """{device_id: bool}，App 上报的订阅状态。未记录的设备默认订阅（不破坏老设备 / 首装未上报的）。"""
    with _file_lock:
        try:
            with open(SUBSCRIBE_STATUS_FILE, "r", encoding="utf-8") as f:
                return json.load(f)
        except FileNotFoundError:
            return {}


def save_subscribe_status(d):
    with _file_lock:
        with open(SUBSCRIBE_STATUS_FILE, "w", encoding="utf-8") as f:
            json.dump(d, f, ensure_ascii=False, indent=2)


def _process_register(payload: dict) -> dict:
    """POST /register 处理逻辑（同步文件 IO + _cfg 改）。原 RegisterHandler.do_POST 的处理部分，
    抽出来供 async handle_register 在 loop 里直接调（文件 IO ms 级，阻塞 loop 可接受；register 低频）。
    返回响应 dict（JSON body）。
    模型 = first-set wins（照 SSH 主机指纹 TOFU + bark 式 device key）：
      · push token：每次都注册/刷新（token 会变，桥要最新的；返 device_known=是否之前已登记过）。
      · gotify 配置：桥【未配置】才收 App 上报（App 已先 check 验过）→ 写回 yaml 持久化 = 锁定；
        桥【已配置】后 App 再发的 gotify 一律【忽略】——防公网攻击者抢首注把后端改成他的 Gotify。
        要零赛跑：在 yaml 预填 gotify，桥启动即"已配置"，/register 永不收首注。"""
    client = payload.get("client", "default")
    push_token = payload.get("token") or payload.get("push_token")

    # 1) 华为 push token：每次都写（token 会刷新；无 agconnect 时为空 → 跳过）。
    device_known = False
    if push_token:
        tokens = load_tokens()
        device_known = client in tokens          # 这个 UUID 之前是否已登记过（反馈用：新设备 vs 已注册过）
        tokens[client] = push_token
        save_tokens(tokens)
        print(f"[注册] {client} push token -> {push_token[:12]}... ({'已登记/刷新' if device_known else '新设备'})")

    # 订阅状态（订阅总开关）：每次都写（像 push_token，【不锁定】——首注锁定只管 gotify，
    # subscribed 走 push_token 同款"每次刷新"路径，故反复订阅/取消都生效，不触发忽略）。
    # App 端默认 false（华为要求"订阅按钮默认关闭"）；未上报的设备 send_to_huawei 视为订阅（不破坏老设备）。
    subscribed = payload.get("subscribed")
    if subscribed is not None:
        status = load_subscribe_status()
        status[client] = bool(subscribed)
        save_subscribe_status(status)
        print(f"[注册] {client} subscribed={'订阅' if subscribed else '已取消'}")

    # 2) gotify 配置：first-set wins，之后锁（防公网抢首注改后端）。
    gurl = normalize_gotify_addr(payload.get("gotify_url") or "")
    gtok = payload.get("gotify_token") or ""
    already = bool(_cfg.get("gotify_url") and _cfg.get("gotify_token"))   # 桥已配置（yaml 预填 或 之前首注过）
    gotify_set = False
    ignored_gotify = False
    if not already:
        if gurl and gtok:                          # App 带了已验过的 gotify → 首注 + 锁定
            _cfg["gotify_url"] = gurl
            _cfg["gotify_token"] = gtok
            save_bridge_config()                   # 写回 yaml 持久化 → 重启不丢 = 锁定
            gotify_set = True
            print(f"[注册] 首次收到 App 的 Gotify 配置，已保存：url={_cfg['gotify_url']} token=***已设置***")
            # _autodetect 异步（后台线程，不阻塞 200 响应；旧同步 ~9s 会卡 HAP 8s connectTimeout）
            threading.Thread(target=_autodetect_local_gotify, daemon=True).start()
        # App 没带 gotify（纯 token 刷新）→ 桥仍 waiting，不动配置
    else:
        if gurl or gtok:                            # 桥已配置 → App 的 gotify 一律忽略（防改后端）
            ignored_gotify = True
            print(f"[注册] Hotify 推送服务配置已存在，本次忽略。需要修改 Gotify 配置：手动修改 bridge_config.yaml 后重启 Hotify 推送服务")

    if not (push_token or gotify_set or ignored_gotify):
        print(f"[注册] {client} 上报为空（无 token 无配置）")

    return {"ok": True, "device_known": device_known, "gotify_set": gotify_set, "ignored_gotify": ignored_gotify}


def _send_http_response(writer: asyncio.StreamWriter, code: int, body: str) -> None:
    """写 HTTP/1.1 响应（手写 status line + headers + body 到 StreamWriter）。
    手写是因为 asyncio.start_server 不带 HTTP 解析（和 BaseHTTPRequestHandler 不同），自己拼最轻量。"""
    reason = {200: "OK", 400: "Bad Request", 404: "Not Found", 500: "Internal Server Error"}.get(code, "OK")
    body_bytes = body.encode("utf-8")
    head = (
        f"HTTP/1.1 {code} {reason}\r\n"
        "Content-Type: application/json\r\n"
        f"Content-Length: {len(body_bytes)}\r\n"
        "Connection: close\r\n"
        "\r\n"
    )
    writer.write(head.encode("utf-8") + body_bytes)


async def handle_register(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    """async register handler（asyncio.start_server 回调）。
    手动解析 HTTP POST /register + body JSON，调 _process_register，写响应。
    单 loop async 并发——无 race（_process_register 在 loop 直接跑，文件 IO ms 级不阻塞 loop 久）；
    客户端断开（HAP 8s 超时）→ IncompleteReadError 吞掉，writer.close 不 BrokenPipe（async 自动）。
    这根治了旧 HTTPServer 单线程 + accept backlog 满 + fd 泄漏的死锁：每连接独立协程，互不阻塞。"""
    try:
        # 读 request line + headers（到 \r\n\r\n）
        head = await reader.readuntil(b"\r\n\r\n")
        head_str = head.decode("utf-8", errors="replace")
        lines = head_str.split("\r\n")
        request_line = lines[0] if lines else ""
        if "POST" not in request_line.upper() or "/register" not in request_line:
            _send_http_response(writer, 404, '{"ok":false}')
            return
        # 解析 Content-Length
        content_length = 0
        for line in lines[1:]:
            if line.lower().startswith("content-length:"):
                try:
                    content_length = int(line.split(":", 1)[1].strip())
                except ValueError:
                    pass
                break
        # 读 body
        body_bytes = await reader.readexactly(content_length) if content_length > 0 else b"{}"
        try:
            payload = json.loads(body_bytes or b"{}")
        except json.JSONDecodeError:
            _send_http_response(writer, 400, '{"ok":false}')
            return
        # 处理（同步文件 IO ms 级，直接在 loop 跑；_autodetect 已异步，不卡）
        result = _process_register(payload)
        _send_http_response(writer, 200, json.dumps(result))
    except asyncio.IncompleteReadError:
        # 客户端断开（HAP 8s 超时）——吞掉，不 BrokenPipe（async writer.close 安全）
        pass
    except Exception as e:
        print(f"[注册] handler 异常: {e}")
        try:
            _send_http_response(writer, 500, '{"ok":false}')
        except Exception:
            pass
    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass


async def start_register_server() -> None:
    """App 上报接口（asyncio.start_server，整合进主 loop）。
    tls_cert_file/tls_key_file（自动探 Gotify config 或手填）→ https；空 → 明文 http（仅 LAN/调试）。
    端口 = register_port（留空→默认 25238；测试/正式各目录 bridge_config.yaml 显式设）。"""
    port_cfg = _cfg.get("register_port")
    port = int(port_cfg or REGISTER_PORT)
    cert = _cfg["tls_cert_file"]
    key = _cfg["tls_key_file"]
    ssl_ctx = None
    if cert and key:
        ssl_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ssl_ctx.load_cert_chain(certfile=cert, keyfile=key)
        print(f"[注册接口] 模式=HTTPS  https://0.0.0.0:{port}/register  （证书：{cert}）")
    else:
        print(f"[注册接口] 模式=HTTP  http://0.0.0.0:{port}/register")
        print(f"[注册接口] ⚠️ 降级明文 http：tls_cert_file/tls_key_file 未配 → /register 走明文，"
              "公网上报 push token 会裸奔。仅 LAN/调试可接受；公网部署请配 TLS。")
    if not port_cfg:
        print(f"[注册接口] ⚠️ 用默认端口 {port}：register_port 留空 → 默认 {REGISTER_PORT}（要改请填 register_port）")
    server = await asyncio.start_server(handle_register, "0.0.0.0", port, ssl=ssl_ctx)
    async with server:
        await server.serve_forever()


# ──────────────────────────── 推送到华为（经推送服务，PushKit.md §10.1）────────────────────────────

# 死-token 白名单：仅这两个码语义 = "该 token 无效/投不出去"（≈ APNs Unregistered），其余码一律【保留】。
#   80100000 部分 token 失败（单 token 推时 failure 的 illegal_tokens 就是它）/ 80300007 所有 token 无效。
#   鉴权 802x、权益 80300002、消息超长 80300008、频控、系统错 81xxxxx 都跟 token 死活无关——误删会丢好 token
#   （鉴权闪一下全台端最惨）。码来自华为官方码表（PushKit.md §5.3），不拍脑袋。
DEAD_TOKEN_CODES = {"80100000", "80300007"}

# 推送服务返 HTTP 502（pushkit_http_error / pushkit_timeout）或网络异常时重试次数（PushKit.md §8.3）。
# 固定 3 次，简单间隔；同 notifyId → Push Kit 原生覆盖 → 防重复推送。
PUSH_RETRY_LIMIT = 3
PUSH_RETRY_INTERVAL = 1.0   # 秒（重试间隔；PushKit.md §8.3「简单间隔」，不指数退避——量小 YAGNI）


def _post_to_push_service(url, token, body, notify_id):
    """向单个推送服务 URL 发一次 POST，返回 (status, code_str_or_None, msg_or_err)。
    status ∈ {"delivered","dead","system_error","retry"}：
      - delivered     : HTTP 200 + code="80000000"
      - dead          : HTTP 200 + code ∈ DEAD_TOKEN_CODES（80100000/80300007）
      - system_error  : HTTP 200 + 其他 code（鉴权/权益/超长/频控/系统错，保留 token），
                        或 HTTP 500/其他 5xx、401、400（PushKit.md §8.2）
      - retry         : HTTP 502（pushkit_http_error/pushkit_timeout，PushKit.md §4.2）或网络异常/超时 → 调用方重试
    code_str：HTTP 200 时为 Push Kit code（字符串）；否则 None。
    msg：人类可读的诊断串（code/msg 或 HTTP body 片段或异常名）。"""
    headers = {"Content-Type": "application/json"}
    if token:                                   # cloud_function_token 非空才带（PushKit.md §4.1/§8.1）
        headers["Authorization"] = f"Bearer {token}"
    data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    try:
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")  # 移入 try：URL 缺 scheme 时 Request() 抛 ValueError
        with urllib.request.urlopen(req, timeout=15) as r:   # 15s：推送服务内部 10s 调 Push Kit + 余量
            resp_raw = r.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        body_snippet = ""
        try:
            body_snippet = e.read().decode("utf-8", errors="replace")[:160]
        except Exception:
            pass
        if e.code == 502:
            # Push Kit HTTP 错/超时（PushKit.md §4.2：pushkit_http_error / pushkit_timeout）→ 重试
            return ("retry", None, f"HTTP 502 {body_snippet}")
        if e.code == 401:
            # AUTH_TOKEN 配错（PushKit.md §8.2）→ 配置问题，重试也没用，但归 SystemError 保留 token
            return ("system_error", None, f"HTTP 401 unauthorized（cloud_function_token 配错？）{body_snippet}")
        if e.code == 400:
            # 请求格式错（缺 token 等，PushKit.md §8.2）→ 调用方代码 bug，SystemError 保留
            return ("system_error", None, f"HTTP 400 bad request {body_snippet}")
        # 其他 5xx（500 等）→ SystemError 保留（PushKit.md §8.2）
        return ("system_error", None, f"HTTP {e.code} {body_snippet}")
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        # 网络异常/连不上/超时 → 重试（可能某 URL 瞬时挂，fallback 或重试能救）
        return ("retry", None, f"{type(e).__name__}: {e}")
    except ValueError as e:
        # URL 格式错（缺 scheme 等，配置问题）→ SystemError 保留 token，不重试（防 Request() ValueError 窜到 Gotify 流）
        return ("system_error", None, f"URL 格式错（检查 cloud_function_urls 带 https://）: {e}")
    except Exception as e:
        # 未预期异常 → 保守归 SystemError 保留 token（不删不重试，等下次）
        return ("system_error", None, f"{type(e).__name__}: {e}")

    # HTTP 200：解析 Push Kit 原始响应的 code（PushKit.md §4.2：body 原样透传，code 是字符串）
    try:
        resp = json.loads(resp_raw or "{}")
    except json.JSONDecodeError:
        # 推送服务返了非 JSON（不合规）→ SystemError 保留
        return ("system_error", None, f"HTTP 200 但 body 非 JSON：{resp_raw[:160]}")
    code = str(resp.get("code"))
    msg = resp.get("msg")
    if code == "80000000":
        return ("delivered", code, msg)
    if code in DEAD_TOKEN_CODES:
        return ("dead", code, msg)
    # 其他 code（鉴权 802x / 权益 80300002 / 消息超长 80300008 / 频控 / 系统错 81xxxxx）→ 保留 token
    return ("system_error", code, f"code={code} msg={msg}")


def send_to_huawei(title, message, priority=4, extras=None, ts="", notify_id=0):
    """转发一条 Gotify 消息到所有已注册鸿蒙设备（经推送服务，非直连 Push Kit）。
    流程（docs/pushkit-delivery.md）：
      1) 构造 notification（含 clickAction.data={ts}：click 时平铺进 App want.parameters，App 直读 ts 反查切来源）。
      2) 逐设备：遍历 cloud_function_urls（fallback），每 URL 重试 ≤3 次（同 notify_id 幂等，
         Push Kit 原生覆盖防重复）。拿到 Push Kit code 按码表分类。
      3) 全局闸门：本轮 delivered==0 则不删任何死 token（防系统性故障误删）。
    notify_id = Gotify msgId（_forward 传入）—— 重试同 id，Push Kit 覆盖防重复。"""
    urls = _cfg.get("cloud_function_urls") or []
    cf_token = _cfg.get("cloud_function_token") or ""
    if not urls:
        print(f"[PushKit] ⏭ 跳过推送（cloud_function_urls 未配置）：{title or '(无标题)'} | {(message or '')[:40]}")
        return
    devs = load_tokens()  # {device_id: push_token}
    if not devs:
        print("[PushKit] 还没注册设备，跳过"); return

    # 订阅类字样标注：华为 Push Kit 通知消息分类要求"订阅"类(SUBSCRIPTION)消息在标题或正文
    # 携带"订阅/预约/关注"等字样（见 push-apply-right#订阅流程要点）。subscribe_label=true 时
    # 给消息加"订阅:"前缀以符合该分类标注要求；false=不加。
    if _cfg.get("subscribe_label", "true").lower() in ("true", "1", "yes", "on"):
        if title:
            title = f"订阅:{title}"
        else:
            message = f"订阅:{message or ''}".strip()

    # notifyId（进 notification）：Gotify msgId = 全局递增整数，重试同 id → Push Kit 原生覆盖防重复。0 省略。
    notify_id_int = int(notify_id) if notify_id else 0

    # 订阅状态过滤：subscribed=false 的设备跳过（用户在 App 取消了订阅）。
    # 未记录的设备默认订阅（get(dev_id, True)），不破坏老设备 / 首装还没上报订阅状态的。
    sub_status = load_subscribe_status()
    delivered, dead = 0, []
    for dev_id, tok in list(devs.items()):
        if not sub_status.get(dev_id, True):    # False = 用户取消订阅 → 跳过该设备（不推、不计数、不清 token）
            continue

        # 转发 body（docs/pushkit-delivery.md）：token + notification 对象（云函数原样透传不解释）+ data 串。
        # clickAction.data = {ts}：click 时平铺进 App want.parameters（实证），App 直读 ts 反查切来源。
        # actionType 必须 0（1 要 action/uri → 80100003，见 pushkit-delivery.md §6）。ts=Gotify msg.date 原值。
        # ⚠️ 控制数据走 clickAction.data，不走顶层 data（顶层 payload.data 不进 click Want，只走到达订阅，实证 §5）。
        notification = {
            "category": NOTIFY_CATEGORY,
            "title": title or "Hotify",
            "body": message,
            "clickAction": {"actionType": 0, "data": {"ts": ts}},
        }
        if notify_id_int:
            notification["notifyId"] = notify_id_int
        data_obj = dict(extras or {})   # 顶层 data 透传 extras + ts（B 方案：ts 走 data，云函数 message.data 透传）
        # ts 反查（B 方案）：塞 data_obj → 云函数 message.data 透传 → HMS 点通知 Intent extras 带 ts → app getString("ts")。
        #   鸿蒙端 ts 走 clickAction.data（line 647，click 平铺 want.parameters）；安卓端 ts 走 data（这里）。
        if ts:
            data_obj["ts"] = str(ts)
        body = {
            "token": tok,
            "notification": notification,
            "data": json.dumps(data_obj, ensure_ascii=False),
            "testMessage": TEST_MESSAGE,
        }

        # 遍历 URLs（fallback，PushKit.md §11）：urls[0] 失败/超时 → urls[1] → ... → 都失败放弃（保留 token）。
        # 每 URL 内部再重试 ≤3 次（仅 retry 状态重试；delivered/dead/system_error 终态即出）。
        final_status, final_msg = None, ""
        for url in urls:
            attempt_status, attempt_msg = None, ""
            for attempt in range(1, PUSH_RETRY_LIMIT + 1):
                status, code, msg = _post_to_push_service(url, cf_token, body, notify_id_int)
                attempt_status, attempt_msg = status, (msg or "")
                if status == "delivered":
                    print(f"[PushKit] ✓ {dev_id} code=80000000  (url={url})")
                    break
                if status == "dead":
                    print(f"[PushKit] ✗ {dev_id} code={code} msg={msg} → 该 token 无效  (url={url})")
                    break
                if status == "system_error":
                    # code 来源已是 Push Kit 原始响应（非死码：鉴权/权益/超长/频控/系统错）或 HTTP 5xx/401/400
                    # → 与 token 死活无关，保留（PushKit.md §8.2）
                    print(f"[PushKit] ⚠️ {dev_id} {msg} → 保留（非死-token，疑系统/参数问题）  (url={url})")
                    break
                # status == "retry"：502/超时/网络 → 同 URL 重试（PushKit.md §8.3，同 notify_id 幂等）
                if attempt < PUSH_RETRY_LIMIT:
                    print(f"[PushKit] ↻ {dev_id} {msg} → 重试 {attempt+1}/{PUSH_RETRY_LIMIT}  (url={url})")
                    time.sleep(PUSH_RETRY_INTERVAL)
            # 走出重试循环：要么拿到终态，要么 retry 用尽
            final_status, final_msg = attempt_status, attempt_msg
            if attempt_status in ("delivered", "dead", "system_error"):
                break                       # 拿到终态 → 不再 fallback 下一个 URL
            # attempt_status == "retry" 且用尽 3 次 → 试下一个 URL（fallback）

        # 所有 URL 都试完，按最终状态汇总
        if final_status == "delivered":
            delivered += 1
        elif final_status == "dead":
            dead.append(dev_id)
        else:
            # system_error 或 retry 用尽（所有 URL 都 502/超时）→ 保留 token，下次新消息再推
            if final_status == "retry":
                print(f"[PushKit] ✗ {dev_id} 所有 URL 重试用尽仍失败 → 保留 token（下次再推）：{final_msg}")
            # system_error 已在上面打印过

    if dead:
        # 全局闸门（PushKit.md §5.3/§10.1）：本轮【至少一台成功】才删。否则疑系统性故障
        # （鉴权/配置/权益/服务端），死码也可能被误触发（如 app 包名配错全台返 80300007）→ 一台都不删，防"全锅端"丢好 token。
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
    """探同机 Gotify:试 443(HTTPS)→ 80(HTTP)→ config 端口(若有)→ gotify_url 端口(若私网)。
    首个 /version 应答 → gotify_url_local。**不信任 config 端口**（Gotify 可能 302 重定向 80→443,或自定义端口）。
    私网 IP(127/10.x/192.168)跳 TLS 验证。探成功才标记 done,失败下次 App 上报/启动再探。"""
    global _autodetect_done_for
    if _cfg.get("gotify_url_local") or _autodetect_done_for == _cfg.get("gotify_url"):
        return
    # 候选 (scheme, port)：443 HTTPS 优先 → 80 HTTP → config 端口 → gotify_url 端口(若私网)
    candidates = [("https", 443), ("http", 80)]
    cfg_port = _cfg.get("gotify_config_port")
    cfg_ssl = _cfg.get("gotify_config_ssl", False)
    if cfg_port and cfg_port not in (443, 80):
        candidates.append(("https" if cfg_ssl else "http", cfg_port))
    base = _cfg.get("gotify_url", "")
    if base:
        parsed = urllib.parse.urlparse(base)
        if parsed.port and parsed.port not in (443, 80, cfg_port):
            candidates.append((parsed.scheme or "https", parsed.port))   # gotify_url 端口也探 127.0.0.1（同机,域名也试——免 hairpin）
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT); ctx.check_hostname = False; ctx.verify_mode = ssl.CERT_NONE
    for scheme, port in candidates:
        local = f"{scheme}://127.0.0.1:{port}"
        try:
            with urllib.request.urlopen(f"{local}/version", timeout=2, context=ctx) as r:
                data = json.loads(r.read().decode() or "{}")
                if r.status == 200 and data.get("version"):
                    _cfg["gotify_url_local"] = local
                    _autodetect_done_for = base or local
                    print(f"[Gotify] 🔍 探到同机 Gotify（{local} → {data.get('version')}），自动走 localhost。")
                    return
        except Exception:
            pass
    # 都没探到:不标记 done,下次 App 上报/启动再探


def _gotify_ssl_ctx():
    """连 Gotify 的 SSL 上下文（按 _gotify_connect_url 判）。明文 http/ws → None；
    https/wss + 私网 IP（127.0.0.1/10.x/172.16-31/192.168）→ 跳过证书校验（同机/LAN TLS Gotify：域名证书在 IP 上主机名对不上，
    私网可信故跳过）；https/wss + 公网域名 → 默认校验。urllib 传 context=、websockets 传 ssl=。"""
    url = _gotify_connect_url()
    if not url.startswith("https://"):
        return None
    host = urllib.parse.urlparse(url).hostname or ""
    if _is_private_ip(host):
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
        print(f"[Gotify] 从最新消息（id={_last_msg_id}）开始，只转发之后的新消息，历史不补推")


def _forward(msg, tag="实时"):
    """转发单条，按 id 去重（高水位）。/stream 与回补共用 → 天然不重不漏。"""
    global _last_msg_id
    mid = msg.get("id", 0)
    if mid <= _last_msg_id:
        return
    _last_msg_id = mid
    send_to_huawei(msg.get("title") or "", msg.get("message") or "",
                   msg.get("priority", 4), msg.get("extras"),
                   ts=msg.get("date", ""),
                   notify_id=msg.get("id", 0))
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
                print("[Hotify 推送服务] ⏳ 等待 App 上报 Gotify 配置。"
                      "在 App「设置」填 Gotify 地址 + client token 并保存，本服务会自动接上订阅。")
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

async def _main_async() -> None:
    """主 loop：register server + keep_subscribed 并发（asyncio.gather，单 loop）。
    register 不再独立 daemon 线程——和 Gotify 订阅同 loop，async 并发，无 race 无 GIL 争用。
    旧架构（HTTPServer daemon 线程 + async 主线程）长跑后单线程阻塞 + accept backlog 满 + fd 泄漏致死锁；
    新架构每 register 连接独立协程，互不阻塞，根治。"""
    await asyncio.gather(
        start_register_server(),
        keep_subscribed(),
    )


if __name__ == "__main__":
    init_config()           # 读持久化(App上报)/env + _autodetect；都没有则空 = waiting for app
    if _cfg["gotify_url"] and _cfg["gotify_token"]:
        print(f"[Hotify 推送服务] 已有 Gotify 配置：{_cfg['gotify_url']}")
    else:
        print("[Hotify 推送服务] 无 Gotify 配置，等待 App 上报（开 /register 等）")
    cf_urls = _cfg.get("cloud_function_urls") or []
    if cf_urls:
        print(f"[Hotify 推送服务] 推送入口：{cf_urls[0]}" + (f"（+{len(cf_urls)-1} 个备用）" if len(cf_urls) > 1 else ""))
    else:
        print("[Hotify 推送服务] ⚠️ cloud_function_urls 未配置，Push Kit 转发将跳过（在 bridge_config.yaml 填）")
    try:
        asyncio.run(_main_async())
    except KeyboardInterrupt:
        print("\n退出。")
