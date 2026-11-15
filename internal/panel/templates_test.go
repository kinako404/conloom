package panel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type templateUserResp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type templateResp struct {
	Name            string                 `json:"name"`
	Source          string                 `json:"source"`
	PersonaName     string                 `json:"persona_name"`
	CharacterName   string                 `json:"character_name"`
	AppearanceImage string                 `json:"appearance_image"`
	Users           []templateUserResp     `json:"users"`
	UpdatedBy       string                 `json:"updated_by"`
	UpdatedAt       *time.Time             `json:"updated_at"`
	Persona         *conloomapi.PersonaDoc `json:"persona"`
}

func templatePath(name string, rest ...string) string {
	p := "/panel/api/persona-templates/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

func decodeTemplate(t *testing.T, w *httptest.ResponseRecorder, status int) templateResp {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body)
	}
	var v templateResp
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

func (h *writeHarness) templates() []templateResp {
	h.t.Helper()
	var resp struct {
		Templates []templateResp `json:"templates"`
	}
	h.getJSON("/panel/api/persona-templates", &resp)
	return resp.Templates
}

func (h *writeHarness) template(name string) templateResp {
	h.t.Helper()
	return decodeTemplate(h.t, h.get(templatePath(name)), http.StatusOK)
}

func (h *writeHarness) putTemplate(name string, doc conloomapi.PersonaDoc, reason string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.write(http.MethodPut, templatePath(name), map[string]any{"persona": doc, "reason": reason})
}

// checkTemplateAudits checks that kind was audited once for each of names, in
// any order, by the harness's user and for no bot, and returns the details.
func checkTemplateAudits(t *testing.T, h *writeHarness, kind string, names ...string) []string {
	t.Helper()
	events := h.audits(kind)
	var targets, details []string
	for _, e := range events {
		if e.Actor != "panel:"+testUser || e.BotInstanceID != "" {
			t.Errorf("%s audit = %+v, want by panel:%s for no bot", kind, e, testUser)
		}
		targets, details = append(targets, e.Target), append(details, e.Detail)
	}
	slices.Sort(targets)
	if want := slices.Sorted(slices.Values(names)); !slices.Equal(targets, want) {
		t.Errorf("%s audit targets = %v, want %v", kind, targets, want)
	}
	return details
}

// someContains reports whether one of details contains every one of want.
func someContains(details []string, want ...string) bool {
	return slices.ContainsFunc(details, func(d string) bool {
		for _, w := range want {
			if !strings.Contains(d, w) {
				return false
			}
		}
		return true
	})
}

