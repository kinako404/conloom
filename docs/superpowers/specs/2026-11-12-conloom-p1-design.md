# ConLoom 织语 P1 Core MVP 设计（Go 实现）

日期：2026-11-12
上位文档：《织语 ConLoom 开发设计与可行性方案 v2.0》（下称“总设计”，不随仓库公开；本文中的 §编号指该文档章节）

## 1. 意图

按总设计实现 ConLoom，但技术栈由 Python／FastAPI 改为 Go，代码托管在 GitHub 公开仓库 `kinako404/conloom`。本轮只做总设计 §16 的 **P1：ConLoom Core MVP**。

用户的明确要求：

- 使用 Go 开发，开公开 GitHub 仓库。
- 安装要方便、跨平台。
- 性能好，有并发能力。
- 模型通过用户的 NewAPI（OpenAI 兼容网关）接入。
- 首个真实平台是 NapCat（OneBot 11，正向 WebSocket），只在指定的一个 QQ 群启用，并有一名管理员。

P1 的退出条件（总设计 §16）：真实平台可持续聊天，无自回复循环，故障不造成重复消息。

## 2. 目标、非目标、反目标

### 2.1 目标（P1 必须交付）

1. 标准 Bot Interaction API（REST）：事件提交、任务查询与取消、Outbox 拉取与 ACK、能力协商、人格与群策略的读写。
2. MockAdapter（测试与本地交互）与 NapCat Adapter（真实平台），两者都只通过公开 API 与核心交互。
3. 幂等事件、持久 Outbox、至少一次投递＋Adapter 端按 `action_id` 去重。
4. 会话隔离与会话内顺序；回复策略（触发、冷却、短消息合并）；自回复防护。
5. 近期上下文 + 基础人格（不可变核心人格 + 群人格模板/实例/版本），按总设计 §6.2 自然表达规则组织提示词。
6. Model Profile + Role Router（`short_context`／`long_context`），全部由配置决定，代码零厂商绑定。
7. 应用层并发准入：Profile 全局并发、群并发（0/1/2）、token 预留预算、优先级队列、队列上限。
8. 基础审查（确定性规则，不可关闭）。
9. 可观测：结构化日志、trace_id、Prometheus 指标。
10. 跨平台单文件分发：Linux／macOS／Windows × amd64／arm64，无 cgo、无外部服务依赖。
11. 测试：单元测试 + 进程内集成测试（假模型服务、假 OneBot 服务）+ 一次真机端到端验证。

### 2.2 非目标（P1 不做，留给后续阶段）

- 混合检索、向量、滚动摘要、长期事实、显式 `/remember` `/forget`、分层遗忘（P2）。
- 人格观察与自动演化（P2）；P1 只有手动修改与版本回滚。
- 图片理解与原图存储（见 §9.4，P1 只做占位）。
- Tool Gateway、Web 搜索、ComfyUI（P3）。
- PostgreSQL／pgvector／Redis 实现（P2 引入，P1 通过接口预留）。
- 多实例水平扩展；P1 是单实例核心 + 任意数量 Adapter 进程。
- 自由插话、多 Agent、流式输出。
- Python SDK（总设计 §18.2 的 `conloom-sdk-python` 改为 Go SDK `pkg/client`）。

### 2.3 反目标（做到了也算失败）

- Bot 在有大量真人的群里乱插话、刷屏、回复自己，或在重启／断线后重复发消息。
- 任何密钥、NapCat token、群号、管理员 QQ 号进入公开仓库（包括测试数据、示例配置、提交历史）。
- 核心编排器出现 OneBot 字段或模型名称。
- 需要用户先装数据库／Redis／Docker 才能跑起来。
- 模型输出可以注入平台控制码（如 CQ 码 `@全体成员`）。

## 3. 约束与已知环境

