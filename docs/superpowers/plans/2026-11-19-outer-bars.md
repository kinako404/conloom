# Outer Bars Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A persona bar can be marked `outer` (a visible state such as fullness or blushing) and can set its own step limits. `PersonaBar` gains `kind` (`inner`/`outer`, empty = inner), `max_step` (1–100, default 10) and `max_day` (1–100, default 30, ≥ the effective `max_step`). `adjust_buff` clamps by the bar's own limits and says when it cut a delta; outer bars show in the hidden state block with their value under their own heading; the panel edits and marks them. Inner bars with default limits behave and render byte-identically.

**Architecture:** `pkg/conloomapi/persona.go` gets the fields, constants, `Outer`/`EffectiveMaxStep`/`EffectiveMaxDay` and the validation; `AutonomyBar` mirrors the three fields (omitempty). The exchange format needs no code change (fields are omitempty; the strict decoder already exists). `internal/skills/tools/buff.go` takes step and daily limits from the bar that `scopeBars` returns (the persona of the conversation being answered; custom bars keep the defaults) and reports cuts. `autonomy.RenderState` splits bars into inner (today's lines, plus `（单次最多 ±N）` for a non-default step) and outer (after the footer, under `【外在状态（…）】`, as `名字：80/100（很高，单次最多 ±40）`); `core.stateBlock` fills the new `StateBar` fields; `StripTranscriptPrefix` learns the new header and lines. `AutonomyService.State` copies the fields; the panel persona form, profile card and autonomy tab edit and tag them.

**Tech Stack:** Go (module `github.com/kinako404/conloom`), SQLite through `internal/store/sqlite` (no migration: bar values stay keyed by name in the scope), vanilla JS + CSS in `internal/panel/web` (no build step). No new Go or JS dependencies.

**Spec:** `docs/superpowers/specs/2026-11-19-outer-bars-design.md` — read it before your task, including "Builder-check decisions", which are binding.

## Global Constraints

- Public repo: no real IDs, tokens, hosts, nicknames or character names in code, tests, comments, docs or commit messages. Fake data only: bots `bot_test`/`qq-main`, users `20001`/`29999`, names 小明/小红, bars 心情/精力/肚子/脸红/饥饿, `example.invalid`.
- Commit messages: English, conventional style, exactly as given per task.
- Before every commit: `gofmt -l .` prints nothing and `go vet ./...` passes; tests run with `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 <pkgs>`; every changed JS file passes `/usr/local/node/bin/node --check <file>`. The last task runs the full `./...`.
- User-facing copy is Chinese. Fixed values: `kind` is `inner` or `outer`, empty/omitted = inner; `max_step`/`max_day` 0/omitted = defaults 10/30 (`conloomapi.DefaultBarMaxStep`, `DefaultBarMaxDay`), otherwise 1–100 (`conloomapi.MaxBarLimit`); effective `max_day` ≥ effective `max_step`.
- Fixed strings (used verbatim):
  - outer heading: `【外在状态（别人看得出来，可以在动作和外表描写里自然带出；不说数字，不提状态条、工具或系统）】`
  - outer line: `<名字>：<round(value)>/100（<档位>，单次最多 ±<step>）` then `（<说明>）` when descriptions are kept; inner line with a non-default step: `<名字>：<档位>（单次最多 ±<step>）` then `（<说明>）`
  - `adjust_buff` step cut suffix `（超过单次上限 ±<max_step>，按 <%+d> 调整）`; daily partial cut suffix `（今天的变化快到上限 ±<max_day>，只调整了 <%+d>）`; daily cap refusal `<名字>今天的变化已经到上限（每天最多 ±<max_day>），这次没有调整。`; bad delta `delta 需要是不为 0 的整数，不超过这一条的单次上限（默认 ±10）。`
  - panel: select `类型` with `内在` / `外在`; inputs `单次最多` / `每天最多`; tag `外在`.
- Inner bars with default limits (no outer bar, no `max_step`, or `max_step` explicitly 10) produce the byte-identical state block, tool results for in-limit deltas, and system prompt; every existing `internal/autonomy/state_test.go` case passes unmodified.
- Over-limit deltas are clamped, never refused (decision 1). Bar values and the daily counter stay keyed by bar name in the memory scope; `kind`/limits come from the persona of the conversation being answered or viewed (decision 3). No exchange version bump (decision 4).
- Prefer new test files so parallel tasks do not edit the same file. Package-level test helpers in new files are prefixed by their theme (`outer…`) to avoid collisions between T3 and T4, which both add tests to `internal/core`.
- Update every comment and doc that describes changed behaviour: `PersonaDoc`/`PersonaBar`/`AutonomyBar`/`AutonomyState` docs, `NewAdjustBuff`, `dayCapped`, the `adjust_buff` spec text, `StateBar`, `RenderState`, `renderState`, `stateBlock`, `stateBlockHeader`/`stateBlockLine`, `StripTranscriptPrefix`, `AutonomyService.State`, `barsEditor`, the persona form and autonomy tab hints, `stateBody`.

## Review Focus

1. **Limit validation edges** — `max_step: 40` without `max_day` (effective day 30 < 40) and `max_day: 5` without `max_step` (5 < 10) are refused; `max_day == max_step` accepted; 0 = default, −1/101 refused; `kind: "Outer"` refused; in the form, a blank limit is omitted while `0` is an error. Owners: T1 `TestPersonaBars_ValidateKindAndLimits`, T5 `TestPersonaForm_BarKindAndLimits` + node self-check of `barLimit`.
2. **Four outer bars near the 300-rune cap** — 6-rune names, `max_step` 100, 40-rune descriptions, a 16-rune place, a 16-rune activity, custom inner bars and effects: descriptions, effects and inner bars go first; every kept outer line keeps its value and limit text whole. Owner: T3 `TestRenderState_OuterBarsCap`.
3. **Exchange of a file with the new fields** — exports without the fields carry no `kind`/`max_step`/`max_day` keys; a file with them imports and round-trips; an invalid limit in a file is refused with the field path; an unknown sibling key is still refused (what an older build does). Owner: T1 `TestExchange_OuterBarFields`.
4. **Panel chats sharing a scope with different kinds/limits** — chat A's persona has 肚子 outer `max_step` 40 `max_day` 100, chat B's 肚子 inner default: one stored value and daily counter, each chat clamps and renders by its own persona, the autonomy view of each chat reports its own kind. Owners: T2 `TestAdjustBuff_PanelChatsUseTheirPersonasLimits`, T3 `TestState_OuterBarsPerChatPersona`, T4 `TestAutonomyService_BarKindPerChat`.
5. **Clamped delta reporting** — step cut, daily partial cut, both at once (daily wins the message), negative deltas, `delta` beyond ±100, value clamped at 0/100 (not reported), refusal names the bar's own daily limit, results stay ≤ `shortResultRunes` and never show a bar value. Owner: T2 `TestAdjustBuff_PerBarLimits`, `TestAdjustBuff_NoNumbersInResult` (updated).

## Task order and parallel groups

| Stage | Tasks (parallel within a stage) | Starts after |
|---|---|---|
| A | T1 API types, validation, exchange tests | — |
| B | T2 `adjust_buff` limits + SKILL.md, T3 state block + strip, T4 `AutonomyService` fields, T5 panel | T1 merged |
| C | T6 docs + full check | all merged |

Files per task (no two tasks of one stage share a file):

- T1: `pkg/conloomapi/{persona.go,autonomy.go}`; tests `pkg/conloomapi/outer_bars_test.go` (new), `internal/persona/exchange_outer_test.go` (new).
- T2: `internal/skills/tools/buff.go`, `internal/skills/builtin/buff/SKILL.md`; tests `internal/skills/tools/buff_test.go` (expectations of `TestAdjustBuff_Caps`, `_BadArguments`, `_NoNumbersInResult` only), `internal/skills/tools/buff_limits_test.go` (new). `tools.go` needs no change (package doc lists tool names only; `shortResultRunes` = 100 holds the longer results) — verify, do not edit.
- T3: `internal/autonomy/state.go`, `internal/contextbuilder/strip.go`, `internal/core/autonomy_state.go`; tests `internal/autonomy/outer_state_test.go`, `internal/contextbuilder/strip_outer_test.go`, `internal/core/outer_state_test.go` (all new).
- T4: `internal/core/autonomy_service.go`; test `internal/core/autonomy_outer_test.go` (new).
- T5: `internal/panel/web/views/{personaform.js,profile.js,autonomy.js}`, `internal/panel/web/lib/format.js`, `internal/panel/web/style.css`; test `internal/panel/web/outer_bars_web_test.go` (new).
- T6: `docs/configuration.md`, `docs/skills.md`, `docs/panel.md`, `docs/architecture.md`, `docs/api.md`, `docs/usage.md` (only if a grep finds affected text).

---

### Task 1: Persona bar fields, validation, `AutonomyBar` fields, exchange

**Files:**
- Modify: `pkg/conloomapi/persona.go` (`PersonaDoc` doc :28-29, `PersonaBar` :65-73, constants near `MinBarHalfLife` :168, methods after `EffectiveBars` :240, `Validate` bar loop :401-427), `pkg/conloomapi/autonomy.go` (`AutonomyBar` :61-72, `AutonomyState` doc :5-14)
- Test: `pkg/conloomapi/outer_bars_test.go` (new), `internal/persona/exchange_outer_test.go` (new; helpers `newServiceWith`, `newTestStore`, `tagged`, `exchangeFile`, `exchangeTemplate`, `checkUnchanged` from `exchange_test.go`)

**Interfaces:**
- Produces (`pkg/conloomapi/persona.go`):
  ```go
  // Values of PersonaBar.Kind; empty is BarKindInner.
  const (BarKindInner = "inner"; BarKindOuter = "outer")
  // Defaults and bound of PersonaBar.MaxStep and MaxDay.
  const (DefaultBarMaxStep = 10; DefaultBarMaxDay = 30; MaxBarLimit = 100)
  type PersonaBar struct {
      Name        string   `json:"name" yaml:"name"`
      Baseline    int      `json:"baseline" yaml:"baseline"`
      HalfLife    Duration `json:"half_life" yaml:"half_life"`
      Description string   `json:"description,omitempty" yaml:"description,omitempty"`
      Kind        string   `json:"kind,omitempty" yaml:"kind,omitempty"`
      MaxStep     int      `json:"max_step,omitempty" yaml:"max_step,omitempty"`
      MaxDay      int      `json:"max_day,omitempty" yaml:"max_day,omitempty"`
  }
  func (b PersonaBar) Outer() bool            // Kind == BarKindOuter
  func (b PersonaBar) EffectiveMaxStep() int  // MaxStep, or DefaultBarMaxStep when <= 0
  func (b PersonaBar) EffectiveMaxDay() int   // MaxDay, or DefaultBarMaxDay when <= 0
  ```
  `PersonaBar` doc: a bar of private chats, inner (hidden: shown to the model as a band, only affects tone) or outer (visible: shown with its value, may show in descriptions); `adjust_buff` moves it by at most `EffectiveMaxStep` per call and `EffectiveMaxDay` net per local day. `PersonaDoc` doc: "Bars … define the bars of private chats" (drop "hidden").
- `Validate` adds, per bar (errors collected like the others):
  - `kind` not `""`/`inner`/`outer` → `%s.kind must be inner or outer, got %q`
  - `max_step` < 0 or > 100 → `%s.max_step must be in [1, 100] (0 for the default 10), got %d`; same for `max_day` with `(0 for the default 30)`
  - only when both are in range: `EffectiveMaxDay() < EffectiveMaxStep()` → `%s.max_day must be at least max_step (%d), got %d` with the effective values.
- Produces (`pkg/conloomapi/autonomy.go`), appended to `AutonomyBar`:
  ```go
  Kind    string `json:"kind,omitempty"`
  MaxStep int    `json:"max_step,omitempty"`
  MaxDay  int    `json:"max_day,omitempty"`
  ```
  Doc: the three are the viewed conversation's persona bar's as written (empty/0 = inner, defaults); a bar the bot added has none.
- Exchange (`internal/persona/exchange.go`): no code change; `ExchangeVersion` stays 1.

- [ ] **Step 1: Write failing tests**
  - `TestPersonaBars_ValidateKindAndLimits` (table on `testBars()`, pattern of `TestPersonaBars_Validate`): ok — kind `""`, `inner`, `outer`; `max_step` 0, 1, 30 (day default 30); `max_step` 100 + `max_day` 100; `max_day` 10 alone; `max_step` 20 + `max_day` 20. Errors (substring) — kind `Outer` → `bars[0].kind must be inner or outer, got "Outer"`; `max_step` −1 / 101 → `bars[0].max_step must be in [1, 100]`; `max_day` 101 → `bars[1].max_day must be in [1, 100]`; `max_step` 40 alone → `bars[0].max_day must be at least max_step (40), got 30`; `max_day` 5 alone → `bars[0].max_day must be at least max_step (10), got 5`; `max_step` 101 + `max_day` 50 → only the range error (no comparison error).
  - `TestPersonaBar_EffectiveLimits`: zero bar → 10/30, `Outer()` false; `{Kind: "outer", MaxStep: 40, MaxDay: 100}` → 40/100, true; `{Kind: "inner"}` → false.
  - `TestPersonaBars_KindAndLimitsJSONAndYAML`: `json.Marshal(DefaultBars())` contains none of `"kind"`, `"max_step"`, `"max_day"`; a bar `{肚子, 20, 2h, "高=很饱", outer, 40, 100}` round-trips through JSON (`"kind":"outer","max_step":40,"max_day":100`) and YAML (`kind: outer`, `max_step: 40`, `max_day: 100`) with `reflect.DeepEqual`.
  - `TestAutonomyBar_JSON`: `AutonomyBar{Name: "心情", …}` marshals without the three keys; with `Kind: "outer", MaxStep: 40, MaxDay: 100` it has `"kind":"outer","max_step":40,"max_day":100`.
  - `TestExchange_OuterBarFields`: a template `tagged("甲")` with default bars exports (`json.Marshal(f)`) without `"kind"`, `"max_step"`, `"max_day"`; a template whose bars are `[心情 default, 肚子 outer 40/100]` exports with them, the file decodes (`json.Unmarshal` into `ExchangeFile`) and imports into a fresh store, `Template` equal (`reflect.DeepEqual`); a raw file whose bar has `"max_step":101` → `ErrInvalidTemplate` mentioning `bars[1].max_step`, nothing stored (`checkUnchanged`); a raw file whose bar has an extra `"max_steps":40` fails `json.Unmarshal` with `unknown field "max_steps"` (the error an older build gives for the new fields).
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./pkg/conloomapi/ ./internal/persona/ -run 'KindAndLimits|EffectiveLimits|AutonomyBar_JSON|OuterBarFields'` → FAIL (undefined fields).
- [ ] **Step 3: Implement** per Interfaces; update the `PersonaDoc`, `PersonaBar`, `AutonomyBar`, `AutonomyState` docs.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./pkg/... ./internal/persona/ ./internal/config/ ./internal/core/ ./internal/panel/... ./internal/gateway/` → PASS (existing struct comparisons of `AutonomyBar` stay equal: new fields are zero).
- [ ] **Step 5: Commit** `feat(api): outer kind and per-bar step limits on persona bars`

### Task 2: `adjust_buff` per-bar limits, cut reporting, tool text, `buff` SKILL.md

**Files:**
- Modify: `internal/skills/tools/buff.go` (constants :20-33, `NewAdjustBuff` doc :45-53, `Spec` :61-72, `Call` :74-140, `dayCapped` :205-212), `internal/skills/builtin/buff/SKILL.md`
- Test: `internal/skills/tools/buff_test.go` (update expectations listed below), `internal/skills/tools/buff_limits_test.go` (new)

**Interfaces:**
- Consumes: T1 `PersonaBar.EffectiveMaxStep/EffectiveMaxDay`, `conloomapi.DefaultBarMaxStep`, `MaxBarLimit`. `scopeBars` (buff.go:145) already returns the persona's bars with all fields and custom bars with zero limits (= defaults) — keep it.
- Produces:
  ```go
  // dayCapped cuts delta so that a bar whose net change today is dayDelta stays within ±maxDay; it never turns delta around.
  func dayCapped(delta, dayDelta, maxDay int) int
  ```
  `buffMaxDelta`/`buffMaxDayDelta` are removed (the defaults live in conloomapi). `Call`: after the bar lookup, `step, dayMax := bar.EffectiveMaxStep(), bar.EffectiveMaxDay()`; `stepped := clamp(delta, ±step)`; inside the `UpdateBuff` closure `d := dayCapped(stepped, dayDelta, dayMax)` recorded in an outer `applied`; result `name：before → after` plus the daily suffix when `applied != stepped`, else the step suffix when `stepped != delta`, else nothing (copy in Global Constraints, `%+d`). `errDayCapped` → the refusal naming `dayMax`. A delta below −100 or above 100 is clamped like any other (not refused). Value clamping at 0/100 is not reported.
- Spec: `Description` = `调整你自己的一条状态条（比如心情、精力，或你自己加的）。聊天里发生了真正影响你的事时调用：小事调一点，大事调多一些，最多到这一条的单次上限（默认 ±10；你的状态里写了“单次最多”的按那个数）；不要每句都调。外在状态可以在动作和外表描写里自然表现，其他的只影响语气；都不要向对方说出数字，也不要提状态条或工具。返回调整前后的大致档位。`; `delta` = `{"type":"integer","description":"变化量，不能为 0；绝对值不超过这一条的单次上限（默认 10），超过的按上限算","minimum":-100,"maximum":100}`.
- `NewAdjustBuff` doc: "one of the bars … cut to the bar's EffectiveMaxStep … net change on the local day within its EffectiveMaxDay … (both from the persona of the conversation being answered; a bar the bot added has the defaults 10 and 30) … a cut delta is reported with the limit; the answer never shows a value".
- `SKILL.md`: frontmatter `description: 自己的状态条和效果：调整状态条，自己加减状态条和效果` (tools and conversations unchanged). Body (keep every existing rule; the wording below is binding in substance):
  - 你有几条状态条，比如心情、精力。数值范围是 0 到 100，会随时间慢慢回到平常的水平。
  - 聊天里发生了真正影响你的事时，调用 adjust_buff 调整一条：小事调一点，大事调多一些，最多到这一条的单次上限（默认 ±10；你的状态里写了“单次最多”的，按那个数）。每条每天的变化也有上限。name 要和状态条的名字完全一致，reason 简单写原因。
  - 不要每句都调；同一条在一次回复里最多调一次。
  - (add_buff bar, add_buff effect, at most 6 each / remove_buff lines unchanged)
  - 直接列在你的状态里的状态条、你自建的状态条和效果只影响你的语气和想不想多聊，不要向对方提起。
  - 列在“外在状态”下面的是别人看得出来的状态，比如吃得很饱、脸红：可以在动作、神态和外表的描写里自然表现出来。
  - 不管哪一种，都不要说出数值，也不要提状态条、工具或调整过程。

- [ ] **Step 1: Write failing tests** (`buff_limits_test.go`; helpers `newStore`, `invocation`, `call`, `checkBuff`, `seedBuff`, `buffEvents`, `newAddBuff`, `newPolicies` from the package; a persona with `心情` default, `肚子 {Baseline 20, HalfLife 2h, Kind outer, MaxStep 40, MaxDay 100}`, `脸红 {Baseline 0, HalfLife 1h, MaxStep 20}`)
  - `TestAdjustBuff_PerBarLimits` (new task ID per step, `testNow`): 肚子 +35 → `肚子：偏低 → 中等`, stored 55, day 35; +60 → `肚子：中等 → 很高（超过单次上限 ±40，按 +40 调整）`, 95/75; +40 → `肚子：很高 → 很高（今天的变化快到上限 ±100，只调整了 +25）`, 100 (value clamped, not reported)/100; +1 → `肚子今天的变化已经到上限（每天最多 ±100），这次没有调整。`, no event added; −500 → `（超过单次上限 ±40，按 -40 调整）`, day 60. 脸红 +20, +20 → second is `（今天的变化快到上限 ±30，只调整了 +10）` (step 20, default day 30). 心情 +40 → `（超过单次上限 ±10，按 +10 调整）`; 心情 at day 25, +40 → daily suffix `只调整了 +5` (daily wins over step). Delta `0` → the bad-delta text. Every result ≤ `shortResultRunes` runes (also with a 6-rune name and `max_step`/`max_day` 100, delta −100).
  - `TestAdjustBuff_CustomBarKeepsDefaults`: `add_buff` 饥饿 then `adjust_buff` 饥饿 +40 → `（超过单次上限 ±10，按 +10 调整）`.
  - `TestAdjustBuff_PanelChatsUseTheirPersonasLimits`: a `PersonaSource` keyed by conversation (`outerPersonas map[string]conloomapi.PersonaDoc`), panel chats A (肚子 outer 40/100) and B (肚子 inner default), keys from `store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a"|"panel_b")`. A +40 → plain, scope day 40; B +5 → `肚子今天的变化已经到上限（每天最多 ±30），这次没有调整。`; B −15 → `按 -10 调整`, scope day 30; A +40 → plain, scope day 70; one stored row under `memory.ScopeKeyOf(a)`, none under A or B.
  - `TestAdjustBuff_SpecWidened`: delta schema `minimum` −100 / `maximum` 100; description contains `默认 ±10` and `外在`, lacks `隐藏` and `只有你自己知道`.
  - Update `buff_test.go`: `TestAdjustBuff_Caps` — `delta 15` → `心情：偏高 → 偏高（超过单次上限 ±10，按 +10 调整）`, `"-15"` → `…按 -10 调整）`, the +10 at day 26 → `心情：很高 → 很高（今天的变化快到上限 ±30，只调整了 +4）`, `capped`/精力 refusals → `…已经到上限（每天最多 ±30），这次没有调整。`; `TestAdjustBuff_BadArguments` — `badDelta` = the new text; `TestAdjustBuff_NoNumbersInResult` — refusal string updated, and the digit check runs on each result with `regexp.MustCompile("[±+-][0-9]+")` matches removed, so limits and applied deltas may show but values never do.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/tools/ -run 'AdjustBuff'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; rewrite `SKILL.md`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/... ./internal/app/ ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(skills): per-bar step and daily limits in adjust_buff`

### Task 3: Outer section and step limits in the state block, strip rule

**Files:**
- Modify: `internal/autonomy/state.go` (`StateBar` :32-36, `RenderState` :44-100, `renderState` :115-157, new const and `barLine`), `internal/contextbuilder/strip.go` (`stateBlockHeader` :49, `stateBlockLine` :50-55, `StripTranscriptPrefix` doc :90-103), `internal/core/autonomy_state.go` (`stateBlock` :22-75)
- Test: `internal/autonomy/outer_state_test.go`, `internal/contextbuilder/strip_outer_test.go`, `internal/core/outer_state_test.go` (all new)

**Interfaces:**
- Produces (`internal/autonomy/state.go`):
  ```go
  // StateBar is a bar with its current value. Outer bars are shown with their value under outerHeader; MaxStep > 0 adds
  // "单次最多 ±MaxStep" to the line.
  type StateBar struct {
      Name, Description string
      Value             float64
      Outer             bool
      MaxStep           int
  }
  const outerHeader = "【外在状态（别人看得出来，可以在动作和外表描写里自然带出；不说数字，不提状态条、工具或系统）】"
  // barLine renders one bar line without the trailing newline (formats in Global Constraints).
  func barLine(bar StateBar, withDescriptions bool) string
  func renderState(scene string, st *StateStatus, inner, outer []StateBar, effects []StateEffect, withDescriptions bool,
      now time.Time, loc *time.Location) string
  ```
  `RenderState`'s signature is unchanged; it splits `bars` into inner and outer, each in input order. Layout: header, scene, status, inner bars, effects, then the footer only when one of those was written, then (when any outer bar) `outerHeader` and the outer lines; no trailing newline. Outer value = `strconv.Itoa(int(math.Round(v)))`. Over 300 runes: drop all descriptions, then trailing effects, then trailing inner bars, then trailing outer bars only while the block would still be over 300 with the activity cut to nothing (the heading goes with the last outer bar), then shorten the activity by the remaining excess. The scene line is never dropped; limit text is part of the line and never trimmed separately. Without outer bars and with `MaxStep` 0 every output is today's byte for byte.
- Produces (`strip.go`): `stateBlockHeader = ^【(?:你的状态|外在状态)[^\n]*(?:\n|$)`; `stateBlockLine` adds the alternatives `【外在状态` and `[^\s【】\n:：]{1,6}[：:]\d{1,3}/100` (outer lines). Inner lines with `（单次最多 …）` already match.
- Produces (`internal/core/autonomy_state.go`):
  ```go
  // stateBar is b at value v as the state block shows it: Outer for an outer persona bar; MaxStep set for an outer bar
  // and for a bar whose EffectiveMaxStep is not conloomapi.DefaultBarMaxStep, else 0.
  func stateBar(b conloomapi.PersonaBar, v float64) autonomy.StateBar
  ```
  `stateBlock` builds its bars with `stateBar` (the persona is `doc`, the persona of the conversation being answered, so panel chats sharing a scope render by their own persona). Doc of `stateBlock`: outer bars with their value under the outer heading, step limits.

- [ ] **Step 1: Write failing tests**
  - `TestRenderState_OuterSection` (exact whole blocks, `wantHeader`/`wantFooter` from `state_test.go`): 心情 65 inner + 肚子 80 outer `MaxStep` 40 `高=很饱` → `wantHeader + "\n心情：偏高（高=开心放松，低=低落烦躁）\n" + wantFooter + "\n" + outerHeader + "\n肚子：80/100（很高，单次最多 ±40）（高=很饱）"`; outer before inner in the input still renders inner first; two outer bars keep their order.
  - `TestRenderState_OuterOnly`: only an outer bar → `wantHeader + "\n" + outerHeader + "\n肚子：…"` (no footer); with a status → status line, footer, outer section.
  - `TestRenderState_StepLimitShown`: inner `MaxStep` 20 → `心情：偏高（单次最多 ±20）（说明）`, and without descriptions `心情：偏高（单次最多 ±20）`; `MaxStep` 0 → today's line; outer 79.5 → `80/100（很高`; outer 0 → `0/100（很低`.
  - `TestRenderState_OuterBarsCap`, each case with the exact expected block written out (computed once by hand, its rune count asserted): (a) 4 outer bars (2-rune names, `MaxStep` 40, 40-rune descriptions), 2 inner custom bars, 3 effects, an 8-rune activity, in-person scene at `图书馆`: ≤ 300 runes, no description, no effect, all 4 outer lines exactly `名字：v/100（档位，单次最多 ±40）`, scene line and activity whole, an inner bar kept only if all outer bars are (by hand: header 24 + scene 41 + status 55 + footer 33 + heading 48 + four outer lines 91 = 292 runes before inner bars). (b) 4 outer bars with 6-rune names at 100 and `MaxStep` 100, a 16-rune place and a 16-rune activity: ≤ 300 runes; scene line whole; every kept outer line is the whole line; kept outer bars are a prefix of the input; `outerHeader` present iff an outer line is; by hand this keeps 3 outer bars and shortens the activity to 8 runes (337 runes in full, 321 with an empty activity).
  - Existing `internal/autonomy/state_test.go` stays unmodified and passes: it pins today's exact blocks, including the cap cases.
  - `contextbuilder` `TestStripTranscriptPrefix_OuterSection`: `RenderState(nil, status, [心情 inner, 肚子 outer 40], nil, …) + "\n好呀"` → `好呀`; outer-only block + `"\n好呀"` → `好呀`; `outerHeader line + "\n肚子：80/100（很高，单次最多 ±40）\n好呀"` → `好呀`; `"肚子：80/100\n好呀"` and `"心情：偏高（单次最多 ±20）\n好呀"` without a header → unchanged.
  - `core` `TestState_OuterBarsInjected` (`stateEnv(t, []string{"buff"}, …)` with `c.LegacyPersonaTemplates["default"]` bars 心情 default, 肚子 outer 40/100 baseline 20 `高=很饱`, 脸红 inner `MaxStep` 20; `seedBar` 肚子 80 now): the request starts with `autonomy.RenderState(nil, nil, [{心情 …60}, {脸红 …0 MaxStep 20}, {肚子 … 80 Outer MaxStep 40}], nil, …) + "\n\n【当前消息】"` and contains `肚子：80/100（很高，单次最多 ±40）（高=很饱）` and `脸红：很低（单次最多 ±20）`.
  - `TestState_ExplicitDefaultsUnchanged`: persona = default bars with `Kind: "inner", MaxStep: 10, MaxDay: 30` → block equals `RenderState(nil, nil, defaultStateBars(60, 70), …)`, no `单次最多`, no `【外在状态`.
  - `TestState_OuterBarsPerChatPersona` (`scopeAutonomyEnv`, `createChat`, `AppendPersonaVersion`, `panelReply`; A: 肚子 outer default limits, B: 肚子 inner): scope 肚子 seeded 80; A's request contains `【外在状态` and `肚子：80/100（很高，单次最多 ±10）`; B's contains `肚子：很高` and lacks `【外在状态` and `80/100`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/autonomy/ ./internal/contextbuilder/ ./internal/core/ -run 'Outer|StepLimit|ExplicitDefaults'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; update the docs of `StateBar`, `RenderState`, `renderState`, `stateBlock`, `stateBlockHeader`/`stateBlockLine`, `StripTranscriptPrefix`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/autonomy/ ./internal/contextbuilder/ ./internal/core/` → PASS, with `internal/autonomy/state_test.go` unmodified.
- [ ] **Step 5: Commit** `feat(core): outer bars with values and step limits in the state block`

### Task 4: `AutonomyService.State` reports kind and limits

**Files:**
- Modify: `internal/core/autonomy_service.go` (`State` :50-130, the `AutonomyBar` literal :94-97 and the doc)
- Test: `internal/core/autonomy_outer_test.go` (new)

**Interfaces:**
- Consumes: T1 `AutonomyBar.Kind/MaxStep/MaxDay`.
- Produces: `State` sets `Kind: b.Kind, MaxStep: b.MaxStep, MaxDay: b.MaxDay` from the bar `scopeBars(doc, custom)` returns (`doc` from `barDefs`, the viewed conversation's persona, or the template it would use). Values unchanged; `SetBar`, `RemoveBar` unchanged (non-goal). Doc of `State`: bars carry their persona's kind and limits as written.

- [ ] **Step 1: Write failing tests**
  - `TestAutonomyService_BarKindAndLimits`: persona bars 心情 default, 肚子 outer 40/100, 脸红 `MaxStep` 20, plus a custom bar 饥饿 (`seedCustomBar`) → `State.Bars` = `[{心情 … Kind "" 0 0}, {肚子 … Kind "outer" 40 100}, {脸红 … MaxStep 20}, {饥饿 … Custom true, zero}]`; `json.Marshal` of the 心情 entry lacks `"kind"`, `"max_step"`, `"max_day"`.
  - `TestAutonomyService_BarKindPerChat` (`scopeAutonomyEnv`, two panel chats with personas A 肚子 outer 40/100, B 肚子 inner): `State(a)` 肚子 has `Kind "outer"`, 40, 100; `State(b)` has zeros; both report the same `Value`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'BarKind'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ ./internal/panel/ ./internal/gateway/` → PASS.
- [ ] **Step 5: Commit** `feat(core): report bar kind and limits in the autonomy view`

### Task 5: Panel — persona form editor, profile card and autonomy tab tags

**Files:**
- Modify: `internal/panel/web/views/personaform.js` (constants :22-24, `barsEditor` :59-110, bars hint :603-605), `internal/panel/web/lib/format.js`, `internal/panel/web/views/profile.js` (`stateBody` :74-94), `internal/panel/web/views/autonomy.js` (`barsSection` :138-172), `internal/panel/web/style.css`
- Test: `internal/panel/web/outer_bars_web_test.go` (new, package `web`, `assets` helper)

**Interfaces:**
- Consumes (JSON fixed by T1): persona bar `{name, baseline, half_life, description?, kind?, max_step?, max_day?}`; autonomy bar `{…, kind?, max_step?, max_day?}`.
- Produces (`lib/format.js`):
  ```js
  // BAR_DEFAULT_MAX_STEP, BAR_DEFAULT_MAX_DAY and BAR_MAX_LIMIT are conloomapi.DefaultBarMaxStep, DefaultBarMaxDay and MaxBarLimit.
  export const BAR_DEFAULT_MAX_STEP = 10;
  export const BAR_DEFAULT_MAX_DAY = 30;
  export const BAR_MAX_LIMIT = 100;
  // barLimit reads a bar limit input: undefined when blank (the default), the integer 1–BAR_MAX_LIMIT, else null.
  export function barLimit(text)          // trims; must match /^\d+$/
  // barLimitsText words a bar's limits when it sets either: `单次最多 ±${step}，每天最多 ±${day}`, else ''.
  export function barLimitsText(b)
  ```
- Produces (`personaform.js`):
  - `const BAR_KIND = { inner: '内在', outer: '外在' };` (keys are `conloomapi.BarKindInner/BarKindOuter`) and `const BAR_KIND_HINT = { inner: '只影响 bot 的语气和想不想多聊，bot 不会提起', outer: '别人看得出来，比如吃饱、脸红；bot 会在动作和外表描写里自然带出，不说数字' };`
  - each bar row adds `kind = select(BAR_KIND, bar.kind === 'outer' ? 'outer' : 'inner')` in a `类型` field whose hint span shows `BAR_KIND_HINT[kind.value]` and follows `change`; `maxStep`/`maxDay` = `h('input', { type: 'number', min: 1, max: BAR_MAX_LIMIT, step: 1, value: bar.max_step ?? '', placeholder: String(BAR_DEFAULT_MAX_STEP) })` (same for day) in fields `单次最多` (hint `1–100 的整数，一次最多调多少；留空为 10`) and `每天最多` (hint `1–100 的整数，一天里净变化最多多少，不能小于单次最多；留空为 30`).
  - `read()`: `barLimit(r.maxStep.value)` / `barLimit(r.maxDay.value)`; `null` → `第 ${i + 1} 条状态条的单次最多必须是 1–100 的整数，或留空` / `…每天最多…`; `(day ?? BAR_DEFAULT_MAX_DAY) < (step ?? BAR_DEFAULT_MAX_STEP)` → `第 ${i + 1} 条状态条的每天最多不能小于单次最多（${step ?? BAR_DEFAULT_MAX_STEP}）`. The output starts from `{ ...r.bar, name, baseline, half_life, description }`, then `delete out.kind; delete out.max_step; delete out.max_day;` and sets `out.kind = 'outer'` only for outer, `max_step`/`max_day` only when not blank — so an untouched inner bar saves exactly as before.
  - bars hint: `私聊里 bot 自己的数值（0–100），不进系统提示；bot 启用了 buff 技能才生效。内在的只影响语气和想不想多聊，bot 不会提起；外在的是别人看得出来的状态，bot 会在动作和外表描写里自然带出，但不说数字。一条都不写时用默认的心情和精力。`; `barsEditor` doc mentions kind and limits.
- Produces (`profile.js`): the item label becomes `[b.name, b.kind === 'outer' ? h('span', { class: 'badge tg-profile-tag' }, '外在') : null]`; values shown as today. `style.css`: `.chat-app .tg-profile-tag { margin-left: 6px; vertical-align: middle; }` (chat-app scoped per `TestChatPages_StylesScoped`).
- Produces (`autonomy.js`): next to the name `b.kind === 'outer' ? [' ', h('span', { class: 'badge' }, '外在')] : null` beside the 自建 badge (a custom bar is never outer); under the description `barLimitsText(b) ? h('div', { class: 'muted small' }, barLimitsText(b)) : null`; first hint gains `标“外在”的是别人看得出来的状态，bot 会在动作和外表描写里带出来。`. All text through `h()`.

- [ ] **Step 1: Write failing tests** (`outer_bars_web_test.go`)
  - `TestPersonaForm_BarKindAndLimits`: `BAR_KIND` keys (regexp as in `TestAssets_GenderOptionsMatchPersona`) = `[conloomapi.BarKindInner, conloomapi.BarKindOuter]`; `personaform.js` contains `'类型'`, `'单次最多'`, `'每天最多'`, `BAR_KIND_HINT[`, `barLimit(r.maxStep.value)`, `barLimit(r.maxDay.value)`, `delete out.kind`, `delete out.max_step`, `delete out.max_day`, `out.kind = 'outer'`, the three error templates; the bars hint contains `外在` and no longer `只有 bot 自己知道`.
  - `TestFormat_BarLimitsMatchPersona`: `format.js` `BAR_DEFAULT_MAX_STEP`/`BAR_DEFAULT_MAX_DAY`/`BAR_MAX_LIMIT` equal `conloomapi.DefaultBarMaxStep`/`DefaultBarMaxDay`/`MaxBarLimit`; it exports `barLimit` and `barLimitsText`.
  - `TestProfile_OuterBarTag`: `profile.js` contains `b.kind === 'outer'`, `'外在'`, `tg-profile-tag`; `style.css` matches `\.chat-app \.tg-profile-tag\s*\{`.
  - `TestAutonomyTab_OuterBarTag`: `autonomy.js` contains `b.kind === 'outer'`, `'外在'`, `barLimitsText(b)`, `标“外在”的是别人看得出来的状态`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/web/ -run 'BarKindAndLimits|BarLimitsMatch|OuterBarTag'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/...` → PASS; `/usr/local/node/bin/node --check` on the four JS files → no output; self-check (not committed): `/usr/local/node/bin/node --input-type=module -e "import {barLimit, barLimitsText} from './internal/panel/web/lib/format.js'; console.log(JSON.stringify(['', ' 40 ', '0', '101', '2.5', '1e1', '100'].map(barLimit)), barLimitsText({}), barLimitsText({max_step: 40}), barLimitsText({max_step: 40, max_day: 100}))"` → `[null,40,null,null,null,null,100]` (JSON shows `undefined` as `null` — also print `barLimit('') === undefined` → `true`), then ``, `单次最多 ±40，每天最多 ±30`, `单次最多 ±40，每天最多 ±100`; report the output.
- [ ] **Step 5: Commit** `feat(panel): edit and tag outer bars and their step limits`

### Task 6: Docs and full check

**Files:**
- Modify: `docs/configuration.md` (状态条 table :188-206), `docs/skills.md` (built-in table :43, `### adjust_buff` :155-170), `docs/architecture.md` (state block :466/:484, strip rule :511), `docs/panel.md` (persona form :115, profile card :93, autonomy tab :125, import :226), `docs/api.md` (autonomy example :150-174 and `bars` bullet :176, `PUT …/autonomy/buffs` limits note :191); `docs/usage.md` only if `grep -n "状态条" docs/usage.md` finds wording that says all bars are hidden.

**Content:**
- `configuration.md`: rows `kind` (`inner`/`outer`, 省略为 `inner`), `max_step` (1–100，省略为 10), `max_day` (1–100，省略为 30，不能小于单次上限，包括默认值：只写 `max_step: 40` 不写 `max_day` 会被拒绝); what outer means (状态块里带数值、在“外在状态”下面，可在动作和外表描写里表现，不说数字；内在的同以前); custom bars are always inner with defaults; example adds `{"name":"肚子","baseline":20,"half_life":"2h","description":"高=很饱","kind":"outer","max_step":40,"max_day":100}`; bars still never enter the system prompt.
- `skills.md`: `buff` row wording (no "只有自己知道" absolutism); `delta` per-bar limit (default ±10, from the persona of the conversation being answered; panel chats sharing a scope share value and daily counter but clamp by their own persona); daily limit per bar; the three result texts (step cut, daily partial cut, refusal naming the limit) and the new bad-delta text; results still never show a value.
- `architecture.md`: state block layout (inner lines unchanged, `（单次最多 ±N）` for a non-default step, footer, outer heading and `名字：80/100（档位，单次最多 ±N）`), trim order with outer bars, byte-identity for personas without outer bars or custom limits; strip rule also covers the 【外在状态 header and `名字：数字/100` lines.
- `panel.md`: form fields 类型/单次最多/每天最多 (blank = default, 0 refused); `外在` tag on profile card and autonomy tab, limits line in the tab; exchange: new fields are omitted when unset so such files import on older builds; files that use them are refused there as unknown fields (“不能有多余的字段”); no version change.
- `api.md`: `bars[]` gains `kind`, `max_step`, `max_day` (omitted when unset; from the viewed conversation's persona; custom bars never have them); the `PUT …/autonomy/buffs` note says it ignores the bar's step and daily limits (not "±10/±30").

- [ ] **Step 1: Edit docs**; `grep -rn "±10\|±30\|只有自己知道\|隐藏状态条" docs/*.md` and fix every hit that now misstates the behaviour (leave `docs/superpowers/` history alone).
- [ ] **Step 2: Full check:** `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS; `for f in $(git diff --name-only main -- '*.js'); do /usr/local/node/bin/node --check "$f"; done` → no output.
- [ ] **Step 3: Commit** `docs: outer bars and per-bar step limits`

---

## Self-review

- **Spec coverage:** `kind`/`max_step`/`max_day`, ranges, `max_day ≥ effective max_step`, JSON/YAML, exchange without a version bump → T1; per-bar clamp with defaults 10/30, schema ±100, cut reporting and the refusal naming the daily limit, custom bars inner with defaults, SKILL.md and tool text reworded generically → T2; outer section with value/band/limit, limit for non-default inner steps, header/footer kept for inner only and absent when nothing inner precedes it, 300-rune cap with outer values dropped only after descriptions, effects and inner bars, strip rule → T3; `AutonomyBar` fields from the viewed persona → T4 (types T1); persona form editor, profile card and autonomy tab tags → T5; docs → T6. Spec "Tests for": validation (T1), state rendering inner byte-identical and outer (T3), adjust_buff limits, texts and daily cap per bar (T2), form source tests (T5), exchange round trip (T1).
- **Names consistent across tasks:** `conloomapi.BarKindInner/BarKindOuter/DefaultBarMaxStep/DefaultBarMaxDay/MaxBarLimit`, `PersonaBar.Kind/MaxStep/MaxDay`, `Outer()`, `EffectiveMaxStep()`, `EffectiveMaxDay()`, `AutonomyBar.Kind/MaxStep/MaxDay` (T1) used by T2–T5; `autonomy.StateBar.Outer/MaxStep`, `outerHeader`, `core.stateBar` (T3); JSON `kind`, `max_step`, `max_day` (T1) used by T5.

## Decisions on spec ambiguities

1. Placement: the outer section comes after the footer, so "只有你自己知道" / "这些只影响你的语气" keep describing what precedes them; the footer is left out when nothing inner precedes it (only outer bars), which cannot happen for personas without outer bars, so their blocks stay byte-identical.
2. Trim order: descriptions → trailing effects → trailing inner bars → trailing outer bars only while even an empty activity would not fit → shorten the activity. For blocks without outer bars this is today's order.
3. Outer bars always show their step limit (default included); inner bars show it only when the effective step is not 10, so an explicit `max_step: 10` renders like today. A non-default `max_day` alone is not shown; the model learns it from the cut/refusal texts.
4. `adjust_buff` results keep bands only (also for outer bars); the cut texts show limits and the applied delta, never a value. `TestAdjustBuff_NoNumbersInResult` is narrowed to "no values".
5. When both the step and the daily limit cut a delta, the daily message (with the finally applied delta) is reported.
6. `AutonomyBar` copies the persona fields as written (empty/0 = default), not effective values, so default bars' JSON is unchanged; the panel derives defaults via `barLimitsText`.
7. Panel form: blank limit = field omitted; `0` (and anything not an integer 1–100) is a client error, though the API accepts 0 as "default". Saving an inner bar writes no `kind`, so untouched personas save byte-identically.
8. The strip header also accepts `【外在状态`, so an echo of only the outer section is cut too.
