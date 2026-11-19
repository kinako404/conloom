# State Step Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replies of bots with state-changing tools come quickly: the reply step offers only lookup tools with thinking off, and afterwards a background state step (thinking as configured) offers only the state tools for the turn that just happened, with the same invocation, and sends nothing. Config `skills.state_step: after_reply` (default) or `inline` (today's single tool loop, byte-identical). Wake turns reply without tools and run a state step limited to `wakeTools`.

**Architecture:** `internal/skills` gets the optional `StateChanger` interface and `Toolset.Lookups`/`StateTools`; the six state built-ins declare it. `config.SkillsConfig` gets `StateStep`. In `internal/core`, `newSkillRun` splits the toolset in `after_reply` mode when it has state tools: the reply run (`skillRun.replyStep`) offers the lookups (possibly none), appends `copyReplyStepNote` to the full skill section and turns thinking off on every request (`thinkingOff`); the state tools travel in `skillRun.state`. `execute` snapshots a `stateTurn` (reply context, routing request, invocation, state tools, reply text) when the reply is accepted; `run` hands it to the new `internal/core/statestep.go` once `finish` stored actions. `stateSteps` keeps at most one running step per memory scope, one pending entry per (scope, speaker, conversation) of not-yet-covered turns, and the reply tasks running per (scope, speaker) (“holds”): a starting reply task cancels the running step of its (scope, speaker) and carries its turns over (only if it ran no tool call yet); pending entries run only while no reply of their key runs. The step routes like a reply, uses background admission priority, `statePassTimeout`, the existing tool round machinery and re-route, and records its calls with purpose `state`. Hooks: `supersede` (AbandonReplyOf), `cancelActive` (/pause, policy pause), `/stop`, `/忘记我`, `DeletePanelChat`, `Close`; a forget through API/panel is caught by a `ForgottenAt` check inside the step.

