# Web 管理面板设计

日期：2026-11-13
状态：已定稿，待实施
相关文档：`docs/api.md`（Bot Interaction API）、`docs/architecture.md`、搜索引擎设计 `2026-11-14-search-engine-design.md`

## 1. 为什么做

- 现在管理 ConLoom 只有两条路：在聊天里敲斜杠命令，或者带 Bearer token 调 HTTP API。两者都只能一次操作一个会话，看不到全局。
- 模型用量只记在进程内存里的 Prometheus 计数器和 journald 日志里，服务一重启就清零，没法回答"这周花了多少 token、哪个模型最慢"。
- 已有的在线可改数据（每个会话的策略、人设、成员记忆和好感度）缺一个能浏览、比较版本、回滚的界面。
- 目标用户是部署者本人（单租户管理员），不是群友。

## 2. 目标、非目标、反目标

**目标**

1. **用量持久化**：每次模型调用（对话、记忆整理、向量）都记一行明细，存进 SQLite，重启不丢，保留期可配置。
2. **用量与性能看板**：按日期和模型（或角色、用途、bot）统计调用次数、失败数、各类 token、延迟 p50/p95，画每日柱状图；另外展示当前进程的实时状态（队列、发件箱、熔断、向量积压等）。
3. **会话管理**：列出所有会话（群聊和私聊），每个会话可以：
   - 浏览和搜索消息；
   - 查看、修改、回滚策略；
   - 查看、修改、套用模板、回滚人设；
   - 查看记忆摘要和成员记忆，修改备注和好感度，忘记成员，触发整理；
   - 查看该会话的任务。
4. **数据浏览**：全局任务列表和详情（含该任务的模型调用）、审计记录列表。
5. **配置查看**：只读展示当前生效的配置，密钥一律打码。
6. **登录保护**：面板必须登录，有会话过期、防暴力破解、防 CSRF、防 XSS。
7. **单二进制**：前端资源用 `go:embed` 打进 conloom 二进制，不引入 npm 构建、不依赖 CDN，CI 不变。

**非目标**

- 在网页上修改 yaml 配置、配置热加载。第一版只读展示，改配置仍然是改文件再重启。
- 多用户分级权限。配置里可以有多个账号，但都是管理员。
- HTTPS 终止。面板跟 core 同一个监听地址（默认只监听本机），远程通过 SSH 隧道访问；需要公网访问时由反向代理负责 HTTPS，文档给出示例。
- 通用指标的长期时序库。实时状态只展示本进程启动以来的值；需要长期曲线的指标，建议另接 Prometheus/Grafana。
- 实时推送。页面用定时轮询（概览页 10 秒一次）。
- 跨会话的全文检索。

**反目标**

- 未登录就能读写任何面板数据，或者面板的会话 cookie 能被别的站点借用（CSRF）。
- 密钥、令牌、密码哈希出现在配置页、接口响应、日志或前端源码里。
- 消息内容等用户数据通过 `innerHTML` 之类的方式注入页面（XSS）。
- 记账拖慢在线回复：写库必须异步、批量，队列满了就丢弃并计数，绝不阻塞模型调用路径。
- 面板查询拖垮服务：每个列表都有上限，统计查询有日期范围上限。
- 改变现有 Bearer API 和斜杠命令的行为。
- 公开仓库里出现真实 ID、令牌、主机名、昵称。测试只用假数据。

## 3. 方案取舍

**前端怎么做**

| 方案 | 结论 |
|---|---|
| **静态页面 + 原生 JS（ES 模块）+ JSON 接口，`go:embed` 打包（选用）** | 保持单二进制、零构建、CI 不变；图表用手写 SVG。代价是界面要自己写，不如组件库丰富 |
| React/Vue 单页应用 + npm 构建，产物嵌入 | 界面更丰富，但要引入 node 工具链、CI 改造、依赖供应链，项目从"单二进制无构建"变成两套工具链 |
| 外挂 Prometheus + Grafana | 图表现成，但多两个服务要运维；管不了数据；token 历史仍然依赖抓取，不是业务数据 |

