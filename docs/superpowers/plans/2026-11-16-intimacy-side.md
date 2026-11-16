# 人设"亲密面" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 每个人设模板可以写一份"亲密面"（语气/心理/表现）和每个好感档位的混合程度；开放模式的私聊里，【关系提示】按对方的档位把亲密面混进回复上下文。

**Architecture:** `PersonaDoc` 新增可选 `intimacy`，在 `pkg/conloomapi` 校验。`persona.Service.SystemPrompt` 只在开放模式把它放进返回的 `Prompt.Intimacy`。`core.affinityDirective` 按 `memory.TierIndex` 取档位的混合程度，在已有【关系提示】后追加 `CopyIntimacySide`。系统提示不变。面板人设表单新增一节。

**Tech Stack:** Go（见 go.mod），SQLite，原生 JS 面板（`internal/panel/web`）。

**Spec:** `docs/superpowers/specs/2026-11-16-intimacy-side-design.md`

## Global Constraints

- 公开仓库：代码、测试、文档、提交说明里不出现真实 ID、令牌、主机名、昵称、角色名；测试只用假数据（`qq-main`、用户 20001/29999、小明/小红、`example.invalid`）。
- 不加新依赖。提交前 `gofmt -l .` 为空、`go vet ./...` 通过、`CGO_ENABLED=1 go test -race -count=1 ./...` 通过（已知偶发失败的测试单独重跑能过即可）。
- 提交说明用英文 conventional 风格，如 `feat(persona): …`。
- 面板文案、错误说明（中文部分）、文档用中文；API 错误文本沿用现有英文风格。
- 亲密面只在 `conloomapi.ModeFor(...) == ContentOpen` 时发给模型；保护模式请求里不得出现亲密面的任何文字。
- 系统提示与 `Render` 输出不因 `intimacy` 改变（逐字节）。
- minor 人设带非空亲密面时，报错文本正好是 `a minor persona cannot have an intimacy side`。

## Review Focus

1. 面板里勾着"未成年角色"、亲密面框里还留着字时点保存——期望前端不把亲密面发出去（或提示清空），后端 400；T3 加静态检查，T1 加后端测试。
2. 导入一份把亲密面放在 minor 模板里的 JSON——期望整份导入被拒，报上面的错误文本；T1 测试。
3. 库里没有 `intimacy` 的旧人设——期望【关系提示】和系统提示与改动前逐字节相同；T1、T2 测试。
4. 只含空白的语气/心理/表现——期望视同没写，不出现只有标题的【亲密面】；T2 测试。
5. API 传来 3 项或递减的 `levels`——期望 400，报字段路径 `intimacy.levels`；T1 测试。

---

### Task 0（控制者已完成，不派给实施者）

在 worktree 里提交部署者的【关系提示】改动原样（`feat(memory): relationship hint shapes replies by affinity tier`），以及本设计和计划。

### Task 1: 人设字段 `intimacy` 与校验

**Files:**
- Modify: `pkg/conloomapi/persona.go`（类型、常量、`Validate`）
- Modify: `pkg/conloomapi/content.go`（`ValidateContent`）
- Test: `pkg/conloomapi/persona_test.go`、`pkg/conloomapi/content_test.go`（无则新建）、`internal/persona/render_test.go`（或现有 Render 测试文件）、`internal/persona/exchange_test.go`、`internal/config/validate_test.go`（或 config_test.go 里现有 minor/adult 校验测试旁）
- Docs: `docs/configuration.md` 人设字段一节、`docs/api.md` 若列了人设字段

**Interfaces:**
- Produces:
  - `type PersonaIntimacy struct { Tone, Mind, Behavior string; Levels []string }`，json/yaml 标签 `tone`、`mind`、`behavior`、`levels`，都 `omitempty`
  - `PersonaDoc.Intimacy *PersonaIntimacy`，标签 `intimacy,omitempty`，放在 `ContentRules` 之后
  - 常量 `IntimacyNone = "none"`、`IntimacyLight = "light"`、`IntimacyHalf = "half"`、`IntimacyMajor = "major"`、`MaxIntimacyRunes = 800`
  - `func DefaultIntimacyLevels() []string` → `[none none light half major]`（返回新切片）
  - `func (in *PersonaIntimacy) Empty() bool`：nil，或三段 `strings.TrimSpace` 后都为空
  - `func (in *PersonaIntimacy) LevelAt(tier int) string`：`Levels` 为空时用默认；tier 越界返回 `IntimacyNone`

