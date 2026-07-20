# opencode Go 额度监控 + 转发网关

用 Go 编写的本地工具：批量监控多个 opencode Go 账号的**额度用量**与**到期时间**，并把
**OpenAI / Anthropic 兼容请求**按「用量最少」的策略负载均衡地转发到 opencode Go 官方接口
（配合 [New API](https://github.com/QuantumNous/new-api) 等只需建 **一个渠道**）。

后端 Go + SQLite（纯 Go 驱动，无 CGO）；前端 Vue 3 + Tailwind v4（CDN），多页面多文件，暗色风格。

## 目录结构

```
.
├── cmd/monitor/          # 程序入口（main）
├── internal/
│   ├── config/           # 运行时目录规划、环境变量
│   ├── store/            # SQLite：账号 / 模型 / 设置
│   ├── opencode/         # 抓取并解析工作空间页面（监控）
│   ├── proxy/            # /v1 转发 + Key 负载均衡（balancer.go）
│   └── server/           # HTTP 路由：页面 + 管理 API + 挂载转发
├── web/                  # 内嵌前端
│   ├── index.html        # 监控页
│   ├── settings.html     # 设置 + 模型管理页
│   └── static/           # common.js / dashboard.js / settings.js
├── data/                 # 运行时：SQLite（已 gitignore）
└── go.mod
```

## 运行

```bash
go run ./cmd/monitor      # 打开 http://localhost:8787
```

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `ADDR` | `:8787` | 监听地址 |
| `DATA_DIR` | `data` | 数据目录，SQLite 存于 `<DATA_DIR>/monitor.db` |

其余可调项在页面 **设置** 里改（存数据库）：自动刷新间隔、请求超时、并发数、额度紧张阈值、
即将到期天数、**转发 Key**。

## 账号字段

| 字段 | 用途 |
|---|---|
| 账号 / 密码 / 辅助邮箱 | 记录用；账号可粘贴 `账号----密码----辅助邮箱` 自动拆分 |
| 工作空间 ID | **监控额度**（`GET /workspace/<id>/go`），必填 |
| Auth | **监控用**：浏览器 Cookie 头（Bearer 开头则作 Authorization） |
| API Key | **转发用**：opencode Go API Key |

批量导入（每行）：`凭据 | 工作空间ID | Auth | APIKey`，凭据为 `账号----密码----辅助邮箱`。

## 转发网关

在 New API 里只建 **一个渠道**，指向本程序：

| 用途 | 地址 |
|---|---|
| OpenAI 兼容 Base URL | `http://<host>:8787/v1` |
| Anthropic Base URL | `http://<host>:8787` |
| 模型列表 | `GET /v1/models` |

- `POST /v1/chat/completions`、`POST /v1/messages` 等 `/v1/*` 请求按**路径镜像**转发到
  `https://opencode.ai/zen/go/v1/*`（chat 模型走 completions，Anthropic 模型走 messages，
  未来新增端点自动透传，无需改代码）。
- 每次请求从数据库挑选**用量水位最低**（三档用量取最大值）、未到期、有 API Key 的账号；
  同水位时按累计转发次数最少者，实现均衡。无可用 Key 返回 `503`。
- **失败自动重试**：上游返回 `401/403`（Key 失效/被封）、`408`、`429`（限流/超额）或 `5xx`、
  或连接失败时，自动改用**其它 Key** 重试，最多重试次数在设置页配置（`maxRetries`，默认 2，0–10）；
  `400/404/422` 等请求级错误对所有 Key 相同，不重试。请求体会被缓存以便重放。
- **转发 Key**：设置后，`/v1/*` 请求需带 `Authorization: Bearer <key>` 或 `x-api-key: <key>`
  才放行（在 New API 渠道密钥里填这个值）；留空则不校验（仅建议内网）。
- **模型管理**（设置页）：驱动 `/v1/models`；新增模型只需填 ID 与协议（openai / anthropic）。

## 监控原理

从工作空间 Go 页面内嵌数据解析三档额度（滚动 / 每周 / 每月的 `usagePercent` 与 `resetInSec`）。
**到期时间**（无需手填）取最后一档（优先每月）的重置时刻 = `抓取时间 + resetInSec`，换算为具体日期。

## API 一览

账号 `POST/PUT/DELETE /api/accounts[...]`、`POST /api/accounts/bulk`、刷新 `POST /api/[accounts/{id}/]refresh`；
设置 `GET/PUT /api/settings`；模型 `GET/POST /api/models`、`DELETE /api/models/{id}`；
转发 `/v1/models`、`/v1/chat/completions`、`/v1/messages`、`/v1/*`。

## 注意

- `data/monitor.db` 含明文密码 / Auth / API Key，已 gitignore，勿提交或放公开机器。
- 当前转发不做「失败自动换 Key 重试」；如需可后续扩展（需缓存请求体以便重放）。
