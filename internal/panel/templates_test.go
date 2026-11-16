package panel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
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

	for _, name := range []string{"night-owl", "cat", "nope"} {
		errorBody(t, h.write(http.MethodPost, templatePath(name, "reset"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	errorBody(t, h.write(http.MethodDelete, templatePath("default"), nil), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodDelete, templatePath("nope"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	h.getErr(templatePath("nope"), http.StatusNotFound, conloomapi.CodeNotFound)

	decodeBot(t, h.write(http.MethodPut, botPath(bot.ID), map[string]any{"name": "小红", "persona_template": "cat", "skills": []string{}}), http.StatusOK)
	if got := h.deleteTemplate("night-owl"); !reflect.DeepEqual(got, deletedResp{Name: "night-owl", AffectedBots: []string{}}) {
		t.Errorf("delete of a template no bot uses = %+v, want no affected bots", got)
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
	if deletes := checkTemplateAudits(t, h, "persona.template.delete", "night-owl"); !someContains(deletes, `"affected_bots":[]`) {
		t.Errorf("delete audit details = %v, want no affected bots", deletes)
	}
}

// deletedResp is the response to a persona template's DELETE.
type deletedResp struct {
	Name         string   `json:"name"`
	AffectedBots []string `json:"affected_bots"`
}

// deleteTemplate deletes the persona template name through the API, which
// must answer 200.
func (h *writeHarness) deleteTemplate(name string) deletedResp {
	h.t.Helper()
	w := h.write(http.MethodDelete, templatePath(name), nil)
	if w.Code != http.StatusOK {
		h.t.Fatalf("DELETE %s: status = %d, want 200 (body %s)", name, w.Code, w.Body)
	}
	var v deletedResp
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		h.t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

func TestTemplatesAPI_DeleteInUseFallsBack(t *testing.T) {
	h := newBotsHarnessUsing(t, "cat")
	bot := h.createBot("小红", "cat", "time")
	h.advance(time.Minute)
	other := h.createBot("小刚", "default")
	if got := h.template("cat"); !reflect.DeepEqual(got.Users, []templateUserResp{{dataBot, "织语", "config"}, {bot.ID, "小红", "panel"}}) {
		t.Fatalf("cat users = %+v, want the config bot and 小红", got.Users)
	}

	got := h.deleteTemplate("cat")
	if want := (deletedResp{Name: "cat", AffectedBots: []string{"织语（" + dataBot + "）", "小红"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("delete of a template in use = %+v, want %+v", got, want)
	}
	h.getErr(templatePath("cat"), http.StatusNotFound, conloomapi.CodeNotFound)
	if _, ok := h.personas.Template("cat"); ok {
		t.Error("deleted template still in the catalog")
	}

	problem := []string{"人设模板“cat”已不存在，暂用 default"}
	bots := h.bots()
	if len(bots.Bots) != 3 {
		t.Fatalf("bots after the delete = %+v, want the config bot, 小红 and 小刚", bots.Bots)
	}
	for i, want := range []botResp{
		{ID: dataBot, Name: "织语", Source: "config", PersonaTemplate: "default", Skills: []string{"time"}, Problems: problem},
		{ID: bot.ID, Name: "小红", Source: "panel", PersonaTemplate: "default", Skills: []string{"time"}, Problems: problem},
		{ID: other.ID, Name: "小刚", Source: "panel", PersonaTemplate: "default", Skills: []string{}, Problems: []string{}},
	} {
		if !reflect.DeepEqual(bots.Bots[i], want) {
			t.Errorf("bot %d after the delete = %+v, want %+v", i, bots.Bots[i], want)
		}
	}
	if slices.Contains(bots.Templates, "cat") {
		t.Errorf("templates to choose = %v, still with cat", bots.Templates)
	}
	if got := h.template("default").Users; !reflect.DeepEqual(got, []templateUserResp{
		{dataBot, "织语", "config"}, {bot.ID, "小红", "panel"}, {other.ID, "小刚", "panel"},
	}) {
		t.Errorf("default users after the delete = %+v, want every bot", got)
	}
	if deletes := checkTemplateAudits(t, h, "persona.template.delete", "cat"); !someContains(deletes,
		`"affected_bots":["织语（`+dataBot+`）","小红"]`) {
		t.Errorf("delete audit details = %v, want the affected bots", deletes)
	}

	// Choosing a template again clears the panel bot's problem.
	decodeBot(t, h.write(http.MethodPut, botPath(bot.ID), map[string]any{"name": "小红", "persona_template": "default", "skills": []string{"time"}}), http.StatusOK)
	if b := h.bots().Bots[1]; b.PersonaTemplate != "default" || len(b.Problems) != 0 {
		t.Errorf("小红 after choosing default = %+v, want no problems", b)
	}
}

func TestTemplatesAPI_CreateBlank(t *testing.T) {
	h := newBotsHarness(t)
	// A saved default is not the built-in default persona a blank template
	// starts from.
	decodeTemplate(t, h.putTemplate("default", lighthousePersona(), ""), http.StatusOK)

	var builtin struct {
		Persona *conloomapi.PersonaDoc `json:"persona"`
	}
	h.getJSON(templatePath("default", "builtin"), &builtin)
	if builtin.Persona == nil || !reflect.DeepEqual(*builtin.Persona, conloomapi.DefaultPersona()) {
		t.Fatalf("built-in default = %+v, want conloomapi.DefaultPersona()", builtin.Persona)
	}
	errorBody(t, h.do(http.MethodGet, templatePath("default", "builtin"), ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	for _, name := range []string{"cat", "nope"} {
		if w := h.get(templatePath(name, "builtin")); w.Code != http.StatusNotFound {
			t.Errorf("GET %s/builtin = %d, want 404: only default has a built-in version", name, w.Code)
		}
	}

	got := decodeTemplate(t, h.putTemplate("blank-1", *builtin.Persona, "从内置默认人设新建"), http.StatusCreated)
	if got.Name != "blank-1" || got.Source != "db" || got.Persona == nil || !reflect.DeepEqual(*got.Persona, conloomapi.DefaultPersona()) {
		t.Errorf("new blank template = %+v, want the built-in default persona", got)
	}
	if doc, _ := h.personas.Template("default"); !reflect.DeepEqual(doc, lighthousePersona()) {
		t.Errorf("default = %+v, want the saved one unchanged", doc)
	}
	for _, name := range []string{"export", "import"} {
		errorBody(t, h.putTemplate(name, *builtin.Persona, ""), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	if puts := checkTemplateAudits(t, h, "persona.template.put", "default", "blank-1"); !someContains(puts,
		`"created":true`, `"reason":"从内置默认人设新建"`) {
		t.Errorf("put audit details = %v, want blank-1 created", puts)
	}
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
	if got := h.deleteTemplate("night-owl"); !slices.Equal(got.AffectedBots, []string{"小红"}) {
		t.Errorf("delete of night-owl = %+v, want 小红 affected", got)
	}
	if b := h.bots().Bots[1]; b.PersonaTemplate != "default" || len(b.Problems) != 1 {
		t.Errorf("bot after its template was deleted again = %+v, want default and one problem", b)
	}
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

// imageReadSpy is a store whose persona image reads first call onRead, which
// may fail them.
type imageReadSpy struct {
	*sqlite.DB
	onRead func(id string) error
}

func (s *imageReadSpy) GetPersonaImage(ctx context.Context, id string, original bool) (store.PersonaImage, error) {
	if s.onRead != nil {
		if err := s.onRead(id); err != nil {
			return store.PersonaImage{}, err
		}
	}
	return s.DB.GetPersonaImage(ctx, id, original)
}

// newExportHarness is a writeHarness whose persona templates read their
// images through the returned spy, with the templates lighthouse and moon,
// each with an image of its own, returned in that order.
func newExportHarness(t *testing.T) (*writeHarness, *imageReadSpy, [2][]byte) {
	t.Helper()
	var spy *imageReadSpy
	h := newWriteHarness(t, func(d *Deps) {
		spy = &imageReadSpy{DB: d.Store.(*sqlite.DB)}
		d.Personas = persona.NewService(spy)
		if err := d.Personas.LoadTemplates(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	images := [2][]byte{pngData(t, testImage(64, 32, true)), pngData(t, testImage(48, 32, false))}
	for i, name := range []string{"lighthouse", "moon"} {
		doc := lighthousePersona()
		doc.AppearanceImage = decodeUpload(t, h.upload(images[i], "image/png")).ID
		decodeTemplate(t, h.putTemplate(name, doc, ""), http.StatusCreated)
	}
	return h, spy, images
}

// serveExport GETs path as the logged-in user into w and returns what the
// handler panicked with, nil when it did not.
func serveExport(h *writeHarness, w http.ResponseWriter, path string) (panicked any) {
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+path, nil)
	r.RemoteAddr = testIP + ":40000"
	withCookie(h.cookie)(r)
	defer func() { panicked = recover() }()
	h.h.ServeHTTP(w, r)
	return nil
}

func TestTemplatesAPI_ExportStreamsTemplates(t *testing.T) {
	h, spy, images := newExportHarness(t)
	w := httptest.NewRecorder()
	var bodies []string
	spy.onRead = func(string) error {
		bodies = append(bodies, w.Body.String())
		return nil
	}
	if v := serveExport(h, w, templatesExportPath); v != nil {
		t.Fatalf("export panicked: %v", v)
	}
	f := decodeExport(t, w, "persona-templates-20261113.json")
	if len(f.Templates) != 4 || f.Templates[2].Name != "lighthouse" || f.Templates[3].Name != "moon" {
		t.Fatalf("exported templates = %+v, want cat, default, lighthouse and moon", f.Templates)
	}
	// The file is written one template at a time, so that at most one image
	// is in memory: lighthouse's image is sent before moon's is read.
	if len(bodies) != 2 {
		t.Fatalf("image reads = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[1], base64.StdEncoding.EncodeToString(images[0])) {
		t.Errorf("moon's image was read before lighthouse's was written (%d bytes written)", len(bodies[1]))
	}
	want, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Body.String(); got != string(want)+"\n" {
		t.Errorf("export is not the exchange file indented by two spaces:\n%.600s\nwant\n%.600s", got, want)
	}
}

func TestTemplatesAPI_ExportImageReadFails(t *testing.T) {
	h, spy, _ := newExportHarness(t)
	ids := map[string]string{}
	for _, name := range []string{"lighthouse", "moon"} {
		ids[name] = h.template(name).AppearanceImage
	}

	// Before anything is written, a failure is an error response.
	spy.onRead = func(id string) error {
		if id == ids["lighthouse"] {
			return errors.New("disk I/O error")
		}
		return nil
	}
	w := httptest.NewRecorder()
	if v := serveExport(h, w, templatePath("lighthouse", "export")); v != nil {
		t.Fatalf("export of lighthouse panicked: %v", v)
	}
	errorBody(t, w, http.StatusInternalServerError, conloomapi.CodeInternal)

	// Once the file has started, a failure aborts the response, so that the
	// download fails rather than leaving a cut-off file.
	spy.onRead = func(id string) error {
		if id == ids["moon"] {
			return errors.New("disk I/O error")
		}
		return nil
	}
	w = httptest.NewRecorder()
	if v := serveExport(h, w, templatesExportPath); v != http.ErrAbortHandler {
		t.Fatalf("export failing at moon's image panicked with %v, want http.ErrAbortHandler (status %d)", v, w.Code)
	}
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name": "lighthouse"`) {
		t.Errorf("aborted export = %d %.300s, want the file up to lighthouse", w.Code, w.Body)
	}
	if got := h.counter("conloom_panel_requests_total", map[string]string{
		"route": "GET /panel/api/persona-templates/export", "code": "200"}); got != 1 {
		t.Errorf("aborted export counted %v times, want 1", got)
	}
	checkTemplateAudits(t, h, "persona.template.export", "")
}

func TestTemplatesAPI_ExportAllSkipsTemplateDeletedMeanwhile(t *testing.T) {
	h, spy, _ := newExportHarness(t)
	lighthouse := h.template("lighthouse").AppearanceImage
	// moon is deleted while lighthouse, written before it, is read.
	spy.onRead = func(id string) error {
		if id == lighthouse {
			_, err := h.srv.d.Personas.DeleteTemplate(context.Background(), "moon")
			return err
		}
		return nil
	}
	w := httptest.NewRecorder()
	if v := serveExport(h, w, templatesExportPath); v != nil {
		t.Fatalf("export panicked: %v (%.300s)", v, w.Body)
	}
	f := decodeExport(t, w, "persona-templates-20261113.json")
	var names []string
	for _, tpl := range f.Templates {
		names = append(names, tpl.Name)
	}
	if !slices.Equal(names, []string{"cat", "default", "lighthouse"}) {
		t.Fatalf("exported templates = %v, want cat, default and lighthouse", names)
	}
	want, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Body.String(); got != string(want)+"\n" {
		t.Errorf("export is not the exchange file indented by two spaces:\n%.600s\nwant\n%.600s", got, want)
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

// The nginx example in docs/deployment.md lets through every body the panel
// accepts, the largest being a template import; otherwise re-importing an
// export with a large reference image gets nginx's 413.
func TestDocs_NginxBodyLimitFitsPanelUploads(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "deployment.md"))
	if err != nil {
		t.Fatal(err)
	}
	limits := regexp.MustCompile(`client_max_body_size (\d+)m;`).FindAllStringSubmatch(string(b), -1)
	if len(limits) == 0 {
		t.Fatal("docs/deployment.md sets no client_max_body_size")
	}
	for _, m := range limits {
		mib, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if got := int64(mib) << 20; got < maxImportBytes || got < imaging.MaxBytes {
			t.Errorf("docs/deployment.md: client_max_body_size %sm is under the import limit %d MiB or the image limit %d MiB",
				m[1], maxImportBytes>>20, imaging.MaxBytes>>20)
		}
	}
}