func TestTemplatesAPI_CRUDResetDelete(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()

	list := h.templates()
	if len(list) != 2 || list[0].Name != "cat" || list[1].Name != "default" {
		t.Fatalf("templates = %+v, want cat and default", list)
	}
	if got := list[0]; got.Source != "db" || got.PersonaName != "cat" || got.CharacterName != "小猫" ||
		len(got.Users) != 0 || got.UpdatedBy != "panel:"+testUser || got.UpdatedAt == nil {
		t.Errorf("cat = %+v, want the stored template without users", got)
	}
	wantDefault := templateResp{Name: "default", Source: "builtin", PersonaName: conloomapi.DefaultPersona().PersonaName,
		Users: []templateUserResp{{dataBot, "织语", "config"}}}
	if !reflect.DeepEqual(list[1], wantDefault) {
		t.Errorf("default = %+v, want %+v", list[1], wantDefault)
	}
	if body := string(h.getJSON("/panel/api/persona-templates", nil)); !strings.Contains(body, `"users":[]`) ||
		strings.Contains(body, `"persona":`) {
		t.Errorf("list = %s, want users as an empty list and no persona", body)
	}
	if body := string(h.getJSON(templatePath("cat"), nil)); strings.Contains(body, `"config"`) {
		t.Errorf("GET cat = %s, want no config file's version", body)
	}
	got := h.template("cat")
	if got.Persona == nil || !reflect.DeepEqual(*got.Persona, catPersona) || got.Source != "db" {
		t.Errorf("GET cat = %+v, want the stored version", got)
	}

	// Edit a stored template.
	edited := catPersona
	edited.CharacterName = "大猫"
	got = decodeTemplate(t, h.putTemplate("cat", edited, "改名"), http.StatusOK)
	if got.Source != "db" || got.CharacterName != "大猫" || got.Persona == nil || got.Persona.CharacterName != "大猫" ||
		got.UpdatedBy != "panel:"+testUser || got.UpdatedAt == nil {
		t.Errorf("PUT cat = %+v, want the edit", got)
	}
	if doc, _ := h.personas.Template("cat"); doc.CharacterName != "大猫" {
		t.Errorf("catalog cat = %+v, want the edit", doc)
	}

	// Save default, then reset it to the built-in one.
	got = decodeTemplate(t, h.write(http.MethodPost, templatePath("default", "reset"), nil), http.StatusOK)
	if got.Source != "builtin" {
		t.Errorf("reset of the built-in default = %+v, want it unchanged", got)
	}
	ownDefault := lighthousePersona()
	got = decodeTemplate(t, h.putTemplate("default", ownDefault, ""), http.StatusOK)
	if got.Source != "db" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, ownDefault) {
		t.Errorf("PUT default = %+v, want the saved default", got)
	}
	got = decodeTemplate(t, h.write(http.MethodPost, templatePath("default", "reset"), nil), http.StatusOK)
	if got.Source != "builtin" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, conloomapi.DefaultPersona()) ||
		got.UpdatedBy != "" || got.UpdatedAt != nil {
		t.Errorf("reset default = %+v, want the built-in default", got)
	}
	if rows, _, err := h.db.ListPersonaTemplates(ctx); err != nil || len(rows) != 1 || rows[0].Name != "cat" {
		t.Errorf("stored templates after reset = %+v, %v; want only cat", rows, err)
	}

	// A new template, used by a panel bot.
	night := lighthousePersona()
	night.PersonaName = "夜猫"
	got = decodeTemplate(t, h.putTemplate("night-owl", night, ""), http.StatusCreated)
	if got.Name != "night-owl" || got.Source != "db" || got.PersonaName != "夜猫" {
		t.Errorf("PUT night-owl = %+v, want a new template", got)
	}
	night.Description = "夜里醒着。"
	got = decodeTemplate(t, h.putTemplate("night-owl", night, "补充设定"), http.StatusOK)
	if got.Persona == nil || got.Persona.Description != "夜里醒着。" {
		t.Errorf("second PUT night-owl = %+v", got)
	}
	bot := h.createBot("小红", "night-owl")
	if got := h.template("night-owl"); !reflect.DeepEqual(got.Users, []templateUserResp{{bot.ID, "小红", "panel"}}) {
		t.Errorf("night-owl users = %+v, want 小红", got.Users)
	}
	e := errorBody(t, h.write(http.MethodDelete, templatePath("night-owl"), nil), http.StatusConflict, conloomapi.CodeTemplateInUse)
	if !strings.Contains(e.Message, "小红") || e.Retryable {
		t.Errorf("in-use delete = %+v, want the bot 小红 named, not retryable", e)
	}
	if _, ok := h.personas.Template("night-owl"); !ok {
		t.Fatal("template in use was deleted")
	}

	for _, name := range []string{"night-owl", "cat", "nope"} {
		errorBody(t, h.write(http.MethodPost, templatePath(name, "reset"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	errorBody(t, h.write(http.MethodDelete, templatePath("default"), nil), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodDelete, templatePath("nope"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	h.getErr(templatePath("nope"), http.StatusNotFound, conloomapi.CodeNotFound)

	decodeBot(t, h.write(http.MethodPut, botPath(bot.ID), map[string]any{"name": "小红", "persona_template": "cat", "skills": []string{}}), http.StatusOK)
	if w := h.write(http.MethodDelete, templatePath("night-owl"), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete free template: %d %s", w.Code, w.Body)
	}
	h.getErr(templatePath("night-owl"), http.StatusNotFound, conloomapi.CodeNotFound)
	if _, ok := h.personas.Template("night-owl"); ok {
		t.Error("deleted template still in the catalog")
	}

	puts := checkTemplateAudits(t, h, "persona.template.put", "cat", "default", "night-owl", "night-owl")
	for _, want := range [][]string{
		{`"source":"db"`, `"created":false`, `"reason":"改名"`},
		{`"source":"db"`, `"created":true`},
		{`"source":"db"`, `"created":false`, `"reason":"补充设定"`},
	} {
		if !someContains(puts, want...) {
			t.Errorf("put audit details %v: none has all of %v", puts, want)
		}
	}
	checkTemplateAudits(t, h, "persona.template.reset", "default", "default")
	checkTemplateAudits(t, h, "persona.template.delete", "night-owl")
}

func TestTemplatesAPI_ClearsPanelBotProblem(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	// A stored panel bot whose template is gone, as after its row was found
	// invalid at startup.
	if err := h.db.CreatePanelBot(ctx, store.PanelBot{ID: "panel-abcd1234", Name: "小红", PersonaTemplate: "night-owl",
		Skills: []string{}, CreatedBy: "panel:" + testUser}); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.d.PanelBots.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if b := h.bots().Bots[1]; b.PersonaTemplate != "default" || len(b.Problems) != 1 {
		t.Fatalf("bot with a missing template = %+v, want default and one problem", b)
	}

	decodeTemplate(t, h.putTemplate("night-owl", lighthousePersona(), ""), http.StatusCreated)
	if b := h.bots().Bots[1]; b.PersonaTemplate != "night-owl" || len(b.Problems) != 0 {
		t.Errorf("bot after its template was created = %+v, want night-owl without problems", b)
	}
	errorBody(t, h.write(http.MethodDelete, templatePath("night-owl"), nil), http.StatusConflict, conloomapi.CodeTemplateInUse)
}

func TestTemplatesAPI_NameRules(t *testing.T) {
	h := newBotsHarness(t)
	doc := lighthousePersona()

	for _, name := range []string{"Upper", strings.Repeat("a", 33), "有中文", "a b", "a.b", "a+b"} {
		e := errorBody(t, h.putTemplate(name, doc, ""), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, "name must be 1 to 32 lowercase letters") {
			t.Errorf("PUT %q message = %q", name, e.Message)
		}
		if _, ok := h.personas.Template(name); ok {
			t.Errorf("invalid name %q stored", name)
		}
	}
	for _, name := range []string{strings.Repeat("z", 32), "a_b-9"} {
		decodeTemplate(t, h.putTemplate(name, doc, ""), http.StatusCreated)
	}

	both := doc
	both.Minor, both.Adult = true, true
	unknown := doc
	unknown.AppearanceImage = strings.Repeat("ab", 32)
	for _, c := range []struct {
		name, body, want string
	}{
		{"minor and adult", mustJSON(t, map[string]any{"persona": both}), "minor"},
		{"unknown image", mustJSON(t, map[string]any{"persona": unknown}), "unknown appearance_image"},
		{"no persona", `{"reason":"x"}`, "persona is required"},
		{"unknown field", `{"persona":{},"extra":1}`, "unknown field"},
		{"not JSON", `{`, "invalid JSON"},
	} {
		e := errorBody(t, h.write(http.MethodPut, templatePath("checked"), c.body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, c.want) {
			t.Errorf("%s: message %q lacks %q", c.name, e.Message, c.want)
		}
	}
	if _, ok := h.personas.Template("checked"); ok {
		t.Error("rejected template stored")
	}
	if got := len(h.audits("persona.template.put")); got != 2 {
		t.Errorf("put audits = %d, want 2 (only the stored ones)", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const templatesExportPath = "/panel/api/persona-templates/export"

// importPath is the import API's path with on_conflict mode, none when empty.
func importPath(mode string) string {
	p := "/panel/api/persona-templates/import"
	if mode != "" {
		p += "?on_conflict=" + url.QueryEscape(mode)
	}
	return p
}

// exchangeFile is an exchange file of templates.
func exchangeFile(templates ...persona.ExchangeTemplate) persona.ExchangeFile {
	return persona.ExchangeFile{
		Format: persona.ExchangeFormat, Version: persona.ExchangeVersion,
		ExportedAt: time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC), Templates: templates,
	}
}

// embedded is data embedded as an exchange file's appearance image.
func embedded(data []byte) *persona.ExchangeImage {
	return &persona.ExchangeImage{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data)}
}

// decodeExport checks that w is a JSON download named filename and returns
// the exchange file it carries, decoded without unknown fields.
func decodeExport(t *testing.T, w *httptest.ResponseRecorder, filename string) persona.ExchangeFile {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200 (body %s)", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("export Content-Type = %q, want application/json", ct)
	}
	disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != filename {
		t.Errorf("export Content-Disposition = %q (%s %v, %v), want an attachment named %q",
			w.Header().Get("Content-Disposition"), disposition, params, err, filename)
	}
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.DisallowUnknownFields()
	var f persona.ExchangeFile
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode export %.200s: %v", w.Body, err)
	}
	if f.Format != persona.ExchangeFormat || f.Version != persona.ExchangeVersion || f.ExportedAt.IsZero() {
		t.Errorf("export header = %q v%d at %v, want %q v%d with a time", f.Format, f.Version, f.ExportedAt,
			persona.ExchangeFormat, persona.ExchangeVersion)
	}
	return f
}

type importedResp struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Created   bool   `json:"created"`
}

func decodeImport(t *testing.T, w *httptest.ResponseRecorder) []importedResp {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("import status = %d, want 200 (body %.500s)", w.Code, w.Body)
	}
	var resp struct {
		Templates []importedResp `json:"templates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return resp.Templates
}

type conflictResp struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Retryable bool     `json:"retryable"`
	TraceID   string   `json:"trace_id"`
	Names     []string `json:"names"`
}

// decodeConflict checks that w is a 409 TEMPLATE_EXISTS and returns its error.
func decodeConflict(t *testing.T, w *httptest.ResponseRecorder) conflictResp {
	t.Helper()
	errorBody(t, w, http.StatusConflict, conloomapi.CodeTemplateExists)
	var resp struct {
		Error conflictResp `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return resp.Error
}

// noImageData fails t when one of details carries the image data, which is
// in standard base64 as b64.
func noImageData(t *testing.T, kind string, details []string, b64 string) {
	t.Helper()
	for _, d := range details {
		if len(d) > 300 || strings.Contains(d, b64[:40]) {
			t.Errorf("%s audit detail %.300s carries image data", kind, d)
		}
	}
}

func TestTemplatesAPI_ExportHeadersAndBody(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	data := pngData(t, testImage(64, 32, true))
	up := decodeUpload(t, h.upload(data, "image/png"))
	doc := lighthousePersona()
	doc.AppearanceImage = up.ID
	decodeTemplate(t, h.putTemplate("lighthouse", doc, ""), http.StatusCreated)

	w := h.get(templatePath("lighthouse", "export"))
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="persona-lighthouse.json"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("export headers = %v, want no-store and nosniff", w.Header())
	}
	f := decodeExport(t, w, "persona-lighthouse.json")
	if len(f.Templates) != 1 {
		t.Fatalf("export of lighthouse = %d templates, want 1", len(f.Templates))
	}
	got := f.Templates[0]
	if got.Name != "lighthouse" || !reflect.DeepEqual(got.Persona, doc) || got.AppearanceImage == nil ||
		got.AppearanceImage.MIME != "image/png" {
		t.Fatalf("exported lighthouse = %s %+v, image %+v; want the template with its image ID and the PNG",
			got.Name, got.Persona, got.AppearanceImage != nil)
	}
	if raw, err := base64.StdEncoding.DecodeString(got.AppearanceImage.Data); err != nil || !bytes.Equal(raw, data) {
		t.Errorf("exported image = %d bytes, %v; want the original upload", len(raw), err)
	}
	if body := w.Body.String(); strings.Contains(body, "panel:"+testUser) || strings.Contains(body, "updated") {
		t.Errorf("export carries who changed the template: %.300s", body)
	}

	// The built-in default, without an image.
	f = decodeExport(t, h.get(templatePath("default", "export")), "persona-default.json")
	if len(f.Templates) != 1 || f.Templates[0].Name != "default" ||
		!reflect.DeepEqual(f.Templates[0].Persona, conloomapi.DefaultPersona()) || f.Templates[0].AppearanceImage != nil {
		t.Errorf("export of the built-in default = %+v", f.Templates)
	}

	// A template migrated from an old config file may have any name; the
	// download keeps it.
	legacy := `小猫 "旧"`
	if err := h.db.PutPersonaTemplate(ctx, store.PersonaTemplateRecord{Name: legacy, Persona: catPersona, UpdatedBy: "system"}); err != nil {
		t.Fatal(err)
	}
	if err := h.personas.LoadTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	f = decodeExport(t, h.get(templatePath(legacy, "export")), "persona-"+legacy+".json")
	if len(f.Templates) != 1 || f.Templates[0].Name != legacy || !reflect.DeepEqual(f.Templates[0].Persona, catPersona) {
		t.Errorf("export of %s = %+v", legacy, f.Templates)
	}

	h.getErr(templatePath("nope", "export"), http.StatusNotFound, conloomapi.CodeNotFound)

	details := checkTemplateAudits(t, h, "persona.template.export", "lighthouse", "default", legacy)
	noImageData(t, "export", details, got.AppearanceImage.Data)
	for _, want := range []string{`{"templates":["lighthouse"]}`, `{"templates":["default"]}`} {
		if !slices.Contains(details, want) {
			t.Errorf("export audit details %v lack %s", details, want)
		}
	}
}

func TestTemplatesAPI_ExportHead(t *testing.T) {
	h := newBotsHarness(t)
	// HEAD answers the headers of the download without building the file or
	// auditing an export.
	paths := []string{templatePath("cat", "export"), templatePath("default", "export"), templatesExportPath}
	heads := make([]*httptest.ResponseRecorder, len(paths))
	for i, p := range paths {
		heads[i] = h.do(http.MethodHead, p, "", withCookie(h.cookie))
	}
	w := h.do(http.MethodHead, templatePath("nope", "export"), "", withCookie(h.cookie))
	if w.Code != http.StatusNotFound {
		t.Errorf("HEAD of an unknown template's export = %d, want 404", w.Code)
	}
	if n := len(h.audits("persona.template.export")); n != 0 {
		t.Errorf("HEAD wrote %d export audits, want none", n)
	}
	for i, p := range paths {
		head, get := heads[i], h.get(p)
		if head.Code != http.StatusOK || head.Body.Len() != 0 {
			t.Errorf("HEAD %s = %d with %d bytes, want 200 without a body", p, head.Code, head.Body.Len())
		}
		for _, k := range []string{"Content-Type", "Content-Disposition", "Cache-Control"} {
			if got, want := head.Header().Get(k), get.Header().Get(k); got != want || want == "" {
				t.Errorf("HEAD %s %s = %q, want %q as GET", p, k, got, want)
			}
		}
	}
	checkTemplateAudits(t, h, "persona.template.export", "cat", "default", "")
}

func TestTemplatesAPI_ExportAll(t *testing.T) {
	h := newBotsHarness(t)
	h.srv.d.Location = time.FixedZone("UTC+8", 8*60*60)
	data := pngData(t, testImage(64, 32, false))
	up := decodeUpload(t, h.upload(data, "image/png"))
	doc := lighthousePersona()
	doc.AppearanceImage = up.ID
	decodeTemplate(t, h.putTemplate("lighthouse", doc, ""), http.StatusCreated)

	// 2026-11-13 18:00 UTC is already the 14th in the server's time zone.
	h.advance(10 * time.Hour)
	w := h.get(templatesExportPath)
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="persona-templates-20261114.json"` {
		t.Errorf("Content-Disposition = %q, want the server's local date", got)
	}
	f := decodeExport(t, w, "persona-templates-20261114.json")
	var names []string
	for _, tpl := range f.Templates {
		names = append(names, tpl.Name)
	}
	if !slices.Equal(names, []string{"cat", "default", "lighthouse"}) {
		t.Fatalf("exported templates = %v, want cat, default and lighthouse", names)
	}
	if cat, def, lh := f.Templates[0], f.Templates[1], f.Templates[2]; !reflect.DeepEqual(cat.Persona, catPersona) ||
		cat.AppearanceImage != nil || !reflect.DeepEqual(def.Persona, conloomapi.DefaultPersona()) ||
		!reflect.DeepEqual(lh.Persona, doc) || lh.AppearanceImage == nil {
		t.Errorf("exported templates = %+v", f.Templates)
	}
	details := checkTemplateAudits(t, h, "persona.template.export", "")
	if want := `{"templates":["cat","default","lighthouse"]}`; !slices.Equal(details, []string{want}) {
		t.Errorf("export audit details = %v, want [%s]", details, want)
	}

	// The file restores the templates elsewhere: another deployment, which
	// has a cat of its own and the built-in default.
	other := newBotsHarness(t)
	got := decodeImport(t, other.write(http.MethodPost, importPath("overwrite"), w.Body.String()))
	want := []importedResp{{"cat", "cat", false}, {"default", "default", true}, {"lighthouse", "lighthouse", true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("import of the export = %+v, want %+v", got, want)
	}
	if lh := other.template("lighthouse"); lh.Persona == nil || !reflect.DeepEqual(*lh.Persona, doc) || lh.AppearanceImage != up.ID {
		t.Errorf("imported lighthouse = %+v, want the same persona and image ID", lh)
	}
	if img, err := other.db.GetPersonaImage(context.Background(), up.ID, true); err != nil || !bytes.Equal(img.Original, data) {
		t.Errorf("imported image: %v, want the original bytes", err)
	}
	if def := other.template("default"); def.Source != "db" {
		t.Errorf("imported default = %+v, want it stored", def)
	}
}

func TestTemplatesAPI_ImportModes(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	// A stored panel bot whose template is missing.
	if err := h.db.CreatePanelBot(ctx, store.PanelBot{ID: "panel-abcd1234", Name: "小红", PersonaTemplate: "night-owl",
		Skills: []string{}, CreatedBy: "panel:" + testUser}); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.d.PanelBots.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if b := h.bots().Bots[1]; b.PersonaTemplate != "default" || len(b.Problems) != 1 {
		t.Fatalf("bot with a missing template = %+v, want default and one problem", b)
	}

	data := pngData(t, testImage(40, 20, false))
	bigCat := catPersona
	bigCat.CharacterName = "大猫"
	night := lighthousePersona()
	night.PersonaName = "夜猫"
	night.AppearanceImage = strings.Repeat("ab", 32)
	ownDefault := lighthousePersona()
	ownDefault.PersonaName = "默认"
	body := mustJSON(t, exchangeFile(
		persona.ExchangeTemplate{Name: "cat", Persona: bigCat},
		persona.ExchangeTemplate{Name: "night-owl", Persona: night, AppearanceImage: embedded(data)},
		persona.ExchangeTemplate{Name: "default", Persona: ownDefault},
	))

	// fail, also without on_conflict, refuses names stored templates have
	// and writes nothing; the built-in default is no conflict.
	for _, mode := range []string{"", "fail"} {
		c := decodeConflict(t, h.write(http.MethodPost, importPath(mode), body))
		if !slices.Equal(c.Names, []string{"cat"}) || c.Retryable || !strings.Contains(c.Message, "cat") || c.TraceID == "" {
			t.Errorf("on_conflict=%q: conflict = %+v, want cat named, not retryable", mode, c)
		}
	}
	if _, ok := h.personas.Template("night-owl"); ok || h.imageStored(imageID(data)) {
		t.Error("refused import stored something")
	}
	if doc, _ := h.personas.Template("cat"); !reflect.DeepEqual(doc, catPersona) {
		t.Errorf("cat after a refused import = %+v", doc)
	}

	// rename stores the cat as cat-2.
	got := decodeImport(t, h.write(http.MethodPost, importPath("rename"), body))
	want := []importedResp{{"cat-2", "cat", true}, {"night-owl", "night-owl", true}, {"default", "default", true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rename import = %+v, want %+v", got, want)
	}
	if doc, _ := h.personas.Template("cat"); !reflect.DeepEqual(doc, catPersona) {
		t.Errorf("cat after rename import = %+v, want it unchanged", doc)
	}
	if doc, _ := h.personas.Template("cat-2"); !reflect.DeepEqual(doc, bigCat) {
		t.Errorf("cat-2 = %+v, want the imported cat", doc)
	}
	wantNight := night
	wantNight.AppearanceImage = imageID(data)
	if got := h.template("night-owl"); got.Source != "db" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, wantNight) ||
		got.UpdatedBy != "panel:"+testUser {
		t.Errorf("night-owl = %+v, want the import with the image's computed ID", got)
	}
	if !h.imageStored(imageID(data)) {
		t.Error("embedded image not stored")
	}
	if got := h.template("default"); got.Source != "db" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, ownDefault) {
		t.Errorf("default = %+v, want the imported one stored", got)
	}
	// The bot has its template back without another write.
	if b := h.bots().Bots[1]; b.PersonaTemplate != "night-owl" || len(b.Problems) != 0 {
		t.Errorf("bot after its template was imported = %+v, want night-owl without problems", b)
	}

	// overwrite replaces them.
	got = decodeImport(t, h.write(http.MethodPost, importPath("overwrite"), body))
	want = []importedResp{{"cat", "cat", false}, {"night-owl", "night-owl", false}, {"default", "default", false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overwrite import = %+v, want %+v", got, want)
	}
	if doc, _ := h.personas.Template("cat"); !reflect.DeepEqual(doc, bigCat) {
		t.Errorf("cat after overwrite import = %+v, want the imported cat", doc)
	}

	// Anything wrong refuses the whole file.
	both := lighthousePersona()
	both.Minor, both.Adult = true, true
	before := h.templates()
	for _, c := range []struct{ name, mode, body, want string }{
		{"unknown mode", "merge", body, "on_conflict"},
		{"wrong format", "rename", strings.Replace(body, persona.ExchangeFormat, "conloom.persona", 1), "format"},
		{"wrong version", "rename", strings.Replace(body, `"version":1`, `"version":2`, 1), "version"},
		{"minor and adult", "rename", mustJSON(t, exchangeFile(
			persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona()},
			persona.ExchangeTemplate{Name: "teen", Persona: both})), "minor"},
		{"not an image", "rename", mustJSON(t, exchangeFile(
			persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona(), AppearanceImage: embedded([]byte("GIF89a not really"))})), "unsupported image"},
		{"not base64", "rename", mustJSON(t, exchangeFile(
			persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona(), AppearanceImage: &persona.ExchangeImage{Data: "%%%"}})), "base64"},
		{"unknown field", "rename", strings.Replace(body, `"version":1`, `"version":1,"extra":1`, 1), "unknown field"},
		{"unknown persona field", "rename", strings.Replace(body, `"persona":{`, `"persona":{"extra":1,`, 1), "unknown field"},
		{"trailing data", "rename", body + "{}", "trailing data"},
		{"not JSON", "rename", "{", "invalid JSON"},
	} {
		e := errorBody(t, h.write(http.MethodPost, importPath(c.mode), c.body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, c.want) {
			t.Errorf("%s: message %q lacks %q", c.name, e.Message, c.want)
		}
	}
	if after := h.templates(); !reflect.DeepEqual(after, before) {
		t.Errorf("templates after refused imports = %+v, want %+v", after, before)
	}

	// Another image being processed refuses the import.
	h.srv.imageUploads <- struct{}{}
	e := errorBody(t, h.write(http.MethodPost, importPath("overwrite"), body), http.StatusConflict, conloomapi.CodeChatBusy)
	if e.Message != "正在处理另一张图片，请稍后" || !e.Retryable {
		t.Errorf("busy import = %+v", e)
	}
	<-h.srv.imageUploads

	details := checkTemplateAudits(t, h, "persona.template.import", "", "")
	noImageData(t, "import", details, embedded(data).Data)
	for _, want := range []string{
		`{"on_conflict":"rename","created":["cat-2","night-owl","default"],"overwritten":[],"renamed":{"cat":"cat-2"}}`,
		`{"on_conflict":"overwrite","created":[],"overwritten":["cat","night-owl","default"]}`,
	} {
		if !slices.Contains(details, want) {
			t.Errorf("import audit details %v lack %s", details, want)
		}
	}
}

func TestTemplatesAPI_ImportHoldsImageSlot(t *testing.T) {
	h := newBotsHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	load := h.srv.d.PanelBots.Load
	h.personas.SetOnImport(func(ctx context.Context) error {
		close(entered)
		<-release
		return load(ctx)
	})
	body := mustJSON(t, exchangeFile(persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona()}))

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- h.write(http.MethodPost, importPath(""), body) }()
	select {
	case <-entered:
	case w := <-first:
		t.Fatalf("import answered %d %s before it called the hook", w.Code, w.Body)
	}
	// Until the import is done, other imports and uploads are refused.
	errorBody(t, h.write(http.MethodPost, importPath("rename"), body), http.StatusConflict, conloomapi.CodeChatBusy)
	png := pngData(t, testImage(20, 20, false))
	errorBody(t, h.upload(png, "image/png"), http.StatusConflict, conloomapi.CodeChatBusy)
	unblock()
	if got := decodeImport(t, <-first); !reflect.DeepEqual(got, []importedResp{{"owl", "owl", true}}) {
		t.Errorf("import = %+v", got)
	}
	decodeUpload(t, h.upload(png, "image/png"))
}

// readSpy is a request body that records whether it was read.
type readSpy struct {
	io.Reader
	read atomic.Bool
}

func (r *readSpy) Read(p []byte) (int, error) {
	r.read.Store(true)
	return r.Reader.Read(p)
}

func TestTemplatesAPI_ImportBusyBeforeReadingBody(t *testing.T) {
	h := newBotsHarness(t)
	raw := mustJSON(t, exchangeFile(persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona()}))
	post := func(body *readSpy) *httptest.ResponseRecorder {
		return h.do(http.MethodPost, importPath(""), "", withCookie(h.cookie), withCSRF(h.csrf),
			func(r *http.Request) { r.Body, r.ContentLength = io.NopCloser(body), int64(len(raw)) })
	}

	// While another image is processed, an import is refused before its body
	// is read, so waiting imports hold no file in memory.
	h.srv.imageUploads <- struct{}{}
	busy := &readSpy{Reader: strings.NewReader(raw)}
	e := errorBody(t, post(busy), http.StatusConflict, conloomapi.CodeChatBusy)
	if !e.Retryable || e.Message != "正在处理另一张图片，请稍后" {
		t.Errorf("busy import = %+v", e)
	}
	if busy.read.Load() {
		t.Error("busy import read the request body")
	}
	<-h.srv.imageUploads

	free := &readSpy{Reader: strings.NewReader(raw)}
	if got := decodeImport(t, post(free)); !reflect.DeepEqual(got, []importedResp{{"owl", "owl", true}}) || !free.read.Load() {
		t.Errorf("import with the slot free = %+v, body read %v", got, free.read.Load())
	}
}

func TestTemplatesAPI_ImportTooLarge413(t *testing.T) {
	h := newBotsHarness(t)
	// A good file, padded to one byte over the limit.
	valid := mustJSON(t, exchangeFile(persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona()}))
	over := valid + strings.Repeat(" ", maxImportBytes+1-len(valid))
	e := errorBody(t, h.write(http.MethodPost, importPath(""), over), http.StatusRequestEntityTooLarge, conloomapi.CodeInvalidEvent)
	if !strings.Contains(e.Message, "64 MiB") {
		t.Errorf("too large message = %q", e.Message)
	}
	if _, ok := h.personas.Template("owl"); ok {
		t.Error("too large import stored")
	}
	if n := len(h.audits("persona.template.import")); n != 0 {
		t.Errorf("import audits = %d, want 0", n)
	}
}

func TestTemplatesAPI_ImportTooManyTemplates(t *testing.T) {
	h := newBotsHarness(t)
	imports := 0
	load := h.srv.d.PanelBots.Load
	h.personas.SetOnImport(func(ctx context.Context) error {
		imports++
		return load(ctx)
	})
	before := h.templates()
	// Every template has a wrong name and an image that is not base64, and
	// the one over the limit an unknown field: the file is refused for its
	// size alone, before any template is checked.
	entries := make([]string, persona.MaxExchangeTemplates+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"name":"Bad %d","persona":{},"appearance_image":{"mime":"image/png","data":"%%%%%%"}}`, i)
	}
	entries[persona.MaxExchangeTemplates] = `{"name":"t200","persona":{},"extra":1}`
	body := `{"format":"conloom.persona-template","version":1,"exported_at":"2026-11-15T04:00:00Z","templates":[` +
		strings.Join(entries, ",") + `]}`
	for _, mode := range []string{"", "overwrite", "rename"} {
		e := errorBody(t, h.write(http.MethodPost, importPath(mode), body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if e.Message != "一个文件最多 200 个模板" || e.Retryable {
			t.Errorf("import (%q) of 201 templates = %+v, want 一个文件最多 200 个模板", mode, e)
		}
	}
	if after := h.templates(); !reflect.DeepEqual(after, before) {
		t.Errorf("templates after refused imports = %+v, want %+v", after, before)
	}
	if n := len(h.audits("persona.template.import")); n != 0 || imports != 0 {
		t.Errorf("refused imports: %d audits, %d imports; want none", n, imports)
	}

	// 200 templates are imported.
	full := make([]persona.ExchangeTemplate, persona.MaxExchangeTemplates)
	for i := range full {
		full[i] = persona.ExchangeTemplate{Name: fmt.Sprintf("t%03d", i), Persona: lighthousePersona()}
	}
	if got := decodeImport(t, h.write(http.MethodPost, importPath(""), mustJSON(t, exchangeFile(full...)))); len(got) != 200 ||
		got[199] != (importedResp{"t199", "t199", true}) {
		t.Errorf("import of 200 templates = %d results, want all of them created", len(got))
	}
}

func TestTemplatesAPI_ReservedNames(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	const reason = "模板名不能是 export 或 import"

	// Saving or copying to a new template of a reserved name.
	for _, name := range []string{"export", "import"} {
		e := errorBody(t, h.putTemplate(name, lighthousePersona(), ""), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, reason) {
			t.Errorf("PUT %s message = %q, want %q", name, e.Message, reason)
		}
	}
	// Importing one, whatever on_conflict says.
	body := mustJSON(t, exchangeFile(persona.ExchangeTemplate{Name: "import", Persona: lighthousePersona()}))
	for _, mode := range []string{"", "overwrite", "rename"} {
		e := errorBody(t, h.write(http.MethodPost, importPath(mode), body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, `templates[0] "import": `+reason) {
			t.Errorf("import (%q) message = %q, want %q", mode, e.Message, reason)
		}
	}
	for _, name := range []string{"export", "import"} {
		if _, ok := h.personas.Template(name); ok {
			t.Errorf("template %s stored", name)
		}
	}

	// A template stored with such a name before keeps working.
	if err := h.db.PutPersonaTemplate(ctx, store.PersonaTemplateRecord{Name: "export", Persona: catPersona, UpdatedBy: "system"}); err != nil {
		t.Fatal(err)
	}
	if err := h.personas.LoadTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	edited := catPersona
	edited.CharacterName = "大猫"
	if got := decodeTemplate(t, h.putTemplate("export", edited, ""), http.StatusOK); got.Persona == nil || got.Persona.CharacterName != "大猫" {
		t.Errorf("PUT of the stored export = %+v", got)
	}
	f := decodeExport(t, h.get(templatePath("export", "export")), "persona-export.json")
	if len(f.Templates) != 1 || f.Templates[0].Name != "export" || !reflect.DeepEqual(f.Templates[0].Persona, edited) {
		t.Errorf("export of the stored export = %+v", f.Templates)
	}
}

func TestTemplatesAPI_ImportCSRF(t *testing.T) {
	h := newBotsHarness(t)
	body := mustJSON(t, exchangeFile(persona.ExchangeTemplate{Name: "owl", Persona: lighthousePersona()}))
	for _, mode := range []string{"", "overwrite"} {
		p := importPath(mode)
		errorBody(t, h.do(http.MethodPost, p, body, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(http.MethodPost, p, body, withCookie(h.cookie), withCSRF("wrong")), http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(http.MethodPost, p, body, withCookie(h.cookie), withCSRF(h.csrf), withOrigin("http://attacker.invalid")),
			http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(http.MethodPost, p, body, withCSRF(h.csrf)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}
	for _, p := range []string{templatesExportPath, templatePath("cat", "export")} {
		errorBody(t, h.do(http.MethodGet, p, ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}
	if _, ok := h.personas.Template("owl"); ok {
		t.Error("refused import stored")
	}
	for _, kind := range []string{"persona.template.import", "persona.template.export"} {
		if n := len(h.audits(kind)); n != 0 {
			t.Errorf("%s audits = %d, want 0", kind, n)
		}
	}
}

func TestTemplatesAPI_ResetOnlyDefault(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()

	// The built-in default stays as it is.
	got := decodeTemplate(t, h.write(http.MethodPost, templatePath("default", "reset"), nil), http.StatusOK)
	if got.Source != "builtin" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, conloomapi.DefaultPersona()) {
		t.Errorf("reset of the built-in default = %+v", got)
	}
	// A stored default goes back to the built-in one.
	decodeTemplate(t, h.putTemplate("default", lighthousePersona(), ""), http.StatusOK)
	got = decodeTemplate(t, h.write(http.MethodPost, templatePath("default", "reset"), nil), http.StatusOK)
	if got.Source != "builtin" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, conloomapi.DefaultPersona()) ||
		got.UpdatedBy != "" || got.UpdatedAt != nil {
		t.Errorf("reset of the stored default = %+v, want the built-in default", got)
	}

	// Any other template, stored or not, is 400 and left alone.
	for _, name := range []string{"cat", "nope"} {
		e := errorBody(t, h.write(http.MethodPost, templatePath(name, "reset"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if !strings.Contains(e.Message, `only "default" can be reset`) {
			t.Errorf("reset %s message = %q", name, e.Message)
		}
	}
	if rows, _, err := h.db.ListPersonaTemplates(ctx); err != nil || len(rows) != 1 || rows[0].Name != "cat" ||
		!reflect.DeepEqual(rows[0].Persona, catPersona) {
		t.Errorf("stored templates = %+v, %v; want only cat, unchanged", rows, err)
	}
	checkTemplateAudits(t, h, "persona.template.reset", "default", "default")
}