**怎么登录**

| 方案 | 结论 |
|---|---|
| **账号 + 密码哈希写在配置里，服务端会话（存 SQLite）+ HttpOnly cookie（选用）** | 浏览器里没有可被脚本读走的长期凭据；能登出、能过期；改密码后旧会话失效 |
| 复用 API 的 Bearer token | token 要存在浏览器 localStorage，XSS 就能偷走；没有登出和过期 |
| 不登录，只靠 SSH 隧道 | 一旦有人加了反向代理就等于裸奔 |

**用量怎么存**

| 方案 | 结论 |
|---|---|
| **每次调用一行明细（选用）** | 能按任意维度和日期统计，能下钻到单个任务；每天几百到几千行，一年几十万行，SQLite 轻松应付 |
| 只存每日汇总 | 体积小，但不能下钻，维度要提前定死 |
| 定时抓 Prometheus 计数器的差值 | 重启前最后一段会丢，粒度粗 |

## 4. 设计

### 4.1 用量记录

**表**：迁移 `0008_model_calls.sql` 新建 `model_calls`，每次模型调用一行。

| 列 | 含义 |
|---|---|
| `id` | 自增主键 |
| `at` | 调用开始时间（沿用现有表的时间列格式） |
| `bot_instance_id`、`conversation_key`、`task_id` | 来源；记忆整理没有任务时 `task_id` 为空，向量后台任务没有会话时为空 |
| `purpose` | `chat`（回复和工具轮次，每轮一行）、`memory`（记忆整理）、`embed_index`（后台生成向量）、`embed_query`（检索时把查询句向量化） |
| `role` | 模型配置的角色（如 `short_context`），向量调用记 `embedding` |
| `model` | 上游返回的模型名；调用失败拿不到时，用配置里的模型名 |
| `prompt_tokens`、`completion_tokens`、`reasoning_tokens` | 上游返回的用量，没有就记 0 |
| `estimated_tokens` | 发请求前估算的输入 token（只有对话有） |
| `latency_ms` | 耗时 |
| `outcome` | `ok`、`error`、`timeout`、`canceled` |
| `finish_reason` | 上游返回的结束原因 |

索引至少要有 `at`，以及支撑"某个任务的调用"的 `task_id`。

**写入**：

- 新建 `internal/usage` 包，提供 `Recorder`：`Record(call)` 不阻塞，把记录放进带缓冲的通道（容量约 4096）；后台协程攒够约 200 条或每 2 秒，一个事务批量写入。
- 通道满了就丢弃，计数到 `conloom_usage_dropped_total`；写库失败记 warn 日志，计数到 `conloom_usage_write_errors_total`。
- 进程退出时在限定时间内刷完剩余记录。
- 记录点（代码里一共 4 处模型调用）：
  - `internal/core/runner.go` 的 `chat`：成功和失败都要记；
  - `internal/memory/consolidate.go` 的模型调用；
  - 向量：后台生成（`internal/search/vector/indexer.go`）记为 `embed_index`，检索时的查询（`internal/search/vector/searcher.go`）记为 `embed_query`。
- **归属靠 ctx 传递**：`internal/usage` 提供 `WithAttribution(ctx, bot, convKey, taskID)` 和对应的读取函数。
  - runner 执行任务时把归属放进 ctx，这样工具调用（如 recall 触发的查询向量化）自动继承；
  - indexer 处理每个会话时也把会话放进 ctx；
  - `vector.Embedder` 接口不变，向量装饰器从 ctx 里取归属。
- **向量 token**：现有 `internal/embedding/client.go` 解析响应时不读 `usage`，要改成解析 `usage.prompt_tokens`（没有就记 0），并通过新增的方法或回调交给装饰器，具体由实施者决定。
- 现有的 Prometheus 指标和 `model call` 日志保持不变。