- 本机：Go 1.26.4；无 PostgreSQL、Redis、Docker；gh 已登录 `kinako404`（有 `repo`、`workflow` 权限）。
- NewAPI：OpenAI 兼容，`/v1/models` 列出 9 个模型；实测 `qwen3.8-27b` 与 `deepseek-flash` 可用。两者都会产生 reasoning tokens（`qwen3.8-27b` 一句问候 47 个输出 token 中 38 个是推理），返回 `content` 可能带前导空行。
- NapCat：4.18.42，OneBot v11，正向 WS 端口 3001，带 access token；Bot QQ 已登录；目标群可读。
- 实际地址、token、群号、管理员 QQ 已由用户在对话中提供，实施时只写入本地被 `.gitignore` 排除的 `conloom.yaml`／`.env`。
- `deepseek-v4-pro` 尚未实测，实施初期先验证；不可用时 long_context 改用 `deepseek-flash`。

## 4. 方案取舍

考虑过的三种做法：

| 方案 | 内容 | 取舍 |
|---|---|---|
| **A（采用）** | 单仓库；存储接口 + SQLite（纯 Go 驱动）；锁/限流/租约进程内实现；Adapter 独立进程走 REST | 零外部服务、单文件跨平台、重启可恢复；P2 需新增 PG/Redis 实现，但都在接口后面 |
| B | 一开始就 PostgreSQL + pgvector + Redis | 与总设计完全一致，但部署门槛高，P1 用不到向量 |
| C | Adapter 内嵌核心进程 | 最简单，但不验证 REST/Outbox 与平台无关性 |

用户要求“方便跨平台安装、性能好、有并发能力”，并把选择交给实现方，因此采用 A。性能与并发来自：Go 原生并发、每会话串行执行单元、有界工作池、无忙轮询的 Outbox 长轮询、SQLite WAL（单写连接 + 读连接池）。SQLite 的写吞吐对单实例 P1 足够；需要多实例或向量检索时切 PostgreSQL。

## 5. 总体结构

### 5.1 进程与分发

一个二进制 `conloom`，多个子命令：

```text
conloom serve   -c conloom.yaml            核心服务（API + 编排 + Worker）
conloom adapter napcat -c conloom.yaml     NapCat Adapter（独立进程，可部署在别的机器）
conloom mock    -c conloom.yaml            交互式 MockAdapter（终端里模拟私聊/群聊）
conloom version
```

各子命令读取同一配置文件的不同段落；Adapter 也可只用命令行参数/环境变量运行。构建全部 `CGO_ENABLED=0`。

### 5.2 仓库布局（实现方可微调）

```text
cmd/conloom/                 子命令入口
pkg/conloomapi/              公开类型：Event、Action、Task、错误契约、能力
pkg/client/                  Go SDK（Adapter 只依赖它与 conloomapi）
internal/config/             YAML 配置、${ENV} 展开、校验
internal/gateway/            HTTP 路由、鉴权与权限范围、错误输出、幂等
internal/core/               编排器：Reply Policy、Session Manager、合并、命令
internal/persona/            核心人格、模板、实例、版本、提示词渲染
internal/contextbuilder/     上下文组装与预算
internal/router/             Model Profile、Role Router、健康与熔断
internal/provider/openai/    OpenAI 兼容客户端
internal/admission/          并发槽位、token 预留、优先级队列、租约
internal/review/             审查流水线（基础规则）
internal/outbox/             出站动作、租约、长轮询通知
internal/store/              存储接口
internal/store/sqlite/       SQLite 实现 + 内嵌迁移
internal/adapter/napcat/     OneBot 11 ↔ 标准 Event/Action
internal/adapter/mock/       MockAdapter
internal/obs/                日志、trace、指标
docs/                        总设计、本设计、部署说明
```

### 5.3 在线时序（P1 版）

1. Adapter 把平台消息转为标准 Event，`POST /api/v1/events`，带 `Idempotency-Key`。
2. Gateway 校验 token 权限、Schema、幂等键；丢弃 Bot 自身消息。
3. 消息先落库，再由 Reply Policy 判定 `reply`／`observe`／`ignore`。
4. `reply`：进入该会话的串行执行单元，等待合并窗口收集同一发送者的连续短消息。
5. 加载人格版本、群策略、近期消息，组装上下文并估算 token。
6. Role Router 选 Profile；准入控制放行后调用模型。
7. 输出经基础审查与确定性后处理，写入 Outbox；通知长轮询中的 Adapter。
8. Adapter 拉取、发送、ACK；核心记录 Bot 回复消息与来源关系。

