# hotify-bridge

> 📖 这是一篇**小白都能跟着做的部署教程**。
> 想看全部配置/原理/生产拓扑 → [README_FULL.md](./README_FULL.md) ｜ 部署细节（NAS 图形界面/HTTPS）→ [docker.md](./docker.md)
> 🌐 [English](README.en.md) · 📄 [更新日志](CHANGELOG.md)

## 这是干啥的？（30 秒）

你鸿蒙手机装了 **Hotify** App，想让消息**即使 App 没开也能推到锁屏**。`hotify-bridge` 就是那个"中转"小程序——你把它跑在自己的 NAS / 服务器上，它把 **Gotify** 里的消息，经华为 Push Kit 推到你手机锁屏。

```
别人发消息 → Gotify（你自己的消息站）→【hotify-bridge】→ 华为 Push Kit → 你手机锁屏 🔔
```

> 桥**不替代** Gotify——它从 Gotify 读消息再转推。所以 Gotify 和桥都要跑。

---

## 你需要先准备 3 样

1. **一台能联网、能跑 Docker 的机器**：NAS（群晖/威联通）、小主机、VPS 都行。
2. **一个跑起来的 [Gotify](https://gotify.net/)**：免费开源的消息中转。没装？去 [gotify.net](https://gotify.net/) 装一个（有 Docker 一行起）。
3. **鸿蒙手机装 Hotify App**（App 上架后这里放链接）。

---

## 🚀 部署桥（跟着做，3 步）

### 第 1 步：登录你的服务器 / NAS

SSH 进去，或直接在机器上开终端。（群晖：控制面板开 SSH；威联通：Container Station 自带终端。）

确认有 Docker——敲 `docker -v`，能出版本号就行。没有？→ [装 Docker](https://docs.docker.com/engine/install/)

### 第 2 步：复制这一行命令，粘进去，回车

```bash
curl -fsSL https://gitee.com/sakura-lolipop/hotify-bridge/raw/main/install.sh -o install.sh && bash install.sh
```

> 用 Gitee（国内打得开）。GitHub 那条（`raw.githubusercontent.com`）国内打不开，别用。

### 第 3 步：脚本问你两个问题，照填

1. **Gotify token**：打开你的 Gotify 网页 → 左边 `CLIENTS` → `+ Create Client` → 复制那串 Token，粘进来。
2. **Gotify 地址**：你的 Gotify 网址，如 `https://gotify.你域名.com`。
   - 桥和 Gotify 在同一台机？填 `http://gotify容器名:端口` 或先留空（App 会自动上报）。
   - 不确定？先留空，不影响起容器。

回车后，脚本**自己**拉镜像、写好配置、起容器。看到这行就成了：

```
═════════════════ ✅ hotify-bridge 已启动 ═════════════════
```

---

## 📱 手机 App 里接上桥

打开 Hotify App → 设置 → 找到 **「Hotify 推送服务」**：

- **地址**填：`http://你服务器的IP:8080`（端口默认 8080；你第 3 步改过就填改的）
- 存盘。App 会自动把 push token 发给桥。

> 跑在公网？务必给桥配 HTTPS（不然 token 明文跑）。看 [docker.md「公网 HTTPS」](./docker.md)。

---

## ✅ 测一下

去 Gotify 发条测试消息（WebUI 右上 `+`）→ 看鸿蒙手机锁屏有没有响。

- 收到 🔔 → 全链路通了，完事。
- 没收到 → 看下面排错。

---

## 🆘 卡住了？

| 现象 | 怎么办 |
|---|---|
| `docker: command not found` | 先装 Docker：[docs.docker.com/engine/install](https://docs.docker.com/engine/install/) |
| 脚本拉不动镜像 | 默认走国内阿里云镜像，一般没问题；还不行看 [docker.md 排错](./docker.md) |
- **Gotify 连不上**（日志报错 / 收不到）：十有八九是 Gotify 地址填错——容器里 `127.0.0.1` 连不到外面的 Gotify。看 [docker.md「Gotify 寻址」](./docker.md)。
- **想看日志**：`docker logs -f hotify-bridge`
- **改配置**：编辑 `./hotify-bridge-data/bridge_config.yaml`，存盘后 `docker restart hotify-bridge`。
- **没 Docker / 不想用 Docker**：直接下二进制跑——国内从 [Gitee Release](https://gitee.com/sakura-lolipop/hotify-bridge/releases) 下，海外从 [GitHub Release](../../releases)。

---

## 🔧 想深入？

- 🐳 部署细节（NAS 图形界面、公网 HTTPS、多机、排错）→ [**docker.md**](./docker.md)
- 📚 全部配置项、运行原理、生产拓扑、Push Kit 深入 → [**README_FULL.md**](./README_FULL.md) ｜ [BRIDGE.md](./BRIDGE.md)
- 📄 版本变化 → [CHANGELOG.md](./CHANGELOG.md)

## 📄 许可证
MIT。原创代码，与 [Gotify](https://github.com/gotify/server)（MIT，© 其作者）互操作，不含其源码。
