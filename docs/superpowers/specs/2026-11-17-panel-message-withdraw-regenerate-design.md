# 面板聊天：撤回、重新生成、右键删除聊天

日期：2026-11-17
状态：已定稿，部署者已确认“做”。标“实施者决定”的条目是我定的，部署者可以事后改。

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

### 重新生成（实施者决定：只允许最新一条 bot 回复）

- 只有聊天里最新的那条消息是 bot 消息时，才显示“重新生成”。老 bot 消息不显示；要改老回复就先撤回它（会删掉它之后的所有消息）。
- 删除这轮 bot 的所有分条（从前面那条用户消息之后到末尾），用那条用户消息作为触发消息新建一个回复任务；不插入新的用户消息。
- 新任务立即排队。聊天页重新加载后显示“对方正在输入…”，回复到了再显示。
- 旧回复触发的工具副作用（`remember`、状态条、状态、主动定时）和已经整理进记忆的内容不回滚；模型调用和用量照常记。
- 找不到前面的用户消息、目标不是最新 bot 消息、会话不是面板私聊时返回 400。

## 3. 接口

沿用现有面板 API 的会话路径、CSRF 和鉴权：

| 方法 | 路径 | 结果 |
|---|---|---|
| `DELETE` | `/panel/api/conversations/{key}/messages/{seq}` | 撤回，204 |
| `POST` | `/panel/api/conversations/{key}/messages/{seq}/regenerate` | 重新生成，202 `{"task_id":"…"}` |

- `seq` 必须是该聊天里的一条消息；撤回成功返回删掉的条数（放在审计里，不放在响应体）。
- 只接受 `platform=panel` 的私聊；其他返回 400。
- bot 忙时 409 `CHAT_BUSY`；目标不是最新 bot 消息时 400；消息不存在时 404。
- 审计：`panel.message.withdraw`（目标会话键，内容 `seq`、`deleted`）、`panel.message.regenerate`（目标会话键，内容 `seq`、`task_id`）。不记消息正文。

`ChatService` 增加两个方法：

