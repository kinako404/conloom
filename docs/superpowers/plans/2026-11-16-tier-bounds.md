# 各档位自定义边界 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 人设的亲密面可以按档位写自定义边界，开放模式下替换【关系提示】里这一档的内置边界句。

**Architecture:** `PersonaIntimacy` 加 `Bounds`；`CopyDirectiveAffinity` 多一个 `bound` 参数，非空时在开放模式下替换内置边界句；内置边界句抽成 `DefaultAffinityBound(tier)` 供面板接口复用。面板人设表单每档加一个输入框，占位文字取自新接口。

**Tech Stack:** Go（见 go.mod），原生 JS 面板。

**Spec:** `docs/superpowers/specs/2026-11-16-tier-bounds-design.md`

## Global Constraints

- 公开仓库：代码、测试、文档、提交说明里不出现真实 ID、令牌、主机名、昵称、角色名；测试只用假数据（`qq-main`、用户 20001/29999、小明/小红、`example.invalid`）。
- 不加新依赖；提交前 `gofmt -l .` 为空、`go vet ./...` 通过、相关包 `CGO_ENABLED=1 go test -race -count=1` 通过。
- 提交说明英文 conventional 风格。面板文案、文档中文；API 错误文本英文、沿用现有风格。
- 保护模式的【关系提示】、系统提示、`Render` 输出不因 `bounds` 改变（逐字节）。
- 每项边界去首尾空白后最多 150 字，不能含 `\r` 或 `\n`；有 `bounds` 时必须正好 5 项。
- minor 人设带任何非空边界时，报错文本正好是 `a minor persona cannot have an intimacy side`。

## Review Focus

1. 只填边界、三段文字全空——期望【关系提示】用自定义边界，不出现【亲密面】；T1 测试。
2. 自定义边界含换行或 151 字——期望 400，字段路径 `intimacy.bounds[i]`；T1 测试。
3. 群聊/保护模式下人设带自定义边界——期望请求里不出现边界文字；T1 测试。
4. 面板里只改了边界、三段为空——期望保存时带上 `intimacy.bounds`，不被当作"全空"删掉；T2 静态检查 + T1 后端测试。
5. 某档边界只含空白——期望视同未填，用内置边界；T1 测试。

---

### Task 1: 后端——字段、校验、替换、接口

**Files:**
- Modify: `pkg/conloomapi/persona.go`（`PersonaIntimacy.Bounds`、`MaxIntimacyBoundRunes = 150`、`Empty`、新 `HasSide`、新 `BoundAt`、`Validate`）
- Modify: `internal/core/copy.go`（`DefaultAffinityBound`、`CopyDirectiveAffinity` 新签名、`CopyIntimacySide` 改用 `HasSide`）
- Modify: `internal/core/memory.go`（`affinityDirective` 传边界）
- Modify: `internal/panel/data_routes.go` + 处理函数所在文件（`GET /panel/api/affinity-bounds`）
- Test: `pkg/conloomapi/persona_test.go`、`pkg/conloomapi/content_test.go`、`internal/core/copy_test.go`、`internal/core/affinity_guidance_test.go`、`internal/persona` 的 SystemPrompt 测试、`internal/panel` 的读接口测试
- Docs: `docs/configuration.md`（亲密面一节）、`docs/usage.md`（"亲密面"条目）、`docs/api.md` 或 `docs/panel.md` 中列面板接口的地方

**Interfaces:**
- Produces:
  - `PersonaIntimacy.Bounds []string`，标签 `bounds,omitempty`（json/yaml）
  - `const MaxIntimacyBoundRunes = 150`
  - `func (in *PersonaIntimacy) HasSide() bool`：非 nil 且三段至少一段去空白后非空
  - `func (in *PersonaIntimacy) Empty() bool`：nil，或 `!HasSide()` 且每项边界去空白后为空（语义扩大）
  - `func (in *PersonaIntimacy) BoundAt(tier int) string`：去首尾空白后的第 tier 项；nil、越界、无 Bounds 时为 `""`
  - `func core.DefaultAffinityBound(tier string) string`：开放模式内置边界句（即现在开放分支追加的那句，逐字不变）；未知档位 `""`
  - `func core.CopyDirectiveAffinity(tier string, open bool, bound string) string`：`open` 且 `strings.TrimSpace(bound) != ""` 时用 bound 代替 `DefaultAffinityBound(tier)`；`!open` 时忽略 bound；其余输出与现在逐字节相同
  - `GET /panel/api/affinity-bounds` → `200 {"tiers":["陌生","认识","熟悉","亲近","很要好"],"defaults":[DefaultAffinityBound(各档)]}`，需登录（同其他 `/panel/api` 读接口）

