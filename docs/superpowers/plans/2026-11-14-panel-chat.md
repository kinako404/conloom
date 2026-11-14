# 面板聊天 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 让面板成为聊天客户端，覆盖两种用法：
- 接管已有私聊的用户一方。机器人对面板消息的回复只投递到面板，原平台收不到。
- 在 `panel` 平台新建、删除测试聊天。

**Architecture:**
- 消息和发件箱动作新增 `channel` 字段：空字符串表示原平台，`panel` 表示面板。
- 任务的通道取它最新一条触发消息的通道。
- 适配器只拉取 `channel=''` 的动作；core 里新增一个"面板投递器"，负责拉取并确认 `channel='panel'` 的动作。
- 面板通过 core 的 `PanelChat` 方法提交消息、新建和删除聊天，并查询会话是否正在回复。

**Spec:** `docs/superpowers/specs/2026-11-14-panel-chat-design.md`（含 §7 补充决定）

## Global Constraints

- 不新增依赖。`CGO_ENABLED=0` 能编译。全量 `go test -race`、`go vet`、`gofmt -l .` 都要干净。
- 迁移编号 `0011`。新增的 ID 一律不带冒号：发言人 `panel_<面板用户名>`，会话 `panel_<随机>`，面板合成的平台消息 ID `panel_<随机>`。
- 常量 `store.ChannelPanel = "panel"`，`store.PlatformPanel = "panel"`。
- 面板消息文本长度为 1 到 2000 个字符，超出返回 400。只支持私聊，群聊返回 400。
- 删除只允许 `panel` 平台的会话，保留 `model_calls` 和 `audit_events`。
- 公开仓库：测试只用假数据，不出现角色名。

## Review Focus

1. **回复投错通道**：面板消息的回复被适配器拉到，或者平台消息的回复进了面板。
2. **两个通道互相阻塞**：同一会话里，一个通道的队首卡住另一个通道。
3. **面板消息绕过审查**：没有经过输入审查、限流、内容模式或私聊等停顿。
4. **删除不干净或删多了**：删除后仍有残留，或者误删了其他会话的数据。
5. **合并后的通道判定**：面板消息和平台消息合进同一个任务时，按最新一条决定通道。

## 分批

- 第 1 批：P1。
- 第 2 批（并行）：P2、P3。
- 第 3 批：P4。

---

### Task P1：存储层的通道、面板投递查询和删除会话

**Files:**
- 新建 `internal/store/sqlite/migrations/0011_channel.sql`：`messages.channel`、`outbox_actions.channel`，都是 `TEXT NOT NULL DEFAULT ''`
- `internal/store/records.go`：`MessageRecord.Channel`、`OutboxRecord.Channel`、两个常量
- `internal/store/sqlite/ingest.go`：`insertMessage` 写入 `channel`
- `internal/store/sqlite/tasks.go`：`insertOutbox` 写入 `channel`
- 读消息和发件箱的 scan 都要读出 `channel`
- `internal/store/sqlite/outbox.go`：
  - `LeaseOutbox` 只看 `channel = ''`，队首也按这个通道判断；
  - 新方法见下。
- 新建 `internal/store/sqlite/delete.go`
- `internal/store/store.go`：接口新增方法
- `internal/outbox/outbox.go`：`Ack` 生成机器人消息时，把 `Channel` 设为动作的 `Channel`
- 测试：`outbox_test.go`、`delete_test.go`、`ingest_test.go`

**Produces（`store.Store` 新增）：**

```go
// LeasePanelOutbox leases due actions with channel "panel" across all bots: per
// conversation only the head of that channel, same not_before/lease rules as LeaseOutbox.
LeasePanelOutbox(ctx context.Context, limit int, now time.Time, lease time.Duration) ([]OutboxRecord, error)
// PendingPanelActions counts the conversation's pending or leased panel-channel actions.
PendingPanelActions(ctx context.Context, convKey string) (int, error)
// DeleteConversation deletes everything stored under convKey (messages and their
// keyword index rows, tasks, outbox actions, personas, policies, members, memory,
// chunks, proactive log, users only referenced by this conversation, events of it)
// except model_calls and audit_events, then the conversation row. ErrNotFound if absent.
DeleteConversation(ctx context.Context, convKey string) error
```

- [ ] **测试**：
  - `TestLeaseOutbox_SkipsPanelChannel`：同一个会话里，面板队首没到时间，平台动作照样能被拉取；反过来也一样。
  - `TestLeasePanelOutbox_HeadOfLineAndNotBefore`。
  - `TestAck_BotMessageKeepsChannel`：放在 `internal/outbox` 包。
  - `TestIngest_MessageChannelRoundTrip`。
  - `TestDeleteConversation_ClearsOnlyThatKey`：两个会话各种表都有数据，删除其中一个后：
    - 它在所有表里都没有行，FTS 也查不到；
    - `model_calls` 和 `audit_events` 保留；
    - 另一个会话的数据完整无缺。
  - `TestMigration0011_ExistingRowsArePlatformChannel`。
