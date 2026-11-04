# norma（本地副本）

ARTEX 的 agent SDK 依赖 `github.com/Autumn-27/norma`。上游仓库由原作者维护，存在被删除/不可用的风险，因此把源码纳入本仓库，并用 `go.mod` 的 `replace` 指向这里：

```
replace github.com/Autumn-27/norma => ./third_party/norma
```

## 来源

| 项 | 值 |
| --- | --- |
| 上游模块 | `github.com/Autumn-27/norma` |
| 副本版本 | `v0.4.3` |
| 许可证 | Apache-2.0（见 `LICENSE`） |

## 与上游的差异

- 移除了 `examples/` 与 `cmd/`（上游的示例程序与独立命令行入口，ARTEX 不使用，保留会让 `go build ./...` 编译额外的 main 包）。
- 其余 Go 源码、`go.mod`、`go.sum`、`README.md` 与 `v0.4.3` 原样一致。
- 补入了上游仓库根目录的 `LICENSE`（模块 zip 未包含）。

## 升级方式

```bash
MOD=$(go env GOMODCACHE)/github.com/\!autumn-27/norma@<新版本>
rsync -a --delete "$MOD/" third_party/norma/
rm -rf third_party/norma/examples third_party/norma/cmd
chmod -R u+w third_party/norma
go mod tidy && go build ./...
```

同时把 `LICENSE`（若上游有变动）从上游仓库同步过来，并更新本文件的「副本版本」。
