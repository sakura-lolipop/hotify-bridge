# envbug.md — `.env` 部署体验 bug

> 不是代码 bug，是**部署体验 bug**：小白反馈"不知道 `.env` 是什么、不会部署"。深挖根因——不是文档没写清，是 `.env` 这套机制在 Docker GUI 入口**根本不生效**。记此待批改。
> 状态：🐛 已定位 + 修法已定（待用户点头执行）。

---

## 🐛 小白卡在 `.env` 不会部署

- **来源**：用户反馈"别人问 `.env` 是什么、不知道如何部署、env 代表什么"（2026-07-28）。用户自陈"没用过 Docker"，追问"dockerui 里怎么配置"。
- **症状**：拿到 `docker-compose.yml` + `.env.example` 的小白，卡在"`.env` 是啥、建在哪、怎么填"，容器起不来 / 起来但值没进去。

## 根因（实锤，web 搜索核实——非印象）

**`.env` 自动读取是命令行 `docker compose` 的专属行为；Docker GUI 基本不吃 `.env` 文件。**

| 入口 | 吃 `.env`？ | 实际行为 |
|---|---|---|
| **命令行** `docker compose up -d` | ✅ 自动读同目录 `.env` | `.env` 约定唯一原生场景 |
| **群晖 Container Manager「项目」** | ❌ 不自动加载旁边的 `.env` | 通病：要么 SSH `--env-file`，要么摸到项目根目录 + **重建项目**（光"重启"还用旧缓存值） |
| **Portainer「Stack」** | ❌ 另一套 `stack.env` | 跟 Docker Compose `.env` 不是一回事；且 `stack.env` **不支持 `${VAR}` 替换** |
| **Docker Desktop** | ⚠️ 文件式 | 主要还是编辑文件 |

**共同点**：所有入口都接受 `environment:` 块里**直填值**的 compose——粘进去就跑。

→ README 里写的"Container Manager 图形界面粘 compose + 填 env"（`README.md:50`）对 `.env` 这条**实际不成立**。小白（多走 GUI）既建不出点开头的 `.env`、GUI 也不读它。

## 叠加的小白坑（就算走命令行也踩）

1. **`.env` 是点开头"没名字"的怪文件**——Windows 资源管理器不让建（逼填名字）→ 建出 `.env.txt` / `env.txt` / `env`，compose 读不到、**静默不报错** → 以为填了没生效。
2. **两文件跳转、线是隐形的**——打开 `docker-compose.yml` 想填 token，看见 `${GOTIFY_CLIENT_TOKEN:-}`（`docker-compose.yml:18-19`），不知道是叫去**另一个文件** `.env` 填。
3. **Mac/Linux 点开头 = 隐藏文件**，建完在文件夹里"消失"，以为没建成又建一遍。

## 结论

`.env` 是为**团队/CI「机密不入库」**设计的抽象层——单用户自托管 GUI 小白场景**根本用不上，纯加层理解成本**（YAGNI，同 [[over-engineering-tendency]]）。而且 `bridge_config.yaml` 里本来就有 token，再 `.env` 藏一遍没多一分安全。

## 修法（待批执行）：砍 `.env`，值直填 `docker-compose.yml`

```yaml
environment:
  GOTIFY_HTTP_URL: https://gotify.你域名.com   # ← 改成你的 Gotify 地址
  GOTIFY_CLIENT_TOKEN: 改成你的token           # ← Gotify WebUI → CLIENTS 里的 token
```

- **唯一在命令行 + 群晖 + Portainer + Docker Desktop 都一致生效的写法**——小白把 compose 正文往任一 GUI 一粘，值已在里面，没有 `.env` / `${}` / "换 GUI 又一套规矩"。
- **桥代码不动**：`config.go:255-264` 还是 `os.Getenv("GOTIFY_HTTP_URL")` / `os.Getenv("GOTIFY_CLIENT_TOKEN")` 读这俩名；compose `environment:` 无论值来自 `.env` 插值还是直填，**到容器里是同一个环境变量**。
- **SSH-light 目标不破**：还是不用 SSH 进去编 `bridge_config.yaml`，改的是 compose 文件本身（GUI 里粘/改都行）。

## 改动清单（待批）

1. `docker-compose.yml`：`environment:` 块从 `${VAR:-}` 改成直填占位中文 + 行内注释；顶部注释同步。
2. `README.md`：compose 节（`:48-56`）去掉"复制 `.env.example` 成 `.env`"那步，改成"编辑 `docker-compose.yml` 这两行"。
3. `README_FULL.md` / `docker.md`：同步去掉 `.env` 措辞。
4. 删 `.env.example`。
5. （可选）`.gitignore` 里的 `.env` 行留着无害，不动。

## Sources

- [SynoForum — Container Manager 不像 CLI 那样支持 .env](https://www.synoforum.com/threads/what-is-the-different-between-run-docker-compose-in-ssh-or-using-container-manager-project.12012/)
- [Synology Community — 改 env 要重建项目，重启用旧值](https://community.synology.com/enu/forum/1/post/191869)
- [Portainer 官方文档 — .env vs stack.env 是两套](https://docs.portainer.io/faqs/troubleshooting/stacks-deployments-and-updates/environment-variable-management-in-docker-.env-vs.-stack.env)
- [Docker 官方 — environment 直接写 / env_file 加载](https://docs.docker.com/compose/how-tos/environment-variables/set-environment-variables/)

## 教训

- 又一例"开发机（命令行 compose）能跑 ≠ 用户（GUI）能跑"——同 [[hotify-distribution-china-gfw]] 的 dev≠用户、同 bug.md #2 的 dev fetch≠用户 fetch。**部署入口的差异（CLI vs GUI）和网络的差异（GFW）一样，都是"自己测通不代表用户通"的盲区。**
- 写部署文档前，得先确认目标用户**实际走哪个入口**，按那个入口的真实行为写（GUI 不吃 `.env` 就别教 `.env`）。用户自陈"没用过 Docker"= 这类盲区的高发信号。
