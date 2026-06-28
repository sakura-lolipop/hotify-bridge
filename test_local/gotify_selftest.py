#!/usr/bin/env python3
"""Hotify 桥↔Gotify 本地自测：对 http://127.0.0.1:25243 验证 Gotify API 全链路。
依赖：websockets（/stream 用）；其余 HTTP 用 stdlib。
产出：确认 API 形状 + /message 单数 + /stream 实时收到。"""
import asyncio, base64, json, time, urllib.request, urllib.parse, urllib.error
import websockets

BASE = "http://127.0.0.1:25243"
WS = "ws://127.0.0.1:25243"
SUFFIX = str(int(time.time()) % 100000)


def http(method, path, token=None, basic=None, body=None, timeout=10):
    url = BASE + path
    if token:
        url += ("&" if "?" in url else "?") + "token=" + urllib.parse.quote(token)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if basic:
        req.add_header("Authorization", "Basic " + base64.b64encode(basic.encode()).decode())
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw
    except Exception as e:
        return None, str(e)


def check(name, ok, detail):
    print(f"[{'PASS' if ok else 'FAIL'}] {name}: {detail}")
    return ok


def wait_up(timeout=25):
    t0 = time.time()
    while time.time() - t0 < timeout:
        s, _ = http("GET", "/health", timeout=3)
        if s == 200:
            return True
        time.sleep(1)
    return False


async def main():
    res = []
    print(f"目标：{BASE}（suffix={SUFFIX}）")
    if not wait_up():
        print("[FATAL] Gotify 没起来"); return False

    # 1. health + version
    s, v = http("GET", "/health")
    res.append(check("1a GET /health", s == 200 and v.get("health") == "green", f"{s} {v}"))
    s, ver = http("GET", "/version")
    res.append(check("1b GET /version", s == 200 and "version" in (ver or {}), f"{s} {ver}"))

    # 2. POST /application (Basic admin:admin) -> appToken
    s, app = http("POST", "/application", basic="admin:admin", body={"name": f"sms-test-{SUFFIX}"})
    app_token = app.get("token") if isinstance(app, dict) else None
    res.append(check("2 POST /application -> appToken", s == 200 and app_token,
                     f"{s} id={app.get('id') if isinstance(app, dict) else app}"))

    # 3. POST /client (Basic) -> clientToken  (= 登录)
    s, cli = http("POST", "/client", basic="admin:admin", body={"name": f"hotify-test-{SUFFIX}"})
    client_token = cli.get("token") if isinstance(cli, dict) else None
    cli_safe = {k: v for k, v in (cli or {}).items() if k != "token"} if isinstance(cli, dict) else cli
    res.append(check("3 POST /client -> clientToken (=登录)", s == 200 and client_token,
                     f"{s} {cli_safe}"))

    # 4. GET /message -> PagedMessages{messages,paging}
    s, msgs = http("GET", "/message", token=client_token)
    shape = isinstance(msgs, dict) and "messages" in msgs and "paging" in msgs
    res.append(check("4 GET /message 单数 -> PagedMessages", s == 200 and shape,
                     f"{s} keys={list(msgs.keys()) if isinstance(msgs, dict) else msgs}"))

    # 5+6. POST /message (appToken) 然后 ws /stream (clientToken) 收到
    async def stream_test():
        async with websockets.connect(f"{WS}/stream?token={client_token}") as ws:
            s2, created = http("POST", "/message", token=app_token,
                               body={"title": "自测", "message": f"hello-{SUFFIX}", "priority": 4})
            if s2 != 200:
                return False, f"POST /message 失败 {s2} {created}"
            try:
                raw = await asyncio.wait_for(ws.recv(), timeout=8)
                fr = json.loads(raw)
                ok = fr.get("message") == f"hello-{SUFFIX}"
                return ok, f"recv id={fr.get('id')} title={fr.get('title')} body={fr.get('message')} match={ok}"
            except asyncio.TimeoutError:
                return False, "超时未收到 /stream 帧"
    ok, det = await stream_test()
    res.append(check("5+6 POST /message -> /stream 实时收到", ok, det))

    # 7. GET /application -> image 路径
    s, apps = http("GET", "/application", token=client_token)
    img = apps[0].get("image", "") if isinstance(apps, list) and apps else ""
    res.append(check("7 GET /application -> image 路径", s == 200 and isinstance(apps, list),
                     f"{s} count={len(apps) if isinstance(apps, list) else apps} sample_image={img}"))

    print(f"\n=== 汇总 {sum(1 for x in res if x)}/{len(res)} PASS ===")
    return all(res)


if __name__ == "__main__":
    raise SystemExit(0 if asyncio.run(main()) else 1)
