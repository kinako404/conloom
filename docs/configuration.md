# 配置参考

ConLoom 只用一个 YAML 配置文件（加可选的 `.env`）。完整的带注释示例见仓库根目录的 [`conloom.example.yaml`](../conloom.example.yaml)，本文按段说明各字段、默认值和取值范围。群聊参与相关字段的行为见 [使用指南](usage.md#机器人在群里怎么说话)。

## 文件、变量与校验

- 所有子命令都用 `-c <文件>` 指定配置，默认 `conloom.yaml`。真实的 `conloom.yaml` 和 `.env` 已被 `.gitignore` 排除，不要提交它们。
- **变量展开**：支持 `${VAR}` 和 `${VAR:-默认值}`，`$$` 表示字面量 `$`。变量先从进程环境变量取，其次取配置文件**同目录**的 `.env`；`.env` 不会覆盖已经存在的进程环境变量。没有设置、也没有默认值的变量展开为空串，如果它导致某个必填字段为空，启动时的错误信息会列出这些没设置的变量。
- **严格解析**：未知字段会被拒绝。`bots[]` 的每一项、`model_profiles` 的每一项分别在各自的默认值上解析：省略的字段取默认值，显式写出的零值保留。
- **按子命令校验**：`serve`、`adapter napcat`、`adapter telegram`、`mock` 只校验各自用到的字段，出错时一次列出全部问题并给出字段路径，退出码为 78（配置错误；命令行用法错误是 2）。所以不用的适配器段（比如只接 QQ 时的 `adapters.telegram`）即使变量没设置也不影响其他子命令。
- 配置里的群号、QQ 号一律写平台原始 ID。

## 示例配置使用的环境变量

`conloom.example.yaml` 把所有地址、密钥和 ID 都放在变量里，建议写进 `.env`（`chmod 600 .env`）：

| 变量 | 用途 |
|---|---|
| `CONLOOM_ADAPTER_TOKEN` | 适配器调用核心 API 的令牌（随机长字符串） |
| `CONLOOM_ADMIN_TOKEN` | 管理令牌（另一个随机长字符串） |
| `CONLOOM_BOT_SELF_ID` | 机器人自己的 QQ 号 |
| `CONLOOM_ADMIN_QQ` | 管理员的 QQ 号 |
| `CONLOOM_GROUP_ID` | 启用的群号 |
| `CONLOOM_SHORT_MODEL_ENDPOINT` / `_NAME` / `_KEY` | 短上下文模型的接口地址、模型名、API Key |
| `CONLOOM_LONG_MODEL_ENDPOINT` / `_NAME` / `_KEY` | 长上下文模型的接口地址、模型名、API Key |
| `CONLOOM_NAPCAT_WS_URL` | NapCat 的 WebSocket 地址，如 `ws://<NapCat 地址>:<端口>` |
| `CONLOOM_NAPCAT_TOKEN` | NapCat 的 access token |
| `CONLOOM_PANEL_ADMIN_HASH` | Web 管理面板 `admin` 账号的密码哈希，启用 `panel` 段时才需要，生成方法见 [部署：Web 管理面板](deployment.md#web-管理面板) |

模型请求发往 `<endpoint>/chat/completions`，所以 endpoint 一般写到 `/v1` 为止。

## 顶层结构

| 段落 | 说明 |
|---|---|
| `server` | 监听地址、时区、指标开关 |
| `storage` | 数据目录 |
| `api_tokens` | API 令牌及其权限范围 |
| `bots` | 机器人实例 |
| `model_profiles` | 模型配置，按角色 |
| `persona_templates` | 人设模板 |
| `default_group_policy` | 新会话的初始群策略 |
| `default_private_policy` | 私聊会话的初始策略，省略时沿用 `default_group_policy` |
| `runtime` | 投递、过时判定、合并窗口等运行时参数 |
| `skills` | 额外技能文件所在的目录 |
| `embedding` | 语义检索用的向量服务，可选 |
| `panel` | Web 管理面板的账号和会话，可选 |
| `adapters` | NapCat、Telegram 和 Mock 适配器 |
| `observability` | 日志 |

## server、storage

| 字段 | 默认 | 说明 |
|---|---|---|
| `server.listen` | `127.0.0.1:8787` | 核心的监听地址，`host:port` 形式，API、`/healthz`、`/metrics` 共用；Web 管理面板默认也在这里，设了 [`panel.listen`](#panel) 时改到那个地址 |
| `server.timezone` | `Asia/Shanghai` | 上下文里时间的显示时区，也是主动搭话每人每日上限和面板用量统计的日界线；时区数据已内嵌在二进制里 |
| `server.metrics_enabled` | `true` | 是否开放 `/metrics` |
| `storage.data_dir` | `./data` | 数据目录，相对路径按配置文件所在目录解析；数据库固定为其中的 `conloom.db` |

## api_tokens

每项包含 `name`、`token`、`bot_instance_id`、`scopes`。令牌只能操作它绑定的机器人实例；`token` 必填且不能重复，`name` 不能重复，scope 必须是已知值。

| scope | 用途 |
|---|---|
| `events:write` | 提交事件 |
| `tasks:read` | 查询、取消任务 |
| `outbox:read`、`outbox:ack` | 拉取、确认发件箱动作 |
| `persona:read` | 读取人格 |
| `policy:write` | 写入人格和群策略 |
| `admin:read` | 读取群策略 |
| `memory:read` | 读取群聊或私聊的摘要、整理进度和群友记忆 |
| `memory:write` | 修改、删除群友记忆，立即整理一次记忆 |

适配器令牌通常只需要 `events:write tasks:read outbox:read outbox:ack persona:read`；管理令牌另加 `policy:write admin:read`，要通过 API 查看或管理记忆时再加 `memory:read memory:write`（示例配置已经加上）。各端点需要的 scope 见 [HTTP API](api.md)。

## bots

| 字段 | 默认 | 说明 |
|---|---|---|
| `id` | 必填 | 机器人实例 ID，不能重复 |
| `platform` | 必填 | 平台名，示例为 `qq` |
| `name` | `织语` | 显示名；人设设置了角色名时，在该会话里以角色名为准 |
| `platform_self_id` | 必填 | 机器人自己的平台 ID，用于自回复防护 |
| `admins` | 空 | 管理员的平台用户 ID 列表，每项不能为空 |
| `enabled_conversations` | 空 | 只在这些会话里工作，每项 `{type: group\|private, platform_id: "<ID>"}`；未列出的群消息会被忽略。`private` 项的 `platform_id` 填用户的平台 ID，只在 `private_chat: allowlist` 时生效，用来放行这些人的私聊；其他档位下会被忽略，启动时打一条警告日志 |
| `private_chat` | `admins` | 私聊策略：`admins`（仅管理员）、`allowlist`（管理员和 `enabled_conversations` 里列为 `private` 的人）、`all`（所有人）、`off`（关闭）。不被允许的私聊不回复 |
| `persona_template` | `default` | 会话初始人格使用的模板名，必须存在于 `persona_templates` |
| `skills` | 空 | 启用的技能名列表，如 `[memory, time]`，不能有空项或重复；必须是内置技能或 `skills.dir` 里的技能，它们用到的工具都必须存在，否则启动失败（退出码 78）。省略或为空时不带任何工具，行为和没有技能时一样。见 [技能](skills.md) |

## model_profiles

按角色配置模型。只有 `short_context`（必需）和 `long_context`（可选）两个名字会被路由使用：短上下文模型装得下就用它，装不下且群策略允许时用长上下文模型。路由与熔断细节见 [架构 5.3 节](architecture.md#53-路由和熔断)。

| 字段 | 默认 | 说明 |
|---|---|---|
| `provider` | `openai_compatible` | 目前唯一支持的值 |
| `endpoint`、`model`、`api_key` | `endpoint`、`model` 必填 | 接口地址、模型名、密钥 |
| `modalities` | 空 | 模型支持的模态，如 `[text]` |
| `context.native_window` / `configured_window` / `soft_input_budget` | 必填，均 > 0 | 必须满足 `soft_input_budget <= configured_window <= native_window`；软预算决定历史最多装多少 |
| `context.reserved_output_tokens` / `safety_margin_tokens` | 0 | 输出预留（大于 0 时也作为请求的 `max_tokens`，推理 token 也算在内）和安全余量 |
| `runtime.max_concurrency` | 2 | 该模型的全局并发上限，≥ 1 |
| `runtime.timeout` | 60s | 单次请求超时，> 0 |
| `runtime.max_retries` | 2 | 网络错误、429、5xx 的重试次数，0~5 |
| `runtime.gpu_kv_cache_tokens` | 0 | 准入控制的 token 预算，0 表示不限 |
| `runtime.cpu_offloaded_kv_tokens`、`runtime.prefix_caching` | — | 可以写，目前不生效 |
| `privacy.allow_sensitive_content` | false | 私聊只会路由到为 `true` 的模型 |
| `privacy.data_location` | — | 说明性标签（如 `remote` 表示数据会离开本机），代码不读取 |
| `extra_body` | 空 | 原样合并进请求体，例如 `enable_thinking: false`；它可以覆盖 `model`、`messages` 等字段，请谨慎使用 |
| `token_estimate_factor` | 1.0 | token 估算系数，≥ 0，0 视为 1 |

## 人设模板

`persona_templates.<名字>` 定义一个人设模板，机器人通过 `bots[].persona_template` 引用。省略 `default` 时使用内置的默认人格。使用方法见 [使用指南：人设模板](usage.md#人设模板)。

| 字段 | 限制 | 说明 |
|---|---|---|
| `persona_name` | 必填 | 人格名称，`/persona` 会显示 |
| `template_version` | — | 模板自身的版本号 |
| `style.warmth` / `humor` / `directness` / `verbosity` / `emoji_usage` / `proactivity` | 各 0~1 | 风格数值，渲染成三档中文描述 |
| `behavior.default_reply_length` | `short` \| `medium` \| `long` | 默认回复长度 |
| `behavior.allow_memes` | 布尔 | 是否接梗 |
| `behavior.ask_followup_questions` | `never` \| `occasionally` \| `often` | 追问频率 |
| `behavior.technical_explanations` | `brief` \| `clear_and_practical` \| `detailed` | 技术解释的详细程度 |
| `character_name` | ≤20 字，单行 | 角色名。设置后就是机器人在使用这个人格的会话里的名字：system prompt 的“你是{名字}”、上下文里的“你（名字）”、回复的回声清洗和 `/help` 都改用它；不设时用 `bots[].name` |
| `description` | ≤2000 字 | 角色设定，渲染为“角色设定：”一段 |
| `speaking_style` | ≤1000 字 | 说话方式，渲染为“说话方式：”一段 |
| `examples` | ≤8 组；每组 `user`、`reply` 非空且各 ≤200 字 | 示例对话，渲染为“群友：… / 你：…”，并注明只示范语气和分寸、不要照搬 |

`character_name` 以下四个文本字段都可以省略。字数按字符计（去掉首尾空白后），超出时启动校验和 `PUT …/persona` 都会拒绝并指出字段。文本可以用 YAML 多行块（`|`）书写，示例见 `conloom.example.yaml` 中的原创角色模板 `librarian`。

## 群策略

`default_group_policy` 是新会话的初始群策略。通过 API 或聊天命令修改后，策略按会话存库并版本化，库里的版本优先；没有存库版本的会话一直使用配置里的默认值。策略分 `concurrency`、`reply`、`context`、`review`、`proactive`、`memory` 六段。

```yaml
default_group_policy:
  concurrency:
    max_interactive_requests: 1   # 本群同时执行的回复数，0~2；0 表示暂停
    max_queued_requests: 10       # 本群排队上限，>= 1
    priority: normal              # low | normal | high；会校验，目前不生效
    allow_long_requests: true     # 是否允许使用长上下文模型
  reply:
    command_prefix: /
    continuation_window: 0s       # 在聊会话保持时间，0~10m；0 表示不开启在聊模式，只回直接对话
    debounce: 1500ms              # 直接对话合并同一人连续消息的防抖，0~1m
    min_reply_interval: 2s        # 本群上一个回复结束到下一个开始的最短间隔，0~1m
    user_rate_limit_per_minute: 6 # 每人每分钟最多触发几次回复，>= 1
    turn_gap: 6s                  # 在聊时等对方停顿多久再处理这一轮，1s~60s
    turn_max_wait: 20s            # 一轮从第一句算起最多等多久，turn_gap~2m
    turn_cooldown: 45s            # 接话冷却，1s~10m；continuation_window 不为 0 时必须大于它
    max_unprompted_replies: 4     # 两次被直接点名之间最多几次未点名回复，达到后会话结束，1~20
    max_consecutive_skips: 2      # 连续几轮没回后会话结束，1~10
    quote: auto                   # 群里回复何时引用原消息：auto（必要时）| always | never
  context:
    max_recent_messages: 50       # 上下文最多带多少条历史消息，0~500
    max_context_tokens: 0         # 本群的上下文窗口上限，0 表示不额外限制
  review:
    level: basic                  # basic | standard | strict；目前三者行为相同
    review_input: true            # 目前不生效，基础审查始终开启
    review_output: true           # 同上
    action: block                 # 命中屏蔽词时：allow（只记录）| sanitize（替换为 *）| block（整条拦截）
    blocked_words: []
  proactive:
    enabled: true                 # 群总开关（观望模式）
    default_level: low            # 获得资格时的默认档位：off | low | mid | high
    eligibility_ttl: 720h         # 多久没 @ 机器人就失去资格（30 天），1h~8760h
    user_cooldown: 30m            # 对同一人两次主动搭话的最短间隔，0~24h
    group_hourly_limit: 4         # 全群每小时主动发言上限，0~60
    daily_limits: {low: 2, mid: 4, high: 8}   # 每人每天主动发言上限，各 0~100，且 low <= mid <= high
    join_lull: 15s                # 群里安静多久算一次停顿，5s~5m
    join_window: 5m               # 加入评估看最近多长时间的对话，1m~30m
    join_min_messages: 4          # 窗口内至少多少条非机器人消息，2~50
    join_evaluations_per_hour: 4  # 每群每小时最多几次加入评估（即模型调用），1~60
    join_cooldown: 10m            # 机器人上次在本群发言后多久之内不加入，1m~24h
  memory:
    enabled: false                # 是否开启记忆（群聊摘要、群友笔记、好感度）；默认关闭，开启时要求 concurrency.allow_long_requests: true
    affinity: true                # 开启记忆时是否计算好感度
    consolidate_every: 150        # 新消息达到这么多条就整理一次，30~2000
    consolidate_interval: 6h      # 新消息不足条数时，距上次整理超过这么久且至少有 consolidate_min 条新消息也整理一次，30m~48h
    consolidate_min: 20           # 按时间整理时至少要有的新消息条数，1~2000
    backlog: 1000                 # 第一次开启时往回整理多少条旧消息，0~5000
```

各字段的具体行为见 [使用指南](usage.md#机器人在群里怎么说话)，汇总表见 [架构 4.9 节](architecture.md#49-阈值一览)。

**私聊默认策略**：`default_private_policy` 的结构和 `default_group_policy` 相同，私聊会话没有存库版本时用它，库里已有版本时仍以库里为准；整段省略时私聊沿用 `default_group_policy`。私聊只有机器人和一个人，适合开启记忆、带更长的历史。示例配置的建议值：

```yaml
default_private_policy:
  context:
    max_recent_messages: 200
  memory:
    enabled: true
    consolidate_every: 40
    consolidate_interval: 1h
    consolidate_min: 6
```

私聊的记忆、`/记住` 等用法见 [使用指南：记忆与好感度](usage.md#记忆与好感度)。

**审查**：基础审查不可关闭。触发消息会清理控制字符并限制长度（4000 字）；模型输出会剥离平台控制码（如 `[CQ:…]`）、对密钥打码、按 `review.action` 处理屏蔽词，并截断到 1500 字。被拦截的回复不发送，只写一条不含正文的审计记录。详见 [架构 5.4 节](architecture.md#54-输出处理链)。

**缺省与兼容规则**：

- YAML 配置里省略的任何字段（或省略整个 `proactive` 段）都保持上面的默认值。
- 已存库的策略版本缺少 `proactive` 段时，读取时按默认值补齐，即主动搭话默认开启、档位默认为低。
- 已存库的版本或 API 请求里缺少 `reply.turn_gap`、`turn_max_wait`、`turn_cooldown`、`max_unprompted_replies`、`max_consecutive_skips`、`quote` 或 `proactive.join_*` 时，逐字段按默认值补齐（`quote` 补为 `auto`，`turn_cooldown` 补为 45s）；这些字段的零值本身都不合法，所以不会覆盖有意的设置。补齐用的是会话类型对应的默认策略：群聊用 `default_group_policy`，私聊用 `default_private_policy`（没配置时也是 `default_group_policy`）。
- `continuation_window` 不补，旧版本里的 `0s` 仍表示不开启在聊模式；它不为 0 时必须大于 `turn_cooldown`，否则校验失败（否则挂起的一轮会在会话超时后才放行，然后被丢弃）。
- 已存库的策略版本缺少整个 `memory` 段时，读取时用会话类型对应的默认策略里的 `memory` 补齐，即群聊记忆按配置默认关闭，私聊按 `default_private_policy.memory`。
- `memory` 段内缺少的键取内置默认值（`affinity: true`、`consolidate_every: 150`、`consolidate_interval: 6h`、`consolidate_min: 20`、`backlog: 1000`，`enabled` 为 `false`）；显式写出的 `0` 和 `false` 保留。加入 `consolidate_min` 之前存库的版本读出来是 20，和原来写死的条数一致。
- 群策略 API 对 `proactive` 和 `memory` 段的处理不同于 YAML，见 [API：群策略](api.md#群策略)。
- `default_private_policy` 段里省略的字段同样取上面的内置默认值（不是 `default_group_policy` 的值），校验规则也一样；整段省略时，私聊会话直接沿用 `default_group_policy`。
- `proactive.contact_cooldown` 已移除：已存库的旧版本里的这个键在读取时被忽略；但配置文件不接受未知字段，升级后要从 `conloom.yaml` 里删掉这一行，否则启动时报错。

## runtime

| 字段 | 默认 | 说明 |
|---|---|---|
| `action_ttl` | 10m | 超过这个时间还没投递的回复动作过期，不再发送 |
| `outbox_lease` | 60s | 适配器拉取动作后的租约时长，超时未确认会重新投递 |
| `stale_task_after` | 2m | 事件发生时间超过这个时长的消息不再回复；重启恢复时超过的任务记为过时 |
| `idempotency_retention` | 168h | 事件幂等记录的保留时长 |
| `max_merge_window` | 4.5s | 直接对话合并连续消息的最长窗口 |
| `session_idle` | 5m | 调度会话（每个会话的任务队列）空闲多久后回收，最小 1m |
| `ops_retention` | 720h | 已结束（成功、失败、取消）的任务和它们已确认或已过期的发件箱动作的保留时长，24h~8760h。实际保留时长取它和 `idempotency_retention` 中较长的一个，保证重放的事件响应里的 `task_id` 仍能查到 |
| `audit_retention` | 2160h | 审计记录（策略和人格修改、审查拦截、面板登录和修改）的保留时长，24h~87600h |
| `usage_retention` | 9600h | 模型用量明细（`model_calls` 表，每次模型调用一行）的保留时长，默认 400 天；面板的用量统计最长也是 400 天，调短后更早的日子显示为 0。体积见 [Web 管理面板：模型用量](panel.md#模型用量) |

`ops_retention` 和 `audit_retention` 会在启动时校验范围，超出范围（包括显式写 0）时启动失败，退出码 78；省略时取默认值。其余字段不做范围校验，≤ 0 时回落到默认值。不要把 `action_ttl` 配到 24 小时以上：适配器的去重记录只保留 24 小时，超过后可能重发。

清理在核心的维护循环里执行：启动时一次，之后每小时一次。每张表每个事务最多删 1000 行，每轮最多 10 个事务，删不完的留到下一轮，所以不会长时间占用数据库写锁。每轮在日志里记一行 `cleaned up operational records`（各表删除条数，`more=true` 表示还有没删完的），删除条数计入指标 `conloom_cleanup_deleted_total{table}`。哪些表会被清理、哪些永远保留，见 [部署：数据目录与权限](deployment.md#数据目录与权限)。

## skills

| 字段 | 默认 | 说明 |
|---|---|---|
| `skills.dir` | 空 | 额外技能的目录，相对路径按配置文件所在目录解析。目录里每个 `<名字>/SKILL.md` 与内置技能同名时替换它，否则作为新技能加入。留空只用内置技能（`memory`、`time`） |

目录不存在、子目录里没有 `SKILL.md`、技能文件格式不对时，`serve` 启动失败，退出码 78。技能文件格式和启用方式见 [技能](skills.md)。

## embedding

可选。配置后，核心在后台把记忆已生效的会话切成段，为每段生成向量，供 `recall` 的语义检索使用（和关键词检索的结果融合，见 [技能：recall](skills.md#recall)）；初次开启时从每个会话的第一条消息回溯全部历史，每段约 4KB，量级见 [部署：向量服务](deployment.md#向量服务)；整段省略时不做语义检索，也不会请求任何向量服务。工作方式见 [架构 5.7 节](architecture.md#57-长期回忆的检索)，部署向量服务见 [部署：向量服务](deployment.md#向量服务)。

| 字段 | 默认 | 说明 |
|---|---|---|
| `endpoint` | 必填 | OpenAI 兼容的向量接口地址，`http://` 或 `https://`；请求发往 `<endpoint>/embeddings`，所以一般写到 `/v1` 为止 |
| `model` | `bge-m3` | 向量模型名，不能为空。改了以后后台按会话重新生成全部向量，新向量补齐之前，旧模型的向量不参与检索；全部补齐后删除旧向量。**换向量服务时也要改这个名字**（哪怕新服务上的模型同名），这个名字会原样发给服务，所以要在新服务上给模型起一个新名字（Ollama 可以用 `ollama cp bge-m3 bge-m3-b`），否则新旧向量会被当成同一个模型：后台发现向量维数和库里不一致时会重建这些段并记一条 Warn 日志，但维数相同、算法不同的向量没法发现 |
| `api_key` | 空 | 有值时以 `Authorization: Bearer` 发送；日志和错误信息里不会出现，审查也会把它从回复里抹掉 |
| `timeout` | 10s | 后台生成向量时单次请求的超时，> 0 且 ≤ 5m。检索时把查询句转成向量另有 2 秒的固定超时 |
| `batch_size` | 16 | 一次请求最多带几段，1~256 |
| `min_score` | 0.48 | 语义命中的最低余弦相似度，0~1，低于它的段不算命中；另外比本次最高分低 0.05 以上的段也不算（见 [技能：recall](skills.md#recall)）。都不命中时 recall 只用关键词结果。默认值是用线上 bge-m3 校准的：相关查询的正确段约 0.50–0.63，不相关的句对大多低于 0.46（第 90 百分位 0.461）。调低会混进不相关的段，调高会漏掉换了说法的记录；0 表示不设门槛。**换模型后要重新校准**，不同模型的分数分布不一样 |

```yaml
embedding:
  endpoint: http://127.0.0.1:11434/v1
  model: bge-m3
```

## panel

可选。配置后，核心在 `server.listen` 的同一端口上（设了 `listen` 时改在这个单独的地址上）提供 Web 管理面板 `/panel/`；整段省略（或写成空的 `panel:`）时不启用。面板的功能和安全机制见 [Web 管理面板](panel.md)，生成密码哈希和远程访问见 [部署：Web 管理面板](deployment.md#web-管理面板)。

| 字段 | 默认 | 说明 |
|---|---|---|
| `users` | 必填，至少一项 | 登录账号，每项 `{name, password_hash}`；都是管理员，权限相同 |
| `users[].name` | 必填 | 用户名，不能重复 |
| `users[].password_hash` | 必填 | `conloom panel hash-password` 输出的哈希，形如 `pbkdf2-sha256:<迭代次数>:<盐>:<哈希>`，迭代次数不低于 100000。建议写成 `${CONLOOM_PANEL_ADMIN_HASH}` 放进 `.env`。换成新哈希并重启后，这个用户已有的登录会话全部失效 |
| `session_ttl` | 168h | 登录后最长有效期，> 0 |
| `idle_timeout` | 12h | 多久没有操作就要重新登录，> 0 |
| `cookie_secure` | false | 会话 cookie 是否带 `Secure`（只经 HTTPS 发送）。经 HTTPS 反向代理访问时设为 `true`；只经 SSH 隧道用 `http://127.0.0.1` 访问时保持 `false` |
| `listen` | 空 | 面板单独的监听地址，`host:port` 形式，例如 `0.0.0.0:8788`。留空时面板在 `server.listen` 的 `/panel/` 下；设置后核心多开一个端口，只提供面板（`/` 跳转到 `/panel/`），`server.listen` 上不再有 `/panel/`，API、`/healthz`、`/metrics` 仍只在 `server.listen` 上。不能和 `server.listen` 占同一个端口。直接开放到公网前务必读 [Web 管理面板：用单独端口直接访问](panel.md#用单独端口直接访问)：普通 HTTP 下密码和会话 cookie 是明文传输的 |

```yaml
panel:
  users:
    - name: admin
      password_hash: ${CONLOOM_PANEL_ADMIN_HASH}
```

`panel` 段只由 `serve` 校验：没有用户、用户名重复、哈希格式不对或迭代次数太低，或者 `listen` 不是 `host:port`（端口须是 0–65535 的数字）、和 `server.listen` 是同一个端口（端口相同且主机相同，或其中一个是 `0.0.0.0`、`::`、空主机）时启动失败，退出码 78，错误信息只指出字段，不包含哈希本身。`server.listen` 同样必须是 `host:port`。`listen` 的端口已被别的程序占用时核心启动失败。

## adapters

| 字段 | 说明 |
|---|---|
| `adapters.napcat.ws_url` | NapCat 的 OneBot 11 正向 WebSocket 地址，`ws://` 或 `wss://`，必填 |
| `adapters.napcat.access_token` | NapCat 的 access token |
| `adapters.napcat.core_url` | 核心的地址，`http://` 或 `https://`，必填 |
| `adapters.napcat.core_token` | 调用核心 API 的令牌，必填 |
| `adapters.napcat.bot_instance_id` | 对应的机器人实例，必填 |
| `adapters.napcat.state_dir` | 去重记录 `napcat-dedup.log` 所在目录，默认同 `storage.data_dir`，相对路径按配置文件目录解析 |
| `adapters.mock.core_url` / `core_token` / `bot_instance_id` | Mock 适配器的同名设置，均必填 |

**Telegram 适配器**（`conloom adapter telegram`，处理私聊和群聊，接入步骤见 [接入 Telegram](telegram.md)）：

| 字段 | 默认 | 说明 |
|---|---|---|
| `adapters.telegram.token` | 必填 | BotFather 给的机器人 token。请求地址里含有它，日志和错误信息里一律显示为 `REDACTED` |
| `adapters.telegram.api_base` | `https://api.telegram.org` | Bot API 地址，`http://` 或 `https://`；可以指向自建的 Bot API 服务器或反向代理 |
| `adapters.telegram.poll_timeout` | 50s | `getUpdates` 长轮询时长，1s–60s，按整秒计 |
| `adapters.telegram.core_url` | 必填 | 核心的地址，`http://` 或 `https://` |
| `adapters.telegram.core_token` | 必填 | 调用核心 API 的令牌，必须绑定 `bot_instance_id` 指定的机器人实例，否则适配器启动时报错退出 |
| `adapters.telegram.bot_instance_id` | 必填 | 对应的机器人实例，该实例的 `platform` 为 `telegram`，`platform_self_id` 填机器人的数字 ID（token 冒号前的部分），`admins` 填 Telegram 用户 ID |
| `adapters.telegram.state_dir` | `storage.data_dir` | 去重记录 `telegram-dedup.log` 所在目录，相对路径按配置文件目录解析 |

`adapter telegram` 只校验上表的字段和 `observability`，出错时退出码为 78；启动后 Telegram 拒绝 bot token、核心拒绝 `core_token` 或令牌属于别的机器人实例时，同样以 78 退出。示例配置为它用到这些变量：

| 变量 | 用途 |
|---|---|
| `CONLOOM_TG_BOT_TOKEN` | 机器人 token |
| `CONLOOM_TG_ADAPTER_TOKEN` | Telegram 适配器调用核心 API 的令牌（随机长字符串，与 NapCat 的不同） |
| `CONLOOM_TG_BOT_ID` | 机器人的数字 ID |
| `CONLOOM_TG_ADMIN_ID` | 管理员的 Telegram 用户 ID |
| `CONLOOM_TG_GROUP_ID` | 开启的 Telegram 群 ID（负数），写进 `bot_tg` 的 `enabled_conversations` |
| `CONLOOM_TG_FRIEND_ID` | `private_chat: allowlist` 下允许私聊的一个用户 ID |

## observability

| 字段 | 默认 | 说明 |
|---|---|---|
| `log_level` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `log_format` | `json` | `json` \| `text`，写到 stderr |
| `log_message_content` | `false` | 是否在日志里记录消息正文，仅建议本地调试时打开 |