## 6. Bot Interaction API

### 6.1 端点（P1）

```text
POST /api/v1/events                         events:write
GET  /api/v1/tasks/{task_id}                tasks:read
POST /api/v1/tasks/{task_id}/cancel         tasks:read（仅限本 Adapter 绑定的 Bot 实例）
GET  /api/v1/outbox?limit=&wait=            outbox:read（长轮询，wait 最长 30s）
POST /api/v1/outbox/{action_id}/ack         outbox:ack
GET  /api/v1/capabilities                   任意有效 token
GET  /api/v1/conversations/{id}/persona     persona:read
PUT  /api/v1/conversations/{id}/persona     policy:write
GET  /api/v1/conversations/{id}/policy      admin:read
PUT  /api/v1/conversations/{id}/policy      policy:write
GET  /healthz   GET /metrics                无鉴权（metrics 可配置关闭或绑定本地地址）
```

总设计中的 `tasks/{id}/events`、memories、compact、stats、tool-jobs、tools 属于后续阶段，P1 返回 404。

### 6.2 鉴权

- 配置中声明 API token：每个 token 有名称、权限范围列表、绑定的 `bot_instance_id`。token 只存 SHA-256 哈希也可接受（实现方决定），但配置文件里允许 `${ENV}` 引用明文。
- 普通 Adapter token 只有 `events:write tasks:read outbox:read outbox:ack persona:read`；管理 token 另配。
- 一个 token 只能操作其绑定 Bot 实例下的会话、任务和 Outbox；越权返回 `FORBIDDEN`。

### 6.3 事件与动作

- 字段严格采用总设计 §3.2／§3.3 的 JSON 结构，`schema_version: "1.0"`。
- 触发类型 `private`、`mention`、`reply`、`command`、`observe`、`manual`：由 Adapter 依据平台语义预判填写，但**核心重新判定**（例如核心检查 `mentions` 中是否有本 Bot、`reply_to` 是否指向 Bot 消息），不盲信 Adapter。
- 附件：P1 接受 `attachments`（类型、URL、名称、大小），只保存元数据。
- 平台特有数据只进 `extensions` 与原始事件存档，核心不读取。

`POST /events` 响应（202）：

```json
{"event_id":"evt_…","disposition":"reply","task_id":"task_…","status":"queued"}
```

`disposition` 取 `reply`／`observe`／`ignore`；非 `reply` 时无 `task_id`。

响应语义（实现方的决定）：

- HTTP 错误只用于请求本身有问题：`UNAUTHORIZED`、`FORBIDDEN`、`INVALID_EVENT`、`DUPLICATE_EVENT`（同键异体）、`INTERNAL_ERROR`。这些响应**不**写入幂等记录，Adapter 可以用同一键重试。
- 请求合法但不生成回复的情况一律 202，并带 `suppressed_reason`：用户限流（`rate_limited`）、会话队列满（`queue_full`）、群暂停（`paused`）、私聊未对该用户开放（`private_not_allowed`）。前三种 `disposition` 为 `observe`（消息照常落库）；未启用会话与私聊未开放为 `ignore`（不落库）。
- 被并入同一发送者已有合并窗口的消息：`disposition: reply`，`task_id` 为已有任务，附 `merged: true`。
- 所有 202 响应都写入幂等记录，重放返回同一响应体。

ID 与引用（实现方的决定）：

- 标准 ID 由 **Adapter** 依据平台 ID 确定性生成，同一平台对象永远得到同一标准 ID：`conversation.id = group_<群号>`／`private_<QQ号>`，`actor.id = user_<QQ号>`，`message.id = msg_<平台消息ID>`；`event_id` 也由 Adapter 生成（`evt_<平台消息ID>` 或 ULID），响应原样回显。
- `platform_conversation_id`、`platform_user_id`、`platform_message_id`（新增于 `message` 中）同时携带平台原始值。
- `reply_to`、`mentions[].id` 使用**标准 ID**；@Bot 时 `mentions` 为 `{"type":"bot","id":"<bot_instance_id>"}`。
- Outbox ACK 携带 `platform_message_id`，核心按同一规则生成 `msg_<平台消息ID>` 记录 Bot 自己的消息，从而在后续 `reply_to` 指向它时识别为“回复 Bot”。
- 配置中的启用会话、管理员列表、Bot 自身 ID 一律写**平台原始 ID**；核心按平台原始字段匹配。