**保留与清理**：新增 `runtime.usage_retention`，默认 400 天（一年多，方便同比）。由现有的每小时清理任务分批删除，并计入 `conloom_cleanup_deleted_total{table="model_calls"}`。

**统计查询**：

- 按 `server.timezone` 的自然日分桶。
- 建议 SQL 先按 UTC 15 分钟聚合，再在 Go 里归并到本地日，这样半点、45 分时区也正确。
- 维度可以选 `model`、`role`、`purpose` 或 `bot`。
- p50/p95 延迟在 Go 里从原始耗时计算，日期范围最多 90 天；token 和次数统计最多 400 天。
- 验收性能：表里共 100 万行（分布在整个保留期），其中 30 天窗口内约 10 万行时，30 天的统计查询（含 p50/p95）在 500ms 内完成。

### 4.2 面板鉴权与外壳

**配置**（新增顶层段，整段省略即不启用面板）：

```yaml
panel:
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
  session_ttl: 168h      # 登录后最长有效期
  idle_timeout: 12h      # 多久不操作就失效
  cookie_secure: false   # 经 HTTPS 反向代理访问时改为 true
```

- 启用时至少要有一个用户，用户名不能重复，哈希必须能解析，迭代次数不低于 100000；不满足就启动失败，报错信息不得包含哈希本身。

**密码哈希**：

- 用 Go 标准库的 `crypto/pbkdf2`，PBKDF2-SHA256，默认 600000 次迭代，16 字节随机盐。不新增依赖。
- 格式为 `pbkdf2-sha256:<迭代次数>:<盐>:<哈希>`，盐和哈希用不带填充的 base64url。分隔符不用 `$`，避免在 `.env` 或 shell 里被当成变量展开。
- 新增子命令 `conloom panel hash-password`：从标准输入读一行密码，输出哈希。

**会话**：

- 迁移 `0009_panel_sessions.sql` 新建 `panel_sessions` 表：
  - `token_hash`：会话令牌的 SHA-256，主键；
  - `user`；
  - `csrf_token`；
  - `cred_fingerprint`：该用户当前密码哈希的 SHA-256 前 16 个十六进制字符；
  - `created_at`、`last_seen_at`、`expires_at`。
- 会话令牌是 32 字节随机数，只放在 cookie 里，数据库只存哈希。
- 每次请求都校验三件事：没过期、没超过空闲时限、指纹与配置里的当前哈希一致。配置里改了密码，旧会话自动失效。`last_seen_at` 最多每分钟更新一次。
- 过期会话由每小时清理任务删除。
- cookie 名 `conloom_panel`，`HttpOnly`，`SameSite=Strict`，`Path=/panel/`，`Secure` 由 `cookie_secure` 决定。

**登录**：

- `POST /panel/api/login`，请求体 `{name, password}`。
- 用户不存在时也要跑一次同等成本的 PBKDF2，防止靠响应时间探测用户名。密码比较用常量时间比较。
- 失败限流：同一来源 IP 15 分钟内失败 5 次，或全局 15 分钟内失败 30 次，就返回 429 并带 `Retry-After`。来源 IP 只取 `RemoteAddr`，不信任 `X-Forwarded-For`。
- 登录成功记一条审计 `panel.login`。失败不写审计，只计数到 `conloom_panel_login_failures_total` 并记 warn 日志；日志里不记录密码，用户名只在它存在时记录。
- `POST /panel/api/logout` 删除会话并清掉 cookie。

**CSRF**：

- 所有 `POST`、`PUT`、`DELETE` 请求（登录除外）必须带 `X-CSRF-Token` 头，值等于本会话的 `csrf_token`，后者通过 `GET /panel/api/session` 获取。
- 请求带了 `Origin` 头时，它的主机部分必须和 `Host` 一致。
- 登录请求也要做这项 `Origin` 检查。

**安全响应头**（所有 `/panel/` 响应）：

- `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: no-referrer`
- 接口响应另加 `Cache-Control: no-store`。

**挂载**：

