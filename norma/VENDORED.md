# norma（内置副本）

ARTEX-RedTeam 的 agent SDK 原为外部依赖 `github.com/Autumn-27/norma`，现整体并入本模块：

- 源码位置：`norma/`
- import 路径：`github.com/Bluestar-coder/ARTEX-RedTeam/norma/...`
- `go.mod` 中**没有**该依赖的 `require` 或 `replace`，也不再有独立 `go.mod`

这样构建与同步都不再依赖上游仓库。

## 来源

| 项 | 值 |
| --- | --- |
| 上游模块 | `github.com/Autumn-27/norma` |
| 并入版本 | `v0.4.3` |
| 许可证 | Apache-2.0（见 `LICENSE`，含上游版权声明，须随分发保留） |

## 与上游的差异

1. import 路径统一替换为 `github.com/Bluestar-coder/ARTEX-RedTeam/norma/...`（含包内互相引用）。
2. 移除了 `examples/` 与 `cmd/`（上游示例程序与独立 CLI，本项目不使用）。
3. 全部 `*_test.go` **默认参与测试**（不再使用构建标签）。上游 `v0.4.3` 有 7 个测试在干净环境下失败，
   本仓库已逐个按根因修掉：

   | 包 | 测试 | 根因 | 处理 |
   | --- | --- | --- | --- |
   | `llm` | `TestDoStreamHonorsConfiguredRetries` | 测试直接调用内部 `doStream`，未设置 `Config.HTTPClient`（生产路径由 `NewProvider` 填充），导致 nil 解引用 | 测试补齐 `HTTPClient`（测试初始化缺陷，非实现缺陷） |
   | `noa` | `TestAdaptiveGrowthTracksTheWindow` | `GrowthFloor` 与 `GrowthCap` 都是 50K，clamp 区间宽度为 0，`GrowthRatio` 永远不起作用，自适应增长恒为常量 | 实现：拆开 floor/cap，令默认增长单元随窗口缩放（128K 以上仍为 50K） |
   | `noa` | `TestSuppressionReleaseFitsInsideTheWindow` | cadence 门槛是绝对值（200K 窗口的 20K），在 40K 窗口占 56%，抑制后不可能在溢出前解除 | 实现：`NudgeGrowthFloor` 以窗口的 1/8 封顶 |
   | `noaadapter` | `TestLongSessionStaysBounded` | 40K 窗口下 cadence 过大，压缩跟不上（同上根因） | 随 `noa` pacing 修复通过 |
   | `noaadapter` | `TestLongSessionWithPartiallyCompliantModel` | 同上：模型只部分配合时机械阀来不及介入 | 随 `noa` pacing 修复通过 |
   | `noaadapter` | `TestOverflowLearningIsWiredIn` | `ParseOverflowWindow` 与 `overflowState.learn` 没有调用方，溢出错误里学到的真实窗口从未生效，下一次视图仍按被拒的配置值重建 | 实现：在 `Compactor.IsOverflow` 中接线（它是唯一能看到错误文本的地方） |
   | `noaadapter` | `TestTierTwoIsUnreachableOnSmallWindows` | 断言已过时：小窗口下 tier 2 现在能正常触发 | 测试：改为正断言 `TestTierTwoFiresOnSmallWindows` |

   `go test ./...` 现在会直接运行这些测试；不再需要 `-tags norma_upstream`。

4. 补入上游仓库根目录的 `LICENSE`（模块 zip 未包含）。

## 升级方式

上游发新版本后，从模块缓存取源码并重新映射路径：

```bash
VER=v0.4.4   # 目标版本
MOD="$(go env GOMODCACHE)/github.com/!autumn-27/norma@${VER}"

# 1) 用上游源码覆盖（保留本文件与 LICENSE 的更新由人工确认）
rsync -a --delete --exclude='go.mod' --exclude='go.sum' \
      --exclude='examples/' --exclude='cmd/' "$MOD/" norma/

# 2) 重新映射 import 路径
grep -rIl 'github.com/Autumn-27/norma' --include='*.go' norma | \
  xargs sed -i '' 's|github.com/Autumn-27/norma|github.com/Bluestar-coder/ARTEX-RedTeam/norma|g'

# 3) 同步许可证、格式化、验证
#    diff 上游 LICENSE 与 norma/LICENSE，有变化则更新
gofmt -w norma
go mod tidy && go build ./... && go vet ./...
go test ./norma/...                # 上游测试默认参与，新版本若有新失败需按根因处理
```

升级后更新本文件的「并入版本」，并检查上游 `go.mod` 的依赖变化是否需要同步到主 `go.mod`（`go mod tidy` 会自动处理）。