### 6.4 幂等

- 幂等键在 `bot_instance_id` 范围内唯一，保留至少 7 天。
- 同键同内容重放：返回首次的响应体，并带响应头 `Idempotent-Replayed: true`。
- 同键不同内容（请求体哈希不同）：`409 DUPLICATE_EVENT`。
- 平台消息另有唯一索引（`bot_instance_id, platform, conversation, platform_message_id`），防止 Adapter 换键重发。

### 6.5 Outbox

- 动作状态：`pending → leased → acked`；另有 `expired`、`cancelled`。
- `GET /outbox`：返回该 Bot 实例的 `pending` 动作，并将其置为 `leased`，租约默认 60s；租约过期未 ACK 则回到 `pending` 重新投递。
- 顺序：同一会话内按动作创建顺序投递，且该会话已有 `leased` 动作时不再发放它的下一条，直到前一条 ACK 或租约过期。不同会话之间互不阻塞。
- P1 假设每个 Bot 实例只有一个 Adapter 进程；上面的会话内顺序规则同时保证了多进程时也不会乱序。
- 长轮询：无动作时挂起直到有新动作或 `wait` 到期；靠进程内通知唤醒，不忙轮询。
- ACK 幂等：重复 ACK 返回 200。ACK 可携带平台消息 ID，核心用它把 Bot 回复与平台消息关联（供“回复 Bot”触发识别）。
- 超过 `action_ttl`（默认 10 分钟）仍未投递的动作置 `expired`，不再发送过时回复。
- Adapter 端按 `action_id` 去重：维护本地持久化的已发送记录（小型追加日志或 SQLite 文件，保留最近 N 条/24 小时），先查记录再发送，发送成功后立刻写记录再 ACK。重复风险只剩“平台发送成功到写记录之间崩溃”的毫秒级窗口，文档中如实说明。

### 6.6 错误契约

完全按总设计 §13.2 的 JSON 结构与 P1 相关错误码：`UNAUTHORIZED`、`FORBIDDEN`、`INVALID_EVENT`、`DUPLICATE_EVENT`、`RATE_LIMITED`、`QUEUE_FULL`、`CONTEXT_TOO_LARGE`、`CONTENT_BLOCKED`、`ATTACHMENT_UNSUPPORTED`、`MODEL_UNAVAILABLE`、`TASK_CANCELLED`、`INTERNAL_ERROR`。

## 7. 会话、回复策略与命令

### 7.1 会话与身份

- `conversation_key = bot_instance_id:platform:conversation_type:conversation_id`。
- 群聊一条共享时间线；每条消息记录发送者平台 ID、当时显示名、回复目标、@ 列表、时间。
- 身份主键是平台用户 ID，昵称变化只更新显示名。

### 7.2 启用范围

- 配置 `enabled_conversations`：P1 只启用一个群；不在名单内的群，Adapter 不转发、核心也拒收（`ignore`，不落库），不保存其消息。
- 私聊：默认只对配置中的管理员开放（便于测试），可配置为全部开放或关闭。这是实现方的保守选择。

### 7.3 回复判定

- 私聊（已开放）：回复。
- 群聊：被 @、回复 Bot 的消息、以命令前缀（默认 `/`）开头且为已知命令 → 回复；其余 `observe`，只落库。
- 受控连续对话：Bot 回复某用户后，该用户在 `continuation_window` 内的下一条消息也视为触发。**默认关闭**（窗口为 0），配置可开。
- 自由插话：P1 不实现。
- 自回复防护：核心依据 Bot 实例配置的平台自身 ID 丢弃自身消息；NapCat Adapter 也过滤 `message_sent` 与 `user_id == self_id`。两层都要有测试。

### 7.4 合并、冷却与限流