- 配置了 `panel` 时，在 core 现有的 HTTP 服务上挂 `/panel/`（静态资源）和 `/panel/api/`（JSON 接口，用 cookie 鉴权，和 Bearer token 无关）。
- `/panel` 重定向到 `/panel/`。
- 未登录访问接口返回 401，前端跳到登录页。
- 现有 `/api/v1/*`、`/healthz`、`/metrics` 不变。

**审计**：面板的每个写操作都写 `audit_events`：`actor` 为 `panel:<用户名>`，`bot_instance_id` 为目标会话所属的 bot，`kind` 沿用 API 已有的种类（如 `policy.update`、`memory.update`），API 没有的再新增。

### 4.3 面板接口

所有接口都在 `/panel/api/` 下，响应 JSON，错误格式沿用 `docs/api.md` 的错误体。

- 会话在路径里用会话键（`bot:platform:type:id`）做 URL 编码。面板跨 bot，所以不用 API 那种按 token 绑定 bot 的会话 ID。
- 列表接口统一用游标分页（`before` 加 `limit`），`limit` 默认 50、最大 200。

**读接口**

| 接口 | 内容 |
|---|---|
| `GET session` | 当前用户、`csrf_token`、过期时间 |
| `GET overview` | 版本、提交、启动时间；各 bot（ID、平台、名字、适配器最近一次调用 core 的时间）；实时状态（队列深度、发件箱待发数、向量积压、关键词索引补建剩余、各角色熔断状态）；今天的调用数、失败数、各类 token。适配器最近调用时间由 gateway 在 Bearer 鉴权通过时记在内存里，重启后清空，页面注明"自本次启动"；发件箱待发数没有现成指标，查库得到 |
| `GET usage?from=&to=&group=` | 每日分桶和区间合计：次数、失败数、提示/回复/思考 token、延迟 p50/p95；`group` 取 `model`、`role`、`purpose` 或 `bot` |
| `GET usage/calls?from=&to=&model=&purpose=&conversation=&before=&limit=` | 调用明细，新的在前 |
| `GET metrics/live` | 从本进程的 Prometheus registry 收集的快照：计数器和仪表直接给值；直方图给次数、总和，以及按桶估算的 p50/p95 |
| `GET conversations` | 全部会话：会话键、bot、平台、类型、平台会话 ID、消息数、最后一条消息时间、是否暂停、主动发言开关、记忆开关、人设模板和版本 |
| `GET conversations/{key}` | 单个会话的上述摘要 |
| `GET conversations/{key}/messages?before=&limit=` | 浏览：消息新的在前，按 seq 游标翻页 |
| `GET conversations/{key}/messages?q=&from=&to=&offset=&limit=` | 检索：直接复用 `SearchMessages`，语义与 recall 相同（任一词命中，按相关度排序，同分时新的在前），用 offset 翻页；命中后按 seq 取回消息 |
| `GET conversations/{key}/policy` | 当前策略和历史版本列表 |
| `GET conversations/{key}/persona` | 当前人设、历史版本列表、可用模板名。**必须只读**：`persona.Service.Current` 在会话没有人设时会写入 v1，面板不能调用它；没有人设时返回"尚未生成，将使用模板 X"和模板内容，不写库 |
| `GET conversations/{key}/memory` | 摘要、整理游标、最近错误、成员列表（备注、好感度、是否已忘记） |
| `GET conversations/{key}/memory/revisions?user_id=` | 某成员（不传则为摘要）的修订记录 |
| `GET tasks?status=&bot=&conversation=&before=&limit=` | 任务列表（受现有 30 天保留期限制） |
| `GET tasks/{id}` | 任务详情：meta、生成的动作、该任务的模型调用 |
| `GET audit?kind=&bot=&before=&limit=` | 审计列表 |
| `GET config` | 当前生效配置，打码后输出（见 4.5） |

**写接口**（都要 CSRF 头，都写审计）

