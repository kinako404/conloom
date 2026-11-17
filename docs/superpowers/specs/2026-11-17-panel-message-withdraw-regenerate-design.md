# 面板聊天：撤回、重新生成、右键删除聊天

日期：2026-11-17
状态：已定稿，部署者已确认“做”。标“实施者决定”的条目是我定的，部署者可以事后改。评审后修订（同日）：重新生成只限普通回复（部署者选方案 A），撤回挡住进行中的向量化写入和唤醒定时，消息不存在单独返回 `MESSAGE_NOT_FOUND`，见各节标“评审后”的条目。

## 1. 要做什么

面板聊天页（经典页和 TG 风格页）增加三件事：

- **右键删除聊天**：左栏聊天记录右键，菜单里“删除这个聊天”。
- **撤回**：消息气泡上的“撤回”，删除这条消息和它之后的所有消息。
- **重新生成**：bot 最新一条回复上的“重新生成”，删掉这轮回复，用前面那条用户消息重新生成，不新增一条用户消息。

只对面板聊天（`platform=panel`）开放；QQ、Telegram 的平台聊天不做。经典页和 TG 风格页都加。

右键在触屏上不可靠，所以两页都保留可见的删除入口：经典页在聊天标题旁加“删除”，TG 页沿用 ⋮ 菜单里的“删除这个聊天”。

## 2. 行为

### 右键删除聊天

- 在左栏聊天项上右键，弹出“删除这个聊天”；点击后走现有的 `deleteChatConfirmed`：确认框、`DELETE conversations/{key}`、409 `CHAT_BUSY` 的提示都不变。
- 删除的是当前打开的聊天时，回到聊天列表，然后刷新左栏。
- 列表本来就只列面板聊天（`listChats` 过滤 `PlatformPanel`），所以不用再判断平台。

### 撤回

- 任何一条消息（用户或 bot）都可以撤回。
- 删除这条消息和它之后的所有消息，不能恢复。
- 确认框写明会删掉多少条，并提示：已经整理进摘要/笔记的内容不会跟着撤回；要彻底清掉用 `/忘记我`。
- bot 正在回复、或还有没投递完的分条时，返回 409 `CHAT_BUSY`；UI 在 `botTyping` 时禁用按钮。
- 成功后重新加载聊天页（重新走路由，重置轮询游标）。

### 重新生成（实施者决定：只允许最新一条 bot 回复；评审后，部署者决定：只允许普通回复）

- 只有聊天里最新的那条消息是 bot 对用户消息的普通回复时，才能重新生成，也才显示“重新生成”。老 bot 消息不显示；要改老回复就先撤回它（会删掉它之后的所有消息）。
- 评审后：“普通回复”由产生这条消息的任务判断：面板 bot 消息的平台消息 ID 是 `panel_<动作 ID>`，动作 → 任务；任务必须是用户私聊消息触发的回复任务（`Meta.Trigger = private`、不是命令、有触发消息），不是唤醒（`initiate`/`follow_up`/`status_return`）、主动或命令任务；触发消息都还在；最后一条触发消息之后的每条消息都是这个任务的分条。否则 400，说明原因（如“它自己发起的消息不能重新生成”）。理由：之前“向前找最近一条用户消息”在 bot 主动发起的消息上会连带删掉更早的回复，并回答一条旧消息。
- 删除这轮 bot 的所有分条（最后一条触发消息之后到末尾），用原任务的全部触发消息（合并的几条都在，评审后）新建一个回复任务；不插入新的用户消息。
- 对方在最后一条触发消息之后被忘记过（面板或 API 的“忘记”）时返回 400：新任务的触发消息都会被运行时丢掉，删掉旧回复却不会有新回复（评审后）。
- 新任务立即排队。聊天页重新加载后显示“对方正在输入…”，回复到了再显示。
- 旧回复触发的工具副作用（`remember`、状态条、状态、主动定时）和已经整理进记忆的内容不回滚；模型调用和用量照常记。
- 目标不是最新 bot 消息、不是普通回复、会话不是面板私聊时返回 400。

