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

## 🧰 第 0 步：先把这几样备齐（小白重点看，缺一不可）

### ① 会进你的 NAS / 服务器敲命令（SSH）
SSH 就是"远程登录到 NAS 的命令行"。没听过没关系，跟着做：
- **群晖**：控制面板 → 终端机和 SNMP → 勾选「**启用 SSH 功能**」→ 保存。
- **你电脑（Windows）**：开始菜单搜「**终端**」（或 PowerShell）打开，敲：
  ```
  ssh 你的用户名@NAS的IP
  ```
  第一次问 `yes/no` 输 `yes`，再输 NAS 密码——**打字时屏幕没反应是正常的**，盲打完回车。
- NAS 的 IP 在哪看：群晖控制面板 → 网络 → 网络界面（一般是 `192.168.x.x`）。
- ⚠️ 这个地址**只有和 NAS 在同一 WiFi / 局域网才能用**；人在外面用 4G 连不上，得另外搞内网穿透（frp / 群晖自带）。

### ② 机器上要有 Docker
- **群晖**：出厂**没有** Docker！它叫 **Container Manager**：打开「**套件中心**」→ 搜 `Container Manager` → 安装。
- **VPS / Linux 小主机**：多半已装；没有就 `curl -fsSL https://get.docker.com | sh`。
- 验证：敲 `docker -v`，能出版本号就行。（群晖若提示找不到 `docker` 命令：`sudo ln -s /var/packages/ContainerManager/target/usr/bin/docker /usr/local/bin/docker`）

### ③ 一个跑起来的 Gotify（消息中转，桥从它读消息）
桥**不替代** Gotify，得先有它。在**和桥同一台机器**上用 Docker 起一个：
```bash
docker run -d --name gotify --restart unless-stopped -p 8081:80 -v "$PWD/gotify-data:/app/data" gotify/server
```
浏览器开 `http://NAS的IP:8081` → 第一次让你**建管理员账号** → 建好登进去。（`8081` 是例子端口，别和桥的 `8080` 冲突就行。）

### ④ 鸿蒙手机装 Hotify App（App 上架后这里放链接）

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

**问题 1：Gotify token**
- 打开 Gotify 网页（`http://NAS的IP:8081`，你第 0 步装的）。
- 左边栏点 **CLIENTS** → `+ Create Client` → 复制那串 Token，粘进来。
- ⚠️ 是 **CLIENTS**，**不是 APPLICATIONS**（APPLICATIONS 的 token 只能"发"消息；桥要的是能"读"消息的 CLIENT token）。
- token 就是让桥读你 Gotify 消息的"密码"。

**问题 2：Gotify 地址**
- ⚠️ **千万别填 `127.0.0.1` 或 `localhost`**——容器里它指的是容器自己，连不到 Gotify。这是 No.1 踩坑。
- Gotify 和桥在**同一台 NAS**：填 `http://host.docker.internal:8081`（脚本已自动开 host-gateway，能寻址宿主机）。
- Gotify 在**另一台机器**：填那台的局域网 IP，如 `http://192.168.1.50:8081`。
- 有**域名**：填 `https://gotify.你域名.com`。
- 完全不确定：先留空（App 上报时也会带过来），不影响先把容器起起来。

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

## 💻 不同平台怎么跑

上面默认按 Linux + SSH + Docker 走。别的平台：

| 平台 | 怎么办 |
|---|---|
| **Linux 服务器 / VPS** | 最顺：`curl -fsSL https://get.docker.com \| sh` 装 Docker → 跑 install.sh。 |
| **Windows** | 装 [Docker Desktop](https://www.docker.com/products/docker-desktop/)，用 **Git Bash** 或 **WSL** 跑 install.sh（PowerShell 跑不了 `.sh`）。**或更省事**：直接下 `gotify-bridge-windows-amd64.exe`（[Gitee Release](https://gitee.com/sakura-lolipop/hotify-bridge/releases)）双击跑，免 Docker。 |
| **群晖 NAS** | 套件中心装 **Container Manager** → 图形界面拉镜像跑（[docker.md 有步骤](./docker.md)）。 |
| **威联通 / Unraid** | Container Station / 应用中心，同理。 |

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

## 🔒 公网部署？用 acme.sh 申请免费 HTTPS 证书

`/register` 走公网**必须 HTTPS**（不然手机上报的 push token 明文裸奔）。用 [acme.sh](https://github.com/acmesh-official/acme.sh) 自动申请 Let's Encrypt 免费证书 + 自动续期：

```bash
# 1) 装 acme.sh
curl https://get.acme.sh | sh && source ~/.bashrc

# 2) 申请证书（standalone 模式，需要 80 端口空闲且指向本机）
acme.sh --issue -d push.你的域名.com --standalone

# 3) 装证书到桥的数据目录（续期后自动重启桥）
acme.sh --install-cert -d push.你的域名.com \
  --key-file       /opt/hotify-bridge/data/key.pem \
  --fullchain-file /opt/hotify-bridge/data/cert.pem \
  --reloadcmd      "docker restart hotify-bridge"
```

然后编辑 `bridge_config.yaml` 指过去（路径是**容器内**路径，证书落在挂载的 `/data` 卷里）：

```
tls_cert_file: /data/cert.pem
tls_key_file:  /data/key.pem
```

重启桥 `docker restart hotify-bridge`，日志看到 `[注册接口] 模式=HTTPS` 就成了；App 那边地址改 `https://push.你的域名.com:8080`。

> **80 端口被占 / 在 NAT 后面？** 用 acme.sh 的 **DNS 模式**（`--dns dns_dp` / `dns_ali` 等，去 DNS 后台填 token），不需要 80 端口、纯内网也能签。详见 [acme.sh dnsapi wiki](https://github.com/acmesh-official/acme.sh/wiki/dnsapi)。

---

## 🔧 想深入？

- 🐳 部署细节（NAS 图形界面、公网 HTTPS、多机、排错）→ [**docker.md**](./docker.md)
- 📚 全部配置项、运行原理、生产拓扑、Push Kit 深入 → [**README_FULL.md**](./README_FULL.md) ｜ [BRIDGE.md](./BRIDGE.md)
- 📄 版本变化 → [CHANGELOG.md](./CHANGELOG.md)

## 📄 许可证
MIT。原创代码，与 [Gotify](https://github.com/gotify/server)（MIT，© 其作者）互操作，不含其源码。
