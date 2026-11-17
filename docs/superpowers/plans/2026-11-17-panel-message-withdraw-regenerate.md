# Panel Chat Withdraw, Regenerate, and Right-Click Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add panel-chat message withdrawal, latest-reply regeneration, and right-click chat deletion, without duplicating user messages or leaving search/memory-chunk debris.

**Architecture:** Store a range delete in SQLite that also cleans FTS, chunks and timers. Core exposes `WithdrawPanel` and `RegeneratePanel`; the latter uses `Scheduler.AdmitWith` with an existing user message as trigger and an `Ingest` that deletes the old reply in the same transaction. Panel handlers expose two routes. Both chat panes render per-message actions and reload via the router after an action; both chat lists get a fixed-position context menu.

**Tech Stack:** Go 1.x, SQLite (modernc), vanilla ES modules, existing panel test harness.

**Spec:** `docs/superpowers/specs/2026-11-17-panel-message-withdraw-regenerate-design.md`

## Global Constraints

- Only `platform=panel` private chats; QQ/Telegram conversations never get these actions.
- Withdraw deletes from the selected `seq` to the end of that conversation.
- Regenerate is only allowed when the selected message is the newest message and is from the bot.
- A busy chat (running task or pending/leased panel action) returns 409 `CHAT_BUSY`; no partial deletion.
- No duplicate user message on regenerate.
- Do not add `innerHTML`, inline `style:` attributes, `http(s)://`, Chrome >89 APIs, or `{ signal }` in `addEventListener`.
- Classic-page classes stay `chat-*` under `.chat-classic`; TG classes stay `tg-*` under `.chat-app`.
- Audit every write; never audit message text.
- Do not commit unless the user asks.

## Review Focus

- Multi-part bot reply mid-delivery: withdraw/regenerate must 409, never leave a late part that reappears after deletion.
- Withdrawing an older message: exactly `seq >= selected` disappears, FTS/chunks/timers are cleaned, other chats untouched.
- Regenerate target not newest, or no preceding user message: 400 and nothing deleted.
- TG chat list re-renders every 5s: the right-click menu must survive and delete the right chat.
- Memory: already consolidated text stays; UI confirm says so, and no code pretends to roll it back.

---

### Task 1: Store range delete and atomic ingest delete

**Files:**
- Modify: `internal/store/records.go` (near `Ingest`, line ~346)
- Modify: `internal/store/store.go` (after `DeleteConversation`, line ~431)
- Modify: `internal/store/sqlite/delete.go`
- Modify: `internal/store/sqlite/ingest.go` (in `(*DB).Ingest`)
- Test: `internal/store/sqlite/delete_test.go`
- Test: `internal/store/sqlite/ingest_test.go`

**Interfaces:**
- Consumes: existing `dropChunks(ctx, tx, drop string, dropArgs []any, rebuild string, rebuildArgs []any) error`.
- Produces:
  ```go
  type MessageDelete struct {
      ConversationKey string
      FromSeq         int64
  }
  // Ingest gains:
  DeleteMessages *MessageDelete
  // Store gains:
  DeleteMessagesFrom(ctx context.Context, convKey string, fromSeq int64) (int, error)
  ```

- [ ] **Step 1: Write the failing store test**

In `internal/store/sqlite/delete_test.go`, add `TestDeleteMessagesFrom_CleansMessagesFTSChunksAndTimers`. Seed two conversations; in the target, insert messages seq 1..6, FTS rows, two chunks (`1..3`, `4..6`), a rebuild row `4..6`, and timers anchored at 3 and 5. Call `DeleteMessagesFrom(ctx, key, 4)`. Assert:
  - return `3`;
  - target messages left are seq 1..3; other conversation untouched;
  - `message_fts` rowids for the target are exactly 1..3;
  - `memory_chunks` has only the `1..3` chunk; `memory_chunk_rebuild` has `1..3` (straddling chunk) and not `4..6`;
  - `conversation_timers` has only the anchor-3 timer.