**Tech Stack:** Go (module `github.com/kinako404/conloom`), SQLite through `internal/store/sqlite` (no migration: `model_calls.purpose` is free text), vanilla JS in `internal/panel/web` (one label). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-11-19-state-pass-design.md` — read it before your task, including "Builder-check decisions", which are binding.

## Global Constraints

- Public repo: no real IDs, tokens, hosts, nicknames or character names in code, tests, comments, docs or commit messages. Fake data only: bots `bot_test`/`qq-main`, users `20001`/`29999`, names 小明/小红, `example.invalid`.
- Commit messages: English, conventional style, exactly as given per task.
- Before every commit: `gofmt -l .` prints nothing and `go vet ./...` passes; tests run with `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 <pkgs>`; a changed JS file passes `/usr/local/node/bin/node --check <file>`. `./internal/core/` takes ~6–7 min: iterate with `-run`, then run the whole package once before committing. The last task runs the full `./...`.
- `inline` mode stays byte-identical to today (prompts, requests, extra_body, behaviour). A toolset without any state tool (skill-less bots, bots with only lookups, every existing core test, whose fake `recTool`s are lookups) takes today's path in both modes. Every existing test passes unmodified except `TestAssets_PurposeLabelsMatchStore` (T3).
- Config default: an empty `skills.state_step` is `after_reply` (set by `config.Load`, and core treats anything but `inline` as `after_reply`, so configs built in Go behave the same).
- Fixed values: `config.StateStepAfterReply = "after_reply"`, `config.StateStepInline = "inline"`; `store.PurposeState = "state"`, panel label `state: '状态更新'`; `statePassTimeout = 90 * time.Second`; `stateMaxTurns = 8`; log message `state step` with `outcome` one of `done`, `cancelled`, `forgotten`, `no_room`, `unavailable`, `failed`.
- Fixed copy (used verbatim):
  - `copyReplyStepNote` = `（这一步只管回复对方：状态、场景、记忆这类变化会在你回复之后另外处理，现在不用调用相应的工具，回复里也不要提起这件事。）`
  - `copyStateStep` = `【状态更新】这一步不是回复，不会发给对方。根据最近这一轮（上面的当前消息和你下面的回复）判断你的状态、场景或要记住的事有没有变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。`
  - `copyStateStepWake` = `【状态更新】这一步不是回复，不会发给对方。你刚才主动给对方发了下面的消息，对方还没有回。判断你的状态有没有因此变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。`
  - state directive = copy + `\n\n` + heading + `\n` + one `「<reply>」` line per covered turn in order; heading `你刚才的回复：` (`你刚才发的：` with `copyStateStepWake`, used only when every covered turn is a wake turn).
- Prefer new test files; core test helpers added by T4 (`stateTool`, `stepEnv`) are reused by T5/T6, helpers added by T5 (`isStateRequest`, `waitStateIdle`) by T6.
- Update every comment and doc that describes changed behaviour: `skills` package doc, `Toolset`, `tools` package doc, `SkillsConfig`, `skillRun`, `newSkillRun`, `withSkills`, `call`, `chat`, `recordCall`, `run`, `Close`, `cancelActive`, `supersede`, `stop`, `DeletePanelChat`, `memCmdForget`, `thinkingOff` (now also used on every reply-step request).

## Review Focus

1. **Reply abandoned after completion** — the reply is stored, its state step is waiting in the model, the person writes again before any part is handed out (`AbandonReplyOf`): the old step is cancelled before any tool runs, the new task's step covers `[m1, m2]` exactly once, with the new task ID. Owner: T5 `TestStateStep_AbandonedReplyCancelsItsStep`.
2. **Two quick messages in a row** — the second reply starts while the first step is in the model: the first step is cancelled and carried over, one step covers both turns (both trigger IDs in order, both replies quoted); if the first step had already run a call, its turn is not carried (no double `adjust_buff`). Owners: T5 `TestStateStep_NextReplyPreemptsAndCoalesces`, `TestStateStep_PreemptedAfterACallIsNotRepeated`.
3. **Shared scopes** — two panel chats of one bot (one memory scope, same panel user) and a group with two speakers: never two steps of one scope at once, each step's invocation carries its own conversation / speaker and triggers. Owners: T5 `TestStateStep_PanelChatsShareAScope`, `TestStateStep_GroupSpeakersRunInTurn`.
4. **Model failure and shutdown inside the step** — the first model fails mid-loop: the step re-routes, keeps its progress (no call runs twice), stays bounded by `statePassTimeout`; `Close` during a step returns promptly, runs no further call, starts no new step. Owners: T5 `TestStateStep_ReRoutesOnModelFailure`, `TestStateStep_CloseCancelsRunningStep`.
5. **Paths that must not change** — a skill-less bot, a bot with only lookups and `inline` mode send today's requests (thinking as configured, no state step); a wake turn replies without tools and its step offers only `wakeTools`. Owners: T4 `TestReplyStep_UnsplitTurnsUnchanged`, `TestReplyStep_InlineModeUnchanged`; T6 `TestStateStep_WakeTurnBuffToolsOnly`.

## Task order and parallel groups

| Stage | Tasks (parallel within a stage) | Starts after |
|---|---|---|
| A | T1 tool classification, T2 config switch, T3 `state` usage purpose | — |
| B | T4 reply step | A merged |
| C | T5 state step engine | T4 merged (both edit `runner.go`, `skills.go`) |
| D | T6 wake turns | T5 merged (edits `statestep.go`) |
| E | T7 docs + full check | all merged |

Files per task (no two tasks of one stage share a file):

- T1: `internal/skills/{skill.go,registry.go}`, `internal/skills/tools/{tools.go,remember.go,status.go,scene.go,buff.go,buff_add.go,buff_remove.go}`; tests `internal/skills/state_test.go`, `internal/skills/tools/state_test.go`, `internal/app/skills_state_test.go` (all new).
- T2: `internal/config/{config.go,validate.go}`, `conloom.example.yaml`; test `internal/config/state_step_test.go` (new).
- T3: `internal/store/usage.go`, `internal/panel/web/lib/format.js`; test `internal/panel/web/web_test.go` (`TestAssets_PurposeLabelsMatchStore` expectations only).
- T4: `internal/core/{skills.go,runner.go}` (`call` only); test `internal/core/replystep_test.go` (new).
- T5: `internal/core/statestep.go` (new), `internal/core/{runner.go,orchestrator.go,commands.go,memory_commands.go,panelchat.go}`; tests `internal/core/statestep_test.go`, `internal/core/statestep_cancel_test.go` (new).
- T6: `internal/core/{statestep.go,skills.go}` (docs of `newSkillRun`); test `internal/core/statestep_wake_test.go` (new).
- T7: `docs/skills.md`, `docs/architecture.md`, `docs/configuration.md`, `docs/panel.md`.

---

### Task 1: Tool classification

**Files:**
- Modify: `internal/skills/skill.go` (after `Tool` :36-43), `internal/skills/registry.go` (`Toolset` :80-129), `internal/skills/tools/{remember.go,status.go,scene.go,buff.go,buff_add.go,buff_remove.go}` (next to each `MaxResultRunes` method), `internal/skills/tools/tools.go` (package doc :1-4)
- Test: `internal/skills/state_test.go`, `internal/skills/tools/state_test.go`, `internal/app/skills_state_test.go` (new)

**Interfaces:**
- Produces (`skill.go`):
  ```go
  // StateChanger is implemented by a tool that changes stored state (memory, status, scene, bars, effects) and whose
  // result the reply does not need. A tool that does not implement it is a lookup.
  type StateChanger interface{ ChangesState() bool }
  // ChangesState reports whether tool is a state tool (StateChanger returning true).
  func ChangesState(tool Tool) bool
  ```
- Produces (`registry.go`):
  ```go
  // Lookups returns t with only its lookup tools, in order; Section is kept, also when no tool is left.
  func (t Toolset) Lookups() Toolset
  // StateTools returns t with only its state tools, in order; Section is kept, also when no tool is left.
  func (t Toolset) StateTools() Toolset
  ```
- `func (*rememberTool) ChangesState() bool { return true }` and the same for `setStatusTool`, `setSceneTool`, `adjustBuffTool`, `addBuffTool`, `removeBuffTool`. `recall`, `get_affinity`, `now` declare nothing. Package doc of `tools`: which tools are lookups and which change state.

- [ ] **Step 1: Write failing tests**
  - `TestChangesState` (`skills`): a tool without the method → false; one returning false → false; one returning true → true.
  - `TestToolset_LookupsAndStateTools`: Toolset `{Section: "S", Tools: [a(lookup), b(state), c(lookup)]}` → `Lookups()` = `{S, [a, c]}`, `StateTools()` = `{S, [b]}`; all-lookup set → `StateTools().Empty()` with `Section == "S"`; zero Toolset → both zero.
  - `TestBuiltinTools_ChangesState` (`tools`): `skills.ChangesState` is true for `NewRemember(RememberDeps{})`, `NewSetStatus(nil, nil, nil, nil)`, `NewSetScene(nil, nil, nil, nil)`, `NewAdjustBuff(nil, nil)`, `NewAddBuff(nil, nil, nil, nil)`, `NewRemoveBuff(nil, nil)`; false for `NewRecall(RecallDeps{})`, `NewGetAffinity(AffinityDeps{})`, `NewNow()`.
  - `TestBuiltinTools_AllClassified` (`app`): `builtinTools(toolDeps{})` names map exactly to `{remember: true, recall: false, get_affinity: false, now: false, set_status: true, set_scene: true, adjust_buff: true, add_buff: true, remove_buff: true}` (a new built-in must be added here).
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/... ./internal/app/ -run 'ChangesState|LookupsAndStateTools|AllClassified'` → FAIL (undefined).
- [ ] **Step 3: Implement** per Interfaces.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/skills/... ./internal/app/` → PASS.
- [ ] **Step 5: Commit** `feat(skills): classify tools as lookups or state tools`

### Task 2: Config switch `skills.state_step`

**Files:**
- Modify: `internal/config/config.go` (`SkillsConfig` :73-78, `Load` defaults near :517), `internal/config/validate.go` (`ValidateServe` :127-140), `conloom.example.yaml` (skills comment :134-137)
- Test: `internal/config/state_step_test.go` (new; helpers `writeConfig`, `mustLoad`, `testdata/valid.yaml`)

**Interfaces:**
- Produces:
  ```go
  // Values of SkillsConfig.StateStep.
  const (StateStepAfterReply = "after_reply"; StateStepInline = "inline")
  type SkillsConfig struct {
      Dir string `yaml:"dir"`
      // StateStep is when state tools run: StateStepAfterReply (default; the reply offers only lookups with thinking
      // off, the state tools run in a background step after it) or StateStepInline (one tool loop with all tools).
      StateStep string `yaml:"state_step"`
  }
  ```
  `Load` sets an empty `StateStep` to `StateStepAfterReply`. `ValidateServe`: other values → `p.addf("skills.state_step", "must be %s or %s, got %q", StateStepAfterReply, StateStepInline, v)` (empty accepted: configs built in Go).
- Example YAML adds under the skills comment: `# state_step 默认 after_reply：回复只带查询类工具、关掉思考，改状态的工具在回复之后单独跑一步；inline 是旧行为（一个工具循环带全部工具）。` and `#   state_step: after_reply`.

