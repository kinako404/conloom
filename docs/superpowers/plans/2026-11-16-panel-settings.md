# Persona Gender, Template/Bot Decoupling, Platform Bot Settings in the Panel, Panel Reply Suggestions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Personas carry a gender and the core prompt guards relationship-word direction; templates are managed independently of bots; QQ/Telegram bot behaviour settings (incl. default private/group templates) move from the config file to the panel; panel chats offer OpenWebUI-style follow-up suggestions.

**Architecture:** `PersonaDoc.Gender` + one CorePrompt sentence; template delete no longer blocked (bots fall back via existing problem mechanism); a `bot_settings` table merged into the bot directory with a one-time config migration (app_meta marker, refuse moved keys afterwards) and `BotConfig.TemplateFor(convType)`; a suggestions endpoint calling the short model with thinking disabled, cached per (conversation, last seq).

**Tech Stack:** Go 1.26, SQLite (modernc), vanilla JS panel. No new dependencies.

**Spec (Chinese, binding):** `docs/superpowers/specs/2026-11-16-panel-settings-design.md`.

## Global Constraints

- No new deps; `CGO_ENABLED=0 go build ./...`; `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...`; `gofmt -l .` clean; `node --check` on changed JS.
- Public repo: fake data only in tests (bots qq-main/tg-main, users 20001/29999, names 小明/小红); no real IDs/names.
- Personas without `gender` render byte-identical to today (the CorePrompt change is the one intended system-prompt change).
- Protected mode intact; suggestions reviewed with OutputProtected in protected mode, Output in open mode.
- Fixed values: gender ∈ {"", "female", "male"} rendering `性别：女。`/`性别：男。` after 角色设定 and before 外貌; CorePrompt sentence exactly as in spec §1; migration `0019_bot_settings.sql`; app_meta key `config_bot_settings_migrated`; moved bot keys `name, admins, private_chat, enabled_conversations, persona_template, skills`; refusal exit 78; defaults for a new config bot (name=id, no admins, private_chat admins, no groups, both templates default, no skills); suggestions: 3 items ≤40 runes each, recent ~12 messages, 20 s timeout, LRU 256 conversations, usage purpose `suggest`, single-flight per conversation.
- Panel JS: no innerHTML/inline style/http(s)/blob:; h() for DOM.

## Review Focus

1. Upgrade on the live layout (two config bots with names/admins/enabled conversations/persona_template/skills in the file) → after first start the settings rows match the file exactly, marker set; second start with keys still present exits 78; with keys removed starts and behaves identically (Task 3 `TestBotSettings_MigrateLiveLayout`).
2. A group conversation of a config bot uses the group template, a private one the private template, on first reply (Task 3 `TestTemplateFor_ByConversationType`).
3. Editing a config bot's groups/private range in the panel takes effect in core immediately and is returned by the capabilities endpoint (Task 4 `TestBotsAPI_EditConfigBotCapabilities`).
4. Deleting a template used by a bot → 200, bot falls back with a problem note, replies keep working (Task 2 `TestTemplatesAPI_DeleteInUseFallsBack`).
5. Suggestions: protected persona → output reviewed and protected rule in prompt; non-panel conversation → 400; repeated request for the same seq → one model call (Task 5 `TestSuggestions_*`).

---

### Task 1: Persona gender + relationship-direction prompt
**Files:** `pkg/conloomapi/persona.go` (+test), `internal/persona/render.go` (+tests incl. legacy byte-identical), CorePrompt template (+tests pinning it), `internal/panel/web/views/personaform.js` (gender select), `docs/configuration.md`.
- [ ] Tests: `TestPersona_GenderValidate`, `TestRender_Gender` (exact placement), legacy render unchanged, `TestCorePrompt_RelationshipDirection`.
- [ ] Commit `feat(persona): gender field and relationship-direction guidance`.