Also add `TestIngest_DeleteMessagesAndNewTaskAreAtomic`: call `Ingest` with `DeleteMessages`, a `NewTask`, and an invalid event (e.g. duplicate idempotency key from another bot) so the insert fails; assert no messages were deleted.

- [ ] **Step 2: Run the tests and see them fail**

Run: `go test ./internal/store/sqlite -run 'TestDeleteMessagesFrom|TestIngest_DeleteMessages' -count=1`
Expected: FAIL, undefined `DeleteMessagesFrom` / `DeleteMessages`.

- [ ] **Step 3: Add the store types and interface**

In `internal/store/records.go` add `MessageDelete` and the `DeleteMessages *MessageDelete` field to `Ingest`. In `internal/store/store.go` add the `DeleteMessagesFrom` method next to `DeleteConversation`, with a comment: deletes messages from `fromSeq` onward, their keyword index rows, overlapping chunks (queuing straddling chunks for rebuild), and timers anchored at or after `fromSeq`; returns the number of messages deleted; `ErrNotFound` if the conversation is absent.

- [ ] **Step 4: Implement the SQLite helper**

In `internal/store/sqlite/delete.go` add:

```go
func deleteMessagesFrom(ctx context.Context, tx *sql.Tx, del store.MessageDelete) (int, error)
```

Order: `message_fts` subquery delete; `dropChunks` with `drop = c.conversation_key = ? AND c.end_seq >= ?` and `rebuild = c.conversation_key = ? AND c.start_seq < ? AND c.end_seq >= ?`; delete `memory_chunk_rebuild` rows `conversation_key = ? AND start_seq >= ?`; delete `conversation_timers` rows `conversation_key = ? AND anchor_seq >= ?`; delete `messages`; return `RowsAffected`.

Add `(*DB).DeleteMessagesFrom` wrapping the helper in `db.inTx`, checking the conversation exists first with `scanConversation(... WHERE key = ?)` and returning `store.ErrNotFound` when absent.