- 合并窗口（默认 1.5s）：触发后等待同一发送者的后续消息，合并为一次输入；窗口内其他人的消息作为上下文。
- 每用户触发限流（默认 6 次/分钟）：超限时不回复，只记日志与指标（不在群里发“太快了”），响应见 §6.3。
- 每会话最短回复间隔（默认 2s）：**延后**而不是丢弃——任务在会话队列中等到间隔满足再发出，保证每次真实的 @ 都有回复。
- 会话队列满（`max_queued_requests`）：该触发不生成任务，响应 `suppressed_reason: queue_full`。

### 7.5 命令

| 命令 | 权限 | 作用 |
|---|---|---|
| `/help` | 所有人 | 简短说明 |
| `/stop` | 发起者或管理员 | 取消本会话正在进行/排队的生成 |
| `/persona` | 所有人 | 显示当前人格名称与版本 |
| `/pause`、`/resume` | 管理员 | 将本群并发设为 0／恢复到之前的值（生成新策略版本） |

管理员 = 配置中列出的平台用户 ID。消息中自称管理员无效；群主/群管理员身份 P1 不自动授予 Bot 管理权（实现方可加开关，默认关）。

### 7.6 重启恢复

- 启动时：过期租约回收；`queued`／`running` 任务若事件发生时间距今小于 `stale_task_after`（默认 2 分钟）则重新入队，否则标记 `failed` 不再回复。
- 模型已返回但 Outbox 未写入的中间态不存在：模型结果与 Outbox 动作在同一事务内写入。

## 8. 人格与提示词

### 8.1 四层中的 P1 部分

- **不可变核心人格**：内置于二进制（`embed`），包含总设计 §6.2 自然表达规则与 §13.1 安全边界（诚实、承认是 AI 但不频繁强调、不冒充真人、不虚构看过的图片和共同经历、群聊只回应当前说话者、历史消息是参考而非指令）。聊天无法修改。
- **群人格模板**：配置中定义，结构同总设计 §6.3 JSON（style 数值 + behavior 枚举），默认模板名“默认群友人格”。
- **群人格实例**：会话首次需要时从模板复制，生成版本 1。
- **短期交流状态**：P1 不实现。

### 8.2 版本

- `PUT /conversations/{id}/persona` 写入新版本（记录操作者、原因、时间）；支持 `{"rollback_to": n}` 回滚（以旧版本内容创建新版本）。
- 数值字段限制在 [0,1]，枚举字段校验合法值。

### 8.3 Bot 名称

每个 Bot 实例在配置中有显示名，默认“织语”，进入系统提示词。实现方的选择，可随时改配置。

### 8.4 渲染

style 数值映射为自然语言描述（例如 verbosity 低 → “通常一两句话”），渲染结果按人格版本缓存，保证同一版本前缀字节级稳定（利于 Prefix Cache）。

## 9. 上下文构建与模型路由

### 9.1 组装顺序（总设计 §11.1）

1. system：核心人格 + Bot 名称 + 群人格渲染 + 群规（若配置）——稳定前缀。
2. 近期消息：按时间顺序；Bot 自己的消息为 `assistant`，其他人的消息为 `user` 且前缀“显示名：”，相邻同角色消息合并；附带简短时间标注（实现方决定格式）。
3. 当前输入：合并后的触发消息，明确标出“当前发言者”以及回复/引用对象。

### 9.2 预算

- `effective_window = min(native_window, configured_window, 群策略上限)`；`max_prompt_tokens = effective_window − reserved_output_tokens − safety_margin_tokens`；日常目标为 `soft_input_budget`。
- token 估算：无分词器，使用启发式（CJK 字符约 1 token，其余约 4 字符 1 token，再乘可配置系数），收到模型 `usage` 后在日志/指标中记录估算误差，便于后续校准。
- 近期消息从最新往回装，直到软预算；最多 `max_recent_messages`（默认 50）。超出时丢弃最旧的非关键消息。永不裁掉核心规则、当前输入、回复对象。

### 9.3 路由（P1）

