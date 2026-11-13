# Web 管理面板

面板是核心自带的网页管理界面，给部署者本人用：看模型用量和运行状态，浏览和修改各个会话的策略、人设和记忆，查看任务、审计记录和当前配置。页面和脚本用 `go:embed` 打包进 `conloom` 二进制，不需要另装任何东西，也不引用外部资源。设计见 `docs/superpowers/specs/2026-11-13-web-panel-design.md`。

## 启用

在 `conloom.yaml` 里加上 `panel` 段，重启核心即可；整段省略（或只写一个空的 `panel:`）时不启用面板，`/panel/` 下的路径都不存在。

```yaml
panel:
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
  session_ttl: 168h      # 登录后最长有效期
  idle_timeout: 12h      # 多久不操作就要重新登录
  cookie_secure: false   # 经 HTTPS 反向代理访问时改为 true
```

- 密码哈希用 `conloom panel hash-password` 生成，写进 `.env`，步骤见 [部署：Web 管理面板](deployment.md#web-管理面板)。字段说明见 [配置参考：panel](configuration.md#panel)。
- 面板和 API 共用 `server.listen` 这一个端口：`/panel/` 是页面，`/panel/api/` 是页面用的 JSON 接口，`/panel` 会跳转到 `/panel/`。
- 面板用自己的登录会话（cookie）鉴权，和 `api_tokens` 的 Bearer 令牌无关。现有的 `/api/v1/*`、`/healthz`、`/metrics` 行为不变。
- 可以配置多个账号，权限都一样，都是管理员。

## 访问

核心默认只监听本机（`127.0.0.1:8787`）。从自己的电脑访问服务器上的面板，最简单的是 SSH 隧道：

```sh
ssh -L 8787:127.0.0.1:8787 <用户>@<服务器>
```

保持这个连接，在本机浏览器打开 `http://127.0.0.1:8787/panel/`。本机 8787 端口被占用时换一个本地端口，例如 `-L 18787:127.0.0.1:8787`，然后打开 `http://127.0.0.1:18787/panel/`。

需要不经 SSH 直接从公网访问时，用反向代理负责 HTTPS，并把 `panel.cookie_secure` 改为 `true`，示例见 [部署：经 HTTPS 反向代理访问](deployment.md#经-https-反向代理访问)。不要把 `server.listen` 直接改成公网地址：那样连 `/metrics` 和 API 一起暴露出去，面板也没有 HTTPS。

## 页面

界面是中文，跟随系统的深浅色，窄屏（手机）上单列显示。未登录时任何页面都会跳到登录页；使用中途会话过期时，登录后回到原来的页面。

- **概览**：今天（`server.timezone` 的当地日）的调用数、失败数和各类 token，最近 7 天按模型的 token 柱状图，版本和启动时间，实时状态，以及各 bot 的适配器最近一次调用核心的时间。每 10 秒刷新一次。
  - 实时状态包括准入队列深度、发件箱待发数、向量积压、关键词索引补建剩余、熔断中的角色。没有配置 `embedding` 段时向量积压显示“未启用”（接口里是 `null`）。
  - “最近调用”是绑定这个 bot 的 API 令牌最近一次通过鉴权的时间（通常就是它的适配器），只记在内存里，从本次进程启动起算：核心刚重启、适配器还没来得及调用时显示“自本次启动未调用”。
  - 页面下方的“进程指标”是本进程 Prometheus 指标的快照，重启后清零；需要长期曲线时请另接 Prometheus。
- **用量**：按日期范围（最近 7、30、90 天或自定义）和分组（模型、角色、用途、bot）统计每天的调用数、失败数和 token，画每日堆叠柱状图，下面是区间合计表和逐次调用明细。统计规则见下文 [模型用量](#模型用量)。
- **会话**：所有 bot 的全部群聊和私聊，带消息数、最后一条消息的时间、是否暂停、主动发言和记忆开关、人设模板和版本。点进一个会话有五个标签页：
  - **消息**：按时间倒序浏览；也可以输入关键词检索，规则和 `recall` 的关键词检索相同（任一词命中，按相关度排序，同分时新的在前），可以限定日期。
  - **策略**：常用字段做成了表单（主动发言、记忆、历史条数），另有“高级：JSON 编辑”可以整份替换；暂停、恢复；历史版本列表，可以把任一版本恢复为新版本。
  - **人设**：编辑人设的各个字段、套用配置里的模板、回滚到历史版本。会话还没有人设时只显示“尚未生成”和将要使用的模板，查看不会生成第 1 版。
  - **记忆**：摘要、整理进度和最近的错误，成员的备注和好感度，可以修改备注或好感度、忘记某个成员、立即整理一次，也可以查看某个成员（或摘要）的修订记录。这里的数据和 Bearer API 的 `GET /api/v1/conversations/{id}/memory` 完全相同：被忘记的成员没有备注和好感度，不会出现在列表里。
  - **任务**：这个会话的任务。
- **任务**：全部任务，可以按状态、bot、会话筛选；任务详情里有 meta、生成的动作，以及这个任务的每次模型调用。任务受 `runtime.ops_retention`（默认 30 天）清理，更早的查不到。
- **审计**：审计记录，可以按种类和 bot 筛选，受 `runtime.audit_retention`（默认 90 天）清理。
- **配置**：当前生效的配置（环境变量展开以后），只读。见下文 [配置页打码](#配置页打码)。

所有用户数据（消息、昵称、人设、审计内容等）都按纯文本显示，里面的 HTML 不会被执行。

### 面板上的修改

面板上的修改和 Bearer API、聊天命令走同一套代码，校验规则、字数上限、好感度范围完全一致，校验失败时原样显示服务端返回的错误。

- 每次修改都写一条审计记录，`actor` 为 `panel:<用户名>`，`bot_instance_id` 是会话所属的 bot。种类沿用 API 的 `policy.update`、`persona.update`、`memory.update`、`memory.forget`；触发整理写 `memory.consolidate`（API 触发整理不写审计）。登录成功写 `panel.login`。
- 策略和人设的新版本里 `updated_by` 为 `panel:<用户名>`；成员记忆的修订记录来源为 `panel`。
- 忘记成员等同于这个人在会话里发 `/忘记我`；暂停、恢复和 `/pause`、`/resume` 写入同样的策略版本（恢复时并发回到暂停前最近一个非 0 的值）。

## 登录与安全

- **会话**：登录后有效期 `session_ttl`（默认 168h），超过 `idle_timeout`（默认 12h）没有操作也会失效，以先到者为准。“登出”立即删除会话。
- **改密码**：在配置里换掉某个用户的 `password_hash` 并重启后，这个用户已有的会话全部失效，需要用新密码重新登录；从配置里删掉一个用户，他的会话同样失效。
- **防暴力破解**：同一来源 IP 15 分钟内登录失败 5 次，或全部来源合计 15 分钟内失败 30 次，之后的登录直接返回 429 并带 `Retry-After`（秒），等最早的失败移出 15 分钟窗口后恢复。还在校验中的登录也计入，所以并发尝试也无法突破上限。来源 IP 只取 TCP 连接的对端地址，不信任 `X-Forwarded-For`；经反向代理访问时所有人都是代理的地址，共用同一个“每 IP 5 次”的额度。用户名不存在时也做同样成本的哈希计算，从响应时间看不出用户名是否存在。
- **cookie**：名为 `conloom_panel`，`HttpOnly`（脚本读不到）、`SameSite=Strict`、`Path=/panel/`；`panel.cookie_secure: true` 时另加 `Secure`，浏览器只经 HTTPS 发送。数据库只存会话令牌的 SHA-256。
- **CSRF**：除登录外，所有 `POST`、`PUT`、`DELETE` 请求都必须带 `X-CSRF-Token` 头，值从 `GET /panel/api/session` 取得，缺少或不对返回 403。请求带 `Origin` 头时（登录也一样），它的主机部分必须和 `Host` 头一致，否则返回 403；所以反向代理必须原样转发 `Host` 头。
- **响应头**：所有 `/panel/` 响应都带严格的 `Content-Security-Policy`（只允许本站资源、禁止被嵌入框架）、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`；接口响应另加 `Cache-Control: no-store`。
- **日志**：登录失败记一条 warn 日志 `panel login failed`，带来源 IP，用户名只在它存在时记录，从不记密码；失败次数计入 `conloom_panel_login_failures_total`。面板接口不记请求体和消息内容。

### 配置页打码

配置页按白名单输出：只输出明确列出的非敏感字段，以后新加的配置字段在列入之前不会显示。

- 所有密钥只显示 `(已设置)` 或 `(未设置)`，不输出值：模型和向量服务的 `api_key`，适配器的 `access_token`、`token`、`core_token`，面板用户的 `password_hash`。
- `api_tokens` 只显示名字、所属 bot 和 scope。
- `model_profiles.*.extra_body` 只显示键名；布尔值和数字照常显示，字符串、列表、对象等其他值一律显示为 `(已设置)` 或 `(未设置)`，因为它们可能藏着凭据。
- 接口地址（模型和向量服务的 `endpoint`，适配器的 `ws_url`、`core_url`、`api_base`）显示主机和路径，但地址里可能带凭据：`user:password@` 部分去掉，查询参数只保留参数名、值一律显示为 `(已隐藏)`，`#` 后面的部分也显示为 `(已隐藏)`；解析不了的地址只显示 `(已设置)`。
- 人格模板（`persona_templates`）和默认群策略、私聊策略整段原样显示，它们只有行为设置，不含凭据。
- 没有配置 `embedding` 段时，`embedding` 显示为 `null`。

## 模型用量

每次模型调用都在 `model_calls` 表里记一行，重启不丢，面板的用量页和概览从这里统计。

- **记哪些调用**：
  - `chat`：回复（包括带工具的回复里的每一轮请求，每轮一行），成功和失败都记；
  - `memory`：记忆整理；
  - `embed_index`：后台为语义检索生成向量；
  - `embed_query`：检索时把查询句转成向量。
- **每行的内容**：时间、bot、会话、任务（记忆整理和后台生成向量没有任务时为空）、用途、角色（如 `short_context`，向量调用记 `embedding`）、模型名（上游返回的；失败拿不到时用配置里的）、提示 / 回复 / 思考 token（上游没返回就记 0）、发请求前估算的输入 token（只有 `chat` 有）、耗时、结果和上游的结束原因（`finish_reason`）。结果是 `ok`、`error`、`timeout`、`canceled` 之一，`ok` 以外的都算失败。
- **不拖慢回复**：记录先放进内存队列（4096 条），后台攒够 200 条或每 2 秒一次批量写库。队列满时丢弃并计入 `conloom_usage_dropped_total`，写库失败记 warn 日志并计入 `conloom_usage_write_errors_total`；退出时在限定时间内把剩下的写完。原有的 `model call` 日志和 `conloom_model_*` 指标不变。
- **保留**：`runtime.usage_retention`，默认 `9600h`（400 天），由每小时的清理任务分批删除，删除条数计入 `conloom_cleanup_deleted_total{table="model_calls"}`。
- **统计规则**：
  - 按 `server.timezone` 的自然日分桶；日期范围最长 400 天，结束日期是今天时包含今天到目前为止的调用。
  - 失败数是结果不为 `ok` 的调用数。
  - 延迟 p50 / p95 只按成功的调用计算，并且只在范围不超过 90 天时计算，更长的范围只给次数和 token。
  - 日期格式错误、开始晚于结束、超过 400 天都返回 400。
  - `runtime.usage_retention` 比所选范围短时，更早的日子显示为 0。
- **体积**：每次调用连同索引占几百字节，其中为统计查询建的覆盖索引每 100 万行约 97MB。按每天几千次调用算，保留 400 天是一两百万行，几百 MB；调用很多时可以调短 `runtime.usage_retention`。

## 接口

页面用的 JSON 接口都在 `/panel/api/` 下，带登录 cookie 访问；这些接口为面板服务，不保证兼容性，脚本和程序请用 [HTTP API](api.md)。

- 错误格式和 Bearer API 相同：`{"error":{"code","message","retryable","trace_id"}}`。没登录或会话已失效返回 401 `UNAUTHORIZED`，CSRF 或 `Origin` 检查不通过返回 403 `FORBIDDEN`，登录被限流返回 429 `RATE_LIMITED`。
- 时间是 RFC3339 UTC 字符串，可以为空的时间为 `null`。用量接口的 `from`、`to` 是 `server.timezone` 的当地日期 `YYYY-MM-DD`。
- 路径里的 `{key}` 是会话键 `bot:平台:类型:会话ID`（如 `qq-main:qq:group:group_10001`）经 URL 编码后的形式。面板跨 bot，所以不用 Bearer API 里那种按令牌绑定 bot 的会话 ID。
- 列表接口用 `before` 游标加 `limit` 翻页，`limit` 默认 50、最大 200；响应里的 `next_before` 是下一页的 `before`，最后一页为 `null`。

**鉴权**

| 接口 | 说明 |
|---|---|
| `POST login` | 请求 `{"name","password"}`，成功返回 `{"user","csrf_token","expires_at"}` 并设置 cookie |
| `GET session` | 当前用户、`csrf_token`、过期时间 |
| `POST logout` | 删除会话、清掉 cookie，返回 204 |

**读接口**

| 接口 | 说明 |
|---|---|
| `GET overview` | 版本、提交、启动时间、时区；各 bot 及其 `last_seen_at`；实时状态 `live`；今天的用量 `today` |
| `GET usage?from=&to=&group=` | 每日分桶和区间合计；`group` 为 `model`（默认）、`role`、`purpose`、`bot`；不传日期时为截至今天的 7 天 |
| `GET usage/calls?from=&to=&model=&purpose=&conversation=&before=&limit=` | 调用明细，新的在前；`conversation` 是会话键 |
| `GET metrics/live` | 本进程 Prometheus 指标的快照：计数器和仪表给值，直方图给次数、总和和按桶估算的 p50 / p95 |
| `GET conversations` | 全部会话 |
| `GET conversations/{key}` | 单个会话 |
| `GET conversations/{key}/messages?before=&limit=` | 浏览消息，按 `seq` 游标翻页 |
| `GET conversations/{key}/messages?q=&from=&to=&offset=&limit=` | 检索消息，按 `offset` 翻页，响应里的 `next_offset` 是下一页 |
| `GET conversations/{key}/policy` | 当前策略和历史版本 |
| `GET conversations/{key}/persona` | 当前人设、历史版本、可用模板；还没有人设时给出将要使用的模板，不写库 |
| `GET conversations/{key}/memory` | 与 `GET /api/v1/conversations/{id}/memory` 相同 |
| `GET conversations/{key}/memory/revisions?user_id=` | 某成员的修订记录，不传 `user_id` 时是摘要的 |
| `GET tasks?status=&bot=&conversation=&before=&limit=` | 任务列表 |
| `GET tasks/{id}` | 任务详情：任务、meta、动作、模型调用 |
| `GET audit?kind=&bot=&before=&limit=` | 审计记录 |
| `GET config` | 打码后的当前配置 |

**写接口**（都要带 `X-CSRF-Token`，都写审计）

| 接口 | 说明 |
|---|---|
| `PUT conversations/{key}/policy` | 请求体同 API 的 `PolicyPut`，返回新的当前版本 |
| `POST conversations/{key}/policy/restore` | 请求 `{"version","reason"}`，把该历史版本的策略写成新版本 |
| `POST conversations/{key}/pause`、`POST conversations/{key}/resume` | 暂停、恢复 |
| `PUT conversations/{key}/persona` | 请求体同 API 的 `PersonaPut`（整份替换、回滚到版本、套用模板） |
| `PUT conversations/{key}/memory/members/{user_id}` | 改备注或好感度，规则同 API |
| `DELETE conversations/{key}/memory/members/{user_id}` | 忘记该成员，返回 204 |
| `POST conversations/{key}/memory/consolidate` | 触发一次整理，返回 202 |

## 指标

面板和用量记录新增的 Prometheus 指标（`/metrics`）：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_usage_dropped_total` | — | 队列满或正在退出时丢弃的用量记录 |
| `conloom_usage_write_errors_total` | — | 用量记录写库失败的次数 |
| `conloom_panel_login_failures_total` | — | 面板登录失败（密码错或用户不存在）的次数 |
| `conloom_panel_requests_total` | `route`（路由模板，不含会话键）、`code` | 面板的请求数 |