In `internal/store/sqlite/ingest.go`, inside the transaction, before `insertEvent`, call the helper when `in.DeleteMessages != nil`; return any error.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/store/sqlite -run 'TestDeleteMessagesFrom|TestIngest_DeleteMessages' -count=1`
Expected: PASS.

Run: `go test ./internal/store/sqlite -count=1`
Expected: PASS.

---

### Task 2: Core withdrawal and regeneration

**Files:**
- Modify: `internal/core/panelchat_errors.go`
- Modify: `internal/core/panelchat.go`
- Test: `internal/core/panelchat_test.go`

**Interfaces:**
- Consumes: Task 1 `DeleteMessagesFrom`, `Ingest.DeleteMessages`.
- Produces:
  ```go
  func (o *Orchestrator) WithdrawPanel(ctx context.Context, convKey string, seq int64) (deleted int, err error)
  func (o *Orchestrator) RegeneratePanel(ctx context.Context, convKey string, seq int64) (taskID string, err error)
  var ErrNotLatest = errors.New("core: only the newest bot reply can be regenerated")
  var ErrChatNotIdle = errors.New("core: the chat is still replying; try again after it finishes")
  ```

- [ ] **Step 1: Write failing core tests**

In `internal/core/panelchat_test.go` add:
- `TestWithdrawPanel_DeletesFromSeqToEnd`: create a panel chat, submit two messages, wait for replies, call `WithdrawPanel(userSeq)`; assert the user message and bot reply are gone, the earlier exchange remains, and `suggestions.forget` is not directly observable (skip).
- `TestRegeneratePanel_ReusesTheUserMessage`: create a panel chat, submit a message, wait for the reply; get the bot seq and preceding user seq; call `RegeneratePanel(botSeq)`; assert the first bot reply is gone, exactly one user message remains, a new task exists with `TriggerMessageIDs == []string{userID}`, and the new task's `EventOccurredAt` is near now.
- `TestRegeneratePanel_RefusesOlderReply`: two turns; call `RegeneratePanel` on the first bot reply; expect `ErrNotLatest` and no deletion.
- `TestWithdrawPanel_RefusesBusy`: block the model completion (use `blockFirstCompletion` or a fake), call `WithdrawPanel` while running; expect `ErrChatNotIdle` and messages unchanged.

Use existing helpers `createChat`, `submitPanel`, `waitTask`, `GetMessages`/`MessagesAfter`. For the bot seq, read messages after submit and find `FromBot`.

- [ ] **Step 2: Run tests and see them fail**

Run: `go test ./internal/core -run 'TestWithdrawPanel|TestRegeneratePanel' -count=1`
Expected: FAIL, undefined methods.

- [ ] **Step 3: Add errors**

In `internal/core/panelchat_errors.go` add `ErrNotLatest` and `ErrChatNotIdle` with comments that the panel API maps them to 400 and 409.

- [ ] **Step 4: Implement `WithdrawPanel`**

In `internal/core/panelchat.go`:
- `panelBots.notDeleting`, `panelLocks.lock(convKey)`, re-check.
- `panelConversation`; require `PlatformPanel` and private; else existing errors.
- Busy check: `o.Busy(convKey)` or `o.st.PendingPanelActions(ctx, convKey, o.now()) > 0` → `ErrChatNotIdle`.
- `o.st.MessagesBySeq(ctx, convKey, []int64{seq})`; missing → `store.ErrNotFound`.
- Find the actor: target's `PlatformUserID` if non-empty; else scan `MessagesBefore` for the nearest non-bot message; if found call `o.sched.EndTurn(convKey, actor)`.
- `deleted, err := o.st.DeleteMessagesFrom(ctx, convKey, seq)`.
- `o.suggestions.forget(convKey)`; `o.replanInitiate(ctx, convKey)`; return.

- [ ] **Step 5: Implement `RegeneratePanel`**

In `internal/core/panelchat.go`, following `createJoinTask` (`join.go:490`):
- Same lock/validation/busy check.
- `target := MessagesBySeq(seq)`; require `target.FromBot`, else `ErrNotLatest`.
- `MessagesAfter(ctx, convKey, seq, 1)` must be empty; else `ErrNotLatest`.
- `before := MessagesBefore(ctx, convKey, seq, 50)`; first `!FromBot` is the user message; none → `ErrNotLatest`.
- `o.sched.EndTurn(convKey, user.PlatformUserID)`.
- `closed := MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}`.
- `o.sched.AdmitWith(convKey, user.PlatformUserID, user.ID, AdmitNew, closed, persist)` where persist:
  - `ap.NewTaskID == ""` → `errRegenMerged` (new core error).
  - `ap.Supersedes != ""` → return `ErrSupersedeRefused`.
  - read policy; `MaxInteractiveRequests == 0` → `errPaused`.
  - build `store.TaskRecord{ID: ap.NewTaskID, BotInstanceID: target.BotInstanceID, ConversationKey: convKey, Status: queued, ActorPlatformUserID: user.PlatformUserID, TriggerMessageIDs: []string{user.ID}, Priority: int(admission.PriorityInteractive), Meta: conloomapi.TaskMeta{Trigger: TriggerPrivate}, EventOccurredAt: o.now().UTC(), CreatedAt: now, UpdatedAt: now}`.
  - build internal `store.EventRecord{BotInstanceID: target.BotInstanceID, IdempotencyKey: "conloom:regen:"+id, EventID: "regen_"+id, Response: resp, CreatedAt: now}` with `resp` containing `task_id`.
  - `o.ingest(ctx, store.Ingest{Event: ev, NewTask: &task, DeleteMessages: &store.MessageDelete{ConversationKey: convKey, FromSeq: user.Seq + 1}})`.
  - set `taskID`.
- After success: `o.suggestions.forget(convKey)`; return `taskID`.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/core -run 'TestWithdrawPanel|TestRegeneratePanel' -count=1`
Expected: PASS.

Run: `go test ./internal/core -count=1`
Expected: PASS.

---

### Task 3: Panel routes, handlers, audit