1. 按模态、隐私（会话标记 vs Profile 的 `allow_sensitive_content`）、群策略、健康状态筛选可用 Profile。
2. 能在 `short_context` 的软预算内装下 → short。
3. 当前输入本身就超出 short 的 `max_prompt_tokens`，而 long 能装下 → long。
4. 都不行 → `CONTEXT_TOO_LARGE`（Bot 回复一句无法处理过长内容）。分块处理留给 P2。
5. 健康：连续失败 N 次（默认 3）熔断该 Profile 30s；short 熔断时若隐私允许降级到 long，否则回复一句暂时不可用。
6. 每次调用记录 `selected_model_role`、`route_reason`、实际模型名。

### 9.4 图片占位

收到图片时，上下文中以 `[图片]` 占位，并在核心人格里要求：没看到图片内容时不得声称看到了。图片理解属于后续阶段。

### 9.5 Provider（OpenAI 兼容）

- `POST {endpoint}/chat/completions`，非流式，带超时（默认 60s）与有限重试（只对网络错误与 5xx/429，最多 2 次，指数退避）。
- Profile 支持 `extra_body`（原样合并进请求体），用于关闭思考等厂商参数；实现时应实测 NewAPI 是否支持对 `qwen3.8-27b` 关闭思考，以降低延迟和 token 消耗。
- 输出后处理：去掉首尾空白与 `<think>…</think>` 段；`reasoning_content` 字段忽略；空输出视为失败。
- 注意：推理 token 计入 `max_tokens`，`reserved_output_tokens` 需留足（参考配置 short 设 1024 起）。

### 9.6 默认 Profile 配置（写在本地配置与示例中）

| 角色 | 模型 | 窗口参考 | 隐私 |
|---|---|---|---|
| short_context | `qwen3.8-27b` | native 32768，soft 16000 | 允许敏感（经用户自己的网关） |
| long_context | `deepseek-v4-pro`（`deepseek-flash` 备选） | native 1000000，soft 120000 | 允许敏感 |

两个 endpoint 都是用户的 NewAPI。示例配置中只出现占位变量，不出现真实地址与密钥。

## 10. 并发与准入

- **会话执行单元**：每个活跃会话一个调度 goroutine + 有界队列，空闲一段时间后回收；负责按触发顺序把任务出队、处理合并窗口与最短回复间隔。
- **群并发**：`max_interactive_requests` 0（暂停：照常收消息落库、不生成）／1（默认，串行）／2。为 2 时，调度器最多同时放出两个任务：
  - 每个任务的上下文只包含开始时已落库的消息，不包含另一个尚未完成的回复；
  - 两个回复各自带 `reply_to_message_id` 指向自己的触发消息，因此按**完成顺序**写入 Outbox 即可，不强求按触发顺序发送；
  - Outbox 的会话内顺序规则（§6.5）照常生效。
- **Profile 全局并发**：每个 Profile 一个信号量（`runtime.max_concurrency`）。
- **token 预留**：若 Profile 配置了 `gpu_kv_cache_tokens`，则 `reservation = prompt + max_output + safety_margin` 必须放得进剩余预算才放行；未配置则跳过。
- **实际并发** = `min(群配置, Profile 全局, 预留预算, 可用槽位)`。
- **优先级**：在线交互 > 用户重试 > 后台（P1 暂无后台任务，但队列结构支持）；低优先级设最大等待时间防饥饿。
- **租约**：每个放行请求持有带请求 ID 与过期时间的租约，完成、失败、取消都释放；过期自动回收。P1 进程内实现，接口与 P2 的 Redis 实现一致。
- **取消**：`/stop` 与 cancel API 通过 context 取消正在进行的模型调用。

## 11. 审查（基础档，不可关闭）

- 输入：长度上限（超长截断进上下文并告知模型被截断）、控制字符清理。
- 上下文：历史消息、引用消息一律以“参考信息”身份放入，不作为系统指令。
- 输出：长度上限；去掉模型可能生成的平台控制标记（Adapter 一律以纯文本消息段发送，核心也剥离 `[CQ:` 形式的文本以防万一）；密钥形态（`sk-…`、Bearer、长 hex/base64 token）与配置中的任意密钥值打码；可配置屏蔽词。
- 动作：`allow`／`sanitize`／`block`（阻断时不发送，记录审计；P1 不做重生成）。
- `standard`／`strict` 档：P1 配置可接受，但行为等同基础档并在启动日志中提示“尚未实现模型复核”。

