# 私聊自主性设计：状态、状态条、主动找人

日期：2026-11-14
状态：已定稿，待实施。部署者确认按讨论中给出的默认方案直接开工；标"实施者决定"的条目是我定的，部署者可以事后改。

## 1. 为什么做

现在私聊里的机器人完全被动：只在对方说话后回复，回复完就"消失"。部署者希望她更像一个有自己生活节奏的人：

- **状态**：对方说"去洗澡吧"，她自己决定洗多久（比如 20 分钟），这段时间她"在洗澡"。对方中途找她，她照常回，但口吻符合正在洗澡；时间到了，她自己回来说一句。
- **状态条**：比如"心情""精力"，对用户隐藏，影响她说话的语气和想不想多聊、想不想主动找人。
- **主动找人**：按对方平时活跃的时段和聊过的内容，在合适的时候主动发消息，或者把聊冷的话题接着往下说。时机到了以后，由她按性格和当时的状态决定说不说，可以选择不说。

## 2. 目标、非目标、反目标

**目标**

1. 只在私聊生效（包括平台私聊和面板聊天），群聊行为完全不变。
2. 状态：模型通过隐藏工具设定"在做什么、多少分钟"；到点由机器人自己发起一轮回复；中途对方说话时，回复带着这个状态。
3. 状态条：每个人设可配置有哪些条，默认"心情""精力"；数值 0–100，随时间回落到基准值；模型通过隐藏工具小幅调整；面板可查看、可修改；用户看不到。
4. 主动找人：私聊里的"主动发言"开关成为总开关；新增每个会话可调的"推进积极性"（关、低、中、高）；有每日上限、免打扰时段、最多追一次、时间带随机。
5. 重启不丢：所有到点要做的事都存在数据库里，进程重启、错过时间都有确定的处理方式。

**非目标**

- 群聊里的主动插话、群聊状态、群聊状态条。
- 让模型自己排日程，比如"明早 8 点叫醒我"这类提醒功能。
- 状态条影响好感度，或者好感度影响状态条。
- 用状态条控制权限、内容规则或任何固定规则。

**反目标**（技术上能跑但算失败）

- 每天固定时间来打卡，或者短时间内连发好几条主动消息。
- 在对方的睡觉时间发消息。
- 对方没回还一直追。
- 主动消息绕过内容规则：保护模式照常生效，标了 `minor` 的人设照旧受保护。
- 把数值、状态条，或"系统""主动""定时"这类字眼说给用户。
- 状态自己串起来：回来后自己又设一个新状态，无限循环。
- 代码、测试、文档里出现具体角色名。

## 3. 开关和策略

**总开关**沿用现有的 `proactive.enabled`。在私聊里，它控制机器人发起的所有消息，包括主动找人、追问和状态结束后回来说话。关掉以后状态照常设定、照常注入提示，只是到点不发消息。

**新字段**放在 `ProactivePolicy` 下，只对私聊有意义，群聊忽略：

```go
Private PrivateProactivePolicy `json:"private,omitzero" yaml:"private,omitempty"`

type PrivateProactivePolicy struct {
    Level      string `json:"level,omitempty" yaml:"level,omitempty"`             // "" | off | low | mid | high；"" 按 mid
    QuietStart string `json:"quiet_start,omitempty" yaml:"quiet_start,omitempty"` // "HH:MM"，server.timezone
    QuietEnd   string `json:"quiet_end,omitempty" yaml:"quiet_end,omitempty"`     // 两个都空 = 按作息推算
}
```

- 字段必须保持可比较，只用字符串，不能有切片或 map，因为 `ProactivePolicy` 在多处用 `==` 比较。
- 校验只检查非空值：`Level` 必须在取值集合里；两个时间必须都能解析成 `HH:MM`，而且要么都填、要么都空。
- `WithDefaults` 不填这些字段，和 `memory.summary_max_chars` 同一个思路："空"表示"按会话类型取默认"，由 core 解析。旧的存档策略不受影响。
- **默认 `mid`**（部署者在讨论中认可）。目前所有私聊的 `enabled` 都是 true，所以部署后功能会直接生效。

| 档位 | 每日上限（主动 + 追问合计） | 上一条消息之后至少隔 |
|---|---|---|
| 关 | 0 | — |
| 低 | 1 | 8 小时 |
| 中 | 2 | 4 小时 |
| 高 | 4 | 2 小时 |

数值写成 core 里的常量，不进策略。状态结束后回来说话不计入上限。

