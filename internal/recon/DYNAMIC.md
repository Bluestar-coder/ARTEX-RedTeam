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
| `RECON_DYNAMIC_HOST_TIMEOUT_SEC` | 300 | 单 host/单次总时长（多页共享） |
| `RECON_DYNAMIC_MAX_PAGES` | 10 | 每次运行页面硬上限（首页 + 候选页） |
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

## 多页候选与导航边界

候选按优先级：

1. 资产图 endpoint 的 GET/HEAD 页面 URL + 静态接口 fact 中同 host 的页面型 path
   （排除 `/api/`、`/v1/`、`.json` 等 API/静态资源；agent 侧 `dynamicKnownPages`）；
2. 首页加载后 DOM 里的同 host `<a href>`；
3. 关键词排序：`login/signin/register/user/account/admin/console/dashboard/pay/order/list/detail/api`
   权重高的先跑，再按路径长度稳定排序。

统一过滤：Scope 内、**仅同 host**（不跟子域/外域）、绝对 http(s)、去掉伪协议
`javascript:/mailto:/tel:`、去掉静态后缀、去 fragment、去重（含尾斜杠归一化）。

导航规则：

- 只 `Page.navigate` 打开 URL；不点击按钮/链接元素、不填表、不登录、不提交；
- 每个页面只加载一次（visited 集合），互链页面不会来回跑；
- 域外链接不进入候选；页面自身跳到域外时由 scopeProxy 拦截，最终 URL 越界则本页
  不记录并继续其它候选；
- 主 frame 重定向链 >3 跳 → 该页标记错误并停止使用其记录；
- 每页独立 capture state，结束后导航 `about:blank` 清页面状态；单页失败/超时只记录
  PageErrors，已捕获的请求仍合并落库；
- 浏览器进程/并发/出口链沿用上一轮（进程级并发 1，无直连回退）。

## 数据边界

- 只记录 xhr/fetch/ping（静态 css/js/png 等后缀排除）；
- URL：只保留 scheme+host+path（query 值不入库，键名单独存 `query_params`）；
- 请求体：只存字段名（JSON 递归 key / form key），值丢弃；
- 请求头：只存非标准头字段名（`x-token`/`x-sign` 等），所有值、Cookie 值丢弃；
- 响应：状态码、content-type；
- 不点击、不填表、不登录；页面发起域外导航/重定向时由 scopeProxy 直接拦截。

## 本地验收（2026-11-09，macOS + Google Chrome + 本地测试出口代理）

### 1. cloudflare：10 页，接口 3 → 4

```text
[recon] provider=dynamic page=https://www.cloudflare.com/ duration=51.139s
  pages=10 max_pages=10 max_memory_mb=512 concurrency=1 processes=1
  observed=1420 xhr_fetch=4 blocked=21

pages_visited=[/, /products, /solutions, /resource-hub, /plans,
  /under-attack-hotline, /resource/contact-enterprise-sales, /partners, /startups, /domains]
```

真实接口（all high，已落库）：

```text
POST https://www.cloudflare.com/static/z/t
GET  https://www.cloudflare.com/svc/marketo-form-schema
GET  https://ot.www.cloudflare.com/ot/consent/.../en.json
GET  https://ot.www.cloudflare.com/ot/consent/.../<uuid>.json
```

外域拦截仍生效（URL 不出现在结果）：

```text
blocked https://static.cloudflareinsights.com
blocked https://www.googletagmanager.com
blocked https://static.ads-twitter.com
...
```

### 2. figma：10 页，动态接口 7

```text
[recon] provider=dynamic page=https://www.figma.com/ duration=33.68s
  pages=10 xhr_fetch=7 blocked=16
```

其中包含 `POST /api/figment-proxy/monitor`、`POST /api/figment-proxy/page`、
`GET /api/geoip` 等真实接口；页面型 GET/静态构建产物已被排除。

### 3. 落库 / sources 累积 / 幂等

`TestExtractJSDynamicE2E`（cloudflare 10 页）：

```text
dynamic first run: pages_count:10 interfaces:4 facts_created:4 known_pages:0
MERGED sources=[dynamic x10, static x1] kinds=[dynamic static]
source_count=10 / 11
by_source=map[dynamic:4 static:1]
second run facts_created=0
```

同一接口被 10 个页面触发时仍只有一个 fact，sources 累积 10 个页面 URL；
`evidence_sources` 可看出触发页面集合。

### 4. 预算 / 循环 / 超时 / kill 回收

```text
PASS TestDynamicPageBudget (10.84s)   # max_pages=2 -> 只跑 2 页
PASS TestDynamicHostTimeout (5.06s)   # host=5s -> 按时停，已捕获 3 条保留
PASS TestDynamicLoopGuard (0.83s)     # / -> /a -> /b 互链，只跑 3 页不循环
PASS TestDynamicPageTimeout (3.57s)   # 单页 1s 超时
PASS TestDynamicUnavailableProxyNoDirect  # 代理不可用 -> 直接失败，无直连
PASS TestDynamicKillCleanup           # 加载中 kill -9；pgrep -f artex-dynamic- 为空
```

正常跑完也检查过 `pgrep -f artex-dynamic-` 为空（profile 目录已清理）。

### 5. 不回归

- `python.org` 静态：interfaces=9、facts=10、二次运行 facts 仍 10；
- `figma.com` 静态：interfaces=14、sensitives=9、facts=35、二次运行 facts 仍 35；
- `linear.app` / `tailwindcss.com` 静态仍 0；
- 动态浏览器用例仍只在 `DYNAMIC_E2E=1` 时跑；CI 全绿（无浏览器时 skip）。

## 复现命令

```bash
# 1) 启动项目出口代理（示例：任意 HTTP CONNECT 代理）并设置
export RECON_DYNAMIC_ENABLED=1
export RECON_DYNAMIC_PROXY=http://127.0.0.1:PORT

# 2) provider 多页验收（需要 Chrome）
DYNAMIC_E2E=1 go test ./internal/recon -run 'TestDynamic(Cloudflare|Figma|PageBudget|HostTimeout|LoopGuard|KillCleanup|PageTimeout|UnavailableProxy)' -v

# 3) 落库/合并验收（需要 Postgres）
ARTEX_REDTEAM_PG_DSN=... DYNAMIC_E2E=1 go test ./agent -run TestExtractJSDynamicE2E -v

# 4) 默认关闭/CI 行为
go test ./internal/recon -run TestDynamicDisabledByDefault
go test ./...   # 未设置 DYNAMIC_E2E 时浏览器测试 skip
```

## 明确不做 / 限制

- 仍不做表单提交和点击，因此只能拿到「页面加载即触发」的接口；需要用户操作才发的请求拿不到；
- SPA 如果完全靠 JS 内部 `pushState` 且页面 URL 不变，只能算一页；
- 只跟随同 host 的 `<a href>`，不跟子域/外域；重定向链上限 3；
- 不做反混淆、不做 MITM 解密、不做跨 host 爬取；
- `--max-old-space-size` 是 JS 堆上限，不是进程 RSS 硬限制。
