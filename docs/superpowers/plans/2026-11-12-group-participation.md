# 群聊参与 v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 机器人在聊时按轮次、可选择地回应，不在聊时只在群聊自然停顿且条件合适时加入，不再句句都回、不乱插话。

**Architecture:** 进程内“会话”状态（每群一个）取代按人的连续对话窗口；会话中参与者的发言以更长的合并窗口组成“轮”，每轮至多一次模型调用、可 `[[SKIP]]`。观望模式由每群一个“停顿计时器”驱动，停顿时按条件与预算决定是否发起一次加入评估。持久化的资格、档位、兴趣画像、`proactive_log` 与预算沿用 v1。

**Tech Stack:** Go（现有依赖），SQLite（无新迁移）。

**Spec:** `docs/superpowers/specs/2026-11-12-group-participation-design.md`（下称“设计”）；其上位 v1 设计与 P1 设计的约束继续有效。

## Global Constraints

- 不新增依赖；不改 `go.mod`/`go.sum`；不新增数据库迁移。
- 核心包非测试源码不得出现 `onebot|napcat|qwen|deepseek|grok`；日志不记录正文与兴趣词。
- 测试只用假 ID（群 `10001`，用户 `20001`/`20002`/`20003`，管理员 `29999`，Bot `30001`）；`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race ./...` 必须通过。
- 固定提示文案逐字使用设计 §3.4、§4.3；`[[SKIP]]` 任何情况下不进 Outbox。
- 直接触发（@/回复 Bot/命令/私聊）的行为不变：每次都回复。
- 并行规则：只改本任务列出的路径；`git add <显式路径>`；不推送。

## Review Focus

1. 会话里对方连发 5 句 → 只有 1 次模型调用，且回复引用最后一句 —— Task 3 `TestSession_TurnMergesBurstIntoOneCall`。
2. 会话被“连续两轮跳过”或“4 次未点名回复”结束后，对方再说话（不 @）不触发，也不会立刻被观望模式拉回（`join_cooldown`）—— Task 3 `TestSession_EndsAfterSkipsAndLimit`、Task 4 `TestJoin_RespectsJoinCooldownAfterBotSpoke`。
3. 群里只有从未 @ 过 Bot 的人在聊 → 停顿也零调用 —— Task 4 `TestJoin_NoEligibleSpeakerNoCall`。
4. 刷屏 100 条、停顿多次 → 每小时加入评估 ≤ `join_evaluations_per_hour` —— Task 4 `TestJoin_HourlyEvaluationBudget`。
5. 旧策略/配置缺少新字段 → 逐字段补默认值；库里旧 JSON 含 `contact_cooldown` 仍可读 —— Task 1 `TestWithDefaultsFillsNewFieldsIndividually`、Task 4 `TestPolicyJSONWithContactCooldownStillDecodes`。

---

### Task 1: 配置字段与逐字段默认值

**Files:** `pkg/conloomapi/policy.go`, `pkg/conloomapi/*_test.go`, `internal/policy/*`（仅在需要时）, `internal/config/config_test.go`, `conloom.example.yaml`

**Interfaces — Produces:**
- `ReplyPolicy` 新增 `TurnGap, TurnMaxWait Duration`（json/yaml `turn_gap`,`turn_max_wait`）、`MaxUnpromptedReplies, MaxConsecutiveSkips int`（`max_unprompted_replies`,`max_consecutive_skips`）；`ContinuationWindow` 校验放宽为 [0, 10m]。
- `ProactivePolicy` 新增 `JoinLull, JoinWindow, JoinCooldown Duration`（`join_lull`,`join_window`,`join_cooldown`）、`JoinMinMessages, JoinEvaluationsPerHour int`（`join_min_messages`,`join_evaluations_per_hour`）。**本任务不删除** `ContactCooldown`（Task 4 删除）。
- 默认值与校验范围按设计 §6；`turn_max_wait ≥ turn_gap`。
- `GroupPolicy.WithDefaults(def)`：除原有“整段为零则整段补齐”外，对上述 9 个新字段**逐个**补齐零值（`continuation_window` 不补）。`policy.Service` 读出与 `Update` 写入前都已调用 `WithDefaults`（确认即可，必要时补上）。

