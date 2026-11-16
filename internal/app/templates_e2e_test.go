package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/app"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/panel"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// legacyTemplate is a fake persona tagged with tag.
func legacyTemplate(tag string) conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName, p.CharacterName = "人格"+tag, "小红"
	return p
}

// startAndClose runs app.New for cfg, logging to the returned buffer, and
// closes the app.
func startAndClose(t *testing.T, cfg *config.Config) (*syncBuffer, error) {
	t.Helper()
	logs := &syncBuffer{}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(logs, nil)), app.Options{})
	if err == nil {
		if err := a.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
	return logs, err
}

// openDB opens the database of cfg, creating its data directory.
func openDB(t *testing.T, cfg *config.Config) *sqlite.DB {
	t.Helper()
	if err := os.MkdirAll(cfg.Storage.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(context.Background(), cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// storedTemplates maps each template stored in db to its record.
func storedTemplates(t *testing.T, db *sqlite.DB) map[string]store.PersonaTemplateRecord {
	t.Helper()
	rows, unreadable, err := db.ListPersonaTemplates(context.Background())
	if err != nil || len(unreadable) != 0 {
		t.Fatalf("ListPersonaTemplates: unreadable %+v, %v", unreadable, err)
	}
	out := map[string]store.PersonaTemplateRecord{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

type migrateDetail struct {
	Imported []string `json:"imported"`
	Skipped  []string `json:"skipped"`
}

// migrateAudits returns the details of the persona.template.migrate audits in
// db, newest first.
func migrateAudits(t *testing.T, db *sqlite.DB) []migrateDetail {
	t.Helper()
	events, err := db.ListAudit(context.Background(), store.AuditFilter{Kind: "persona.template.migrate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var out []migrateDetail
	for _, e := range events {
		var d migrateDetail
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil || e.Actor != "system" || e.BotInstanceID != "" {
			t.Fatalf("migrate audit = %+v (%v), want by system for no bot", e, err)
		}
		out = append(out, d)
	}
	return out
}

func logLines(logs *syncBuffer, level string, parts ...string) int {
	n := 0
	for _, l := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(l, "level="+level) {
			continue
		}
		all := true
		for _, p := range parts {
			all = all && strings.Contains(l, p)
		}
		if all {
			n++
		}
	}
	return n
}

func TestMigrate_ImportsLegacy(t *testing.T) {
	cfg := testConfig(t, configParams{modelURL: "http://127.0.0.1:1"})
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{"default": legacyTemplate("默认"), "lighthouse": legacyTemplate("灯塔")}
	cfg.Bots[0].PersonaTemplate = "lighthouse"
	before := time.Now()
	e := newEnv(t, cfg)
	e.observe(alice, "大家好")
	state, err := e.client.GetPersona(context.Background(), "group_10001")
	if err != nil || state.Version != 1 || !reflect.DeepEqual(state.Persona, legacyTemplate("灯塔")) {
		t.Fatalf("persona of the config bot's group = %+v, %v; want the imported lighthouse", state, err)
	}
	e.close()

	db := openDB(t, cfg)
	got := storedTemplates(t, db)
	if len(got) != 2 {
		t.Fatalf("stored templates = %+v, want default and lighthouse", got)
	}
	for name, doc := range cfg.LegacyPersonaTemplates {
		r := got[name]
		if !reflect.DeepEqual(r.Persona, doc) || r.UpdatedBy != "system" || r.UpdatedAt.Before(before.Add(-time.Second)) || r.UpdatedAt.After(time.Now()) {
			t.Errorf("stored %s = %+v, want the config file's version stored by system now", name, r)
		}
	}
	if v, ok, err := db.GetMeta(context.Background(), store.MetaConfigTemplatesMigrated); err != nil || !ok || v == "" {
		t.Errorf("migration marker = %q, %v, %v; want it set", v, ok, err)
	}
	if audits := migrateAudits(t, db); len(audits) != 1 ||
		!slices.Equal(audits[0].Imported, []string{"default", "lighthouse"}) || len(audits[0].Skipped) != 0 || audits[0].Skipped == nil {
		t.Errorf("migrate audits = %+v, want one importing default and lighthouse, skipping none", audits)
	}
}

func TestMigrate_DBWinsAndMarker(t *testing.T) {
	cfg := testConfig(t, configParams{modelURL: "http://127.0.0.1:1"})
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{
		"default": legacyTemplate("配置默认"), "lighthouse": legacyTemplate("配置灯塔"),
		"cat": legacyTemplate("配置猫"), "owl": legacyTemplate("配置猫头鹰"),
	}
	db := openDB(t, cfg)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	online := map[string]store.PersonaTemplateRecord{}
	for _, name := range []string{"lighthouse", "cat", "owl"} {
		r := store.PersonaTemplateRecord{Name: name, Persona: legacyTemplate("在线" + name), UpdatedBy: "panel:admin", CreatedAt: at, UpdatedAt: at}
		if err := db.PutPersonaTemplate(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		online[name] = r
	}

	logs, err := startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	got := storedTemplates(t, db)
	if len(got) != 4 {
		t.Fatalf("stored templates = %+v, want the three stored ones and default", got)
	}
	for name, want := range online {
		r := got[name]
		if !reflect.DeepEqual(r.Persona, want.Persona) || r.UpdatedBy != want.UpdatedBy || !r.UpdatedAt.Equal(at) || !r.CreatedAt.Equal(at) {
			t.Errorf("stored %s = %+v, want it untouched %+v", name, r, want)
		}
	}
	if r := got["default"]; !reflect.DeepEqual(r.Persona, legacyTemplate("配置默认")) || r.UpdatedBy != "system" {
		t.Errorf("stored default = %+v, want the config file's", r)
	}
	if _, ok, err := db.GetMeta(context.Background(), store.MetaConfigTemplatesMigrated); err != nil || !ok {
		t.Errorf("migration marker = %v, %v; want it set", ok, err)
	}
	audits := migrateAudits(t, db)
	if len(audits) != 1 || !slices.Equal(audits[0].Imported, []string{"default"}) || !slices.Equal(audits[0].Skipped, []string{"cat", "lighthouse", "owl"}) {
		t.Errorf("migrate audits = %+v, want default imported, cat, lighthouse and owl skipped", audits)
	}
	if n := logLines(logs, "INFO", "template=default"); n != 1 {
		t.Errorf("log lines on importing default = %d, want 1:\n%s", n, logs)
	}
	for _, name := range []string{"cat", "lighthouse", "owl"} {
		if n := logLines(logs, "INFO", "template="+name); n != 1 {
			t.Errorf("log lines on skipping %s = %d, want 1:\n%s", name, n, logs)
		}
	}
	if n := logLines(logs, "WARN", "persona_templates"); n != 1 {
		t.Errorf("warnings to remove persona_templates = %d, want 1:\n%s", n, logs)
	}
}

func TestMigrate_SecondStartRefuses(t *testing.T) {
	cfg := testConfig(t, configParams{modelURL: "http://127.0.0.1:1"})
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{"default": legacyTemplate("默认")}
	if _, err := startAndClose(t, cfg); err != nil {
		t.Fatalf("first start: %v", err)
	}
	db := openDB(t, cfg)
	edited := legacyTemplate("面板改过")
	if err := db.PutPersonaTemplate(context.Background(), store.PersonaTemplateRecord{Name: "default", Persona: edited, UpdatedBy: "panel:admin"}); err != nil {
		t.Fatal(err)
	}

	_, err := startAndClose(t, cfg)
	if !errors.Is(err, store.ErrAlreadyMigrated) {
		t.Fatalf("second start with persona_templates err = %v, want ErrAlreadyMigrated", err)
	}
	for _, want := range []string{"persona_templates", "删除", "数据库"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if r := storedTemplates(t, db)["default"]; !reflect.DeepEqual(r.Persona, edited) {
		t.Errorf("stored default after a refused start = %+v, want the panel's edit kept", r.Persona)
	}
	if audits := migrateAudits(t, db); len(audits) != 1 {
		t.Errorf("migrate audits = %+v, want only the first start's", audits)
	}

	cfg.LegacyPersonaTemplates = nil
	if _, err := startAndClose(t, cfg); err != nil {
		t.Fatalf("start after removing persona_templates: %v", err)
	}
}

func TestMigrate_NoLegacyNoop(t *testing.T) {
	cfg := testConfig(t, configParams{modelURL: "http://127.0.0.1:1"})
	cfg.LegacyPersonaTemplates = map[string]conloomapi.PersonaDoc{}
	logs, err := startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	db := openDB(t, cfg)
	if got := storedTemplates(t, db); len(got) != 0 {
		t.Errorf("stored templates = %+v, want none", got)
	}
	if _, ok, err := db.GetMeta(context.Background(), store.MetaConfigTemplatesMigrated); err != nil || ok {
		t.Errorf("migration marker set = %v, %v; want nothing recorded", ok, err)
	}
	if audits := migrateAudits(t, db); len(audits) != 0 {
		t.Errorf("migrate audits = %+v, want none", audits)
	}
	if strings.Contains(logs.String(), "persona_templates") {
		t.Errorf("logs mention persona_templates:\n%s", logs)
	}
}

func TestDirectory_ConfigBotMissingTemplate(t *testing.T) {
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: "http://127.0.0.1:1"}, hash)
	cfg.Bots[0].PersonaTemplate = "night-owl"
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe with a template that exists nowhere: %v", err)
	}
	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)

	type botView struct {
		ID              string   `json:"id"`
		PersonaTemplate string   `json:"persona_template"`
		Problems        []string `json:"problems"`
	}
	configBot := func() botView {
		t.Helper()
		var resp struct {
			Bots []botView `json:"bots"`
		}
		p.get("/panel/api/bots", &resp)
		for _, b := range resp.Bots {
			if b.ID == botID {
				return b
			}
		}
		t.Fatalf("bots %+v lack %s", resp.Bots, botID)
		return botView{}
	}
	want := []string{"人设模板“night-owl”已不存在，暂用 default"}
	if b := configBot(); b.PersonaTemplate != "default" || !slices.Equal(b.Problems, want) {
		t.Fatalf("config bot = %+v, want default with the problem %q", b, want)
	}
	e.observe(alice, "大家好")
	state, err := e.client.GetPersona(context.Background(), "group_10001")
	if err != nil || state.Version != 1 || !reflect.DeepEqual(state.Persona, conloomapi.DefaultPersona()) {
		t.Fatalf("persona of the config bot's group = %+v, %v; want the built-in default", state, err)
	}

	owl := legacyTemplate("夜猫")
	if status, raw := p.do("PUT", "/panel/api/persona-templates/night-owl", map[string]any{"persona": owl}); status != 201 {
		t.Fatalf("PUT night-owl: status %d, body %s", status, raw)
	}
	if b := configBot(); b.PersonaTemplate != "night-owl" || len(b.Problems) != 0 {
		t.Errorf("config bot after its template was created = %+v, want night-owl without problems", b)
	}
	status, raw := p.do("DELETE", "/panel/api/persona-templates/night-owl", nil)
	if status != 200 || !strings.Contains(string(raw), botID) {
		t.Errorf("DELETE night-owl: status %d, body %s; want 200 naming %s", status, raw, botID)
	}
	if b := configBot(); b.PersonaTemplate != "default" || !slices.Equal(b.Problems, want) {
		t.Errorf("config bot after its template was deleted = %+v, want default with the problem %q", b, want)
	}
}

func TestTemplates_PanelExportImport(t *testing.T) {
	hash, err := panel.HashPassword(panelPassword, panel.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	cfg := panelTestConfig(t, configParams{modelURL: "http://127.0.0.1:1"}, hash)
	cfg.Bots[0].PersonaTemplate = "night-owl"
	e := newEnv(t, cfg)
	p := panelLogin(t, e.srv.URL)
	problems := func() []string {
		t.Helper()
		var resp struct {
			Bots []struct {
				ID       string   `json:"id"`
				Problems []string `json:"problems"`
			} `json:"bots"`
		}
		p.get("/panel/api/bots", &resp)
		for _, b := range resp.Bots {
			if b.ID == botID {
				return b.Problems
			}
		}
		t.Fatalf("bots %+v lack %s", resp.Bots, botID)
		return nil
	}
	if got := problems(); len(got) != 1 {
		t.Fatalf("config bot problems = %v, want its missing template", got)
	}

	// The built-in default, exported and imported as night-owl.
	var f persona.ExchangeFile
	p.get("/panel/api/persona-templates/default/export", &f)
	if len(f.Templates) != 1 || f.Templates[0].Name != "default" {
		t.Fatalf("export of default = %+v", f)
	}
	f.Templates[0].Name = "night-owl"
	f.Templates[0].Persona = legacyTemplate("夜猫")
	status, raw := p.do("POST", "/panel/api/persona-templates/import", f)
	if status != 200 || !strings.Contains(string(raw), `"name":"night-owl"`) {
		t.Fatalf("import: status %d, body %s", status, raw)
	}
	if got := problems(); len(got) != 0 {
		t.Errorf("config bot problems after its template was imported = %v, want none", got)
	}
	var tpl struct {
		Source  string                `json:"source"`
		Persona conloomapi.PersonaDoc `json:"persona"`
	}
	p.get("/panel/api/persona-templates/night-owl", &tpl)
	if tpl.Source != "db" || !reflect.DeepEqual(tpl.Persona, legacyTemplate("夜猫")) {
		t.Errorf("imported night-owl = %+v", tpl)
	}

	status, raw = p.do("POST", "/panel/api/persona-templates/import?on_conflict=fail", f)
	if status != 409 || !strings.Contains(string(raw), `"code":"TEMPLATE_EXISTS"`) || !strings.Contains(string(raw), `"names":["night-owl"]`) {
		t.Errorf("second import: status %d, body %s; want 409 TEMPLATE_EXISTS naming night-owl", status, raw)
	}
}
