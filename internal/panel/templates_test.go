package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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
	Config          *conloomapi.PersonaDoc `json:"config"`
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
	wantCat := templateResp{Name: "cat", Source: "config", PersonaName: "cat", CharacterName: "小猫", Users: []templateUserResp{}}
	if !reflect.DeepEqual(list[0], wantCat) {
		t.Errorf("cat = %+v, want %+v", list[0], wantCat)
	}
	if want := []templateUserResp{{dataBot, "织语", "config"}}; !reflect.DeepEqual(list[1].Users, want) || list[1].Source != "config" {
		t.Errorf("default = %+v, want source config, users %+v", list[1], want)
	}
	if body := string(h.getJSON("/panel/api/persona-templates", nil)); !strings.Contains(body, `"users":[]`) ||
		strings.Contains(body, `"persona":`) {
		t.Errorf("list = %s, want users as an empty list and no persona", body)
	}
	got := h.template("cat")
	if got.Persona == nil || !reflect.DeepEqual(*got.Persona, catPersona) || got.Config != nil || got.Source != "config" ||
		got.UpdatedBy != "" || got.UpdatedAt != nil {
		t.Errorf("GET cat = %+v, want the config file's version without config original", got)
	}

	// Override the config template, then reset it.
	override := catPersona
	override.CharacterName = "大猫"
	got = decodeTemplate(t, h.putTemplate("cat", override, "改名"), http.StatusOK)
	if got.Source != "config_override" || got.CharacterName != "大猫" || got.Persona == nil || got.Persona.CharacterName != "大猫" ||
		got.Config == nil || !reflect.DeepEqual(*got.Config, catPersona) || got.UpdatedBy != "panel:"+testUser || got.UpdatedAt == nil {
		t.Errorf("PUT cat = %+v, want the override with the config original", got)
	}
	if doc, _ := h.personas.Template("cat"); doc.CharacterName != "大猫" {
		t.Errorf("catalog cat = %+v, want the override", doc)
	}
	if got := h.template("cat"); got.Source != "config_override" || got.Config == nil || got.Config.CharacterName != "小猫" {
		t.Errorf("GET cat after override = %+v", got)
	}
	got = decodeTemplate(t, h.write(http.MethodPost, templatePath("cat", "reset"), nil), http.StatusOK)
	if got.Source != "config" || got.Persona == nil || got.Persona.CharacterName != "小猫" || got.Config != nil {
		t.Errorf("reset cat = %+v, want the config file's version", got)
	}
	if rows, _, err := h.db.ListPersonaTemplates(ctx); err != nil || len(rows) != 0 {
		t.Errorf("stored templates after reset = %+v, %v; want none", rows, err)
	}
	decodeTemplate(t, h.write(http.MethodPost, templatePath("cat", "reset"), nil), http.StatusOK)

	// A new panel template, used by a panel bot.
	night := lighthousePersona()
	night.PersonaName = "夜猫"
	got = decodeTemplate(t, h.putTemplate("night-owl", night, ""), http.StatusCreated)
	if got.Name != "night-owl" || got.Source != "panel" || got.PersonaName != "夜猫" || got.Config != nil {
		t.Errorf("PUT night-owl = %+v, want a new panel template", got)
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

	errorBody(t, h.write(http.MethodPost, templatePath("night-owl", "reset"), nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodDelete, templatePath("cat"), nil), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodDelete, templatePath("default"), nil), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.write(http.MethodDelete, templatePath("nope"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, templatePath("nope", "reset"), nil), http.StatusNotFound, conloomapi.CodeNotFound)
	h.getErr(templatePath("nope"), http.StatusNotFound, conloomapi.CodeNotFound)

	decodeBot(t, h.write(http.MethodPut, botPath(bot.ID), map[string]any{"name": "小红", "persona_template": "cat", "skills": []string{}}), http.StatusOK)
	if w := h.write(http.MethodDelete, templatePath("night-owl"), nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete free template: %d %s", w.Code, w.Body)
	}
	h.getErr(templatePath("night-owl"), http.StatusNotFound, conloomapi.CodeNotFound)
	if _, ok := h.personas.Template("night-owl"); ok {
		t.Error("deleted template still in the catalog")
	}

	puts := checkTemplateAudits(t, h, "persona.template.put", "cat", "night-owl", "night-owl")
	for _, want := range [][]string{
		{`"source":"config_override"`, `"created":false`, `"reason":"改名"`},
		{`"source":"panel"`, `"created":true`},
		{`"source":"panel"`, `"created":false`, `"reason":"补充设定"`},
	} {
		if !someContains(puts, want...) {
			t.Errorf("put audit details %v: none has all of %v", puts, want)
		}
	}
	checkTemplateAudits(t, h, "persona.template.reset", "cat", "cat")
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
