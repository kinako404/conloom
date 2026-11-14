# Private-Chat Autonomy (status, bars, initiating) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In private chats the bot can hold a timed status set by a hidden tool, carries hidden mood/energy bars, and may message the user first, all restart-safe and bounded by caps, quiet hours and the existing content rules.

**Architecture:** Pure scheduling/decay/rendering logic lives in a new `internal/autonomy` package. Durable state lives in four new SQLite tables (migration 0012). Two new tools (`set_status`, `adjust_buff`) write the state. A polling goroutine in the orchestrator fires due rows of `conversation_timers` as trigger-less "wake" tasks that flow through the normal runner, review and outbox. The panel and gateway get a read/write "autonomy" view.

**Tech Stack:** Go (existing module `github.com/kinako404/conloom`), SQLite via the existing store, vanilla JS panel. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-14-bot-autonomy-design.md` (Chinese; section numbers below refer to it). Read it before your task.

## Global Constraints

- No new Go module dependencies. Build with `CGO_ENABLED=0 go build ./...`; test with `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...` and `gofmt -l .` clean.
- Public repo: no real IDs, tokens, hostnames, nicknames or character names anywhere (code, tests, comments, docs, commit messages). Tests use fake data only: bots `qq-main`/`tg-main`, users `20001`/`29999`, names 小明/小红, `example.invalid`. The controller runs the deployment's character-name check before merging; the names are deliberately not written in this repo.
- Code never targets a specific persona. Protected mode (`conloomapi.ModeFor` ≠ `ContentOpen`) stays intact: outputs of wake tasks and `set_status` activities go through `OutputProtected` there.
- Group chats: behavior and all existing group tests unchanged. Nothing in this plan runs for `ConvGroup`.
- A persona without `bars` renders a byte-identical system prompt (`TestRender_LegacyPersonaUnchanged` must pass unchanged). Status and bars never enter the system prompt or `PersonaDoc` values stored per turn.
- Fixed values (spec): level caps off/low/mid/high = 0/1/2/4; min gap after last message low 8h, mid 4h, high 2h; empty level = mid; follow-up 1.5–4h after the initiate; at most 2 bot-initiated messages (initiate + follow-up) since the user's last message; default quiet 00:30–08:00; inference uses the user's messages of the last 30 days (≤1000 newest), needs ≥40 messages over ≥5 local days, picks the 8-hour circular hourly window with fewest messages (ties: start closest to 00:00); planning candidates every 15m over 24h, weight `0.2 + share×10`, jitter 0–14m; poll every 15s; claim lease 2m; missed grace 30m; initiate/follow-up postpone 2m when busy; status-return postpone 1m, at most until due+30m; `set_status` minutes 0–240, activity 1–16 runes; bars ≤4, name 1–6 runes, baseline 0–100, half-life 30m–7d, description ≤40 runes; default bars 心情 (60, 6h, "高=开心放松，低=低落烦躁") and 精力 (70, 4h, "高=有精神，低=累、困"); `adjust_buff` delta −10..10 non-zero, reason 1–40 runes, one per bar per task, daily net ±30 per bar, value 0–100; bands 0–19 很低, 20–39 偏低, 40–59 中等, 60–79 偏高, 80–100 很高; state block ≤300 runes; buff events kept 30 days.
- Copy strings are Chinese and come from the spec §6 and §9 verbatim (wording of directives may be polished but must keep every listed constraint).
- Times stored with the store's `fmtTime`; local days and hours use the orchestrator's `o.loc` (`server.timezone`).

## Review Focus

1. The user sends a message while a wake task is queued or its model call is running: the wake output must not be delivered and no second reply appears (Task 9 test `TestWake_UserSpeaksDuringWakeCancels`).
2. The process was down past due times: on the first poll an overdue (>30m) status return clears the status silently, an overdue initiate is re-planned, an overdue follow-up is dropped; no burst of messages (Task 10 test `TestWakeLoop_MissedTimers`).
3. The conversation's persona changes so a stored bar no longer exists: it is not rendered, `adjust_buff` rejects the name listing the current bars, the panel view omits it, nothing panics (Task 6 `TestAdjustBuff_UnknownBarListsCurrent`, Task 8 `TestState_IgnoresRemovedBars`).
4. Quiet hours that wrap midnight (`23:00`–`07:00`) and equal start/end: wrap works; equal is rejected by validation (Task 1 `TestPrivateProactive_Validate`, Task 5 `TestQuiet_ContainsWraps`).
5. `/忘记我`, panel forget, or panel chat delete while timers are pending: no message is ever sent afterwards, including a timer already claimed in flight (Task 3 `TestForgetPrivate_ClearsAutonomy`, Task 9 `TestWake_FireAfterForgetIsGone`).

---

### Task 1: Policy — `proactive.private`

**Files:**
- Modify: `pkg/conloomapi/policy.go` (ProactivePolicy ~:81-100, Validate ~:348; WithDefaults must NOT fill the new fields)
- Test: `pkg/conloomapi/policy_test.go`
- Modify: `docs/configuration.md` (群策略 proactive section), `conloom.example.yaml` (`default_private_policy.proactive.private` commented, default level comment "留空 = mid")

**Interfaces:**
- Produces:
  - `type PrivateProactivePolicy struct { Level, QuietStart, QuietEnd string }` with json `level,omitempty` / `quiet_start,omitempty` / `quiet_end,omitempty`, yaml same with `omitempty`; field `Private PrivateProactivePolicy` on `ProactivePolicy` with `json:"private,omitzero" yaml:"private,omitempty"`.
  - `const PrivateLevelOff, PrivateLevelLow, PrivateLevelMid, PrivateLevelHigh = "off","low","mid","high"`.
  - `func (p PrivateProactivePolicy) EffectiveLevel() string` — `""` → `"mid"`.
  - `func (p PrivateProactivePolicy) QuietMinutes() (start, end int, set bool)` — minutes of day; `set` false when both empty.

- [ ] **Step 1: Write failing tests** `TestPrivateProactive_Validate` (table): `{}` ok; level `low|mid|high|off` ok; level `"medium"` error mentioning `proactive.private.level`; quiet `23:00`/`07:00` ok; only one of the two set → error; `24:00` → error; equal `08:00`/`08:00` → error. `TestPrivateProactive_EffectiveLevel` (`""`→mid). `TestPrivateProactive_QuietMinutes` (`00:30`/`08:00` → 30, 480, true; empty → false). `TestWithDefaults_KeepsPrivateZero`: stored policy without `private` stays zero after `WithDefaults`; a group default with non-zero `private` is not copied into a stored proactive section that exists. JSON round-trip: a policy with zero `Private` marshals without a `"private"` key.
- [ ] **Step 2: Run** `go test ./pkg/conloomapi/ -run 'PrivateProactive|KeepsPrivateZero'` → FAIL (undefined).
- [ ] **Step 3: Implement** the type, constants, methods and validation (only non-empty values validated; struct must stay comparable — strings only). Update docs and example config.
- [ ] **Step 4: Run** `go test ./pkg/conloomapi/ ./internal/config/` → PASS.
- [ ] **Step 5: Commit** `feat(policy): proactive.private level and quiet hours for private chats`

### Task 2: Persona bars

**Files:**
- Modify: `pkg/conloomapi/persona.go` (PersonaDoc ~:17, Validate ~:116, rune caps ~:39-47)
- Test: `pkg/conloomapi/persona_test.go`, `internal/persona/persona_test.go` (legacy render unchanged)
- Modify: `docs/configuration.md` (persona section: `bars`), `conloom.example.yaml` (commented `bars` example with generic names)

**Interfaces:**
- Produces:
  - `type PersonaBar struct { Name string; Baseline int; HalfLife Duration; Description string }` (json/yaml `name`, `baseline`, `half_life`, `description,omitempty`; `Duration` is the existing conloomapi duration type).
  - Field `Bars []PersonaBar` (`json:"bars,omitempty" yaml:"bars,omitempty"`) on `PersonaDoc`.
  - `func DefaultBars() []PersonaBar` — 心情 (60, 6h, "高=开心放松，低=低落烦躁"), 精力 (70, 4h, "高=有精神，低=累、困").
  - `func (p PersonaDoc) EffectiveBars() []PersonaBar` — `p.Bars` when non-empty else `DefaultBars()`.

- [ ] **Step 1: Write failing tests** `TestPersonaBars_Validate`: 5 bars → error `bars`; name `""`, 7 runes, containing space, `【`, `：`, `:` → error `bars[i].name`; duplicate names → error; baseline −1/101 → error; half-life 29m / 8d → error; description 41 runes → error; valid two bars ok. `TestPersonaBars_Effective`: nil → DefaultBars; set → as set. In `internal/persona`: `TestRender_LegacyPersonaUnchanged` still passes and a new `TestSystemPrompt_BarsNotRendered` shows a persona with bars yields the same system prompt as without.
- [ ] **Step 2: Run** `go test ./pkg/conloomapi/ ./internal/persona/ -run 'Bars|Legacy'` → FAIL.
- [ ] **Step 3: Implement** fields and validation; do not touch `DefaultPersona()` or `Render`.
- [ ] **Step 4: Run** `go test ./pkg/conloomapi/ ./internal/persona/ ./internal/config/` → PASS.
- [ ] **Step 5: Commit** `feat(persona): configurable hidden bars with defaults`

### Task 3: Store — migration 0012, status, timers, message times, forget/delete

**Files:**
- Create: `internal/store/sqlite/migrations/0012_autonomy.sql` (all four tables + indexes exactly as spec §8.2 and §10)
- Modify: `internal/store/records.go` (types; `Ingest` gets `FireTimer`), `internal/store/store.go` (interface), `internal/store/sqlite/delete.go` (`conversationTables`), `internal/store/sqlite/memory.go` (`ForgetPrivate` ~:198), the sqlite ingest implementation (find `CancelTaskID` handling)
- Create: `internal/store/sqlite/autonomy.go`, `internal/store/sqlite/autonomy_test.go`
- Test: `internal/store/sqlite/delete_test.go` (`keyedTables`, `seedConversation`), `internal/store/sqlite/memory_test.go` or the private-forget test file

**Interfaces:**
- Produces (records.go):
  ```go
  type TimerKind string
  const (TimerStatusReturn TimerKind = "status_return"; TimerInitiate TimerKind = "initiate"; TimerFollowUp TimerKind = "follow_up")
  type ConversationTimer struct { ConversationKey string; Kind TimerKind; BotInstanceID, PlatformUserID string; DueAt time.Time; AnchorSeq int64; LeaseUntil, CreatedAt time.Time }
  type TimerRef struct { ConversationKey string; Kind TimerKind; DueAt time.Time }
  type ConversationStatus struct { ConversationKey, Activity, TaskID string; StartedAt, UntilAt, UpdatedAt time.Time }
  var ErrTimerGone = errors.New("store: timer gone")
  ```
  `Ingest.FireTimer *TimerRef` — when set, the ingest transaction deletes that timer row (matching key, kind and due_at); no row → whole transaction rolled back, returns `ErrTimerGone`.
- Produces (store.Store):
  ```go
  SetStatus(ctx context.Context, s ConversationStatus, ret *ConversationTimer) error // upsert status; ret!=nil upserts its status_return timer, nil deletes it; one tx
  GetStatus(ctx context.Context, convKey string) (ConversationStatus, error)         // ErrNotFound
  ClearStatus(ctx context.Context, convKey string, untilAt time.Time) (bool, error)   // zero untilAt = unconditional; also deletes the status_return timer
  ArmTimer(ctx context.Context, t ConversationTimer) error                            // upsert by (key, kind), resets lease
  CancelTimers(ctx context.Context, convKey string, kinds ...TimerKind) (int, error)
  ListTimers(ctx context.Context, convKey string) ([]ConversationTimer, error)
  ClaimDueTimers(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]ConversationTimer, error) // due_at<=now AND lease_until<now; sets lease_until=now+lease; ordered by due_at
  PostponeTimer(ctx context.Context, ref TimerRef, dueAt time.Time) (bool, error)     // only if (key,kind,due_at) still match; clears lease
  UserMessageTimes(ctx context.Context, convKey string, since time.Time, limit int) ([]time.Time, error) // non-bot occurred_at, newest first
  ```

- [ ] **Step 1: Write failing tests** in `autonomy_test.go`: `TestStatus_SetGetClear` (set with timer → `ListTimers` has one status_return; set again replaces; `ClearStatus` with wrong untilAt → false, with right → true and timer gone); `TestTimers_ArmClaimLease` (two due, one future: claim returns 2 ordered; immediate re-claim returns 0; claim after lease expiry returns them again); `TestTimers_PostponeOnlyIfUnchanged` (re-armed timer → postpone with old ref false); `TestTimers_Cancel` (kinds filter; status_return kept when cancelling initiate/follow_up); `TestIngest_FireTimer` (ref matches → task created and row deleted; second ingest with same ref → `ErrTimerGone` and no task/event row); `TestUserMessageTimes` (skips bot messages, respects since and limit, newest first). `TestForgetPrivate_ClearsAutonomy`: status, timers, buffs and buff events rows of that conversation are gone after `ForgetPrivate`, another conversation's rows remain. Update `keyedTables`/`seedConversation` so `TestDeleteConversation_CoversEveryKeyedTable` covers the 4 new tables. A migration test that 0012 only adds tables (pattern in `memory_test.go` ~:134).
- [ ] **Step 2: Run** `go test ./internal/store/...` → FAIL.
- [ ] **Step 3: Implement** migration, types, methods (read-modify-write in `inTx`; `NOT NULL` columns; `fmtTime`), `FireTimer` in ingest, `ForgetPrivate` extension (`conversation_status`, `conversation_timers`, `conversation_buffs`, `conversation_buff_events`), `conversationTables`. Fakes embed `store.Store`, so no fake changes are needed.
- [ ] **Step 4: Run** `go test -race ./internal/store/... ./internal/core/... ./internal/memory/...` → PASS.
- [ ] **Step 5: Commit** `feat(store): autonomy tables, status, durable conversation timers`

### Task 4: Store — bars

**Files:**
- Modify: `internal/store/records.go`, `internal/store/store.go`, `internal/store/sqlite/autonomy.go`
- Test: `internal/store/sqlite/autonomy_test.go`

**Interfaces:**
- Consumes: tables from Task 3.
- Produces:
  ```go
  type BuffValue struct { ConversationKey, Name string; Value float64; Day string; DayDelta int; UpdatedAt time.Time }
  type BuffEvent struct { ID int64; ConversationKey, Name, TaskID, Source string; Before, After float64; Reason string; CreatedAt time.Time }
  const BuffSourceModel, BuffSourcePanel, BuffSourceAPI = "model", "panel", "api"
  var ErrBuffDuplicate = errors.New("store: bar already adjusted by this task")
  GetBuffs(ctx context.Context, convKey string) ([]BuffValue, error)
  UpdateBuff(ctx context.Context, convKey, name string, ev BuffEvent, apply func(cur BuffValue, found bool) (next BuffValue, before float64, err error)) (BuffValue, error)
  ListBuffEvents(ctx context.Context, convKey string, limit int) ([]BuffEvent, error) // newest first
  PurgeBuffEvents(ctx context.Context, before time.Time) (int64, error)
  ```
  `UpdateBuff` runs in one tx: if `ev.TaskID != ""` and an event `(task_id,name)` exists → `ErrBuffDuplicate`, nothing changes; else calls `apply`, upserts `next` and inserts `ev` with `Before = before` (callers report the decayed current value, or the baseline when nothing is stored) and `After = next.Value`. (Amended during Task 6.)

- [ ] **Step 1: Write failing tests** `TestBuffs_UpdateAndList` (first update inserts with caller Before; second uses stored value as Before; events newest first), `TestBuffs_DuplicateTask` (same task+name → `ErrBuffDuplicate`, value unchanged; same task other name ok; empty task id never duplicate), `TestBuffs_ApplyErrorRollsBack`, `TestBuffs_Purge`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `go test -race ./internal/store/...` → PASS.
- [ ] **Step 5: Commit** `feat(store): hidden bar values and events`

### Task 5: `internal/autonomy` — pure logic

**Files:**
- Create: `internal/autonomy/level.go`, `bars.go`, `quiet.go`, `plan.go`, `state.go` and `_test.go` for each

**Interfaces:**
- Consumes: `conloomapi.PrivateProactivePolicy`, `conloomapi.PersonaBar` (Tasks 1–2).
- Produces:
  ```go
  func DailyCap(level string) int                 // off 0, low 1, mid 2, high 4, unknown 0
  func MinGap(level string) time.Duration         // low 8h, mid 4h, high 2h, else 0
  const MaxUnanswered = 2
  func Band(v float64) string                      // 很低/偏低/中等/偏高/很高 on rounded v
  func Decay(v0 float64, baseline int, halfLife, elapsed time.Duration) float64 // baseline + (v0-baseline)*0.5^(elapsed/halfLife); elapsed<=0 → v0
  func Clamp(v float64) float64                    // [0,100]
  type Quiet struct { Start, End int; Inferred bool } // minutes of day; Start==End never produced
  var DefaultQuiet = Quiet{Start: 30, End: 480}
  func (q Quiet) Contains(t time.Time, loc *time.Location) bool // wraps midnight
  func (q Quiet) String() string                   // "00:30–08:00"
  func ResolveQuiet(p conloomapi.PrivateProactivePolicy, times []time.Time, loc *time.Location) Quiet // policy wins; else InferQuiet
  func InferQuiet(times []time.Time, loc *time.Location) Quiet // <40 msgs or <5 local days → DefaultQuiet (Inferred=false); else 8h window, Inferred=true
  type Hist [24]int
  func HistOf(times []time.Time, loc *time.Location) Hist
  type PlanInput struct { Now, Last time.Time; Level string; Quiet Quiet; Hist Hist; Enough bool; SentOn func(day string) int; Loc *time.Location; Rand *rand.Rand }
  func PlanInitiate(in PlanInput) (time.Time, bool)
  func PlanFollowUp(now time.Time, q Quiet, loc *time.Location, r *rand.Rand) time.Time
  type StateStatus struct { Activity string; StartedAt, UntilAt time.Time }
  type StateBar struct { Name, Description string; Value float64 }
  func RenderState(st *StateStatus, bars []StateBar, loc *time.Location) string // spec §6 exact layout, "" when both empty, ≤300 runes (drop descriptions first, then trailing bars)
  ```
  `PlanInitiate` algorithm (spec §7.2): `earliest = max(Now, Last+MinGap)`; candidates every 15m in `[earliest, earliest+24h)`; drop those in `Quiet` and those whose local day has `SentOn(day) >= DailyCap`; weight `0.2 + share(hour)*10` where `share = Hist[h]/total` (all weights equal when `!Enough`); weighted pick with `Rand`, plus `Rand.Intn(15)` minutes; none → `false`; level off → `false`. `PlanFollowUp`: `now + 90m + rand(0..150m)`; if inside quiet → quiet end (next occurrence) + `rand(0..59m)`.

- [ ] **Step 1: Write failing tests**: `TestDailyCapAndGap`; `TestBand` (19.4→很低, 19.6→偏低, 59→中等, 60→偏高, 100→很高); `TestDecay` (one half-life halfway to baseline; v0 below baseline rises; elapsed 0 → v0); `TestQuiet_ContainsWraps` (23:00–07:00 contains 02:00 and 23:00, not 07:00, not 12:00; 00:30–08:00 contains 00:30 not 00:29); `TestInferQuiet` (39 msgs → default; 5 days of messages only between 10:00–23:59 → window starting 00:00, 00:00–08:00, Inferred true; ties pick start closest to 00:00); `TestResolveQuiet_PolicyWins`; `TestPlanInitiate` with seeded `rand.New(rand.NewPCG(1,2))`: result ≥ Last+gap, never inside quiet, never on a day at cap (SentOn returns cap for today → result tomorrow), mid with all-quiet-except-one-hour → inside that hour, off → false; `TestPlanInitiate_PrefersActiveHours` (1000 samples, ≥70% land in the user's two active hours); `TestPlanFollowUp` (1.5–4h; moved out of quiet); `TestRenderState` (exact string for status+2 bars from spec §6 using 20:10/20:30 in Asia/Shanghai; status only; bars only; empty → ""; 300-rune cap drops descriptions first).
- [ ] **Step 2: Run** `go test ./internal/autonomy/` → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(autonomy): quiet hours, planning, decay and state rendering`