```go
WithdrawPanel(ctx context.Context, convKey string, seq int64) (deleted int, err error)
RegeneratePanel(ctx context.Context, convKey string, seq int64) (taskID string, err error)
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

不处理、也写进文档的遗留：

- `events.raw_event` 里仍有撤回消息的原文。
- 已经整理进 `group_memory` 摘要和成员笔记的内容不会回滚。
- 旧任务、已投递的 outbox、`model_calls` 不退。
- 旧回复触发的工具副作用（记忆、状态条、效果、状态）不回滚。

## 5. 核心

### `WithdrawPanel`

1. `panelBots.notDeleting`，拿 `panelLocks.lock(convKey)`，再确认一次。
2. `panelConversation`，要求 `PlatformPanel` 且是私聊。
3. 忙就返回 `ErrChatBusy`：`o.Busy(convKey)` 或 `o.st.PendingPanelActions` 还有未投递的分条。
4. 用 `MessagesBySeq` 找到目标消息；不存在 `store.ErrNotFound`。
5. `o.sched.EndTurn(convKey, actor)`，`o.st.DeleteMessagesFrom`，`o.suggestions.forget(convKey)`。
6. 返回删掉的条数；审计在面板 handler 写。

### `RegeneratePanel`

1. 同样的锁、会话校验、忙检查。
2. 找到目标消息，要求 `FromBot`；用 `MessagesAfter(ctx, convKey, seq, 1)` 确认它是最新一条，否则 `ErrNotLatest`。
3. 用 `MessagesBefore(ctx, convKey, seq, 50)` 往前找第一条非 bot 消息，作为触发消息；找不到 `ErrNotLatest`。
4. `o.sched.EndTurn(convKey, actor)`。
5. 用 `o.sched.AdmitWith(convKey, user.PlatformUserID, user.ID, AdmitNew, closedWindow, persist)`，`closedWindow` 是 `MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}`：
   - `ap.NewTaskID == ""`（会被合并）时返回错误 `errRegenMerged`，让这次重新生成失败而不是并进别的任务。
   - `ap.Supersedes != ""` 时返回 `ErrSupersedeRefused`，让调度器把已结束的旧任务结算掉；下次 persist 会拿到干净的新任务。
   - 再读一次策略；`MaxInteractiveRequests == 0` 时返回 `errPaused`。
   - 建 `store.TaskRecord`：`TriggerMessageIDs: []string{user.ID}`、`EventOccurredAt: now`（不是旧消息时间）、`Meta.Trigger: TriggerPrivate`、`Priority: interactive`。
   - 建内部事件：`IdempotencyKey: "conloom:regen:"+taskID`、`EventID: "regen_"+taskID`、`Response` 里带 `task_id`（这样删对话时能清掉内部事件）。
   - `o.ingest(ctx, store.Ingest{Event: ev, NewTask: &task, DeleteMessages: &store.MessageDelete{ConversationKey: convKey, FromSeq: user.Seq + 1}})`，删除旧回复和写新任务在同一个事务里。
6. `o.suggestions.forget(convKey)`，返回 task ID。

`ErrNotLatest` 新增在 core；面板 `chatFail` 映射成 400 `INVALID_EVENT`。`ErrChatBusy` 已有 409 映射。

## 6. 前端

### 经典页

- `views/chatclassic.js`：`bubble(m, actions)` 加一行 `.chat-actions`，按钮 `.chat-action`（撤回）和 `.chat-action`（重新生成）；`add` 之后、`botTyping` 变化时刷新按钮状态，只有 `last.from_bot && m.seq === last.seq` 显示“重新生成”。`withdraw` / `regenerate` 调用新接口，成功后 `go(href('chat', key))` 重新走路由，失败显示在 `sendError`。
- `views/chatsclassic.js`：`chatItem` 加 `oncontextmenu`，菜单是 layout 里的一个 `.chat-ctx`（`position: fixed`，不能放进 `.chat-side`，会被滚动的容器裁掉）；菜单项“删除聊天”调用新 `removeChat(key)`。`chatHead` 加一个“删除”链接作为触屏入口。
- CSS：`.chat-classic .chat-actions`、`.chat-classic .chat-action`、`.chat-classic .chat-ctx`；经典页不加动画。

### TG 风格页

- `views/chat.js`：`bubble(m, actions)` 加 `.tg-actions` / `.tg-action`，同样的“只有最新 bot 消息能重新生成”规则；成功后 `go(href('im', key))`。导出 `listen` 给列表菜单用（或本地复用）。
- `views/chats.js`：`chatRow` 加 `oncontextmenu`；菜单是 `.tg` layout 里的一个 `.tg-ctx-menu`（`position: fixed`），菜单项用现有的 `.tg-bar-item.danger`“删除聊天”，调用现有的 `removeChat`。列表每 5 秒重渲染，菜单不能挂在行里。
- CSS：`.chat-app .tg-actions`、`.chat-app .tg-action`、`.chat-app .tg-ctx-menu`；菜单动画沿用 `tg-menu-in`（150ms，ease-out）。

两页的按钮在 `botTyping`、`gone`、发送禁用时都禁用。右键菜单的关闭条件：点外面、Esc、列表滚动、窗口 resize、路由切换。

### 刷新

删除/重新生成后不原地改 DOM：重新走路由（`go(href('chat', key))` / `go(href('im', key))`）。这样会重置 `after`、`last`、建议回复、日期分隔和分组，避免旧轮询把已删消息又加回来。TG 页还要清掉 `arrived[key]`，否则撤回最新一条后未读预览会把旧文本带回来。

## 7. 测试和文档

- 存储：`DeleteMessagesFrom` 删消息、FTS、chunk 重建、定时，其他会话不动；`Ingest.DeleteMessages` 和 `NewTask` 同事务。
- 核心：撤回删到尾；重新生成不插重复用户消息、触发消息是前面那条用户消息、只允许最新 bot 消息、忙时 `ErrChatBusy`、暂停时 `errPaused`。
- 面板：路由、`seq` 校验、400/404/409、审计、CSRF。
- 前端：`web_test.go` / `static_test.go` 的字符串断言、经典页/TG 页样式作用域、按钮和菜单文案；更新 `chatPane` 相关精确签名断言。
- 文档：`docs/panel.md` 的聊天页和接口表、`docs/architecture.md` 的删除/重生成说明、`docs/usage.md` 的面板聊天说明。

## 8. 非目标

- 不做分支、编辑消息、单条恢复。
- 不回滚记忆摘要、笔记、好感度、工具副作用、用量和审计。
- 不做平台聊天（QQ/Telegram）的消息撤回。
- 不做旧 bot 消息的“重新生成”；要改就先撤回。