**状态和状态条的开关**是机器人的技能列表：`bots[].skills` 里有 `status`，才提供 `set_status` 工具；有 `buff`，才提供 `adjust_buff` 工具，并在提示里注入状态条。没启用的机器人，提示和行为与现在完全一样。

**暂停的会话**（`max_interactive_requests: 0`）不发起任何消息。

**`/主动` 命令在私聊里的用法**（现在私聊里会被拒绝）：

- `/主动`：显示当前档位、今天已发次数和上限、免打扰时段（注明是推算的还是设定的）。总开关关闭时说明原因。
- `/主动 关|低|中|高`：设置本会话的档位，会话里的人就能设，因为这是他自己的私聊。通过 `policy.Service` 新增的 `SetPrivateLevel` 写入，生成新的策略版本并记审计。
- 总开关和免打扰时段只能在面板或 API 里改。

## 4. 状态

**工具** `set_status(activity, minutes)`，属于技能 `status`，`conversations: [private]`：

- `activity`：清理后 1–16 个字。清理包括：只保留一行，去掉控制字符，去掉 `【】`。清理后的内容要过输出审查：保护模式用 `OutputProtected`，开放模式用 `Output`。审查不通过时返回"这个状态不能用。"，不写库。
- `minutes`：整数 0–240，也接受 `20.0` 和 `"20"`。0 表示结束当前状态，同时取消回来说话的定时。
- 同一时间只有一个状态，新设的覆盖旧的。写状态和设定"到点回来"的定时在同一个事务里完成。
- 工具在执行时还要再确认一次会话是私聊。
- 返回给模型的结果示例："已记下：洗澡，约 20 分钟（到 20:30）。到时你会自己回来说话。"总开关关闭时，后半句换成"时间到了状态会自动结束。"
- 只在用户触发的私聊回复里提供（含面板聊天）。机器人自己发起的回合**不提供**，这样状态不会自己串起来。

**技能说明**（SKILL.md 正文，600 字以内，不含任何动态数值）要讲清楚：

- 什么时候用：对方让你去做一件需要一段时间的事，或者你按自己的性格决定去做点什么。
- 一两分钟的小事不用设。
- 状态期间对方找你，照常回复，但要符合正在做的事。
- 对方叫你提前回来时，用 `minutes=0` 结束状态。

**状态生效的判断**：`until_at > now`。过了时间的状态不再注入，即使回来说话的那一轮因为总开关关闭等原因没有执行。

## 5. 状态条

**定义**写在人设里，和人设版本一起冻结：

```go
Bars []PersonaBar `json:"bars,omitempty" yaml:"bars,omitempty"`

type PersonaBar struct {
    Name        string   `json:"name" yaml:"name"`                                   // 1–6 字，唯一，不含空白、【】、冒号
    Baseline    int      `json:"baseline" yaml:"baseline"`                           // 0–100
    HalfLife    Duration `json:"half_life" yaml:"half_life"`                         // 30m–7d
    Description string   `json:"description,omitempty" yaml:"description,omitempty"` // ≤40 字，例如"高=开心放松，低=低落烦躁"
}
```

- 最多 4 条。人设没写 `bars` 时，运行时用默认的两条：心情（基准 60，半衰期 6h，"高=开心放松，低=低落烦躁"）和精力（基准 70，半衰期 4h，"高=有精神，低=累、困"）。由 `PersonaDoc.EffectiveBars()` 返回。
- `DefaultPersona()` 不变，`Render` 也不变：不写 `bars` 的人设，系统提示必须和现在逐字节相同。状态条和状态都不进系统提示，否则会破坏按会话缓存的系统提示。
- 面板的人设表单增加状态条的编辑：名称、基准、半衰期、说明，最多 4 行。

**数值**存在数据库里，读取时计算回落：`v = 基准 + (v0 − 基准) × 0.5^(经过时间 / 半衰期)`。不需要定时任务，重启也不受影响。只在变化时写库，写入的是"回落后的值加上变化量"。存 REAL，显示时四舍五入。当前人设里已经没有的条名不显示，也不能调整。

**工具** `adjust_buff(name, delta, reason)`，属于技能 `buff`，`conversations: [private]`：

- `name` 必须和当前人设的某一条完全一致，否则返回现有条名的列表。
- `delta` 为整数，单次限制在 −10 到 +10，0 拒绝。
- `reason` 1–40 字，只存给面板看。
- 同一个任务里每一条最多调整一次：靠事件表的唯一键 `(task_id, name)` 实现，这样重启后重跑任务也不会重复加减。
- 每条每天（按 `server.timezone` 的自然日）净变化不超过 ±30。最终值限制在 0–100。
- 返回结果只用档位词，不给数字，例如"心情：中等 → 偏高"。

