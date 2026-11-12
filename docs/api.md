# HTTP API

核心通过 REST API 对外提供全部能力，适配器（包括 NapCat 和 Mock）也只通过这套 API 和核心交互。请求与响应的 Go 类型见 [`pkg/conloomapi`](../pkg/conloomapi)，Go SDK 见 [`pkg/client`](../pkg/client)。一条消息在核心内部怎么处理，见 [架构](architecture.md#2-一条群消息的完整往返)。

## 鉴权

- 所有 `/api/v1` 请求带 `Authorization: Bearer <token>`，令牌在配置的 `api_tokens` 里定义（见 [配置参考](configuration.md#api_tokens)）。
- 每个令牌绑定一个机器人实例，只能操作该实例下的会话、任务和发件箱；还要具备端点要求的 scope，否则返回 403。
- 核心只保存令牌的 SHA-256 哈希，并用常量时间比较。

## 端点

| 方法与路径 | 所需 scope | 说明 |
|---|---|---|
| `POST /api/v1/events` | `events:write` | 提交事件，必须带 `Idempotency-Key`；返回 202 |
| `GET /api/v1/tasks/{task_id}` | `tasks:read` | 查询任务及其动作 |
| `POST /api/v1/tasks/{task_id}/cancel` | `tasks:read` | 取消排队中或运行中的任务 |
| `GET /api/v1/outbox?limit=&wait=` | `outbox:read` | 拉取待发送动作（长轮询） |
| `POST /api/v1/outbox/{action_id}/ack` | `outbox:ack` | 确认动作的发送结果 |
| `GET /api/v1/capabilities` | 任意有效令牌 | 能力协商 |
| `GET /api/v1/conversations/{id}/persona` | `persona:read` | 当前人格版本 |
| `PUT /api/v1/conversations/{id}/persona` | `policy:write` | 写入新人格版本 |
| `GET /api/v1/conversations/{id}/policy` | `admin:read` | 当前群策略版本 |
| `PUT /api/v1/conversations/{id}/policy` | `policy:write` | 写入新群策略版本 |
| `GET /healthz` | 无 | 健康检查：查一次数据库（2 秒超时），正常返回纯文本 `ok`，失败返回 503 |
| `GET /metrics` | 无 | Prometheus 指标，`server.metrics_enabled: false` 时不注册 |

会话 `{id}` 是标准会话 ID，如 `group_10001`、`private_20001`；核心收到过这个会话的消息后它才存在，在此之前读写它的人格和群策略都返回 404。没有列出的路径返回纯文本 404（不是下面的 JSON 错误格式）。

## 事件

`POST /api/v1/events` 提交一个标准事件（`schema_version` 为 `1.0`，`event_type` 为 `message.created`）：

- 必须带 `Idempotency-Key` 请求头，否则 400。事件里的 `bot_instance_id` 必须和令牌绑定的机器人一致，否则 403。
- 请求体最大 1 MiB，`message.text` 最多 16000 字；会校验 schema 版本、事件类型、会话类型、各个 ID、`occurred_at` 和平台。核心不读取事件里的 `trigger` 和 `actor.roles`，会自己重新判定怎么处理。
- **幂等**：同一个幂等键、同样的请求体重放时，返回首次的响应并带 `Idempotent-Replayed: true`；同一个键、不同的请求体返回 409。同一条平台消息换一个幂等键再提交，同样返回 409。返回 HTTP 错误的请求不留幂等记录，可以用同一个键重试。
- **响应**（202）：`disposition` 为 `reply`、`observe` 或 `ignore`；需要回复时带 `task_id` 和 `status`，并入已有任务时 `merged` 为 true。限流、队列满、暂停等情况**不作为 HTTP 错误**返回，而是在 202 里用 `suppressed_reason` 说明：`rate_limited`、`queue_full`、`paused`、`stale`、`not_enabled`、`private_not_allowed`、`self_message`。

## 任务

- `GET /api/v1/tasks/{task_id}` 返回任务和它产生的动作（`actions` 总是数组）。状态为 `queued`、`running`、`succeeded`、`failed`、`cancelled`。
- 任务的 `trigger` 可能是 `private`、`mention`、`reply`、`command`，还可能是 `continuation`（在聊模式的一轮）或 `proactive`（加入群聊）。后两者只在核心内部产生，不出现在能力协商的 `trigger_types` 里。
- `meta.review_result` 为 `skipped` 表示模型选择不回复或任务在调用模型前被丢弃，为 `blocked` 表示输出被审查拦截；两者都没有动作。
- `POST /api/v1/tasks/{task_id}/cancel` 取消排队中或运行中的任务；正在执行的命令无法取消，只能等它结束。

## 发件箱

- `GET /api/v1/outbox?limit=&wait=`：`limit` 默认 10，取值限制在 1~50；`wait` 是 Go 时长写法（如 `25s`），服务端最长等 30 秒。没有动作时一直等到有新动作、超时或服务关闭，然后返回空列表。
- 每个会话每次只发放最老的一条未确认动作，并加租约（`runtime.outbox_lease`，默认 60 秒）；租约到期还没确认的动作会重新发放。
- `POST /api/v1/outbox/{action_id}/ack`：请求体 `{"status":"sent|unsupported|failed","platform_message_id":"…","error":"…"}`。动作不存在返回 404，重复确认也返回 200。`sent` 时核心把这条回复记为机器人自己的消息。
- 投递语义（至少一次）和残留风险见 [架构 7 节](architecture.md#7-可靠性语义)。

## 能力协商

`GET /api/v1/capabilities` 返回机器人名、`platform_self_id`、当前令牌的 scopes、支持的事件和动作类型（`message.created`、`message.send`）、`trigger_types`、启用的会话列表和私聊策略。NapCat 适配器连接时拉取一次，之后每 5 分钟刷新。

## 人格

`PUT /api/v1/conversations/{id}/persona` 写入新人格版本，请求体三选一：

- `{"persona":{…},"reason":"…"}`：写入完整人格。
- `{"rollback_to":n,"reason":"…"}`：用第 n 版的内容新建一个版本。
- `{"template":"<模板名>","reason":"…"}`：复制配置里的同名模板，版本记下模板名；未知模板返回 400。

人格字段和文本字数限制见 [配置参考：人设模板](configuration.md#人设模板)，超限返回 400。每次写入都记审计。

## 群策略

`PUT /api/v1/conversations/{id}/policy` 写入新群策略版本：`{"policy":{…},"reason":"…"}`。字段同配置里的 `default_group_policy`（见 [配置参考：群策略](configuration.md#群策略)），不合法时返回 400 并列出所有问题。

- 请求体完全不含 `proactive` 段时，保留本群**当前**的主动搭话设置。
- 只要含有 `proactive` 段，它就必须是完整且合法的一段。API 不会把它与当前设置或默认值合并，像 `{"enabled": false}` 这样只写部分字段的请求会被拒绝并返回校验错误。
- 例外：`reply.turn_gap`、`turn_max_wait`、`turn_cooldown`、`max_unprompted_replies`、`max_consecutive_skips`、`quote` 和 `proactive.join_*` 在请求里缺少时逐字段按默认值补齐，方便旧的调用方继续使用；已移除的 `proactive.contact_cooldown` 出现在请求里会被忽略。
- 只想关闭主动搭话时，管理员可以在群里发 `/主动 全群 关`：它以当前版本为基础写入一个新版本，只改 `proactive.enabled`（`开` 同理），可以用 `GET …/policy` 查看结果。
- 把 `concurrency.max_interactive_requests` 设为 0 等同于 `/pause`：本群正在生成和排队的回复会被静默取消。

## 错误格式

所有 JSON 错误响应统一为：

```json
{"error":{"code":"FORBIDDEN","message":"…","retryable":false,"retry_after_ms":0,"trace_id":"trace_…"}}
```

| 状态码 | `code` | 场景 |
|---|---|---|
| 400 | `INVALID_EVENT` | 请求不合法，包括人格、群策略校验失败 |
| 401 | `UNAUTHORIZED` | 缺少或无效的令牌 |
| 403 | `FORBIDDEN` | 缺少 scope，或事件的机器人与令牌不一致 |
| 404 | `NOT_FOUND` | 任务、动作或会话不存在 |
| 409 | `DUPLICATE_EVENT` | 幂等键重复但请求体不同，或平台消息已经提交过 |
| 503 | `INTERNAL_ERROR`，`retryable: true` | 服务正在关闭，可以稍后重试 |
| 500 | `INTERNAL_ERROR` | 其他内部错误，细节只写日志，可用 `trace_id` 查找 |

`retry_after_ms` 目前总是 0。`RATE_LIMITED`、`QUEUE_FULL` 等错误码有定义，但不会作为 HTTP 错误返回（见上文 `suppressed_reason`）。Go SDK 把网络错误、5xx、429 和 `retryable: true` 视为可重试。