## 12. 存储（SQLite）

- 驱动：纯 Go（如 `modernc.org/sqlite`）；WAL、`busy_timeout`、外键开启；一个写连接 + 读连接池。
- 迁移：SQL 文件内嵌二进制，启动时按版本号顺序执行，记录在 `schema_migrations`。
- P1 表：`bot_instances`、`users`、`conversations`、`messages`、`events`（幂等键、请求哈希、响应体、原始事件）、`tasks`、`outbox_actions`、`persona_templates`、`persona_instances`、`persona_versions`、`group_policies`（版本化）、`audit_events`。
- 关键唯一约束：幂等键、平台消息、`action_id`、人格版本号、策略版本号。
- 所有查询带 `bot_instance_id` 等作用域条件。
- 存储层以接口暴露，P2 加 PostgreSQL 实现时编排层不改。
- 原始消息 P1 不做遗忘清理，但保存 `created_at` 以便 P2 实施保留策略。

## 13. NapCat Adapter

- 正向 WebSocket 连接 NapCat（URL 与 access token 来自配置），断线指数退避重连；心跳超时判定断线。
- 启动时 `get_login_info` 获得 self_id；`message`（group/private）事件转标准 Event：
  - 消息段：`text` 拼文本；`at` 转 mentions（self_id → bot 类型）；`reply` 转 `reply_to`；`image` 转附件元数据；`face` 等转简短占位文本；其余忽略。
  - 发送者 `card` 优先于 `nickname` 作显示名；`sender.role` 放入 actor.roles。
  - 幂等键：`napcat:{self_id}:{message_id}`。
- 不在启用名单内的会话不转发；`self_id` 发出的消息不转发。
- 出站：长轮询 Outbox → `send_group_msg`／`send_private_msg`，消息体使用**消息段数组**（`text` + 可选 `reply` 段），从不使用 CQ 字符串；拿到平台 message_id 后 ACK。
- 不支持的动作类型（P1 只实现 `message.send`；`typing.*` 等）安全忽略并 ACK 为 `unsupported`。
- 向核心提交失败时有限重试（同一幂等键），核心不可用时在内存中有界缓冲。

## 14. 可观测性

- 日志：`log/slog` JSON；每个任务一个 `trace_id`；默认**不记录**消息正文，只记 ID、长度、会话、路由、耗时；`log_message_content: true` 可在本地调试时打开。
- 指标（`/metrics`，Prometheus 文本格式）：事件数（按 disposition）、任务数（按状态）、模型调用耗时与 token（按角色/模型）、估算误差、排队时间、队列长度、准入拒绝、Outbox 投递/重试/过期、审查结果、熔断状态。

## 15. 配置

- 单一 YAML，支持 `${ENV}` 与 `${ENV:-default}` 展开；启动时严格校验并给出可读错误。
- 仓库提供 `conloom.example.yaml`（全部占位）；真实配置 `conloom.yaml` 与 `.env` 被 `.gitignore` 排除。
- 主要段落：`server`、`storage`、`api_tokens`、`bots`（id、平台、显示名、平台自身 ID、管理员列表、启用会话、私聊策略）、`model_profiles`、`persona_templates`、`default_group_policy`、`adapters.napcat`、`adapters.mock`、`observability`。
- 群策略 P1 字段：`concurrency`（max_interactive_requests、max_queued_requests、priority、allow_long_requests）、`reply`（command_prefix、continuation_window、debounce、min_reply_interval、user_rate_limit）、`context`（max_recent_messages、max_context_tokens 上限）、`review`（level、review_input、review_output、action、blocked_words）。配置给默认值，API/命令修改后存库并版本化，库内版本优先。

## 16. 测试与验收

### 16.1 自动化（`go test ./...`，CI 必跑，含 `-race`）