**Files:**
- Modify: `internal/panel/write_routes.go`
- Modify: `internal/panel/chat.go`
- Modify: `internal/panel/respond.go`
- Test: `internal/panel/chat_test.go`

**Interfaces:**
- Consumes: Task 2 methods.
- Produces: `ChatService` gains `WithdrawPanel` and `RegeneratePanel`; routes:
  - `DELETE /panel/api/conversations/{key}/messages/{seq}` → 204
  - `POST /panel/api/conversations/{key}/messages/{seq}/regenerate` → 202 `{"task_id":"…"}`

- [ ] **Step 1: Write failing handler tests**

Extend `fakeChat` with `WithdrawPanel`/`RegeneratePanel` recording calls and answering configurable errors/deleted/task IDs. Add:
- `TestWithdrawChatMessage`: valid panel chat → 204, fake called with `(key, 7)`, audit `panel.message.withdraw` contains `"seq":7,"deleted":3`.
- `TestRegenerateChatMessage`: valid → 202 `{"task_id":"task_x"}`, audit `panel.message.regenerate` contains `"seq":7,"task_id":"task_x"`.
- `TestChatMessageActions_Validation`: non-panel → 400; seq `0`/`x` → 400; missing chat → 404; fake `ErrChatNotIdle` → 409 `CHAT_BUSY`; fake `ErrNotLatest` → 400.
- CSRF missing → 403 for both writes.

- [ ] **Step 2: Run and see fail**

Run: `go test ./internal/panel -run 'TestWithdrawChatMessage|TestRegenerateChatMessage|TestChatMessageActions' -count=1`
Expected: FAIL, undefined methods/routes.

- [ ] **Step 3: Add interface methods and routes**

Add methods to `ChatService` in `internal/panel/chat.go`. Add the two routes to `writeRoutes` in `write_routes.go`.

- [ ] **Step 4: Implement handlers**

In `chat.go`:
- `withdrawChatMessage`: `chatService`, `conversation`, parse `r.PathValue("seq")` with `strconv.ParseInt`, require `>0`; call `chat.WithdrawPanel`; on error `s.chatFail`; audit `panel.message.withdraw` with `{Seq, Deleted}`; `w.WriteHeader(204)`.
- `regenerateChatMessage`: same parse; call `chat.RegeneratePanel`; audit `panel.message.regenerate` with `{Seq, TaskID}`; `s.writeJSON(w, 202, struct{TaskID string `json:"task_id"`}{taskID})`.
- In `chatFail`, add `core.ErrNotLatest` to the 400 list.

In `respond.go`, add:
```go
case errors.Is(err, core.ErrChatNotIdle):
    s.respondError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, strings.TrimPrefix(core.ErrChatNotIdle.Error(), "core: "), err)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/panel -run 'TestWithdrawChatMessage|TestRegenerateChatMessage|TestChatMessageActions' -count=1`
Expected: PASS.

Run: `go test ./internal/panel -count=1`
Expected: PASS.

---

### Task 4: Classic chat page actions and context menu

**Files:**
- Modify: `internal/panel/web/views/chatclassic.js`
- Modify: `internal/panel/web/views/chatsclassic.js`
- Modify: `internal/panel/web/style.css`
- Test: `internal/panel/web/web_test.go`
- Test: `internal/panel/static_test.go`

**Interfaces:**
- Consumes: routes from Task 3; existing `del`, `post`, `go`, `href`, `deleteChatConfirmed`.
- Produces: no exported signature changes; `chatPane({ key, signal, hint })` stays exact.

- [ ] **Step 1: Write failing UI tests**