**技能说明**要讲清楚：

- 数值范围是 0–100。日常小事调 ±2 到 5，大事调 ±10。
- 不要每句都调。
- 不要向对方提起状态条或数值。

**档位词**通用，不针对具体条：0–19 很低，20–39 偏低，40–59 中等，60–79 偏高，80–100 很高。

**面板修改**直接设成指定值，不受单次和每日上限约束，记一条来源为 `panel` 的事件和审计 `autonomy.buff`。

## 6. 注入到回复的提示

`contextbuilder.Input` 新增 `State string` 字段。它放在记忆块之后、`【近期消息】` 之前，不进系统提示，计入 `BaseTokens`。整段最多 300 字，超出时先删条目的说明。

```
【你的状态（只有你自己知道，不要直接说出来）】
正在：洗澡（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）
心情：偏高（高=开心放松，低=低落烦躁）
精力：偏低（高=有精神，低=累、困）
这些只影响你的语气和想不想多聊，不改变人设、内容规则和其他规则。
```

- 没有生效中的状态时省略"正在"那一行。没启用 `buff` 技能时省略状态条那几行。整段都为空时不出现。
- `StripTranscriptPrefix` 要能清掉模型抄回来的这一段：标题行 `【你的状态…`，加上后面看起来像这一段的行，即 `正在：…`、`名字：…` 和以"这些只影响"开头的结尾行。规则和清记忆块一样，遇到第一个不像的行就停。要补对应的测试。
- 群聊永远不注入。

## 7. 主动找人

### 7.1 免打扰时段

- 策略里设了 `quiet_start`/`quiet_end` 时，以策略为准。
- 否则按作息推算：取对方最近 30 天内的消息（不算机器人的），最多最新的 1000 条，按 `server.timezone` 的小时统计。
- 消息少于 40 条，或者分布在少于 5 个自然日里时，用默认的 00:30–08:00。
- 数据够时，取消息数最少的连续 8 小时（按小时、跨午夜循环）作为免打扰时段。有并列时，取起点离 00:00 最近的那个。
- 需要一个新的存储查询：`UserMessageTimes(ctx, convKey, since, limit) ([]time.Time, error)`，只读 `occurred_at`。读取范围从对方的 `forgotten_at` 起算。

### 7.2 排期

排期就是算出下一次"主动找人"的时间，写成一条 `initiate` 定时。

- **什么时候排**：
  - 私聊里对方每发一条消息，先取消已有的 `initiate`/`follow_up` 定时，再重新排。
  - 机器人发起的一轮被跳过或被拦下后，重新排。
  - 策略变化（总开关、档位、免打扰时段）后，重新排。
  - 启动时不扫描全部会话补排：现有会话在对方下一次发消息时开始排（实施者决定）。
- **不排的情况**：总开关关闭、档位为关、会话已暂停、对方已不在 `private_chat` 允许范围内、面板新建的聊天（平台为 `panel`）、追问额度已用完（见 7.3）。
- **算法**：
  1. `earliest = max(now, 最后一条消息的时间 + 间隔(档位))`，最后一条消息不分双方。
  2. 从 `earliest` 起往后 24 小时，每 15 分钟一个候选点。去掉落在免打扰时段里的点，去掉当天已到上限的那些日子里的点。
  3. 每个点的权重 = 0.2 + 该小时在对方作息直方图中的占比 × 10。数据不够时，所有不在免打扰时段的点权重相同。
  4. 按权重随机选一个点，再加 0–14 分钟的随机偏移。没有可选的点就不排。
  5. 随机数源可以注入，方便测试。
- 排期的时候不看状态条（实施者决定）。到点以后，模型会看着状态条决定说不说。

### 7.3 追问和上限

- "追问额度"：对方最后一条消息之后，机器人发起的主动和追问（`proactive_log` 里的 `private_initiate` 和 `private_followup`）合计最多 2 条，也就是主动 1 次、追 1 次。用完以后不再排，直到对方开口。这个判断直接从日志计算，不另存状态。
- 主动消息发出后，排一条 `follow_up` 定时：1.5 到 4 小时之后的随机时间，跳过免打扰时段，落进去就顺延到时段结束后再加随机偏移。
- 追问被模型跳过，或者没发出去，都不再追。
- 每日上限按 `private_initiate` 加 `private_followup` 的当日条数计算，用现有的 `underLimit` 和 `dayStart`。