- [ ] **Step 1: Write failing tests**
  - `TestLoad_SkillsStateStep`: base `valid.yaml` → `after_reply`; `+ "skills:\n  state_step: inline\n"` → `inline`; both pass `ValidateServe`.
  - `TestValidate_SkillsStateStep`: `Inline`, `later` → error containing `skills.state_step: must be after_reply or inline, got "later"`; `""` and both values → no `skills.state_step` problem.
  - The existing example-config load test still passes (comment-only change).
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/config/ -run 'StateStep'` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/config/ ./internal/app/` → PASS.
- [ ] **Step 5: Commit** `feat(config): skills.state_step switch`

### Task 3: Usage purpose `state`

**Files:**
- Modify: `internal/store/usage.go` (:6-11), `internal/panel/web/lib/format.js` (`PURPOSE` :164-171)
- Test: `internal/panel/web/web_test.go` (`TestAssets_PurposeLabelsMatchStore` :1020-1037)

**Interfaces:**
- Produces `store.PurposeState = "state"` (after `PurposeSuggest`, doc: a reply's state step); `PURPOSE` gains `state: '状态更新',` as the last entry.

- [ ] **Step 1: Update the test:** `want` appends `store.PurposeState`; also require `state: '状态更新'` in the block.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/panel/web/ -run PurposeLabels` → FAIL.
- [ ] **Step 3: Implement**; `node --check internal/panel/web/lib/format.js`.
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/store/... ./internal/panel/... ./internal/usage/` → PASS.
- [ ] **Step 5: Commit** `feat(usage): state purpose for state step model calls`

### Task 4: Reply step — lookups only, note line, thinking off

**Files:**
- Modify: `internal/core/skills.go` (const block :22-59, `skillRun` :75-90, `newSkillRun` :99-159, `withSkills` :161-169), `internal/core/runner.go` (`call` :642-727 and its doc)
- Test: `internal/core/replystep_test.go` (new)

**Interfaces:**
- Consumes: T1 `skills.ChangesState`, `Toolset.Lookups/StateTools`; T2 `config.StateStepInline`.
- Produces (`skills.go`):
  ```go
  const copyReplyStepNote = "…" // Global Constraints
  type skillRun struct {
      … // unchanged fields
      // replyStep marks the reply step of a turn whose state tools run in a state step afterwards: set offers only
      // lookups (possibly none), the skill section gets copyReplyStepNote and every request turns thinking off.
      replyStep bool
      // state holds the turn's state tools under the same skill section, for its state step; empty without one.
      state skills.Toolset
  }
  // newToolRun is the run offering set's tools for inv: tool definitions, schema ("" without tools), result limit.
  func newToolRun(set skills.Toolset, inv skills.Invocation) *skillRun
  // stateStepAfterReply reports whether state tools run in a state step (config skills.state_step not inline).
  func (o *Orchestrator) stateStepAfterReply() bool
  ```
  `newSkillRun` keeps its eligibility and wake filter, builds the invocation as today, then: in `after_reply` mode with `!set.StateTools().Empty()` returns `newToolRun(set.Lookups(), inv)` with `replyStep = true`, `state = set.StateTools()`; otherwise `newToolRun(set, inv)` (today). `withSkills`: with `replyStep` the system prompt gets `"\n\n" + Section + "\n" + copyReplyStepNote`.
- `call` (`runner.go`): with `run.replyStep` and no tools: no tool-round reservation; one request with `withSkills(in, run)` and `ExtraBody: thinkingOff(p.Config.ExtraBody)`; an answer containing a `toolCallMarkers` entry is `ErrEmptyOutput` (reason `tool call written as text`, re-routed by `execute`), any other answer is returned as a reply without skills is (a `length` finish is sent). With lookups: today's loop, but every request (not only the final one) carries `thinkingOff`. The no-room fallback of a reply-step run keeps thinking off. Without `replyStep` nothing changes.

- [ ] **Step 1: Write failing tests** (`replystep_test.go`). Helpers: `type stateTool struct{ *recTool }` with `ChangesState() bool { return true }`; `stepEnv(t, mode string, configure func(*config.Config)) (env, now *recTool, mark stateTool)` = `skillEnvWith` with override skill `mixed` (`Tools: [now, mark]`, private+group, Body `- 用 now 查时间，用 mark 记下变化。`), bot skills `[mixed]`, everyone may chat privately, both profiles `ExtraBody = {"chat_template_kwargs": {"enable_thinking": true}}`, `Skills.StateStep = mode`.
  - `TestReplyStep_OffersOnlyLookupsWithThinkingOff` (`after_reply`; model calls `now` then answers): every reply request offers exactly `[now]`, has `chat_template_kwargs.enable_thinking == false`, its system message contains `- 用 now 查时间，用 mark 记下变化。` followed by `\n` + `copyReplyStepNote`; `now` called once; the reply is sent; `mark` not called by the reply.
  - `TestReplyStep_NoLookupsIsOneRequest` (override `mixed` with `Tools: [mark]`): exactly one reply request, no `tools` key, thinking off, system has the section and note; a reply containing `<tool_call>` is re-routed to `long_context` (`env.long.Calls() == 1`).
  - `TestReplyStep_UnsplitTurnsUnchanged`: for (a) a bot without skills and (b) a bot with only `privnote` (lookup `now`), the requests in `after_reply` and `inline` mode are `reflect.DeepEqual`, offer what today offers, keep `enable_thinking: true` on every non-final request, and contain no `copyReplyStepNote`.
  - `TestReplyStep_InlineModeUnchanged`: `inline` with `mark` as `stateTool` vs `after_reply` with `mark` as plain `recTool` (lookup): all requests `reflect.DeepEqual` (both offer `[now, mark]`, thinking on until the final request), `mark` called in the reply loop in both.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'ReplyStep'` → FAIL.
- [ ] **Step 3: Implement** per Interfaces; update docs of `skillRun`, `newSkillRun`, `withSkills`, `call`. (Until T5 the state tools of a split turn are not run; T5 lands before release.)
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/` → PASS (all existing skill, wake and autonomy tests unmodified).
- [ ] **Step 5: Commit** `feat(core): reply step offers lookups only with thinking off`

### Task 5: State step engine

**Files:**
- Create: `internal/core/statestep.go`
- Modify: `internal/core/runner.go` (`run` :80-144, `execute` :189-451, `chat` :729-774, `recordCall` :776-787), `internal/core/orchestrator.go` (struct :91-167, `New` :204-220, `cancelActive` :321-334, `Close` :336-355, `supersede` :744-753), `internal/core/commands.go` (`stop` :168+), `internal/core/memory_commands.go` (`memCmdForget` :234-249), `internal/core/panelchat.go` (`DeletePanelChat` :177-214)
- Test: `internal/core/statestep_test.go`, `internal/core/statestep_cancel_test.go` (new)

**Interfaces:**
- Consumes: T3 `store.PurposeState`; T4 `skillRun.state`, `newToolRun`.
- Produces (`statestep.go`):
  ```go
  const (statePassTimeout = 90 * time.Second; stateMaxTurns = 8)
  // stateTurn is a reply whose state changes a state step judges, snapshotted when the reply was accepted.
  type stateTurn struct {
      task  store.TaskRecord      // the reply task
      bot   config.BotConfig
      doc   conloomapi.PersonaDoc // persona of the reply (state block); zero when it failed to load
      tools skills.Toolset        // skillRun.state
      inv   skills.Invocation     // the reply run's invocation
      in    contextbuilder.Input  // the reply's context without skills
      req   router.Request        // the reply's routing request, Exclude cleared
      reply string                // the parts sent, joined by "\n"
      wake  bool
      at    time.Time             // CreatedAt of its first trigger, or the task's without triggers
  }
  type stateKey struct{ scope, speaker string } // memory.ScopeKeyOf(conv), task.ActorPlatformUserID
  type stateEntry struct { key stateKey; convKey string; turns []stateTurn }
  type stateStep struct { entry *stateEntry; cancel context.CancelFunc; called atomic.Bool; done chan struct{} }
  // stateSteps: at most one running step per scope; pending entries FIFO, one per (key, convKey); holds count the
  // reply tasks running per key.
  type stateSteps struct {
      mu sync.Mutex; closed bool; wg sync.WaitGroup
      running map[string]*stateStep; pending []*stateEntry; holds map[stateKey]int
  }
  func newStateSteps() *stateSteps
  func (s *stateSteps) idle() bool // nothing running or pending
  // holdState marks task's key busy and cancels its running step, whose turns go back to the front of their
  // pending entry unless it already ran a call. release adds turn (nil: none) to its entry (merging, keeping the
  // newest stateMaxTurns), drops the hold and starts what may run.
  func (o *Orchestrator) holdState(task store.TaskRecord) (release func(turn *stateTurn))
  // abandonStateTurn drops taskID's turn from pending; a running step covering it is cancelled and its other turns
  // carried back (unless it ran a call).
  func (o *Orchestrator) abandonStateTurn(taskID string)
  // cancelStateSteps drops convKey's pending turns (only speaker's unless speaker is "") and cancels its running step;
  // the channel closes when that step returned, nil when none ran.
  func (o *Orchestrator) cancelStateSteps(convKey, speaker string) <-chan struct{}
  func (o *Orchestrator) kickStateSteps()          // starts, per idle scope, the first pending entry whose key has no hold
  func (o *Orchestrator) runStateStep(st *stateStep) // goroutine body; logs "state step"
  func stateInvocation(turns []stateTurn) skills.Invocation // newest inv; TriggerMessageIDs = all turns', in order, deduped
  func stateDirective(turns []stateTurn) string            // copyStateStep + "\n\n你刚才的回复：\n「…」…"
  // stateInput is the newest turn's context: Current = all turns' Current (deduped, by Seq); History = its History
  // before the first covered message's Seq; Directive = stateDirective; Image nil; State refreshed (private:
  // o.stateBlock(ctx, task, bot, doc, o.readScene(ctx, task, bot))).
  func (o *Orchestrator) stateInput(ctx context.Context, turns []stateTurn) contextbuilder.Input
  func stateTask(t store.TaskRecord) store.TaskRecord // Priority = int(admission.PriorityBackground)
  // stateCall runs the tool rounds of a state step on d: no final request (it stops when lastRequest says so or the
  // model calls nothing; text is discarded); calls run with context.WithoutCancel(ctx) and toolCallTimeout, but none
  // after ctx ended or the speaker was forgotten since the first turn (errStateForgotten); a tool call written as
  // text or a cut tool round is ErrEmptyOutput; no room for a tool round is errStateNoRoom.
  func (o *Orchestrator) stateCall(ctx context.Context, task store.TaskRecord, d router.Decision, in contextbuilder.Input, run *skillRun) error
  ```
  `runStateStep`: ctx from `o.bgCtx` with `statePassTimeout` and `usage.WithAttribution` (newest task); forgotten check first; `run := newToolRun(newest.tools, stateInvocation(turns))`, `run.deadline = start + statePassTimeout` (no `restartClock`); route with `req.BaseTokens = BaseTokens(withSkills(in, run))`; on error other than ctx/forgotten/no-room re-route once with `Exclude` (progress kept); log `state step` with `task_id`, `conversation`, `turns`, `role`, `rounds`, `calls` (`skillCallSummary`), `duration_ms`, `outcome`. Nothing is ever written to the outbox.
- `runner.go`: `chat(ctx, task, d, req, built, estimate int, purpose string, logAttrs []any)` and `recordCall(task, p, purpose, start, latency, estimate, resp, err)`; `call` passes `store.PurposeChat`, `stateCall` passes `store.PurposeState` and `stateTask(task)`. `execute` captures the persona doc and, when the reply is accepted (`out.text` set) and `run != nil && !run.state.Empty()`, sets `out.stateTurn`. `run`: right after the task is marked running, `release := o.holdState(task)`; a deferred `release(turn)` where `turn = out.stateTurn` only when `finish` returned `sent`, no error and `out.status == conloomapi.TaskSucceeded` (a failed reply's error copy is sent too, but `execute` never snapshots a turn on its fail/skip/blocked paths; the status check is a second guard). Every early return of `run` after the hold releases with nil.
- `orchestrator.go`: `states *stateSteps` (initialised in `New`); `cancelActive` calls `cancelStateSteps(convKey, "")`; `Close`: after `bgCancel` mark `states` closed (cancel running, drop pending, refuse new), after `sched.Close` wait for them; `supersede` calls `abandonStateTurn(old.ID)` after a successful `AbandonReplyOf` ingest. `stop`: `cancelStateSteps(conv, "")` for an admin, else the sender's. `memCmdForget`: `cancelStateSteps(conv, sender)` before forgetting. `DeletePanelChat`: after the tasks stopped, cancel the chat's steps and await that channel with the tasks (`awaitStopped`).

- [ ] **Step 1: Write failing tests** (`statestep_test.go`; helpers `isStateRequest(t, req)` = last user message starts with `【状态更新】`, `waitStateIdle(t, env)` polling `env.o.states.idle()` for 5 s; T4's `stepEnv`; the fake answers state requests per test, e.g. calls `mark` then `"随便写点"`)
  - `TestStateStep_RunsAfterReplyWithStateToolsOnly` (private, `recordUsage`): after the reply action is stored a state request follows that offers exactly `[mark]`, keeps `enable_thinking: true`, has the section without `copyReplyStepNote`, its last user message contains the trigger text and ends with `你刚才的回复：\n「` + fakeReply + `」`; `mark` called once with `TaskID`, `ConversationKey`, `PlatformUserID`, `DisplayName`, `TriggerMessageIDs`, `IsAdmin` equal to the reply's `now` invocation; `"随便写点"` sent nowhere (task actions unchanged, no other outbox row); usage: reply calls `chat`, state calls `state`, all with the reply's task ID.
  - `TestStateStep_NoStepWithoutASentReply` (table): `[[SKIP]]`; output blocked by review; both models fail; `CancelTask` while the reply is in the model → `waitStateIdle`, no state request, `mark` never called. Plus a case superseded before the reply is stored (the `CancelTaskID` path, set up as in `TestPrivate_FollowUpBeforeReplyIsStoredSupersedesIt`): exactly one state request, for the new task (`mark` invocation `TaskID` = new task, `TriggerMessageIDs == [m1, m2]`).
  - `TestStateStep_NextReplyPreemptsAndCoalesces` (group, 小明 addresses the bot twice; the first state request blocks on a gate until the second reply is stored): the first state request ends cancelled (`mark` not called), then exactly one more state request whose last user message has both trigger texts and `「` + reply1 + `」\n「` + reply2 + `」`; `mark` invocation `TriggerMessageIDs == [m1, m2]`, `TaskID` = second task.
  - `TestStateStep_PreemptedAfterACallIsNotRepeated`: the first step calls `mark` and blocks in its second round; after the second reply the next step's `TriggerMessageIDs == [m2]` and `mark` was called exactly twice in total (once per step).
  - `TestStateStep_AbandonedReplyCancelsItsStep` (private; reply 1 stored, not handed out; its state request blocks): message 2 abandons reply 1 (`outbox.superseded` audit) → the blocked step is cancelled without a call; the new task's step has `TriggerMessageIDs == [m1, m2]` (each once), `TaskID` = new task; `mark` called once overall.
  - `TestStateStep_PanelChatsShareAScope` (`scopeAutonomyEnv`-style two panel chats of one panel bot, same panel user; the fake counts concurrent state requests): max concurrency 1; two steps, each `mark` invocation with its own `ConversationKey` and triggers.
  - `TestStateStep_GroupSpeakersRunInTurn`: 小明 and 小红 address the bot → two steps, never concurrent, `PlatformUserID` 20001 and 29999 with their own triggers.
  - `TestStateStep_ReRoutesOnModelFailure`: `short_context` calls `mark` in round 0 then answers 500; `long_context` gets a state request carrying the earlier tool message, answers without calls; `mark` called once; log/usage show one failed and one ok `state` call.
  - `TestStateStep_BackgroundAdmission`: `stateTask(task).Priority == int(admission.PriorityBackground)`, other fields equal.
  - `TestStateInvocation_CoalescesTriggersInOrder` and `TestStateDirective_Copy` (pure): newest TaskID/DisplayName, IDs `[a, b, c]` from turns `[a, b]`, `[b, c]`; directive equals the Global Constraints copy exactly for one and two turns.
- [ ] **Step 2: Write failing tests** (`statestep_cancel_test.go`; the state request blocks on a gate)
  - `TestStateStep_CloseCancelsRunningStep`: `env.o.Close()` returns within 2 s; after opening the gate `mark` count stays 0; `states.idle()`.
  - `TestStateStep_CancelledByCommands` (table, private): `/stop`, `/pause` (admin), `/忘记我` while the step blocks → step cancelled, `mark` not called, no further state request.
  - `TestStateStep_ForgetThroughAdminStopsIt`: `memory.Admin.ForgetMember` while the step blocks → after the gate opens no call runs (outcome `forgotten`).
  - `TestStateStep_DeletePanelChatWaitsForIt`: `DeletePanelChat` returns nil only after the chat's step returned; no `mark` call afterwards.
- [ ] **Step 3: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'StateStep|StateInvocation|StateDirective'` → FAIL.
- [ ] **Step 4: Implement** per Interfaces; update docs of `run`, `execute`, `chat`, `recordCall`, `Close`, `cancelActive`, `supersede`, `stop`, `DeletePanelChat`, `memCmdForget`.
- [ ] **Step 5: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ ./internal/app/ ./internal/panel/ ./internal/gateway/` → PASS.
- [ ] **Step 6: Commit** `feat(core): run state tools in a background step after the reply`

### Task 6: Wake turns

**Files:**
- Modify: `internal/core/statestep.go` (`stateDirective`, wake copy), `internal/core/skills.go` (doc of `newSkillRun` :99-106 only)
- Test: `internal/core/statestep_wake_test.go` (new)

**Interfaces:**
- Produces: `const copyStateStepWake = "…"`; `stateDirective` uses it with heading `你刚才发的：` when every turn is a wake turn. No other code: T4 already makes the wake reply tool-less (its `wakeTools` are state tools) and T5 runs the step after `finishWake` stored the message (`sent`). `newSkillRun` doc: in `after_reply` mode a wake reply offers no tools and its state step only `wakeTools`.

- [ ] **Step 1: Write failing tests** (env: `skillEnv` with bot skills `[buff, status, scene]`, `stateTool`s named `adjust_buff`, `add_buff`, `remove_buff`, `set_status`, `set_scene`, plus `now`; profiles with `enable_thinking: true`; a status return armed as in `TestWake_StatusReturnSends`)
  - `TestStateStep_WakeTurnBuffToolsOnly`: the wake reply request has no `tools`, thinking off, the buff section and `copyReplyStepNote`; the sent message is followed by one state request offering exactly `[adjust_buff, add_buff, remove_buff]` (never `set_status`/`set_scene`), thinking on, whose last user message is `copyStateStepWake + "\n\n你刚才发的：\n「" + fakeReply + "」"` (no `【当前消息】`); the `adjust_buff` invocation has empty `TriggerMessageIDs`, the wake name, the wake task ID.
  - `TestStateStep_WithheldWakeHasNoStep`: the person writes before the wake reply is stored (withheld `user_message`) → no state request for the wake task.
  - `TestStateStep_WakeInlineUnchanged`: `inline` → the wake reply offers the three buff tools as today and no state request follows.
- [ ] **Step 2: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/ -run 'Wake'` → FAIL (wake copy).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./internal/core/` → PASS.
- [ ] **Step 5: Commit** `feat(core): state step after wake turns`

### Task 7: Docs and full check

**Files:**
- Modify: `docs/configuration.md` (## skills table :418-424), `docs/skills.md` (## 一次回复里的工具调用 :266, wake paragraph :217, ## 观测 :292, built-in tool list :45), `docs/architecture.md` (5.6 :583-593, 8.1 记录点 :774-785), `docs/panel.md` (模型用量 :285-296)

**Content:**
- `configuration.md`: row `skills.state_step` | `after_reply` | `after_reply`：回复只带查询类工具（`recall`、`get_affinity`、`now` 和 `skills.dir` 技能里没声明的工具）并关掉思考，改状态的工具（`remember`、`set_status`、`set_scene`、`adjust_buff`、`add_buff`、`remove_buff`）在回复存好之后单独跑一步（思考按模型配置）；`inline`：旧行为。其他值启动失败（退出码 78）.
- `skills.md`: lookup vs state tools and `skills.StateChanger`; reply step (full section + the note, only lookups, `thinkingOff` on every request, one request when no lookup); state step: when it runs (sent direct reply that had state tools; not for skip/blocked/failed/cancelled/superseded), what it sees (same system prompt and section, history up to the covered turns, refreshed state block, memory block, `copyStateStep` with the replies), same invocation (coalesced trigger IDs), 3 rounds / 4 calls / 10 s per call / 90 s per step, re-route, background admission, never sends; ordering (one per memory scope, coalescing per scope + speaker, the next reply cancels and carries over unless a call already ran, abandoned replies, /stop /pause /忘记我, chat deletion, forget via API/panel, shutdown; not resumed after a restart); wake turns; observability (`state step` log fields and outcomes, `model call` logs, metrics unchanged, usage `state`).
- `architecture.md` 5.6: the two modes, `newSkillRun` split, `statestep.go` (`stateSteps`, holds, `statePassTimeout`); 8.1: 7 record points, `state` from `internal/core/statestep.go` with the reply's task ID, `estimated_tokens` also for `state`.
- `panel.md`: purpose `state` (标签"状态更新"), has bot, conversation and task.

- [ ] **Step 1: Edit docs**; `grep -rn "收尾请求\|wakeTools\|只提供 .buff\|记录点" docs/*.md` and fix every hit that now misstates behaviour (leave `docs/superpowers/` alone).
- [ ] **Step 2: Full check:** `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...` → PASS; `/usr/local/node/bin/node --check internal/panel/web/lib/format.js`.
- [ ] **Step 3: Commit** `docs: reply step and state step`

---

## Self-review

- **Spec coverage:** classification with an optional interface, external tools are lookups → T1; reply step: lookups only, full skill section + note, thinking off on all reply requests, single request without lookups → T4; state step after a successful stored reply, same eligibility, persona prompt + section + instruction, context with the just-written reply, profile extra_body, same invocation, tool loop machinery with own budget and re-route, sends nothing → T5; no step for cancelled/superseded/skipped/blocked/failed, shutdown and /stop /pause forget delete → T5; ordering: one per scope, coalescing per (scope, speaker), next reply never waits (holds + preemption), abandoned replies (decision 2–4) → T5; wake turns → T4 (reply) + T6 (step, copy); switch, default, docs, example → T2, T7; observability (purpose `state`, `state step` log) → T3, T5, T7; inline byte-identical → T4 tests + unmodified existing suite; background admission → T5.
- **Names consistent:** `skills.StateChanger`, `skills.ChangesState`, `Toolset.Lookups/StateTools` (T1) → T4; `config.StateStepAfterReply/StateStepInline` (T2) → T4; `store.PurposeState` (T3) → T5; `skillRun.replyStep/state`, `newToolRun`, `copyReplyStepNote`, `stateTool`, `stepEnv` (T4) → T5/T6; `stateTurn`, `stateSteps`, `holdState`, `stateDirective`, `isStateRequest`, `waitStateIdle` (T5) → T6.

## Decisions on spec ambiguities

1. The split applies only when the turn's toolset has at least one state tool; otherwise (no skills, lookups only, every external tool) the turn runs exactly as today in both modes, thinking included.
2. The state step's system prompt is the reply's (persona + parts directive + full skill section, without the note), so the prefix matches the reply for prompt caching; the instruction and the reply quotes go in the final user message as its directive, which replaces the reply's directive. The appearance image is left out.
3. Context: the newest turn's snapshot; Current = all covered turns' trigger messages; history ends before the first of them, so stored copies of earlier replies are not repeated; replies are quoted from what was sent (parts joined by newlines). The state block is re-read at step time (`readScene` touching an in-person scene again is harmless) so a previous step's changes are seen.
4. Coalescing key (scope, speaker) decides preemption and the one-at-a-time rule; turns of the same speaker in two panel chats of one scope stay separate pending entries (one per conversation, run one after another), since an invocation has one conversation. At most `stateMaxTurns` (8) newest turns are kept.
5. A preempted or abandoned step that already ran a tool call is not repeated: its turns count as covered (prevents state changes applied twice). Calls of a round already started finish (`context.WithoutCancel`, 10 s each); no call starts after cancellation.
6. Time budget: `statePassTimeout` (90 s) bounds the whole step including admission waits and the re-route; there is no final request without tools and no `restartClock`.
7. A forget through the API or panel goes through `memory.Admin`, outside core: the step re-checks `ForgottenAt` before its first request and before each round. `/stop` by a non-admin cancels only the sender's steps.
8. State steps live in memory: shutdown cancels them, they are not resumed after a restart (the reply was delivered; at most that turn's state change is lost).
9. Usage label `状态更新` is added to the panel (the spec wants the usage page to show the calls; no other panel UI).
