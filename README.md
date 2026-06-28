# hotify-bridge

> Gotify → Huawei Push Kit bridge for **[Hotify](#)** — a HarmonyOS NEXT notification-forwarding client.
> Subscribes to a Gotify message stream and forwards each message to Huawei Push Kit, so it lands on your HarmonyOS **lock screen** — even when the app isn't running.

```
[sender] → Gotify (store + /stream) → 【this bridge】→ Huawei Push Kit v3 → HarmonyOS lock screen
```

This is the **server-side** half of Hotify. The HarmonyOS client app lives in a separate (closed-source) repo. The bridge is **self-hostable** — you run it next to your own Gotify instance, so your notifications only ever pass through infrastructure you control.

## ✨ What it does
- Subscribes to Gotify's `/stream` (WebSocket) for real-time messages.
- Forwards each message to Huawei Push Kit v3 (`POST /v3/{project_id}/messages:send`) as a lock-screen notification.
- Reconnects automatically on disconnect and **backfills** any messages missed while down (id high-watermark dedup — no doubles, no drops).
- Exposes `POST /register` for the app to upload its push token + Gotify config.
- Per-token delivery with automatic dead-token cleanup (bark-style).

## 🔗 Built to work with Gotify
This bridge is original Python code that talks to a [Gotify](https://github.com/gotify/server) server (MIT). It does **not** bundle Gotify — run Gotify separately. Hotify reuses Gotify's protocol, storage and streaming; only the last-mile delivery is swapped from FCM to Huawei Push Kit.

## 📋 Prerequisites
- Python 3.8+
- A running [Gotify](https://github.com/gotify/server) server (self-hosted)
- A Huawei AGC project with **Push Kit** enabled + a **service account** key (`private.json`, RSA) — see [push-jwt-token](https://developer.huawei.com/consumer/cn/doc/harmonyos-guides/push-jwt-token)
- Python deps: `pip install websockets PyJWT cryptography`

## 🚀 Quick start
```bash
git clone <this-repo> hotify-bridge && cd hotify-bridge
pip install websockets PyJWT cryptography

# 1) Gotify CLIENT token (reads messages / subscribes to /stream)
#    Gotify WebUI → CLIENTS → Create Client → copy Token
#    (NOT the app token — that's for SENDING only)
# 2) Huawei service-account key → save as private.json
#    Huawei Developer Console → your project → Service Account → create → download JSON

cp bridge_config.example.json bridge_config.json   # then fill in YOUR values
python -u gotify_pushkit_bridge.py
```

## ⚙️ Configuration
Gotify config is read in priority order: **app upload** (`POST /register`, persisted to `bridge_config.json`) > **environment variables** > nothing (`waiting for app`).

| Where | Keys | Notes |
|---|---|---|
| `bridge_config.json` | `gotify_url`, `gotify_token` | Copy from `.example`. **gitignored — never commit your real token.** |
| Env | `GOTIFY_HTTP_URL`, `GOTIFY_CLIENT_TOKEN` | Headless fallback |
| `private.json` | Huawei service account (RSA key) | AGC download. **gitignored.** Missing = "spine mode" (subscribes, skips Push Kit). |
| `push_tokens.json` | device push tokens | Auto-managed from app uploads. gitignored. |

**Gotify address smart mode**: enter just a port (e.g. `25234`) → bridge assumes Gotify is co-located and connects `http://127.0.0.1:<port>` (fastest, no TLS). Enter a full URL → remote Gotify (wss/https, needs a valid cert).

## 🔧 Two run modes
- **Spine mode** (no `private.json`): subscribes to Gotify `/stream` + backfill work, but **skips Push Kit delivery** (logs `⏭ skip`). Use it to validate the Gotify link first.
- **Full mode** (with `private.json`): end-to-end → HarmonyOS lock screen.

## 🔒 Security
- **Never commit** `private.json`, `bridge_config.json`, or `push_tokens.json` — they're gitignored. Copy the `.example` and fill it in locally.
- Run the bridge **co-located with Gotify** on `127.0.0.1` when possible (avoids exposing Gotify to the internet).
- Auth uses a Huawei **service-account JWT as the Bearer token directly** (the official `push-jwt-token` flow) — *not* client_id/secret. See `BRIDGE.md` for the deep dive.

## 📖 More
Full runbook, troubleshooting, and the Push Kit auth deep-dive: see [`BRIDGE.md`](./BRIDGE.md).

## 📄 License
MIT. This bridge is original code; it interoperates with [Gotify](https://github.com/gotify/server) (MIT, © its authors) but does not include Gotify source.