| 接口 | 做什么 |
|---|---|
| `PUT conversations/{key}/policy` | 请求体同 API 的 `PolicyPut` |
| `POST conversations/{key}/policy/restore` | 请求体 `{version, reason}`，把该历史版本的策略作为新版本写入 |
| `POST conversations/{key}/pause`、`.../resume` | 暂停或恢复 |
| `PUT conversations/{key}/persona` | 请求体同 API 的 `PersonaPut`（整份替换、回滚到版本、套用模板） |
| `PUT conversations/{key}/memory/members/{user_id}` | 改备注或好感度，规则同 API |
| `DELETE conversations/{key}/memory/members/{user_id}` | 忘记该成员，等同 `/忘记我` |
| `POST conversations/{key}/memory/consolidate` | 触发一次整理 |

**写操作不复制逻辑**：

- 校验、限长、好感度范围必须和现有 API 完全一致。
- policy 和 persona 的校验、版本、审计已经在 `policy.Service`、`persona.Service` 里，面板直接调用，`by` 传 `panel:<用户名>`；暂停和恢复也走 service，会自动写 `policy.update` 审计。
- memory 的业务逻辑目前写在 `internal/gateway/memory.go:93-219` 里，要抽成 gateway 和面板共用的函数（放在 `internal/memory` 或新建 `internal/admin`，由实施者决定），两边只保留 HTTP 解析。抽取时：
  - 审计的 `bot_instance_id` 改成从会话记录取，不再从 token 取；对 API 来说两者本来就相同，所以行为不变；
  - 成员修订记录的来源：API 仍为 `store.RevisionAPI`，面板新增 `panel` 来源；如果数据库对这个字段有取值约束，要同步放开；
  - 触发整理：API 维持现状不写审计；面板写一条新种类 `memory.consolidate`。
- 现有 API 的测试必须继续通过。

**新增的 store 方法**（名字由实施者定）：

- 用量：批量插入、按区间聚合、明细分页、按任务查、分批清理；
- 会话统计：每个会话的消息数和最后消息时间，用一条 GROUP BY 查询，不要逐个会话查；
- 按 seq 取消息（检索命中以后用）；
- 任务分页列表；
- 审计分页列表；
- 人设版本列表；
- 面板会话的增删查和清理。

### 4.4 前端

- 放在 `internal/panel/web/`，用 `go:embed` 打包：`index.html`、`style.css`、若干 ES 模块，比如 `api.js`（带 CSRF 头的 fetch 封装）、`router.js`（hash 路由）、`views/*.js`、`chart.js`（SVG 柱状图和折线图）。不引用任何外部资源。
- 界面中文。宽度小于 700px 时单列显示，手机上也能用。跟随系统深浅色。
- 页面：
  - 登录；
  - 概览（今日用量卡片、7 天柱状图、实时状态、bot 在线情况）；
  - 用量（日期范围可选 7、30、90 天或自定义；分组可选；每日堆叠柱状图；合计表；调用明细）；
  - 会话列表；
  - 会话详情（标签页：消息、策略、人设、记忆、任务）；
  - 任务；
  - 审计；
  - 配置。
- 策略编辑：常用字段做成表单（暂停、主动发言开关、记忆开关、历史条数等，具体由实施者对照 `GroupPolicy` 选），另外提供"高级：JSON 编辑"。服务端返回的校验错误原样展示。
- 人设编辑：`PersonaDoc` 的字段做成表单；有套用模板下拉框，以及带"回滚到此版本"的历史列表。
- 所有用户数据都用 `textContent` 或 DOM API 渲染，禁止把数据拼进 `innerHTML`。
- 静态资源的缓存策略：`index.html` 设 `no-cache`；其他资源用 URL 带版本号或内容哈希，或者统一 `no-cache`，由实施者决定。

### 4.5 配置页打码

- 输出当前生效的配置，环境变量展开以后的值。
- 以下字段显示为"已设置"或"未设置"，不输出值：
  - 所有 token 和 `api_key`；
  - 适配器的 `access_token`、`core_token`；
  - `panel.users[].password_hash`；
  - 将来新增的任何密钥类字段。
