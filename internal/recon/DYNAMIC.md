# M3 动态分析（无头浏览器）说明与验收记录

定位：与 `active`、`fofa` 并列的第三类 provider（`dynamic`），默认 **OFF**，只由独立
工具 `extract_js_dynamic` 触发，**不混入** `extract_js`。

## 依赖与安装

- 外部 Chrome/Chromium（不打包进主二进制）：
  - macOS：自动发现 `/Applications/Google Chrome.app/...`；
  - Linux：`google-chrome` / `chromium` / `chromium-browser` 在 PATH；
  - 自定义：`RECON_DYNAMIC_BROWSER=/path/to/chrome`。
- Go 侧使用 `chromedp`（CDP 客户端）驱动；主程序实测 **+1.83MB**（43.1MB → 44.9MB），
  浏览器本体在外部进程。
- 没有 Chrome / 未设置 proxy 时 provider 直接失败并给出安装/配置提示；CI 不启动浏览器，
  `DYNAMIC_E2E=1` 才跑浏览器用例（否则明确 skip）。

## 开关与上限

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `RECON_DYNAMIC_ENABLED` / `ARTEX_REDTEAM_RECON_DYNAMIC_ENABLED` | 0 | 总开关；recon 默认行为不变 |
| `RECON_DYNAMIC_PROXY` / `ARTEX_REDTEAM_RECON_DYNAMIC_PROXY` | 空 | **必填**出口代理（http/https/socks5）；为空时读取 `HTTP(S)_PROXY`，仍为空则直接失败 |
| `RECON_DYNAMIC_PAGE_TIMEOUT_SEC` | 30 | 单页导航超时 |
| `RECON_DYNAMIC_IDLE_SEC` | 5 | load 后被动等待异步请求时间 |
| `RECON_DYNAMIC_HOST_TIMEOUT_SEC` | 120 | 单 host/单次总时长 |
| `RECON_DYNAMIC_MAX_PAGES` | 3 | 每次运行页面硬上限（当前实现只加载 1 页） |
| `RECON_DYNAMIC_MAX_MEMORY_MB` | 1024 | 传给 Chromium 的 JS heap 上限（`--max-old-space-size`） |
| `RECON_DYNAMIC_CONCURRENCY` | 1 | 进程级硬上限固定为 1（另有配置值日志） |
| `RECON_DYNAMIC_BROWSER` | 自动 | chrome/chromium 路径 |

## 出口与拦截

Chrome 只连 provider 内的本地 `scopeProxy`（127.0.0.1 随机端口），scopeProxy 对每个
HTTP 请求/CONNECT **先做 Scope 校验**：

- 越界 host → 立即 403，不 dial 上游，记录 blocked 日志/结果；
- 范围内 host → 通过 `RECON_DYNAMIC_PROXY`（HTTP CONNECT 或 SOCKS5）转发；
- 上游不可用 → 请求失败，**没有直连回退**。

因此外域请求不是“发出去再丢弃”，而是在浏览器出口前被拒绝；Network 事件里即使出现
被拒 URL，也不会写入接口 fact（动态 E2E 断言过）。

## 数据边界

- 只记录 xhr/fetch/ping（静态 css/js/png 等后缀排除）；
- URL：只保留 scheme+host+path（query 值不入库，键名单独存 `query_params`）；
- 请求体：只存字段名（JSON 递归 key / form key），值丢弃；
- 请求头：只存非标准头字段名（`x-token`/`x-sign` 等），所有值、Cookie 值丢弃；
- 响应：状态码、content-type；
- 不点击、不填表、不登录；页面发起域外导航/重定向时由 scopeProxy 直接拦截。

## 本地验收（2026-11-09，macOS + Google Chrome + 本地测试出口代理）

### 1. cloudflare 核心场景（静态 0 条）

```text
[recon] provider=dynamic page=https://www.cloudflare.com/ duration=8.266s
  pages=1 max_pages=1 max_memory_mb=512 concurrency=1 processes=1
  observed=156 xhr_fetch=3 blocked=12
```

真实接口（均 high，写入 fact）：

```text
POST https://www.cloudflare.com/static/z/t                 (params 字段名 40+，无值)
GET  https://ot.www.cloudflare.com/ot/consent/.../en.json
GET  https://ot.www.cloudflare.com/ot/consent/.../<id>.json
```

外域拦截日志样例：

```text
[recon] provider=dynamic blocked url=https://static.cloudflareinsights.com reason=out_of_scope: ...
[recon] provider=dynamic blocked url=https://www.googletagmanager.com reason=out_of_scope: ...
[recon] provider=dynamic blocked url=https://static.ads-twitter.com reason=out_of_scope: ...
```

落库/合并（`TestExtractJSDynamicE2E`）：

```text
dynamic first run: interfaces:3 facts_created:3
DYN FACT POST /static/z/t www.cloudflare.com source=dynamic conf=high
MERGED sources=[dynamic, static] kinds=[dynamic static]
by_source=map[dynamic:3 static:1]
idempotency: second run facts_created=0
```

### 2. 超时 / 出口失败 / kill 回收

```text
PASS TestDynamicPageTimeout (3.57s)       # 1s 页面超时，上下游 stall，按时失败
PASS TestDynamicUnavailableProxyNoDirect  # 代理 127.0.0.1:1，ERR_TUNNEL_CONNECTION_FAILED，无直连
PASS TestDynamicKillCleanup               # 页面加载中 kill -9 Chrome；随后 pgrep -f artex-dynamic- 为空
```

正常跑完也检查过 `pgrep -f artex-dynamic-` 为空（profile 目录已清理）。

### 3. 不回归

- `python.org` 静态：interfaces=9、facts=10、二次运行 facts 仍 10；scope/size-cap 日志不变；
- `linear.app` / `tailwindcss.com` 动态未新增接口；静态 0 条保持；
- `go test ./...` 36 包全绿（DYNAMIC_E2E 未开启时浏览器用例 skip）。

## 复现命令

```bash
# 1) 启动项目出口代理（示例：任意 HTTP CONNECT 代理）并设置
export RECON_DYNAMIC_ENABLED=1
export RECON_DYNAMIC_PROXY=http://127.0.0.1:PORT

# 2) provider 验收（需要 Chrome）
DYNAMIC_E2E=1 go test ./internal/recon -run 'TestDynamic(Cloudflare|KillCleanup|PageTimeout|UnavailableProxy)' -v

# 3) 落库/合并验收（需要 Postgres）
ARTEX_REDTEAM_PG_DSN=... DYNAMIC_E2E=1 go test ./agent -run TestExtractJSDynamicE2E -v

# 4) 默认关闭/CI 行为
go test ./internal/recon -run TestDynamicDisabledByDefault
go test ./...   # 未设置 DYNAMIC_E2E 时浏览器测试 skip
```

## 明确不做

不执行页面业务操作（点击/表单/登录），不做反混淆，不做 MITM 解密，不做跨页面爬取；
单实例只加载一个页面。当前 `--max-old-space-size` 是 JS 堆上限，不是进程 RSS 硬限制。