## 3. 接口

沿用现有面板 API 的会话路径、CSRF 和鉴权：

| 方法 | 路径 | 结果 |
|---|---|---|
| `DELETE` | `/panel/api/conversations/{key}/messages/{seq}` | 撤回，204 |
| `POST` | `/panel/api/conversations/{key}/messages/{seq}/regenerate` | 重新生成，202 `{"task_id":"…"}` |

- `seq` 必须是该聊天里的一条消息；撤回成功返回删掉的条数（放在审计里，不放在响应体）。
- 只接受 `platform=panel` 的私聊；其他返回 400。
- bot 忙时 409 `CHAT_BUSY`；目标不是最新 bot 消息或不是普通回复（见 §2）、回复的是命令、触发消息在忘记之前时 400；聊天暂停时 409 `PAUSED`（“这个聊天暂停中，不能重新生成。”）。
- 评审后：消息不存在时 404 `MESSAGE_NOT_FOUND`（`conloomapi.CodeMessageNotFound`），聊天不存在时仍是 404 `NOT_FOUND`；前端只在后者把聊天标为已删除。
- 评审后：`GET …/chat` 多返回 `regenerate_seq`：最新一条消息现在能重新生成时是它的 `seq`，否则（包括 `typing` 为 true）为 0；两页只在这条消息上显示“重新生成”，和服务端是否接受一致（`ChatService.RegenerableSeq`）。
- 审计：`panel.message.withdraw`（目标会话键，内容 `seq`、`deleted`）、`panel.message.regenerate`（目标会话键，内容 `seq`、`task_id`）。不记消息正文。

`ChatService` 增加两个方法：

```go
WithdrawPanel(ctx context.Context, convKey string, seq int64) (deleted int, err error)
RegeneratePanel(ctx context.Context, convKey string, seq int64) (taskID string, err error)
RegenerableSeq(ctx context.Context, convKey string) (int64, error) // 评审后
```

## 4. 存储

新增：

```go
// Store
DeleteMessagesFrom(ctx context.Context, convKey string, fromSeq int64) (int, error)
```

`store.Ingest` 增加一个可选字段，让“删旧回复 + 写新任务”在同一个事务里：

```go
type MessageDelete struct {
    ConversationKey string
    FromSeq         int64
}

type Ingest struct {
    // …
    DeleteMessages *MessageDelete
}
```

删除一个范围（`conversation_key = K AND seq >= F`）必须显式处理：

1. `message_fts`：先删掉这些消息的索引行（`DELETE FROM message_fts WHERE rowid IN (SELECT seq FROM messages WHERE conversation_key = ? AND seq >= ?)`）。
2. `memory_chunks`：删掉 `end_seq >= F` 的 chunk；对 `start_seq < F AND end_seq >= F` 的跨切点 chunk，用现有的 `dropChunks` 排队重建；删掉 `memory_chunk_rebuild` 里 `start_seq >= F` 的过期行。
3. `conversation_timers`：删掉 `conversation_key = K AND anchor_seq >= F` 的定时（实施者决定；避免撤回后还按旧消息主动说话）。
4. `messages`：最后删 `conversation_key = K AND seq >= F`。

`seq` 是全局自增且不复用，所以不需要改 `memory_chunk_state.cursor_seq`、`message_fts_progress` 或会话统计；聊天列表的计数和最后消息本来就是实时查 `messages`。

评审后：

- 向量化在读消息和写段之间要等一次向量服务，撤回可能落在中间。删掉了消息时同一个事务里把 `conversations.withdrawals`（迁移 `0020_message_withdrawals.sql`）加一，它是遗忘戳（`ForgetStamp`）的一部分；`WriteChunks` 本来就在遗忘戳变了时整轮跳过（`/忘记我` 的做法），所以删除之前读到的段不会写进去，排队的重建行也留给下一轮。选它而不是“检查段的首末消息还在”，是因为后者要求每次写段都有对应的消息行，和现有的段写入方式不一致。
- `Ingest` 同时带 `DeleteMessages` 和 `NewTask`/`Message` 时，要求它们是同一个会话，否则什么都不写、返回错误。

