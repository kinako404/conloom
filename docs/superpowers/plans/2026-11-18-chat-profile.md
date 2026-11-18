# Chat Profile Card Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clicking the bot's avatar or name in the `#/im` chat header opens a Telegram-style profile card showing the chat's hidden state (scene, activity, bars, effects), relationship (affinity, notes, summary), persona facts and counts.

**Architecture:** Front end only. A new `internal/panel/web/views/profile.js` exports `openProfile({ key, bot, signal, opener })`; it fetches the four existing conversation endpoints in parallel, renders a modal (`.tg-profile`), polls `…/autonomy` every 5 s while open, and cleans up on close or page teardown. `chat.js`'s `chatBar` makes the avatar + name a button that opens it.

**Tech Stack:** vanilla JS modules + CSS in `internal/panel/web` (no build, no deps); Go `_test.go` source tests in `internal/panel/web`; node for `--check` and throwaway behaviour checks.

**Spec:** `docs/superpowers/specs/2026-11-18-chat-profile-design.md`

## Global Constraints

- Public repo: fake data only (bots `bot_test`, users `20001`, names 小明/小红, places 食堂).
- English conventional commit messages, exactly as given.
- All DOM through `h()` (text nodes); every document/window listener through `listen(…, signal)`; no `innerHTML`.
- Motion only inside chat-page CSS (`.chat-app …`), with a `prefers-reduced-motion: reduce` override, so `TestIM_Motion` keeps passing (extend its allow-list only if that test is designed to be extended).
- Before each commit: `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/...`, `gofmt -l .`, `go vet ./...`, `/usr/local/node/bin/node --check` on changed JS.
- Copy (Chinese), verbatim: section titles `状态`, `关系`, `资料`; rows `最近变化`, `消息`, `技能`; no-bars hint `这个 bot 没有启用 buff 技能，没有状态条。`; header status line uses `sceneLabel` from `lib/format.js` (`面对面 · <place>` / `面对面` / `线上`) or `正在：<activity>（还剩 N 分钟）` when a status is active; edit button title `在会话详情里编辑`, close title `关闭`.

## Review Focus

1. A data source failing (e.g. 503 autonomy, memory off) → only that section shows an inline error/empty note; the rest renders. Owner T1 node check + test asserting per-section `errorBox`/catch.
2. Group conversation → no 状态/关系 sections, no autonomy request/poll. Owner T1.
3. Close via ×, Esc, backdrop; reopen; navigating away while open → no leftover poll timers or listeners (AbortController per open, chained to the page signal). Owner T1.
4. Long notes/summary/description → collapsed with a toggle; HTML-like text stays text. Owner T1.
5. Narrow screen (≤ 600px) → full screen card; header still one row in chat; the avatar/name button is keyboard reachable (Enter/Space) and has an aria-label. Owner T2.

---

### Task 1: `views/profile.js` — data loading and the card

**Files:**
- Create: `internal/panel/web/views/profile.js`
- Test: `internal/panel/web/profile_web_test.go` (new)

**Interfaces:**
- Consumes: `api` / `convPath` helpers used by `views/autonomy.js` and `views/conversation.js` (read them for the exact import paths and the JSON shapes of `GET /panel/api/conversations/{key}`, `…/persona`, `…/memory`, `…/autonomy`); `h`, `listen`, `mount` from `lib/dom.js`; `sceneLabel`, `fmtTime` from `lib/format.js`; `avatarEl` from `views/avatar.js`; `href` for the 会话详情 link.
- Produces: `export function openProfile({ key, bot, signal, opener })` → `{ close() }`. Appends `div.tg-profile-backdrop > section.tg-profile[role=dialog][aria-modal=true]` to `document.body`; `close()` removes it, aborts its own controller (polling + listeners) and focuses `opener`.

