# 人设内容规则 + 私聊节奏 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 两项改动：
- **A**：人设新增 `adult`、`content_rules`、`memory_rules`，开放模式下的规则全部来自配置；保护模式是代码里的固定底线。
- **B**：私聊等对方停顿再回；回复可以分条、分条按时间间隔投递；对方中途补话时放弃当前回复重新组织。

**Architecture:**
- **A**：
  - 受保护名单和模式判定放在 `pkg/conloomapi`；
  - `persona.SystemPrompt` 按模式注入规则，`Prompt.Mode` 交给 runner；
  - review 新增 `OutputProtected`，带内置词表；
  - 记忆整理第 6 条按模式选用。
- **B**：
  - 策略新增 `reply.max_parts`、`reply.private_wait`、`reply.private_max_wait`；
  - 发件箱动作新增"最早发送时间"和分条序号；
  - 调度器在私聊里支持"取消正在跑的任务并合并"；
  - runner 把模型输出按 `|||` 切成多个动作；
  - Telegram 在分条之间发送"正在输入"。

**Tech Stack:** Go 1.26、modernc SQLite、现有测试辅助（`internal/testutil/fakeopenai` 等）。

**Specs:**
- `docs/superpowers/specs/2026-11-14-persona-content-rules-design.md`
- `docs/superpowers/specs/2026-11-14-private-chat-rhythm-design.md`（含 §7 补充决定）

## Global Constraints

- 不新增依赖；`CGO_ENABLED=0` 可编译；全量 `go test -race`、`go vet`、`gofmt -l .` 干净。
- 公开仓库：测试只用假数据；内置色情词表以 base64 存放，源码和测试中不出现明文露骨词。测试可以把词表里的条目解码后使用。
- 代码里不写死任何角色名。人设新增 `minor`，与 `adult` 互斥；开放模式的条件：`adult && !minor && convType == "private"`，三者缺一不可（spec §8）。
- `max_parts` 取 1 到 5，默认 1。`private_wait` 默认 6s，范围 0 到 60s。`private_max_wait` 默认 20s，范围从 `private_wait` 到 2m。
- 分条分隔符：单独一行的 `|||`。分条间隔为 `clamp(800ms + 60ms × 段的字数, 1s, 4s)`，从上一段起累计。
- `max_parts=1` 时，群聊和私聊的回复形态与现在一致。
- 新迁移编号：`0010`。

## Review Focus

1. **未成年人设经任何路径进入开放模式**：配置、PUT、套模板、回滚、库里的旧数据。由 A1 和 A2 的测试覆盖。
2. **取消与完成的竞态**：可能导致重复回复或丢消息。由 B3 的测试覆盖。
3. **分条与"正在输入"阻塞其他会话**。由 B2 和 B5 的测试覆盖。
4. **词表误伤日常聊天，或被空格、全角绕过**。由 A3 的测试覆盖。
5. **省略第 6 条后，第 3、11 条还留着指向第 6 条的引用**。由 A4 的测试覆盖。

## 分批与分支

- 集成分支 `feat/rhythm-rules`，从 main 切出。每个任务在 `.worktrees/rr-<id>` 上开发，分支名 `feat/rr-<id>`。
- **第 1 批**（并行）：A1、B1、B2。
- **第 2 批**（并行）：A2、A3、A4、A5、B3、B4、B5。
- **第 3 批**：A6、B6。

---

### Task A1：内容字段、未成年标记与校验

**Files:**
- `pkg/conloomapi/persona.go`：新增字段、长度上限、`Validate` 增加长度检查
- 新建 `pkg/conloomapi/content.go`
- `internal/persona/persona.go`：`Update` 的三条路径，包括回滚
- `internal/config/validate.go`
- 测试：`pkg/conloomapi/content_test.go`、`internal/persona/persona_test.go`、`internal/config/validate_test.go`

**Produces：**

