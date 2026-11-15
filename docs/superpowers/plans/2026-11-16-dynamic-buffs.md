# Bot-Created Bars and Timed Effects — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The bot can add and remove its own numeric bars and timed effects (tools `add_buff`, `remove_buff`; `adjust_buff` accepts custom bars), shown in the hidden state block and the panel, stored per memory scope.

**Architecture:** Two new scope-keyed tables (custom bar definitions, effects) plus a `kind` column on buff events; custom bars reuse the existing value/decay/event machinery; `autonomy.RenderState` gains custom bars and an effects line; tools live in the `buff` skill; panel/gateway get delete endpoints.

**Tech Stack:** Go 1.26, SQLite (modernc), vanilla JS panel. No new dependencies.

**Spec (Chinese, binding):** `docs/superpowers/specs/2026-11-16-dynamic-buffs-design.md`.

## Global Constraints

- No new dependencies; `CGO_ENABLED=0 go build ./...`; `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...`; `gofmt -l .` clean.
- Public repo hygiene: fake data only in tests; no real IDs/names.
- Fixed values: ≤6 custom bars and ≤6 active effects per scope; names 1–6 runes (persona bar name rules); description 0–40 runes one line; baseline 0–100; half-life 0.5–168 hours (default 6); initial value 0–100 (default baseline); effect minutes 5–10080; state block ≤300 runes with trim order descriptions → effects → trailing bars → status line; event kinds `adjust|bar_add|bar_remove|effect_add|effect_remove`; audits `autonomy.bar.remove`, `autonomy.effect.remove`; migration `0017_dynamic_buffs.sql`.
- Names and descriptions reviewed with `OutputProtected` in protected mode, `Output` in open mode (same as `set_status`); results never show numbers.
- Everything keyed by the memory scope (`store`/`memory` scope helpers); persona bars cannot be removed; persona wins on name clash.
- Panel JS: no innerHTML/inline style/http(s)/blob:; h() for DOM.

## Review Focus

1. A wake turn or a re-run task calls `add_buff` twice for the same name → one row, one event (Task 2 `TestAddBuff_IdempotentPerTask`).
2. Limits: 7th custom bar / 7th active effect refused; an expired effect frees a slot (Task 2 `TestAddBuff_Limits`).
3. Protected persona + blocked word in name or description → refused, nothing stored (Task 2 `TestAddBuff_Reviewed`).
4. Echoed state block containing an effects line is stripped; a normal reply line starting "效果：" without the header is kept (Task 1 `TestStripTranscriptPrefix_EffectsLine`).
5. Forget / delete conversation / delete scope remove custom bars and effects (Task 1 store tests).

---

### Task 1: Store, rendering, strip rule

**Files:** create `internal/store/sqlite/migrations/0017_dynamic_buffs.sql`, store methods in `internal/store/sqlite/autonomy.go` (+tests); modify records/store interface, `delete.go` conversationTables, forget paths, DeleteScope, `internal/autonomy/state.go` (+tests), `internal/contextbuilder/strip.go` (+tests), the buff-event insert to set `kind`.

**Produces:**
```go
type CustomBar struct { ConversationKey, Name, Description, TaskID string; Baseline int; HalfLife time.Duration; CreatedAt time.Time }
type Effect struct { ConversationKey, Name, Description, TaskID string; StartedAt, UntilAt time.Time }
var ErrBuffNameTaken = errors.New("store: buff name already in use") // a different task holds the name
var ErrBuffLimit = errors.New("store: too many buffs")
AddCustomBar(ctx, b CustomBar, initial float64, limit int) error // tx: limit check, insert def + value row (day bookkeeping zero) + event kind bar_add; same task_id → nil no-op
RemoveCustomBar(ctx, scopeKey, name, source, taskID string) (bool, error) // deletes def + value row, event bar_remove
ListCustomBars(ctx, scopeKey string) ([]CustomBar, error) // by created_at
AddEffect(ctx, e Effect, limit int, now time.Time) error // limit counts until_at > now; expired same-name row replaced; same task → no-op; event effect_add
RemoveEffect(ctx, scopeKey, name, source, taskID string) (bool, error) // event effect_remove
ListEffects(ctx, scopeKey string, now time.Time) ([]Effect, error) // active only, by started_at
PurgeExpiredEffects(ctx, now time.Time) (int, error)
// BuffEvent gains Kind string
// autonomy
type StateEffect struct { Name, Description string; UntilAt time.Time }
func RenderState(st *StateStatus, bars []StateBar, effects []StateEffect, now time.Time, loc *time.Location) string
```
- [ ] Tests: CRUD + limits + idempotency + expired replacement; `TestDeleteConversation_CoversEveryKeyedTable` with the new tables; forget/DeleteScope clear them; migration-only-adds test; `TestRenderState_Effects` (exact line, remaining time wording, trim order with 300 cap); `TestStripTranscriptPrefix_EffectsLine`.
- [ ] Commit `feat(store,autonomy): custom bars and timed effects`.

### Task 2: Tools and core wiring

**Files:** `internal/skills/tools/buff.go` (+ new `add_buff`, `remove_buff`, tests), `internal/skills/builtin/buff/SKILL.md`, `internal/app/skills.go` registration + `TestBuiltinTools`, `internal/core/autonomy_state.go` (custom bars + effects into the state block), wake turn tool gating (`internal/core/skills.go`: buff skill tools allowed in wake turns), hourly maintenance purge of expired effects (`internal/core/recover.go`/cleanup label), `docs/skills.md`, `docs/architecture.md`.

Rules: spec §2–§3 (validation, review, persona-name protection, persona wins on clash, results without numbers, adjust_buff accepts custom bars with their own baseline/half-life, daily cap per bar as today).
- [ ] Tests: `TestAddBuff_BarAndEffect`, `TestAddBuff_IdempotentPerTask`, `TestAddBuff_Limits`, `TestAddBuff_Reviewed`, `TestAddBuff_NameRules` (collision with persona bar / existing), `TestRemoveBuff_PersonaBarRefused`, `TestAdjustBuff_CustomBar`, `TestState_CustomBarsAndEffectsInjected`, `TestWake_BuffToolsOffered`, `TestMaintenance_PurgesExpiredEffects`.
- [ ] Commit `feat(skills): bots add and remove their own bars and effects`.

### Task 3: Panel and gateway

**Files:** core autonomy service (`State` includes custom flag + effects; `RemoveBar`, `RemoveEffect` with audits), panel routes/handlers + tests, gateway routes + tests, `pkg/conloomapi/autonomy.go` (AutonomyBar.Custom bool, AutonomyEffect{Name, Description, StartedAt, UntilAt}, AutonomyState.Effects, event Kind), `internal/panel/web/views/autonomy.js`, `docs/panel.md`, `docs/api.md`, `docs/usage.md`.
- [ ] Tests: `TestAutonomyAPI_RemoveCustomBar` (persona bar → 400), `TestAutonomyAPI_RemoveEffect`, audits, CSRF, gateway scope `memory:write`; static/JS checks; `node --check`.
- [ ] Commit `feat(panel,gateway): view and remove bot-created bars and effects`.
