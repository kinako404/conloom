# 氛围聊天（识趣退出 + 主动搭话）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让机器人在连续对话中识趣退出，并对 @ 过它的群友按各自档位、在话题相关时主动搭话。

**Architecture:** 规则先行、模型兜底。连续对话先用纯规则排除（@ 别人 / 回复别人），再由模型以 `[[SKIP]]` 标记拒答；主动搭话由“资格 + 兴趣关键词匹配 + 持久化的发言/评估预算”层层筛选后才调用模型，模型同样可用 `[[SKIP]]` 放弃。新数据进 SQLite 迁移 0002。

**Tech Stack:** Go（现有依赖，不新增）、SQLite（modernc）。

**Spec:** `docs/superpowers/specs/2026-11-12-ambient-chat-design.md`（下称“设计”）；P1 设计 `docs/superpowers/specs/2026-11-12-conloom-p1-design.md` 的全部约束继续有效。

## Global Constraints

- 不新增第三方依赖；不改 `go.mod`/`go.sum`。
- 核心包非测试源码不得出现 `onebot|napcat|qwen|deepseek|grok`；日志不记录正文与兴趣词（用 `obs.MessageAttr` 只记长度）。
- 测试只用假 ID（群 `10001`，用户 `20001`/`20002`，管理员 `29999`，Bot `30001`）；不访问网络；`CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race ./...` 必须通过。
- 长度按 rune 计；数据库时间 UTC；用户可见文案为中文，固定文案逐字使用设计 §6.5、§7。
- 跳过标记固定为 `[[SKIP]]`，任何情况下不得进入 Outbox。
- 并行工作规则：只改自己任务列出的路径；`git add <显式路径>`；遇到 `index.lock` 等待重试；不推送。

## Review Focus

1. 旧群策略（库中第 5 版，无 `proactive` 段）读取后主动搭话按默认值开启而不是零值关闭 —— Task 1 `TestPolicy_CurrentFillsMissingProactiveDefaults`。
2. 模型把 `[[SKIP]]` 夹在句子里（如“好的[[SKIP]]”）—— 整条丢弃，不发送 —— Task 4 `TestRunner_SkipMarkerAnywhereSendsNothing`。
3. 从没 @ 过的人聊到相同话题 —— 零模型调用 —— Task 5 `TestE2E_NeverMentionedUserGetsNoProactive`。
4. 群里刷屏式连发（同一有资格的人 1 分钟 30 条相关消息）—— 评估次数受预算封顶，模型调用不随消息数增长 —— Task 5 `TestProactive_EvaluationBudgetCapsModelCalls`。
5. `/pause` 期间与过时消息 —— 不评估、不主动 —— Task 5 `TestProactive_PausedOrStaleNeverEvaluates`。

---

### Task 1: 公开类型、群策略 `proactive` 段与默认值补齐

**Files:** Modify `pkg/conloomapi/policy.go`, `pkg/conloomapi/types.go`, `pkg/conloomapi/*_test.go`, `internal/policy/policy.go`, `internal/policy/policy_test.go`, `internal/config/config_test.go`, `conloom.example.yaml`

**Interfaces — Produces:**
- `conloomapi.TriggerContinuation = "continuation"`、`conloomapi.TriggerProactive = "proactive"`（与现有 `Trigger*` 常量同组）。
- `TaskMeta.Trigger string \`json:"trigger,omitempty"\``。
- `ProactiveOff/ProactiveLow/ProactiveMid/ProactiveHigh = "off"/"low"/"mid"/"high"`。
- `type ProactiveDailyLimits struct{ Low, Mid, High int }`（json/yaml：`low`,`mid`,`high`）。
- `type ProactivePolicy struct{ Enabled bool; DefaultLevel string; EligibilityTTL, UserCooldown Duration; GroupHourlyLimit int; DailyLimits ProactiveDailyLimits }`（json/yaml：`enabled`,`default_level`,`eligibility_ttl`,`user_cooldown`,`group_hourly_limit`,`daily_limits`）。
- `func (p ProactivePolicy) DailyLimit(level string) int`（off/未知 → 0）。
- `GroupPolicy.Proactive ProactivePolicy`（json/yaml `proactive`）；`DefaultGroupPolicy()` 填入设计 §6.1 默认值；`GroupPolicy.Validate()` 加设计 §6.1 规则。
- `func (p GroupPolicy) WithDefaults(def GroupPolicy) GroupPolicy`：`p.Proactive == ProactivePolicy{}` 时用 `def.Proactive` 替换，其余不变。
- `policy.Service.current` 对库中读出的版本与默认版本都应用 `WithDefaults(s.defaults)`。