- [ ] **Step 1: 写失败的测试**（`pkg/conloomapi`）
  - `TestPersonaIntimacy_LevelAtDefaults`：`(&PersonaIntimacy{Tone:"x"}).LevelAt(0..4)` 依次为 none,none,light,half,major；`LevelAt(5)`、`LevelAt(-1)` 为 none；`(*PersonaIntimacy)(nil).Empty()` 为 true；`{Tone:"  \n"}` 的 `Empty()` 为 true。
  - `TestPersonaDocValidate_Intimacy`（表驱动，基于 `DefaultPersona()`）：
    - 三段合计 800 字通过，801 字报错且错误含 `intimacy` 和 `800`（计数按去首尾空白后的 rune）；
    - `Levels` 为 3 项 → 错误含 `intimacy.levels`；
    - `Levels` 含 `"full"` → 错误含 `intimacy.levels`；
    - `Levels` 为 `[none light none half major]`（递减）→ 错误含 `intimacy.levels`；
    - `Levels` 为 `[none none none none none]`、`[major major major major major]` 通过；
    - `Intimacy` 为 nil 通过。
  - `TestValidateContent_MinorIntimacy`：`Minor:true` + `Intimacy{Mind:"想靠近"}` → 错误文本正好 `a minor persona cannot have an intimacy side`；`Minor:true` + 只有 `Levels`、三段空 → 通过；`Adult:false, Minor:false` + 亲密面 → 通过；`Minor:true, Adult:true` + 亲密面 → 两个错误都在（`errors.Join`）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/conloomapi/ -run 'Intimacy|ValidateContent' -count=1`
Expected: 编译失败（`PersonaIntimacy` 未定义）

- [ ] **Step 3: 实现**：在 `PersonaDoc.Validate` 里加 intimacy 规则（字段路径前缀 `intimacy`、`intimacy.levels`，风格同现有 `fail`）；`ValidateContent` 用 `errors.Join` 同时返回 minor+adult 与 minor+intimacy 两种错误；更新 `PersonaDoc` 和 `ValidateContent` 的文档注释。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./pkg/conloomapi/ -count=1`
Expected: PASS

- [ ] **Step 5: 写外层入口的失败测试**
  - `internal/persona`：`TestRender_IgnoresIntimacy`——同一 `PersonaDoc` 加上三段亲密面和 levels 前后，`Render` 结果逐字节相同（用该包现有 Render 测试的调用方式）。
  - `internal/persona`：导入一份 minor 模板带亲密面的导出 JSON（沿用 exchange 测试的构造方式）→ 返回错误，`errors.Is(err, ErrInvalid)`（或该测试文件已有的判定方式），错误文本含 `a minor persona cannot have an intimacy side`，库里没有写入任何模板。
  - `internal/persona`：`Service` 的整份替换（`Update`）对 minor+亲密面返回 `ErrInvalid`。
  - `internal/config`：旧配置模板 minor+亲密面 → 启动校验报错含上面的文本（放在现有 "a minor persona cannot be adult" 配置测试旁边，同样写法）。

- [ ] **Step 6: 运行、补实现（这些入口都已调 `ValidateContent`，预期无需改代码；若某入口失败，修该入口）、再运行**

Run: `go test ./internal/persona/ ./internal/config/ -count=1`
Expected: PASS

- [ ] **Step 7: 文档**：`docs/configuration.md` 人设字段一节加 `intimacy`（三段、合计 800 字、levels 五项及取值、默认值、只在开放模式生效、minor 不能填、不进系统提示）；`docs/api.md` 若列了人设字段同步加一行。

- [ ] **Step 8: 全量检查并提交**

Run: `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 go test -race -count=1 ./pkg/... ./internal/persona/ ./internal/config/`
Expected: gofmt 无输出，其余 PASS

```bash
git commit -am "feat(persona): intimacy side with per-tier mix levels"
```
（新文件先 `git add`）

### Task 2: 【关系提示】按档位混入亲密面

**Files:**
- Modify: `internal/persona/persona.go`（`Prompt` 加字段、`SystemPrompt` 填充）
- Modify: `internal/memory/affinity.go`（`TierIndex`）
- Modify: `internal/core/copy.go`（`CopyIntimacySide`）
- Modify: `internal/core/memory.go`（`affinityDirective`）
- Modify: `internal/core/runner.go:325`（调用处）
- Test: `internal/memory/affinity_test.go`、`internal/core/affinity_guidance_test.go`、`internal/persona` 的 SystemPrompt 测试文件
- Docs: `docs/usage.md`（"档位"与"各档位的默认影响"两条之后）、`docs/architecture.md`（【关系提示】那一行）