不处理、也写进文档的遗留（`docs/architecture.md`、`docs/panel.md`、`docs/usage.md` 都写了）：

- `events.raw_event` 里仍有撤回消息的原文，到事件保留期（`idempotency_retention`）满才被清理。
- 已经整理进 `group_memory` 摘要和成员笔记的内容不会回滚。
- 旧任务、已投递的 outbox、`model_calls` 不退。
- 旧回复触发的工具副作用（记忆、状态条、效果、状态）不回滚。
- 评审后记下的决定：撤回删掉锚点在切点之后的 `status_return` 定时时，共用的状态本身不结束（不会再发“回来了”那条消息，状态到期照常结束）；和“状态副作用不回滚”一致，不另外处理。

## 5. 核心

### `WithdrawPanel`

1. `panelBots.notDeleting`，拿 `panelLocks.lock(convKey)`，再确认一次。
2. `panelConversation`，要求 `PlatformPanel` 且是私聊。
3. 评审后：`holdWakes`——像收到用户消息时一样 `speakers.begin(convKey)`，再拿一下 `proactiveLocks` 等正在启动的唤醒任务入队；直到返回都不会有唤醒定时启动任务（`fireTimer` 返回 `errUserSpeaking`，定时留着下次再试）。不取消已有的唤醒任务。
4. 忙就返回 `ErrChatNotIdle`（评审时记下：实现用了这个新错误而不是 `ErrChatBusy`，面板一样映射成 409 `CHAT_BUSY`；`ErrChatBusy` 的文字是“取消的任务还没停下”，不合适）：`o.Busy(convKey)` 或 `o.st.PendingPanelActions` 还有未投递的分条。
5. 用 `MessagesBySeq` 找到目标消息；不存在 `ErrMessageNotFound`（包着 `store.ErrNotFound`，评审后）。
6. `o.sched.EndTurn(convKey, actor)`，`o.st.DeleteMessagesFrom`，`o.suggestions.forget(convKey)`，`replanInitiate`。
7. 返回删掉的条数；审计在面板 handler 写。

### `RegeneratePanel`（评审后改写）

1. 同样的锁、会话校验、bot 存在（`o.bots.Bot`）、`holdWakes`、忙检查。
2. `regenerable(conv, seq)`：
   - 目标消息存在（否则 `ErrMessageNotFound`），是 bot 消息，`MessagesAfter(seq, 1)` 为空（否则 `ErrNotLatest`）。
   - `replyTaskID`：平台消息 ID 去掉 `panel_` 是动作 ID，`GetOutbox` → `TaskID`；没有对应动作 `ErrNotReply`。`GetTask`；命令任务 `ErrCommandReply`；`Meta.Trigger != private`、没有触发消息、不是这个会话的 `ErrNotReply`。
   - `GetMessages` 取全部触发消息：少了、不是这个会话的、是 bot 的 `ErrNotReply`；按当前策略是命令的 `ErrCommandReply`。按 `seq` 排好，最后一条是 L。
   - `MessagesAfter(L.Seq, 50)`：最后一条必须是目标，其余每条都是 bot 消息、并且动作属于同一个任务，否则 `ErrNotReply`。
   - 任务发起人的 `ForgottenAt`（共用记忆范围）晚于 L 的 `CreatedAt` 时 `ErrForgotten`。