- `api_tokens` 只显示名字、所属 bot 和 scope。
- 打码按"白名单输出"实现：只输出明确列出的非敏感字段，新加的字段默认不输出。不用"黑名单"打码，避免以后新加的密钥字段漏打。
- 主机地址（endpoint）对管理员不是秘密，照常显示。

### 4.6 可观测性

新增指标：

- `conloom_usage_dropped_total`
- `conloom_usage_write_errors_total`
- `conloom_panel_login_failures_total`
- `conloom_panel_requests_total{route,code}`：`route` 用路由模板，不含会话键

面板接口不记请求体，也不记消息内容。

## 5. 测试与验收

**自动测试**（全部用假数据）：

- 用量：
  - 记录器在批量、定时刷新、通道满时丢弃并计数、退出时刷完这几种情况下都正确；
  - runner 成功和失败的调用都会落库；记忆整理和向量调用的用途标记正确；
  - 统计按时区正确分日（含半点时区）；
  - 保留期清理正确。
- 鉴权：
  - 未登录访问接口返回 401；密码错返回 401；限流返回 429；
  - 会话过期、空闲超时、改密码后旧会话都会失效；
  - 写请求缺 CSRF 头或头不对返回 403，`Origin` 不匹配返回 403；
  - cookie 属性正确；安全响应头都在。
- 接口：每个读写接口至少一条正常用例；写接口校验失败的用例，和现有 API 用同一组规则；审计写入；分页上限。
- 配置打码：构造一份每个密钥字段都填了特征字符串的配置，断言 `GET config` 的输出里一个特征字符串都不出现。
- 静态资源：都已嵌入，类型正确，`index.html` 里没有内联脚本。
- 现有测试全部通过：`go test -race ./...`、`go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0` 能交叉编译。

**上线验收**（线上实测）：

1. 经 SSH 隧道打开 `http://127.0.0.1:8787/panel/`，未登录会跳到登录页，登录后能进概览。
2. 跟 bot 聊几句后，用量页能看到新的调用和 token；重启 conloom 后数据仍在。
3. 在面板上改一个会话的某个策略字段，用斜杠命令或 API 读回来确认已生效，审计页能看到这条记录。
4. 能浏览消息、任务、审计；配置页没有任何密钥。

## 6. 上线步骤（概要）

1. 备份数据库、配置和二进制。
2. 生成随机初始密码，用 `conloom panel hash-password` 算出哈希，写入 `/opt/conloom/.env`（`CONLOOM_PANEL_ADMIN_HASH`）。明文密码只保存在服务器上一个仅 root 可读的文件里，不进仓库、不进日志。
3. 在线上配置里加 `panel` 段，部署新二进制，重启。迁移 0008、0009 会自动执行。
4. 按第 5 节做上线验收。

## 7. 留给实施者的决定

- 共用写逻辑放在哪个包。
- 前端模块怎么拆分、具体样式、图表细节。
- 策略表单具体包含哪些字段。
- store 新方法的命名和签名。
- 统计查询的具体 SQL，只要满足 4.1 的性能和时区要求。
- 静态资源的缓存方式。

## 8. 实施前审查后补充的决定

以下由设计者拍板（实施前的通读审查提出）：

- 性能验收的 100 万行指整表，30 天窗口约 10 万行；p50/p95 仍在 Go 里算。
- 消息检索沿用 `SearchMessages` 的相关度排序和"任一词命中"，用 offset 翻页；浏览用 seq 游标。两种模式在前端分开展示。
- 向量调用的归属用 ctx 传递，不改 `vector.Embedder` 接口；embedding 客户端改为解析 `usage`。
- 只有 memory 需要抽取共用逻辑；policy、persona 直接复用 service。
- 面板读人设走只读路径，不触发 v1 写入。
- 适配器最近调用时间只在内存里记，重启清空。