- [ ] **Step 1: 失败测试**：`TestDefaultGroupPolicyProactive`（Enabled、low、720h、30m、4、2/4/8）；`TestGroupPolicyValidateProactive`（表驱动覆盖每条规则的边界）；`TestWithDefaultsFillsZeroProactiveOnly`；`TestPolicy_CurrentFillsMissingProactiveDefaults`（直接往库里写一个 `proactive` 缺失的策略 JSON 版本，`Current` 读出 Enabled=true）；`TestLoad_ConfigWithoutProactiveUsesDefaults`（`default_group_policy` 只写 `reply` 段时 Proactive 等于默认值）；`TestLoad_ExampleFileParsesWithEnv` 继续通过。
- [ ] **Step 2: 确认失败** → **Step 3: 实现**（`conloom.example.yaml` 加入设计 §6.1 的 `proactive` 段与中文注释）→ **Step 4: 通过** `go test -race ./pkg/... ./internal/policy/... ./internal/config/...`
- [ ] **Step 5: 提交** `feat(policy): add proactive group policy section`

### Task 2: 存储（迁移 0002、成员表、主动记录）

**Files:** Create `internal/store/sqlite/migrations/0002_ambient.sql`, `internal/store/sqlite/members.go`, `internal/store/sqlite/members_test.go`; Modify `internal/store/store.go`, `internal/store/records.go`

**Interfaces — Produces（`package store`）:**
- `type MemberRecord struct{ ConversationKey, PlatformUserID string; LastEngagedAt time.Time; ProactiveLevel string; LevelSetByUser bool; Interests map[string]float64; UpdatedAt time.Time }`
- `type ProactiveLogEntry struct{ ConversationKey, PlatformUserID, Kind, TaskID string; CreatedAt time.Time }`；`const ProactiveEvaluated = "evaluated"`, `ProactiveSent = "sent"`
- `Store` 接口新增：`GetMember(ctx, convKey, userID string) (MemberRecord, error)`（无则 `ErrNotFound`）；`PutMember(ctx, MemberRecord) error`（按主键 upsert）；`AppendProactiveLog(ctx, ProactiveLogEntry) error`；`CountProactive(ctx, convKey, userID, kind string, since time.Time) (int, error)`（`userID==""` 表示全会话）；`PurgeProactiveLog(ctx, before time.Time) (int, error)`。
- 0002 表结构按设计 §6.2、§6.4；索引 `proactive_log(conversation_key, created_at)`；时间一律 `fmtTime`。

- [ ] **Step 1: 失败测试**：`TestMigration0002Applies`（新库与已有 0001 库都能升级）；`TestMember_GetMissingIsNotFound`；`TestMember_PutGetRoundTrip`（含 Interests JSON）；`TestMember_PutUpserts`；`TestProactiveLog_CountByUserKindAndWindow`；`TestProactiveLog_CountAllUsersWhenEmptyUser`；`TestProactiveLog_Purge`。
- [ ] **Step 2–4** → `go test -race ./internal/store/...`
- [ ] **Step 5: 提交** `feat(store): add member engagement and proactive log`

### Task 3: 兴趣画像包 `internal/interest`

**Files:** Create `internal/interest/interest.go`, `internal/interest/stopwords.go`, `internal/interest/interest_test.go`

