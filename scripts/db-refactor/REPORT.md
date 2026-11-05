# db 查询/扫描与一次性迁移样板收敛（重构第 3 条）

> 分支 `release/v0.4.3`。三个独立提交：
> - A（上一轮）`0d2bc7f`：系统目录查询限定当前 schema；
> - B1 `0b71335`：reseed + watermark 收敛；
> - B2：rows 扫描收敛 + 本报告/脚本。

不改 SQL 语义、事务边界、错误处理、锁；不动 `db/schema.sql`、planner/worker/engine。

## 一、reseed / watermark 收敛（B1）

| 项 | 改前 | 改后 |
|---|---|---|
| reseed 提示词迁移 | 4 份同构函数（goals/mainagent/planner/worker，约 96 行重复） | `reseedPromptOnce(flag, agentKey, change)` 一份实现 + 4 个一行函数（保留各自注释/flag/变更说明） |
| watermark 触发 | 4 份重复循环（finding / taskTimeout / taskCreate / toolCall） | `fireWatermark(triggers, watermarkFire{…})` 统一控制流，差异（匹配条件、跳过规则、标题、消息模板、工具名过滤）用闭包传入 |
| watermark 初始化 | `init()` 里 4 段相同 `max(NodeID)` 播种 | `seedWatermark(key, query)` 一份实现 |

保留的行为细节：

- `reseedPromptOnce`：flag 在 `defer` 中置位（无论成功与否只尝试一次，与改前一致）、
  agent 行/模板缺失直接返回、已是默认则跳过、失败/成功日志文案逐字保留；
- `fireWatermark`：即使 `want` 为空也推进 watermark（避免后来启用触发器重放历史）；
  toolCall 的 `report_finding` 失败事件跳过、`containsFold(ToolNames)` 过滤、
  消息模板（工具/入参/返回、截断）全部原样；`StartTriggeredRun` 的 7 个参数不变；
- 查询顺序（finding → goals → timeout → taskCreate → toolCall）与 `init` 顺序不变；
  SQL 全部仍在 `db` 层方法（`NewFindingsSince`/`TimedOutTasksSince`/…）里，文本零变化。

## 二、rows 扫描收敛（B2）

新增 `db/query_rows.go`（52 行）：

```go
type RowScanner[T any] func(*sql.Rows) (T, error)

// scanRows / queryRows 统一 rows.Next/Scan/append/rows.Err/Close 契约。
// SQL 原样传入；*DB（嵌入 *sql.DB）与 *sql.Tx 都满足 queryer 接口。
```

用法（新查询，包括侦察阶段的资产查询）：

```go
func (d *DB) AssetsByCompany(companyID int64) ([]Asset, error) {
    return queryRows(d, `
SELECT id, domain, type FROM assets
WHERE company_id=$1 ORDER BY id`, []any{companyID}, func(r *sql.Rows) (Asset, error) {
        var a Asset
        err := r.Scan(&a.ID, &a.Domain, &a.Type)
        return a, err
    })
}
```

本次转换 7 处重复扫描（全部保持原有 nil/空切片契约与错误返回）：

| 函数 | 文件 | 原空值契约 |
|---|---|---|
| `TaskCompanyIDs` | `db/task_context.go` | nil |
| `TaskSources` | `db/task_context.go` | nil |
| `TaskLLMProfiles` | `db/task_context.go` | nil |
| `TimedOutTasksSince` | `db/triggers.go` | **非 nil 空切片**（显式归一化） |
| `NewTasksSince` | `db/triggers.go` | **非 nil 空切片**（显式归一化） |
| `ActivityByIDs` | `db/exploration.go` | **非 nil 空切片**（显式归一化） |
| `ActivityByIDsForTerminalIntents` | `db/exploration.go` | **非 nil 空切片**（显式归一化） |

`db` 包仍有 141 处手写 `rows.Next()` 循环未强制迁移（本次范围允许）；**新查询必须走
`queryRows`**，后续可增量迁移旧循环，但不再是「每加一个查询就抄一遍 Scan」。

## 三、SQL 快照对照（机器证据）

`scripts/db-refactor/sql_snapshot.py` 提取全仓 Go 源码里的 SQL 字符串字面量
（反引号原串与解释串，去掉 Go 注释后按空白归一化），形成 multiset 快照；
`run.sh` 在任意两个 ref 上重跑并 diff。

```text
before(0d2bc7f): distinct=766 total=1003
after(HEAD):     distinct=766 total=1003
sha256: 12c0ab9fd189c21c3ace9282d9649f33ac82e58e1c1e3a9f2ff200cf02d3fd42
diff: 空（0 字节）
```

说明：`queryRows` 的参数绑定方式由 `args ...` 变为 `[]any` 传参，但 SQL 文本与
占位符（`$1`…）逐字未变，所以快照一致。

## 四、验收

```text
go build ./...                  ✅
go vet ./...                    ✅
go test ./...                   ✅ 34 packages
go test ./... -shuffle=on       ✅ 34 packages
go test ./... -count=5          ✅ 34 packages
go test ./db -count=20          ✅
```

## 五、侦察阶段的资产查询能用这个入口吗？

**能。** 新查询在 `db` 包里直接用 `queryRows(db, sql, args, scanner)`：

- 支持 `*DB` 与 `*sql.Tx`（事务内查询无需额外适配）；
- SQL 任意 SELECT/JOIN/分页/排序，参数照旧用 `$1…`；
- 扫描端可做任意字段映射、`COALESCE`、`json.Unmarshal` 等（在 scanner 闭包里）。

尚未覆盖、如侦察阶段需要需另行补的：

1. **批量插入/更新**（`INSERT … ON CONFLICT`、`CopyFrom`）——本入口只负责 SELECT 扫描；
2. **动态列/动态排序** 仍要手写 SQL 字符串拼接（现有代码同样如此）；
3. 旧 141 处 `rows.Next()` 循环未迁移，若侦察查询复用这些旧函数不受影响，但**新增**查询
   请直接用 `queryRows`，不要复制旧样板。
