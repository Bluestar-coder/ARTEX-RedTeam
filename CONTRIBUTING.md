# 贡献与分支约定

## 分支模型

- **`main` 是唯一主干**，永远保持可发版状态：任何时刻从 `main` 打 tag 都应该能发布。
- 新功能从 `main` 拉 `feat/<主题>`，修 bug 从 `main` 拉 `fix/<主题>`；主题用小写短横线，例如
  `feat/recon-phase`、`fix/update-checksum`。
- **不设长期开发分支**：一个任务一个短分支，完成即通过 PR 合回 `main` 并删除。
- **不设长期 `release/*` 分支**。发版直接从 `main` 打 tag（`vX.Y.Z`）；如果需要 RC，用
  pre-release tag 或短期分支，合并/发布后立即删除。
- 分支合并方式：**rebase merge**，保留每个提交独立可回滚；不要 squash 成一坨。
  合并后删除远端与本地分支。

## PR 要求

- PR 目标分支只能是 `main`；
- CI 必须绿（`ci` workflow 的 `test` job）：
  - `go build ./...`
  - `go vet ./...`
  - `go test ./...`
  - `go test ./... -shuffle=on`
- 重的重复测试由定时任务跑（`ci` workflow 的 `heavy` job：`-count=5`、`go test ./db -count=20`），
  不阻塞日常 PR，但红灯要当天处理。
- 不允许通过跳过/关闭/放宽测试让 CI 变绿；测试依赖真实 PostgreSQL（CI 里由 service 容器提供）。

## 版本与发布

- 版本号由 git tag 驱动；Release 由 `.github/workflows/release.yml` 在 `v*` tag 上自动构建。
- 发布包同时包含 `artex-redteam-*`（新名）与 `artex-*`（旧名兼容）两套资产，见 README。
