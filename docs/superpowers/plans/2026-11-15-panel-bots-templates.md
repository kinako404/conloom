# Panel Bots, Chat Page, Online Persona Templates and Appearance Reference — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bots created and managed in the panel (panel-only chats) with a top-level chat page, persona templates editable online, and an appearance reference image per persona that a vision model can describe and the reply model receives.

**Architecture:** Panel bots live in SQLite and are merged with config bots in an atomic bot directory that every component reads instead of `cfg.Bots`. Persona templates become an atomic merged catalog (config + DB) inside `persona.Service`. Images are immutable rows keyed by sha256 with a pre-made downscaled JPEG; the reply request carries it as an image part on the first user message when the routed profile declares `image`.

**Tech Stack:** Go (module `github.com/kinako404/conloom`, Go 1.26, stdlib `image/png`, `image/jpeg` only — no new dependencies), SQLite (modernc), vanilla JS panel.

**Specs (Chinese, binding):** `docs/superpowers/specs/2026-11-15-panel-bots-design.md` (tasks 1–5) and `docs/superpowers/specs/2026-11-15-persona-templates-appearance-design.md` (tasks 6–12). Read the relevant spec before your task.

## Global Constraints

- No new Go module dependencies. `CGO_ENABLED=0 go build ./...`; `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...`; `gofmt -l .` clean.
- Public repo: no real IDs, tokens, hostnames, nicknames or character names in code, tests, comments, docs or commit messages; no real images committed. Tests use fake data (bots `qq-main`/`tg-main`, users `20001`/`29999`, names 小明/小红, `example.invalid`) and images generated in the test.
- Existing behaviour unchanged unless a spec says otherwise: group chats, config bots, adapters, existing tests (adjust a test only when the spec changes the behaviour it pins, and say so).
- Personas without the new fields: system prompt and reply request JSON byte-identical to today (`TestRender_LegacyPersonaUnchanged`, openai client JSON tests).
- Protected mode intact: `ValidateContent` (minor && adult rejected) applies to every template/persona write path.
- Panel JS: no `innerHTML`/`outerHTML`/`insertAdjacentHTML`/`eval`, no inline `style=`, no `http(s)://`, text via `h()`/`textContent`; CSP `img-src 'self' data:` (no `blob:`); new view files added to `web_test.go` list.
- Panel writes: session + same-origin + `X-CSRF-Token` (via `s.api`); errors 400 invalid / 403 config-owned / 404 missing / 409 conflict-busy / 503 service missing; every write audited.
- Fixed values: panel bot ID `panel-` + 8 chars `[a-z0-9]`; config bot IDs may not start with `panel-`; bot name 1–32 runes, no control chars/newlines; max 50 panel bots; `/chats` ≤200 chats per bot; template name `^[a-z0-9_-]{1,32}$`; `appearance` ≤800 runes; image body ≤8 MiB, PNG/JPEG sniffed by `http.DetectContentType`, width and height ≤8000, ≤25,000,000 pixels; reply copy long edge ≤768 px JPEG quality 85; describe copy long edge ≤1536 px; image tokens `ceil(w/28)*ceil(h/28)+2`; unreferenced images purged after 7 days; describe timeout 40 s; describe text target 150–400 字; label line `【你的外观参考图（这就是你自己的样子，不是对方发来的图片）】`.
- Migrations: `0013_panel_bots.sql` (task 1), `0014_persona_templates.sql` (task 7). All columns `NOT NULL`, times via `fmtTime`; non-nil `[]byte{}` for BLOBs.

## Review Focus

1. Deleting a panel bot while one of its chats has a reply running: the delete returns 409 and nothing of other bots is touched; after the task stops a retry succeeds and leaves no rows under the bot except `bot_instances`, `model_calls`, `audit_events` (Task 3 `TestPanelBots_DeleteBusyThenRetry`).
2. A template edited online while replies run: no data race (`-race`), replies see a whole old or new catalog (Task 8 `TestCatalog_ConcurrentPutAndRead`).
3. Reply routed to a profile without `image` modality, or re-routed to `long_context` after a failure: the request carries no image part (Task 9 `TestReplyImage_DroppedWithoutImageModality`, `TestReplyImage_DroppedOnReroute`).
4. Uploading a huge-dimension PNG (e.g. 20000×20000 header, tiny file) or a non-image body labelled `image/png`: 400 before full decode, no row stored (Task 7 `TestImaging_RejectsDecompressionBomb`, `TestImaging_SniffsNotHeader`).
5. A persona referencing an image ID that was never uploaded (panel, gateway, template PUT): 400; an old version referencing a purged image still replies (no image, warning) (Task 8 `TestPersonaWrite_UnknownImage400`, Task 9 `TestReplyImage_MissingImageStillReplies`).