### Task 2: Templates independent of bots
**Files:** `internal/persona/persona.go` DeleteTemplate (no in-use refusal; returns affected users), `internal/panel/templates.go` (+tests; response lists affected bots; drop 409 TEMPLATE_IN_USE), core panel-bot problem refresh after delete (existing hook), `internal/panel/web/views/templates.js` (remove 默认模板用于 column; 新建模板 dialog: name + start from 空白/内置默认 or copy of a template; delete confirm lists users), `pkg/conloomapi` error code cleanup if unused, `docs/panel.md`, `docs/api.md`.
- [ ] Tests: `TestTemplatesAPI_DeleteInUseFallsBack`, `TestCatalog_DeleteInUseAllowed`, `TestTemplatesAPI_CreateBlank` (PUT new name with default doc), static JS checks.
- [ ] Commit `feat(templates): manage templates independently of bots`.

### Task 3: Bot settings store, migration, directory
**Files:** `internal/store/sqlite/migrations/0019_bot_settings.sql`, store methods (`GetBotSettings`, `ListBotSettings`, `PutBotSettings`, `MigrateConfigBotSettings(ctx, rows, now) (migrated []string, err)` with marker), `internal/config` (moved keys parsed as legacy-only fields for migration; ValidateServe no longer requires them; refusal after migration handled in app like templates), `config.BotConfig` gains `PrivateTemplate`, `GroupTemplate`, `TemplateFor(convType string) string`, `internal/botdir` merge (config identity + settings; panel bots map their template to both), `internal/core` call sites of `bot.PersonaTemplate` → `TemplateFor` (commands, runner, memory_commands, autonomy_service, wake, panelbots), capabilities endpoint reads the directory (verify), `internal/app` startup order (migrate before directory load), `conloom.example.yaml`, `docs/configuration.md`, `docs/deployment.md` upgrade note.
- [ ] Tests: `TestBotSettings_MigrateLiveLayout`, `TestBotSettings_SecondStartRefuses`, `TestBotSettings_NewConfigBotDefaults`, `TestTemplateFor_ByConversationType`, `TestCapabilities_FromSettings`, existing tests adapted (fixtures that set bot fields in config now seed settings).
- [ ] Commit `feat(bots): platform bot settings live in the database`.

### Task 4: Panel editing of platform bots
**Files:** core bot service (Update for config bots: only movable fields; validation; directory swap; audit `panel.bot.update`), `internal/panel/bots.go` (+tests), `internal/panel/web/views/chats.js` bot dialog (config-bot form fields per spec §3; read-only identity), `docs/panel.md`, `docs/api.md`.
- [ ] Tests: `TestBotsAPI_EditConfigBot` (fields saved, identity immutable, invalid IDs with ':' or whitespace 400, unknown template 400), `TestBotsAPI_EditConfigBotCapabilities`, `TestBotsAPI_DeleteConfigBot403`, static JS checks.
- [ ] Commit `feat(panel): edit QQ/Telegram bot settings in the panel`.

### Task 5: Panel reply suggestions
**Files:** core `internal/core/suggest.go` (+test): `func (o *Orchestrator) Suggest(ctx, convKey string, afterSeq int64) ([]string, error)`; openai request extra_body override for thinking off (provider/openai or router profile clone); usage purpose `suggest` (+ panel PURPOSE label 建议回复); panel endpoint `POST /panel/api/conversations/{key}/suggestions` (+tests: non-panel 400, CSRF, cache single model call, protected review); `internal/panel/web/views/chat.js` (toggle in localStorage, chips under last message, click sends, hide on typing/new message), `docs/panel.md`, `docs/api.md`.
- [ ] Tests: `TestSuggestions_PanelOnly`, `TestSuggestions_CachedPerSeq`, `TestSuggestions_SingleFlight`, `TestSuggestions_ProtectedReviewed`, `TestSuggestions_ThinkingDisabled` (request body has enable_thinking false), `TestSuggestions_BadModelOutputEmpty`, static JS checks.
- [ ] Commit `feat(panel): follow-up reply suggestions in panel chats`.