- [ ] **Step 1: 失败测试**：`TestDefaultGroupPolicyParticipationFields`；`TestValidateParticipationFields`（每个字段两端边界，`turn_max_wait < turn_gap` 报错，`continuation_window` 10m 合法、11m 非法）；`TestWithDefaultsFillsNewFieldsIndividually`（只有新字段为零的旧 JSON 读出后为默认值，其余字段不变）；`TestLoad_ConfigWithoutParticipationFieldsUsesDefaults`。
- [ ] **Step 2–4** → `go test -race ./pkg/... ./internal/policy/... ./internal/config/...`
- [ ] **Step 5: 提交** `feat(policy): add turn-taking and join settings`

### Task 2: 文档（可与 Task 1 并行）

**Files:** `README.md`, `docs/superpowers/specs/2026-11-12-ambient-chat-design.md`（仅在开头加一行“§5 触发方式与 §6.4 已被群聊参与 v2 取代”的说明）

- [ ] 按设计 §2–§7 改写 README 中“识趣退出与主动搭话”相关小节：在聊模式（轮次、结束条件）、观望模式（停顿、条件、预算）、新配置字段与默认值、`continuation_window` 新含义、移除 `contact_cooldown`、成本上界。保持现有结构与口吻，不出现真实 ID。提交 `docs: describe group participation v2`。

### Task 3: 在聊模式（会话与轮次）（依赖 Task 1）

**Files:** Create `internal/core/session.go`, `internal/core/session_test.go`; Modify `internal/core/scheduler.go`（+test）, `internal/core/orchestrator.go`, `internal/core/runner.go`, `internal/core/decide.go`（仅注释/字段名如需）, `internal/core/copy.go`, `internal/core/commands.go`（`/pause` 清空会话）, `internal/app/e2e_test.go`

**Interfaces:**
- Scheduler：`type MergeOptions struct{ Debounce, MaxMergeWindow time.Duration }`；`func (s *Scheduler) AdmitWith(convKey, actorID, messageID string, mode AdmitMode, opts MergeOptions, persist func(AdmitPlan) error) (AdmitResult, error)`（零值字段回落到会话配置）；`Admit` 委托给 `AdmitWith`。该任务创建时的合并参数用于后续并入。删除不再使用的 `MarkReplied`/`InContinuation`/`EndContinuation`（连同测试）。
- `session.go`（`Orchestrator` 上的进程内会话，互斥保护）：`inSession(convKey, user string, now time.Time, idle time.Duration) bool`；`sessionActive(convKey string, now time.Time, idle time.Duration) bool`；`directReplied(convKey, user string, now time.Time)`（加入并重置 `unprompted/skips`）；`startJoinSession(convKey string, users []string, now time.Time)`（`unprompted=1`，Task 4 使用）；`turnReplied(convKey string, now time.Time, maxUnprompted int) (ended bool)`；`turnSkipped(convKey string, maxSkips int) (ended bool)`；`leaveSession(convKey, user, reason string)`；`endSession(convKey, reason string)`；`lastBotSpoke(convKey string) time.Time`（任何 Bot 发言都更新，Task 4 使用）。
- 决策：`DecideInput.InContinuation` 由 `inSession` 计算（字段名可保留）；参与者满足退出规则 → `leaveSession(..., "rule")`。会话轮次以 `MergeOptions{TurnGap, TurnMaxWait}` 入队。
- runner：会话轮次用设计 §3.4 新提示（`CopyDirectiveContinuation` 改为新文案）；回复后 `turnReplied`，跳过后 `turnSkipped`；群聊直接触发回复后 `directReplied`；所有 Bot 发言更新 `lastBotSpoke`。结束原因计入 `ContinuationExits{reason}`：`rule|model|limit|idle`（idle 在检测到过期时计一次）。
- 兴趣画像：会话轮次仍更新兴趣、不刷新资格时间（保持 v1 修复后的行为）。