```go
// PersonaDoc 新字段
Adult        bool   `json:"adult,omitempty" yaml:"adult,omitempty"`
Minor        bool   `json:"minor,omitempty" yaml:"minor,omitempty"`
ContentRules string `json:"content_rules,omitempty" yaml:"content_rules,omitempty"`
MemoryRules  string `json:"memory_rules,omitempty" yaml:"memory_rules,omitempty"`
const MaxContentRulesRunes = 2000
const MaxMemoryRulesRunes = 1000

// content.go
type ContentMode string
const (
	ContentProtected ContentMode = "protected"
	ContentOpen      ContentMode = "open"
)
// ModeFor：只有 doc.Adult && !doc.Minor && convType == ConvPrivate 时返回 ContentOpen。
func ModeFor(doc PersonaDoc, convType string) ContentMode
// ValidateContent：Adult 与 Minor 同时为 true 时报错，报错文字："a minor persona cannot be adult"。
func ValidateContent(doc PersonaDoc) error
```

- [ ] **测试**：
  - `TestModeFor_Table`：adult × minor × 群聊/私聊 共 8 种组合中，只有一种是开放模式。
  - 代码和测试里不出现任何具体角色名（用"角色甲"之类的假名）。
  - `TestValidate_ContentRulesLength`：两个字段超长都报错。
  - `TestUpdate_RejectsMinorAdult`：整份替换、套模板、回滚到一个被手动写成 `minor: true, adult: true` 的版本，三种情况都返回 `persona.ErrInvalid`。
  - `TestValidateConfig_MinorTemplateAdult`：配置里某个模板同时设了 `minor` 和 `adult`，校验报错。
- [ ] 先跑测试，确认失败；再实现，确认通过；然后提交：`feat(persona): adult flag, content rules and the protected characters`。

### Task B1：节奏相关的策略字段

**Files:** `pkg/conloomapi/policy.go`（`ReplyPolicy`、默认值、`WithDefaults`、`Validate`），测试 `pkg/conloomapi/policy_test.go`。

**Produces：** `ReplyPolicy` 新增三个字段：
- `MaxParts int`，键名 `max_parts`；
- `PrivateWait Duration`，键名 `private_wait`；
- `PrivateMaxWait Duration`，键名 `private_max_wait`。

默认值按 Global Constraints。`WithDefaults` 对旧版本的策略补齐零值，做法与 `TurnGap` 一样。

- [ ] **测试**：
  - 默认值正确；
  - 存过的旧策略（没有这三个字段）补齐默认值；
  - 校验边界：`max_parts` 为 0、1、5、6；`private_wait` 为 -1、0、60s、61s；`private_max_wait` 小于 `private_wait`、大于 2m。
- [ ] 提交：`feat(policy): max_parts and private reply timing`。

### Task B2：发件箱的最早发送时间、分条序号和取消

**Files:**
- 新建 `internal/store/sqlite/migrations/0010_outbox_not_before.sql`
- `internal/store/records.go`：`OutboxRecord.NotBefore`
- `pkg/conloomapi/types.go`：`Action` 新增 `PartIndex`、`PartCount`
- `internal/store/sqlite/tasks.go`：`insertOutbox` 写入新列
- `internal/store/sqlite/outbox.go`：`LeaseOutbox` 和新方法
- `internal/store/sqlite/cleanup.go`：`PurgeOutbox` 同时清理已取消的动作
- `internal/store/store.go`：接口新增方法
- 测试：`internal/store/sqlite/outbox_test.go`

**Produces：**

```go
// OutboxRecord
NotBefore time.Time // 零值表示立即可发
// Action
PartIndex int `json:"part_index,omitempty"` // 从 1 开始
PartCount int `json:"part_count,omitempty"`
// store.Store
// CancelPendingActions：把 taskID 下所有 status=pending（未租出）的动作标为 cancelled，返回条数。
CancelPendingActions(ctx context.Context, taskID string, now time.Time) (int, error)
```

`LeaseOutbox` 的规则：每个会话的队首动作要等到 `not_before <= now` 才能租出，在那之前它挡住该会话后面的所有动作。其他会话不受影响。

- [ ] **测试**：
  - `TestLeaseOutbox_NotBeforeBlocksHeadOnly`：A 会话的队首还没到时间，A 一条都租不出；B 会话照常租出。
  - `TestCancelPendingActions`：只取消 pending 的动作，leased 和 acked 的不动；被取消的不会再被租出。
  - `TestPurgeOutbox_IncludesCancelled`。
  - 迁移之前写入的动作（`not_before` 为空）立即可发。