- [ ] **Step 1: 写失败的测试**
  - `TestPersonaIntimacy_BoundsHelpers`：`{Bounds: [" ", "只牵手", "", "", ""]}` → `HasSide()` false、`Empty()` false、`BoundAt(1)` `"只牵手"`、`BoundAt(0)` `""`、`BoundAt(5)` `""`；`{Tone:"x"}` → `HasSide()` true；`{Bounds: ["","","","",""]}` → `Empty()` true；nil → `BoundAt(0)` `""`。
  - `TestPersonaDocValidate_IntimacyBounds`（表驱动）：5 项、各 150 字通过；151 字 → 错误含 `intimacy.bounds[2]`（放在第 3 项）；含 `\n` → 错误含 `intimacy.bounds[0]`；4 项 → 错误含 `intimacy.bounds`；省略通过。
  - `TestValidateContent_MinorIntimacy` 加一例：minor + 只有 `Bounds[1]="可以牵手"` → 错误文本正好 `a minor persona cannot have an intimacy side`。
  - `TestCopyDirectiveAffinity` 表测试加：开放模式 `("认识", true, "可以牵手，但不接受表白。")` == `"【关系提示】你们已经认识。语气可以友好、放松，但不要主动发展更亲密的关系。可以牵手，但不接受表白。"`；`("认识", true, "  ")` == `("认识", true, "")`；保护模式 `("认识", false, "可以牵手")` == `("认识", false, "")`；对五档，`CopyDirectiveAffinity(t, true, "") == "【关系提示】" + <基础句> + DefaultAffinityBound(t)`（基础句沿用测试里已钉住的写法）。
  - `TestCopyIntimacySide`：只有 Bounds、三段空 → `""`。
  - `internal/persona`：`TestSystemPrompt_IntimacyOnlyOpen` 加一例：adult 私聊、只有 Bounds → `Prompt.Intimacy` 非 nil；群聊 → nil；`Prompt.Text` 与无 Bounds 时逐字节相同。
  - `internal/core/affinity_guidance_test.go`：
    - `TestAffinityGuidance_CustomBoundReplacesDefault`：adult 私聊，`Intimacy{Bounds: ["", "认识档自定义边界。", "", "", ""]}`，好感 30 → 最后一条用户消息含 `CopyDirectiveAffinity("认识", true, "认识档自定义边界。")`，不含 `DefaultAffinityBound("认识")`，不含 `【亲密面】`。
    - `TestAffinityGuidance_CustomBoundNeverProtected`：同一人设在群聊（@机器人，好感 30）→ `requestText` 不含 `认识档自定义边界`。
  - `internal/panel`：`TestAffinityBounds_ListsDefaults`（沿用该包读接口测试的搭法）→ 200，`tiers` 为五档名，`defaults[i] == core.DefaultAffinityBound(tiers[i])` 且都非空；未登录 → 401（同其他读接口）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/conloomapi/ ./internal/core/ ./internal/persona/ ./internal/panel/ -run 'Bounds|Bound|CopyDirectiveAffinity|IntimacySide|IntimacyOnlyOpen|MinorIntimacy' -count=1`
Expected: 编译失败（新符号未定义）

- [ ] **Step 3: 实现**：按 Interfaces；`Validate` 的 bounds 规则风格同 levels；`CopyIntimacySide` 改用 `HasSide()`；`affinityDirective` 在 `open && side != nil` 时取 `side.BoundAt(memory.TierIndex(a))` 传给 `CopyDirectiveAffinity`，否则传 `""`；`persona.Service.SystemPrompt` 已按 `!Empty()` 填 `Prompt.Intimacy`，语义扩大后只填边界也会带上，确认拷贝含 `Bounds`。更新这些函数和类型的文档注释。

- [ ] **Step 4: 运行确认通过**

Run: `CGO_ENABLED=1 go test -race -count=1 ./pkg/... ./internal/core/ ./internal/persona/ ./internal/panel/ ./internal/contextbuilder/ ./internal/config/`
Expected: PASS

- [ ] **Step 5: 文档**：configuration.md 亲密面一节加 `bounds`（5 项、每项 ≤150 字、单行、空用内置、只替换开放模式边界句、内容规则仍是上限、minor 不能填）；usage.md "亲密面"条目加一句；列面板接口的文档加 `GET /panel/api/affinity-bounds`。

- [ ] **Step 6: 全量检查并提交**

Run: `gofmt -l . ; go vet ./... ; CGO_ENABLED=1 go test -race -count=1 ./...`
Expected: PASS（已知偶发失败的测试单独重跑能过即可）

```bash
git commit -am "feat(core): per-persona boundary for each affinity tier"
```

### Task 2: 面板——每档边界输入框

**Files:**
- Modify: `internal/panel/web/views/personaform.js`（`intimacyEditor`）
- Test: `internal/panel/web/web_test.go`
- Docs: `docs/panel.md`（人设表单"亲密面"）

**Interfaces:**
- Consumes (Task 1): `GET /panel/api/affinity-bounds` → `{tiers, defaults}`；JSON `intimacy.bounds`（5 项，空串表示用内置）；每项 ≤150 字、无换行
- Produces: 无

表单行为：
- 每档下拉框下方一个 `<input type="text">`，标签 `边界（可选）`，`placeholder` 为这一档的内置边界（接口取到前为空字符串；取失败时占位文字为 `空着就用内置边界`）；值来自 `base.intimacy.bounds[i]`。
- 说明文字（逐字，加在现有说明后面）：`边界替换这一档内置的分寸说明，空着就用内置的；它不能越过内容规则。`
- 保存检查：任一项去空白后超过 150 字 → `fail` 文案 `每档边界最多 150 字`；含换行（粘贴进来的）→ 去掉换行后再算，不报错（输入框本身单行）。
- 三段文字和 5 项边界都空白 → `delete persona.intimacy`；否则 `persona.intimacy` 带上 `bounds`（5 项，原样字符串；全空时省略 `bounds` 键）。
- "未成年角色"已勾选时：边界输入框也禁用；「清空亲密面」按钮的显示条件改为"三段或任一边界有字"，点击时也清空边界；保存检查"未成年且有字"同样把边界算进去。
- 接口只在打开表单时取一次（模板页和会话人设页都用同一函数）。

- [ ] **Step 1: 写失败的测试**：`TestPersonaForm_IntimacyBounds`（同现有静态检查写法）——`views/personaform.js` 含 `'affinity-bounds'`（或 `affinity-bounds` 路径字面量）、`边界（可选）`、上面的说明文字逐字、`每档边界最多 150 字`、`空着就用内置边界`、`bounds`。
- [ ] **Step 2: 运行确认失败**：`go test ./internal/panel/web/ -run PersonaForm -count=1` → FAIL
- [ ] **Step 3: 实现**（沿用 `intimacyEditor` 的结构和现有 `api` 调用方式）。
- [ ] **Step 4: 运行确认通过**：`go test ./internal/panel/... -count=1 && /usr/local/node/bin/node --check internal/panel/web/views/personaform.js` → PASS
- [ ] **Step 5: 文档**：`docs/panel.md` 亲密面一节加边界输入框说明（与表单文案一致）。
- [ ] **Step 6: 检查并提交**：`gofmt -l . ; go vet ./internal/panel/... ; CGO_ENABLED=1 go test -race -count=1 ./internal/panel/...`

```bash
git commit -am "feat(panel): edit each affinity tier's boundary in the intimacy section"
```