---

### Task 1: Store — panel bots

**Files:** Create `internal/store/sqlite/migrations/0013_panel_bots.sql`, `internal/store/sqlite/panelbots.go`, `internal/store/sqlite/panelbots_test.go`; modify `internal/store/records.go`, `internal/store/store.go`, `internal/store/sqlite/delete_test.go` only if a keyed-table test requires it (panel_bots has no `conversation_key`).

**Interfaces — Produces:**
```go
type PanelBot struct { ID, Name, PersonaTemplate string; Skills []string; CreatedBy string; CreatedAt, UpdatedAt time.Time }
type BotInstance struct { ID, Platform, Name string }
var ErrBotHasData = errors.New("store: bot still has conversations")
CreatePanelBot(ctx context.Context, b PanelBot) error            // tx: panel_bots + bot_instances(platform "panel"); duplicate id → error
UpdatePanelBot(ctx context.Context, b PanelBot) error            // tx: panel_bots row + bot_instances.name; missing → ErrNotFound
ListPanelBots(ctx context.Context) ([]PanelBot, error)           // by created_at, id
DeletePanelBot(ctx context.Context, id string) error             // tx: refuse ErrBotHasData if any conversations/tasks/outbox_actions row has bot_instance_id=id; else DELETE users, events WHERE bot_instance_id=id, DELETE panel_bots row; keep bot_instances, model_calls, audit_events
ListBotInstances(ctx context.Context) ([]BotInstance, error)
```
- [ ] Tests: `TestPanelBots_CRUD` (create→list→update name also updates bot_instances→list), `TestPanelBots_DeleteRefusesWithConversations`, `TestPanelBots_DeleteSweepsLeftovers` (users/events of the bot gone, another bot's rows and model_calls/audit_events kept, bot_instances row kept), `TestPanelBots_SkillsRoundTrip` (nil skills stored as `[]`), migration-only-adds-tables test.
- [ ] RED → implement → GREEN (`go test -race ./internal/store/...`) → commit `feat(store): panel bots`.

### Task 2: Bot directory and call-site migration

**Files:** Create `internal/botdir/botdir.go`, `internal/botdir/botdir_test.go`; modify `internal/config/validate.go` (+test: `panel-` prefix reserved for config bot IDs), and every non-test reader of `cfg.Bot(...)` / `cfg.Bots` except `config/validate.go`, `config/config.go` internals and the startup `UpsertBotInstance` loop in `app/app.go` (list: `memory/scheduler.go`, `memory/consolidate.go`, `search/vector/indexer.go`, `app/skills.go` (persona adapter; `registerSkills` keeps config bots), `panel/conversations.go`, `panel/overview.go`, `panel/tasks.go`, `panel/configview.go` (bots view: config bots + panel bots marked), `panel/writes.go`, `panel/readviews.go`, `gateway/handlers.go`, `core/*` (memory_commands, autonomy_service, wake_loop, commands, runner, wake, panelchat, orchestrator)), wiring in `internal/app/app.go`, deps structs of core/memory/search/panel/gateway.

**Interfaces — Produces:**
```go
package botdir
type Directory struct{ /* config bots + atomic.Pointer to panel snapshot */ }
type PanelEntry struct { Bot config.BotConfig; Problems []string } // Problems: Chinese notes like "模板已不存在"
func New(configBots []config.BotConfig) *Directory
func (d *Directory) Bot(id string) (config.BotConfig, bool)
func (d *Directory) All() []config.BotConfig          // config bots in config order, then panel bots in snapshot order
func (d *Directory) IsPanel(id string) bool
func (d *Directory) Problems(id string) []string
func (d *Directory) SetPanel(entries []PanelEntry)    // atomic swap of the whole panel snapshot
func PanelBotConfig(b store.PanelBot) config.BotConfig // Platform "panel", PrivateChat "admins", Name, PersonaTemplate, Skills
```
- [ ] Tests: `TestDirectory_LookupAndOrder`, `TestDirectory_ConcurrentSetAndRead` (-race), `TestValidate_PanelPrefixReserved`; all existing tests stay green with the directory holding only config bots.
- [ ] Commit `refactor: read bots through a shared directory`.

### Task 3: Core — panel bot service

**Files:** Create `internal/core/panelbots.go`, `internal/core/panelbots_test.go`; modify `internal/core/panelchat.go` (Create/Submit refuse deleting bots), `internal/core/orchestrator.go` (reject non-`panel` platform events for panel bots; load directory at start), `internal/app/app.go`.

**Interfaces — Consumes:** Task 1 store, Task 2 directory, `persona.Service.TemplateNames()`/`Template` (Task 8 later swaps in the catalog), skills registry (`skills.Registry` with conversation filter). **Produces:**
```go
var ErrBotDeleting = errors.New("core: bot is being deleted")
var ErrConfigBot = errors.New("core: bot is defined in the config file")
var ErrInvalidBot = errors.New("core: invalid bot") // wrapped with a Chinese reason
type PanelBotInput struct { Name, PersonaTemplate string; Skills []string }
func (o *Orchestrator) PanelBots() *PanelBotService
func (s *PanelBotService) Load(ctx context.Context) error // read store, resolve problems (missing template → runtime "default" + problem, missing/non-private skill → dropped + problem), SetPanel
func (s *PanelBotService) Create(ctx context.Context, in PanelBotInput, actor string) (config.BotConfig, error)
func (s *PanelBotService) Update(ctx context.Context, id string, in PanelBotInput, actor string) (config.BotConfig, error)
func (s *PanelBotService) Delete(ctx context.Context, id, confirmName, actor string) error
func (s *PanelBotService) PrivateSkills() []SkillInfo // name, description of registered skills allowed in private chats
```
Rules: spec panel-bots §4.1 (ID generation, retry on collision), §4.4 validation (name, template exists, skills private-capable, max 50), §4.7, §4.8 delete flow (deleting flag → DeletePanelChat each chat of the bot, any ErrChatBusy → clear flag, 409; then store.DeletePanelBot; SetPanel; audit `panel.bot.delete`); audits `panel.bot.create`, `panel.bot.update`. Background jobs skip unknown bots silently.
- [ ] Tests: `TestPanelBots_CreateValidates` (names, template, skills, cap 50), `TestPanelBots_CreateThenChat` (panel chat works, system prompt uses the bot name), `TestPanelBots_UpdateTakesEffectNextReply`, `TestPanelBots_DeleteBusyThenRetry`, `TestPanelBots_DeleteWrongName400`, `TestPanelBots_ConfigBotRefused`, `TestPanelBots_NonPanelEventRejected`, `TestPanelBots_LoadMarksProblems`.
- [ ] Commit `feat(core): panel bot service`.

### Task 4: Panel API — bots, chats list, bot lists everywhere

**Files:** Create `internal/panel/bots.go`, `internal/panel/bots_test.go`; modify `internal/panel/common.go` (Deps: `PanelBots` service interface), `data_routes.go`, `write_routes.go`, `respond.go` (map core errors), `conversations.go` (bot list from directory, `source` field), `overview.go`, `tasks.go`, usage view data (deleted bot names via `ListBotInstances`, marked `deleted`), `internal/app/app.go`, `docs/panel.md`, `docs/architecture.md`, `docs/configuration.md` (`panel-` reserved), `docs/usage.md`.

**Produces:** `GET /panel/api/bots`, `POST /panel/api/bots`, `PUT /panel/api/bots/{id}`, `DELETE /panel/api/bots/{id}` (body `{confirm_name}`), `GET /panel/api/chats` (spec §4.10), `GET /panel/api/bots/skills` (private skills with descriptions, for the form). JSON: `GET bots` → `{bots: [bot…], templates: [names from persona.Service.TemplateNames()]}`; bot `{id, name, source: "config"|"panel", persona_template, skills, problems, chats, last_message_at}`; chats `{bots: [{id, name, source, chats: [{key, title, platform, takeover, last_message_at}]}]}`.
- [ ] Tests: `TestBotsAPI_CRUDAndAudit`, `TestBotsAPI_ConfigBot403`, `TestBotsAPI_CSRF`, `TestChatsAPI_GroupsAndOrder` (takeover flag for platform private chats, panel chats listed, groups excluded, cap 200), `TestUsage_DeletedBotName`.
- [ ] Commit `feat(panel): bot management and chat list API`.

### Task 5: Panel UI — chat page

**Files:** Create `internal/panel/web/views/chats.js`; modify `web/app.js` (routes `/chat`, `/chat/:key`; nav `['chat','聊天']` before 会话), `views/chat.js` (export chat pane with a `hint` option), `views/conversation.js` (remove chat tab, default 消息, "在聊天页打开" link for private), `views/conversations.js` (new chat → `#/chat/<key>`; dialog shared from chats.js), `web/style.css`, `web/web_test.go`, `internal/panel/static_test.go` if needed, `docs/panel.md`.

Behaviour: spec panel-bots §4.9. Left list loaded once per page visit and refreshed after writes; switching `#/chat/<key>` rebuilds only the right pane (cache list in module scope keyed by page visit). Bot dialogs: name, template select (from `GET /panel/api/persona-templates` if present else conversation list's templates — use `GET bots` response field `templates` provided by Task 4: add `templates: [names]` to `GET /panel/api/bots`), skill checkboxes with descriptions; delete dialog requires typing the name.
- [ ] Tests: web_test list + unsafe-API test pass; `node --check` on changed JS if node exists; Go test that `/panel/` serves `views/chats.js`.
- [ ] Commit `feat(panel): chat page with bot management`.

### Task 6: Persona appearance fields

**Files:** modify `pkg/conloomapi/persona.go` (+test), `internal/persona/render.go` (+`persona_test.go`), `docs/configuration.md`.

**Produces:** fields per spec appearance §4.1 (`Appearance`, `AppearanceImage`, `AppearanceImageSkip`), `const MaxAppearanceRunes = 800`; `Validate` checks the rune cap and that `AppearanceImage` is empty or 64 lowercase hex chars; Render adds `"\n\n外貌：\n" + trimmed` after 角色设定 and before 说话方式 only when non-empty.
- [ ] Tests: `TestPersona_AppearanceValidate`, `TestRender_Appearance` (exact placement), legacy render tests unchanged, JSON of a persona without the fields unchanged.
- [ ] Commit `feat(persona): appearance text and reference image fields`.

### Task 7: Imaging + store for images and templates

**Files:** Create `internal/imaging/imaging.go`, `internal/imaging/imaging_test.go`, `internal/store/sqlite/migrations/0014_persona_templates.sql`, `internal/store/sqlite/personaimages.go` (+test); modify `internal/store/records.go`, `internal/store/store.go`.

**Produces:**
```go
package imaging
var ErrUnsupported, ErrTooLarge = errors.New("imaging: unsupported image"), errors.New("imaging: image too large")
type Processed struct { ID, MIME string; Width, Height int; Original, ReplyJPEG []byte; ReplyWidth, ReplyHeight, ReplyTokens int }
func Process(data []byte) (Processed, error)          // sniff, DecodeConfig caps, decode, flatten alpha on white, box downscale ≤768, JPEG q85, sha256 ID of data
func Scaled(data []byte, maxEdge int) ([]byte, error) // JPEG copy for the vision call (≤1536)
func Tokens(w, h int) int                             // ceil(w/28)*ceil(h/28)+2
// store
type PersonaImage struct { imaging fields...; CreatedBy string; CreatedAt time.Time }
type PersonaTemplateRecord struct { Name string; Persona conloomapi.PersonaDoc; UpdatedBy string; CreatedAt, UpdatedAt time.Time }
PutPersonaImage(ctx context.Context, img PersonaImage) error                 // INSERT OR IGNORE by id
GetPersonaImage(ctx context.Context, id string, original bool) (PersonaImage, error) // original=false leaves Original nil
PersonaImageExists(ctx context.Context, id string) (bool, error)
PurgePersonaImages(ctx context.Context, createdBefore time.Time) (int, error) // only ids not referenced by persona_templates.persona or persona_versions.persona (json_extract '$.appearance_image')
ListPersonaTemplates(ctx context.Context) ([]PersonaTemplateRecord, error)
PutPersonaTemplate(ctx context.Context, r PersonaTemplateRecord) error
DeletePersonaTemplate(ctx context.Context, name string) (bool, error)
```
- [ ] Tests: `TestImaging_PNGAndJPEG` (generated images; reply long edge 768; tokens), `TestImaging_FlattensAlpha`, `TestImaging_RejectsDecompressionBomb` (crafted PNG header 20000×20000 → ErrTooLarge without decoding pixels), `TestImaging_SniffsNotHeader` (GIF/text → ErrUnsupported), `TestImaging_SameBytesSameID`; store `TestPersonaImages_PutIdempotentAndGet`, `TestPersonaImages_PurgeKeepsReferenced` (referenced by a template row and by a persona version are kept; old unreferenced purged; young unreferenced kept), `TestPersonaTemplates_CRUD`, migration-only-adds-tables.
- [ ] Commit `feat(store,imaging): persona images and template rows`.

### Task 8: Template catalog

**Files:** modify `internal/persona/persona.go` (+tests), `internal/core/autonomy_service.go`, `internal/panel/readviews.go`, `internal/core/panelbots.go` (template existence via catalog), `internal/app/app.go` (load DB templates after `NewService`), `internal/panel/writes.go` and `internal/gateway/handlers.go` (persona writes check image existence), `docs/architecture.md`.

**Produces (persona.Service):**
```go
type TemplateSource string // "config" | "config_override" | "panel"
type TemplateInfo struct { Name string; Source TemplateSource; Doc conloomapi.PersonaDoc; Config *conloomapi.PersonaDoc; UpdatedBy string; UpdatedAt time.Time }
var ErrInvalidTemplate, ErrTemplateInUse, ErrTemplateConfigOnly = ...
func (s *Service) SetImageChecker(f func(ctx context.Context, id string) (bool, error))
func (s *Service) SetTemplateUsers(f func(name string) []string) // panel bot names using a template (from botdir)
func (s *Service) LoadTemplates(ctx context.Context) error
func (s *Service) Template(name string) (conloomapi.PersonaDoc, bool)
func (s *Service) Templates() []TemplateInfo
func (s *Service) PutTemplate(ctx context.Context, name string, doc conloomapi.PersonaDoc, actor string) error
func (s *Service) ResetTemplate(ctx context.Context, name string) error   // config templates only
func (s *Service) DeleteTemplate(ctx context.Context, name string) error  // panel templates only; ErrTemplateInUse with users
```
`NewService(st, map)` signature unchanged (map = config layer). Every write path of personas (`Update`, PutTemplate, panel/gateway PUT) validates image IDs via the checker → invalid → 400.
- [ ] Tests: `TestCatalog_MergeOrder`, `TestCatalog_PutResetDelete` (default not deletable; config template reset; panel template delete in use → ErrTemplateInUse), `TestCatalog_ConcurrentPutAndRead` (-race), `TestCatalog_NewConversationUsesOverride`, `TestCatalog_LoadSkipsInvalidRows`, `TestPersonaWrite_UnknownImage400` (panel + gateway).
- [ ] Commit `feat(persona): online template catalog`.

### Task 9: Reply image

**Files:** modify `internal/provider/openai/client.go`/`tools.go` (+tests), `internal/contextbuilder/build.go` (+tests), `internal/core/runner.go` (+`internal/core/reply_image_test.go`), `docs/architecture.md`.

**Produces:** `ChatMessage.Images []string` (empty → JSON byte-identical; else `content` = image parts then `{"type":"text","text":…}`); `contextbuilder.ImageRef{DataURL string; Tokens int}`, `Input.Image *ImageRef`, `const AppearanceLabel = "【你的外观参考图（这就是你自己的样子，不是对方发来的图片）】"`; Build puts the image on the first user message (label line + "\n" prepended to its text); `BaseTokens`/`EstimatedTokens` add `Image.Tokens + Estimate(label)`; `Output.Messages` carry `Images` (extend the message type used by Build/call). Runner: load `GetPersonaImage(id,false)` when `AppearanceImage != "" && !AppearanceImageSkip`; in `o.call`, strip images when `d.Profile.Config.Modalities` lacks `"image"`; never set `NeedsImage`; read error → warn, no image.
- [ ] Tests: `TestChatMessage_JSONUnchangedWithoutImages`, `TestChatMessage_ImageParts`, `TestBuild_ImageOnFirstUserMessage` (with and without history), `TestBaseTokens_CountsImage`, `TestReplyImage_SentWhenProfileSupportsImage`, `TestReplyImage_DroppedWithoutImageModality`, `TestReplyImage_DroppedOnReroute`, `TestReplyImage_SkipFlag`, `TestReplyImage_MissingImageStillReplies`, `TestReplyImage_SystemPromptUnchanged`.
- [ ] Commit `feat: attach the persona appearance image to replies`.

### Task 10: Vision profile and describe

**Files:** modify `internal/config/validate.go` (+test), `internal/router/router.go` (+test: `RoleVision`, `func (r *Router) Profile(role Role) (*Profile, bool)`, never routed), `internal/core/orchestrator.go` (`maxTokenFactor` excludes vision), `internal/store/usage.go` (`PurposeVision`), `internal/panel/web/lib/format.js` (purpose label 识图); create `internal/core/describe.go` (+test).

**Produces:** `var ErrNoVision = errors.New("core: no vision model configured")`; `func (o *Orchestrator) DescribeAppearance(ctx context.Context, imageID, actor string) (string, error)` — loads original, `imaging.Scaled(…,1536)`, admission lease on profile `vision`, single user message with the image part (uses Task 9's `ChatMessage.Images`), fixed Chinese prompt meeting spec appearance §4.4, 40 s timeout, records usage `PurposeVision`, returns trimmed final text (no reasoning).
- [ ] Tests: `TestValidate_VisionNeedsImageModality`, `TestRouter_NeverRoutesVision`, `TestDescribe_SendsImageAndRecordsUsage` (fake OpenAI server asserts an `image_url` part with `data:image/jpeg;base64,`), `TestDescribe_NoVision503`, `TestDescribe_Timeout`.
- [ ] Commit `feat(core): vision profile and appearance description`.

### Task 11: Panel API — templates and images

**Files:** create `internal/panel/templates.go`, `internal/panel/images.go` (+tests); modify `common.go` (Deps: describe service), routes, `respond.go`, `internal/core` maintenance (hourly `PurgePersonaImages(now-7d)`, cleanup label), `internal/app/app.go`, `docs/panel.md`, `docs/deployment.md` (DB grows with images; vision config example), `conloom.example.yaml` (commented `vision` profile with placeholder model name, `appearance` example, `modalities` note).

**Produces:** endpoints in spec appearance §4.7. Upload: `http.MaxBytesReader` 8 MiB (separate from the 1 MiB JSON limit), body is the raw image, `imaging.Process`, `PutPersonaImage`, audit `persona.image.upload`, 201 `{id, width, height, reply_tokens}`. `GET persona-images/{id}?size=reply|original` returns bytes with stored MIME (reply copy is image/jpeg). Describe: 503 when no vision, 504 on timeout, audit `persona.appearance.describe`. Template endpoints audit `persona.template.put|reset|delete`; template list includes `users` (bots using it as default).
- [ ] Tests: `TestTemplatesAPI_CRUDResetDelete`, `TestTemplatesAPI_NameRules`, `TestImagesAPI_UploadGetAndAudit`, `TestImagesAPI_Rejects` (too big 413/400, non-image, bomb), `TestImagesAPI_CSRF`, `TestDescribeAPI_NoVision503`, `TestMaintenance_PurgesImages`.
- [ ] Commit `feat(panel): template and image endpoints`.

### Task 12: Panel UI — templates page and shared persona form

**Files:** create `internal/panel/web/views/templates.js`, `internal/panel/web/views/personaform.js`; modify `web/app.js` (nav `['templates','人设模板']`, routes `/templates`, `/templates/:name`), `views/conversation.js` (persona tab uses `personaform.js`), `web/lib/api.js` (`upload(path, blob)` sending raw body with `Content-Type: blob.type` and CSRF), `web/style.css`, `web_test.go`, `docs/panel.md`.

Behaviour: spec appearance §4.6 — list, edit with appearance textarea, thumbnail (`<img src="api/persona-images/<id>?size=reply&v=<id>">`), upload (preview via `FileReader.readAsDataURL`), "生成外观介绍" fills the textarea (not saved until 保存), "回复时附带外观图" checkbox (checked = `!appearance_image_skip`), save/reset/delete/copy, note about existing conversations. Bars editor and all existing persona fields keep working in both places.
- [ ] Tests: web_test list/unsafe checks; Go test serving the new views; `node --check`.
- [ ] Commit `feat(panel): persona templates page with appearance reference`.

---

## Deployment notes (controller)

- Merge with the deployer's uncommitted relationship-hint WIP exactly as before (stash → ff → pop; keep it uncommitted).
- Live config: add `model_profiles.vision` (model `grok-4.20-0309-reasoning`, gateway env vars as long_context, `modalities: [text, image]`); set `short_context.modalities: [text, image]` only after a real image request succeeds (verify via the describe endpoint and one reply).
- Fetch the 立绘 for the live templates from the wiki, upload through the panel API, describe, save as template overrides; re-apply to live conversations only where their current persona equals the template apart from the new fields.