- [ ] 提交：`feat(outbox): not-before time, part numbers and cancelling pending actions`。

### Task A2：回复提示词按模式注入，`core.md` 改写

**Files:** `internal/persona/core.md`、`internal/persona/persona.go`（`SystemPrompt`、`Prompt`、缓存）、新建 `internal/persona/content.go`（固定保护段常量），测试 `internal/persona/persona_test.go`。

**Consumes：** A1 的 `conloomapi.ModeFor` 和 `PersonaDoc` 新字段。

**Produces：** `Prompt` 新增 `Mode conloomapi.ContentMode` 字段。

**`core.md` 的改动：**
- 第 6 条删掉"不参与色情或露骨内容。"。
- 第 7 条的"普通聊天只发一条纯文本消息"改为"普通聊天发纯文本消息（分几条发由系统另行说明）"，其余文字不变。

**`SystemPrompt` 的改动：**
- 从 `convKey`（格式 `bot:platform:type:id`）解析出会话类型；
- 用当前人设版本的 `Persona` 和会话类型，通过 `conloomapi.ModeFor` 计算模式；
- 在 core 之后、人设渲染之前插入：保护模式插入 `protectionRules` 常量，开放模式插入 `ContentRules`（为空就不插）；
- 缓存键加上模式。

`protectionRules` 由实施者撰写，意思不能弱于原第 6 条加某未成年角色人设里的底线：不参与色情、性暗示、露骨内容和恋爱暧昧；用角色的方式岔开话题；面对未成年或孩童化角色时尤其如此。

- [ ] **测试**：
  - 保护模式的提示词包含 `protectionRules`，不包含 `ContentRules`；
  - 开放模式包含 `ContentRules`，不包含 `protectionRules`；`ContentRules` 为空时两者都不出现；
  - 群聊里即使是成年人设也是保护模式；
  - 库里存了 `minor: true, adult: true`（绕过校验写入）的版本，仍是保护模式；
  - 按 `core.md` 的新文字更新相关断言（`persona_test.go:54,60`）。
- [ ] 提交：`feat(persona): content rules by mode in the system prompt`。

### Task A3：保护模式的输出词表

**Files:** 新建 `internal/review/protected.go`、新建 `internal/review/protected_words.b64`（用 `go:embed` 打包，一行一个 base64 编码的词），测试 `internal/review/protected_test.go`。

**Produces：**

```go
const ReasonProtectedContent = "protected_content"
// OutputProtected：先做词表检查，命中就拦截；没命中再走普通的 Output 流程。
func (r *Reviewer) OutputProtected(text string, pol conloomapi.ReviewPolicy) Result
```

- 词表检查不受 `pol` 里任何字段影响，命中一律拦截。
- 匹配前的归一化：NFKC、转小写、去掉空白和常见分隔符（`·`、`.`、`_`、`-`、`*`、零宽字符）。
- 词表只收明确的露骨用语，中英各若干个；具体内容由实施者选，以不误伤日常聊天为准。

- [ ] **测试**：
  - 词表里的每个词，以及加空格、全角、大小写混写的变体，都被拦截，原因值正确；
  - 30 句日常聊天句子（吃饭、天气、游戏、工作、身体不舒服、医院、宠物等）都不被拦截；
  - `Output` 的行为不变；
  - 词表文件里只有合法 base64，`git grep` 能看到的源码里没有明文露骨词。
- [ ] 提交：`feat(review): protected-mode lexicon block`。

### Task A4：记忆整理第 6 条按模式选用

**Files:** `internal/memory/cons_prompt.go`、`internal/memory/consolidate.go`（取人设版本、计算模式），测试 `internal/memory/private_test.go`、`consolidate_test.go`。

**Consumes：** A1 的 `conloomapi.ModeFor`。

- 保护模式：第 6 条用原文。
- 开放模式：`MemoryRules` 不为空，就作为第 6 条；为空则去掉第 6 条，第 3 条中"按第 6 条处理"的说法和第 11 条中"第 5、6 条优先"相应改写，不能留下悬空的引用。具体措辞由实施者定。
- 人设版本通过现有的 `Personas`（`consolidate.go:78`）获取，`botName()` 已经在做类似的调用，可以参照。

