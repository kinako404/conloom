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
  # listen: 0.0.0.0:8788 # 面板单独的端口，见下文“用单独端口直接访问”
```

- 密码哈希用 `conloom panel hash-password` 生成，写进 `.env`，步骤见 [部署：Web 管理面板](deployment.md#web-管理面板)。字段说明见 [配置参考：panel](configuration.md#panel)。
- 默认面板和 API 共用 `server.listen` 这一个端口：`/panel/` 是页面，`/panel/api/` 是页面用的 JSON 接口，`/panel` 会跳转到 `/panel/`。设了 `panel.listen` 时面板改到那个单独的端口，见 [用单独端口直接访问](#用单独端口直接访问)。
- 面板用自己的登录会话（cookie）鉴权，和 `api_tokens` 的 Bearer 令牌无关。现有的 `/api/v1/*`、`/healthz`、`/metrics` 行为不变。
- 可以配置多个账号，权限都一样，都是管理员。

## 访问

核心默认只监听本机（`127.0.0.1:8787`）。从自己的电脑访问服务器上的面板，最简单的是 SSH 隧道：

```sh
ssh -L 8787:127.0.0.1:8787 <用户>@<服务器>
```

保持这个连接，在本机浏览器打开 `http://127.0.0.1:8787/panel/`。本机 8787 端口被占用时换一个本地端口，例如 `-L 18787:127.0.0.1:8787`，然后打开 `http://127.0.0.1:18787/panel/`。

需要不经 SSH 直接从公网访问时，推荐用反向代理负责 HTTPS，并把 `panel.cookie_secure` 改为 `true`，示例见 [部署：经 HTTPS 反向代理访问](deployment.md#经-https-反向代理访问)。不要把 `server.listen` 直接改成公网地址：那样连 `/metrics`（不需要鉴权）和 API 一起暴露出去，面板也没有 HTTPS。只想开放面板时用下面的 `panel.listen`。

### 用单独端口直接访问

`panel.listen` 让核心多开一个只提供面板的端口，`server.listen` 保持只监听本机：

```yaml
panel:
  listen: 0.0.0.0:8788
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

重启后在浏览器打开 `http://<服务器地址>:8788/panel/`（直接打开 `http://<服务器地址>:8788/` 会跳转过去）。启动日志里有一行 `panel listening`，带实际监听的地址。

- 这个端口上只有面板：`/api/v1/*`、`/healthz`、`/metrics` 都返回 404，它们仍然只在 `server.listen` 上。
- 设了 `panel.listen` 后，`server.listen` 上不再有 `/panel/`（返回 404）。用 SSH 隧道时转发面板的端口，例如 `ssh -L 8788:127.0.0.1:8788 <用户>@<服务器>`，再打开 `http://127.0.0.1:8788/panel/`。
- `panel.listen` 不能和 `server.listen` 是同一个端口；端口被占用时核心启动失败。
- 这个端口上每个请求（含请求体）必须在 30 秒内读完，否则连接被断开，免得慢速客户端长期占着连接。

**安全警告**：面板自己不提供 HTTPS。经普通 HTTP 从公网访问时，登录密码和会话 cookie 都以明文经过网络，路上的任何人（同一 Wi-Fi、运营商、中间节点）都可能看到并冒用，拿到 cookie 就等于拿到管理员权限。

- 推荐在前面放 HTTPS 反向代理，并设 `cookie_secure: true`，做法见 [部署：经 HTTPS 反向代理访问](deployment.md#经-https-反向代理访问)。这时让 `panel.listen` 只监听本机（如 `127.0.0.1:8788`），代理转发到这个端口。
- 至少要用防火墙只放行自己的 IP 访问这个端口，见 [部署：用单独端口直接访问](deployment.md#用单独端口直接访问)。
- 登录限流有一个全局上限（全部来源合计 15 分钟内失败 30 次，见下文 [登录与安全](#登录与安全)）。端口对整个互联网开放时，任何人乱试密码都能让这个上限用尽，在最多 15 分钟内谁都无法登录（包括你自己）；已经登录的会话不受影响，可以继续使用。

## 页面

界面是中文，跟随系统的深浅色，窄屏（手机）上单列显示。未登录时任何页面都会跳到登录页；使用中途会话过期时，登录后回到原来的页面。

- **概览**：今天（`server.timezone` 的当地日）的调用数、失败数和各类 token，最近 7 天按模型的 token 柱状图，版本和启动时间，实时状态，以及各 bot 的适配器最近一次调用核心的时间。每 10 秒刷新一次。
  - 实时状态包括准入队列深度、发件箱待发数、向量积压、关键词索引补建剩余、熔断中的角色。没有配置 `embedding` 段时向量积压显示“未启用”（接口里是 `null`）。
  - “最近调用”是绑定这个 bot 的 API 令牌最近一次通过鉴权的时间（通常就是它的适配器），只记在内存里，从本次进程启动起算：核心刚重启、适配器还没来得及调用时显示“自本次启动未调用”。
  - 页面下方的“进程指标”是本进程 Prometheus 指标的快照，重启后清零；需要长期曲线时请另接 Prometheus。
- **用量**：按日期范围（最近 7、30、90 天或自定义）和分组（模型、角色、用途、bot）统计每天的调用数、失败数和 token，画每日堆叠柱状图，下面是区间合计表和逐次调用明细。统计规则见下文 [模型用量](#模型用量)。
- **会话**：所有 bot 的全部群聊和私聊，带消息数、最后一条消息的时间、是否暂停、主动发言和记忆开关、人设模板和版本。右上角的“新建聊天”在面板里和 bot 开一个私聊，见下文 [在面板里聊天](#在面板里聊天)。点进一个会话有以下标签页：
  - **聊天**：只有私聊有。以这个会话的用户身份和 bot 聊天，见下文 [在面板里聊天](#在面板里聊天)。面板里新建的聊天默认打开这一页，其他会话默认打开“消息”。
  - **消息**：按时间倒序浏览，从面板发出或送到面板的消息带“面板”标记；也可以输入关键词检索，规则和 `recall` 的关键词检索相同（任一词命中，按相关度排序，同分时新的在前），可以限定日期。
  - **策略**：常用字段做成了表单（主动发言、记忆、历史条数、分条上限 `reply.max_parts`；私聊会话还有“私聊等待” `reply.private_wait` 和“私聊最长等待” `reply.private_max_wait`，写成 `6s`、`500ms`、`1m` 这样的时长，两项都填 0 表示用默认值），另有“高级：JSON 编辑”可以整份替换；保存时服务端校验，不合法的值会在表单下方显示服务端给出的错误；暂停、恢复；历史版本列表，可以把任一版本恢复为新版本。字段含义见 [使用指南：私聊的节奏](usage.md#私聊的节奏)。
  - **人设**：编辑人设的各个字段、套用配置里的模板、回滚到历史版本。会话还没有人设时只显示“尚未生成”和将要使用的模板，查看不会生成第 1 版。
    - “内容尺度”一栏有“未成年角色”和“成年人设”两个开关，以及“内容规则”“记忆规则”两个文本框，对应人设的 `minor`、`adult`、`content_rules`、`memory_rules`。勾选“未成年角色”后，“成年人设”会被取消并且不可用；服务端同样拒绝两者同时为真。两段规则只在开放模式（成年、非未成年、私聊）下生效，规则见 [配置：内容模式](configuration.md#内容模式)。
    - 页面顶部显示“私聊内容模式”：当前人设（还没有人设时是将要使用的模板）放在私聊里会是开放还是保护模式。群聊不论人设如何都是保护模式。
  - **记忆**：摘要、整理进度和最近的错误，成员的备注和好感度，可以修改备注或好感度、忘记某个成员、立即整理一次，也可以查看某个成员（或摘要）的修订记录。这里的数据和 Bearer API 的 `GET /api/v1/conversations/{id}/memory` 完全相同：被忘记的成员没有备注和好感度，不会出现在列表里。
    - 编辑成员时，好感度输入框下方有“-5”“+5”“+10”“设为 100”四个快捷按钮：按已保存的好感度（还没有时从初始值 20 起算）计算新值，限制在 0–100，点击后立即保存，只改好感度；原因取“原因”框的内容，留空时记为“快捷调整 +5”这样的文字。备注或好感度框里有未保存的修改时先弹窗确认（这些修改会丢失），取消则不保存。保存过程中四个按钮都不可用。结果已经是当前值时只提示“好感度已经是 N”，不写入。
  - **任务**：这个会话的任务。
- **任务**：全部任务，可以按状态、bot、会话筛选；任务详情里有 meta、生成的动作，以及这个任务的每次模型调用。任务受 `runtime.ops_retention`（默认 30 天）清理，更早的查不到。
- **审计**：审计记录，可以按种类和 bot 筛选，受 `runtime.audit_retention`（默认 90 天）清理。
- **配置**：当前生效的配置（环境变量展开以后），只读。见下文 [配置页打码](#配置页打码)。

所有用户数据（消息、昵称、人设、审计内容等）都按纯文本显示，里面的 HTML 不会被执行。

### 在面板里聊天

面板可以直接和 bot 私聊，有两种用法：

- **接管已有私聊**：在一个平台私聊的“聊天”标签里发消息，发言人是这个会话的用户（最近一条用户消息的平台用户 ID 和昵称）；会话里还没有用户消息时不能发，返回 400。
- **新建聊天**：在会话列表点“新建聊天”，选择 bot、填写 bot 对你的称呼（留空时用面板用户名，最长 32 字），提交后打开新会话的“聊天”标签。新会话的平台是 `panel`，会话键形如 `qq-main:panel:private:panel_<随机>`，发言人 ID 是 `panel_<面板用户名>`。这个发言人是该 bot 的管理员，可以用管理员命令；私聊白名单和 `private_chat` 开关对 `panel` 平台不生效。

两种用法的共同点：

- 消息和平台发来的消息走同一套流程：输入审查、限流、私聊的等待节奏都照常生效。消息长度 1 到 2000 字，只能在私聊里发，群聊返回 400。
- 从面板发出的消息记为 `panel` 通道，带“面板”标记。由它触发的回复也走 `panel` 通道：只在面板里显示，不会发到原平台，适配器拉取发件箱时也拿不到。之后从原平台发来的消息照常在原平台回复，上下文里包括在面板里聊过的内容。面板消息和平台消息在等待期间合并成同一个任务时，以最新一条消息定通道：平台消息之后紧接着在面板里发一句，这次回复就只出现在面板里。
- “聊天”标签打开时显示最近 50 条消息，之后每 1.5 秒拉一次新消息，离开页面就停止。bot 自己的消息在左，用户的在右。会话有排队中或运行中的任务，或者面板通道还有没送达的分条时，显示“对方正在输入…”。
- 回车发送，Shift+回车换行。发送失败时显示服务端返回的错误，输入框里的内容保留。

面板里新建的聊天详情页有“删除这个聊天”按钮，确认后删除这个会话键下的全部数据（消息、任务、动作、记忆、人设、策略等），只保留用量统计（`model_calls`）和审计记录。平台上的会话不能在面板里删除。

### 面板上的修改

面板上的修改和 Bearer API、聊天命令走同一套代码，校验规则、字数上限、好感度范围完全一致，校验失败时原样显示服务端返回的错误。

- 每次修改都写一条审计记录，`actor` 为 `panel:<用户名>`，`bot_instance_id` 是会话所属的 bot。种类沿用 API 的 `policy.update`、`persona.update`、`memory.update`、`memory.forget`；触发整理写 `memory.consolidate`（API 触发整理不写审计）。登录成功写 `panel.login`。
- 面板聊天的审计：发消息写 `panel.message`（内容只有消息 ID，不含正文），新建聊天写 `panel.chat.create`，删除聊天写 `panel.chat.delete`；目标都是会话键。
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
- **不拖慢回复**：记录先放进内存队列（4096 条），后台攒够 200 条或每 2 秒一次批量写库。队列满时丢弃并计入 `conloom_usage_dropped_total`，写库失败记 warn 日志并计入 `conloom_usage_write_errors_total`；退出时最多等 5 秒把剩下的写完，超时没写完的丢弃并记 warn 日志，不影响正常退出。原有的 `model call` 日志和 `conloom_model_*` 指标不变。
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
- 任务列表、审计记录、调用明细（`usage/calls`）和消息浏览用 `before` 游标加 `limit` 翻页；响应里的 `next_before` 是下一页的 `before`，最后一页为 `null`。消息检索改用 `offset` 翻页（见下表）。`limit` 都是默认 50、最大 200。会话列表不分页，一次返回全部会话。

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
| `GET conversations` | 全部会话 `conversations`，以及配置里的 bot 列表 `bots`（`id`、`name`、`platform`，“新建聊天”从这里选） |
| `GET conversations/{key}` | 单个会话 |
| `GET conversations/{key}/messages?before=&limit=` | 浏览消息，按 `seq` 游标翻页。每条消息带 `channel`：`panel` 表示从面板发出或送到面板，平台上的消息为空字符串 |
| `GET conversations/{key}/messages?q=&from=&to=&offset=&limit=` | 检索消息，按 `offset` 翻页，响应里的 `next_offset` 是下一页 |
| `GET conversations/{key}/chat?after=` | 面板聊天的增量拉取：`seq` 大于 `after`（默认 0）的消息，从旧到新，每次最多 200 条，格式同消息浏览；`typing` 为 true 表示会话有排队中或运行中的任务，或面板通道还有没送达的分条；`next_after` 是下一次的 `after`（没有新消息时等于本次的 `after`） |
| `GET conversations/{key}/policy` | 当前策略和历史版本 |
| `GET conversations/{key}/persona` | 当前人设、历史版本、可用模板；还没有人设时给出将要使用的模板，不写库。`minor` 是当前人设（没有时是该模板）的 `minor`，`content_mode` 是它在私聊里的内容模式（`open` 或 `protected`），与会话本身是群聊还是私聊无关 |
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
| `POST conversations/{key}/messages` | 面板聊天发消息，请求 `{"text"}`，返回 202 `{"message_id"}`。只限私聊；群聊、文本为空或超过 2000 字、接管的会话还没有用户消息时返回 400 |
| `POST chats` | 新建面板聊天，请求 `{"bot_instance_id","display_name"}`，返回 201 `{"key"}`；bot 没有配置时返回 404 |
| `DELETE conversations/{key}` | 删除面板聊天，返回 204；不是 `panel` 平台的会话返回 400 |

## 指标

面板和用量记录新增的 Prometheus 指标（`/metrics`）：

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_usage_dropped_total` | — | 队列满或正在退出时丢弃的用量记录 |
| `conloom_usage_write_errors_total` | — | 用量记录写库失败的次数 |
| `conloom_panel_login_failures_total` | — | 面板登录失败（密码错或用户不存在）的次数 |
| `conloom_panel_requests_total` | `route`（路由模板，不含会话键）、`code` | 面板的请求数 |