- [ ] **Step 1: Failing source test** `TestProfile_Card` in `profile_web_test.go`: asserts `views/profile.js` exists and contains `export function openProfile`, the four endpoint path segments (`'persona'`, `'memory'`, `'autonomy'` via `convPath`, and the conversation itself), `role: 'dialog'`, `'aria-modal': 'true'`, every fixed copy string from Global Constraints, `setInterval` or `setTimeout` polling with `5000`, a `type === 'private'` (or equivalent) guard before the autonomy request, and no `innerHTML`.
- [ ] **Step 2: Run** `go test ./internal/panel/web/ -run TestProfile_Card` → FAIL (file missing).
- [ ] **Step 3: Implement `profile.js`.** Fetch with `Promise.allSettled`; each section renders from its own result (rejected → small `errorBox` in that section). Bars: `div.tg-profile-bar > span.fill[style width:%]` + value + band word (reuse the band words shown in `views/autonomy.js` if exported; otherwise a local helper matching `internal/autonomy/bars.go` bands — check the Go file for the exact thresholds and words). Effects: name + `还剩 N 分钟` (round up). Affinity / notes for the chat's person: pick the member entry matching the conversation's peer (inspect the memory JSON; private chats have one person); summary in a `<details>` collapsed. 资料: character name, description in `<details>` when over ~80 chars, gender, `personaLabel`-style template + version (see `conversation.js`). Bottom rows: 最近变化 count with a `<details>` listing events (reuse the event wording of `views/autonomy.js` `eventChange` if exportable), 消息 = `message_count`, 技能 = bot skills joined with `、`. Header ✎ is an `<a href=href('conversations', key)>`; × button; Esc and backdrop click close. Poll `…/autonomy` every 5000 ms while open (private only) and re-render only the 状态 section and header status line.
- [ ] **Step 4: Throwaway node check** in /tmp with a fake DOM (as Task 6/7 of the scene feature did, or a tiny stub): open with fake fetch responses for private + group, one rejected source, long text with `<b>`; verify sections, escaping, and that close clears timers. Not committed.
- [ ] **Step 5: Run** the focused test → PASS; then the full `./internal/panel/...` with `-race`, gofmt, vet, node --check.
- [ ] **Step 6: Commit** `feat(panel): Telegram-style profile card for a chat`

### Task 2: Header entry point, styles, docs

**Files:**
- Modify: `internal/panel/web/views/chat.js` (`chatBar`, ~line 195-227; pass what `openProfile` needs from `chatPane`), `internal/panel/web/style.css` (chat-app section), `docs/panel.md`
- Test: `internal/panel/web/profile_entry_web_test.go` (new; Task 1 owns `profile_web_test.go`), existing `web_test.go` motion/header tests must still pass.

**Interfaces:**
- Consumes: `openProfile({ key, bot, signal, opener })` from Task 1.
- Produces: in `chatBar`, the avatar + `.tg-bar-name` are wrapped in `button.tg-bar-profile` (`type=button`, `aria-label='查看资料'`, `title='查看资料'`) that calls `openProfile`; the existing back button, status line, typing indicator and ⋮ menu are unchanged. Other `chatBar` callers without a key/bot get no button (plain markup as today).

- [ ] **Step 1: Failing test** `TestProfile_HeaderEntry`: `chat.js` imports `openProfile` from `'./profile.js'`, contains `class: 'tg-bar-profile'` and `'查看资料'`; `style.css` has `.chat-app .tg-profile`, `.chat-app .tg-profile-backdrop`, an `@media (max-width: 600px)` rule for `.tg-profile` (full screen), and a `prefers-reduced-motion` rule covering the profile animation.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** the button (reset button styles so the header looks unchanged; focus ring visible), CSS for the card (dark theme variables already used by `.chat-app`; header band in `--accent`-like theme colour; sections separated like Telegram; progress bars; centered max-width ~420px on desktop, full screen on ≤600px; fade + slide-up ≤200ms) and docs/panel.md (the profile card: how to open, what it shows, read-only, 5 s refresh).
- [ ] **Step 4: Run** focused + full `./internal/panel/...` `-race`, gofmt, vet, node --check → PASS.
- [ ] **Step 5: Commit** `feat(panel): open the profile card from the chat header`

## Self-review

Spec coverage: entry (T2), header band + status line (T1), 状态/关系/资料/bottom rows (T1), ✎/×/Esc/backdrop (T1), 5 s refresh + cleanup (T1), motion + mobile (T2), groups (T1), per-section failure (T1), docs (T2). Names consistent: `openProfile`, `.tg-profile`, `.tg-profile-backdrop`, `.tg-bar-profile`.