- [ ] **Step 1: 失败测试**：`TestSession_DirectReplyStartsSession`、`TestSession_TurnMergesBurstIntoOneCall`（5 句一轮、1 次调用、引用最后一句）、`TestSession_NonParticipantDoesNotTrigger`、`TestSession_RuleExitRemovesParticipant`、`TestSession_EndsAfterSkipsAndLimit`、`TestSession_IdleExpires`、`TestSession_DirectMentionResetsUnprompted`、`TestSession_PauseClearsSessions`、`TestScheduler_AdmitWithMergeOptions`；端到端 `TestE2E_TurnTakingDoesNotReplyEverySentence`。
- [ ] **Step 2–4** → `go test -race ./internal/core/... ./internal/app/...`，再全量。
- [ ] **Step 5: 提交** `feat(core): turn-based participation in ongoing conversations`

### Task 4: 观望模式（停顿时加入）（依赖 Task 3）

**Files:** Create `internal/core/join.go`, `internal/core/join_test.go`; Modify `internal/core/orchestrator.go`, `internal/core/proactive.go`, `internal/core/runner.go`, `internal/core/copy.go`, `internal/core/recover.go`, `internal/core/*_test.go`（删除/改写 v1 逐条主动候选的测试）, `internal/app/e2e_test.go`, `pkg/conloomapi/policy.go`（仅删除 `ContactCooldown` 及其默认值/校验/测试）, `conloom.example.yaml`（删除 `contact_cooldown` 行）

**Interfaces:**
- `join.go`：每群一个停顿计时器（`time.AfterFunc`），每条存入的非 Bot 群消息（任何处置结果，除 ignore 外）重置；`Close` 与 `/pause` 停止全部计时器。到点调用 `considerJoin(convKey string)`，按设计 §4.2 的 7 条依次检查（廉价的在前），通过后按 §4.3 选目标、写 `evaluated`、以 trigger `proactive`、后台优先级创建任务（触发消息 = 目标消息）。`func isQuestion(text string) bool` 按 §4.2 第 6 条。
- 删除：`proactiveCandidate`、`submitProactive`、`hasPendingProactive`、联系时间表与 `pruneContacts`、`ContactCooldown` 字段（旧 JSON 中的 `contact_cooldown` 键被忽略即可）。
- runner：proactive 任务用设计 §4.3 新提示（目标人名、目标资格人名单、合并后的前 5 个兴趣词）；发出后 `startJoinSession(窗口内目标资格人)`；发送前复核沿用 `proactiveSendAllowed`。
- 预算计数：加入评估 = `CountProactive(conv, "", evaluated, now−1h)`；其余沿用 v1。

- [ ] **Step 1: 失败测试**：`TestJoin_LullTriggersOneEvaluation`、`TestJoin_NewMessageResetsTimer`、`TestJoin_NoEligibleSpeakerNoCall`、`TestJoin_RequiresMinMessagesAndRelevance`（兴趣匹配或提问二选一）、`TestJoin_SkipsWhileSessionActive`、`TestJoin_RespectsJoinCooldownAfterBotSpoke`、`TestJoin_HourlyEvaluationBudget`、`TestJoin_TargetIsLatestEligibleMessage`、`TestJoin_PausedOrDisabledNoCall`、`TestJoin_SentStartsSessionWithEligibleSpeakers`、`TestIsQuestion`、`TestPolicyJSONWithContactCooldownStillDecodes`；端到端 `TestE2E_JoinAfterLullWithEligibleSpeaker`、`TestE2E_NoJoinWithoutEligibleSpeaker`。
- [ ] **Step 2–4** → `go test -race ./internal/core/... ./internal/app/... ./pkg/...`，再全量。
- [ ] **Step 5: 提交** `feat(core): join group conversations at natural pauses`
