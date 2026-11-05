# f53a6fd 工具重构验收补充

> 范围：`release/v0.4.3` 分支，重构提交 `f53a6fd`（父提交 `f997b0a`）。
> 本文只补验收证据，**不改生产代码**；新增文件只有 `scripts/tool-contract/`。

## 摘要

| 问题 | 结论 |
|---|---|
| 一、前后工具契约是否一致 | **空 diff**：两份序列化快照 sha256 均为 `ce1d122b35d700f2c6397857f27fb867394193c0bdbf7c5b85420e02f9461568`，`diff -u` 无输出；共 73 条（agent 27 / SDK 20 / server 23 / traffic 3） |
| 二、清单第 20 号是否缺失 | **没有缺失，总数 53，编号 1–53 连续；第 20 号 = `expand_digest`**（`agent/tools_digest.go:122`） |
| 三、`list_worker_traces` 归属 | **选项 3：有意如此**——它是跨任务工具 `list_task_worker_traces` 的 per-task 实现，只在 `delegateToTask` 内注入，不 seed 给任何 agent |

---

## 一、schema 前后机器对照

### 方法

`scripts/tool-contract/run.sh` 用 `git worktree` 分别检出 `f53a6fd^` 与 `f53a6fd`，向每个 worktree 临时放入 dumper（跑完即删，不进入提交），序列化所有 LLM 可见工具的完整契约后做 `diff`：

- **agent 家族（27）**：`MainAgentTools + PlannerTools + WorkerTools + 仅由 accessor 暴露的 list_worker_traces`，按名字去重；
- **SDK 家族（20）**：`DefaultTools(9) + ShellSessionTools(5) + WebFetch + TodoWrite + TaskOutput/TaskStop/TaskList/Monitor`，按 agentcore 的组装方式构造；
- **server 家族（23）**：`orchestrationTools + platformTools + findingRetestTools`；
- **traffic 家族（3）**：`(&traffic.Traffic{}).Tools()`。

每条工具记录以下字段（**全部显式序列化，不用 `omitempty`**，零值也保留）：

```json
{
  "family": "agent",
  "name": "...",
  "description": "...",          // 完整描述文本
  "prompt": "...",               // 工具附加上下文提示
  "input_schema": { ... },       // 完整 JSON Schema（字段/类型/required/enum/default/嵌套）
  "read_only_at_empty_input": true,
  "concurrent_safe_at_empty_input": true,
  "permission_behavior": "allow",// allow / ask
  "permission_message": "...",   // Ask 的确认文案
  "raw_input": false,
  "result_shape": "norma/tool.Result{Content []llm.ContentBlock; IsError bool; Extra []llm.Message}"
}
```

关于「返回结构类型定义」：当前 SDK 的 `actool.Spec` **没有 per-tool 输出 schema**（工具返回的是运行时 `actool.Result`），所以快照把该返回结构作为常量字段记录，前后同样一致；不存在被忽略的逐工具返回声明。

稳定性：对象键由 `encoding/json` 排序，数组保持原顺序（required/enum/参数顺序敏感），合并脚本再按 `(family, name)` 排序并 `sort_keys=True` 输出；因此 diff 不会因 map/构造顺序产生噪声。

### 结果（原文）

```text
[1/4] snapshot before: f53a6fd^
[2/4] snapshot after:  f53a6fd
[3/4] diff
IDENTICAL: tools-before.json == tools-after.json
[4/4] hashes / counts
ce1d122b35d700f2c6397857f27fb867394193c0bdbf7c5b85420e02f9461568  /tmp/tool-contract/tools-before.json
ce1d122b35d700f2c6397857f27fb867394193c0bdbf7c5b85420e02f9461568  /tmp/tool-contract/tools-after.json
total=73 by_family={'agent': 27, 'sdk': 20, 'server': 23, 'traffic': 3}
```

`/tmp/tool-contract/tools.diff` 为 0 字节（`diff -u` 退出码 0）。**没有任何一条工具的 name/description/prompt/schema/权限行为/只读标记/并发标记/返回结构存在差异。**

两个 JSON 各 114,308 字节；需要时可随时用脚本重新生成（命令见文末）。

---

## 二、清单第 20 号核对

**结论：总数 53，编号 1–53 连续，第 20 号是 `expand_digest`，没有空号、没有漏工具。**

按交付清单的排序（`file:line`）复算，第 17–23 行原文：

