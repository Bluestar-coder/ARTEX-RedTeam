# JS 接口抽取：正则 → AST 选型记录

日期：2026-11-09；范围：`internal/jsintel`（纯静态，不执行 JS）。

## 候选对比（最小验证）

| 候选 | AST 公开性 | 编译后增量 | 许可证/维护 | 结论 |
|---|---|---:|---|---|
| evanw/esbuild | 否，parser/内部 AST 在 `internal/js_parser` | — | MIT，活跃 | 否决：公共 API 只有 transform，不暴露 AST；vendor internal 包升级风险大 |
| dop251/goja/parser | 是（ESTree 风格） | ≈ +2.78 MB（同条件空 main 对比） | MIT，活跃 | 备选：现代语法好，但体积更大；本轮未选 |
| tdewolff/parse/v2 v2.8.20 | 是（`js.Parse` + `js.Walk`） | **≈ +1.05 MB**（4.3MB 主程序基准） | MIT，活跃、纯 Go、无 cgo | **选用**：体积最小、解析快、AST 可遍历 |

阈值：依赖导致二进制 +10MB 以上需先报告；实测 +1.05MB，通过。

## 真实 bundle 解析基准（GitHub Actions，run 42602239447）

| 样本 | 字节 | 解析耗时（3 次最优） | Heap 增量 | 结果 |
|---|---:|---:|---:|---|
| figma `2545-...js` | 1,097,636 | 18 ms | 10 MB | 成功 |
| cloudflare `_ai-search-modal...js` | 90,503 | 1 ms | 0 MB | 成功 |
| python `jquery-ui-1.12.1.min.js` | 253,668 | 7 ms | 9 MB | 成功 |
| 合成 5MiB（单数组 bundle） | 5,242,860 | 7–9 ms | 4–10 MB | 成功 |

单文件 5MiB 上限下解析耗时约 7–9ms，远低于 2s 预算；解析失败返回 error（例如把 mp4 当作 JS 时会明确报错），调用方回退正则结果。每文件解析两次（声明收集 + 调用遍历），不保留 AST，内存与文件大小同阶。

## 取舍

- 常量传播：同 host 的 bundle 内跨文件，最多 5 跳；超过或不还原 → `needs_manual`（detail 记录 `exceeded_hops` / `runtime_constant` / `dynamic_construction` / `runtime_base_url`）。
- 不做：执行 JS、无头浏览器、eval/Function/atob 解码、控制流分析、反混淆。
- 回归策略：AST 结果与保留的正则结果按 `host|method|path` 合并取最高 confidence，确保已接受的 high 路径不会丢失；正则作为保守回退层保留。
