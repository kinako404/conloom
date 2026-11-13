# Telegram 适配器与私聊记忆设计

日期：2026-11-13
状态：已定稿，待实施
相关：记忆层设计 `2026-11-12-memory-affinity-design.md`（下称"记忆 v1"）

## 1. 为什么做

- 接下来的重点是 Telegram 私聊对话。核心不认识任何平台，所以缺的是 Telegram 适配器。
- 私聊记忆不好：记忆 v1 只做了群聊，私聊只能靠最近 50 条原文。超出这个范围的事机器人会忘掉，甚至编造"你昨天说想学钢琴"。
- 短模型的思考已经通过配置打开（`chat_template_kwargs.enable_thinking: true`，输出预留调到 3072），这次不改代码。

## 2. 目标与非目标

**目标**

1. 新增 `conloom adapter telegram`：通过 Telegram Bot API 长轮询收消息，把回复发回去。只依赖标准库。可靠性和 NapCat 适配器一样：入站幂等，出站至少投递一次，本地去重。
2. 私聊有记忆：私聊会话同样整理"对话摘要"和"关于对方的笔记"，回复时注入，好感度照常累计。
3. 私聊默认参数单独配置：新增 `default_private_policy`，私聊会话的默认策略从这里取，没配置时退回 `default_group_policy`。
4. 新增 `/记住 <内容>`：立即把一句话写进自己的笔记，不用等下次整理。
5. 可以按需开启：Telegram 群聊按群开启（列入 `enabled_conversations`），私聊可以只对名单里的人开放（`private_chat: allowlist`）。

**非目标（这次不做）**

- Webhook 方式（需要公网入口），图片下载和理解，消息编辑和撤回，内联按钮。
- 跨会话记忆：同一个人在 QQ 和 Telegram 是两个会话，记忆互不相通。

**反目标**

- token 出现在日志、仓库或错误信息里。
- 私聊内容出现在别的会话的记忆或回复里。
- 适配器重启或断网后丢消息，或者把同一条回复发两遍。

## 3. 方案取舍

| 方案 | 说明 | 结论 |
|---|---|---|
| **长轮询 getUpdates（选用）** | 适配器主动拉取更新，确认靠 offset，Telegram 会保存未确认的更新 24 小时 | 不需要公网入口，和现有"适配器主动连出去"的部署方式一致 |
| Webhook | Telegram 推送到我们的 HTTPS 地址 | 需要域名、证书和对外端口，不做 |
| 引入第三方 Telegram 库 | — | 只用到 getMe、getUpdates、sendMessage、sendChatAction 四个接口，标准库就够，不加依赖 |

私聊默认策略的几种做法：

| 方案 | 结论 |
|---|---|
| **新增 `default_private_policy`（选用）** | 私聊和群聊的默认值本来就不同（私聊要开记忆、历史要更长）；按会话类型选默认值，规则清楚，也能配置 |
| 机器人级开关 `private_memory: true` | 只能管记忆一项，其他私聊参数（历史条数、整理阈值）还是没法单独配 |
| 每个私聊会话用 API 单独开启 | 新用户每来一个都要手动操作，不可行 |

## 4. 设计

### 4.1 Telegram 适配器

**进程与配置**

```yaml
adapters:
  telegram:
    token: ${CONLOOM_TG_BOT_TOKEN}
    api_base: https://api.telegram.org   # 可省略
    poll_timeout: 50s                    # getUpdates 长轮询时长，1s–60s
    core_url: http://127.0.0.1:8787
    core_token: ${CONLOOM_TG_ADAPTER_TOKEN}
    bot_instance_id: bot_tg
    # state_dir 默认与 storage.data_dir 相同
```

- 机器人实例用 `platform: telegram`，`platform_self_id` 填 Bot 的数字 ID，`admins` 填 Telegram 用户 ID。
- `private_chat` 原有三档语义不变，另新增 `allowlist`（见 4.4）。

**收消息**

- 循环调用 `getUpdates?offset=…&timeout=…&allowed_updates=["message"]`。
- 每条更新**提交给核心成功之后**才推进 offset（下一轮用 `update_id+1`）。提交失败就退避重试，不推进 offset，交给 Telegram 保存的未确认更新兜底，不丢消息。
- offset 不需要在本地持久化：重启后 Telegram 会把未确认的更新重新发来，核心靠幂等键去重。
- 幂等键为 `telegram:<bot_id>:<update_id>`。
- 处理 `private`、`group`、`supergroup` 三种会话，`channel` 记一条 Debug 日志后推进 offset。群是否工作由核心按 `enabled_conversations` 判断，适配器不做过滤。