**Interfaces — Produces（纯函数，不依赖其他内部包；档位用字符串 `"low"|"mid"|"high"`）:**
- `const MaxTerms = 50`, `Decay = 0.9`
- `func Terms(text string) []string`（设计 §6.3；按首次出现排序，去重）
- `func Update(profile map[string]float64, text string) map[string]float64`（返回新 map，不修改入参）
- `func Top(profile map[string]float64, n int) []string`（权重降序，同权按字典序）
- `func Match(profile map[string]float64, text, level string) (matched []string, ok bool)`（低 N=20 K=2；中 N=30 K=1；高 N=50 K=1；其他档位 false）

- [ ] **Step 1: 失败测试**：`TestTerms_CJKBigramsAndASCIIWords`（“我最近在玩原神和Minecraft” → 含“原神”“minecraft”，不含停用词“我们/最近”类）；`TestTerms_DropsStopwordsAndDedups`；`TestUpdate_DecaysAndCaps`（权重 ×0.9 后 +1；超过 50 只留最高的）；`TestUpdate_DoesNotMutateInput`；`TestTop_OrderAndTies`；`TestMatch_Levels`（同一画像与文本：共享 1 个词时 low=false、mid=true；共享 2 个时 low=true；词在第 40 名时 mid=false、high=true）；`TestMatch_EmptyProfile`。
- [ ] **Step 2–4** → `go test -race ./internal/interest/...`
- [ ] **Step 5: 提交** `feat(interest): add keyword interest profiles`

### Task 4: 识趣退出（依赖 Task 1）

**Files:** Modify `internal/core/decide.go`, `internal/core/scheduler.go`, `internal/core/orchestrator.go`, `internal/core/runner.go`, `internal/core/copy.go`, 对应 `_test.go`; `internal/contextbuilder/build.go`, `internal/contextbuilder/build_test.go`; `internal/obs/metrics.go`, `internal/obs/obs_test.go`

**Interfaces:**
- Consumes：`conloomapi.TriggerContinuation`、`TaskMeta.Trigger`（Task 1）。
- Produces：`DecideInput.MentionsOthers, ReplyToOther bool`；`Decision.EndContinuation bool`；`core.TriggerContinuation` 改为 `= conloomapi.TriggerContinuation`；`func (s *Scheduler) EndContinuation(convKey, actorID string)`；`const SkipMarker = "[[SKIP]]"`；`CopyDirectiveContinuation`（设计 §7 原文）；`contextbuilder.Input.Directive string`（非空时以空行分隔附在当前 user 消息末尾，计入 `BaseTokens`）；`obs.Metrics.ContinuationExits *prometheus.CounterVec`（`conloom_continuation_exits_total{reason}`）。
- 规则：`InContinuation && (MentionsOthers || ReplyToOther)` → observe 且 `EndContinuation=true`（设计 §5.1）。`MentionsOthers` = mentions 中存在 `user` 或 `all` 类型；`ReplyToOther` = `reply_to` 指向库中存在且非 Bot 的消息。
- 新任务创建时 `Meta.Trigger = Decision.Trigger`；runner 完成时保留该字段。
- runner：trigger 为 continuation 时传 `Directive = CopyDirectiveContinuation`；模型输出（`CleanOutput` 后、输出审查前）只要包含 `SkipMarker` → 无动作、`succeeded`、`ReviewResult="skipped"`、`EndContinuation`、`ContinuationExits{reason="model"}`；规则退出记 `reason="rule"`。

- [ ] **Step 1: 失败测试**：`TestDecide_ContinuationEndsWhenMentioningOthers`、`TestDecide_ContinuationEndsWhenReplyingToOther`、`TestDecide_ContinuationStillTriggersOtherwise`；`TestScheduler_EndContinuation`；`TestBuild_DirectiveAppendedToCurrent`（且计入 BaseTokens，角色交替不变）；`TestRunner_ContinuationSkipSendsNothingAndEndsWindow`；`TestRunner_SkipMarkerAnywhereSendsNothing`；`TestRunner_TriggerRecordedInMeta`；`TestSubmit_ContinuationRuleExit`（窗口内 @ 别人 → observe，随后同人普通消息也不再触发）。
- [ ] **Step 2–4** → `go test -race ./internal/core/... ./internal/contextbuilder/... ./internal/obs/...`
- [ ] **Step 5: 提交** `feat(core): leave conversations that are no longer addressed to the bot`

