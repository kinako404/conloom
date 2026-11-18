# Chat profile card (Telegram-style)

## Intent

The bot now has a lot of hidden per-chat state: scene, current activity,
hidden bars, effects, affinity, memory notes and summary. All of it is visible
only deep in 会话详情 (自主 / 记忆 tabs) as tables. In the `#/im` chat it is
invisible, so the user concluded persona bars were "not used" when they were.
A profile card opened from the chat header puts this state where the user is
chatting, in a Telegram-like layout (the user supplied a Telegram profile
screenshot as the reference).

## Goals

- Clicking the bot's avatar or name in the `#/im` chat header opens a profile
  card: a centered modal on desktop, full screen on narrow screens.
- Header band (theme colour): large avatar, character/bot name, one status
  line: current activity with time left when a status is active, otherwise the
  scene (`面对面 · <place>` / `线上`) when the bot enables `scene`, otherwise
  nothing.
- Sections, Telegram style (value on top, muted label under it):
  - **状态** (private chats with the `buff` skill): each bar with a progress
    bar, value 0–100 and band word; running effects with time left. Without
    `buff`: a one-line hint that the bot has no bars.
  - **关系** (private chats with memory on): affinity tier for the chat's
    person, the notes remembered about them (collapsed when long), the chat
    summary (collapsed).
  - **资料**: character setting (collapsed when long), gender, persona template
    and version.
  - Bottom list rows: 最近变化 (count; expands to the bar/effect change list),
    消息 (message count), 技能 (enabled skills).
- Top-right: ✎ goes to 会话详情 (where editing already exists), × closes.
  Esc and a click on the backdrop close too; focus returns to the header.
- While open, the 状态 part refreshes about every 5 s; polling stops on close
  and when the chat page goes away.
- Opening/closing uses a short fade/slide, like the rest of the chat page.

## Non-goals

- No editing in the card; no new backend endpoint; no change to the classic
  chat page or 会话详情.
- Avatar-derived header colours.

## Anti-goals

- The card must not break or slow the chat: failures of one data source show
  a small inline error in that section only; the chat keeps polling normally.
- No raw HTML from data (names, notes, summaries are text).

## Approach (chosen)

Front end only. The card fetches the existing endpoints in parallel:
`GET /panel/api/conversations/{key}` (stats, persona label, type),
`…/persona` (character fields, avatar), `…/memory` (summary, member notes,
affinity), `…/autonomy` (scene, status, bars, effects, events; private only).
Rejected: a new aggregate `…/profile` endpoint — duplicates four existing
readers and their tests for no real gain at this data size.

## Constraints

- Vanilla JS/CSS in `internal/panel/web` (no build step, no dependencies),
  `h()` for all DOM (text nodes only), `listen(..., signal)` for cleanup.
- Web tests in `internal/panel/web/*_test.go` check source strings; motion is
  allowed only on the chat page (`TestIM_Motion`) — keep the card's motion in
  chat-page CSS and respect `prefers-reduced-motion` like existing chat motion.
- Groups: no 状态/关系 hidden-state sections (they have none); show 资料 and
  the bottom rows.
- Public repo: fake data only in tests/docs.
- Update `docs/panel.md`.

## Left to the builder

Exact module/file split (a new `views/profile.js` is expected), how affinity
and notes are picked out of the memory response for the chat's person,
colours, collapse thresholds, the progress-bar markup.