| # | 工具名 | 定义位置 |
|---:|---|---|
| 17 | `list_findings` | `agent/tools.go:877` |
| 18 | `list_facts` | `agent/tools.go:915` |
| 19 | `node_detail` | `agent/tools.go:968` |
| **20** | **`expand_digest`** | **`agent/tools_digest.go:122`** |
| 21 | `insert_assets` | `agent/tools_insert.go:127` |
| 22 | `add_company_scope` | `agent/tools_insert.go:382` |
| 23 | `add_task_scope` | `agent/tools_insert.go:436` |

构成：agent 27 + server 23 + traffic 3 = **53**（明细表与上面一致）。
机器快照也独立验证了：agent 家族 27 条中同时包含 `expand_digest` 与 `list_worker_traces`，无缺项。

> 如果看到的表里第 20 行是空的，那是复制/渲染时丢行（该行恰好跨了 `agent/tools.go` → `agent/tools_digest.go` 的文件切换）；原文就是上表加粗那行，工具本身存在且已纳入本次重构与快照。

---

## 三、`list_worker_traces` 归属结论

**选项 3：有意如此——只在特定 delegate 场景下注入，不应进入任何 agent 默认工具列表。**

### 证据链

| # | 事实 | 位置 |
|---|---|---|
| 1 | 工具定义存在（task-scoped 读工具，返回本任务已跑过的 work 索引） | `agent/tools.go:1954-1955`（`listWorkerTraces`，改造前 `readExpTool`，改造后 `toolbuild.Read`） |
| 2 | 对外工厂只有 accessor，没有放进任何 per-agent 工具表 | `agent/tools.go:216` `ListWorkerTracesTool()` |
| 3 | `MainAgentTools` / `PlannerTools` / `WorkerTools` **均不含**它 | `agent/tools_insert.go:730`、`agent/tools.go:2008`、`agent/tools_insert.go:708` |
| 4 | 因此 `builtinToolsByAgent()` 不构造它 → `BuiltinToolSeeds()` 不 seed 它 → DB `tools` 表没有行 → UI 不可绑定、agent 不可见 | `agent/toolcatalog.go:34-64`；快照对比：agent 27 个工具中仅 `list_worker_traces` 不属于 26 个 seed |
| 5 | **唯一注入点**：`toolListWorkerTraces()`（模型可见工具是 `list_task_worker_traces`）通过 `delegateToTask` 注入，`delegateToTask` 临时构造 per-task `ToolSet` 后调用该 accessor | `server/orchestration.go:361-366`、`server/orchestration.go:115-135`（`agent.NewToolSet(t.Store, "orchestrator")`） |
| 6 | 历史迁移明确把它从 worker 默认绑定里摘除，注释说明「worker 只执行单条意图，列所有 work 的 traces 属 planning/main 关注点」 | `server/orchestration.go:830-844`（`seedWorkerReadToolsUnbind`，key 列表第 836 行） |

### 为什么不是另外两种

- **不是死代码**：`list_task_worker_traces`（server 工具，已进入 orchestrationTools 并在启动时 seed）在调用时必然走到 `delegateToTask(..., ListWorkerTracesTool)`，路径可达且被使用。
- **不是漏注册**：它本来就不该是 agent 可直接调用的工具——它在当前任务内需要 task-scoped `ExplorationStore`，语义是「给跨任务查询做 per-task 实现」；直接暴露会与 `graph_overview`（本任务态势）和 `search_all_worker_traces`（跨 work 检索）重叠，并让 worker 越过「只执行单条意图」的边界（第 6 条迁移正是为此把它解绑）。
- **结论**：**选项 3（有意）**。模型侧应有的能力通过 `list_task_worker_traces` + `get_task_worker_trace` + `search_task_worker_traces` 这组跨任务工具提供；`list_worker_traces` 只是它们背后的 per-task 实现细节。

后续建议（不在本次范围）：可在 `agent/tools.go:216` 的 accessor 上加一行注释说明「仅由 server 跨任务 delegate 使用，不属于 agent 默认工具」，避免后来者误以为漏注册。

---

## 复现

```bash
cd <repo-root>
# 默认 before=f53a6fd^ after=f53a6fd
./scripts/tool-contract/run.sh

# 或指定 ref / 输出目录
./scripts/tool-contract/run.sh f53a6fd^ f53a6fd /tmp/tool-contract
```

脚本会在 `git worktree` 里运行，临时 dumper 文件用后即删，不污染工作区；输出：

- `/tmp/tool-contract/tools-before.json`
- `/tmp/tool-contract/tools-after.json`
- `/tmp/tool-contract/tools.diff`（应为空）
- `/tmp/tool-contract/agent-*.jsonl`、`server-*.jsonl`（原始分片）

依赖：Go（模块缓存已就绪）、python3、git；不需要数据库、不需要前端构建。
