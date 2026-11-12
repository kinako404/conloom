# ConLoom 织语

让对话沉淀为记忆，让记忆塑造人格。

ConLoom 是一个用 Go 编写的聊天机器人核心：平台适配器（Adapter）把聊天消息通过标准 REST API 交给核心，核心负责回复判定、上下文组装、模型调用、审查和可靠投递。单个二进制、无 cgo、无需数据库或 Docker，Linux／macOS／Windows × amd64／arm64 开箱即用。

## 功能（P1）

- 标准 Bot Interaction API（REST）：事件提交、任务查询与取消、Outbox 长轮询拉取与 ACK、能力协商、人格与群策略读写。
- 两个 Adapter，都只通过公开 API 与核心交互：NapCat（OneBot 11 正向 WebSocket，真实 QQ 平台）与 Mock（终端里本地对话）。
- 可靠投递：幂等事件、持久化 Outbox、至少一次投递 + Adapter 端按 `action_id` 本地去重；重启后恢复未完成任务，过时的回复不再发送。
- 回复策略：私聊、@Bot、回复 Bot、命令触发；同一发送者连续短消息合并为一次回复；每用户限流、每会话最短回复间隔；核心与 Adapter 两层自回复防护。
- 群聊参与：Bot 在和人聊时，对方不 @ 的后续发言**按轮次**处理，不句句都回，对方转头和别人聊时**识趣退出**；Bot 没在聊时，只在群聊**自然停顿**、在场有 @ 过 Bot 的群友且话题相关或有人提问时，才**主动加入**一两句。主动频率由每人用 `/主动` 自己调，管理员有群总开关（见[群聊参与](#群聊参与)）。
- 人格：内置不可变核心人格 + 可配置的群人格模板，人格与群策略都有版本，可修改、可回滚。
- 模型：任意 OpenAI 兼容接口，按 `short_context`／`long_context` 角色路由，换模型只改配置；熔断与降级。
- 并发准入：模型全局并发、群并发（0／1／2）、token 预留预算、优先级队列与队列上限。
- 基础审查（不可关闭）：控制字符清理、平台控制码剥离、密钥打码、屏蔽词。
- 可观测：`log/slog` 结构化日志、trace_id、Prometheus 指标。

## 路线

- P2：长期记忆（混合检索、向量、滚动摘要、长期事实、`/remember` `/forget`、分层遗忘）、人格观察与自动演化、PostgreSQL／pgvector／Redis 存储实现。
- P3：Tool Gateway、Web 搜索、ComfyUI 等工具能力。
- P4：多实例水平扩展、多 Agent、流式输出等进阶能力（规划中，以届时设计为准）。

## 架构

```text
QQ 群 ⇄ NapCat ⇄(OneBot 11 正向 WS)⇄ conloom adapter napcat ⇄(REST /api/v1)⇄ conloom serve ⇄ 模型网关（OpenAI 兼容）
                                                                             │
                                                              <data_dir>/conloom.db（SQLite）
```

一条消息的处理过程：

1. Adapter 把平台消息转为标准 Event，`POST /api/v1/events`，带 `Idempotency-Key`。
2. Gateway 校验 token 权限、Schema 与幂等键，丢弃 Bot 自身的消息。
3. 消息先落库，再由回复策略判定 `reply`／`observe`／`ignore`。没有被忽略的群消息还会重置该群的停顿计时器；群里安静下来时按观望模式的条件检查一次，全部满足才会建一个加入群聊的任务，否则不调用模型。
4. `reply`：进入该会话的串行执行单元，在合并窗口内收集同一发送者的连续短消息（在聊模式的一轮用更长的 `turn_gap`／`turn_max_wait`）。
5. 加载人格版本、群策略与近期消息，组装上下文并估算 token；他人的每条历史消息最多保留 300 字（超出部分截断并标注，附件占位符保持完整），Bot 自己的历史回复不截断。
6. 角色路由选出模型 Profile，准入控制放行后调用模型。
7. 输出经基础审查与后处理写入 Outbox，并唤醒长轮询中的 Adapter。
8. Adapter 拉取、发送、ACK；核心记录 Bot 自己的回复及其来源关系。

### 投递语义与残留风险

Outbox 是至少一次投递：ACK 丢失或 Adapter 重启后，同一个动作会被再次投递。NapCat Adapter 在发送前查本地去重记录（`napcat-dedup.log`，按 `action_id`），已发送过的动作只补 ACK、不重发。仍有两种残留情况：

- **可能重复一条**：Adapter 在平台发送成功之后、写入去重记录之前崩溃或断电，重启后这条动作会被重发一次。
- **可能丢失一条**：发送结果未知时（请求已发出但没等到 NapCat 的响应，或 NapCat 返回错误——NapCat 对实际已送达的消息也可能报发送超时），这条动作会被记入去重记录并以 `failed` 确认，之后不再重发，以避免重复；代价是这条回复可能没有发出去。

## 安装

**预编译二进制（推荐）**：在 [Releases](https://github.com/kinako404/conloom/releases) 下载对应平台的压缩包 `conloom_<版本>_<系统>_<架构>.tar.gz`（Windows 为 `.zip`），解压即可，不需要任何运行时。可用同页的 `checksums.txt` 校验：

```sh
sha256sum -c checksums.txt --ignore-missing
```

压缩包内含 `conloom` 二进制、`README.md`、`LICENSE`、`conloom.example.yaml` 与 `deploy/`。macOS 首次运行若被拦截，可执行 `xattr -d com.apple.quarantine ./conloom`。

**从源码安装**：需要 Go ≥ 1.26（由 SQLite 纯 Go 驱动的要求决定）。

```sh
go install github.com/kinako404/conloom/cmd/conloom@latest
```

## 快速开始

1. 复制示例配置：

   ```sh
   cp conloom.example.yaml conloom.yaml
   ```

2. 在 `conloom.yaml` 同目录新建 `.env`（`chmod 600 .env`），填入真实值。示例配置里的 `${VAR}` 会从进程环境变量或这个 `.env` 中展开（进程环境变量优先）：

   ```sh
   CONLOOM_ADAPTER_TOKEN=<随机长字符串>
   CONLOOM_ADMIN_TOKEN=<另一个随机长字符串>
   CONLOOM_BOT_SELF_ID=<Bot 的 QQ 号>
   CONLOOM_ADMIN_QQ=<管理员 QQ 号>
   CONLOOM_GROUP_ID=<启用的群号>
   CONLOOM_SHORT_MODEL_ENDPOINT=https://<你的模型网关>/v1
   CONLOOM_SHORT_MODEL_NAME=<短上下文模型名>
   CONLOOM_SHORT_MODEL_KEY=<API Key>
   CONLOOM_LONG_MODEL_ENDPOINT=https://<你的模型网关>/v1
   CONLOOM_LONG_MODEL_NAME=<长上下文模型名>
   CONLOOM_LONG_MODEL_KEY=<API Key>
   CONLOOM_NAPCAT_WS_URL=ws://<NapCat 地址>:<端口>
   CONLOOM_NAPCAT_TOKEN=<NapCat access token>
   ```

   模型请求发往 `<endpoint>/chat/completions`，所以 endpoint 一般写到 `/v1` 为止。

3. 启动核心服务（默认监听 `127.0.0.1:8787`）：

   ```sh
   ./conloom serve -c conloom.yaml
   curl http://127.0.0.1:8787/healthz   # 返回 ok
   ```

4. 另开一个终端，用 Mock Adapter 在本地对话：

   ```sh
   ./conloom mock -c conloom.yaml                                  # 以第一个管理员的身份私聊
   ./conloom mock -c conloom.yaml -conv group:<群号> -user <QQ号>:<昵称>   # 模拟群聊
   ```

   每行输入一条消息；在群聊里以 `@` 开头表示 @Bot。按 Ctrl+D（Windows 为 Ctrl+Z 回车）退出。

5. 接入 QQ：在 NapCat 中开启 OneBot 11 正向 WebSocket 服务并设置 access token，然后启动 NapCat Adapter：

   ```sh
   ./conloom adapter napcat -c conloom.yaml
   ```

   之后在启用的群里 @Bot 即可聊天。注意：Mock 与 NapCat Adapter 默认使用同一个 Bot 实例，会争抢同一个 Outbox；P1 假设每个 Bot 实例只有一个 Adapter 进程，不要同时运行两者。

## 命令行

```text
conloom serve -c conloom.yaml                                      核心服务（API + 编排 + Worker）
conloom adapter napcat -c conloom.yaml                             NapCat Adapter（独立进程，可部署在别的机器）
conloom mock -c conloom.yaml [-conv group:<群号>] [-user <QQ号>:<昵称>]   终端里的 Mock Adapter
conloom backup -c conloom.yaml -o <文件>                           在线备份数据库（目标文件必须不存在）
conloom version                                                    显示版本与提交
```

`-c` 默认为 `conloom.yaml`。退出码：0 正常，1 运行时错误，2 参数或配置错误。收到 SIGINT／SIGTERM 时优雅退出。

## 聊天命令

| 命令 | 权限 | 作用 |
|---|---|---|
| `/help` | 所有人 | 简短说明 |
| `/stop` | 发起者或管理员 | 取消本会话正在进行或排队的生成 |
| `/persona` | 所有人 | 显示当前人格名称与版本 |
| `/pause`、`/resume` | 管理员 | 暂停本群回复（群并发设为 0），并静默取消本群正在生成或排队的回复、清空在聊会话与停顿计时器／恢复；暂停期间也不会主动加入群聊 |
| `/主动`、`/proactive` | 所有人 | 查看自己的主动搭话档位、是否有资格、今天已被主动搭话的次数 |
| `/主动 关`／`低`／`中`／`高`，`/proactive off`／`low`／`mid`／`high` | `eligibility_ttl` 内 @ 过或回复过 Bot 的人 | 设置自己的主动搭话档位（中英文档位等价），任意档位都可设置，包括从“关”重新打开 |
| `/主动 全群 开`／`关`，`/proactive group on`／`off` | 管理员 | 本群主动搭话总开关（生成新的群策略版本） |

命令前缀默认为 `/`（群策略 `reply.command_prefix`）。管理员只认配置中 `bots[].admins` 列出的平台用户 ID。

- 非管理员的命令与普通回复共用每用户限流（`reply.user_rate_limit_per_minute`），超出后命令只落库不回应（`suppressed_reason` 为 `rate_limited`）；群暂停期间非管理员的命令同样不回应（`paused`）。管理员的命令（包括 `/pause`、`/resume`、`/stop`）不受限流和暂停影响。
- 通过 `/pause` 或 `PUT /api/v1/conversations/{id}/policy` 把群并发设为 0 时，该会话正在生成和排队的回复都会被静默取消（不发“已停止”提示），`/resume` 之后不会补发暂停前积压的回复。
- 发生时间（事件的 `occurred_at`）早于 `runtime.stale_task_after`（默认 2 分钟）的 @Bot、回复 Bot 或命令消息只落库、不回复，`suppressed_reason` 为 `stale`。例如核心停机期间 NapCat Adapter 缓存的消息，在核心恢复后才提交时就是这样处理的，不会在恢复后集中回复一批旧消息。消息时间来自平台（NapCat 所在机器的时钟），所以 NapCat 与核心所在机器的时钟要保持同步（如启用 NTP）；若 NapCat 的时钟比核心慢超过这个阈值，所有消息都会被当作过时而不回复。
- `/主动` 系列命令只在群聊中使用，私聊中回复“主动搭话只在群聊里使用。”。还没有资格的人设置档位时回复“先 @ 我聊几句，才能开启主动搭话。”；非管理员使用 `全群`／`group` 子命令不会生效。

## 群聊参与

目标是不乱插话、不句句都回，但在合适的时机主动加入群聊。以下行为都只在群聊中生效，Bot 在群里有三种参与方式：

1. **直接对话**：@Bot、回复 Bot 的消息、命令（以及私聊）照旧，每次都回复（仍受限流、暂停等限制）。
2. **在聊模式**：Bot 正在和某些人聊时，这些人不 @Bot 的后续发言按“轮”处理，模型判断这一轮值不值得回应，可以不回。
3. **观望模式**：Bot 没在聊时，只有群里的对话出现自然停顿，且资格、相关性与预算都满足，才让模型看最近的对话，决定要不要加入一两句。

后两种都遵循“规则先筛、模型兜底”：便宜的规则判定在前，只有通过筛选才会调用模型，模型仍可以拒绝发言。

**模型拒答与跳过标记**：在聊模式的每一轮和加入群聊的任务会在当前消息末尾附加一段固定提示，告诉模型没有人 @ 它、不需要每句都回应，不合适就只输出内部跳过标记 `[[SKIP]]`。模型输出（去掉思考段后）只要在任何位置含有这个标记（忽略大小写、空白、零宽字符，全角括号与【】视同方括号），或整条输出只是 `SKIP`／`[SKIP]`（允许前后带标点），整条输出都会被丢弃：不产生任何动作，也不进入 Outbox，标记永远不会出现在群里。这类任务的状态仍是 `succeeded`，`meta.review_result` 为 `skipped`。

**回复格式**：群里的回复默认直接发出，不使用 QQ 的引用回复，只在可能弄不清是在回应谁时才引用它回应的那条消息（任务的最后一条触发消息）。由群策略 `reply.quote` 控制：

- `auto`（默认）：生成回复时满足任一条件才引用——触发消息之后已有其他群友发言（触发者本人的补充和 Bot 自己的消息不算），或触发消息已超过 60 秒；查询近期消息失败时也按引用处理。直接对话、命令、在聊轮次与加入群聊都按这条规则。
- `always`：总是引用（旧版本的行为）。
- `never`：从不引用。

私聊从不引用。

### 在聊模式

群策略 `reply.continuation_window` 是在聊会话的保持时间（0~10m；0 表示不开启在聊模式，只回直接对话；示例配置为 `0s`）。每个群同时最多有一个在聊会话，只记在内存里，重启后清空（最多让一段闲聊需要重新 @Bot）。

- **开始与刷新**：Bot 回复了群里 @Bot 或回复 Bot 的消息后，发起者成为在聊会话的参与者（会话不存在时随之开始）；会话中的直接对话同样把发起者加入，并把下面的未点名回复与连续跳过计数清零。Bot 以加入群聊的方式发言后（见[观望模式](#观望模式加入群聊)），也会开始一段会话，参与者是当时最近对话中的全部目标资格人，并且已计 1 次未点名回复。
- **轮次**：参与者不 @Bot 的发言触发一轮（任务 `trigger` 为 `continuation`）。Bot 等他停顿 `turn_gap`（默认 6 秒）再处理；他一直在说时，从这一轮第一句算起最多等 `turn_max_wait`（默认 20 秒）。同一人在这期间的几句话合并为一轮，只调用一次模型，回复针对他最后一句（需要引用时引用这一句，见上文回复格式）。模型被告知不需要每句都回应：只有对方在问它、在等它回应，或者它有明显值得说的话时才回复一两句；对方在和别人说话、话题已经转开或没必要回应时输出跳过标记。
- **规则退出（不调用模型）**：参与者的非 @ 消息如果 @ 了 Bot 以外的人（包括 @全体成员），或回复的是另一位群友的消息，就不回复、只落库，并把此人移出参与者；参与者都走了，会话结束。
- **非参与者**：不在会话里的人不 @Bot 的发言只落库（会进入上下文），不触发回复。
- **结束**：满足任一条件会话即结束——连续 `max_consecutive_skips`（默认 2）轮模型都输出跳过标记或调用失败（在聊轮次和加入群聊的任务失败时不发出错误提示）；自上次被直接点名以来，未点名的回复达到 `max_unprompted_replies`（默认 4）次；超过 `continuation_window` 既没有参与者发言也没有 Bot 回复；参与者全部规则退出。`/pause` 也会清空会话。

会话结束后，之前的参与者不 @Bot 的消息只落库；要等他再 @Bot 或回复 Bot 的消息并得到回复（或 Bot 加入群聊时他作为目标资格人在场），才会再次成为参与者。由于观望模式要求 Bot 上次发言后至少过 `join_cooldown`，会话刚结束时 Bot 也不会马上以加入群聊的方式回来。会话轮次中的消息会更新发言者的兴趣画像，但不刷新资格。

### 观望模式（加入群聊）

- **时机**：每条落库的非 Bot 群消息都会重置该群的停顿计时器（`proactive.join_lull`，默认 15 秒）。群里安静满这个时长算一次“停顿”，此时做一次加入评估检查；新消息到来会取消尚未到点的计时器，所以大家聊得正热时不会插话。停顿检查本身不调用模型。
- **条件**（全部满足才调用模型）：
  1. 本群主动搭话总开关打开（`proactive.enabled`），群未暂停；
  2. 当前没有在聊会话，本群也没有排队或运行中的任务；
  3. Bot 上一次在本群发言（任何方式）距今至少 `join_cooldown`（默认 10 分钟；这个时间记在内存里，重启后第一次检查时从库里本群最近的 Bot 消息恢复，所以重启不会让 Bot 立刻加入）；
  4. 最近 `join_window`（默认 5 分钟）内至少有 `join_min_messages`（默认 4）条非 Bot、非命令的消息，且最后一条不过时（`runtime.stale_task_after`）；例外是没人接的提问：最新一条消息是目标资格人提的明确问题（含问号、以“吗”结尾，或含“怎么”“为什么”“为啥”“有没有”“谁知道”“是不是”“能不能”“会不会”“要不要”“哪个”“哪里”“多少”，比第 6 条的判定更严）、且它本身可以作为下面的目标消息时，一条也够；
  5. 窗口内至少有一位发过言的**目标资格人**：有资格、档位不是“关”、今天被主动搭话的次数未达档位上限、距上次被主动搭话至少 `user_cooldown`；
  6. 相关：窗口内的文本与某位目标资格人的兴趣画像按其档位匹配，**或者**窗口里有人提问（含“？”或“?”，以“吗”“呢”“么”结尾，或含“怎么”“为什么”“为啥”“有没有”“谁知道”“啥”“咋”）；
  7. 预算：本群最近一小时的加入评估少于 `join_evaluations_per_hour` 次（默认 4），且最近一小时的主动发言少于 `group_hourly_limit` 次（默认 4）。
- **会话外的提问**：Bot 正在和别人聊（有在聊会话）或刚发过言（冷却中）时，若最新一条消息是会话外一位目标资格人提出、没人接的明确问题（判定同第 4 条的例外，且它本身可作为目标消息），停顿后仍会只针对他调用一次模型回答（日志带 `guest=true`），条件 7 的预算照常适用；回答后他加入当前的在聊会话。
- **目标消息**：窗口内最新的一条由目标资格人发出、不是命令、没有 @ 任何人、也没有回复其他群友或 Bot 的消息（@Bot 和回复 Bot 的消息已经直接回复过）；没有这样的消息就不加入。
- **发出**：通过检查后记一次评估，建一个 `trigger` 为 `proactive` 的任务（触发消息为目标消息），等待模型准入时排在普通回复之后。模型看到最近的对话，被告知没有人 @ 它、只有能自然接上正在聊的话题、对大家有帮助或有趣、而且不会打断别人时才说一两句，并会知道目标资格人以前常和它聊的话题（合并后的前 5 个兴趣词）。提示的措辞随目标消息发送者的档位变化：“低”最保守，不合适就跳过；“中”在能自然接上话题时插一句；“高”会告诉模型对方很欢迎它多参与，可以像群友一样接梗、回应，只有大家在谈正事或实在没话说时才跳过。调用模型前和发出前都会重新检查总开关、暂停、目标人的档位和发言上限，已不满足就按跳过处理（不调用模型或不发送）。模型决定加入时，回复针对目标消息发出（是否引用按 `reply.quote`；目标消息之后通常已有别人发言，所以默认多半会引用），随后开始一段在聊会话；模型输出跳过标记则不发送。

### 资格、档位与上限

- **资格**：在本群 @ 过 Bot 或回复过 Bot 的消息（只发命令不算；在聊会话中不 @ 的消息只更新兴趣画像，不刷新资格）。超过 `eligibility_ttl`（默认 30 天）没有再这样做，资格失效。有资格的人可以用 `/主动` 设置任意档位，包括把“关”重新打开；但只有有资格且档位不是“关”的人才算目标资格人。从没 @ 过 Bot 的人永远不是目标资格人：只有这样的人在聊天时，停顿再多次也不会调用模型。
- **档位**：`关`／`低`／`中`／`高`（`off`／`low`／`mid`／`high`）。首次获得资格时为 `default_level`（默认“低”）；没自己设过档位的人每次 @Bot 或回复 Bot 时跟随群当前的 `default_level`；用 `/主动` 自己设过的档位之后一直保留，不会被默认值覆盖。
- **兴趣话题**：核心从每人 @Bot、回复 Bot 以及在聊会话轮次中的消息里提取关键词（英文单词与连续汉字的二字词组，去掉常见虚词与口头禅），形成带衰减的兴趣画像，每人最多保留 50 个词。观望模式中，最近对话的文本与某人的画像足够重合才算“聊到了他常聊的话题”：低档需与前 20 个词至少重合 2 个，中档与前 30 个词至少重合 1 个，高档与前 50 个词至少重合 1 个。
- **发言上限**：每人每天最多被主动搭话 低 2 次、中 4 次、高 8 次（`daily_limits`）；对同一人两次主动搭话至少间隔 `user_cooldown`（默认 30 分钟）；全群每小时最多主动发言 `group_hourly_limit` 次（默认 4）。
- **“每天”的界定**：每人每天的发言上限按 `server.timezone`（默认 `Asia/Shanghai`）的自然日计数，从当地 00:00 开始，到下一个当地午夜清零。
- **计数持久化**：评估与发出的记录存在数据库里，按时间窗口计数，重启后依然有效；7 天前的记录会被定期清理。
- **总开关**：管理员用 `/主动 全群 开`／`关`（只改 `proactive.enabled`，其余设置不变），或通过群策略 API 写入完整的 `proactive` 段（见 [API](#api)）；`/pause` 期间同样不会主动加入群聊。总开关只管观望模式，在聊模式由 `reply.continuation_window` 决定。

### 成本上界

- **直接对话**：每次触发（合并后）至多一次模型调用，受每用户限流约束。
- **在聊模式**：每轮至多一次模型调用，对方连发几句也只算一轮。两次被直接点名之间，一段会话最多有 `max_unprompted_replies` 次未点名回复，两次回复之间最多连续跳过 `max_consecutive_skips − 1` 轮（再多一轮会话就结束），所以最多 `max_unprompted_replies × max_consecutive_skips` 次模型调用（默认 4 × 2 = 8 次），之后必须重新 @Bot。
- **观望模式**：每群每小时至多 `join_evaluations_per_hour` 次模型调用（默认 4），与群消息数量无关；有人刷屏时，停顿只会更少。

### 指标

| 指标 | 标签 | 说明 |
|---|---|---|
| `conloom_continuation_exits_total` | `reason`：`rule`、`model`、`limit`、`idle` | 在聊模式的退出次数：`rule` 为参与者规则退出，`model` 为连续跳过使会话结束，`limit` 为未点名回复达到上限使会话结束，`idle` 为超过保持时间无活动而结束 |
| `conloom_proactive_total` | `result`：`evaluated`、`sent`、`skipped` | 加入群聊：`evaluated` 为通过检查、交给模型评估，`sent` 为实际发出，`skipped` 为模型放弃，或调用模型前／发出前重新检查时已不允许发出 |

每次停顿检查没有加入时记一条 INFO 日志 `join skipped`，带原因代码（如 `few_messages`、`cooldown`、`no_eligible`）以及窗口消息数、目标资格人数。日志不记录兴趣词、消息正文与群友昵称。

## 配置

单一 YAML 文件，完整带注释的示例见 [`conloom.example.yaml`](conloom.example.yaml)。支持 `${VAR}` 与 `${VAR:-默认值}` 展开，`$$` 表示字面量 `$`；未知字段会被拒绝，启动时严格校验并给出可读错误。真实的 `conloom.yaml` 与 `.env` 已被 `.gitignore` 排除，不要提交它们。

| 段落 | 说明 |
|---|---|
| `server` | `listen`（默认 `127.0.0.1:8787`）、`timezone`（展示用时区，也是主动搭话每人每日上限的日界线，默认 `Asia/Shanghai`）、`metrics_enabled`（是否开放 `/metrics`，默认开） |
| `storage` | `data_dir`：数据目录，默认 `./data`，相对路径按配置文件所在目录解析 |
| `api_tokens` | API 令牌列表：`name`、`token`、绑定的 `bot_instance_id`、`scopes`。Adapter 令牌通常只需 `events:write tasks:read outbox:read outbox:ack persona:read`；管理令牌另加 `memory:read memory:write policy:write admin:read` |
| `bots` | Bot 实例：`id`、`platform`、`name`（显示名，默认“织语”）、`platform_self_id`（Bot 自身的平台 ID，必填，用于自回复防护）、`admins`、`enabled_conversations`（只在这些会话工作）、`private_chat`（`admins`／`all`／`off`，默认 `admins`）、`persona_template` |
| `model_profiles` | 按角色配置模型，`short_context` 必需、`long_context` 可选：`provider`（`openai_compatible`）、`endpoint`、`model`、`api_key`、`modalities`、`context`（窗口与预算，须满足 `soft_input_budget <= configured_window <= native_window`）、`runtime`（并发、超时、重试）、`privacy`、`extra_body`（原样合并进请求体）、`token_estimate_factor` |
| `persona_templates` | 群人格模板：`style` 各项 0~1，`behavior` 为枚举；省略 `default` 时使用内置默认人格 |
| `default_group_policy` | 新会话的初始群策略：`concurrency`、`reply`（命令前缀、在聊会话保持时间 `continuation_window` 与轮次设置（见下）、合并窗口 `debounce`、最短回复间隔、每用户限流、引用方式 `quote`）、`context`、`review`、`proactive`（观望模式与主动搭话，见下）。通过 API 或命令修改后存库并版本化，库内版本优先 |
| `runtime` | `action_ttl`、`outbox_lease`、`stale_task_after`、`idempotency_retention`、`max_merge_window`、`session_idle` |
| `adapters.napcat` | `ws_url`、`access_token`、`core_url`、`core_token`、`bot_instance_id`、`state_dir`（去重记录目录，默认同 `data_dir`） |
| `adapters.mock` | `core_url`、`core_token`、`bot_instance_id` |
| `observability` | `log_level`、`log_format`（`json`／`text`）、`log_message_content`（默认 `false`） |

配置中的群号、QQ 号一律写平台原始 ID。

群策略中与[群聊参与](#群聊参与)有关的字段及其默认值（`reply` 段的其他字段见示例配置）：

```yaml
default_group_policy:
  reply:
    continuation_window: 0s     # 在聊会话保持时间，0~10m；0 表示不开启在聊模式，只回直接对话
    turn_gap: 6s                # 在聊时等对方停顿多久再处理这一轮，1s~60s
    turn_max_wait: 20s          # 一轮从第一句算起最多等多久，turn_gap~2m
    max_unprompted_replies: 4   # 两次被直接点名之间最多几次未点名回复，达到后会话结束，1~20
    max_consecutive_skips: 2    # 连续几轮跳过后会话结束，1~10
    quote: auto                 # 群里回复何时引用原消息：auto（必要时）| always | never
  proactive:
    enabled: true               # 群总开关（观望模式）
    default_level: low          # 获得资格时的默认档位：off | low | mid | high
    eligibility_ttl: 720h       # 多久没 @Bot 就失去资格（30 天），1h~8760h
    user_cooldown: 30m          # 对同一人两次主动搭话的最短间隔，0~24h
    group_hourly_limit: 4       # 全群每小时主动发言上限，0~60
    daily_limits: {low: 2, mid: 4, high: 8}   # 每人每天主动发言上限，各 0~100，且 low ≤ mid ≤ high
    join_lull: 15s              # 群里安静多久算一次停顿，5s~5m
    join_window: 5m             # 加入评估看最近多长时间的对话，1m~30m
    join_min_messages: 4        # 窗口内至少多少条非 Bot 消息，2~50
    join_evaluations_per_hour: 4  # 每群每小时最多几次加入评估（即模型调用），1~60
    join_cooldown: 10m          # Bot 上次在本群发言后多久之内不加入，1m~24h
```

YAML 配置中省略的任何字段（或省略整个 `proactive` 段）都保持上面的默认值；群策略 API 不会这样合并，不完整的 `proactive` 段会被拒绝（下面的 v2 新增字段除外，见 [API](#api)）。升级前已存库的群策略版本没有 `proactive` 段时，读取时同样按默认值补齐，即主动搭话默认开启、档位默认为低。已存库的版本或 API 请求里缺少群聊参与 v2 新增的字段（`turn_gap`、`turn_max_wait`、`max_unprompted_replies`、`max_consecutive_skips` 与 `join_*`）或 `reply.quote` 时，逐字段按默认值补齐（`quote` 补为 `auto`）；这些字段的零值本身都不合法，所以不会覆盖有意的设置。`continuation_window` 不补，旧版本里的 `0s` 仍表示不开启在聊模式。

`proactive.contact_cooldown` 已移除：已存库的旧版本里的这个键在读取时被忽略；但配置文件不接受未知字段，升级后要从 `conloom.yaml` 里删掉这一行，否则启动时报错。

## API

所有 `/api/v1` 请求带 `Authorization: Bearer <token>`；令牌只能操作其绑定 Bot 实例下的会话、任务和 Outbox。

| 方法与路径 | 所需 scope | 说明 |
|---|---|---|
| `POST /api/v1/events` | `events:write` | 提交事件，必须带 `Idempotency-Key`；返回 202 与 `disposition` |
| `GET /api/v1/tasks/{task_id}` | `tasks:read` | 查询任务 |
| `POST /api/v1/tasks/{task_id}/cancel` | `tasks:read` | 取消任务 |
| `GET /api/v1/outbox?limit=&wait=` | `outbox:read` | 拉取待发送动作（长轮询，`wait` 如 `30s`，最长 30s） |
| `POST /api/v1/outbox/{action_id}/ack` | `outbox:ack` | 确认动作：`{"status":"sent|unsupported|failed","platform_message_id":"…"}` |
| `GET /api/v1/capabilities` | 任意有效令牌 | 能力协商 |
| `GET /api/v1/conversations/{id}/persona` | `persona:read` | 当前人格版本 |
| `PUT /api/v1/conversations/{id}/persona` | `policy:write` | 写入新人格版本：`{"persona":{…},"reason":"…"}` 或 `{"rollback_to":n,"reason":"…"}` |
| `GET /api/v1/conversations/{id}/policy` | `admin:read` | 当前群策略版本 |
| `PUT /api/v1/conversations/{id}/policy` | `policy:write` | 写入新群策略版本：`{"policy":{…},"reason":"…"}` |
| `GET /healthz` | 无 | 健康检查 |
| `GET /metrics` | 无 | Prometheus 指标（`server.metrics_enabled: false` 可关闭） |

群策略包含 `proactive` 段（字段同上文配置）。`PUT …/policy` 的请求体完全不含 `proactive` 段时，保留本群**当前**的主动搭话设置；只要含有 `proactive` 段，它就必须是完整且合法的一段，API 不会与当前设置或默认值合并，像 `{"enabled": false}` 这样只写部分字段的请求会被拒绝并返回校验错误。唯一的例外是群聊参与 v2 新增的字段（`reply.turn_gap`、`reply.turn_max_wait`、`reply.max_unprompted_replies`、`reply.max_consecutive_skips` 与 `proactive.join_*`）及 `reply.quote`：请求里缺少时逐字段按默认值补齐，方便旧的调用方继续使用；已移除的 `proactive.contact_cooldown` 若出现在请求里会被忽略。只想关闭主动搭话时，可由管理员在群里发 `/主动 全群 关`，它以当前版本为基础写入一个新的群策略版本，且只改 `proactive.enabled`（`开` 同理），可用 `GET …/policy` 查看。任务的 `trigger` 除 `private`、`mention`、`reply`、`command` 等外，还可能是 `continuation`（在聊模式的一轮）与 `proactive`（加入群聊）。

会话 `{id}` 为标准会话 ID，如 `group_<群号>`、`private_<QQ号>`。同一幂等键重放返回首次响应并带 `Idempotent-Replayed: true`，同键不同内容返回 409。错误响应统一为：

```json
{"error":{"code":"FORBIDDEN","message":"…","retryable":false,"retry_after_ms":0,"trace_id":"trace_…"}}
```

请求与响应的 Go 类型见 [`pkg/conloomapi`](pkg/conloomapi)，Go SDK 见 [`pkg/client`](pkg/client)。

## 部署为服务

`/healthz` 与 `/metrics` 不需要鉴权。若要让别的机器上的 Adapter 访问核心而把 `server.listen` 改为非本地地址，请用防火墙限制来源，或关闭 `metrics_enabled`。

### Linux（systemd）

仓库的 [`deploy/systemd/`](deploy/systemd) 提供两个单元：`conloom.service`（核心）与 `conloom-napcat.service`（Adapter，依赖核心），都以 `conloom` 用户运行、崩溃 3 秒后自动重启，停止时最多等待 90 秒（Adapter 会等正在发送的消息拿到结果再退出）。单元启用了 `ProtectSystem=strict`，只有 `/opt/conloom/data` 可写：该目录必须在启动前存在；若把 `storage.data_dir` 或 `adapters.napcat.state_dir` 改到别处，要同步修改单元里的 `ReadWritePaths`。

```sh
sudo useradd --system --home-dir /opt/conloom --shell /usr/sbin/nologin conloom
sudo mkdir -p /opt/conloom/data
sudo cp conloom conloom.yaml .env /opt/conloom/
sudo chown -R conloom:conloom /opt/conloom
sudo chmod 600 /opt/conloom/.env
sudo cp deploy/systemd/conloom.service deploy/systemd/conloom-napcat.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now conloom conloom-napcat
journalctl -u conloom -u conloom-napcat -f
```

### macOS（launchd）

保存为 `~/Library/LaunchAgents/io.github.kinako404.conloom.plist`（路径改成你的实际目录）：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.kinako404.conloom</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/<你的用户名>/conloom/conloom</string>
    <string>serve</string>
    <string>-c</string>
    <string>/Users/<你的用户名>/conloom/conloom.yaml</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/<你的用户名>/conloom</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/Users/<你的用户名>/conloom/conloom.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.kinako404.conloom.plist
```

Adapter 同理：复制一份，`Label` 改为 `io.github.kinako404.conloom-napcat`，参数改为 `adapter napcat -c …`，日志文件另起名字。

### Windows（NSSM）

`conloom.exe` 是普通控制台程序，不实现 Windows 服务控制协议，不能直接用 `sc.exe create` 注册（启动会报 1053）。推荐用 [NSSM](https://nssm.cc/) 包装：

```bat
nssm install ConLoom C:\conloom\conloom.exe serve -c C:\conloom\conloom.yaml
nssm set ConLoom AppDirectory C:\conloom
nssm set ConLoom AppStderr C:\conloom\conloom.log
nssm start ConLoom

nssm install ConLoomNapCat C:\conloom\conloom.exe adapter napcat -c C:\conloom\conloom.yaml
nssm set ConLoomNapCat AppDirectory C:\conloom
nssm set ConLoomNapCat AppStderr C:\conloom\conloom-napcat.log
nssm set ConLoomNapCat DependOnService ConLoom
nssm start ConLoomNapCat
```

## 数据与迁移

全部状态在一个数据目录里，全部配置在 `conloom.yaml` 加可选的 `.env`，二进制没有其他依赖：

```text
<data_dir>/
  conloom.db         SQLite 数据库（运行时旁边还有 conloom.db-wal、conloom.db-shm）
  napcat-dedup.log   NapCat Adapter 的已发送记录（按 action_id 去重，保留 24 小时）
```

`napcat-dedup.log` 写在 `adapters.napcat.state_dir` 下，默认与 `storage.data_dir` 相同；Adapter 部署在另一台机器时，它在 Adapter 那台机器上。

迁移到另一台机器：

1. 在旧机器上导出一致快照（服务运行中也可以执行）：

   ```sh
   ./conloom backup -c conloom.yaml -o conloom-backup.db
   ```

2. 在新机器下载对应平台的二进制，拷贝 `conloom.yaml`、`.env`、备份文件，以及 `napcat-dedup.log`（若 Adapter 也一起迁移）。
3. 把备份文件放到新机器的 `<data_dir>/conloom.db`（确认该目录下没有遗留的 `conloom.db-wal`、`conloom.db-shm`），`napcat-dedup.log` 放到新机器的 `adapters.napcat.state_dir`（默认即 `<data_dir>`）。按上文 systemd 布局部署时，恢复的文件要交给服务用户：

   ```sh
   sudo chown conloom:conloom /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   sudo chmod 600 /opt/conloom/data/conloom.db /opt/conloom/data/napcat-dedup.log
   ```

4. 停止旧实例（先停 Adapter 再停核心），启动新实例（先启核心再启 Adapter）。

备份之后到停止旧实例之前收到的消息不会出现在备份里；若要一条不丢，可以先停止旧实例再执行 `conloom backup`（服务停止时同样可用）。核心与 Adapter 可以分别迁移：Adapter 只需能访问 NapCat 与核心 API 地址。数据库的恢复方法始终是把备份文件放到 `<data_dir>/conloom.db`。

## 基准

在 Intel Xeon E5-2680 v4 @ 2.40GHz（56 线程，linux/amd64）上运行 `go test -run '^$' -bench . -benchtime 2s ./internal/app/` 的原始结果：

```text
goos: linux
goarch: amd64
pkg: github.com/kinako404/conloom/internal/app
cpu: Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz
BenchmarkIngestObserve-56    	    3386	    730560 ns/op	   28351 B/op	     393 allocs/op
BenchmarkMentionToAck-56     	     439	   5386188 ns/op	  316897 B/op	    4327 allocs/op
PASS
ok  	github.com/kinako404/conloom/internal/app	6.692s
```

两项都是经过真实 Gateway、SQLite（WAL，磁盘临时目录）与 Go 客户端的完整 HTTP 往返。`BenchmarkIngestObserve` 并行提交只落库不回复的群消息，约 0.73 ms/条（所有写入由 SQLite 单写连接串行化，约 1.4k 条/秒）；`BenchmarkMentionToAck` 顺序测量一条 @Bot 消息从提交到回复被 Outbox 拉取并 ACK 的全过程，约 5.4 ms，其中模型是立即返回的假服务、合并窗口为 0。真实环境中的回复延迟主要取决于模型本身（通常为秒级）。

## 隐私

- 日志默认**不记录**消息正文，只记 ID、长度、会话、路由与耗时；本地调试时可设 `observability.log_message_content: true`。
- 主动搭话的兴趣画像（每位 @ 过 Bot 的群友最多 50 个关键词及权重）、档位与最近一次 @Bot 的时间保存在 `conloom.db` 中，不写入日志，也不会出现在群消息里；加入群聊时只把在场目标资格人合并后的前 5 个兴趣词放进发给模型的提示。在聊会话与停顿计时器只在内存里，不落库。
- 只有 `enabled_conversations` 中的会话（以及按 `private_chat` 开放的私聊）的消息会被保存；其他会话的消息既不转发也不落库。
- 消息保存在本机 `<data_dir>/conloom.db` 中，用作对话上下文；请像对待聊天记录一样保护数据目录和备份文件。
- 组装好的上下文会发送到你配置的模型 endpoint；`privacy.data_location: remote` 表示数据会离开本机。
- 模型输出会剥离平台控制码，并对密钥形态的字符串与配置中的任何密钥打码后才发送。

## 开发

```sh
go vet ./...
CGO_ENABLED=1 go test -race ./...                        # -race 需要 cgo 与 C 编译器
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build ./cmd/conloom   # 发布构建一律不用 cgo
```

推送 `v*` 标签时，GitHub Actions 用 GoReleaser 构建六个平台的压缩包与 `checksums.txt` 并发布到 Releases。

## 许可证

[MIT](LICENSE)
