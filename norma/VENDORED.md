# norma（内置副本）

ARTEX 的 agent SDK 原为外部依赖 `github.com/Autumn-27/norma`，现整体并入本模块：

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
3. 全部 `*_test.go` 加了 `//go:build norma_upstream`。原因：上游 `v0.4.3` 有 7 个测试在干净环境下本身就失败——
   `llm` 1 个（`Config.HTTPClient` 为 nil 时 `doStream` 直接调用导致 panic）、`noa` 2 个、`noaadapter` 4 个（断言实现缺陷）。
   默认 `go test ./...` 不编译它们，避免污染本项目的测试结果；需要时：

   ```bash
   go test -tags norma_upstream ./norma/...
   ```

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

# 3) 重新挂上测试构建标签
find norma -name '*_test.go' | while read -r f; do
  grep -q '^//go:build norma_upstream' "$f" || \
    { printf '//go:build norma_upstream\n\n' | cat - "$f" > "$f.tmp" && mv "$f.tmp" "$f"; }
done

# 4) 同步许可证、格式化、验证
#    diff 上游 LICENSE 与 norma/LICENSE，有变化则更新
gofmt -w norma
go mod tidy && go build ./... && go vet ./...
go test ./...                      # 上游测试默认不参与
go test -tags norma_upstream ./norma/...   # 观察上游测试是否恢复全绿
```

升级后更新本文件的「并入版本」，并检查上游 `go.mod` 的依赖变化是否需要同步到主 `go.mod`（`go mod tidy` 会自动处理）。