3. `o.sched.EndTurn(convKey, actor)`，`actor` 是原任务的 `ActorPlatformUserID`。
4. 用 `o.sched.AdmitWith(convKey, actor, L.ID, AdmitNew, closedWindow, persist)`，`closedWindow` 是 `MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}`：
   - `ap.NewTaskID == ""`（会被合并）时返回错误 `ErrRegenMerged`，让这次重新生成失败而不是并进别的任务。
   - `ap.Supersedes != ""` 时返回 `ErrSupersedeRefused`（没有 `Interrupt` 时调度器不会给出，留作防御）。
   - 再读一次策略；`MaxInteractiveRequests == 0` 时返回 `errPaused`。
   - 建 `store.TaskRecord`：`TriggerMessageIDs` 是原任务的全部触发消息、`EventOccurredAt: now`（不是旧消息时间）、`Meta.Trigger: TriggerPrivate`、`Priority: interactive`。
   - 建内部事件：`IdempotencyKey: "conloom:regen:"+taskID`、`EventID: "regen_"+taskID`、`Response` 里带 `task_id`（这样删对话时能清掉内部事件）。
   - `o.ingest(ctx, store.Ingest{Event: ev, NewTask: &task, DeleteMessages: &store.MessageDelete{ConversationKey: convKey, FromSeq: L.Seq + 1}})`，删除旧回复和写新任务在同一个事务里。
5. `o.suggestions.forget(convKey)`，`replanInitiate`，返回 task ID。

`RegenerableSeq(convKey)`：不是面板私聊、bot 不在或正在删除、会话忙、没有消息、`regenerable(最新一条)` 拒绝、聊天暂停时返回 0，否则返回最新一条的 `seq`。

错误：`ErrNotLatest`、`ErrNotReply`、`ErrCommandReply`、`ErrForgotten` 在面板 `chatFail` 映射成 400 `INVALID_EVENT`（中文原因）；`ErrMessageNotFound` 404 `MESSAGE_NOT_FOUND`；`ErrChatNotIdle` 已有 409 映射；暂停 409 `PAUSED`，`ErrQueueFull`/`ErrRegenMerged` 409 `CHAT_BUSY`。

## 6. 前端

### 经典页

- `views/chatclassic.js`：`bubble(m, actions)` 加一行 `.chat-actions`，按钮 `.chat-action`（撤回）和 `.chat-action`（重新生成）；`add` 之后、轮询返回后刷新按钮状态，只在 `GET chat` 的 `regenerate_seq` 那条消息上显示“重新生成”（评审后）。`withdraw` / `regenerate` 调用新接口，成功后调用 `reload()`（chatsclassic 传入，`go(href('chat', key))`）重新走路由，失败显示在 `sendError`。
- `views/chatsclassic.js`：列表上 `contextmenu`，菜单是 layout 里的一个 `.chat-ctx`（`position: fixed`，不能放进 `.chat-side`，会被滚动的容器裁掉）；菜单项“删除聊天”调用新 `removeChat(key)`，错误显示在 `listMsg`。聊天标题旁加一个“删除”按钮作为触屏入口。
- CSS：`.chat-classic .chat-actions`、`.chat-classic .chat-action`、`.chat-classic .chat-ctx`；经典页不加动画。

### TG 风格页

- `views/chat.js`：`bubble(m, actions)` 加 `.tg-actions` / `.tg-action`，同样按 `regenerate_seq` 显示“重新生成”；成功后调用 chats.js 传入的 `reload()`。导出 `listen`（带 capture 参数）给列表菜单用。
- `views/chats.js`：列表上 `contextmenu`（行带 `data-key`）；菜单是 `.tg` layout 里的一个 `.tg-ctx-menu`（`position: fixed`），菜单项用现有的 `.tg-bar-item.danger`“删除聊天”，调用 `removeChat(key, listMsg)`，失败提示显示在列表上方（评审后：没开聊天时 `paneMsg` 不在页面上）。列表每 5 秒重渲染，菜单不能挂在行里。
- CSS：`.chat-app .tg-actions`、`.chat-app .tg-action`、`.chat-app .tg-ctx-menu`；菜单动画沿用 `tg-menu-in`（150ms，ease-out）。

