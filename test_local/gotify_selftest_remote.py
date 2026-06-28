#!/usr/bin/env python3
"""0b 远程真实链路自测：wss/https 连 <your-gotify-host>。
读环境变量（token 不落盘）：GOTIFY_URL / CLIENT_TOKEN / APP_TOKEN。
"""
import asyncio, json, os, time, urllib.request, urllib.parse, urllib.error
import websockets

BASE = os.environ["GOTIFY_URL"].rstrip("/")
WS = BASE.replace("http://", "ws://").replace("https://", "wss://")
CLIENT = os.environ["CLIENT_TOKEN"]
APP = os.environ["APP_TOKEN"]
SUF = str(int(time.time()) % 100000)


def http(method, path, token=None, body=None, timeout=15):
    url = BASE + path
    if token:
        url += ("&" if "?" in url else "?") + "token=" + urllib.parse.quote(token)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode())
        except Exception:
            return e.code, e.read().decode()
    except Exception as e:
        return None, str(e)


def ck(n, ok, d):
    print(f"[{'PASS' if ok else 'FAIL'}] {n}: {d}")
    return bool(ok)


async def main():
    res = []
    print(f"目标 {BASE}  suf={SUF}")
    s, v = http("GET", "/health")
    res.append(ck("health", s == 200 and v.get("health") == "green", f"{s} {v}"))
    s, ver = http("GET", "/version")
    res.append(ck("version", s == 200, f"{s} {ver}"))

    # GET /message —— 观察顺序/形状（为桥回补设计）
    s, msgs = http("GET", "/message?limit=5", token=CLIENT)
    ids = []
    order = "?"
    if isinstance(msgs, dict) and isinstance(msgs.get("messages"), list):
        ids = [m.get("id") for m in msgs["messages"]]
        if len(ids) >= 2:
            order = "desc(new先)" if ids[0] > ids[-1] else "asc(old先)"
        else:
            order = "single/empty"
    res.append(ck("GET /message", s == 200 and isinstance(msgs, dict) and "messages" in msgs,
                  f"{s} paging={msgs.get('paging') if isinstance(msgs, dict) else msgs} order={order} ids={ids}"))

    # /stream + POST 测试消息
    async def stream():
        async with websockets.connect(f"{WS}/stream?token={CLIENT}") as ws:
            s2, c = http("POST", "/message", token=APP,
                         body={"title": "Hotify自测0b", "message": f"hello-{SUF}", "priority": 4})
            if s2 != 200:
                return False, f"POST fail {s2} {c}"
            try:
                raw = await asyncio.wait_for(ws.recv(), timeout=12)
                fr = json.loads(raw)
                ok = fr.get("message") == f"hello-{SUF}"
                return ok, f"recv id={fr.get('id')} appid={fr.get('appid')} title={fr.get('title')} match={ok}"
            except asyncio.TimeoutError:
                return False, "timeout 未收到 /stream 帧"
    ok, d = await stream()
    res.append(ck("POST->/stream 实时", ok, d))

    # GET /application 图标
    s, apps = http("GET", "/application", token=CLIENT)
    info = [(a.get("id"), a.get("name"), a.get("image")) for a in apps] if isinstance(apps, list) else apps
    res.append(ck("GET /application", s == 200 and isinstance(apps, list), f"{s} apps={info}"))

    print(f"\n=== {sum(1 for x in res if x)}/{len(res)} PASS ===")
    return all(res)


if __name__ == "__main__":
    raise SystemExit(0 if asyncio.run(main()) else 1)