**消息映射**

- 会话：私聊为 `Type=private`、`ID="private_<user_id>"`；群为 `Type=group`、`ID="group_<chat_id>"`（群的 chat_id 是负数，实施时要确认 ID 校验允许 `-`，不允许的话就换一种无损编码）。`PlatformConversationID` 一律填 `<chat_id>`。
- 群里的 @ 和回复：消息 entities 里的 `mention`，如果文本等于 `@<bot用户名>`，算 @ 机器人，其他 `mention` 和 `text_mention` 算 @ 群友；`reply_to_message` 映射成 reply_to。核心靠消息 ID 判断是不是在回复机器人。
- 群里的隐私模式：隐私模式开着时，机器人在群里只收到发给它的命令（如 `/help@bot`）、回复它的消息和服务消息，**单纯 @ 机器人的消息收不到**。要正常使用群聊（@ 回复、在聊会话、主动加入），需要在 BotFather 里执行 `/setprivacy` 设为 Disable，或者把机器人设为群管理员。这一点写进 docs/telegram.md。
- 发送者：`Actor.PlatformUserID=<from.id>`，显示名为 `first_name + " " + last_name`，去掉首尾空白；都为空时用 `username`。
- 消息 ID：Telegram 的 message_id 只在单个会话内唯一。标准 ID 为 `msg_<chat_id>_<message_id>`，回复关系也用这个格式。核心记录机器人自己的消息时用的是 ACK 里的平台消息 ID，所以适配器 ACK 时上报的平台消息 ID 也必须是 `<chat_id>_<message_id>`，保证两边对得上。实施时先核对核心怎么从平台消息 ID 生成 `msg_…`，再按它对齐。
- 正文：
  - `text` 原样使用；
  - 图片：`caption` 加上 `[图片]` 占位；
  - `sticker` 写成 `[表情]`，`voice` 和 `audio` 写成 `[语音]`，`video` 写成 `[视频]`，`document` 写成 `[文件]`。
- 命令：
  - `/start`（可以带参数）转成普通文本"你好"；
  - 其他以 `/` 开头的命令去掉 `@<bot用户名>` 后缀后原样上报，例如 `/help@xxx_bot` 变成 `/help`。

**发消息**

- 长轮询拉取核心的发件箱，用 `sendMessage` 纯文本发送，不设 parse_mode。
- 动作带 reply_to 时，填 `reply_parameters.message_id`，同时设 `allow_sending_without_reply: true`。
- 超过 4096 字时按段落或字数拆成多条，只有第一条带引用。
- 发送成功后 ACK，平台消息 ID 用第一条消息的 `<chat_id>_<message_id>`。
- 按 `action_id` 本地去重，规则和 NapCat 一样：发送结果不明时宁可当失败，也不重发。去重逻辑抽成两个适配器共用的包，不复制代码。
- 遇到 429 限流时，按返回的 `retry_after` 等待后重试。

**"正在输入"提示**

提交事件后，如果核心返回 `disposition=reply`，就在这个会话里每 4 秒调用一次 `sendChatAction(typing)`，直到这个会话的回复发出或者过了 60 秒。开启思考后回复会慢 1 秒左右，这个提示能明显改善体验。

**安全**

- 所有日志和错误信息里，URL 中的 token 都替换成 `REDACTED`。Bot API 的 URL 里本身就带 token，这一点必须有测试覆盖。

**部署**

- 新增 `deploy/systemd/conloom-telegram.service`，写法参照 napcat 的单元。
- 新增一个适配器令牌，scope 和 napcat 的适配器令牌相同，绑定 `bot_tg`。

### 4.2 私聊默认策略

- 配置新增 `default_private_policy`，结构与 `default_group_policy` 相同。
- 策略服务按会话类型选默认值：私聊会话用 `default_private_policy`，没配置时用 `default_group_policy`；库里已有的会话版本仍然优先。
- 补默认值、校验规则都和群聊一样。
- 示例配置给私聊的建议值：
  - `memory.enabled: true`；
  - `context.max_recent_messages: 200`；
  - `memory.consolidate_every: 40`；
  - `memory.consolidate_interval: 1h`；
  - `memory.consolidate_min: 6`（见 4.3）。

### 4.3 私聊记忆

在记忆 v1 的基础上，把私聊会话也纳入进来：