### Task 6: Tools `set_status` and `adjust_buff`, skills `status` and `buff`

**Files:**
- Create: `internal/skills/tools/status.go`, `internal/skills/tools/buff.go`, tests next to them; `internal/skills/builtin/status/SKILL.md`, `internal/skills/builtin/buff/SKILL.md`
- Modify: `internal/skills/tools/tools.go` (add `intArg` next to `stringArg`; interfaces), `internal/app/skills.go` (`toolDeps`, `builtinTools`), `internal/app/skills_test.go` (`TestBuiltinTools` list), `internal/app/app.go` (wire a `PersonaSource` adapter), `docs/skills.md`, `conloom.example.yaml` (mention `status`, `buff` in the bots' skills comment)

**Interfaces:**
- Consumes: Task 3 `SetStatus/ClearStatus/GetStatus/ListTimers`, Task 4 `UpdateBuff/GetBuffs`, Task 5 `Band/Decay/Clamp`, Task 2 `EffectiveBars`.
- Produces:
  ```go
  type PersonaSource interface { Persona(ctx context.Context, botInstanceID, convKey string) (conloomapi.PersonaDoc, error) } // in tools.go; app adapter uses cfg.Bot(id).PersonaTemplate + persona.Service.Current
  type StatusStore interface { SetStatus(...); ClearStatus(...) } // subset of store.Store
  type BuffStore interface { UpdateBuff(...); GetBuffs(...) }
  func NewSetStatus(st StatusStore, pol PolicySource, personas PersonaSource, rv Reviewer) skills.Tool // name "set_status"
  func NewAdjustBuff(st BuffStore, personas PersonaSource) skills.Tool                          // name "adjust_buff"
  const StatusMaxMinutes = 240; const ActivityMaxRunes = 16
  func CleanActivity(s string) string // one line, no control chars, no 【】, trimmed
  ```
  `set_status` (spec §4): private only (else "只能在私聊里用。"); `minutes` via `intArg` (accept 20, 20.0, "20"; reject "20分钟" with a sentence); 0 → `ClearStatus(zero)` → "已结束当前状态。"; activity cleaned, 1–16 runes, reviewed with `OutputProtected` when `ModeFor(persona, private) != ContentOpen` else `Output`; blocked → "这个状态不能用。"; writes `ConversationStatus{StartedAt: inv.Now, UntilAt: inv.Now+minutes, TaskID: inv.TaskID}` with a status_return timer `{Kind: TimerStatusReturn, DueAt: UntilAt, BotInstanceID, PlatformUserID: inv.PlatformUserID}`; result "已记下：{activity}，约 {m} 分钟（到 {HH:MM}）。到时你会自己回来说话。" or, when the policy's `proactive.enabled` is false, "…时间到了状态会自动结束。".
  `adjust_buff` (spec §5): private only; unknown name → "没有这一条。现有：心情、精力。" (current bars); delta integer −10..10, 0 rejected; reason 1–40 runes; `UpdateBuff` with `apply` = decay stored value to `inv.Now` (or start at baseline when not found), reset `DayDelta` when `Day` ≠ local day of `inv.Now`, clamp delta so `|DayDelta+delta| ≤ 30`, clamp value 0–100; `ErrBuffDuplicate` → "这一条这次已经调过了。"; result "心情：中等 → 偏高" (bands only, never numbers). Event source `model`, `TaskID: inv.TaskID`.
  SKILL.md frontmatter: `name: status|buff`, `tools: [set_status]|[adjust_buff]`, `conversations: [private]`; bodies ≤600 runes covering spec §4/§5 guidance; no numbers of current state.

- [ ] **Step 1: Write failing tests** (use `tools/helpers_test.go` helpers): `TestSetStatus_SetsStatusAndReturnTimer`, `TestSetStatus_MinutesForms` (20, 20.0, "20" ok; "20分钟", -1, 241 rejected with nil error), `TestSetStatus_ZeroEnds`, `TestSetStatus_ActivityCleanedAndReviewed` (newline/【】 removed; 17 runes rejected; protected persona + blocked word → "这个状态不能用。" and nothing stored), `TestSetStatus_GroupRefused`, `TestSetStatus_SwitchOffWording`; `TestAdjustBuff_DecayThenDelta` (stored 90 at baseline 60 half-life 6h, 6h later +5 → stored 80, result shows bands), `TestAdjustBuff_Caps` (delta 15 → applied 10; daily +30 reached → further + clamps to 0 change with message; next local day resets), `TestAdjustBuff_OncePerTask`, `TestAdjustBuff_UnknownBarListsCurrent`, `TestAdjustBuff_NoNumbersInResult`. Update `TestBuiltinTools` and `TestTools_DeclareResultLimits`. Registry check: a bot config with skills `[status, buff]` loads.
- [ ] **Step 2: Run** `go test ./internal/skills/... ./internal/app/` → FAIL. **Step 3: Implement**, update `docs/skills.md` (new tools; bot-initiated runs only get `adjust_buff`, see Task 9). **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(skills): set_status and adjust_buff tools`

### Task 7: contextbuilder — `State`, no current message, strip rules

**Files:**
- Modify: `internal/contextbuilder/build.go` (Input ~:48, BaseTokens ~:78, Build ~:150, renderCurrent ~:381), `internal/contextbuilder/strip.go`
- Test: `internal/contextbuilder/build_test.go`, `internal/contextbuilder/strip_test.go`
- Modify: `docs/architecture.md` (prompt layout ~:370, strip description ~:411)

**Interfaces:**
- Produces: `Input.State string` placed after `Memory` and before the recent history (`Memory + "\n\n" + State + "\n\n" + 【近期消息…`), counted in `BaseTokens` as `Estimate(State+"\n\n")`. When `len(Current)==0`, the final user message has no `【当前消息】` header: it is the history part followed by `Directive` (Directive required in that case).

- [ ] **Step 1: Write failing tests** `TestBuild_StateAfterMemory` (exact last-message string with Memory, State, history, current), `TestBaseTokens_CountsState`, `TestBuild_NoCurrentMessage` (no `【当前消息】`, ends with the directive, history kept), `TestStripTranscriptPrefix_EchoedStateBlock` (state header + 正在/心情/精力 lines + "这些只影响…" footer + "好呀" → "好呀"; a state block followed by the memory block → stripped too; "心情：不错哦" alone without header is NOT stripped).
- [ ] **Step 2: Run** `go test ./internal/contextbuilder/` → FAIL. **Step 3: Implement** (strip: `^【你的状态[^\n]*(?:\n|$)` header then block lines reusing the memory line rule plus `正在[：:]` and `这些只影响` prefixes). **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(contextbuilder): hidden state block and turns without a current message`

### Task 8: core — state in private replies, tool gating

**Files:**
- Create: `internal/core/autonomy_state.go`, `internal/core/autonomy_state_test.go`
- Modify: `internal/core/runner.go` (Input construction ~:279), `internal/core/skills.go` (`newSkillRun` ~:85-120)

**Interfaces:**
- Consumes: Tasks 3–7.
- Produces: `func (o *Orchestrator) stateBlock(ctx context.Context, task store.TaskRecord, bot config.Bot, doc conloomapi.PersonaDoc) string` — private only; status shown while `UntilAt > o.now()`; bars only when the bot's skills include `buff`, values = stored decayed to now or baseline, ordered as `doc.EffectiveBars()`, names not in the persona ignored; read errors logged and leave the block out. `execute` sets `in.State`. The persona doc comes from the same `persona.Service.Current` the system prompt uses (add a small accessor if `SystemPrompt` does not expose it; do not change the prompt cache key).

- [ ] **Step 1: Write failing tests** (`newTestOrchestrator`, `env.event(conloomapi.ConvPrivate, …)`, `lastContent`): `TestState_InjectedInPrivate` (bot skills `[status, buff]`, status row active + one bar row → request contains the exact block from `autonomy.RenderState`), `TestState_ExpiredStatusHidden`, `TestState_BarsNeedBuffSkill`, `TestState_IgnoresRemovedBars` (stored bar "饥饿" not in persona → not rendered), `TestState_NeverInGroups`, `TestSkills_StatusAndBuffPrivateOnly` (group addressed message never offers `set_status`/`adjust_buff`).
- [ ] **Step 2: Run** `go test ./internal/core/ -run 'State_|StatusAndBuff'` → FAIL. **Step 3: Implement.** **Step 4: Run** `go test -race ./internal/core/` → PASS (all existing tests unchanged).
- [ ] **Step 5: Commit** `feat(core): inject the hidden state into private replies`

### Task 9: core — wake tasks (fire and execute)

**Files:**
- Modify: trigger constants next to `TriggerProactive` (grep `TriggerProactive =`), `internal/core/runner.go` (`execute` ~:175-330, `finish` ~:140, `directive` ~:502, `speaker` ~:531), `internal/core/skills.go` (`newSkillRun`), `internal/core/memory.go` (`injectMemory` ~:59, `noteInteraction` ~:207), `internal/core/recover.go` (`recoverTask` actor ~:148), `internal/core/commands.go` (`/stop` ~:176), `internal/core/orchestrator.go` (`submit` ~:464 cancel path), `internal/core/copy.go` (directives), `internal/obs/metrics.go` (`conloom_wake_total{kind,result}`)
- Create: `internal/core/wake.go`, `internal/core/wake_test.go`

**Interfaces:**
- Consumes: Task 3 (`Ingest.FireTimer`, `ErrTimerGone`, timers, status), Task 5 (`DailyCap`, `MaxUnanswered`, `PlanFollowUp`), Task 7 (`Input` without current), Task 8 (`stateBlock`).
- Produces:
  - `TriggerStatusReturn`, `TriggerInitiate`, `TriggerFollowUp` and `func isWake(t conloomapi.Trigger) bool` (use the existing trigger type).
  - `func wakeActor(userID string) string` → `"wake:"+userID`.
  - `func (o *Orchestrator) fireTimer(ctx context.Context, t store.ConversationTimer) error` — builds the internal event `conloom:timer:<key>:<kind>:<due_at>`, ingests a task with `FireTimer`, trigger by kind, no trigger messages, `EventOccurredAt = o.now()`, background priority, then `sched.Restore` with `wakeActor`. `ErrTimerGone` → returns nil (nothing to do).
  - Proactive log kinds `private_initiate`, `private_followup`, `private_skip` (constants next to `ProactiveSent`).
  - Copy: `CopyDirectiveInitiate(name, now string, gap string, protected bool) string`, `CopyDirectiveFollowUp(name string, protected bool) string`, `CopyDirectiveStatusReturn(activity string) string` — text from spec §9; protected appends "不要表现出恋爱或暧昧的想念。" to the first two.
  - `func (o *Orchestrator) cancelWakes(ctx context.Context, convKey, userID, reason string)` — cancels queued/running wake tasks of the conversation (all three kinds) and their unsent parts (as `cancelUnsentParts`, with audit); a cancelled status return also `ClearStatus(zero)`.
- Behaviour (spec §9): execute skips trigger loading and the forgotten-drop for wake tasks; speaker = newest user display name (`LatestDisplayNames` or newest non-bot message); memory and state injected for that user; history from `forgotten_at`; no `【当前消息】`; directive by kind (status return reads the activity from `GetStatus`; missing status → skip); tools only `adjust_buff` when enabled (never `set_status`, never memory tools); non-direct (echo-only → skipped); no error copy; output review by mode; `noteInteraction` skips wake tasks. In `finish` under `proactiveLocks.lock(convKey)`: a user message newer than the task's creation → withhold (cancelled, nothing sent); initiate/follow-up re-check `DailyCap` and unanswered count; on send log `private_initiate`/`private_followup`, then arm `follow_up` via `PlanFollowUp` after an initiate when unanswered < `MaxUnanswered`; on skip log `private_skip` and (initiate only) call the re-plan hook (`o.replanInitiate`, a no-op stub here, implemented in Task 10); status return always ends with `ClearStatus(task's status UntilAt)`. `submit` calls `cancelWakes` for every non-ignored private user message before dispatch.

- [ ] **Step 1: Write failing tests** in `wake_test.go` (use `newGatedModel`, `hookStore`, `privateConvKey`, fake clock): `TestWake_StatusReturnSends` (status "洗澡" + fire → request has `CopyDirectiveStatusReturn("洗澡")`, no `【当前消息】`, state block; one outbox action on the platform channel; status cleared); `TestWake_SkipSendsNothing` (`[[SKIP]]` → no action, `private_skip` logged for initiate); `TestWake_InitiateLogsAndArmsFollowUp`; `TestWake_FollowUpNoFurtherTimer`; `TestWake_ProtectedDirectiveAndReview` (persona `minor: true` → directive contains "不要表现出恋爱或暧昧的想念。" and output goes through `OutputProtected`); `TestWake_OpenDirectiveNoProtectedLine`; `TestWake_ToolsOnlyBuff`; `TestWake_NoAffinityBump`; `TestWake_UserSpeaksDuringWakeCancels` (gate the model in the wake task, submit a user message, release → wake output never queued, user turn answered once, audit present); `TestWake_FireTwiceOneTask` (same timer fired twice → one task); `TestWake_FireAfterForgetIsGone` (claim, then `ForgetPrivate`, then `fireTimer` → no task); `TestWake_RecoverKeepsWakeActor`; `TestWake_StopCancelsWake`.
- [ ] **Step 2: Run** `go test ./internal/core/ -run Wake_` → FAIL. **Step 3: Implement.** **Step 4: Run** `go test -race ./internal/core/` → PASS, including all existing proactive/join/turn tests unchanged.
- [ ] **Step 5: Commit** `feat(core): trigger-less wake tasks for status returns and initiating`

### Task 10: core — wake loop, planning, policy hooks, `/主动` in private

**Files:**
- Create: `internal/core/wake_loop.go`, `internal/core/wake_loop_test.go`, `internal/core/wake_plan.go`, `internal/core/wake_plan_test.go`
- Modify: `internal/core/recover.go` (start loop next to `maintain` ~:86; purge buff events in `maintain`/`cleanup`), `internal/core/cleanup.go` (label), `internal/core/orchestrator.go` (`submit`: re-plan after a private user message; `PolicyChanged` ~:273: private branch), `internal/core/proactive.go` (`proactiveCommand` ~:229 private branch), `internal/core/copy.go` (private `/主动` copy; replace `CopyProactivePrivateOnly`), `internal/policy/policy.go` (`SetPrivateLevel` mirroring `SetProactiveEnabled` ~:177), `internal/core/proactive_test.go` (private `/主动` test replaced), `docs/usage.md`, `docs/architecture.md`

**Interfaces:**
- Consumes: Tasks 3, 5, 9.
- Produces:
  - `const wakePollInterval = 15 * time.Second; wakeLease = 2 * time.Minute; wakeMissedGrace = 30 * time.Minute`
  - `func (o *Orchestrator) wakeLoop(ctx context.Context)` and `func (o *Orchestrator) pollWakes(ctx context.Context)` (one poll; tests call it directly with the fake clock).
  - `func (o *Orchestrator) replanInitiate(ctx context.Context, convKey string)` — cancels `initiate`/`follow_up`, then arms a new `initiate` via `autonomy.PlanInitiate` unless a spec §7.2 exclusion applies (switch off, level off, paused, user not allowed by `private_chat`, platform `panel`, unanswered ≥ `MaxUnanswered`). Inputs: `UserMessageTimes(since = max(now−30d, forgotten_at), 1000)`, `ResolveQuiet`, today's and later days' counts from `proactive_log` (`private_initiate`+`private_followup`), `AnchorSeq` = newest message seq. Random source `o.rand` (seedable via `testOpts`).
  - `func (o *Orchestrator) privateProactiveStatus(ctx, convKey) (...)` used by `/主动` and Task 11.
  - `func (s *policy.Service) SetPrivateLevel(ctx context.Context, convKey, level, actor string) (conloomapi.PolicyVersion, error)`.
- Poll behaviour (spec §7.4, §8): claim due timers; overdue by more than 30m → status return `ClearStatus` (metric `missed`), initiate `replanInitiate`, follow-up delete; run the §7.4 checks in order with the listed outcomes (drop = delete row; re-plan = `replanInitiate`; postpone = `PostponeTimer` +2m initiate/follow-up, +1m status return until due+30m then clear); passing timers go to `fireTimer`. Respect `o.closing`. `submit` calls `replanInitiate` after `cancelWakes` for private user messages. `PolicyChanged` for private conversations calls `replanInitiate` (which cancels when disabled).
- `/主动` in private (spec §3): no args → status line, e.g. "主动找你：中（今天 1/2 次；免打扰 00:30–08:00，按你的作息推算）"; "关|低|中|高" → `SetPrivateLevel` then confirm; other args → usage. Group behaviour unchanged.

- [ ] **Step 1: Write failing tests**: `TestReplan_ArmsInitiateAfterUserMessage` (private message → one `initiate` timer ≥ now+4h, not in quiet); `TestReplan_Exclusions` (switch off / level off / paused / panel platform / user removed from allowlist / 2 unanswered → no timer); `TestReplan_UserMessageCancelsFollowUp`; `TestWakeLoop_FiresDue` (advance clock past due, `pollWakes` → task created and answered); `TestWakeLoop_Checks` (quiet → re-planned outside quiet; cap reached → re-planned to next day; busy → postponed 2m; newer user message → dropped); `TestWakeLoop_StatusReturnIgnoresQuietAndCap`; `TestWakeLoop_StatusReturnSwitchOffClearsStatus`; `TestWakeLoop_MissedTimers` (due 31m ago: status cleared & no message, initiate re-planned to the future, follow-up deleted); `TestWakeLoop_StopsOnClose`; `TestPolicyChanged_PrivateReplans`; `TestProactiveCommand_Private` (status line; "高" sets level and audit; "开" → usage; group `/主动` tests unchanged); `TestPolicy_SetPrivateLevel` in `internal/policy`.
- [ ] **Step 2: Run** `go test ./internal/core/ ./internal/policy/ -run 'Replan|WakeLoop|PolicyChanged_Private|ProactiveCommand_Private|SetPrivateLevel'` → FAIL. **Step 3: Implement**; update docs (usage: private `/主动` and the rules; architecture: timer table, loop, wake flow, state block). **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `feat(core): wake loop, initiate planning and private /主动`

### Task 11: Panel and gateway API

**Files:**
- Create: `pkg/conloomapi/autonomy.go`, `internal/core/autonomy_service.go` (+ test), `internal/panel/autonomy.go` (+ test), `internal/gateway/autonomy.go` (+ test)
- Modify: `internal/panel/data_routes.go`, `internal/panel/write_routes.go`, `internal/panel/common.go` (Deps + private-only guard like `memoryConversation` ~:65), `internal/panel/conversations.go` (~:104 badge field), `internal/panel/respond.go` (map new invalid errors to 400), `internal/gateway/server.go` (~:85), `internal/app/app.go` (wire), `docs/api.md`, `docs/panel.md` (endpoints + audit kinds `autonomy.buff`, `autonomy.status.clear`)

**Interfaces:**
- Produces:
  ```go
  // pkg/conloomapi/autonomy.go
  type AutonomyState struct { Status *AutonomyStatus `json:"status"`; Bars []AutonomyBar `json:"bars"`; Events []AutonomyBarEvent `json:"events"`; Proactive AutonomyProactive `json:"proactive"` }
  type AutonomyStatus struct { Activity string `json:"activity"`; StartedAt time.Time `json:"started_at"`; UntilAt time.Time `json:"until_at"` }
  type AutonomyBar struct { Name string `json:"name"`; Value int `json:"value"`; Band string `json:"band"`; Baseline int `json:"baseline"`; Description string `json:"description,omitempty"` }
  type AutonomyBarEvent struct { Name, Source, Reason string; Before, After int; At time.Time } // json snake_case
  type AutonomyProactive struct { Enabled bool; Level string; SentToday, DailyCap int; QuietStart, QuietEnd string; QuietInferred bool; UnansweredLeft int; Timers []AutonomyTimer } // json snake_case
  type AutonomyTimer struct { Kind string `json:"kind"`; DueAt time.Time `json:"due_at"` }
  type AutonomyBarPut struct { Value *int `json:"value"`; Reason string `json:"reason"` }
  // internal/core/autonomy_service.go
  func (o *Orchestrator) Autonomy() *AutonomyService
  func (s *AutonomyService) State(ctx context.Context, convKey string) (conloomapi.AutonomyState, error) // ErrNotPrivate for non-private
  func (s *AutonomyService) SetBar(ctx context.Context, convKey, name string, value int, reason, source, actor string) error // value 0..100, name must be a current bar; event source panel|api; audit autonomy.buff
  func (s *AutonomyService) ClearStatus(ctx context.Context, convKey, actor string) error // audit autonomy.status.clear
  ```
- Routes: panel `GET /panel/api/conversations/{key}/autonomy`, `PUT …/autonomy/buffs/{name}`, `DELETE …/autonomy/status` (session + CSRF + Origin as existing writes; 503 when the service is nil; 400 non-private); gateway `GET|PUT|DELETE /api/v1/conversations/{id}/autonomy…` with `memory:read` / `memory:write`. `conversationView` gains `proactive_level` (effective level for private, "" for group).

- [ ] **Step 1: Write failing tests**: service `TestAutonomyService_State` (status, decayed bars in persona order, events, proactive block with today's count and next timers), `TestAutonomyService_SetBarBypassesCaps`, `TestAutonomyService_RejectsGroupAndUnknownBar`; panel `TestPanelAutonomy_GetPutDelete` (CSRF required, audit rows, 400 for group, 400 for value 101, 404 unknown conversation); gateway `TestGatewayAutonomy_Scopes`; `TestConversations_PrivateProactiveLevel`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement**; docs. **Step 4: Run** `go test -race ./internal/panel/... ./internal/gateway/... ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(panel,gateway): autonomy view, bar edits and status clear`

### Task 12: Panel UI

**Files:**
- Create: `internal/panel/web/views/autonomy.js`
- Modify: `internal/panel/web/views/conversation.js` (`TABS` ~:9, `TAB_VIEWS` ~:622, `tabsOf` ~:635 private only; policy form private level + quiet inputs near ~:143; persona form bars editor), `internal/panel/web/views/conversations.js` (~:11 badge: private shows `主动：低|中|高` or `主动关`), `internal/panel/web/web_test.go` (~:40 file list), `docs/panel.md`

**Interfaces:**
- Consumes: Task 11 endpoints and `proactive_level`; existing helpers `write()`, `flash`, `select()`, `put/post/del/convPath`.
- Produces: tab "自主" with sections 当前状态 (activity, start/end, "结束状态" button → DELETE), 状态条 (`<meter min=0 max=100>` + band + baseline + number input + reason + 保存 → PUT), 最近变化 (table), 主动找人 (switch, level, `今天 x/y`, quiet hours with "推算"/"设定", unanswered left, next timers). Policy form: level `<select>` (留空=中、关、低、中、高) and two `HH:MM` inputs, sent inside `proactive.private`. Persona form: up to 4 bar rows (name, baseline, half-life, description) with add/remove. No `innerHTML`, no inline `style=`, no external URLs.

- [ ] **Step 1: Write failing test**: `web_test.go` includes `autonomy.js` and its checks pass (no `innerHTML`, no `http(s)://`); add a JS-free Go test that the panel serves `views/autonomy.js`.
- [ ] **Step 2: Run** `go test ./internal/panel/...` → FAIL. **Step 3: Implement.** **Step 4: Run** → PASS; also load the panel locally (`go run ./cmd/conloom` with the example config is not required) — at minimum `node --check internal/panel/web/views/*.js` if `node` is available.
- [ ] **Step 5: Commit** `feat(panel): autonomy tab, private initiative settings and bar editor`

---

## Deployment notes (for the controller, not a task)

- Merge per spec §16 (stash the deployer's uncommitted relationship-hint work, ff-merge, pop, resolve so the order is Memory → Relationship → State; leave that work uncommitted).
- Live config: add `status` and `buff` to the skills of bots that serve private chats. Default private level is mid, so private chats with `proactive.enabled: true` start initiating after the user's next message.
