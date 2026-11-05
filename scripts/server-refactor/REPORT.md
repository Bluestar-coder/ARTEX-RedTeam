# server 路由拆分 + handler 模板统一（重构第 2 条）

> 分支 `release/v0.4.3`。两个独立提交：路由/文件拆分、handler 模板统一。
> 不改业务逻辑、不改 HTTP 契约、不动 planner/worker/engine、不做前端改动。

## 一、路由清单核对

机器提取（`routeinv2.go.txt`，支持常量拼接与 loop 展开）覆盖**全部实际注册**：

- 实际路由 **270 条**（method + pattern 唯一）；
- 体检报告说的 **254** 是 `server.go` 里 `HandleFunc/Handle` 的文本数量（含 2 条根 mux 的 `/`、`/api/` 挂载）；
  口径差额来自：
  - `registerFindingTraffic` 动态拼接的 **7 条**（`GET/POST/PATCH/DELETE/PUT /api/exploration/findings/{id}/traffic…`）；
  - `registerSideRoutes` 循环展开的 **9 条** + 2 条直接注册；
  - 即 252（Handler 内字面 HandleFunc）+ 7 + 11 = 270。
- 完整清单见 `ROUTES.md`（270 行，含方法/路径/领域/处理函数/鉴权）。

领域分布：platform 91、tasks 80、system 28、findings 22、assets 19、side 18、traffic 7、auth 4、report 1。

## 二、拆文件（commit 1）

| 项 | 结果 |
|---|---|
| `server/server.go` | 3,983 → **1,263** 行（只保留构造/中间件/路由总装配 + 通用 helper） |
| `server/server_mgmt.go` | 2,252 → **565** 行（保留 settings/技能/上传等 helper，handler 已移出） |
| 新增文件 | `routes_{auth,system,tasks,side,assets,findings,traffic,platform,report}.go` |
| 搬移的注册 | 252 条 `mux.HandleFunc`（逐字未改）+ 2 个动态注册函数整体搬移 |
| 搬移的 handler | 111 个方法（server.go / server_mgmt.go → 领域文件，函数体逐字未改） |

`Handler()` 现在只做：`mux` 构造 → 9 个领域 `register*Routes(mux)` 调用 → CORS/JWT →
根 mux（`/api/` 与前端）挂载。注册顺序变化不影响 `ServeMux` 路由集合（无重复 pattern）。

**机器证据**：`routes-before.tsv` 与 `routes-after.tsv`（method/pattern/handler/auth）diff 为空
（`routes.diff` 0 字节）；可用 `./run.sh <before-ref> [after-ref]` 在任意两个 ref 上重跑。

## 三、handler 模板统一（commit 2）

新增 `server/handler_templates.go`：

- `writeOK(w, v)`：统一 200 成功返回（内部就是 `writeJSON(w, 200, v)`；非 200 的
  `201/202/204` 等仍用 `writeJSON`，状态码不变）；
- `decodeBodyErr / decodeBodyMsg / decodeBodyBadJSON`：`json.NewDecoder(r.Body).Decode`
  的请求体解析模板，错误码与错误文案由调用方原样传入（`err.Error()` / 固定文案 /
  `"bad json: "+err.Error()` 三种历史文案分别保留）；
- `decodeBytesErr / decodeBytesMsg / decodeBytesBadJSON`：`json.Unmarshal` 的 []byte 版本，
  **仍用 Unmarshal 语义**（不改成 Decoder，避免尾部数据行为变化）。

`s.pg(w)` 与 `writeErr`/`writeJSON` 本来就是单一实现（分别是 DB guard 与错误/成功写出），
本次不重复造轮子；模板文件头注释写清了这一点。

### 收敛统计（server 非测试代码）

| 项 | 改前 | 改后 | 说明 |
|---|---:|---:|---|
| `writeJSON(w, 200, …)` 调用点 | 262 | **0** | 全部改为 `writeOK`（合计 280 处，含 `http.StatusOK` 变体） |
| 内联 `json.NewDecoder(r.Body).Decode` 请求体解析 | 35 | 13（另有 3 处在 helper 内部） | 19 处三行样板改为模板调用 |
| `writeErr` 调用点 | 875 | 862 | 13 处错误分支由模板 helper 内部统一写出，文案/码不变 |
| `s.pg(w)` | 84 | 84 | 已是统一 DB guard（单实现），不再包一层 |
| 新增 helper 文件 | — | 81 行 | `server/handler_templates.go` |

### 保留未统一的特例（不硬改语义）

- `findings_groups.go`（MaxBytesError 分支）、`assets.go`（多形态 body / 严格解码）、
  `routes_system.go`（`io.EOF` 允许空 body）、`side_questions.go`（忽略解码错误的容错分支）、
  `server_mgmt.go:75` 的 `decode()` / `decodeStrict()`（既有 helper，语义不同）；
- 14 处「错误块里有额外处理/错误文案不是三种历史形态」的站点保持内联；
- 响应/内部 JSON 解析（非请求体，如 DB payload、工具参数、archive manifest）不在本次
  模板范围内，未改动。

## 四、验收

```text
go build ./...                 ✅
go vet ./...                   ✅
go test ./...                  ✅ 34 packages（真实 PostgreSQL）
go test ./... -shuffle=on      ✅（见提交/回复）
go test ./... -count=5         ✅（见提交/回复）
```

### API 契约快照

`contract_snapshot.py` 对 `routes-after.tsv` 的每条路由发送固定探针矩阵
（auth+空 body / 无鉴权 / 畸形 JSON），记录 status + content-type + body，
对 JWT/时间戳/UUID/临时目录做归一化后 diff：

```bash
python3 scripts/server-refactor/contract_snapshot.py <repo> <before-ref> <after-ref> /tmp/server-contract
```

实测结果（`7a92f9b`（拆分前）→ `aaad159`（模板统一后））：

```text
before: 610 probes
after: 610 probes
probes=610 differing=0
IDENTICAL
```

探针矩阵：270 条路由 ×（带 token + 空 body；无鉴权 + 空 body；写方法再加一次畸形 JSON），
共 610 次请求。跳过有网络/流式/文件副作用的端点（`/stream`、`/events`、`/api/update/*`、
`/api/llm/test`、`/api/mcp*`、`/api/logs*`、`*/download`、`*/upload`、`*/export`、selfupdate、gc），
清单在脚本 `SKIP_FRAGMENTS` 中。归一化项：JWT、RFC3339 时间戳、Unix 时间戳、UUID、临时目录。

### 手工冒烟（真实二进制 + 真实 PostgreSQL）

```text
GET  /api/health                        200 {"ok":true}
POST /api/auth/init                     200 token（随后 login 同样通过）
POST /api/tasks                         201 {"id":"1","exploration_id":1,...}
GET  /api/assets                        200 {"assets":[{"id":2,"type":"root_domain","domain":"example.com",...}],"count":...}
GET  /api/traffic                       200 {"enabled":false,"exchanges":[]}
GET  /api/exploration/findings/export?format=csv&scope=all
                                        200 text/csv 107 bytes
```

全程无 5xx、无未预期错误；`/api/traffic` 因冒烟实例关闭捕获代理返回 `enabled:false`（符合设计）。