- **生效条件**：私聊和群聊一样，都是 `memory.enabled && allow_long_requests`。原来写死只限群聊的四处要放开：调度、注入、命令、API。
- **新策略字段 `memory.consolidate_min`**：替换写死的 20 条。默认 20，范围 1–2000。含义是：距上次整理超过 `consolidate_interval`、并且新消息至少有这么多条时，就整理一次。
- **整理提示词有私聊版本**：
  - 第一句说明这是机器人和一位用户的私聊记录；
  - 摘要写"你们最近聊过什么、约定或答应过什么、还没聊完的话题、对方的近况"；
  - 笔记写"对方是谁、喜好、正在做的事、重要经历"；
  - 群聊版的其他规则全部保留（指令通道、敏感信息、暧昧内容、机器人自己的说法不算事实、不用代号、不写过程性字眼）。
- **字数上限按会话类型区分**：私聊只有一个人，可以记得更细。
  - 私聊：摘要 ≤1200 字，笔记 ≤500 字；提示词里写 1000 和 400，留出余量。
  - 群聊：维持 800 和 200 不变。
- **注入**：私聊的记忆块把"群里最近："换成"你们最近聊过："，笔记行仍是"名字（档位）：笔记"。私聊只有一个人，不带别人的笔记。
- **命令**：
  - `/好感度`、`/记忆`、`/忘记我` 在私聊里可用；
  - `/记忆 摘要` 在私聊里所有人都能用（摘要就是他自己的对话），在群聊里仍然只有管理员能用，群聊原来的 `/记忆 群` 保留；
  - `/记忆 整理` 在私聊里只有管理员能用；
  - 原来的"记忆只在群聊里使用。"不再使用。
- **`/记住 <内容>`**（别名 `/remember`）：
  - 群聊和私聊都能用，所有人可用，记忆必须已生效；
  - 内容去掉首尾空白后为 1–100 字；
  - 写入方式：在自己的笔记末尾追加"（他让我记住）<内容>"。超出笔记上限时，从前面删掉最旧的内容腾出位置，使用 UpdateMember，source 为 command，并写 revision；
  - 回复"记住了。"；
  - 整理员提示词加一条：笔记里标有"（他让我记住）"的内容要保留，除非本人明确更正或要求忘记。
- **API**：私聊会话不再返回 400，按会话本身的策略处理。

### 4.4 私聊白名单

- `bots[].private_chat` 新增一档 `allowlist`：管理员，以及 `enabled_conversations` 里以 `{type: private, platform_id: "<用户ID>"}` 列出的人可以私聊，其他人不回复。
- 原有三档 `admins`、`all`、`off` 的行为不变。这三档下名单里的 private 条目不起作用，启动时打 Warn 提示。
- 群和人都在同一个名单里配置，改名单需要重启核心。v1 不做运行时增删命令。

### 4.5 文档

- README 的特性、架构图里加上 Telegram。
- 新增 `docs/telegram.md`：接入步骤，包括 BotFather、配置、令牌、systemd，以及如何在 BotFather 重新生成 token。
- `configuration.md`：补 `adapters.telegram`、`default_private_policy`、`memory.consolidate_min`。
- `usage.md`：补私聊记忆和 `/记住`。
- `architecture.md`：补 Telegram 适配器和私聊记忆。
- `deployment.md`：补 systemd 单元。

## 5. 我做的决定（可以推翻）

- 长轮询，不用 webhook；不引入第三方库。
- Telegram v1 只做私聊。
- 私聊默认值用独立的 `default_private_policy`。
- 私聊笔记 500 字、摘要 1200 字。
- `/start` 转成"你好"，让人设自己打招呼。
- `/记住` 追加到笔记末尾并加标记，满了从前面删。
- TG 机器人的 `private_chat` 先用 `allowlist`，只有管理员和名单里的人能聊，防止陌生人消耗模型额度；想开放时改成 `all`。
- 名单写在配置里，改了要重启；运行时增删命令以后再做。

## 6. 测试要点

- 用假 Telegram 服务（httptest）模拟 getUpdates、sendMessage、sendChatAction 和 429，以及核心的 API，跑通完整往返：
  - 提交失败时不推进 offset；
  - 重启后同一更新不会被处理两次；
  - 超长消息会被拆分；
  - 引用回复和机器人消息的 ID 能对上；
  - 日志里不出现 token。
- 策略：私聊会话取到 `default_private_policy`，没配置时退回群聊默认值，库里的版本优先。
- 私聊记忆：
  - 调度器会整理私聊会话；
  - 私聊用私聊版提示词、私聊字数上限，`consolidate_min` 生效；
  - 注入块用私聊措辞；
  - 命令在私聊里可用；
  - `/记住` 的追加、裁剪、revision 都正确，整理提示词里有保留规则；
  - 群聊行为保持不变，作为回归测试。