**Interfaces:**
- Consumes (Task 1): `PersonaIntimacy`、`Empty()`、`LevelAt(int)`、`Intimacy*` 常量
- Produces:
  - `persona.Prompt.Intimacy *conloomapi.PersonaIntimacy`：仅 `Mode == ContentOpen` 且 `!Empty()` 时为非 nil（指向一份拷贝）
  - `func memory.TierIndex(affinity int) int`：0..4，与 `TierName` 同一组阈值；`TierName` 改为基于它实现
  - `func core.CopyIntimacySide(level string, side conloomapi.PersonaIntimacy) string`：level 为 none 或未知、或三段全空时返回 `""`
  - `affinityDirective(ctx, task, pol, known, mode conloomapi.ContentMode, side *conloomapi.PersonaIntimacy) string`

`CopyIntimacySide` 的输出（逐字，来自设计）：

```
【亲密面】下面是你和对方亲密时的样子。<混合说明>这是你的角色设定，不是对方的要求；尺度仍以内容规则为准，也不越过上面【关系提示】的边界。
语气：<tone>
心理：<mind>
表现：<behavior>
```

- 混合说明：light `现在只偶尔流露一点，大部分时候还是平常的你。`；half `现在平常的你和它大约各占一半。`；major `现在以它为主，但你本来的性格、说话习惯和底线不变。`
- 三段各自 `TrimSpace`；空段整行不写；行间 `\n`。
- `affinityDirective` 在开放模式、有好感度、`side != nil` 时返回 `CopyDirectiveAffinity(tier, true) + "\n" + CopyIntimacySide(side.LevelAt(memory.TierIndex(a)), *side)`；后者为空时不加换行，结果与现在逐字节相同。