### Task 5: 主动搭话与 `/主动` 命令（依赖 Task 1–4）

**Files:** Modify `internal/core/orchestrator.go`, `internal/core/runner.go`, `internal/core/commands.go`, `internal/core/copy.go`, `internal/core/decide.go`（仅 `KnownCommands`）, `internal/core/recover.go`（维护清理）, 新增 `internal/core/proactive.go` 与 `proactive_test.go`, `internal/obs/metrics.go`, `internal/app/e2e_test.go`

**Interfaces:**
- Consumes：Task 1 的 `ProactivePolicy`/常量，Task 2 的成员与记录 API，Task 3 的 `interest`，Task 4 的 `Directive`/`SkipMarker`/`EndContinuation`。
- Produces：`KnownCommands` 增加 `"主动"`, `"proactive"`；`CopyDirectiveProactive(terms []string) string`、`CopyProactiveStatus`、`CopyProactiveSet`、`CopyProactiveNotEligible = "先 @ 我聊几句，才能开启主动搭话。"`、`CopyProactivePrivateOnly = "主动搭话只在群聊里使用。"`、`CopyProactiveGroupOn/Off`（措辞由实现者定，中文、简短）；`CopyHelp` 加入 `/主动`；`obs.Metrics.Proactive *prometheus.CounterVec`（`conloom_proactive_total{result=evaluated|sent|skipped}`）。
- 行为：设计 §6.2（成员更新只在群聊、trigger ∈ mention/reply/continuation、非命令时）、§6.4（候选筛选 1–7 全部满足才写 `evaluated` 并以 trigger `proactive` 走 `Admit(AdmitNew)`）、§6.5（命令；档位中文别名 关/低/中/高 与英文等价；`全群`/`group` 子命令仅管理员，通过 `Policies.Update` 生成新版本）、§7（主动提示带前 5 个兴趣词）。runner 对 proactive 任务：成功发出 → 写 `sent`、`Proactive{sent}`、`MarkReplied`；`[[SKIP]]` → `Proactive{skipped}`。维护循环每小时清理 7 天前的 `proactive_log`。

- [ ] **Step 1: 失败测试**（`proactive_test.go`）：`TestEngagement_MentionCreatesMemberWithDefaultLevel`、`TestEngagement_UserSetLevelPreserved`、`TestEngagement_CommandsDoNotCount`；`TestProactive_MatchingTopicCreatesTask`、`TestProactive_NotEligibleExpiredOrOff`、`TestProactive_FiltersMentionsRepliesCommandsShortText`、`TestProactive_DailyCooldownAndGroupLimits`、`TestProactive_EvaluationBudgetCapsModelCalls`、`TestProactive_PausedOrStaleNeverEvaluates`、`TestProactive_GroupSwitchOff`、`TestProactive_SkipRecordsNoSent`；`TestCommand_ProactiveShowSetAndPermissions`（含未获资格、私聊、非管理员设全群）。`internal/app/e2e_test.go`：`TestE2E_ProactiveReplyForEligibleUser`、`TestE2E_NeverMentionedUserGetsNoProactive`、`TestE2E_ContinuationExitWhenTalkingToOthers`。
- [ ] **Step 2–4** → `go test -race ./internal/core/... ./internal/app/... ./internal/obs/...`，再跑全量。
- [ ] **Step 5: 提交** `feat(core): proactive chat for members who engaged the bot`

### Task 6: README（可与 Task 1–3 并行）

**Files:** Modify `README.md`

- [ ] 在功能、命令、配置、群策略 API 各节加入设计 §5–§6 的行为说明：识趣退出、主动搭话的资格/档位/上限/总开关、`/主动` 命令表、`proactive` 配置段、新指标；不出现真实 ID。提交 `docs: document ambient chat`。
