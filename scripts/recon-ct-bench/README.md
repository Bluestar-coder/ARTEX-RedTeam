# CT 后端基准（M2）

三个候先后端：`crt.sh`、`crt.name`、`certspotter`。每个后端对同一批种子各查 5 次，
统计成功率、耗时与返回条数，用数据决定默认首选。

## 复现

```bash
./scripts/recon-ct-bench/run.sh                       # example.com,python.org × 5
./scripts/recon-ct-bench/run.sh example.com,python.org 5
```

输出中的 `CTBENCH,backend,seed,run,ok,dur_ms,names,err` 行可直接汇总。
基准实现：`internal/recon/ct_bench_test.go`（`CT_BENCH=1` 才运行，CI 默认 skip）。

## 实测结果（2026-11-09，GitHub Actions ubuntu-latest）

| 后端 | 成功率 | 平均耗时 | 中位耗时 | 平均返回条数 | 单次最多 |
|---|---:|---:|---:|---:|---:|
| certspotter | **10/10** | **59.6 ms** | 31 ms | 120.5 | 201 |
| crt.sh | 9/10 | 15,138 ms | 4,546 ms | **3,707.4** | **8,972** |
| crt.name | 10/10 | 166.4 ms | 128 ms | 0 | 0 |

- crt.sh 唯一失败是 `python.org` 一次 45s 超时；其余 9 次成功且返回量最大（比 certspotter 多 1-2 个数量级）。
- crt.name 在本 CI 出口返回 200 但 0 条名称（当前 API 行为/出口差异），暂不作为主路径。
- certspotter 免费额度的返回量明显少于 crt.sh，但 100% 成功、亚秒级。

## 默认选择

`ARTEX_REDTEAM_RECON_CT_BACKENDS` 默认 **`certspotter,crt.sh,crt.name`**：

1. 先保证「CT 主路径稳定可用」——certspotter 10/10、平均 60ms；
2. 失败自动切 crt.sh（覆盖最大，但慢且偶发超时）；
3. crt.name 放最后（当前返回 0 条，保留为可切换实现）。

如果更看重枚举覆盖而非延迟，可显式改为 `ARTEX_REDTEAM_RECON_CT_BACKENDS=crt.sh,certspotter`；
全部失败时 active provider 仍按 M1 行为降级到内置字典并记日志。