- [ ] 提交：`feat(store): message and outbox channels, panel leasing and deleting a conversation`。

### Task P2：core 的面板聊天、通道判定和面板投递器

**Files:**
- 新建 `internal/core/panelchat.go`
- `internal/core/orchestrator.go`：
  - `newAction`、`newActions` 设置 `Channel`，取最新一条触发消息的通道；
  - 统一管理员判断；
  - 删除时清理调度器和各类缓存。
- 管理员判断的另外几处：`internal/core/commands.go`、`internal/core/skills.go`。
- 白名单放行：`internal/core/decide.go`、`internal/config/config.go`（`PrivateAllowed` 增加平台参数，或者新增 `PrivateAllowedOn(platform, uid)`）、`internal/memory/scheduler.go`、`internal/search/vector/indexer.go`。
- 新建 `internal/outbox/panel.go`，实现 `PanelDeliverer`。
- 测试：`internal/core/panelchat_test.go`、`internal/outbox/panel_test.go`。

**Consumes：** P1 的方法和 `Channel` 字段。

**Produces：**

```go
// core
func (o *Orchestrator) SubmitPanel(ctx context.Context, convKey, panelUser, text string) (messageID string, err error)
func (o *Orchestrator) CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (convKey string, err error)
func (o *Orchestrator) DeletePanelChat(ctx context.Context, convKey string) error
func (o *Orchestrator) Busy(convKey string) bool // queued or running tasks in the scheduler
var ErrNotPrivate, ErrNotPanelChat, ErrNoUserYet, ErrBadText error

// outbox
type PanelDeliverer struct{ /* … */ }
func NewPanelDeliverer(svc *Service, st store.Store, interval time.Duration, now func() time.Time, log *slog.Logger) *PanelDeliverer
func (d *PanelDeliverer) Start()
func (d *PanelDeliverer) Wake()
func (d *PanelDeliverer) Close()
```

**`SubmitPanel` 的规则：**
- 找不到会话返回 `store.ErrNotFound`；不是私聊返回 `ErrNotPrivate`；文本为空或超过 2000 字返回 `ErrBadText`。
- 发言人：
  - `panel` 平台的会话，发言人 ID 和平台用户 ID 都是 `panel_<panelUser>`，显示名取用户表里的记录；
  - 其他平台的会话，用最近一条用户消息的平台用户 ID 和显示名；没有用户消息时返回 `ErrNoUserYet`。
- 通过与 `Submit` 相同的内部流程入库和调度：幂等键、输入审查、等停顿、限流都照常执行。消息的 `Channel` 为 `panel`，平台消息 ID 为 `panel_<随机>`。
- 返回新消息的 ID。

**`CreatePanelChat`：**
- 机器人不存在返回 `store.ErrNotFound`；显示名去掉首尾空白后为空，就用 `panelUser`，最长 32 字。
- 建会话 `panel_<随机>` 和用户 `panel_<panelUser>`，然后返回会话键。

**`DeletePanelChat`：**
- 不是 `panel` 平台返回 `ErrNotPanelChat`。
- 先取消该会话的活动任务，清掉调度会话以及人设、策略的缓存，再调用 `DeleteConversation`。

**通道判定：** `newActions` 以最新一条触发消息的 `Channel` 为准；没有触发消息时，`panel` 平台的会话用 `panel`，其他平台用空字符串。

**管理员判断：** 新增 `func isAdmin(bot config.BotConfig, platform, uid string) bool`，`platform == panel` 时一律返回 true。原来四处各自判断的地方都改用它。

**面板投递器：**
- 每隔一个间隔（默认 1s），或者被 `Wake` 唤醒时，调用 `LeasePanelOutbox`。
- 对拉到的每个动作调用 `svc.Ack(sent, PlatformMessageID = "panel_"+actionID)`。
- `Close` 时停下循环并等它退出。

**唤醒：** 给 orchestrator 的 `Notifier` 包一层，同时通知发件箱和面板投递器。这一步在 app 装配时完成（P4），P2 只需要提供可以组合的类型。

- [ ] **测试：**
  - 面板消息的回复动作 `Channel = panel`，适配器侧的 `LeaseOutbox` 拉不到，面板投递器确认之后，机器人消息带 `channel = panel`。
  - 平台消息的回复仍是空通道。
  - 合并后的任务以最新一条触发消息定通道。
  - 接管私聊时，发言人是该会话的用户。
  - 没有用户消息时返回 `ErrNoUserYet`；群聊返回 `ErrNotPrivate`；文本过长返回 `ErrBadText`。
  - `panel` 会话里能执行管理员命令，且不受 `private_chat: off` 或白名单的影响。
  - 删除后调度器里没有这个会话，存储里也被清掉。
  - 投递器遵守最早发送时间，被唤醒后能立刻确认。
  - `Close` 之后没有协程泄漏。
