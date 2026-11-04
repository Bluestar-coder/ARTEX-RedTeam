<div align="center">

# ARTEX-RedTeam

**AI 自主红队作战系统**：LLM 多 agent 驱动，自主规划、执行并推进攻击链

Go 单体后端（内嵌 Next.js 前端）· PostgreSQL · 单二进制部署

</div>

> **本仓库是 ARTEX 的独立二次开发版本**（`Bluestar-coder/ARTEX-RedTeam`），现已更名为 **ARTEX-RedTeam**，更聚焦**红队方向**：以攻击链推进和权限获取为主线，而不是以漏洞数量为产出。原始项目仓库已被原作者删除，本仓库在此代码基础上继续维护与演进，协议仍为 AGPL-3.0。
>
> 本仓库不沿用上游的任何发布物：Docker 镜像、Release 压缩包、演示站都不再指向原来的项目。自 v0.4.1 起，Release 压缩包与 Docker 镜像（`bluestarcoder/artex-redteam`，amd64/arm64）均由本仓库 CI 构建发布。

---

## 项目概述

ARTEX-RedTeam 把一次红队行动组织成可观察、可复盘的自主闭环：

- **多 agent 分工**：`goals` 分解目标并提取测试范围，`planner` 是唯一的意图生成者，`worker` 领取意图后用真实工具执行，`mainagent` 负责人在环路。
- **双图架构**：全局共享的**资产图**（根域名、子域、IP、服务、应用、端点）保存资产真值；每任务独立的**探索图**（目标、意图、事实、漏洞、提示的血缘链）记录推进过程；两图通过锚点相连，支撑资产测试覆盖度统计。
- **事件驱动闭环**：探索图一变就唤醒 planner 派发新意图，worker 写回结果后再次触发下一轮，直到目标被 `prove_goal` 证明。
- **多步攻击链稳定推进**：planner 持有跨唤醒共享的 todolist，按前置依赖逐步派发，不重复、不错序；worker 还能检索其他 work 的执行过程，复用尚未沉淀为事实的观察。
- **全程留痕与管控**：MITM 记录代理为 Bash / HTTP 调用留痕并验证 CA，危险操作经审批门拦截；出口可走代理池，发现可推送到 IM 或汇总进报告。

---

## 红队定位

ARTEX-RedTeam 不是“扫一遍出漏洞清单”的扫描器，它围绕红队行动的三个核心问题组织能力：

| 红队问题 | 系统里的对应机制 |
| --- | --- |
| **打哪里** | 资产图（根域 → 子域 → 服务 → 端点）+ 覆盖度/覆盖图，回答“范围内还有什么没打” |
| **怎么打进去、走到哪一步** | 攻击链 todolist + 串行意图派发：发现入口 → 拿凭据 → 横向 → 提权，按前置依赖逐步推进 |
| **能做什么、不能做什么** | 任务级**操作约束**（allow/deny）作为交战规则（RoE），最高优先级注入 planner/worker 提示词 |

配套的 OPSEC 与协作能力：

- **出口管理**：代理池 + 本地转发网关，给 Bash / HTTP 调用统一出口并按需轮换；
- **人在环路**：危险工具调用走审批门（guard / intercept），执行中可 `steer` 纠偏或 `kill` 终止单个 work；
- **过程情报共享**：worker 可检索同任务其他 work 的执行过程，复用尚未写成 fact 的观察；
- **交付物**：漏洞 + 证据 + 会话链路 + 报告，支持 IM 推送与归档。

> 当前边界（规划中）：资产模型仍以网络 / Web 资产为主，**凭据、落地主机、会话 / 隧道、域内对象**等“访问状态”尚未作为一等资产建模；攻击阶段（侦察 → 初始访问 → 提权 → 横向 → 收集）也还没有独立的阶段字段。这两项是红队化改造的下一步。

---

## 截图预览

> 以下截图为本系统实际运行界面。

| 仪表盘（总览 / Token 消耗 / 活动流） | 任务列表 |
| :---: | :---: |
| ![仪表盘](screenshots/dashboard.png) | ![任务](screenshots/tasks.png) |

| 任务 · 执行过程（会话 / 工具调用） | 探索链路 |
| :---: | :---: |
| ![执行过程](screenshots/sessions.png) | ![探索链路](screenshots/graph.png) |

| 发现 | 资产 |
| :---: | :---: |
| ![发现](screenshots/findings.png) | ![资产](screenshots/assets.png) |

| 资产覆盖图（力导向布局 · 已测高亮 · 节点折叠展开） |
| :---: |
| ![资产覆盖图](screenshots/assets_test.png) |