### 7.4 到点时的检查

`initiate`/`follow_up` 到点后按顺序检查，任何一项不满足都按括号里的方式处理：

1. 会话还在，是私聊，不是面板新建的聊天（否则丢弃）。
2. 总开关开、档位不为关、会话没暂停、对方仍在允许范围内（否则丢弃）。
3. 现在不在免打扰时段（否则重新排）。
4. 当天还没到上限（否则重新排，会落到之后的日子）。
5. 追问额度还够：`initiate` 要求对方开口后机器人还没发起过，`follow_up` 要求正好发起过 1 次（否则丢弃）。
6. 对方在定时设好之后没有新消息（否则丢弃，因为他发消息时已经重新排过）。
7. 会话里没有正在进行的任务，并且对方最后一条消息距今超过 `PrivateMaxWait`（否则顺延 2 分钟）。

`status_return` 到点后：

- 总开关关闭或会话已暂停时，清掉状态，不发消息。
- 会话忙时顺延 1 分钟，最多顺延到原定时间之后 30 分钟，再晚就清掉状态、不发消息。
- 不检查免打扰时段和上限，因为这件事是在聊天里说好的。

**错过的定时**：进程停过，到点时已经晚了超过 30 分钟：

- `status_return`：清掉状态，不发消息。
- `initiate`：重新排。
- `follow_up`：丢弃。

## 8. 定时唤醒机制

### 8.1 为什么要新做

- outbox 的 `not_before`：同一个会话里，排在前面、时间还没到的动作会堵住后面所有回复；动作 10 分钟过期；而且只能发提前写好的文字。
- 任务表：调度器按顺序处理，一个还没到时间的任务会堵住后面的任务；重启后，事件时间超过 2 分钟的任务会被判为过期。
- 冷场计时器只在内存里，重启就没了。

### 8.2 表

一个会话、一种定时只有一行：

```sql
CREATE TABLE conversation_timers (
  conversation_key TEXT NOT NULL,
  kind TEXT NOT NULL,              -- status_return | initiate | follow_up
  bot_instance_id TEXT NOT NULL,
  platform_user_id TEXT NOT NULL,
  due_at TEXT NOT NULL,
  anchor_seq INTEGER NOT NULL,     -- 设定时会话里最新消息的 seq
  lease_until TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (conversation_key, kind)
);
CREATE INDEX conversation_timers_due ON conversation_timers (due_at);
```

### 8.3 轮询和触发

- orchestrator 里起一个后台协程，和 `maintain` 一样挂在 `bgWG` 上，在 `Close` 里停止。每 15 秒用 `o.now()` 查一次到点的定时。
- 领取定时时加 2 分钟的租约（`lease_until`），重启后没处理完的行能被重新领取。
- 检查都通过后创建任务，做法参照 `createJoinTask`：
  - 内部事件键为 `conloom:timer:<会话>:<种类>:<due_at>`，同一个定时触发两次也只建一个任务。
  - 在同一个事务里删除这条定时，条件是会话、种类、`due_at` 三者都对得上。删不到（已被取消或改期）就整个回滚、不建任务。具体是给 `Ingest` 加一个可选字段，还是另起一个事务，由实施者决定。
  - 任务的 trigger 是新增的三种之一：`TriggerStatusReturn`、`TriggerInitiate`、`TriggerFollowUp`。另外提供 `isWake(trigger)`。
  - 不带触发消息。`EventOccurredAt` 设为触发时间，避免重启后被判为过期。优先级为后台。
  - 用 `sched.Restore` 放进调度器，调度器里的 actor 用 `"wake:"+用户ID`。这样对方的新消息不会合并进这个任务，也不会顶替它。`recoverTask` 和 `/stop` 也要认这个 actor。

### 8.4 对方先开口

对方在私聊里发消息（不算被忽略的消息）时，`submit` 里做三件事：

1. 删除这个会话的 `initiate`/`follow_up` 定时。还没到点的 `status_return` 定时保留。
2. 取消这个会话里所有已经建好、还没完成的唤醒任务（三种都算），并取消它们还没发出的分条，做法同 `cancelUnsentParts`，记审计。被取消的如果是 `status_return`，同时清掉状态，因为马上要回复对方了。
3. 重新排 `initiate`。

和 `finish` 里的复查用同一把会话锁。只剩一种来不及撤回的情况：第一条分条已经交给了平台。