- API 合约：Schema 校验、错误结构、权限范围、跨 Bot 实例越权。
- 幂等：重复事件、同键异体、重复 Outbox 拉取、重复 ACK、租约过期重投、Adapter 端去重 → 均不产生重复发送。
- 自回复：核心与 Adapter 两层过滤。
- 回复策略：私聊、@、回复 Bot、命令、普通观察、未启用群、连续对话窗口、合并窗口、限流。
- 并发：群并发 0/1/2；不能突破 Profile 全局上限；队列满；取消。
- 路由：软预算/配置窗口/原生窗口/输出预留组合；short 装不下转 long；都装不下 `CONTEXT_TOO_LARGE`；熔断与隐私限制下的降级。
- 重启恢复：任务与 Outbox 在进程重启后正确恢复或过期。
- 审查：控制码剥离、密钥打码、屏蔽词。
- NapCat Adapter：对假 OneBot WS 服务测试消息段转换、自身消息过滤、断线重连、发送与 ACK。
- 配置替换：仅改配置即可换模型（测试用两个假 Profile）。
- 基准：事件接入与 Outbox 往返的 `go test -bench`，结果写入 README（不预先承诺数字）。

### 16.2 真机端到端（P1 退出条件）

在启用的群中由管理员配合完成，并记录结果：

1. @Bot 提问，得到自然、简短、对题的回复；不 @ 的普通消息不回复。
2. 回复 Bot 的消息继续对话，能利用近期上下文。
3. 连续快速发两三条短消息，只得到一次合并回复。
4. `/persona`、`/help`、`/stop` 生效；管理员 `/pause` 后不再回复，`/resume` 恢复。
5. 生成过程中杀掉并重启 Adapter，再杀掉并重启核心：不出现重复消息、不回复自己、不发过时回复。

### 16.3 交付物

- 公开仓库 `kinako404/conloom`，MIT 许可证：代码、`README`（中文，含安装、配置、运行、迁移、架构简图）、`conloom.example.yaml`、本设计文档与实施计划。总设计原文**不**进入仓库。
- GitHub Actions：`go vet`、`go test -race`、六个平台交叉编译。
- 发布：打 tag 时用 GoReleaser 产出各平台压缩包与校验和（实现方选择，便于“方便安装”）。
- 测试运行实例：在本机以 systemd 服务运行核心与 NapCat Adapter（崩溃自动重启），连接真实群，完成 §16.2，之后保持测试运行。开发期间只在真机验证环节才连群。

### 16.4 可迁移性

用户要求测试运行之后能方便迁移到别的机器：

- 全部状态在一个数据目录（SQLite 数据库 + Adapter 去重记录），全部配置在一个 YAML + 可选 `.env`；二进制无其他依赖。
- 提供 `conloom backup -o <file>`：在服务运行中用 SQLite 在线备份（`VACUUM INTO` 或备份 API）导出一致快照。
- README 写明迁移步骤：新机器下载对应平台二进制 → 拷贝配置与备份 → 停旧实例 → 启新实例；以及 systemd（Linux）、launchd（macOS）、Windows 服务或计划任务的示例。
- 核心与 Adapter 可分别迁移：Adapter 只需能访问 NapCat 与核心 API 地址。

## 17. 已定事项与留给实现方的部分

用户已定：Go；公开 GitHub 仓库，MIT 许可证；总设计原文不公开；P1 范围；NewAPI 作为模型网关；NapCat 正向 WS 作为首个真实平台；只启用一个群；一名管理员；“方便跨平台安装 + 性能好/有并发能力”，具体方案交给实现方；交付后在本机测试运行，且要方便后续迁移。

实现方（本设计作者）已定、可被用户否决：方案 A（SQLite + 进程内锁 + 独立 Adapter 进程）；单二进制多子命令；短/长 Profile 的模型映射；Bot 默认名“织语”；私聊默认只对管理员开放；连续对话默认关闭；合并/限流/租约/过期等默认数值；最短回复间隔延后而非丢弃；`POST /events` 的 202/错误划分与 ID 生成规则（§6.3）；群并发 2 时按完成顺序入 Outbox（§10）；`standard`/`strict` 审查档在 P1 等同基础档；GoReleaser 发布；systemd 测试部署。

留给实现方：具体包内结构、ID 生成方式、token 估算系数、时间标注格式、style 数值到自然语言的映射措辞、Adapter 本地去重记录的存储形式、指标命名、Go 最低版本（不必追最新，能用即可）。
