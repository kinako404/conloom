# Chat Scene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A private chat has a persistent scene, online or in person (with an optional place), that the bot switches itself with a new `set_scene` tool (built-in skill `scene`, opt-in per bot). The scene is the first line of the hidden state block, shapes the wake directives, falls back to online after 3 hours of the person's silence, is restored to in person when the bot comes back from an errand, and is visible and editable in the panel and the API.

**Architecture:** Migration `0022` adds `conversation_scene`, keyed by the memory scope like `conversation_status`; the store gets `GetScene`/`SetScene`/`TouchScene`/`EndIdleScene` and the table joins every cleanup and merge list. `internal/skills/tools/scene.go` is modelled on `status.go`. A new `internal/core/scene.go` holds the effective-scene rule (`sceneIdleTimeout`), reads the scene once per private task (`readScene`: write back the fallback, then touch for a person's message) and hands the result to both `wakeDirective` and `stateBlock`; `autonomy.RenderState` renders it as the block's first line and never drops it. The status-return wake restores the in-person scene before building its directive; `copy.go` directives get in-person variants. `AutonomyService` gains `Scene`/`SetScene`, exposed by a panel route, an API route and a `pkg/client` method. The panel autonomy tab gets a 场景 section; `#/im` shows `面对面 · <place>` under the name and mutes `（…）` segments in bubbles.

**Tech Stack:** Go (go.mod, module `github.com/kinako404/conloom`), SQLite through `internal/store/sqlite`, vanilla JS + CSS in `internal/panel/web` (no build step, no dependencies). No new Go or JS dependencies.

**Spec:** `docs/superpowers/specs/2026-11-18-chat-scene-design.md` — read it before your task.

## Global Constraints

- Public repo: no real IDs, tokens, hosts, nicknames or character names in code, tests, comments, docs or commit messages. Fake data only: bots `bot_test`/`qq-main`, users `20001`/`29999`, names 小明/小红, places 食堂/图书馆, `example.invalid`.
- Commit messages: English, conventional style, exactly as given per task.
- Before every commit: `gofmt -l .` prints nothing and `go vet ./...` passes; tests run with `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 <pkgs>`; every changed JS file passes `/usr/local/node/bin/node --check <file>`. The last task runs the full `./...`.
- User-facing copy is Chinese. The following strings are fixed by the spec and used verbatim:
  - state line online: `场景：线上（你们隔着聊天软件发消息，不在一起）`
  - state line in person: `场景：面对面，在<place>（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）`, and without `，在<place>` when the place is empty: `场景：面对面（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）`
  - panel fallback note: `超过 3 小时没说话，已回到线上`
  - `#/im` header label: `面对面 · <place>` or `面对面`
  - audit kind `autonomy.scene.set`; API route `PUT /api/v1/conversations/{id}/autonomy/scene`; panel route `PUT /panel/api/conversations/{key}/autonomy/scene`
  - constants `sceneIdleTimeout = 3 * time.Hour` (fallback when `now - touched_at >= 3h`), `sceneReturnSlack = 2 * time.Minute`, place at most 16 runes (`conloomapi.ScenePlaceMaxRunes = 16`), migration `0022_scene.sql`, mode values `online` / `in_person`.
- Wake turns (status return, initiate, follow-up) must NOT offer `set_scene`: they keep offering only `wakeTools` (`adjust_buff`, `add_buff`, `remove_buff`). Only the person's messages refresh `touched_at`; the bot's replies are refreshed through those same tasks, wake tasks never touch.
- Groups are always online: nothing in this plan runs for `conloomapi.ConvGroup`; all group tests stay unchanged.
- Follow existing patterns: `set_status` (tool), `0016` origin column, `AutonomyService` + `panel/autonomy.go` + `gateway/autonomy.go`, `web_test.go`-style source tests. Prefer new test files so parallel tasks do not edit the same file.
- Update every comment and doc that describes changed behaviour (`stateBlock`, `RenderState`, `StripTranscriptPrefix`, `newSkillRun`, `wakeDirective`, `CopyDirective*`, `tools` package doc, `shortResultRunes`).

## Review Focus

1. **Odd place input** — `【食堂】`, `食\n堂`, zero-width characters, 17 runes, whitespace only, a place the reviewer blocks, a place given with `online`. Owners: T2 `TestSetScene_PlaceCleanedAndBounded`, T5 `TestAutonomyService_SetScenePlace`, T3 `TestRenderState_ScenePlaceFlattened`.
2. **Group conversations** — `set_scene` in a group answers `只能在私聊里用。` and stores nothing; panel/API scene writes on a group are 400; `GET …/chat` of a group has `"scene": null`. Owners: T2 `TestSetScene_GroupRefused`, T5 `TestPanelAutonomy_Scene` / `TestGatewayAutonomy_Scene`, T7 `TestGetChat_Scene`.
3. **Fallback boundary and order** — touched exactly 3h ago falls back, 3h−1s does not; a message after a day apart sees the online line and does not touch the in-person scene back to life. Owners: T3 `TestEffectiveScene_IdleBoundary`, `TestState_SceneFallbackBeforeTouch`.
4. **Status return edge cases** — the bot never switched to online (stays in person, nothing rewritten, in-person directive); switch exactly 2 minutes before the status started (restored) vs 2m1s (not); switched online long before the errand (not restored); skill off (nothing touched). Owner: T4 `TestSceneReturn_*`.
5. **Empty place and escaping in the panel** — panel PUT `in_person` with `place: ""` is accepted and shows `面对面` (state line without `，在`); a bubble `好（<b>笑</b>）` renders `<b>` as text inside a `tg-action` span. Owners: T5 `TestAutonomyService_SetSceneEmptyPlace`, T6 `TestAutonomyTab_SceneSection`, T7 `TestChatPane_ActionBrackets` + node self-check.

## Task order and parallel groups

| Stage | Tasks (parallel within a stage) | Starts after |
|---|---|---|
| A | T1 store + shared API types | — |
| B | T2 tool + skill, T3 state line + effective scene, T6 autonomy tab | T1 merged |
| C | T4 status return + directives, T5 service + panel/API/client | T2, T3 merged (T5 needs `core/scene.go` from T3 and nothing from T4) |
| D | T7 `#/im` header + bubbles | T5, T6 merged |
| E | T8 docs | all merged |

Files per task (no two tasks of one stage share a file):

- T1: `internal/store/sqlite/migrations/0022_scene.sql`, `internal/store/{records.go,store.go}`, `internal/store/sqlite/{scene.go,autonomy.go,delete.go,scope_migrate.go}`, `pkg/conloomapi/autonomy.go`; tests `internal/store/sqlite/{scene_test.go,delete_test.go}`, `pkg/conloomapi/autonomy_test.go`.
- T2: `internal/skills/tools/{scene.go,tools.go}`, `internal/skills/builtin/scene/SKILL.md`, `internal/app/skills.go`; tests `internal/skills/tools/scene_test.go`, `internal/skills/registry_test.go`, `internal/app/skills_test.go`, `internal/skills/tools/limits_test.go`.
- T3: `internal/autonomy/state.go`, `internal/contextbuilder/strip.go`, `internal/core/{scene.go,autonomy_state.go,runner.go}`; tests `internal/autonomy/state_test.go`, `internal/contextbuilder/{strip_test.go,strip_hint_test.go}`, `internal/core/{scene_test.go,autonomy_state_test.go,scope_autonomy_test.go,wake_test.go}` (call sites and the fake tool list only).
- T4: `internal/core/{copy.go,wake.go,runner.go}`; tests `internal/core/scene_wake_test.go` (new), `internal/core/wake_test.go` (`TestWake_Copy` call sites).
- T5: `internal/core/autonomy_service.go`, `pkg/conloomapi/autonomy.go` (`Scene` field), `internal/panel/{autonomy.go,write_routes.go,respond.go}`, `internal/gateway/{autonomy.go,server.go,errors.go}`, `pkg/client/client.go`; tests `internal/core/autonomy_scene_test.go` (new), `internal/panel/autonomy_scene_test.go` (new), `internal/gateway/autonomy_scene_test.go` (new), `pkg/client/client_test.go`.
- T6: `internal/panel/web/views/autonomy.js`, `internal/panel/web/lib/format.js`; test `internal/panel/web/scene_web_test.go` (new).
- T7: `internal/panel/chat.go`, `internal/panel/web/views/chat.js`, `internal/panel/web/lib/format.js`, `internal/panel/web/style.css`; tests `internal/panel/chat_scene_test.go` (new), `internal/panel/web/scene_web_test.go`.
- T8: `docs/skills.md`, `docs/architecture.md`, `docs/panel.md`, `docs/api.md`.

---

### Task 1: Store — migration 0022, scene rows, cleanup lists, shared API types

**Files:**
- Create: `internal/store/sqlite/migrations/0022_scene.sql`, `internal/store/sqlite/scene.go`
- Modify: `internal/store/records.go` (next to `ConversationStatus` ~:418), `internal/store/store.go` (interface, after `ClearStatus` ~:365), `internal/store/sqlite/autonomy.go` (`autonomyTables` :17), `internal/store/sqlite/delete.go` (`conversationTables` :18, the shared-scope status delete :59-64, `scopeTables` :129), `internal/store/sqlite/scope_migrate.go` (`scopeMergeTables` :17, `mergeIntoScope` ~:129), `pkg/conloomapi/autonomy.go`
- Test: `internal/store/sqlite/scene_test.go` (new), `internal/store/sqlite/delete_test.go` (`keyedTables` :22, `seedConversation`), `pkg/conloomapi/autonomy_test.go` (new)

**Interfaces:**
- Produces SQL (header comment in the style of 0016):
  ```sql
  CREATE TABLE conversation_scene (
      conversation_key        TEXT PRIMARY KEY,           -- memory scope
      mode                    TEXT NOT NULL,              -- online | in_person
      place                   TEXT NOT NULL DEFAULT '',
      since_at                TEXT NOT NULL,
      touched_at              TEXT NOT NULL,
      prev_mode               TEXT NOT NULL DEFAULT '',
      prev_place              TEXT NOT NULL DEFAULT '',
      updated_at              TEXT NOT NULL,
      origin_conversation_key TEXT NOT NULL DEFAULT ''
  );
  ```
- Produces (`pkg/conloomapi/autonomy.go`):
  ```go
  const (SceneOnline = "online"; SceneInPerson = "in_person"; ScenePlaceMaxRunes = 16)
  // AutonomyScene is a private chat's scene as in effect now. Enabled: the bot enables the scene skill.
  // IdleOnline: online because the person said nothing for 3 hours while in person.
  type AutonomyScene struct {
      Enabled    bool      `json:"enabled"`
      Mode       string    `json:"mode"`
      Place      string    `json:"place"`
      SinceAt    time.Time `json:"since_at,omitzero"`
      IdleOnline bool      `json:"idle_online"`
  }
  type AutonomyScenePut struct { Mode string `json:"mode"`; Place string `json:"place"` }
  ```
  (`AutonomyState.Scene` is added in T5, not here.)
- Produces (`internal/store/records.go`):
  ```go
  type ConversationScene struct {
      ConversationKey, OriginConversationKey string
      Mode, Place                            string
      SinceAt, TouchedAt                     time.Time
      PrevMode, PrevPlace                    string
      UpdatedAt                              time.Time
  }
  func (s ConversationScene) Origin() string // as ConversationStatus.Origin
  ```
- Produces (`store.Store`, implemented in `sqlite/scene.go`, all writes in `db.inTx`, times with `fmtTime`):
  ```go
  GetScene(ctx context.Context, convKey string) (ConversationScene, error) // ErrNotFound without a row (= online)
  // SetScene stores s as the scene of scope s.ConversationKey, set in s.Origin(). When the stored scene (online and ""
  // without a row) has s's Mode and Place it only stores s.TouchedAt (and UpdatedAt, origin) — no row is created for an
  // online no-op — and reports false; otherwise PrevMode/PrevPlace become the replaced scene's, SinceAt/TouchedAt s's,
  // and it reports true. Zero SinceAt/TouchedAt/UpdatedAt are now.
  SetScene(ctx context.Context, s ConversationScene) (changed bool, err error)
  // TouchScene sets TouchedAt to at when the scope's scene is in_person and touched before at.
  TouchScene(ctx context.Context, convKey string, at time.Time) (bool, error)
  // EndIdleScene switches the scope's scene to online since `since` (PrevMode/PrevPlace = the in-person scene,
  // TouchedAt kept) only if it is still in_person with TouchedAt equal to touchedAt.
  EndIdleScene(ctx context.Context, convKey string, touchedAt, since time.Time) (bool, error)
  ```
- Cleanup: `"conversation_scene"` joins `autonomyTables` (so `ForgetPrivate`/`ForgetPrivateScope` delete it), `conversationTables`, `scopeTables`; `DeleteConversation` of a panel chat also deletes the shared scope's scene row whose `origin_conversation_key` is that chat (same statement shape as the status at delete.go:60); `scopeMergeTables` + `keepNewest(ctx, tx, "conversation_scene", "", "updated_at DESC", keys, scope, <origin CASE clause as for status>)`.

- [ ] **Step 1: Write failing tests** (`scene_test.go`, fake keys built with `store.ConversationKey(testBot, "qq", private, "20001")`)
  - `TestScene_GetMissingIsNotFound`: `errors.Is(err, store.ErrNotFound)`.
  - `TestScene_SetRecordsPrevAndNoop`: no row → `SetScene(online)` returns false and `GetScene` still `ErrNotFound`; `SetScene(in_person, 食堂, since=t1)` → true, row `{in_person, 食堂, since t1, touched t1, prev "online"/""}`; same mode+place at t2 → false, `SinceAt` still t1, `TouchedAt` t2, prev unchanged; `SetScene(online, since t3)` → true, prev `in_person`/`食堂`, place `""`; `OriginConversationKey` round-trips.
  - `TestScene_TouchOnlyInPerson`: online row → `TouchScene` false, unchanged; in-person row → true, `TouchedAt` = at; an older `at` → false.
  - `TestScene_EndIdleOnlyIfUntouched`: in person touched t1 → `EndIdleScene(t0)` false; `EndIdleScene(t1, t1+3h)` true → `{online, "", since t1+3h, touched t1, prev in_person/食堂}`; again → false.
  - `TestForgetPrivate_ClearsScene`: scene rows of the chat and of a panel scope (via `ForgetPrivateScope`) are gone, another chat's row stays.
  - `TestDeleteConversation_DeletesSceneSetInTheChat`: panel chat A sets the scope scene, `DeleteConversation(A)` removes it; a scene set in chat B survives deleting A.
  - `TestMigrateScope_KeepsNewestScene`: two panel chats with legacy rows under their own keys → one row under the scope, the newer `updated_at`, origin = its chat.
  - `TestMigration0022_AddsSceneTable`: `open(ctx, path, migrationsUpTo(t, 21))` then full migrate keeps existing rows and creates the table (pattern of `TestMigration0021_AddsTableAndFollowsColumn`).
  - `delete_test.go`: add `"conversation_scene"` to `keyedTables` and a seeded row in `seedConversation`, so `TestDeleteConversation_CoversEveryKeyedTable` covers it.
  - `pkg/conloomapi`: `TestAutonomyScene_JSON`: zero `SinceAt` marshals as `{"enabled":true,"mode":"online","place":"","idle_online":false}`; `AutonomyScenePut` decodes `{"mode":"in_person","place":"食堂"}`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/... ./pkg/conloomapi/ -run 'Scene|ForgetPrivate|DeleteConversation|MigrateScope|0022'` → FAIL (undefined / missing table).
- [ ] **Step 3: Implement** per Interfaces. Fakes embed `store.Store`, so none need new methods; check with `go vet ./...`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/... ./pkg/conloomapi/ ./internal/core/ ./internal/memory/` → PASS.
- [ ] **Step 5: Commit** `git commit -am "feat(store): conversation scene table and store methods"` (add the new files first).

### Task 2: `set_scene` tool, built-in `scene` skill, registration

**Files:**
- Create: `internal/skills/tools/scene.go`, `internal/skills/builtin/scene/SKILL.md`
- Modify: `internal/skills/tools/tools.go` (package doc list, `SceneStore`, `shortResultRunes` comment), `internal/app/skills.go` (`builtinTools`)
- Test: `internal/skills/tools/scene_test.go` (new), `internal/skills/registry_test.go` (`TestLoad_Builtins` :49-55), `internal/app/skills_test.go` (`TestBuiltinTools` :60), `internal/skills/tools/limits_test.go` (`TestTools_DeclareResultLimits`: add `{NewSetScene(nil, nil, nil, nil), shortResultRunes}`)

**Interfaces:**
- Consumes: T1 `store.ConversationScene`, `SetScene`, `conloomapi.SceneOnline/SceneInPerson/ScenePlaceMaxRunes`; existing `CleanActivity`, `privateOnly`, `clock`, `stringArg`, `Reviewer`, `PolicySource`, `PersonaSource`.
- Produces:
  ```go
  // SceneStore is the part of store.Store that set_scene uses.
  type SceneStore interface {
      SetScene(ctx context.Context, s store.ConversationScene) (bool, error)
  }
  func NewSetScene(st SceneStore, pol PolicySource, personas PersonaSource, rv Reviewer) skills.Tool
  func (*setSceneTool) MaxResultRunes() int // shortResultRunes
  ```
  Spec: name `set_scene`; parameters `{"mode":{"type":"string","enum":["online","in_person"]},"place":{"type":"string","maxLength":16}}`, `required: ["mode"]`; description (Chinese) says online = 隔着聊天软件发消息, in_person = 面对面在一起, switch only when the chat clearly says you met or parted, private chats only.
- Call: group → `privateOnly`; mode not one of the two → `mode 只能是 online（线上）或 in_person（面对面）。`; `place = CleanActivity(place)`, dropped for online; over 16 runes → `地点（place）超过 16 字，写短一点。`; non-empty place reviewed exactly as `set_status` (`Output` in `ContentOpen`, else `OutputProtected`), blocked or cleaned to empty → `这个地点不能用。`, nothing stored; `SetScene{ConversationKey: memory.ScopeKeyOf(inv.ConversationKey), OriginConversationKey: inv.ConversationKey, Mode, Place, SinceAt: now, TouchedAt: now}`; result `已记下：面对面，在食堂。` / `已记下：面对面。` / `已记下：线上。`, unchanged → `场景没有变化。`.
- `SKILL.md` frontmatter: `name: scene`, `description: 记下你们现在是隔着聊天软件说话，还是面对面在一起`, `tools: [set_scene]`, `conversations: [private]`. Body (polish wording, keep every rule): switch to `in_person` only when the chat clearly says you met / arrived / sit together (place ≤16 字, optional); `online` when one of you clearly leaves; never on hints, plans or hypotheticals, not every turn; leaving for something that takes a while → `set_scene online` together with `set_status`, coming back to them → `set_scene in_person`; in person at most one short action or expression in full-width brackets per message and not in every message, online no actions — only what you would type; never mention the scene, tools or system, never announce "我们现在面对面了".
- `builtinTools`: `tools.NewSetScene(d.Store, d.Policies, d.Personas, d.Reviewer)` right after `NewSetStatus`.

- [ ] **Step 1: Write failing tests** (`scene_test.go`, helpers from `status_test.go`/`helpers_test.go`: `newStore`, `newPolicies`, `invocation(privateKey, "20001", "小明")`, `call`, `checkSpec`, `fakePersonas`)
  - `TestSetScene_SwitchesAndRecordsPrev`: `checkSpec(t, spec, "set_scene", "mode", "place")`; `{"mode":"in_person","place":"食堂"}` → `已记下：面对面，在食堂。`, row in the scope with origin `privateKey`, since = touched = `testNow`; `{"mode":"online","place":"食堂"}` → `已记下：线上。`, place `""`, prev `in_person`/`食堂`; `{"mode":"in_person"}` → `已记下：面对面。`.
  - `TestSetScene_SameSceneIsNoop`: second identical call → `场景没有变化。`, `SinceAt` unchanged, `TouchedAt` = second call's `inv.Now`.
  - `TestSetScene_BadMode`: `"offline"`, `""`, `1`, missing → the mode message, nothing stored.
  - `TestSetScene_PlaceCleanedAndBounded`: `"【食堂】"` → `食堂`; `"食\n堂"` → `食 堂`; `"食\u200b堂"` → `食堂`; 16 runes ok; 17 runes → the over-long message, nothing stored; `"   "` → stored in person without place; a place the protected reviewer blocks (pattern of `TestSetStatus_ActivityCleanedAndReviewed`) → `这个地点不能用。`, nothing stored.
  - `TestSetScene_GroupRefused`: group invocation → `只能在私聊里用。`, `GetScene` of the group key → `ErrNotFound`.
  - `TestSetScene_PanelChatsShareTheScope`: two panel chats of one bot (pattern of `status_scope_test.go`) → one row under `store.PanelScopeKey`, origin = the calling chat.
  - `TestLoad_Builtins`: names `buff, memory, scene, status, time`; `{"scene", []string{"set_scene"}}` private-only with a description.
  - `TestBuiltinTools`: `[remember recall get_affinity now set_status set_scene adjust_buff add_buff remove_buff]`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/... ./internal/app/ -run 'SetScene|Builtin'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/... ./internal/app/` → PASS.
- [ ] **Step 5: Commit** `feat(skills): set_scene tool and built-in scene skill`

### Task 3: Effective scene, scene line in the state block, fallback write-back and touch

**Files:**
- Create: `internal/core/scene.go`, `internal/core/scene_test.go`
- Modify: `internal/autonomy/state.go`, `internal/contextbuilder/strip.go` (`stateBlockLine` :55 and doc comments), `internal/core/autonomy_state.go` (`stateBlock`), `internal/core/runner.go` (~:312-356)
- Test: `internal/autonomy/state_test.go`, `internal/contextbuilder/strip_test.go`, `internal/core/autonomy_state_test.go`; mechanical `RenderState` call-site updates in `internal/core/{wake_test.go:248,autonomy_state_test.go,scope_autonomy_test.go}`, `internal/contextbuilder/{strip_test.go,strip_hint_test.go}`; add `"set_scene"` to the fake tool list of `newWakeEnvWith` (`wake_test.go` ~:41)

**Interfaces:**
- Produces (`internal/autonomy/state.go`):
  ```go
  type StateScene struct { InPerson bool; Place string }
  func RenderState(scene *StateScene, st *StateStatus, bars []StateBar, effects []StateEffect, now time.Time, loc *time.Location) string
  ```
  The scene line is the first line after the header (exact copy in Global Constraints; place passed through `oneLine`); `nil` scene renders exactly today's block. Over 300 runes the trim order is unchanged and the scene line is never dropped or shortened (the status line is shortened by the extra runes instead).
- Produces (`strip.go`): `stateBlockLine` also matches `场景[：:]`.
- Produces (`internal/core/scene.go`):
  ```go
  const (
      skillScene       = "scene"
      sceneIdleTimeout = 3 * time.Hour
      sceneReturnSlack = 2 * time.Minute // used by T4
  )
  // effectiveScene: no row → {Mode: online}; in_person with now-TouchedAt >= sceneIdleTimeout → online, Place "",
  // PrevMode in_person, PrevPlace the place, SinceAt TouchedAt+sceneIdleTimeout, TouchedAt kept, fellBack true.
  func effectiveScene(sc store.ConversationScene, found bool, now time.Time) (eff store.ConversationScene, fellBack bool)
  // idleOnline reports a written-back fallback: online, PrevMode in_person, SinceAt == TouchedAt+sceneIdleTimeout.
  func idleOnline(sc store.ConversationScene) bool
  // taskScene is the scene a private task runs in; on when the bot enables skillScene and the read worked.
  type taskScene struct { on bool; sc store.ConversationScene }
  func (s taskScene) inPerson() bool
  func (s taskScene) state() *autonomy.StateScene // nil when !on
  // readScene: private chat and bot enables skillScene → GetScene(scope), effectiveScene; fellBack → EndIdleScene(scope,
  // raw.TouchedAt, eff.SinceAt); then, unless isWake(task.Meta.Trigger), TouchScene(scope, now) when eff is in person.
  // Read errors are logged (logStateError) and give taskScene{}; write errors are logged and keep eff.
  func (o *Orchestrator) readScene(ctx context.Context, task store.TaskRecord, bot config.BotConfig) taskScene
  ```
- Modifies: `stateBlock(ctx, task, bot, doc, scene taskScene) string` passes `scene.state()` to `RenderState`. `runner.go`: for private conversations `scene := o.readScene(ctx, task, bot)` right before the `if out.wake != nil` block (:316), then `in.State = o.stateBlock(ctx, task, bot, pv.Persona, scene)`. T4 extends this call site; keep the variable name `scene`.
- Test helper (in `core/scene_test.go`, reused by T4/T5): `var sceneSkill = skills.Skill{Name: "scene", Description: "d", Tools: []string{"set_scene"}, Conversations: []string{"private"}, Body: "- 用 set_scene。"}` passed as an override to `skillEnvWith`/`newWakeEnvWith` together with `&recTool{name: "set_scene"}`, so the tests do not depend on T2; `(*testEnv).setScene(t, mode, place string, since, touched time.Time)` writing the scope's row directly.

- [ ] **Step 1: Write failing tests**
  - `autonomy`: `TestRenderState_SceneLine` (table, exact whole blocks): online scene alone → `stateHeader + "\n场景：线上（你们隔着聊天软件发消息，不在一起）\n" + stateFooter`; in person 食堂 with a status → scene line, then the `正在：` line; in person with empty place → `场景：面对面（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）`; nil scene equals the old output. `TestRenderState_SceneLineSurvivesCap`: 16-rune place + long status + 4 bars with descriptions + effects → ≤300 runes, line 2 is exactly the in-person scene line, the status line was shortened. `TestRenderState_ScenePlaceFlattened`: place `"食\n堂【x】"` → `在食 堂x（`.
  - `contextbuilder`: `TestStripTranscriptPrefix_SceneLine`: `RenderState(&StateScene{InPerson: true, Place: "食堂"}, status, bars, nil, …) + "\n好呀"` → `好呀`; `"场景：随便\n好呀"` without a header is unchanged.
  - `core` `TestEffectiveScene_IdleBoundary`: touched `now-3h` → online, fellBack, since `touched+3h`, prev in_person/食堂; touched `now-3h+time.Second` → in person, not fellBack; no row → online, not fellBack; `idleOnline` true for the written-back form, false for an online row with `SinceAt == TouchedAt`.
  - `TestState_SceneLine`: bot skills `scene` → no row: request block contains the online line; row in person 食堂 → contains `场景：面对面，在食堂（`; bot without `scene` → no `场景：` line (and no block at all when nothing else shows).
  - `TestState_SceneFallbackBeforeTouch`: in person 食堂 touched a day before; 小明 writes `早上好` → request block has the online line; stored row is online, prev in_person/食堂, since touched+3h, `TouchedAt` unchanged (not refreshed).
  - `TestState_SceneTouchedByPersonOnly`: in person touched 1h ago; a private reply task → `TouchedAt == now`; an initiate wake task (via `newWakeEnvWith`) → `TouchedAt` unchanged.
  - `TestState_SceneSharedByPanelChats`: scene set under the panel scope from chat A shows in chat B's reply block.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/autonomy/ ./internal/contextbuilder/ ./internal/core/ -run 'Scene|RenderState|Strip'` → FAIL (compile errors first).
- [ ] **Step 3: Implement** per Interfaces; update the call sites of `RenderState` (insert a leading `nil`), the doc comments of `RenderState`, `renderState`, `stateBlock`, `stateBlockLine`, `StripTranscriptPrefix`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/autonomy/ ./internal/contextbuilder/ ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(core): scene line in the hidden state block with a silence fallback`

### Task 4: Status-return restore, in-person wake directives, no `set_scene` in wake turns

**Files:**
- Modify: `internal/core/copy.go` (:133-164), `internal/core/wake.go` (`wakeDirective` :449-484 and its doc), `internal/core/runner.go` (the `wakeDirective` call :320), `internal/core/skills.go` (`newSkillRun` doc: neither `set_status` nor `set_scene`)
- Test: `internal/core/scene_wake_test.go` (new), `internal/core/wake_test.go` (`TestWake_Copy` call sites only)

**Interfaces:**
- Consumes: T3 `taskScene`, `readScene`, `sceneReturnSlack`, `sceneSkill` test helper; T1 `SetScene`.
- Produces (`copy.go`; online variants byte-identical to today):
  ```go
  func CopyDirectiveInitiate(name, now, gap string, protected, inPerson bool) string
  func CopyDirectiveFollowUp(name string, protected, inPerson bool) string
  func CopyDirectiveStatusReturn(activity string, inPerson bool) string
  ```
  In-person variants keep every constraint of the online text (start `【提示】`, `[[SKIP]]` rule, no 主动/系统/提示 mention, `copyNoRomance` when protected) but say `你们现在在一起` and that the bot speaks up beside them (`在对方身边开口`) instead of sending a message; no `发消息`. Status return in person: `你刚做完「<activity>」，回到了对方身边，你们现在在一起。…`.
- Produces (`wake.go`):
  ```go
  func (o *Orchestrator) wakeDirective(ctx context.Context, task store.TaskRecord, w *wakeRun, mode conloomapi.ContentMode,
      history []store.MessageRecord, scene *taskScene) (string, bool)
  // restoreScene, for a status return whose status st has ended: when scene.on, scene.sc is online, PrevMode is in_person
  // and !scene.sc.SinceAt.Before(st.StartedAt.Add(-sceneReturnSlack)), SetScene{Mode: in_person, Place: PrevPlace,
  // SinceAt: now, TouchedAt: now, Origin: task.ConversationKey} and updates *scene; errors logged, scene left as is.
  func (o *Orchestrator) restoreScene(ctx context.Context, task store.TaskRecord, st store.ConversationStatus, scene *taskScene)
  ```
  The status-return branch calls `restoreScene` after the status checks and before `CopyDirectiveStatusReturn(st.Activity, scene.inPerson())`; initiate/follow-up pass `scene.inPerson()`. Because `runner.go` hands the same `scene` to `stateBlock`, the block and the directive agree. A status return that ends silently in `dueStatusReturn` (missed, quiet, not allowed) restores nothing.

- [ ] **Step 1: Write failing tests** (`scene_wake_test.go`, `newWakeEnvWith(t, nil, []skills.Skill{sceneSkill}, []string{"scene", "status", "buff"}, nil)`, `setWakeStatus`, `fireWake`, `wakeRequest`)
  - `TestSceneReturn_RestoresInPerson`: in person 食堂 touched t0; at t0+1m status 洗澡 20m and scene online since t0+1m (prev in_person/食堂); fire the return at the status end → stored `{in_person, 食堂, since now, touched now, prev online}`; request contains `你刚做完「洗澡」，回到了对方身边` and `场景：面对面，在食堂（`.
  - `TestSceneReturn_SlackBoundary`: online since `StartedAt-2m` → restored; since `StartedAt-2m-1s` → stays online, request has the online status-return text and the online line.
  - `TestSceneReturn_NeverSwitchedOnline`: in person touched 10m before the status, never switched → row unchanged (`SinceAt`, `PrevMode`, `TouchedAt`), in-person directive.
  - `TestSceneReturn_OnlineBeforeErrandStaysOnline`: online with prev in_person since 2h before the status → not restored, online directive.
  - `TestSceneReturn_ErrandOutlastsSilence`: in person touched at the status start, no switch, 4h status → at return the fallback (since start+3h) is written and then restored → in person, touched now.
  - `TestSceneReturn_SkillOff`: bot without `scene`, same rows as the first test → rows untouched, online text, no `场景：` line.
  - `TestWake_SceneDirectives`: in person → initiate and follow-up requests contain `你们现在在一起`; online → they contain `主动找 小明 说话` and not `你们现在在一起`.
  - `TestWake_SetSceneNotOffered`: skills `scene, status, buff`; initiate and status return → `offeredTools` = `[adjust_buff add_buff remove_buff]`; system prompt has no `set_scene`.
  - `TestCopyDirective_SceneVariants`: every in-person variant starts `【提示】`, contains `SkipMarker` and `你们现在在一起`, lacks `发消息`; protected in-person initiate/follow-up end with `noRomanceLine`; `CopyDirectiveStatusReturn("洗澡", true)` lacks `noRomanceLine`. Update `TestWake_Copy` to the new signatures with `inPerson=false`, assertions unchanged.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'SceneReturn|SceneDirectives|SetSceneNotOffered|CopyDirective|Wake_Copy'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; update the doc comments of the three copy functions, `wakeDirective` and `newSkillRun`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(core): restore the in-person scene after an errand and in-person wake directives`

### Task 5: `AutonomyService` scene read/write, panel and API routes, `pkg/client`

**Files:**
- Modify: `internal/core/autonomy_service.go`, `pkg/conloomapi/autonomy.go` (`AutonomyState.Scene`, doc), `internal/panel/autonomy.go` (interface + handler), `internal/panel/write_routes.go` (:31), `internal/panel/respond.go` (:101), `internal/gateway/autonomy.go`, `internal/gateway/server.go` (:107), `internal/gateway/errors.go` (:44), `pkg/client/client.go`
- Test: `internal/core/autonomy_scene_test.go`, `internal/panel/autonomy_scene_test.go`, `internal/gateway/autonomy_scene_test.go` (all new), `pkg/client/client_test.go`

**Interfaces:**
- Consumes: T1 types and store methods; T3 `skillScene`, `effectiveScene`, `idleOnline`; existing `CleanActivity` (`internal/skills/tools`; core may import it — `go list -deps ./internal/skills/tools` has no core), `checkPanelSource`, `privateChat`, `audit`.
- Produces (core):
  ```go
  var ErrInvalidScene = errors.New("core: invalid scene") // 400 in panel and API
  // Scene: the effective scene without writing; Enabled from the bot's skills; IdleOnline when the fallback applies
  // now (effectiveScene) or was written back (idleOnline); SinceAt zero without a row.
  func (s *AutonomyService) Scene(ctx context.Context, convKey string) (conloomapi.AutonomyScene, error)
  // SetScene: mode online|in_person else ErrInvalidScene; place = tools.CleanActivity, dropped for online, >16 runes
  // ErrInvalidScene; empty place allowed; not reviewed (operator input). Stores SinceAt=TouchedAt=now, origin convKey,
  // allowed whether or not the bot enables scene. Audits autonomy.scene.set {mode, place, source, changed}, also when
  // unchanged; a failed audit only logged.
  func (s *AutonomyService) SetScene(ctx context.Context, convKey, mode, place, source, actor string) error
  ```
  `State` fills `out.Scene` with `Scene`'s value. `conloomapi.AutonomyState` gains `Scene AutonomyScene \`json:"scene"\``.
- Produces (panel): `AutonomyService` interface gains `Scene` and `SetScene`; `putAutonomyScene` decodes `conloomapi.AutonomyScenePut`, calls `SetScene(…, store.BuffSourcePanel, panelActor(sess))`, answers 204; route `s.api("PUT /panel/api/conversations/{key}/autonomy/scene", s.putAutonomyScene)`; `respond.go` maps `core.ErrInvalidScene` to 400 with the trimmed message, as for `ErrInvalidBar`.
- Produces (gateway): `s.handle("PUT /api/v1/conversations/{id}/autonomy/scene", conloomapi.ScopeMemoryWrite, s.putAutonomyScene)`, `store.BuffSourceAPI`, actor `tok.name`, answers `{"ok":true}`; `errors.go` maps `ErrInvalidScene` to 400.
- Produces (client): `func (c *Client) PutAutonomyScene(ctx context.Context, convID string, put conloomapi.AutonomyScenePut) error` → `PUT conversationPath(convID, "autonomy/scene")`.

- [ ] **Step 1: Write failing tests**
  - `TestAutonomyService_Scene`: bot without `scene` → `Enabled` false; with it and no row → `{true, online, "", zero, false}`; in person 食堂 touched 1h ago → in person, `SinceAt` set; touched exactly 3h ago → online, `IdleOnline` true and `GetScene` still in_person (nothing written); written-back fallback row → `IdleOnline` true; online set by the panel → `IdleOnline` false.
  - `TestAutonomyService_SetScene`: `in_person, 食堂, panel, panel:admin` → row `{in_person, 食堂, prev online, since = touched = now, origin convKey}`; audit `autonomy.scene.set` detail `{"mode":"in_person","place":"食堂","source":"panel","changed":true}`, actor `panel:admin`, target convKey; same again → `changed:false`, `SinceAt` kept, second audit written; `online, 食堂` → place dropped; unknown source → error; group → `ErrNotPrivate`; missing chat → `store.ErrNotFound`.
  - `TestAutonomyService_SetScenePlace`: `"【图书馆】"` → `图书馆`; `"食\n堂"` → `食 堂`; 17 runes → `errors.Is(err, ErrInvalidScene)`, nothing stored; mode `"offline"` → `ErrInvalidScene`.
  - `TestAutonomyService_SetSceneEmptyPlace`: `in_person, ""` → stored with empty place; `State(...).Scene` = `{Enabled: true, Mode: in_person, Place: ""}`; a following private reply's block contains `场景：面对面（你们在同一个地方；` and not `，在`.
  - `TestPanelAutonomy_Scene` (harness of `autonomy_test.go`, harness bot configured with `scene` in its skills): PUT without CSRF → 403; PUT `{"mode":"in_person","place":""}` → 204; GET `…/autonomy` → `scene.mode == "in_person"`, `scene.enabled`; PUT `{"mode":"offline"}` → 400 whose message names the mode; group conversation → 400; audit actor `panel:<user>`.
  - `TestGatewayAutonomy_Scene`: `memory:read` token PUT → 403; `memory:write` token → 200 `{"ok":true}`, audit actor = token name, detail source `api`; GET autonomy has `"scene"`; group → 400; invalid place → 400.
  - `TestClient_PutAutonomyScene`: httptest server sees `PUT /api/v1/conversations/c%2F1/autonomy/scene` (escaped ID) with body `{"mode":"in_person","place":"食堂"}`; a 400 response maps to the client's API error.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ ./internal/panel/ ./internal/gateway/ ./pkg/client/ -run 'Scene'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; doc comments of `AutonomyService`, `State`, `AutonomyState`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ ./internal/panel/... ./internal/gateway/ ./pkg/...` → PASS.
- [ ] **Step 5: Commit** `feat(api): read and set a private chat's scene in the panel and the API`

### Task 6: Panel autonomy tab — 场景 section

**Files:**
- Modify: `internal/panel/web/views/autonomy.js`, `internal/panel/web/lib/format.js`
- Test: `internal/panel/web/scene_web_test.go` (new, package `web`, uses the `assets` helper of `web_test.go`)

**Interfaces:**
- Consumes (JSON contract fixed in T1/T5): `GET …/autonomy` → `scene: {enabled, mode, place, since_at?, idle_online}`; `PUT convPath(key, 'autonomy', 'scene')` body `{mode, place}` → 204.
- Produces:
  - `lib/format.js`: `export function sceneLabel(scene)` → `` `面对面 · ${place}` `` / `'面对面'` for `in_person`, `'线上'` otherwise; `export const SCENE_PLACE_MAX = 16;`.
  - `autonomy.js`: `function sceneSection(key, scene, write)`, rendered first in `autonomyTab` (above `statusSection`). Card `h2` `场景`. `!scene.enabled` → only `p.hint` `bot 没有启用 scene 技能，不区分线上和面对面。`. Otherwise `kv([['现在', sceneLabel(scene)], ['开始', fmtTime(scene.since_at)]])`; `scene.idle_online` → `p.hint.warn` `超过 3 小时没说话，已回到线上`; a 切到线上 button (`put(…, { mode: 'online', place: '' })`, flash `已切到线上`); a form with a place input (`maxlength: SCENE_PLACE_MAX`, placeholder `地点（可选，最多 16 字）`) and a 切到面对面 submit (`{ mode: 'in_person', place }`, flash `已切到面对面`); client check `[...place].length > SCENE_PLACE_MAX` → `errorBox({ message: '地点最多 16 个字' })`; hint `模型用 set_scene 切换；面对面时对方超过 3 小时没说话，会自动回到线上。`. All text through `h()` (no HTML).

- [ ] **Step 1: Write failing tests** `TestAutonomyTab_SceneSection`: `autonomy.js` contains `'场景'`, `'超过 3 小时没说话，已回到线上'`, `'autonomy', 'scene'`, `mode: 'in_person'`, `mode: 'online'`, `scene.enabled`, `scene.idle_online`, `sceneLabel(`, `SCENE_PLACE_MAX`, `'地点最多 16 个字'`; `sceneSection(` appears before `statusSection(` inside `autonomyTab`; `format.js` contains `` `面对面 · ${`` and `'面对面'`. Existing `TestAssets_NoExternalOrUnsafeAPIs`, `TestAssets_ImportsResolve`, `TestAssets_NoAPIsNewerThanChrome89` cover the rest.
- [ ] **Step 2: Run** `go test ./internal/panel/web/ -run 'SceneSection' -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/web/` → PASS; `for f in internal/panel/web/views/autonomy.js internal/panel/web/lib/format.js; do /usr/local/node/bin/node --check "$f"; done` → no output; self-check (not committed): `/usr/local/node/bin/node --input-type=module -e "import {sceneLabel} from './internal/panel/web/lib/format.js'; console.log(sceneLabel({mode:'in_person',place:'食堂'}), sceneLabel({mode:'in_person',place:''}), sceneLabel({mode:'online'}))"` → `面对面 · 食堂 面对面 线上`.
- [ ] **Step 5: Commit** `feat(panel): scene section in the autonomy tab`

### Task 7: `#/im` — scene in GET chat, header label, muted action brackets

**Files:**
- Modify: `internal/panel/chat.go` (`getChat` :185-238 and its doc), `internal/panel/web/views/chat.js` (`bubble` :93, `chatPane` status line :255-263 and `poll` :760), `internal/panel/web/lib/format.js`, `internal/panel/web/style.css` (next to `.chat-app .tg-typing` ~:732)
- Test: `internal/panel/chat_scene_test.go` (new), `internal/panel/web/scene_web_test.go`

**Interfaces:**
- Consumes: T5 panel `AutonomyService.Scene`; T6 `sceneLabel`.
- Produces (Go): `getChat` JSON gains `Scene *conloomapi.AutonomyScene \`json:"scene"\``: `null` when `s.d.Autonomy` is nil or the conversation is not private, otherwise `Scene(ctx, conv.Key)` (other errors fail the poll as the other reads do).
- Produces (JS):
  - `lib/format.js`: `export const ACTION = /（[^（）\n]*）/g;` and `export function actionParts(text)` → `[{ text, action }]` covering the whole string in order (unmatched `（` stays plain text).
  - `chat.js`: `bubble` renders `actionParts(m.text).map((p) => (p.action ? h('span', { class: 'tg-action' }, p.text) : p.text))` inside `.tg-text` for bot and user messages; a `tg-scene` span in the status line shows `sceneLabel(res.scene)` only when `res.scene && res.scene.enabled && res.scene.mode === 'in_person'` and `!botTyping` (typing takes precedence; toggle with `hidden`); updated on every poll.
  - `style.css`: `.chat-app .tg-action { color: var(--muted); }` and `.chat-app .tg-scene { color: var(--muted); }` (only `.chat-app`-scoped selectors, per `TestChatPages_StylesScoped`); the label and the typing indicator share the status line without shifting it (implementer decides, e.g. stacking them).

- [ ] **Step 1: Write failing tests**
  - `TestGetChat_Scene` (panel harness with a real core `AutonomyService`, bot with `scene`): panel chat set in person 食堂 via the service → GET `…/chat` has `scene.mode == "in_person"`, `scene.place == "食堂"`; group conversation → `"scene": null`; `Deps.Autonomy` nil → `"scene": null`, messages still returned; bot without `scene` → `scene.enabled == false`.
  - `TestChatPane_SceneLabel`: `chat.js` contains `res.scene`, `sceneLabel(`, `tg-scene`, `'in_person'`, and the label toggle references `botTyping`; `style.css` matches `\.chat-app \.tg-scene\s*\{[^}]*color:\s*var\(--muted\)`.
  - `TestChatPane_ActionBrackets`: `format.js` contains `` /（[^（）\n]*）/g `` and `export function actionParts`; `chat.js` contains `actionParts(m.text)` and `class: 'tg-action'` and still `h('span', { class: 'tg-text' }`; `style.css` matches `\.chat-app \.tg-action\s*\{[^}]*color:\s*var\(--muted\)`; `views/chatclassic.js` does not use `tg-action`.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/... -run 'GetChat_Scene|SceneLabel|ActionBrackets'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/...` → PASS; `/usr/local/node/bin/node --check` on `chat.js`, `format.js`; self-check (not committed): `/usr/local/node/bin/node --input-type=module -e "import {actionParts} from './internal/panel/web/lib/format.js'; console.log(JSON.stringify(actionParts('好（<b>笑</b>）呀（\n）（')))"` → `[{"text":"好","action":false},{"text":"（<b>笑</b>）","action":true},{"text":"呀（\n）（","action":false}]` — the `<b>` stays text because `h()` makes text nodes; report the output.
- [ ] **Step 5: Commit** `feat(panel): in-person label in the chat header and muted action brackets`

### Task 8: Docs

**Files:**
- Modify: `docs/skills.md` (built-in table :41, new `### set_scene` after `### set_status` :111, the wake-tools paragraph :185), `docs/architecture.md` (state block :458/:476, strip rule :503, wake turn :274, autonomy tables and cleanup :145/:264/:547), `docs/panel.md` (`#/im` section ~:141, autonomy page, audit kinds :244, endpoint table :364-367), `docs/api.md` (route table :29-33, `GET …/autonomy` fields ~:147, new `PUT …/autonomy/scene` section)

**Content:**
- `skills.md`: `scene` row (`set_scene`, 私聊); tool rules (modes, place cleaning/16 字/review, online drops the place, no-op, results); the 3-hour fallback, touch rule and status-return restore with 2-minute slack; wake turns do not offer `set_scene`.
- `architecture.md`: `conversation_scene` (0022, scope key, origin, prev, touched); `readScene` order (fallback write-back → touch for a person's message; wake tasks never touch); scene line first in the state block and never trimmed; strip also removes `场景：…` lines after the header; in-person directive variants; cleanup lists.
- `panel.md`: 场景 card (fallback note, controls, skill-off hint), `PUT conversations/{key}/autonomy/scene` row (204, 400 cases), audit `autonomy.scene.set`, `#/im` header label (typing first) and muted `（…）` (panel only, text still escaped).
- `api.md`: route row (`memory:write`), `scene` object fields, request/response and errors of `PUT …/autonomy/scene`, audit detail; `pkg/client.PutAutonomyScene`.

- [ ] **Step 1: Edit docs**; `grep -n "set_status" docs/*.md` and check every list of built-in tools/skills now includes `set_scene`/`scene`.
- [ ] **Step 2: Full check:** `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS; `for f in $(git diff --name-only main -- '*.js'); do /usr/local/node/bin/node --check "$f"; done` → no output.
- [ ] **Step 3: Commit** `docs: chat scene skill, state line, panel and API`

---

## Self-review

- **Spec coverage:** storage + cleanup lists → T1; tool, private-only, cleaning, review, online drops place, no-op, SKILL.md rules → T2; effective scene, 3h fallback written by the reply path before the touch, only the person's messages touch, scene line always first and never dropped → T3; wake turns without `set_scene`, deterministic status-return restore with 2-minute slack, in-person directive variants → T4; `State` + API/panel write + client + audit with source → T5; autonomy tab → T6; `#/im` header, `getChat.scene`, `（…）` styling → T7; docs → T8. Spec "Tests for" list: tool validation (T2), store CRUD and cleanup (T1), fallback (T3), RenderState line and cap (T3), wake tool availability (T4), directive variants (T4), panel API + audit (T5), web source tests (T6, T7).
- **Names consistent across tasks:** `conloomapi.SceneOnline/SceneInPerson/ScenePlaceMaxRunes`, `AutonomyScene{enabled,mode,place,since_at,idle_online}`, `AutonomyScenePut{mode,place}` (T1) used by T5–T7; `store.ConversationScene`, `GetScene/SetScene/TouchScene/EndIdleScene` (T1) used by T2–T5; `skillScene`, `sceneIdleTimeout`, `sceneReturnSlack`, `effectiveScene`, `idleOnline`, `taskScene`, `readScene` (T3) used by T4–T5; `sceneLabel` (T6) used by T7.

## Decisions on spec ambiguities

1. Store API: the spec names `GetScene`, `SetScene`, `ClearScene`. Deleting the row would lose `prev_*`, and the fallback and the touch need compare-and-set writes, so the plan uses `GetScene`, `SetScene` (prev and no-op handled inside the transaction), `TouchScene`, `EndIdleScene`. No `ClearScene`: deletion is covered by the cleanup lists.
2. The spec says the existing strip rules already cover echoes of the block. They do not: `stateBlockLine` stops at the first unknown line, so an echoed `场景：…` line would end the strip right after the header. T3 adds `场景[：:]`.
3. "Proactive" directives in a private chat are the initiate directive; the group join directive is unchanged because groups are always online.
4. The fallback is written with `since_at = touched_at + 3h`, not the read time, so the panel's computed fallback and the written-back one show the same time. The panel note also shows for a written-back fallback, detected by `online && prev_mode = in_person && since_at = touched_at + 3h`.
5. A no-op `set_scene` (same mode and place) keeps `since_at` but refreshes `touched_at`: the model saying they are still together is a sign of presence.
6. The restore runs inside the status-return task, before its directive, after the fallback write-back. So a long errand during which the person stayed silent still returns in person, as long as the online switch (or the fallback) happened after the status started (2-minute slack). A status return that ends silently (missed, quiet hours, not allowed) does not restore.
7. Panel/API writes are accepted even when the bot does not enable `scene` (stored, no prompt effect). Reads report `enabled` so the tab can show the skill-off hint. Panel/API places are cleaned and capped but not reviewed, because they are operator input.
8. Deleting a panel chat also deletes the shared scene it set, mirroring the status.
9. A place that the reviewer masks down to nothing is refused (`这个地点不能用。`), as `set_status` refuses an empty activity.
10. `getChat.scene` is `null` for groups and when there is no autonomy service. The header label shows only for `in_person`.