## 9. 机器人发起的一轮

`execute` 对唤醒任务的处理：

- 没有触发消息：跳过读取触发消息，也跳过"全部被忘记就丢弃"这一步。说话人取会话里对方最新的显示名。所有读 `triggers[len-1]` 的地方都要防空：`directive`、`speaker`、`newSkillRun`、`injectMemory`。
- 历史、记忆块、状态块和普通私聊回复一样，历史从对方的 `forgotten_at` 起算。
- `contextbuilder` 在没有当前消息时，不输出 `【当前消息】` 标题，只输出提示。
- 按非直接回复处理：模型只抄了格式时视为跳过，而不是失败。不发"暂时不可用"之类的错误文案。
- 工具：只提供 `adjust_buff`（启用了 `buff` 时）。不提供 `set_status`，也不提供记忆类工具，避免拿旧消息当触发写记忆。
- 输出走现有的审查：保护模式用 `OutputProtected`，开放模式用 `Output`。投递走 `replyChannel` 和现有的分条。
- 不计入好感度的互动（`noteInteraction` 跳过唤醒任务）。
- `finish` 时，在会话锁下复查：任务建好以后对方有没有发新消息，`initiate`/`follow_up` 是否还在上限内。有新消息就作废，不发。
  - 发出后写日志：`private_initiate` 或 `private_followup`。
  - 被跳过时写 `private_skip`，不计入上限。
  - 然后按 7.2、7.3 排下一步。
  - `status_return` 结束时（不论发出还是跳过）清掉状态，条件是状态的 `until_at` 和这次一致。

**提示文案**放在 `copy.go`，只用对方的显示名，不出现任何角色名。保护模式下多加一句约束。

- 主动：`【提示】现在没人在等你回复，这是你可以主动找 {名字} 说话的机会（这次不受“只回应别人说的”这类习惯的限制）。现在是 {星期几 HH:MM}，你们上次聊天是 {约 N 小时} 前。结合聊天记录、你记得的事和你现在的状态，如果确实有自然想说的——接上没聊完的话题、关心对方提过的事、分享你在做的事——就像平常发消息那样说一两句，不要解释为什么来找对方，不要提“主动”“系统”“提示”。如果会显得突兀、打扰，或者你现在不想说话，只输出 [[SKIP]]。`
- 追问：`【提示】你上次主动找 {名字} 后，对方还没回，可能在忙。可以再轻轻说一句，不要催促或抱怨；不合适就只输出 [[SKIP]]。`
- 回来：`【提示】你刚做完「{activity}」，回来了。可以自然地说一句（说声回来了，或者接上离开前的话题）；如果这段时间你们一直在聊、或者话题已经结束，只输出 [[SKIP]]。`
- 保护模式在主动和追问后面加：`不要表现出恋爱或暧昧的想念。`

文案的措辞可以由实施者打磨，但上面列出的约束不能少。

## 10. 存储

迁移 `0012_autonomy.sql`，在 8.2 的定时表之外再加三张表：

```sql
CREATE TABLE conversation_status (
  conversation_key TEXT PRIMARY KEY,
  activity TEXT NOT NULL, started_at TEXT NOT NULL, until_at TEXT NOT NULL,
  task_id TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE conversation_buffs (
  conversation_key TEXT NOT NULL, name TEXT NOT NULL,
  value REAL NOT NULL, day TEXT NOT NULL, day_delta INTEGER NOT NULL, updated_at TEXT NOT NULL,
  PRIMARY KEY (conversation_key, name));
CREATE TABLE conversation_buff_events (
  id INTEGER PRIMARY KEY, conversation_key TEXT NOT NULL, name TEXT NOT NULL,
  task_id TEXT NOT NULL, source TEXT NOT NULL,      -- model | panel | api
  before REAL NOT NULL, after REAL NOT NULL, reason TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE UNIQUE INDEX conversation_buff_events_task ON conversation_buff_events (task_id, name) WHERE task_id <> '';
CREATE INDEX conversation_buff_events_conv ON conversation_buff_events (conversation_key, created_at);
```

- 沿用现有约定：列都是 `NOT NULL`，时间用 `fmtTime` 格式，"读—改—写"放在 `inTx` 里完成。
- 四张表都加进 `conversationTables`，`delete_test` 的 `keyedTables` 和种子数据也要补上。
- `ForgetPrivate`（`/忘记我`，以及面板和 API 的"忘记"）在同一个事务里清掉这个会话的状态、定时、状态条数值和事件。
- 状态条事件保留 30 天，由 `maintain` 清理。