- [ ] **测试**：
  - 保护模式下，群聊和私聊的提示词与原来逐字相同，现有的快照测试全部通过；
  - 开放模式且 `MemoryRules` 不为空时，第 6 条换成 `MemoryRules`；
  - 开放模式且 `MemoryRules` 为空时，没有第 6 条，提示词里也没有出现"第 6 条"或"5、6"这样的引用。
- [ ] 提交：`feat(memory): consolidation rule 6 by content mode`。

### Task A5：面板的人设表单与相关文档

**Files:** `internal/panel/readviews.go`（`GET …/persona` 新增 `minor`、`content_mode` 两个字段，`content_mode` 按私聊计算；人设表单新增"未成年角色"开关，勾选后"成年人设"开关不可用）、`internal/panel/web/views/conversation.js`（人设表单）、`docs/configuration.md`、`docs/panel.md`、`docs/architecture.md`、`docs/usage.md`、`conloom.example.yaml`（字段示例，`content_rules` 用中性占位文字）。

**Consumes：** A1。

- [ ] **测试**：
  - `GET persona`：`minor` 原样返回；成年、非未成年的人设返回 `content_mode: open`；
  - PUT 同时设 `minor` 和 `adult` 返回 400；
  - 前端：`web_test.go` 照常通过，并用 `node --check` 检查改过的文件。
- [ ] 提交：`feat(panel): edit adult flag and content rules; docs`。

### Task B3：私聊等停顿、按任务限流、打断重来

**Files:** `internal/core/orchestrator.go`（私聊入队路径、限流）、`internal/core/scheduler.go`（取消正在运行的任务并合并），以及新增的测试 `internal/core/rhythm_test.go`。

**Consumes：** B1 的新字段，B2 的 `CancelPendingActions`。

**私聊入队：**
- `MergeOptions{Debounce: pol.Reply.PrivateWait, MaxMergeWindow: pol.Reply.PrivateMaxWait, Addressed: …}`；
- 限流改为与群聊相同的 `allowNew` 方式，只有新建任务才扣额度。

**打断重来**（私聊、非命令消息）：
1. 同一用户有正在运行、还没完成（没写出动作）的任务：取消它，新任务的触发消息为"旧任务的触发消息 + 新消息"，重新等停顿。旧任务的状态记为已取消，不写出任何动作。
2. 该会话最近一个已完成的任务还有未租出的动作：调用 `CancelPendingActions`，写一条审计 `outbox.superseded`，detail 为 `{task_id, cancelled}`。新消息按正常流程入队。
3. 取消与完成的竞态以写库事务为准：
   - 任务已写出动作，就走第 2 条；
   - 还没写出，就走第 1 条；
   - 绝不能两条都走，也不能两条都不走。

- [ ] **测试**（策略里把 `private_wait` 设为几十毫秒，用假模型，可以控制模型响应的时间）：
  - 间隔 3 个"停顿单位"内连发 3 条：只产生 1 个任务、1 次回复，当前消息包含全部 3 条，限流只扣 1 次；
  - 生成中途补话：旧任务被取消、没有动作；新任务的触发消息是两者之和；最终只回复 1 次；
  - 已有 3 段待发、第 1 段已发出时补话：第 2、3 段被取消，并有审计；
  - 竞态测试：用钩子在完成前后注入新消息，断言不重复、不丢消息；
  - 管理员命令不参与合并和取消；
  - 群聊行为不变，现有测试全部通过。
- [ ] 提交：`feat(core): private chats wait for a pause and restart when interrupted`。

### Task B4：runner 分条

**Files:** `internal/core/runner.go`（追加分条说明、切分、生成多个动作）、`internal/core/orchestrator.go`（`newAction` 支持分条序号和最早发送时间），新建 `internal/core/parts.go`，测试 `internal/core/parts_test.go`。

**Consumes：** B1 的 `MaxParts`；B2 的 `NotBefore`、`PartIndex`、`PartCount`。

**Produces：**