两页的按钮在 `botTyping`、`gone`、正在发送、正在撤回/重新生成时都禁用。右键菜单的关闭条件：点外面、Esc、滚动、窗口 resize、路由切换。评审后：这些 `document`/`window` 监听用 `listen(…, ctx.signal)` 挂在路由的 signal 上（`listenMenu`，`chatsView`/`chatsClassicView` 每次渲染调用），路由切换时移除并关闭菜单；之前每次进入页面都多挂 5 个，永不移除。

### 刷新

删除/重新生成后不原地改 DOM：重新走路由。这样会重置 `after`、`last`、建议回复、日期分隔和分组。评审后：

- 发请求之前先停轮询（`acting`），并让正在进行的那次轮询作废（`epoch` 变了就丢掉结果），所以删除之前读到的消息不会再被加回来；请求失败时恢复轮询。
- TG 页的 `reload` 是 `go(chatHref(key))`，保留 `?bot=` 文件夹（地址不变时路由同步重渲染）；之后再清掉 `arrived[key]` 并重读列表，否则撤回最新一条后未读预览会把旧文本带回来。
- 撤回/重新生成返回 404 `MESSAGE_NOT_FOUND`（消息已经不在，比如在别的窗口撤回了）时重新载入聊天并提示一句；只有 404 `NOT_FOUND`（聊天不在了）才标成已删除。

## 7. 测试和文档

- 存储：`DeleteMessagesFrom` 删消息、FTS、chunk 重建、定时，其他会话的段、重建行和定时不动；`Ingest.DeleteMessages` 和 `NewTask` 同事务（成功和回滚两条路径），不同会话时拒绝；会话不存在 `ErrNotFound`；评审后：读消息 → 撤回 → 写段时什么都不写（`TestWriteChunks_SkippedAfterWithdraw`）。
- 核心：撤回删到尾；重新生成不插重复用户消息、触发消息是原任务的全部触发消息、只允许最新 bot 消息、唤醒/主动消息拒绝、忘记之后拒绝、消息不存在 `ErrMessageNotFound`、忙时 `ErrChatNotIdle`、暂停时 `errPaused`；评审后：撤回/重新生成期间到点的唤醒定时不启动任务。
- 面板：路由、`seq` 校验（0、负数、溢出、非数字）、400/404（`MESSAGE_NOT_FOUND` 和 `NOT_FOUND`）/409、面板群聊、`regenerate_seq`、审计（消息正文确实存在，审计里没有）、CSRF。
- 前端：`web_test.go` / `static_test.go` 的字符串断言、经典页/TG 页样式作用域、按钮和菜单文案；更新 `chatPane` 相关精确签名断言；评审后：菜单监听挂在路由 signal 上、消息不存在不当成聊天已删、动作期间轮询作废、TG 列表删除失败提示在列表。
- 文档：`docs/panel.md` 的聊天页和接口表、`docs/architecture.md` 的删除/重生成说明、`docs/usage.md` 的面板聊天说明。

## 8. 非目标

- 不做分支、编辑消息、单条恢复。
- 不回滚记忆摘要、笔记、好感度、工具副作用、用量和审计。
- 不做平台聊天（QQ/Telegram）的消息撤回。
- 不做旧 bot 消息的“重新生成”；要改就先撤回。
- 不做 bot 自己发起的消息（唤醒、主动）和命令回复的“重新生成”（评审后，部署者决定）。

评审后记下的决定（不改）：

- 重新生成不计入 `user_rate_limit_per_minute`：只有面板用户能点，而且每次都要等聊天空闲。
- 重新生成的任务不带 `Interrupt`：回复分条还没发完时用户再发消息，不会像普通私聊回复那样作废剩下的分条。
- 撤回只结束目标消息发送人（或前面最近一条用户消息的发送人）的回合（`EndTurn`），同一个面板聊天里其他面板用户的回合不动；聊天此时空闲，影响很小。
- 撤回删掉 `status_return` 定时但不结束共用状态（见 §4）。