## 11. 面板和 API

- **新标签"自主"**，只在私聊会话里显示：
  - 当前状态：内容、开始和结束时间，以及"结束状态"按钮。
  - 各状态条：回落后的当前值、档位词、基准值。用 `<meter>` 显示，CSP 不允许内联 style。可以输入新值保存。
  - 最近的状态条变化：时间、条名、前后值、原因、来源。
  - 主动找人：总开关、档位、今天已发次数和上限、免打扰时段（注明推算还是设定）、追问额度、下一次定时的种类和时间。
- **策略表单**：私聊会话增加档位选择和免打扰时段的两个输入框。
- **会话列表**：私聊的徽章改为"主动：中"或"主动关"，不再是没有意义的"主动发言开"。
- **人设表单**：增加状态条的编辑。
- **面板接口**：
  - `GET /panel/api/conversations/{key}/autonomy`
  - `PUT …/autonomy/buffs/{name}`，请求体 `{value, reason}`，审计 `autonomy.buff`
  - `DELETE …/autonomy/status`，审计 `autonomy.status.clear`，同时取消回来说话的定时
  - 写接口通过 `Deps` 注入的 core 服务完成，为 nil 时返回 503。非私聊会话返回 400。
- **网关**镜像同样三个接口，路径在 `/api/v1/conversations/{id}/autonomy` 下，复用 `memory:read`/`memory:write` 权限。
- 同步更新 `docs/api.md`、`docs/panel.md`。

## 12. 指标和日志

- **指标** `conloom_wake_total{kind,result}`，`result` 取 `sent`、`skipped`、`dropped`、`postponed`、`missed`、`cancelled`。不改动现有 `conloom_proactive_total` 的标签。
- **日志**：定时的设定、触发、丢弃、顺延各记一条 info，带会话、种类和时间，不记内容。工具调用沿用现有的 skill 调用日志。

## 13. 文档

需要更新：

- `docs/architecture.md`：定时机制、唤醒任务流程、状态块。
- `docs/configuration.md`：人设的 `bars`，策略的 `proactive.private`。
- `docs/usage.md`：私聊里的 `/主动`，主动找人的规则。
- `docs/skills.md`：两个新工具，以及"机器人发起的回合只提供 `adjust_buff`"。
- `docs/api.md`、`docs/panel.md`。
- `conloom.example.yaml`：在技能列表的注释里加上 `status`、`buff`；加一段注释掉的人设 `bars` 示例，用通用名称；`default_private_policy.proactive.private` 写上注释和默认值。

## 14. 考虑过的做法

- **到点执行**：
  - 选了：新表加轮询。能扛重启，错过的时间有确定的处理，取消就是删一行。
  - 内存定时器：重启就丢。
  - outbox 延时：会堵住后面的回复，而且文字只能提前写死。
  - 任务表放未来时间：会堵住后面的任务，重启后还会被判为过期。
- **状态和状态条怎么写入**：
  - 选了：工具调用。参数有类型，可以校验、加上限、做到幂等。
  - 让模型在回复里输出标记再解析：容易漏给用户，格式也不稳定。
  - 记忆整理时推断：要等几十条消息，太慢。
- **状态条怎么回落**：
  - 选了：读的时候按时间计算。
  - 定时任务：多一个后台作业，重启后还要补算。
- **什么时候主动**：
  - 选了：按作息加权随机，加上档位和上限的硬约束，到点后由模型自己决定说不说。
  - 固定时刻：就是打卡，属于反目标。
  - 完全交给模型排日程：不可控，也没法限流。

## 15. 留给实施者

- 权重函数、随机偏移等细节可以微调，但不能违反第 7 节的硬约束。
- 文案措辞（第 9 节）可以打磨。
- 面板的布局。
- `Ingest` 怎么在同一个事务里删除定时。
- 免打扰时段的推算结果是否在内存里缓存一会儿。

## 16. 和部署者未提交改动的关系

部署者的工作目录里有一份还没提交的改动：好感度的【关系提示】，涉及 `Input.Relationship`、`copy.go`、`memory.go`、`runner.go`。本功能基于 `main` 开发，不包含那份改动。

合并时，如果那份改动仍未提交：先 stash，再 fast-forward 合并，然后 stash pop 并解决冲突。冲突解决后，最后一条 user 消息的顺序是：记忆块 → 关系提示 → 状态块 → 近期消息。不替部署者提交那份改动。