```go
// splitParts：按单独一行的 "|||" 切分，去掉首尾空白，丢弃空段；
// 超过 max 段的部分并入最后一段（用换行连接）；max<=1 时整段作为一段返回。
func splitParts(text string, max int) []string
// partDelay：clamp(800ms + 60ms*runes, 1s, 4s)
func partDelay(part string) time.Duration
```

**分条说明**，在 `directive()` 附近追加到发给模型的指示里：
- `max_parts == 1`：`回复只发一条消息。`
- `max_parts > 1`：`回复可以像聊天那样分成 1 到 N 条短消息，条与条之间用单独一行的 ||| 分隔；不需要分时就只发一条。`

**处理顺序**：
1. 输出审查对整段做一次，审查之后再切分；
2. 每段生成一个动作，带 `PartIndex`、`PartCount`；
3. 第 i 段的 `NotBefore = 完成时间 + 前面各段的 partDelay 之和`，第 1 段没有延迟；
4. `reply_to` 只放在第 1 段。

- [ ] **测试**：
  - `splitParts` 的表格测试：正常切分、超出上限并入最后一段、空段丢弃、没有分隔符、分隔符前后有空白；
  - `partDelay` 的边界；
  - runner 在 `max_parts=3`、模型输出 4 段时生成 3 个动作，序号和时间正确；
  - 整段被审查拦截时，不生成任何动作；
  - `max_parts=1` 时只生成 1 个动作，现有测试全部通过。
- [ ] 提交：`feat(core): split replies into parts with paced delivery`。

### Task B5：Telegram 在分条之间发送"正在输入"

**Files:** `internal/adapter/telegram/send.go` 和 `typing.go`（发送后的处理），测试 `internal/adapter/telegram/deliver_test.go`。

- 发出 `PartIndex < PartCount` 的动作之后，发送一次 `sendChatAction typing`。不阻塞，失败只记日志。
- 其他行为不变。NapCat 不需要改。

- [ ] **测试**：
  - 第 1/3 段发出后有一次 typing；第 3/3 段之后没有；
  - 没有序号的动作，行为与现在相同。
- [ ] 提交：`feat(telegram): typing between reply parts`。

### Task A6：runner 在保护模式下使用词表，端到端测试

**Files:** `internal/core/runner.go`（`prompt.Mode == ContentProtected` 时调用 `OutputProtected`，否则调用 `Output`），新建 `internal/app/content_e2e_test.go`。

**Consumes：** A2 的 `Prompt.Mode`、A3 的 `OutputProtected`。

- [ ] **端到端测试**（假模型返回词表里的一个词）：
  - 未成年人设的私聊：被拦截，有审计；
  - 成年人设的私聊：正常发出；
  - 成年人设的群聊：被拦截；
  - 开放模式下，系统提示词里出现 `content_rules`。
- [ ] 提交：`feat(core): protected mode blocks explicit replies`。

### Task B6：面板策略表单、文档和端到端测试

**Files:** `internal/panel/web/views/conversation.js`（策略表单新增 `max_parts`、`private_wait`、`private_max_wait`）、`docs/configuration.md`（策略表格）、`docs/architecture.md`（私聊时序）、`docs/usage.md`、`docs/telegram.md`、`conloom.example.yaml`（`default_private_policy.reply` 的示例），新建 `internal/app/rhythm_e2e_test.go`。

- [ ] **端到端测试**（mock 适配器加假模型，`private_wait` 很短）：
  - 连发 3 条，只回复 1 次；
  - `max_parts=3` 时，适配器按顺序收到 3 个动作，第 2、3 个在到时间之前租不出来；
  - 生成中途补话，只回复 1 次，回复包含后补的内容。
- [ ] 提交：`feat: private chat rhythm in the panel and docs`。

## 上线（主代理执行）

1. 备份。
2. 部署。
3. 线上配置：
   - `default_private_policy.reply.max_parts: 3`；
   - 成年模板 模板加上 `adult: true`，规则字段留空；未成年模板、孩童化模板 模板加上 `minor: true`，并在已有会话上重新套用模板，让库里的版本也带上这个标记。
4. 重启。
5. 实测：
   - 面板显示某未成年角色会话是未成年、保护模式；
   - 私聊连发三句只回复一次，并且分条发出、中间有"正在输入"；
   - 中途补话会放弃当前回复，重新组织。
