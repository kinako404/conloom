# Built-in Persona Template Storage with JSON Export/Import — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persona templates live only in the database (built-in `default` fallback), legacy `persona_templates` in the config file is migrated once at startup and then rejected, and templates can be exported to / imported from JSON with the appearance image embedded as base64.

**Architecture:** Drop the config layer from `persona.Service`'s catalog; a one-time startup migration (guarded by a new `app_meta` row) copies legacy config templates into `persona_templates`; a small exchange codec (format `conloom.persona-template` v1) plus `persona.Service.Import` handle validation, image processing (`imaging.Process`) and conflict modes; panel endpoints and the templates page expose export/import.

**Tech Stack:** Go 1.26 stdlib (`encoding/json`, `encoding/base64`), SQLite (modernc), vanilla JS panel. No new dependencies.

**Spec (Chinese, binding):** `docs/superpowers/specs/2026-11-15-builtin-templates-design.md`.

## Global Constraints

- No new Go module dependencies. `CGO_ENABLED=0 go build ./...`; `CGO_ENABLED=1 GOFLAGS=-mod=readonly go test -race -count=1 ./...`; `go vet ./...`; `gofmt -l .` clean.
- Public repo hygiene: no real IDs, tokens, hostnames, nicknames or character names; tests use fake data (bots `qq-main`/`tg-main`, names 小明/小红, `example.invalid`) and images generated in tests; no images committed.
- Unchanged behaviour: persona versions snapshot templates on first reply; protected mode (`ValidateContent` on every write/import); group chats; adapters.
- Fixed values: format string `conloom.persona-template`, `version` 1; import body ≤64 MiB; template name rule `^[a-z0-9_-]{1,32}$` (existing); rename suffix `-2`, `-3`, … (truncate the base so the result is ≤32 chars); `on_conflict` ∈ `fail` (default) | `overwrite` | `rename`; app_meta key `config_templates_migrated`; startup refusal exit code 78 with a Chinese message; download names `persona-<name>.json` and `persona-templates-<YYYYMMDD>.json`; audits `persona.template.migrate`, `persona.template.export`, `persona.template.import`.
- Panel JS: no `innerHTML`/inline style/`http(s)://`/`blob:`; text via `h()`/textContent; writes via existing CSRF helpers.

## Review Focus

1. Upgrade on the live layout: legacy config has `default` + three templates, DB already has rows for the three → after first start DB keeps its three rows untouched, gains `default` from config, marker set; second start with the key still present exits 78 (Task 1 `TestMigrate_DBWinsAndMarker`, `TestMigrate_SecondStartRefuses`).
2. A config bot whose `persona_template` no longer exists anywhere → service starts, bot falls back to `default`, problem shown (Task 1 `TestDirectory_ConfigBotMissingTemplate`).
3. Import of a file with one bad entry among good ones → 400, nothing written (Task 2 `TestImport_AllOrNothing`).
4. Import whose embedded image is a decompression bomb or not PNG/JPEG → 400 (Task 2 `TestImport_RejectsBadImage`).
5. Export → import round trip on another (fresh) store reproduces persona and the same image ID (Task 2 `TestExchange_RoundTripSameImageID`).

---

### Task 1: DB-only catalog, legacy migration, config changes

**Files:** modify `internal/config/config.go`, `internal/config/validate.go` (+tests), `internal/persona/persona.go` (+tests), `internal/botdir` or core panel-bot/problem code (config bots get "模板已不存在" problems + runtime `default`), `internal/app/app.go` (run migration before `LoadTemplates`; refusal → exit 78 via the existing config-error path), `cmd/conloom` only if needed for the exit code; create `internal/store/sqlite/migrations/0015_app_meta.sql`, store methods `GetMeta(ctx, key) (string, bool, error)`, `MigrateConfigTemplates(ctx, rows []PersonaTemplateRecord, now time.Time) (imported, skipped []string, err error)` (one tx: insert missing names, skip existing, set marker; returns `ErrAlreadyMigrated` when the marker exists); docs (`configuration.md`, `deployment.md` upgrade note, `architecture.md`), `conloom.example.yaml` (remove `persona_templates`).

**Interfaces — Produces:** `config.Config.LegacyPersonaTemplates map[string]conloomapi.PersonaDoc` (yaml `persona_templates`, still parsed and content-validated, never defaulted); `persona.Service` catalog = DB rows + built-in `default` when absent (`TemplateSource` values `"db"`, `"builtin"`); `ResetTemplate` valid only for `default`; `DeleteTemplate` in-use check covers config bots too (via the users callback); `ReferencedImages()` = DB templates only; `store.ErrAlreadyMigrated`.