In `web_test.go`, add `TestChatClassic_MessageActions` asserting `chatclassic.js` contains `'撤回'`, `'重新生成'`, `del(convPath(key, 'messages', seq))`, `post(convPath(key, 'messages', seq, 'regenerate'))`, `go(href('chat', key))`, and that the regenerate button is gated by `last.from_bot && m.seq === last.seq`.
Add `TestChatsClassic_ContextMenu` asserting `chatsclassic.js` imports `deleteChatConfirmed`, attaches `'contextmenu'`, calls `e.preventDefault()`, has a `.chat-ctx` menu outside `.chat-side`, and calls `removeChat`/`go('#/chat')`/`refresh()`.
Add the new classic classes to `classicClasses`: `chat-actions`, `chat-action`, `chat-ctx`, `chat-ctx-item`.
Add string checks to `static_test.go` for the new labels.

- [ ] **Step 2: Run and see fail**

Run: `go test ./internal/panel/web -run 'TestChatClassic_MessageActions|TestChatsClassic_ContextMenu' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement classic message actions**

In `chatclassic.js`:
- Import `del` and `go, href`.
- Change `bubble(m)` to `bubble(m, actions)`; add a `div.chat-actions` with buttons `撤回` and, for bot messages, `重新生成`.
- In `chatPane`, define `refreshActions()` to show regenerate only on `last.from_bot && el.dataset.seq === String(last.seq)`, and disable both buttons when `botTyping || gone`.
- Call `refreshActions()` after `add` and after `botTyping` updates.
- `withdraw(seq)`: confirm with the design text; `del(convPath(key, 'messages', seq))`; on success `go(href('chat', key))`; on 409 show `对方正在回复，等它说完再试。`; on 404 `markGone()`; other errors `errorBox`.
- `regenerate(seq)`: if not latest, show a hint; `post(convPath(key, 'messages', seq, 'regenerate'))`; on success `go(href('chat', key))`; same error handling.

- [ ] **Step 4: Implement classic context menu**

In `chatsclassic.js`:
- Import `deleteChatConfirmed` from `./chat.js`.
- Add a `chatMenu` element to the layout (outside `.chat-side`), `position: fixed`.
- `chatItem(c)` adds `oncontextmenu: (e) => { e.preventDefault(); openChatMenu(e, c); }`.
- `openChatMenu` positions at `clientX/clientY`, clamps to viewport, shows one item `删除聊天`.
- Clicking calls `removeChat(c.key)`: `await deleteChatConfirmed(key)`; if current, `go('#/chat')`; `await refresh()`; errors to `listMsg`.
- Close on outside click, Escape, scroll, resize, and route signal.
- Add a `删除` link in `chatHead` as the touch fallback, calling `removeChat(key)`.

- [ ] **Step 5: Add CSS**

In `style.css` under `.chat-classic`:
- `.chat-classic .chat-actions { display: flex; gap: 4px; margin-top: 2px; }`
- `.chat-classic .chat-action { ... }` small link-like button; `.chat-action.danger { color: var(--danger); }`
- `.chat-classic .chat-ctx { position: fixed; z-index: ...; background: ...; border: ...; }`
- `.chat-classic .chat-ctx-item { ... }`
- No animations.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/panel/web ./internal/panel -run 'TestChatClassic_MessageActions|TestChatsClassic_ContextMenu|TestStatic_ServesChatsView' -count=1`
Expected: PASS.

---

### Task 5: TG-style chat actions and context menu

**Files:**
- Modify: `internal/panel/web/views/chat.js`
- Modify: `internal/panel/web/views/chats.js`
- Modify: `internal/panel/web/style.css`
- Test: `internal/panel/web/web_test.go`

**Interfaces:**
- Consumes: routes from Task 3; existing `del`, `post`, `href`, `removeChat`.
- Produces: `chatPane` signature unchanged; TG class rules under `.chat-app`.

- [ ] **Step 1: Write failing UI tests**

Add `TestChatTG_MessageActions` asserting `chat.js` contains `'撤回'`, `'重新生成'`, the two API paths, `go(href('im', key))`, and the latest-only gate.
Add `TestChatsTG_ContextMenu` asserting `chats.js` attaches `'contextmenu'` to `a.tg-chat`, calls `e.preventDefault()`, has a `.tg-ctx-menu` outside the list, and calls the existing `removeChat`.
Add string checks to `static_test.go` if needed.

- [ ] **Step 2: Run and see fail**

