# Panel Chats Share Memory per Bot; Remove Takeover — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** All panel-created chats of one bot share one memory scope (summary, notes, affinity, revisions, forget time, semantic chunks, status and bars), platform conversations keep per-conversation memory, and the panel "takeover" of platform private chats is removed with its code.

**Architecture:** A `memory.ScopeKey(conv)` maps a panel private chat to `<botID>:panel:private:~shared` and every other conversation to its own key; every memory-class read/write goes through the scope key, while history, persona, policy, tasks, outbox and timers stay per chat. Consolidation and semantic search run per scope; a one-time startup migration (app_meta `panel_memory_scoped`) folds existing panel chat memory into the scope.

**Tech Stack:** Go 1.26, SQLite (modernc), vanilla JS panel. No new dependencies.

**Spec (Chinese, binding):** `docs/superpowers/specs/2026-11-16-panel-shared-memory-design.md`.

## Global Constraints

- No new Go module dependencies; `CGO_ENABLED=0 go build ./...`; `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...`; `gofmt -l .` clean.
- Public repo hygiene: fake data only in tests (bots `qq-main`/`tg-main`, users `20001`/`29999`, names 小明/小红); no real IDs/names.
- Platform conversations (QQ/Telegram private and group): memory behaviour byte-for-byte unchanged; existing tests for them keep passing unchanged.
- Panel-created chats keep per-chat history, persona versions, policies, tasks, outbox (panel channel) and timers.
- Scope key format exactly `<botID>:panel:private:~shared`.
- app_meta key `panel_memory_scoped`; audit `memory.scope.migrate`.
- Protected mode intact (consolidation rule 6 / memory_rules chosen by the persona of the chat whose policy is used).

## Review Focus

1. A memory read or write path that still uses the chat key for a panel chat (silently splitting memory) — Task 2 adds `TestScope_PanelChatsShareEverything` exercising summary, notes, affinity, forget, /记住, admin API and panel memory view across two panel chats of one bot.
2. Deleting one panel chat removes its semantic chunks but leaves shared memory; deleting the bot removes the scope (Task 3 `TestScope_DeleteChatKeepsSharedMemory`, `TestScope_DeleteBotClearsScope`).
3. Status set in chat A, user talks in chat B → B's prompt shows the status; the return speaks in A and clears the shared status; deleting A clears the status and its timer (Task 3 `TestScope_StatusSharedReturnInOrigin`).
4. Migration with two panel chats holding memory merges by the spec rules, runs once, and leaves platform conversations untouched (Task 3 `TestMigrateScope_MergeRules`, `TestMigrateScope_Once`).
5. After takeover removal, posting to a platform conversation from the panel is rejected and nothing reaches the platform outbox (Task 1 `TestChat_PlatformConversationRejected`).

---

### Task 1: Remove takeover

**Files:** core (`panelchat.go`, `panelchat_errors.go`, `orchestrator.go` replyChannel, `skills.go` set_status gating), store (`sqlite/autonomy.go` UserMessageTimes filter + comment in `store.go`), panel (`chat.go`, `bots.go` /chats + takeover field, `respond.go` mappings), panel JS (`views/chats.js`, `views/chat.js` hint, `views/conversation.js` open-in-chat link only for panel chats), tests that exercised takeover (remove or convert), docs (`panel.md`, `architecture.md`, `usage.md`, `api.md` if listed).

- [ ] Tests: `TestChat_PlatformConversationRejected` (panel POST message to a Telegram private conversation → 400, no event/message/outbox rows), `TestChatsAPI_OnlyPanelChats`, `TestSubmitPanel_RejectsPlatform` (core `ErrNotPanelChat`), existing panel-chat tests for panel-created chats unchanged.
- [ ] `grep -rn -i 'takeover\|接管\|ErrNoUserYet' internal pkg docs/*.md` returns only intentional history mentions (list them in the report).
- [ ] Commit `refactor: remove the panel takeover of platform private chats`.

### Task 2: Memory scope — core memory

**Files:** create `internal/memory/scope.go` (+test); modify memory consolidation (`internal/memory/consolidate.go`, scheduler, cons prompt input), remember/admin/forget (`internal/memory/*.go`), core injection and commands (`internal/core/memory.go`, `memory_commands.go`, runner private speaker row / forgotten_at), gateway and panel memory endpoints, affinity paths, store queries that need "messages of all chats in a scope" (e.g. `ScopeConversations(ctx, botID) ([]string, error)` listing panel private chats of a bot, and messages-after-cursor across a key set).

**Interfaces — Produces:**
```go
const PanelScopeID = "~shared"
func ScopeKey(conv store.ConversationRecord) string             // platform → conv.Key; panel private → store.ConversationKey(bot, panel, private, PanelScopeID)
func IsSharedScope(key string) bool
func (…) ScopeChats(ctx, scopeKey string) ([]string, error)      // shared → all panel private chats of the bot; else [key]
```
Rules: spec §3.1–§3.3. Consolidation scheduled per scope (deduplicate), material = messages of all scope chats with seq > cursor ordered by seq and tagged with a short chat label; policy/persona from the most recently active chat in the scope; injection reads scope rows; recent history stays per chat; forget and /记住 act on the scope.
- [ ] Tests: `TestScopeKey`, `TestScope_PanelChatsShareEverything` (two panel chats of one bot: note set via /记住 in A is injected in B; affinity bump in B visible in A; summary consolidated from both; forget in B clears for A; panel memory view of A and B identical), `TestScope_PlatformUnchanged` (platform private and group keys unchanged), `TestConsolidate_ScopeOncePerRun`.
- [ ] Report: list every read/write site of the memory tables (grep) and how each maps to the scope.
- [ ] Commit `feat(memory): panel chats of a bot share one memory scope`.

### Task 3: Scope — search, status/bars, delete, migration

**Files:** semantic chunks (`internal/search/vector/*`, `internal/memory` search tool, store chunk queries), autonomy (`internal/core/autonomy_state.go`, `wake.go` status return/clear, `autonomy_service.go`, tools `set_status`/`adjust_buff` via the scope), delete flows (`internal/core/panelchat.go` DeletePanelChat, `internal/core/panelbots.go` delete, store helpers `DeleteScope(ctx, scopeKey)`, `DeleteChunksOf(ctx, convKey)`), migration (app/startup + store tx, app_meta marker, audit), docs.

Rules: spec §3.4–§3.7 (status row records the origin chat key — add a column `origin_conversation_key TEXT NOT NULL DEFAULT ''` in a new migration if needed; the return timer stays on the origin chat; clear on return/delete of origin).
- [ ] Tests: `TestSearch_PanelScopeAcrossChats`, `TestScope_DeleteChatKeepsSharedMemory`, `TestScope_DeleteBotClearsScope`, `TestScope_StatusSharedReturnInOrigin`, `TestScope_BarsShared`, `TestMigrateScope_MergeRules`, `TestMigrateScope_Once`, `TestMigrateScope_PlatformUntouched`.
- [ ] Commit `feat: shared panel memory for search, status and bars; scope migration`.

### Task 4: Panel display and docs

**Files:** `internal/panel/web/views/conversation.js` (memory and autonomy tabs: note "这个 bot 的所有面板聊天共用这份记忆" for panel chats), panel read views if they need a `memory_scope` field, `docs/architecture.md`, `docs/usage.md`, `docs/panel.md`, `docs/api.md` (three memory scopes).
- [ ] Tests: static/JS checks; Go test that the conversation view for a panel chat reports the shared scope.
- [ ] Commit `docs,panel: shared memory scope for panel chats`.