- [ ] Tests: `TestMigrate_ImportsLegacy`, `TestMigrate_DBWinsAndMarker`, `TestMigrate_SecondStartRefuses` (app-level: exit/err classified as config error), `TestMigrate_NoLegacyNoop`, `TestCatalog_BuiltinDefault` (no DB default → builtin; save default → db; reset → builtin), `TestCatalog_DeleteInUseByConfigBot`, `TestDirectory_ConfigBotMissingTemplate`, `TestValidate_ConfigBotTemplateNotChecked`; update tests that relied on config templates.
- [ ] Commit `feat(persona): templates live in the database; migrate legacy config templates once`.

### Task 2: Exchange format and import

**Files:** create `internal/persona/exchange.go` (+`exchange_test.go`); modify `internal/persona/persona.go` (Import), store (one-tx `ImportPersonaTemplates(ctx, images []PersonaImage, rows []PersonaTemplateRecord) error` or reuse existing methods inside `inTx`).

**Interfaces — Produces:**
```go
const ExchangeFormat = "conloom.persona-template"; const ExchangeVersion = 1
type ExchangeFile struct { Format string `json:"format"`; Version int `json:"version"`; ExportedAt time.Time `json:"exported_at"`; Templates []ExchangeTemplate `json:"templates"` }
type ExchangeTemplate struct { Name string `json:"name"`; Persona conloomapi.PersonaDoc `json:"persona"`; AppearanceImage *ExchangeImage `json:"appearance_image,omitempty"` }
type ExchangeImage struct { MIME string `json:"mime"`; Data string `json:"data"` } // base64 std encoding of the original
func (s *Service) Export(ctx context.Context, names []string) (ExchangeFile, error) // names nil = all (incl. builtin default when absent from DB)
type ConflictMode string // "fail" | "overwrite" | "rename"
type ImportResult struct { Name, Requested string; Created bool }
var ErrImportConflict = errors.New("persona: template names already exist") // wrapped with the names
func (s *Service) Import(ctx context.Context, f ExchangeFile, mode ConflictMode, actor string) ([]ImportResult, error)
```
Rules: spec §3.4–§3.5 (all-or-nothing validation incl. `imaging.Process` per image, duplicate names in file rejected, image ID recomputed and written into `appearance_image`, rename suffix rule, one transaction, refresh catalog, users callback refresh hook for panel bot problems).
- [ ] Tests: `TestExchange_RoundTripSameImageID`, `TestImport_AllOrNothing`, `TestImport_RejectsBadImage`, `TestImport_ConflictModes` (fail→ErrImportConflict lists names; overwrite; rename `-2`/`-3` and 32-char truncation), `TestImport_RejectsWrongFormatOrVersion`, `TestImport_DuplicateNamesInFile`, `TestImport_ProtectedModeRule` (minor+adult → invalid), `TestExport_BuiltinDefault`.
- [ ] Commit `feat(persona): template export and import`.

### Task 3: Panel API

**Files:** modify `internal/panel/templates.go` (+tests), routes, `respond.go` (map `ErrImportConflict`→409 with names, invalid→400), `docs/panel.md`, `docs/api.md`.

**Produces:** spec §3.7 endpoints. Export responses: `Content-Type: application/json`, `Content-Disposition: attachment; filename="persona-<name>.json"` (all: `persona-templates-<YYYYMMDD>.json` in server timezone); audit `persona.template.export` (names only). Import: raw JSON body via `http.MaxBytesReader` 64 MiB (413 over), decode with unknown-field rejection, shares the existing one-at-a-time image lock (409 busy), audit `persona.template.import` (names, mode, created/overwritten; never data). Reset: 400 for non-default. Template list/detail: drop config-original fields; `source` only `db|builtin`.
- [ ] Tests: `TestTemplatesAPI_ExportHeadersAndBody`, `TestTemplatesAPI_ExportAll`, `TestTemplatesAPI_ImportModes`, `TestTemplatesAPI_ImportTooLarge413`, `TestTemplatesAPI_ImportCSRF`, `TestTemplatesAPI_ResetOnlyDefault`.
- [ ] Commit `feat(panel): template export and import endpoints`.

### Task 4: Panel UI

**Files:** modify `internal/panel/web/views/templates.js`, `views/personaform.js` if source UI lives there, `web/style.css`, `web_test.go` if new files, `docs/panel.md`.

Behaviour: spec §3.6 — per-template 导出 (`<a href="api/persona-templates/<name>/export" download>`), 导出全部, 导入 (file input → `File.text()` → POST with `on_conflict=fail` → on 409 dialog listing names with 覆盖 / 改名导入 / 取消), result message lists final names; remove 恢复为配置文件版本 and config-source labels; `default` shows 恢复为内置默认.
- [ ] Tests: web_test unsafe-API checks; Go static test that the new strings/links are served; `node --check`.
- [ ] Commit `feat(panel): export and import persona templates`.

---

## Deployment notes (controller)

- Deploy with the live config unchanged (migration imports `default`, skips the three DB templates), confirm the marker and audit, then remove `persona_templates` from `/opt/conloom/conloom.yaml` (backup first) and restart; confirm the bots' templates resolve (no "模板已不存在").
- Merge with the deployer's uncommitted WIP as before.