Run: `go test ./internal/panel/web -run 'TestChatTG_MessageActions|TestChatsTG_ContextMenu' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement TG message actions**

In `chat.js`:
- Import `go` from `../lib/router.js`.
- `bubble(m, actions)` adds `div.tg-actions` with `button.tg-action` `撤回` and, for bot messages, `重新生成`.
- Add `refreshActions()` gated by `last.from_bot && m.seq === last.seq` and disabled when `botTyping || gone`.
- Call it after `add` and when `botTyping` changes.
- `withdraw(seq)` / `regenerate(seq)` use the same API and errors as Task 4; on success `go(href('im', key))`.
- Clear `arrived[key]`? That lives in `chats.js`; the route re-render/refresh list handles it. If needed, export a callback or clear in `chats.js` on list refresh.

- [ ] **Step 4: Implement TG context menu**

In `chats.js`:
- Add a single `div.tg-ctx-menu` to the `.tg` layout.
- `chatRow` adds `oncontextmenu`; `openChatMenu(e, c)` positions fixed and shows `删除聊天` using `button.tg-bar-item.danger`.
- Clicking calls the existing `removeChat(c.key)`; close on outside/Escape/scroll/resize.
- Do not put the menu inside the list; the list re-renders every 5s.

- [ ] **Step 5: Add CSS**

Under `.chat-app`:
- `.chat-app .tg-actions { display: flex; gap: 4px; margin-top: 2px; justify-content: flex-end; }`
- `.chat-app .tg-action { ... }`, `.tg-action.danger`.
- `.chat-app .tg-ctx-menu { position: fixed; z-index: ...; }` using existing menu colors; optional `tg-menu-in` 150ms ease-out.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/panel/web -run 'TestChatTG_MessageActions|TestChatsTG_ContextMenu' -count=1`
Expected: PASS.

Run: `go test ./internal/panel/web -count=1`
Expected: PASS.

---

### Task 6: Docs and full verification

**Files:**
- Modify: `docs/panel.md`
- Modify: `docs/architecture.md`
- Modify: `docs/usage.md`

- [ ] **Step 1: Document behavior and API**

In `docs/panel.md`, add to the chat page section: right-click delete, withdraw semantics, latest-only regenerate, busy 409, memory caveat. Add rows to the panel API table for the two routes. Add the audit kinds to the audit list.
In `docs/architecture.md`, add a short paragraph under panel chat / storage: range delete cleans `messages`, `message_fts`, overlapping chunks and timers; regenerate reuses the existing user message and deletes the reply in the same transaction.
In `docs/usage.md`, add a panel-chat note: how to withdraw/regenerate and that memory/tool side effects are not rolled back.

- [ ] **Step 2: Run the full verification**

Run: `go test ./...`
Expected: PASS.

Run: `go vet ./...`
Expected: PASS (or no new vet errors).

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 3: Manual smoke check**

Run the panel locally or use the deployed panel; open a panel chat:
- Right-click a chat row → 删除聊天 works and navigates away if it was open.
- Send a message, wait for the reply; click 重新生成 on the newest bot message; the old reply disappears and a new one arrives; the user message is not duplicated.
- Click 撤回 on a user message; it and its reply disappear; later messages too.
- While the bot is typing, the action buttons are disabled (or the API returns 409).
- `message_fts` and `memory_chunks` are cleaned: search/recall does not return withdrawn text.

---

## Self-Review Notes

- Spec coverage: right-click delete (Tasks 4/5), withdraw (Tasks 1–4/5), regenerate (Tasks 1–5), busy 409 (Tasks 2/3), memory caveat (Tasks 4/5/6), API/audit (Task 3), docs (Task 6).
- Latest-only regenerate is enforced both in the UI and in core; older bot messages only offer withdraw.
- Store deletion cleans FTS and chunks; timers anchored at/after the cut are deleted.
- Regeneration deletes the reply and writes the task in one `Ingest` transaction; no duplicate user message.
- Tests pin the five review-focus cases.