| 流量录制 | 人在环路对话 |
| :---: | :---: |
| ![流量](screenshots/traffic.png) | ![对话](screenshots/chat.png) |

| Agent 管理 | LLM 配置 |
| :---: | :---: |
| ![Agent](screenshots/agents.png) | ![LLM](screenshots/llm.png) |

| 拦截审批 | 后端日志 |
| :---: | :---: |
| ![拦截](screenshots/intercept.png) | ![日志](screenshots/logs.png) |


---

## 审批记录详情

全局「审批记录」、任务内「拦截审批」及对话中的审批卡片均支持展开查看详情。展示结构参考
[AegisHook 的审批详情组件](https://github.com/RuoJi6/AegisHook/blob/main/web/src/components/CallDetail.vue)，沿用 ARTEX-RedTeam 的组件和主题：


## 资产同步（ScopeSentry）

支持从 [ScopeSentry](https://github.com/Autumn-27/ScopeSentry) 直接同步资产数据，免去重复收集：

- 在「**资产同步**」页填 ScopeSentry 的地址与 API Key，接入数据源；
- 按**项目**或**任务**维度选择要同步的目标与资产类型（域名 / 子域 / IP / 端口 / 站点 / 端点…）；
- 一键导入并按公司资产范围归并，直接进入 ARTEX-RedTeam 的资产图供 agent 探索使用。

---

## 安装

> 依赖数据库 **PostgreSQL**；探索需配置 **LLM**（`ANTHROPIC_API_KEY` 或 `OPENAI_API_KEY`，也可在 UI 里配）。

### 方式一：一键安装脚本（推荐）

```bash
git clone https://github.com/Bluestar-coder/ARTEX-RedTeam.git
cd ARTEX-RedTeam
./install.sh
```

脚本会：检测 / 自动安装 Docker → 让你选 **① 全部 Docker** 或 **② 本地编译运行**：

- **① 全部 Docker**：填一个 Postgres 密码（可回车随机）→ 自动写 `.env` → `docker compose up -d`。
- **② 本地运行**：选数据库（连已有 / 用 Docker 起一个）→ 生成 `config.json` → `go` 编译内嵌单二进制 → 启动。

装好后打开 **http://localhost:8787**（首次进入 `/setup` 设置管理员密码）。

### 方式二：Docker Compose（手动）

```bash
git clone https://github.com/Bluestar-coder/ARTEX-RedTeam.git
cd ARTEX-RedTeam
cp .env.example .env          # 填 POSTGRES_PASSWORD、可选 ANTHROPIC_API_KEY
docker compose up -d --build  # 本地构建 artex-redteam 镜像 + 启动 postgres
# → http://localhost:8787
```

镜像默认由本仓库 `Dockerfile` 本地构建；也可直接拉取 CI 发布的镜像（`bluestarcoder/artex-redteam`，amd64/arm64）：

```bash
docker compose pull artex-redteam && docker compose up -d
```

镜像已含常用工具（ripgrep/curl/vim/npm/nmap…）；`./skills` 与宿主 `./data`（容器内 `/app/data-redteam`）以绑定挂载持久化。

远程 MCP 可在系统设置中选择 `http`（Streamable HTTP）或 `sse`（旧版 SSE）。
旧版 SSE 服务通常使用 `GET /sse` 建立事件流，再通过服务返回的
`/message?sessionId=...` 接收 JSON-RPC 请求；配置时将 URL 填为 `/sse`，请求头按
`Authorization=Bearer <token>` 填写。

### 方式三：下载预编译二进制（Releases）

到 [Releases](https://github.com/Bluestar-coder/ARTEX-RedTeam/releases) 下载对应平台的 zip，解压后得到 `artex-redteam` + `start.sh`（Windows 为 `start.bat`）+ `skills/` + `config.example.json`：

> 需要其他平台或自定义构建时，可执行 `./build.sh --release` 在本地 `dist/` 生成压缩包（见方式五）。

```bash
cp config.example.json config.json   # 填好 database 连接
./start.sh                           # → http://localhost:8787
```

> 请用 `start.sh` / `start.bat` 启动，而不是直接跑 `./artex-redteam`。它是个守护脚本：程序退出后按退出码决定是否重新拉起，**页面上的[一键更新](#方式一页面一键更新推荐)靠它完成换装**。直接运行 `./artex-redteam` 时更新完就不会被拉起了。
> 后台常驻：`nohup ./start.sh >artex-redteam.log 2>&1 &`。

### 方式四：从源码编译单二进制

```bash
# 1) 前端静态导出
cd web && npm ci && npm run build:static && cd ..
# 2) 拷进内嵌目录
cp -r web/out server/webui/dist
# 3) 编译（-tags embedui 才内嵌前端）
CGO_ENABLED=0 go build -tags embedui -o artex-redteam ./cmd/artex-redteam
./start.sh
```

### 方式五：构建跨平台 Release 压缩包

`build.sh` 会先构建并嵌入前端，再使用 Go linker 去除调试信息，并将发布文件压缩为 zip。Release 模式默认生成 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64 的 zip 包：

```bash
./build.sh --release
# 产物：dist/artex-redteam-<version>-*.zip
```

UPX 自解压二进制可能与部分 Linux 内核、虚拟化环境或安全策略不兼容，因此默认不启用。可用 `ARTEX_REDTEAM_TARGETS` 自定义目标；确认目标运行环境兼容时，可显式传入 `--upx` 进一步缩小二进制：

```bash
ARTEX_REDTEAM_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
./build.sh --target linux/amd64 --upx
```

---

## 更新升级

> 升级只换程序、不动数据：Postgres 数据卷 `pgdata`、数据目录（jwt.key / SQLite / workspaces）与 `./skills` 都会保留。**数据库迁移无需手动执行**——`artex-redteam` 每次启动会幂等重跑 `schema.sql`（含 `ADD COLUMN` / `CREATE INDEX IF NOT EXISTS`），即“重启即迁移”。升级前仍建议先备份数据目录与数据库。

### 从 ARTEX 升级到 ARTEX-RedTeam

改名不改变数据与配置的语义，老部署可以直接升级，具体兼容点：

| 项目 | 行为 |
| --- | --- |
| 环境变量 | 新名 `ARTEX_REDTEAM_*` 优先；旧名 `ARTEX_*` 继续可用（如 `ARTEX_PG_DSN`、`ARTEX_LLM_*`） |
| 数据目录（本地运行） | 默认改为 `data-redteam`；启动时若只有旧的 `data/`，会自动改名迁移并打日志，失败则继续用旧目录 |
| 数据目录（Docker） | 宿主目录**仍是 `./data`**（不迁移、不丢流量索引），容器内挂载点改为 `/app/data-redteam` |
| 登录用户名 | 新的 `ARTEX-RedTeam` 与旧的 `ARTEX` 都接受；登录 cookie 从 `artex_token` 改为 `artex_redteam_token`，升级后需**重新登录一次** |
| 二进制名 | 新安装为 `artex-redteam`；`start.sh` / `start.bat` 会优先启动 `artex-redteam`，找不到再回退 `artex`，老目录无需改名 |
| 发布包 | 新版本资产名为 `artex-redteam-<version>-<os>-<arch>.zip`。**旧版（≤0.4.x）的一键更新不认识新资产名**，从老版本升级请手动下载一次 zip 覆盖二进制 |
| 管理端口 | 默认监听从 `:8787` 改为 `127.0.0.1:8787`，compose 端口映射也改为 `127.0.0.1:8787:8787`；远程访问请显式传 `-addr :8787` 并改回端口映射，或加 VPN / 带认证的反向代理 |
| Docker 项目 | compose 项目名取自目录名，`pgdata` 具名卷按项目名隔离；**请沿用原目录升级**，把仓库 clone/改名到新目录会得到一个新的空数据库卷 |

### 方式一：页面一键更新（推荐）

一键更新从本仓库的 GitHub Releases 获取最新正式版（`v0.4.1` 起可用），按当前平台选包、校验 `SHA256SUMS` 后换装。

在 **系统配置** 页（侧边栏「系统配置」→ `/system/settings`）的**版本与更新**卡片里，可以直接检查并安装新版本，无需登录服务器。

点「更新」后：下载当前平台的发布包 → 比对 Release 的 `SHA256SUMS` → 用 `-h` 冒烟测试新二进制 → 暂存为 `artex-redteam.new` → 程序退出，由 `start.sh` / `start.bat` 重新拉起并完成换装。页面会自动等到新版本上线后刷新。

- **失败不会留下坏程序**：校验或冒烟不通过就丢弃暂存件、继续跑当前版本；换装后的新版若连续 3 次启动失败，会自动回滚到 `artex-redteam.old`（失败的那个留作 `artex-redteam.failed` 供排查）。
- **随时可回退**：上一版本保留为 `artex-redteam.old`，卡片上有「回滚到上一版本」。注意数据库结构不会回退。
- **更新会中断正在运行的任务**——更新即重启，请在空闲时进行。
- **开发构建不给更新**：版本号是 `dev` 或 `git describe` 带后缀时禁用，避免正式版覆盖掉本地调试的二进制。
- **Docker 下只换程序、不换镜像**：镜像里的 playwright / nmap 等工具链不会跟着升级，且 `docker compose up -d` 重建容器后会退回镜像自带的版本。要连镜像一起升级请用 `docker compose up -d --build artex-redteam`（本地构建镜像）。
- 访问 GitHub 需要代理时，在同一页面配置**全局代理**即可，更新链路会走它。更新只从 GitHub 域名下载并强制 HTTPS。

### 方式二：一键更新脚本

```bash
cd ARTEX-RedTeam
./update.sh
```

脚本先可选 `git pull` 拉取最新代码，再让你选 **① Docker 更新** 或 **② 本地编译更新**（与 `install.sh` 对应）：

- **① Docker**：重新构建 artex-redteam 镜像（`docker compose build`）→ `docker compose up -d`（重启即自动迁移 schema）。
- **② 本地**：重建前端静态产物 → 重新编译 `./artex-redteam`（完成后重启进程生效）。

### 方式三：Docker Compose（手动）

```bash
cd ARTEX-RedTeam
git pull                                  # 更新代码
docker compose up -d --build artex-redteam        # 重新构建镜像并重启 → 自动迁移 schema
docker image prune -f                     # 清理旧镜像（可选）
```

### 方式四：预编译二进制（Releases）

到 [Releases](https://github.com/Bluestar-coder/ARTEX-RedTeam/releases) 下载新版本 zip，停掉旧进程后覆盖 `artex-redteam` 与 `skills/`（保留你的 `config.json` 与数据目录），重启即可：

```bash
cp -r <解压目录>/skills ./ && cp <解压目录>/artex-redteam ./
./start.sh
```

### 方式五：从源码编译

```bash
git pull
cd web && npm ci && npm run build:static && cd ..
cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -tags embedui -o artex-redteam ./cmd/artex-redteam
# 重启 ./start.sh
```

---

## 配置

**数据库**（`config.json`，或用环境变量 `ARTEX_REDTEAM_PG_DSN` 覆盖）：

```json
{
  "database": {
    "host": "127.0.0.1", "port": 5432,
    "user": "artex", "password": "yourpass",
    "dbname": "artex", "sslmode": "disable"
  }
}
```

**LLM**：`export ANTHROPIC_API_KEY=sk-...`（或 `OPENAI_API_KEY`），也可在 UI 的「LLM 配置」页填写。
可选：`ARTEX_REDTEAM_LLM_PROVIDER` / `ARTEX_REDTEAM_LLM_MODEL` / `ARTEX_REDTEAM_LLM_BASE_URL` / `ARTEX_REDTEAM_LLM_PROXY`。

> 环境变量统一使用 `ARTEX_REDTEAM_*` 前缀；改名前的 `ARTEX_*` 仍可继续使用（新名优先）——老 `.env` / `docker-compose` 配置无需修改。

**并发**：每个任务的 work agent 数在「系统设置」里配置（默认 3）。

**常用参数**：`./start.sh -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788`（`-addr` 前端+API，`-proxy` 流量录制代理）。启动脚本会把参数原样透传给 `artex-redteam`。

> **监听地址默认是回环 `127.0.0.1:8787`，Docker Compose 的端口映射同样只绑回环。** 管理面等价于执行权限（能配 MCP、能造 bash 工具），所以默认不对外暴露。需要从其它机器访问时：裸二进制显式传 `-addr :8787`，Docker 把 compose 的 `ports` 改回 `8787:8787`；无论哪种，前面都应有 VPN、SSH 隧道或带认证的反向代理。

### 反向代理部署（HTTPS / 只开放 443）

前端和 API/SSE 都由同一个后端端口（默认 `127.0.0.1:8787`）提供，实时活动流默认走**同源**地址，因此**无需配置 `NEXT_PUBLIC_SSE_BASE`**，公网只开放 443、把 8787 留在内网即可。

SSE 是长连接 + 持续推送，反代**必须关闭缓冲**，否则浏览器能连上却收不到事件（表现为活动流一直转圈）。Nginx 示例：

```nginx
server {
    listen 443 ssl;
    server_name your.domain.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE 关键项：关缓冲、长超时、HTTP/1.1
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
    }
}
```

> 仅当 SSE 需要走与页面不同的来源（如独立子域）时，才在**构建期**设置 `NEXT_PUBLIC_SSE_BASE`（该变量在 `next build` 时固化进静态包，容器运行时再设无效）。

---



## 开发

### 手动漏洞复测

任务详情的「复测」页签可分页选择本任务的漏洞、查看历次结论和证据，并手动发起复测。启动后保留当前页签，显示转圈图标和「复测中」；确认修复后同步更新漏洞状态。

在漏洞列表每行操作区点击「复测」，或在漏洞详情的「漏洞复测」区域点击「发起复测」，填写可选的修复版本、测试条件或限制，系统会创建独立的复测 Agent 会话，启动后保留当前页面。列表的平铺、按任务分组和资产视图均支持该入口；复测运行时显示转圈图标和「复测中」，需要查看时点击进入对应会话，结束后恢复「复测」。复测无需重新启动原扫描任务，结论分为「仍可复现」「已修复」「无法确认」，每次的结论、证据和会话链接保存在漏洞详情中。

新版后端首次启动会预置可编辑的「漏洞复测」（`retester`）Agent，可在 Agent 管理中配置提示词、LLM、运行预算和工具。默认使用其绑定的 LLM，未绑定则使用全局激活配置。复测会话成功完成且结论为「已修复」时，系统自动将漏洞处置状态改为「已修复」；执行中、失败、停止或其他结论保留原状态。原始证据和报告始终保留。也可在状态下拉菜单中手动选择「已修复」。同一漏洞正在复测时复用已有会话，停止、失败或服务重启后可重新发起。

本版历史记录通过漏洞详情和会话查看，暂未纳入漏洞报告导出或任务归档包，也未自动关联流量包。演示模式只生成明确标注的模拟记录，不请求真实目标。

### 本地运行与测试

```bash
./dev.sh    # 后端(:8787) + 流量代理(:8788) + 前端 next dev(:5173) → http://localhost:5173
```

- 后端：`go run ./cmd/artex-redteam`（不带 `-tags embedui` 则不内嵌前端）
- 前端：`cd web && npm run dev`（`/api` 反代到后端，带热更新）
- 测试：`go test ./...`
- Mock 预览（无后端）：`cd web && NEXT_PUBLIC_MOCK=1 npm run dev`

### 数据库测试（按包 schema 隔离 + 套件锁）

带数据库的测试需要 `ARTEX_REDTEAM_PG_DSN`（或旧名 `ARTEX_PG_DSN`、`config.json`）；未配置时这些用例自行 skip，其余测试照常：

```bash
export ARTEX_REDTEAM_PG_DSN='postgres://artex:pass@127.0.0.1:5432/artex?sslmode=disable'
go test ./...
go test ./... -count=20 -timeout 45m   # 各 DB 包串行（套件锁），20 轮约 40 分钟
go test ./... -shuffle=on
```

**新增数据库测试必须走统一入口，不要裸调 `db.Open`：**

```go
func TestMain(m *testing.M) { os.Exit(testdb.Lock(m)) }   // 每个包一次
...
d := testdb.Open(t, db.Open)                              // 每个用例
```

- `testdb.Lock` 在 `TestMain` 里做两件事：为本次测试进程创建专属 schema（`test_<包名>_<pid>`）并把连接的 `search_path` 固定过去（`public` 兜底）；同时获取全局套件锁（PostgreSQL advisory lock `7337741002`）。整个包运行期间有效，结束时终止遗留会话并 `DROP SCHEMA ... CASCADE`；
- `testdb.Open` 只在「锁已持有」时调用 `db.Open`（触发幂等迁移），并把 `Close` 注册到 `t.Cleanup`；没先 `Lock` 会直接 fail，顺序写不反；
- 无 DSN 时 `Lock` 照常跑、`Open` 跳过，行为与改造前一致。

**为什么必须「先锁后 open」**：`db.Open` 每次都会重跑 `schema.sql`，其中包含 `ALTER TABLE` / `CREATE INDEX` 等 ACCESS EXCLUSIVE 的 DDL。历史上所有包共用一个库时，锁外执行的迁移会与另一个包正在提交的 `INSERT INTO assets(...)` 互相等待，PostgreSQL 判定死锁（`40P01`）——2026-11 实测 `llmrec` 没进套件锁、`evidence` 先 open 后拿锁，`go test ./...` 约每 2 次红 1 次，失败点随机落到 `db`/`agent`。现在按包 schema 隔离让每个包的迁移只碰自己的 schema，跨包冲突在物理上已经不可能；套件锁仍然保留，作为第二层保险并继续串行化迁移。

> 隔离通过覆写 `ARTEX_REDTEAM_PG_DSN` / `ARTEX_PG_DSN` 实现：`Manager` 等按 env DSN 自开连接的生产代码也会自动落在本包 schema，不需要改生产路径。

---

## 安全加固

本项目本身是攻击工具，管理面一旦被拿走就等于把 root 交出去。针对公开披露的 ARTEX 利用链（未认证接管 → MCP stdio 命令执行），默认行为已按下面的方式收紧：

| 风险点 | 当前行为 |
| --- | --- |
| 初始化接口 fail-open | `/api/auth/init` 读数据库出错一律 503，绝不把"读不到"当成"没设置"；写入用 `INSERT ... ON CONFLICT DO NOTHING`，已有密码时无法被覆盖 |
| 免认证路径过宽 | 只有 `status` / `init` / `login` / `health` 四个**精确路径**免认证，不再用 `/api/auth/` 前缀放行 |
| 暴力尝试 / 并发打满 | 三个会改状态的认证接口按来源 IP 限流（默认 12 次/分钟、突发 10），可用 `ARTEX_REDTEAM_AUTH_RATE_PER_MIN` 调整 |
| 改密后旧 token 仍有效 | JWT 携带密码版本（`pv`），改密后所有旧 token **立即失效**，当前会话自动换发新 token |
| 版本号指纹 | 未认证的 `/api/health` 只返回 `{"ok":true}`，认证后才带 `service` / `version` |
| 管理口默认暴露 | 默认只监听 `127.0.0.1:8787`；绑定非回环地址时启动日志会告警 |
| MCP stdio 变 webshell | 拒绝把 `sh`/`bash`/`powershell` 等 shell 作为 stdio 启动命令，也拒绝 `python -c` 这类内联代码参数；确需时设置 `ARTEX_REDTEAM_ALLOW_STDIO_SHELL=1` |
| 签名密钥可被文件管理器读到 | `jwt.key` 存放在程序目录而不是可浏览的 data 工作区，老位置会一次性迁移并删除 |

> 这些改动只能收缩"平台自身被打"的面。另有一条设计级事实必须清楚：**有管理员会话 = 有命令执行**（worker 需要跑 Bash，MCP stdio 与自定义工具也都能起进程）。所以对外部署务必：跑在低权限用户下、前面加认证反代或 VPN、定期审计 `mcp_servers` 与自定义 `tools`。

**供应链提醒。** 2026-11 的公开事件中，第三方 fork `trhhubsd/ARTEX` 推荐的 `tomicdd/artex` 镜像被植入未认证 WebShell：登录接口多读一个 `Captcha` 字段，值以 `!` 开头就把剩余内容交给 `sh -c` 执行并回显。本仓库的源码与 CI 构建的镜像不含该逻辑——登录/初始化/改密接口只接受固定字段，多余字段直接 400——但这类事件值得记住：只从本仓库 CI（`bluestarcoder/artex-redteam`）或自己的本地 `docker compose build` 取镜像，并核对 digest；不要使用来源不明的预构建镜像。

---

## 系统技术架构

ARTEX-RedTeam 是一套 **LLM 多 agent 驱动的自主红队作战系统**：Go 单体后端（内嵌 Next.js 前端）+ PostgreSQL，agent 能力由 `norma` SDK 提供（`agentcore` / `tool` / `permission` / `harness` / `memory` / `transcript`）。`norma` 源码已并入本仓库的 [`norma/`](norma) 目录（Apache-2.0），构建不依赖任何上游仓库。核心是**双图架构**，以及围绕它的两条自主性机制：**worker 间过程级信息交换**与 **planner 多轮共享 todolist 稳定攻击链路**。

### 总体分层

```mermaid
flowchart TB
  subgraph FE["前端 Next.js（go:embed 内嵌单二进制）"]
    UI["仪表盘 · 任务 · 资产 · 覆盖图 · 流量 · 工作空间 · 系统配置"]
  end
  subgraph SRV["server（Go net/http）"]
    API["REST /api/*　JWT 鉴权　SSE"]
    ENG["engine 调度循环"]
    MGR["Manager　任务/引擎/store 生命周期"]
  end
  subgraph AG["agent（norma SDK）"]
    GO["goals　目标分解 + 提取范围"]
    PL["planner　规划者（唯一意图生成者）"]
    WK["worker　执行者 ×N"]
    MA["mainagent　人在环路"]
  end
  subgraph DB["PostgreSQL"]
    AGRAPH["资产图　assets / companies / task_scope"]
    EGRAPH["探索图　exploration_nodes / anchors / activity"]
  end
  subgraph SUB["支撑子系统"]
    PROXY["流量记录代理　MITM + CA 留痕"]
    GUARD["guard / intercept　工具审批门"]
    ENR["enrich　DNS / HTTP 异步补全"]
    EXT["MCP · skills · memory · report"]
  end

  UI -->|HTTP| API
  API --> MGR --> ENG
  ENG --> PL
  ENG --> WK
  API --> MA
  API --> GO
  PL --> DB
  WK --> DB
  MA --> DB
  GO --> DB
  WK -->|"Bash / HTTP 全程留痕"| PROXY
  WK --> GUARD
  WK --> ENR
  PL -.-> EXT
  WK -.-> EXT
  MA -.-> EXT
```

| 层 | 职责 |
| --- | --- |
| **前端** | Next.js 静态导出，`go:embed` 内嵌进单二进制；可视化任务/资产/探索链路/覆盖图，人在环路对话 |
| **server** | `net/http` 路由 + JWT 鉴权 + SSE；`Manager` 托管任务、引擎、DB store 的生命周期 |
| **engine** | 每任务一个 `plannerLoop` + N 个 worker goroutine；意图领取、超时/暂停/drain |
| **agent** | goals / planner / worker / mainagent，`ToolSet` 把双图暴露成 LLM 工具 |
| **db** | 双图的 Postgres 落地（pgx）；schema 随 `go:embed` 每次启动幂等建表 |
| **支撑** | 记录型 MITM 代理、审批门、异步补全、MCP/技能/记忆/报告 |

### 双图架构：探索图 + 资产图

系统把「**目标是什么**」和「**测到了什么程度**」拆成两张相互独立、又通过锚点相连的图：

- **资产图（Asset Graph，全局共享）**：跨任务同一份的资产真值库。节点为 `root_domain / subdomain / ip / service / app / endpoint`，归属公司；域名→子域→服务→端点的父子关系与去重 key 全部由程序计算，agent 只提交原始信息。
- **探索图（Exploration Graph，每任务独立）**：一次任务的“思考与推进”过程。节点为 `goal（目标）/ intent（意图）/ fact（事实）/ finding（漏洞）/ hint（提示）`，靠 `spawns / derived_from / yields / proves` 等边连成**血缘链**，回答“哪个方向派生自哪些事实、产出了什么”。
- **两图靠锚点相连**：`exploration_anchors(node_id, asset_id)` 把意图/事实/漏洞锚定到具体资产上——于是既能从“探索方向”看它打的是哪些资产，也能从“某个资产”反查它在本任务被哪些意图测过、得出过哪些事实。这也支撑了**资产测试覆盖度**与**资产覆盖图**（范围内资产 + 已测高亮）。

```mermaid
flowchart LR
  subgraph EG["探索图（每任务独立 · 推进链）"]
    direction TB
    G["goal 目标"]
    I1["intent 意图 A"]
    F1["fact 事实"]
    I2["intent 意图 B"]
    FD["finding 漏洞"]
    G -->|spawns| I1
    I1 -->|yields| F1
    F1 -->|derived_from| I2
    I2 -->|proves| FD
  end
  subgraph AG["资产图（全局共享 · 真值库）"]
    direction TB
    RD["root_domain"]
    SD["subdomain"]
    SV["service"]
    EP["endpoint"]
    RD --> SD --> SV --> EP
  end
  I1 -. anchor .-> SD
  F1 -. anchor .-> SV
  I2 -. anchor .-> EP
  FD -. anchor .-> EP
```

> 分工：**planner** 读探索图态势、判目标、只在有未覆盖的新方向时派**意图**进 frontier；**worker** 领**一条意图**、用真实工具执行、把新资产/事实/漏洞写回两图后即停。资产图是共享事实，探索图是每任务的推进链。

### 引擎与意图生命周期（一次探索的闭环）

引擎是**事件驱动**的闭环：图一变就唤醒 planner，planner 派意图，worker 领意图执行并写回，写回又触发下一轮——直到目标被证明（`prove_goal`）。

```mermaid
sequenceDiagram
  autonumber
  participant EV as 图变更 debounce
  participant P as planner
  participant FR as frontier 意图队列
  participant W as worker
  participant PX as 记录代理
  participant DB as 双图 + activity

  EV-->>P: 唤醒
  P->>DB: 读态势(graph_overview 预取 + coverage)
  P->>FR: 派 0..N 个意图(带 asset_ids)
  Note over P,FR: 大多数唤醒派 0 个——无新方向即结束
  W->>FR: claimNext 领一条意图
  W->>DB: 取意图 asset_ids 的原始资产作为初始信息
  W->>PX: 真实工具执行(Kali / Bash / HTTP)
  PX-->>W: 响应(全程留痕 + CA 验证)
  W->>DB: 写回 fact / asset / finding + 每步 activity
  DB-->>EV: 图变更
  EV-->>P: 再次唤醒(闭环)
```

### worker 间的过程级信息交换

一次深入的探索里，很多有价值的观察（某个报错、某段响应、某个隐藏参数）出现在一个 worker 的**执行过程**中，却未必被写成正式 fact。为避免重复劳动、让链路上的 worker 能站在彼此的肩膀上，worker 具备**跨 work 检索过程**的能力：

- `search_all_worker_traces(q)`：在**本任务其他 work 的执行过程**里按关键字检索（自动排除自己这条意图的步骤），命中项带 `intent_id`；
- `list_worker_traces` / `get_worker_trace(intent_id, step_ids=[…])`：先看有哪些 work 跑过，再取某个 work 具体几步的完整内容做细节交换。

这样即便探索图上还没有对应的 fact，后续 worker 也能复用他人过程中的观察——**信息在 worker 之间以“执行过程”为粒度流动**，而边界不变（每个 worker 仍只做自己领到的那条意图）。

```mermaid
flowchart LR
  WA["worker A（意图 #12）"] -->|"每步 activity"| ACT[("探索图 · activity 过程库")]
  WB["worker B（意图 #34）"] -->|"每步 activity"| ACT
  WC["worker C（意图 #56）"] ==>|"1) search_all_worker_traces(q)"| ACT
  ACT ==>|"2) 命中 A/B 的步骤（排除自己）"| WC
  WC ==>|"3) get_worker_trace(id, step_ids)"| ACT
  ACT ==>|"4) 返回完整过程内容"| WC
```

### planner 多轮共享 todolist → 稳定的攻击链路

真实攻击链往往是**有前后依赖的多步序列**（如：发现注入点 → 拿到凭据 → 横向 → 提权），一次性把这些并行派下去只会乱套。planner 因此持有一份**按任务保留、跨唤醒共享的规划待办（todolist）**：

- planner 是事件驱动的——图一变就被唤醒，但**每次唤醒是全新会话**；共享的 todolist 让它把一条串行利用链**记录一次**、然后在后续多轮里**按依赖逐步派意图**，而不是把整条链在一轮里全部前置展开；
- 每轮只对「前置步骤已完成、其依赖的 fact 已存在」的下一步派意图，并随进展更新清单（把已被 fact 满足的步骤标完成）。

```mermaid
flowchart TB
  subgraph TODO["共享 todolist（按任务保留 · 跨唤醒常驻）"]
    direction LR
    T1["1 注入点　[已完成]"]
    T2["2 取凭据　[进行中]"]
    T3["3 横向　[待前置]"]
    T4["4 提权　[待前置]"]
    T1 -.前置满足.-> T2 -.-> T3 -.-> T4
  end
  R1["第 1 轮唤醒　派意图①"] --> T1
  R2["第 2 轮（①产出 fact）　派意图②"] --> T2
  R3["第 3 轮（②产出 fact）　派意图③"] --> T3
```

于是攻击链在“事件驱动 + 无状态会话”的环境下依然**稳定推进、不重复、不错序**——这是 ARTEX-RedTeam 能自主走完多步利用链的关键。

---

## 参考

https://github.com/oritera/Cairn


## 许可与免责声明

### 开源协议

本项目采用 **GNU Affero General Public License v3.0（AGPL-3.0）** 授权，完整条款见仓库根目录的 [LICENSE](LICENSE) 文件。

这意味着任何人都可以自由使用、修改和分发本项目，但**衍生作品必须同样以 AGPL-3.0 开源**；特别地，**若你修改本项目并通过网络（如部署为在线服务）向用户提供，也必须向这些用户公开对应的完整源码**。

> ⚠️ **重要提示**：开源协议本身不限制软件的使用用途。以下的「使用限制」与「免责声明」是作者对使用者的额外约定与郑重声明，请务必遵守。

**ARTEX-RedTeam 仅供个人学习、代码研究与本地技术验证使用，不得用于对任何线上系统或网站发起实际测试。**

### 允许使用范围

- 仅可用于**阅读、学习与研究本项目源码**，以及在**本地隔离环境**中进行技术原理验证；
- 适用于个人学习、学术研究、代码审阅等非攻击性用途。

### 禁止事项

- **严禁使用本工具对任何网站、线上服务或联网系统发起扫描、探测、利用或攻击**（无论是否获得授权、是否为自有资产）；
- 严禁将本工具用于任何实际的渗透测试、攻防对抗或生产环境；
- 严禁将本工具用于非法入侵、数据窃取、勒索、拒绝服务或任何破坏性、犯罪性活动；
- 严禁利用本工具从事违反所在国家/地区法律法规的行为。

### 合规责任

使用者须自行遵守所在国家/地区关于网络安全、数据保护与计算机犯罪的全部法律法规（在中国大陆包括但不限于《网络安全法》《数据安全法》《个人信息保护法》及相关司法解释）。**因使用本工具产生的一切法律责任与后果，均由使用者自行承担。**

### 免责声明

本项目按“现状（AS IS）”提供，不附带任何明示或默示的担保。作者及贡献者不对使用本工具（无论使用方式是否得当）所导致的任何直接或间接损失、数据丢失、系统损坏或法律纠纷承担责任。**下载、安装或使用本项目，即表示你已阅读、理解并同意上述全部条款。**