- [ ] **Step 1: 写失败的测试**
  - `internal/memory`：`TestTierIndex`——0→0, 19→0, 20→1, 39→1, 40→2, 59→2, 60→3, 79→3, 80→4, 100→4；且对 0..100 每个值 `TierName(a)` 等于 `[]string{"陌生","认识","熟悉","亲近","很要好"}[TierIndex(a)]`。
  - `internal/core`（`copy` 的单测，放 `copy_test.go`）：`TestCopyIntimacySide`——major + 三段 → 与上面格式逐字相等；light + 只有 `Mind:"  想多陪陪你 "` → 只有标题行和 `心理：想多陪陪你`；none → `""`；三段全空白 + major → `""`。
  - `internal/persona`：`TestSystemPrompt_IntimacyOnlyOpen`——adult 人设带亲密面：私聊会话键 → `Prompt.Intimacy` 非 nil 且 `Prompt.Text` 与去掉亲密面的同一人设逐字节相同；群聊会话键 → nil；minor（无 adult）私聊 → nil；adult 但三段空白 → nil。
  - `internal/core/affinity_guidance_test.go`（沿用 `TestAffinityGuidance_OpenPrivate` 的搭法：adult + `ContentRules`，私聊 `privateKey("29999")`，`injectSeed` 设好感度）：
    - `TestAffinityGuidance_IntimacyMajorAtTop`：好感 90，亲密面三段 → 最后一条用户消息含 `【关系提示】`、`【亲密面】`、major 混合说明、三段文字；system 消息不含任何一段文字。
    - `TestAffinityGuidance_IntimacyNoneAtAcquaintance`：好感 30（认识，默认 none）→ 不含 `【亲密面】`，且含 `CopyDirectiveAffinity("认识", true)` 原文。
    - `TestAffinityGuidance_IntimacyCustomLevels`：`Levels: [none light light major major]`，好感 50（熟悉）→ 含 light 混合说明。
    - `TestAffinityGuidance_IntimacyNeverProtected`：同一 adult 人设（带亲密面）在群聊（`testConvKey`，@机器人触发，好感 90）→ 整个请求文本（`requestText`）不含任何一段亲密面文字和 `【亲密面】`。
    - `TestAffinityGuidance_NoIntimacyUnchanged`：不带亲密面，好感 90 → `【关系提示】` 那段与 `CopyDirectiveAffinity("很要好", true)` 完全一致，后面紧跟 `\n\n`。
  - `internal/core`：`TestAffinityDirective_ReadsMemoryScope`——面板聊天（参照现有面板共享记忆测试里建面板聊天和写共享范围好感度的方式），好感度只写在 `memory.ScopeKeyOf(key)` 下，直接调用 `o.affinityDirective(ctx, task, pol, nil, mode, nil)`（known 为 nil）→ 返回值非空、含该档位名对应句子。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/memory/ ./internal/persona/ ./internal/core/ -run 'TierIndex|IntimacySide|IntimacyOnlyOpen|AffinityGuidance|AffinityDirective' -count=1`
Expected: 编译失败（新符号未定义）

- [ ] **Step 3: 实现**：`TierIndex` 与 `TierName`；`Prompt.Intimacy`（缓存条目里存的是 `Prompt`，直接带上即可；注意拷贝，避免共享可变切片）；`CopyIntimacySide`；`affinityDirective` 新签名，并把 fallback 读取改为 `memory.ScopeKeyOf(task.ConversationKey)`；`runner.go` 传 `system.Mode, system.Intimacy`。更新这些函数的文档注释。

- [ ] **Step 4: 运行确认通过**

Run: `CGO_ENABLED=1 go test -race -count=1 ./internal/memory/ ./internal/persona/ ./internal/core/ ./internal/contextbuilder/`
Expected: PASS

- [ ] **Step 5: 文档**：`docs/usage.md` 在"各档位的默认影响"后加一条"亲密面"：开放模式下按人设的 levels 混入、默认档位、只在开放模式、群聊和 minor 不用、改模板不影响已有会话；`docs/architecture.md` 布局里【关系提示】一行补"开放模式下可带【亲密面】"。

- [ ] **Step 6: 全量检查并提交**

Run: `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 go test -race -count=1 ./...`
Expected: 无 gofmt 输出，PASS

```bash
git commit -am "feat(memory): mix the persona's intimacy side into the relationship hint by tier"
```

### Task 3: 面板人设表单"亲密面"

**Files:**
- Modify: `internal/panel/web/views/personaform.js`
- Modify: `internal/panel/web/style.css`（仅在需要时）
- Test: `internal/panel/web/web_test.go`
- Docs: `docs/panel.md`（人设表单一节）

**Interfaces:**
- Consumes (Task 1): JSON 形状 `intimacy: {tone, mind, behavior, levels}`；默认 levels `[none,none,light,half,major]`；800 字上限
- Produces: 无（叶子任务）

表单行为（来自设计第 6 节）：
- 新一节标题"亲密面"，三个 textarea：语气、心理、表现；下方 `合计 N / 800 字`（按去首尾空白后的字符数，用 `[...s].length` 计 Unicode 字符）。
- 五个下拉：陌生、认识、熟悉、亲近、很要好；选项 不混/少量/一半/为主 → `none/light/half/major`；无 `intimacy.levels` 时用默认。
- 说明文字（逐字）：`只在成年人设的私聊里生效；群聊和保护模式下不使用。改模板不影响已有会话，要到会话的人设页套用模板。`
- 勾选"未成年角色"时整节禁用，显示 `未成年人设不能设置亲密面`；未勾"成年人设"时显示 `当前不是成年人设，亲密面不会生效`。
- 保存时：
  - 勾了未成年且三段有字 → 用表单现有的 `fail` 显示 `未成年人设不能设置亲密面，请先清空`，不提交；
  - 超过 800 字或 levels 递减 → 同样 `fail`，文案 `亲密面合计最多 800 字` / `亲密面的档位要从低到高不递减`；
  - 三段全空 → `delete persona.intimacy`；
  - 否则 `persona.intimacy = {tone, mind, behavior, levels}`（三段原样，后端做 trim 计数）。
- 加载时保留 `base.intimacy` 里未知的键不需要（结构是封闭的）。

- [ ] **Step 1: 写失败的测试**（沿用 `web_test.go` 里对 `GENDER`、`persona.gender = gender.value` 的静态检查写法）
  - `TestPersonaForm_IntimacySection`：`views/personaform.js` 含 `INTIMACY_LEVELS`（或同名常量，列出 none/light/half/major）、含三段标签 `语气`、`心理`、`表现`、含 `800`、含上面四条中文文案逐字、含 `persona.intimacy =` 与 `delete persona.intimacy`。
  - 若 `web_test.go` 有对 JS 做语法检查或 `node --check` 的测试，确认它覆盖 personaform.js。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/panel/web/ -run PersonaForm -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**（照 `appearanceEditor`/`barsEditor` 的组织方式写一个 `intimacyEditor(base, { minor, adult })`，返回 `{ node, read() }`；`read()` 返回 `{ error }` 或 `{ value }`（value 为 undefined 表示删除））。

- [ ] **Step 4: 运行确认通过，并手动语法检查**

Run: `go test ./internal/panel/web/ -count=1 && node --check internal/panel/web/views/personaform.js`
Expected: PASS，`node --check` 无输出（若机器没有 node，跳过并在报告里说明）

- [ ] **Step 5: 文档**：`docs/panel.md` 人设表单一节加"亲密面"的说明（与表单文案一致）。

- [ ] **Step 6: 全量检查并提交**

Run: `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 go test -race -count=1 ./internal/panel/...`
Expected: PASS

```bash
git commit -am "feat(panel): edit a persona's intimacy side and per-tier mix levels"
```