- [ ] 提交：`feat(core): panel chats and the panel delivery channel`。

### Task P3：面板接口与前端

**Files:**
- 新建 `internal/panel/chat.go`；在 `internal/panel/write_routes.go`、`data_routes.go` 里注册路由。
- `internal/panel/server.go`：`Deps` 新增 `Chat ChatService`。
- 前端：新建 `internal/panel/web/views/chat.js`；修改 `views/conversation.js`（新增"聊天"标签，`panel` 会话默认打开它，并加删除按钮）、`views/conversations.js`（"新建聊天"）、`style.css`（聊天气泡）。
- 文档：`docs/panel.md`、`docs/api.md`。
- 测试：`internal/panel/chat_test.go`，用一个假的 `ChatService` 和真实的 sqlite。

**Consumes**（接口按 P2 的签名定义在 panel 包里）：

```go
type ChatService interface {
	SubmitPanel(ctx context.Context, convKey, panelUser, text string) (string, error)
	CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (string, error)
	DeletePanelChat(ctx context.Context, convKey string) error
	Busy(convKey string) bool
}
```

错误映射：`ErrNotPrivate`、`ErrNotPanelChat`、`ErrNoUserYet`、`ErrBadText` 返回 400；`store.ErrNotFound` 返回 404。panel 包里通过 `errors.Is` 和 P2 的变量比较，所以 P3 需要从 core 导入它们。如果这样会造成循环依赖，就把这几个错误变量放到 `pkg/conloomapi` 或 `internal/store`，在报告里说明放在了哪里。

**接口：**

| 接口 | 请求 | 响应 |
|---|---|---|
| `POST /panel/api/conversations/{key}/messages` | `{text}` | 202 `{message_id}`；审计 `panel.message` |
| `GET /panel/api/conversations/{key}/chat?after=<seq>` | — | `{messages:[Msg…], typing, next_after}` |
| `POST /panel/api/chats` | `{bot_instance_id, display_name}` | 201 `{key}`；审计 `panel.chat.create` |
| `DELETE /panel/api/conversations/{key}` | — | 204；审计 `panel.chat.delete` |

- `chat` 接口：Msg 在现有字段之外加 `channel`；每次最多返回 200 条，用 `MessagesAfter` 实现。`typing = Busy(key) || PendingPanelActions(key) > 0`。
- Msg JSON 新增 `channel` 字段，现有的消息浏览接口也一并输出。

**前端：**
- "聊天"标签：
  - 每 1.5 秒按 `after` 增量拉取；
  - 气泡分左右；
  - 面板发出的消息带"面板"标记；
  - 显示"对方正在输入…"；
  - 回车发送，Shift+回车换行；
  - 发送失败时保留输入框内容并显示错误；
  - 离开页面时停止轮询。
- "新建聊天"：弹出表单，选择机器人（来自会话或配置里的 bot 列表）并填写称呼，提交后跳到新会话。
- `panel` 会话的删除按钮需要 `confirm()`。
- 不使用 `innerHTML`。

- [ ] **测试：**
  - 四个接口的正常和错误情况，包括 CSRF。
  - `after` 增量拉取。
  - `typing` 的两种来源。
  - 审计内容不含正文。
  - `web_test.go` 照常通过，并对改过的 JS 文件跑 `node --check`。
- [ ] 提交：`feat(panel): chat with the bot from the panel`。

### Task P4：装配和端到端测试

**Files:**
- `internal/app/app.go`：
  - 新建 `PanelDeliverer` 并启动；
  - 用组合的 `Notifier` 唤醒它；
  - 把 orchestrator 作为 `panel.Deps.Chat` 传入；
  - 关闭顺序：投递器在 orchestrator 之后、发件箱和数据库之前关闭。
- 新建 `internal/app/panel_chat_e2e_test.go`。
- 文档：`docs/architecture.md`（通道和面板投递器）。

- [ ] **端到端测试**（假模型加 mock 适配器）：
  - 面板新建聊天并发一句，回复出现在 `chat` 接口里，适配器拉不到；
  - 接管一个已有的私聊：先由适配器发一条，再由面板发一条，面板这条的回复只出现在面板；
  - 随后适配器再发一条，这次回复由适配器拉到，而且模型请求的上下文里包含面板那轮对话；
  - 分条的节奏在面板里同样生效（`max_parts=2`）；
  - 删除聊天后，`GET` 这个会话返回 404。
- [ ] 跑全量测试，然后提交：`feat(app): wire panel chats`。
