package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var panelBotIDPattern = regexp.MustCompile(`^panel-[a-z0-9]{8}$`)

// stubTool is a registered tool that is never called.
type stubTool string

func (t stubTool) Spec() skills.Spec { return skills.Spec{Name: string(t)} }

func (stubTool) Call(context.Context, skills.Invocation, json.RawMessage) (string, error) {
	return "", nil
}

// newBotsHarness is a writeHarness whose panel bots, panel chats and
// appearance descriptions are served by a real orchestrator (without a vision
// model) over its store, on the harness's clock; deleting a persona template
// finds the bots using it, as in the app. The config bot is dataBot (qq,
// platform self ID 30001), named 织语, chatting privately with its admins
// (none), in no groups, with the skill time and the persona template
// default; the persona templates are the built-in default
// and the stored cat, and the skills are the built-in ones, with stub tools,
// plus grouponly, which only group chats may use.
func newBotsHarness(t *testing.T) *writeHarness {
	t.Helper()
	return newBotsHarnessUsing(t, "default")
}

// newBotsHarnessUsing is newBotsHarness whose config bot's persona template
// is template.
func newBotsHarnessUsing(t *testing.T, template string) *writeHarness {
	t.Helper()
	loaded, err := skills.Load("")
	if err != nil {
		t.Fatal(err)
	}
	loaded["grouponly"] = skills.Skill{Name: "grouponly", Description: "只在群里用", Tools: []string{"now"},
		Conversations: []string{conloomapi.ConvGroup}, Body: "- 群里用 now。"}
	var tools []skills.Tool
	for _, name := range []string{"now", "set_status", "adjust_buff", "add_buff", "remove_buff", "remember", "recall", "get_affinity"} {
		tools = append(tools, stubTool(name))
	}
	reg, err := skills.NewRegistry(loaded, tools)
	if err != nil {
		t.Fatal(err)
	}
	h := &writeHarness{trigger: &fakeTrigger{}}
	h.storeHarness = newStoreHarness(t,
		[]config.BotConfig{{ID: dataBot, Platform: "qq", PlatformSelfID: "30001", Name: "织语", PrivateChat: "admins",
			PrivateTemplate: template, GroupTemplate: template, Skills: []string{"time"}}},
		map[string]conloomapi.PersonaDoc{"cat": catPersona},
		func(d *Deps) {
			o, err := core.New(core.Deps{
				Store: d.Store.(*sqlite.DB), Router: router.New(nil, router.BreakerConfig{}, time.Now),
				Admission: admission.New(nil), Personas: d.Personas, Policies: d.Policies,
				Reviewer: review.New(nil, review.Options{}), Config: d.Config, Bots: d.Bots, Location: time.UTC, Now: d.Now,
				Skills: reg,
			})
			if err != nil {
				t.Fatalf("core.New: %v", err)
			}
			t.Cleanup(o.Close)
			d.Personas.SetTemplateUsers(d.Bots.TemplateUsers)
			d.Audit, d.Memory, d.Chat, d.PanelBots, d.Appearance = d.Store.(*sqlite.DB), h.trigger, o, o.PanelBots(), o
		})
	return h
}

type botResp struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Source           string     `json:"source"`
	Platform         string     `json:"platform"`
	PlatformSelfID   string     `json:"platform_self_id"`
	Admins           []string   `json:"admins"`
	PrivateChat      string     `json:"private_chat"`
	PrivateAllowlist []string   `json:"private_allowlist"`
	Groups           []string   `json:"groups"`
	PersonaTemplate  string     `json:"persona_template"`
	PrivateTemplate  string     `json:"private_template"`
	GroupTemplate    string     `json:"group_template"`
	Skills           []string   `json:"skills"`
	Problems         []string   `json:"problems"`
	Chats            int        `json:"chats"`
	LastMessageAt    *time.Time `json:"last_message_at"`
}

// harnessConfigBot is the config bot of newBotsHarnessUsing(template) as
// GET bots lists it before any edit, with problems.
func harnessConfigBot(template string, problems ...string) botResp {
	return botResp{ID: dataBot, Name: "织语", Source: "config", Platform: "qq", PlatformSelfID: "30001",
		Admins: []string{}, PrivateChat: "admins", PrivateAllowlist: []string{}, Groups: []string{},
		PersonaTemplate: template, PrivateTemplate: template, GroupTemplate: template, Skills: []string{"time"},
		Problems: append([]string{}, problems...)}
}

type botsResp struct {
	Bots      []botResp `json:"bots"`
	Templates []string  `json:"templates"`
}

func (h *writeHarness) bots() botsResp {
	h.t.Helper()
	var resp botsResp
	h.getJSON("/panel/api/bots", &resp)
	return resp
}

func decodeBot(t *testing.T, w *httptest.ResponseRecorder, status int) botResp {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body)
	}
	var b botResp
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return b
}

// createBot creates a panel bot through the API.
func (h *writeHarness) createBot(name, template string, skills ...string) botResp {
	h.t.Helper()
	return decodeBot(h.t, h.write(http.MethodPost, "/panel/api/bots",
		map[string]any{"name": name, "persona_template": template, "skills": append([]string{}, skills...)}), http.StatusCreated)
}

func botPath(id string) string { return "/panel/api/bots/" + id }

// sameTime reports whether a and b are both nil or the same instant.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// checkBotAudit checks that kind was audited once, for bot id by the
// harness's user, with a detail containing each of want.
func checkBotAudit(t *testing.T, h *writeHarness, kind, id string, want ...string) {
	t.Helper()
	events := h.audits(kind)
	if len(events) != 1 {
		t.Fatalf("%s audits = %+v, want one", kind, events)
	}
	e := events[0]
	if e.BotInstanceID != id || e.Actor != "panel:"+testUser || e.Target != id {
		t.Errorf("%s audit = %+v, want bot and target %s by panel:%s", kind, e, id, testUser)
	}
	for _, s := range want {
		if !strings.Contains(e.Detail, s) {
			t.Errorf("%s audit detail %s lacks %s", kind, e.Detail, s)
		}
	}
}

func TestBotsAPI_CRUDAndAudit(t *testing.T) {
	h := newBotsHarness(t)

	if body := string(h.getJSON("/panel/api/bots", nil)); !strings.Contains(body, `"problems":[]`) {
		t.Errorf("GET bots = %s, want problems as an empty list", body)
	}
	list := h.bots()
	if !slices.Equal(list.Templates, []string{"cat", "default"}) {
		t.Errorf("templates = %v, want [cat default]", list.Templates)
	}
	configBot := harnessConfigBot("default")
	if len(list.Bots) != 1 || !reflect.DeepEqual(list.Bots[0], configBot) {
		t.Fatalf("bots = %+v, want only %+v", list.Bots, configBot)
	}

	var sk struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	h.getJSON("/panel/api/bots/skills", &sk)
	var names []string
	for _, s := range sk.Skills {
		names = append(names, s.Name)
		if s.Name == "time" && s.Description != "查询当前的日期、星期和时间" {
			t.Errorf("time description = %q", s.Description)
		}
	}
	if !slices.Equal(names, []string{"buff", "memory", "status", "time"}) {
		t.Errorf("skills = %v, want the private-chat skills buff, memory, status and time", names)
	}

	created := decodeBot(t, h.write(http.MethodPost, "/panel/api/bots",
		map[string]any{"name": " 小红 ", "persona_template": "cat", "skills": []string{"time", "memory", "time"}}), http.StatusCreated)
	if !panelBotIDPattern.MatchString(created.ID) {
		t.Fatalf("created ID %q does not match %s", created.ID, panelBotIDPattern)
	}
	want := botResp{ID: created.ID, Name: "小红", Source: "panel", Platform: "panel", PersonaTemplate: "cat",
		PrivateTemplate: "cat", GroupTemplate: "cat", Skills: []string{"time", "memory"}, Problems: []string{}}
	if !reflect.DeepEqual(created, want) {
		t.Fatalf("created = %+v, want %+v", created, want)
	}
	checkBotAudit(t, h, "panel.bot.create", created.ID, `"name":"小红"`, `"persona_template":"cat"`)

	list = h.bots()
	if len(list.Bots) != 2 || !reflect.DeepEqual(list.Bots[1], want) {
		t.Fatalf("bots after create = %+v, want the config bot, then %+v", list.Bots, want)
	}
	w := h.write(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": created.ID, "display_name": "小明"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create chat: status %d, body %s", w.Code, w.Body)
	}
	var chat struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &chat); err != nil || !strings.HasPrefix(chat.Key, created.ID+":panel:private:") {
		t.Fatalf("create chat body %s (%v)", w.Body, err)
	}
	if b := h.bots().Bots[1]; b.Chats != 1 || b.LastMessageAt != nil {
		t.Errorf("bot with a new chat = %+v, want 1 chat and no last message", b)
	}

	for _, tc := range []struct {
		body any
		msg  string
	}{
		{map[string]any{"name": "", "persona_template": "cat", "skills": []string{}}, "名字不能为空"},
		{map[string]any{"name": "小蓝", "persona_template": "dog"}, "人设模板“dog”不存在"},
		{map[string]any{"name": "小蓝", "skills": []string{"grouponly"}}, "技能“grouponly”不能在私聊里用"},
		{`{"name":"小蓝","color":"blue"}`, ""},
		{`{"name":`, ""},
	} {
		body := errorBody(t, h.write(http.MethodPost, "/panel/api/bots", tc.body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if tc.msg != "" && body.Message != tc.msg {
			t.Errorf("create %v: message %q, want %q", tc.body, body.Message, tc.msg)
		}
	}
	if n := len(h.bots().Bots); n != 2 {
		t.Errorf("bots after refused creates = %d, want 2", n)
	}
	if n := len(h.audits("panel.bot.create")); n != 1 {
		t.Errorf("panel.bot.create audits = %d after refused creates, want 1", n)
	}

	path := botPath(created.ID)
	updated := decodeBot(t, h.write(http.MethodPut, path,
		map[string]any{"name": "小绿", "persona_template": "default", "skills": []string{}}), http.StatusOK)
	want = botResp{ID: created.ID, Name: "小绿", Source: "panel", Platform: "panel", PersonaTemplate: "default",
		PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{}, Problems: []string{}, Chats: 1}
	if !reflect.DeepEqual(updated, want) {
		t.Fatalf("updated = %+v, want %+v", updated, want)
	}
	checkBotAudit(t, h, "panel.bot.update", created.ID, `"name":"小绿"`)
	if b := h.bots().Bots[1]; !reflect.DeepEqual(b, want) {
		t.Errorf("listed after update = %+v, want %+v", b, want)
	}
	full := map[string]any{"name": "小绿", "persona_template": "default", "skills": []string{}}
	for _, body := range []any{
		map[string]any{"name": "小绿", "skills": []string{}},
		map[string]any{"name": "小绿", "persona_template": "default"},
		`{"persona_template":"default","skills":[]}`,
		map[string]any{"name": "小绿", "persona_template": "default", "skills": nil},
		map[string]any{"name": "", "persona_template": "default", "skills": []string{}},
	} {
		errorBody(t, h.write(http.MethodPut, path, body), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	errorBody(t, h.write(http.MethodPut, botPath("panel-zzzzzzzz"), full), http.StatusNotFound, conloomapi.CodeNotFound)
	if n := len(h.audits("panel.bot.update")); n != 1 {
		t.Errorf("panel.bot.update audits = %d after refused updates, want 1", n)
	}

	body := errorBody(t, h.write(http.MethodDelete, path, map[string]string{"confirm_name": "小红"}), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != "确认的名字和 bot 的名字不一致" {
		t.Errorf("wrong confirm name: message %q", body.Message)
	}
	errorBody(t, h.write(http.MethodDelete, path, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodDelete, path, `{"confirm_name":"小绿","force":true}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	h.getJSON(convPath(chat.Key), nil)

	if w := h.write(http.MethodDelete, path, map[string]string{"confirm_name": " 小绿 "}); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", w.Code, w.Body)
	}
	checkBotAudit(t, h, "panel.bot.delete", created.ID, `"name":"小绿"`, `"chats":1`)
	h.getErr(convPath(chat.Key), http.StatusNotFound, conloomapi.CodeNotFound)
	if list := h.bots(); len(list.Bots) != 1 || list.Bots[0].ID != dataBot {
		t.Fatalf("bots after delete = %+v, want only the config bot", list.Bots)
	}
	errorBody(t, h.write(http.MethodDelete, path, map[string]string{"confirm_name": "小绿"}), http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPut, path, full), http.StatusNotFound, conloomapi.CodeNotFound)

	// The core audits each change; the panel adds no second event.
	for _, kind := range []string{"panel.bot.create", "panel.bot.update", "panel.bot.delete", "panel.chat.create"} {
		if n := len(h.audits(kind)); n != 1 {
			t.Errorf("%s audits = %d, want 1", kind, n)
		}
	}
}

func TestBotsAPI_DeleteConfigBot403(t *testing.T) {
	h := newBotsHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)

	for _, confirm := range []string{"织语", "冒名"} {
		body := errorBody(t, h.write(http.MethodDelete, botPath(dataBot), map[string]string{"confirm_name": confirm}),
			http.StatusForbidden, conloomapi.CodeForbidden)
		if body.Message != "bot is defined in the config file" || body.Retryable {
			t.Errorf("DELETE config bot confirmed as %s: error %+v", confirm, body)
		}
	}

	list := h.bots()
	want := harnessConfigBot("default")
	want.Chats = 1
	if len(list.Bots) != 1 {
		t.Fatalf("bots = %+v, want the config bot only", list.Bots)
	}
	got := list.Bots[0]
	last := got.LastMessageAt
	got.LastMessageAt = nil
	if !reflect.DeepEqual(got, want) || !sameTime(last, new(h.now.Add(-time.Hour))) {
		t.Fatalf("config bot = %+v, last message %v; want %+v, last message %s", got, last, want, h.now.Add(-time.Hour))
	}
	h.getJSON(convPath(private.Key), nil)
	if n := len(h.audits("panel.bot.delete")); n != 0 {
		t.Errorf("panel.bot.delete audits = %d after refused deletes, want 0", n)
	}
}

// configBotBody is a PUT bots/{id} body that sets every setting of a config
// bot, changed by edit when not nil.
func configBotBody(edit func(map[string]any)) map[string]any {
	b := map[string]any{
		"name": "小明", "admins": []string{"20001", "29999"}, "private_chat": "allowlist",
		"private_allowlist": []string{"20002"}, "groups": []string{"10001", "10002"},
		"private_template": "cat", "group_template": "default", "skills": []string{"time", "grouponly"},
	}
	if edit != nil {
		edit(b)
	}
	return b
}

// configBotSettingKeys are the keys of configBotBody.
var configBotSettingKeys = []string{"name", "admins", "private_chat", "private_allowlist", "groups",
	"private_template", "group_template", "skills"}

func TestBotsAPI_EditConfigBot(t *testing.T) {
	h := newBotsHarness(t)
	ctx := context.Background()
	if err := h.db.UpsertBotInstance(ctx, dataBot, "qq", "织语"); err != nil {
		t.Fatal(err)
	}

	// A config bot may enable every usable skill, also one only for groups.
	var sk struct {
		ConfigSkills []struct {
			Name          string   `json:"name"`
			Description   string   `json:"description"`
			Conversations []string `json:"conversations"`
		} `json:"config_skills"`
	}
	h.getJSON("/panel/api/bots/skills", &sk)
	var names []string
	for _, s := range sk.ConfigSkills {
		names = append(names, s.Name)
		if s.Name == "grouponly" && (s.Description != "只在群里用" || !slices.Equal(s.Conversations, []string{conloomapi.ConvGroup})) {
			t.Errorf("config skill grouponly = %+v, want its description and conversations", s)
		}
	}
	if !slices.Equal(names, []string{"buff", "grouponly", "memory", "status", "time"}) {
		t.Errorf("config skills = %v, want every registered skill with its tools", names)
	}

	// The list shows a config bot's identity and settings; a panel bot has
	// none of the config bot's fields.
	panelBot := h.createBot("小红", "cat")
	var raw struct {
		Bots []map[string]json.RawMessage `json:"bots"`
	}
	h.getJSON("/panel/api/bots", &raw)
	for _, key := range []string{"platform_self_id", "admins", "private_chat", "private_allowlist", "groups"} {
		if _, ok := raw.Bots[0][key]; !ok {
			t.Errorf("config bot lacks %s", key)
		}
		if _, ok := raw.Bots[1][key]; ok {
			t.Errorf("panel bot has %s", key)
		}
	}
	if got := h.bots().Bots[0]; !reflect.DeepEqual(got, harnessConfigBot("default")) {
		t.Fatalf("config bot = %+v, want %+v", got, harnessConfigBot("default"))
	}

	updated := decodeBot(t, h.write(http.MethodPut, botPath(dataBot), configBotBody(func(b map[string]any) {
		b["name"] = " 小明 "
		b["admins"] = []string{"20001", "29999", "20001"}
	})), http.StatusOK)
	want := botResp{ID: dataBot, Name: "小明", Source: "config", Platform: "qq", PlatformSelfID: "30001",
		Admins: []string{"20001", "29999"}, PrivateChat: "allowlist", PrivateAllowlist: []string{"20002"},
		Groups: []string{"10001", "10002"}, PersonaTemplate: "cat", PrivateTemplate: "cat", GroupTemplate: "default",
		Skills: []string{"time", "grouponly"}, Problems: []string{}}
	if !reflect.DeepEqual(updated, want) {
		t.Fatalf("updated = %+v\nwant %+v", updated, want)
	}
	if got := h.bots().Bots[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("listed after the edit = %+v\nwant %+v", got, want)
	}
	stored, err := h.db.GetBotSettings(ctx, dataBot)
	if err != nil {
		t.Fatal(err)
	}
	stored.CreatedAt, stored.UpdatedAt = time.Time{}, time.Time{}
	wantStored := store.BotSettings{BotInstanceID: dataBot, Name: "小明", Admins: []string{"20001", "29999"},
		PrivateChat: "allowlist", PrivateAllowlist: []string{"20002"}, Groups: []string{"10001", "10002"},
		PrivateTemplate: "cat", GroupTemplate: "default", Skills: []string{"time", "grouponly"}, UpdatedBy: "panel:" + testUser}
	if !reflect.DeepEqual(stored, wantStored) {
		t.Errorf("stored settings = %+v\nwant %+v", stored, wantStored)
	}
	insts, err := h.db.ListBotInstances(ctx)
	if err != nil || len(insts) != 2 || insts[1] != (store.BotInstance{ID: dataBot, Platform: "qq", Name: "小明"}) {
		t.Errorf("bot instances = %+v, %v; want %s renamed to 小明", insts, err, dataBot)
	}
	var tpl struct {
		Users []templateUserResp `json:"users"`
	}
	h.getJSON(templatePath("cat"), &tpl)
	if want := []templateUserResp{{dataBot, "小明", "config"}, {panelBot.ID, "小红", "panel"}}; !reflect.DeepEqual(tpl.Users, want) {
		t.Errorf("cat users = %+v, want %+v", tpl.Users, want)
	}

	// The audit names the changed settings and keeps the platform IDs out.
	checkBotAudit(t, h, "panel.bot.update", dataBot,
		`"changed":["name","admins","private_chat","private_allowlist","groups","private_template","skills"]`,
		`"name":"小明"`, `"private_chat":"allowlist"`, `"private_template":"cat"`, `"group_template":"default"`,
		`"skills":["time","grouponly"]`)
	detail := h.audits("panel.bot.update")[0].Detail
	for _, id := range []string{"20001", "29999", "20002", "10001", "10002"} {
		if strings.Contains(detail, id) {
			t.Errorf("panel.bot.update detail %s has the platform ID %s", detail, id)
		}
	}

	// The identity stays the config file's, and every setting is required.
	put := func(body any) *httptest.ResponseRecorder { return h.write(http.MethodPut, botPath(dataBot), body) }
	for _, key := range []string{"id", "platform", "platform_self_id", "persona_template", "source"} {
		errorBody(t, put(configBotBody(func(b map[string]any) { b[key] = "x" })), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	for _, key := range configBotSettingKeys {
		errorBody(t, put(configBotBody(func(b map[string]any) { delete(b, key) })), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		errorBody(t, put(configBotBody(func(b map[string]any) { b[key] = nil })), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	}
	for _, tc := range []struct {
		key   string
		value any
		msg   string
	}{
		{"name", "", "名字不能为空"},
		{"admins", []string{"20001", "200 01"}, "管理员：ID“200 01”不能包含空白、冒号或不可见字符"},
		{"admins", []string{"20001\t"}, "管理员：ID“20001\t”不能包含空白、冒号或不可见字符"},
		{"private_allowlist", []string{""}, "私聊白名单：ID 不能为空"},
		{"private_allowlist", []string{"200\u200b02"}, "私聊白名单：ID“200\u200b02”不能包含空白、冒号或不可见字符"},
		{"groups", []string{"qq:10001"}, "启用的群：ID“qq:10001”不能包含空白、冒号或不可见字符"},
		{"private_chat", "everyone", "私聊开放范围必须是 off、admins、allowlist、all 之一"},
		{"private_chat", "", "私聊开放范围必须是 off、admins、allowlist、all 之一"},
		{"private_template", "dog", "私聊默认模板“dog”不存在"},
		{"group_template", "dog", "群聊默认模板“dog”不存在"},
		{"skills", []string{"time", "nope"}, "技能“nope”不存在"},
	} {
		body := errorBody(t, put(configBotBody(func(b map[string]any) { b[tc.key] = tc.value })), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		if body.Message != tc.msg {
			t.Errorf("%s %q: message %q, want %q", tc.key, tc.value, body.Message, tc.msg)
		}
	}
	if got := h.bots().Bots[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("after refused edits = %+v\nwant %+v", got, want)
	}
	if n := len(h.audits("panel.bot.update")); n != 1 {
		t.Errorf("panel.bot.update audits = %d after refused edits, want 1", n)
	}

	// A panel bot takes no config bot setting.
	errorBody(t, h.write(http.MethodPut, botPath(panelBot.ID),
		map[string]any{"name": "小红", "persona_template": "cat", "skills": []string{}, "admins": []string{}}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	// Empty templates are default; a save changing one setting names only it.
	// Audit IDs use real time, not the harness clock, so leave the previous
	// save's millisecond before the next one; otherwise the two IDs can sort
	// either way.
	h.advance(time.Minute) // audits are listed newest first
	time.Sleep(time.Millisecond)
	decodeBot(t, put(configBotBody(func(b map[string]any) { b["private_template"] = "" })), http.StatusOK)
	h.advance(time.Minute)
	time.Sleep(time.Millisecond)
	decodeBot(t, put(configBotBody(func(b map[string]any) { b["private_template"], b["groups"] = "", []string{"10003"} })), http.StatusOK)
	events := h.audits("panel.bot.update")
	if len(events) != 3 || !strings.Contains(events[0].Detail, `"changed":["groups"]`) ||
		!strings.Contains(events[1].Detail, `"changed":["private_template"]`) || !strings.Contains(events[1].Detail, `"private_template":"default"`) {
		t.Errorf("panel.bot.update audits = %+v, want the private template, then the groups changed", events)
	}
	if b := h.bots().Bots[0]; b.PrivateTemplate != "default" || !slices.Equal(b.Groups, []string{"10003"}) {
		t.Errorf("config bot after the last edits = %+v, want the private template default and the group 10003", b)
	}
}

// platformEvent is the n-th message on qq to the config bot of
// newBotsHarness, from user in the group or private chat convID, at.
func platformEvent(n int, convType, convID, user string, at time.Time) conloomapi.Event {
	id := strconv.Itoa(n)
	return conloomapi.Event{
		SchemaVersion: conloomapi.SchemaVersion, EventID: "evt_" + id, EventType: conloomapi.EventTypeMessageCreated,
		BotInstanceID: dataBot, Platform: "qq", OccurredAt: at,
		Conversation: conloomapi.Conversation{Type: convType, ID: convType + "_" + convID, PlatformConversationID: convID},
		Actor:        conloomapi.Actor{ID: "user_" + user, PlatformUserID: user, DisplayName: "小明"},
		Trigger:      conloomapi.Trigger{Type: conloomapi.TriggerObserve},
		Message:      conloomapi.Message{ID: "msg_" + id, PlatformMessageID: id, Text: "你好"},
	}
}

// TestBotsAPI_EditConfigBotCapabilities checks that a config bot's groups and
// private range saved in the panel take effect in the core at once and are
// what the adapters read from the capabilities endpoint.
func TestBotsAPI_EditConfigBotCapabilities(t *testing.T) {
	h := newBotsHarness(t)
	o, ok := h.srv.d.Chat.(*core.Orchestrator)
	if !ok {
		t.Fatal("the harness's chat service is not the orchestrator")
	}
	const token = "adapter-token-qq-main"
	gw := gateway.New(gateway.Deps{
		Orchestrator: o, Store: h.db, Policies: h.policies, Personas: h.personas, Bots: h.srv.d.Bots,
		Config: &config.Config{APITokens: []config.APIToken{{Name: "adapter", Token: token, BotInstanceID: dataBot,
			Scopes: []string{conloomapi.ScopeEventsWrite}}}},
	}).Handler()
	call := func(method, path string, body any, idemKey string) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if body == nil {
			r = httptest.NewRequest(method, path, nil)
		} else {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r = httptest.NewRequest(method, path, strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Authorization", "Bearer "+token)
		if idemKey != "" {
			r.Header.Set("Idempotency-Key", idemKey)
		}
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, r)
		return w
	}
	capabilities := func() conloomapi.Capabilities {
		t.Helper()
		w := call(http.MethodGet, "/api/v1/capabilities", nil, "")
		var c conloomapi.Capabilities
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatalf("capabilities: status %d, body %s", w.Code, w.Body)
		}
		return c
	}
	submit := func(ev conloomapi.Event) conloomapi.EventAccepted {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/events", ev, ev.EventID)
		var resp conloomapi.EventAccepted
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
			t.Fatalf("event %s: status %d, body %s", ev.EventID, w.Code, w.Body)
		}
		return resp
	}

	if c := capabilities(); c.BotName != "织语" || c.PlatformSelfID != "30001" || c.PrivateChat != "admins" || len(c.EnabledConversations) != 0 {
		t.Fatalf("capabilities before the edit = %+v, want the harness's settings", c)
	}
	if resp := submit(platformEvent(1, conloomapi.ConvGroup, "10002", "20001", h.now)); resp.Disposition != conloomapi.DispositionIgnore ||
		resp.SuppressedReason != conloomapi.SuppressedNotEnabled {
		t.Fatalf("message in group 10002 before the edit = %+v, want ignored as not enabled", resp)
	}
	if resp := submit(platformEvent(2, conloomapi.ConvPrivate, "20002", "20002", h.now)); resp.Disposition != conloomapi.DispositionIgnore ||
		resp.SuppressedReason != conloomapi.SuppressedPrivateNotAllowed {
		t.Fatalf("private message from 20002 before the edit = %+v, want it not allowed", resp)
	}

	decodeBot(t, h.write(http.MethodPut, botPath(dataBot), configBotBody(nil)), http.StatusOK)

	c := capabilities()
	wantConvs := []conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10001"},
		{Type: conloomapi.ConvGroup, PlatformID: "10002"}, {Type: conloomapi.ConvPrivate, PlatformID: "20002"}}
	if c.BotInstanceID != dataBot || c.BotName != "小明" || c.PlatformSelfID != "30001" || c.PrivateChat != "allowlist" ||
		!slices.Equal(c.EnabledConversations, wantConvs) {
		t.Fatalf("capabilities after the edit = %+v, want the saved groups, then the private allowlist, and private_chat allowlist", c)
	}
	if resp := submit(platformEvent(3, conloomapi.ConvGroup, "10002", "20003", h.now)); resp.Disposition != conloomapi.DispositionObserve {
		t.Errorf("message in group 10002 after the edit = %+v, want it observed", resp)
	}
	if resp := submit(platformEvent(4, conloomapi.ConvGroup, "10009", "20003", h.now)); resp.SuppressedReason != conloomapi.SuppressedNotEnabled {
		t.Errorf("message in group 10009 after the edit = %+v, want ignored as not enabled", resp)
	}
	if resp := submit(platformEvent(5, conloomapi.ConvPrivate, "20003", "20003", h.now)); resp.SuppressedReason != conloomapi.SuppressedPrivateNotAllowed {
		t.Errorf("private message from 20003 after the edit = %+v, want it not allowed", resp)
	}
	if resp := submit(platformEvent(6, conloomapi.ConvPrivate, "20002", "20002", h.now.Add(time.Second))); resp.Disposition != conloomapi.DispositionReply {
		t.Errorf("private message from the allowlisted 20002 after the edit = %+v, want a reply", resp)
	}
}

func TestBotsAPI_CSRF(t *testing.T) {
	h := newBotsHarness(t)
	bot := h.createBot("小红", "cat", "time")
	path := botPath(bot.ID)

	for _, req := range []struct{ method, path, body string }{
		{http.MethodPost, "/panel/api/bots", `{"name":"小蓝","persona_template":"cat","skills":[]}`},
		{http.MethodPut, path, `{"name":"小蓝","persona_template":"cat","skills":[]}`},
		{http.MethodDelete, path, `{"confirm_name":"小红"}`},
	} {
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie), withCSRF("wrong")),
			http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCookie(h.cookie), withCSRF(h.csrf), withOrigin("http://attacker.invalid")),
			http.StatusForbidden, conloomapi.CodeForbidden)
		errorBody(t, h.do(req.method, req.path, req.body, withCSRF(h.csrf)), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}
	for _, p := range []string{"/panel/api/bots", "/panel/api/bots/skills", "/panel/api/chats"} {
		errorBody(t, h.do(http.MethodGet, p, ""), http.StatusUnauthorized, conloomapi.CodeUnauthorized)
	}

	if list := h.bots(); len(list.Bots) != 2 || !reflect.DeepEqual(list.Bots[1], bot) {
		t.Fatalf("bots after refused writes = %+v, want the config bot and %+v", list.Bots, bot)
	}
	for kind, n := range map[string]int{"panel.bot.create": 1, "panel.bot.update": 0, "panel.bot.delete": 0} {
		if got := len(h.audits(kind)); got != n {
			t.Errorf("%s audits = %d, want %d", kind, got, n)
		}
	}

	// Without the panel bot service the writes and the skills answer 503;
	// the lists still work.
	u := newWriteHarness(t, nil)
	for _, req := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/panel/api/bots", map[string]any{"name": "小蓝", "persona_template": "cat", "skills": []string{}}},
		{http.MethodPut, path, map[string]any{"name": "小蓝", "persona_template": "cat", "skills": []string{}}},
		{http.MethodDelete, path, map[string]string{"confirm_name": "小红"}},
		{http.MethodGet, "/panel/api/bots/skills", nil},
	} {
		errorBody(t, u.write(req.method, req.path, req.body), http.StatusServiceUnavailable, conloomapi.CodeInternal)
	}
	u.getJSON("/panel/api/bots", nil)
	u.getJSON("/panel/api/chats", nil)
}

type chatsResp struct {
	Bots []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Source string `json:"source"`
		Avatar string `json:"avatar"`
		Chats  []struct {
			Key           string     `json:"key"`
			Title         string     `json:"title"`
			Platform      string     `json:"platform"`
			LastMessageAt *time.Time `json:"last_message_at"`
			MessageCount  int        `json:"message_count"`
			Last          *struct {
				Seq      int64     `json:"seq"`
				FromBot  bool      `json:"from_bot"`
				SenderID string    `json:"sender_id"`
				At       time.Time `json:"at"`
				Text     string    `json:"text"`
			} `json:"last"`
			Online bool `json:"online"`
		} `json:"chats"`
	} `json:"bots"`
}

// storeBotConv stores a conversation of bot botID.
func (h *storeHarness) storeBotConv(botID, platform, convType, convID string) store.ConversationRecord {
	h.t.Helper()
	conv := store.ConversationRecord{
		BotInstanceID: botID, ID: convID, Key: store.ConversationKey(botID, platform, convType, convID),
		Type: convType, Platform: platform, PlatformConversationID: convID,
	}
	h.seeded++
	h.ingestAs(botID, store.Ingest{Conversation: &conv})
	return conv
}

// seedChatMessage stores a message from actorID, going by name, in conv of
// any bot.
func (h *storeHarness) seedChatMessage(conv store.ConversationRecord, actorID, name string, at time.Time) {
	h.t.Helper()
	h.seeded++
	m := store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", h.seeded), BotInstanceID: conv.BotInstanceID, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: fmt.Sprint(h.seeded), ActorID: actorID, PlatformUserID: actorID, DisplayName: name,
		Text: "你好", OccurredAt: at.UTC(), CreatedAt: at.UTC(),
	}
	h.ingestAs(conv.BotInstanceID, store.Ingest{Message: &m})
}

// ingestAs stores in under the seeded event of bot botID.
func (h *storeHarness) ingestAs(botID string, in store.Ingest) {
	h.t.Helper()
	in.Event = store.EventRecord{BotInstanceID: botID, IdempotencyKey: fmt.Sprintf("seed:%d", h.seeded), EventID: fmt.Sprintf("evt_%d", h.seeded)}
	if _, err := h.db.Ingest(context.Background(), in); err != nil {
		h.t.Fatalf("seed: %v", err)
	}
}

// panelChat creates a panel chat of bot botID, whose user goes by name.
func (h *writeHarness) panelChat(botID, name string) store.ConversationRecord {
	h.t.Helper()
	w := h.write(http.MethodPost, "/panel/api/chats", map[string]string{"bot_instance_id": botID, "display_name": name})
	if w.Code != http.StatusCreated {
		h.t.Fatalf("create chat: status %d, body %s", w.Code, w.Body)
	}
	var resp struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		h.t.Fatal(err)
	}
	conv, err := h.db.GetConversationByKey(context.Background(), resp.Key)
	if err != nil {
		h.t.Fatal(err)
	}
	return conv
}

func TestChatsAPI_GroupsAndOrder(t *testing.T) {
	h := newBotsHarness(t)
	// The store records a conversation's creation on the wall clock; these
	// messages come after it, so they order the chats that have them.
	base := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	red := h.createBot("小红", "cat")
	h.advance(time.Minute) // panel bots are listed by creation
	green := h.createBot("小绿", "default")

	group := h.seedConv("qq", conloomapi.ConvGroup, "10001")
	h.seedMessage(group, "大家好", base.Add(time.Hour), false)
	withUser := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(withUser, "在吗", base.Add(2*time.Minute), false)
	h.seedMessage(withUser, "在的", base.Add(3*time.Minute), true)
	h.seedConv("qq", conloomapi.ConvPrivate, "20002")
	configQuiet := h.panelChat(dataBot, "小灰")
	configPanel := h.panelChat(dataBot, "小白")
	h.seedChatMessage(configPanel, "panel_"+testUser, "白白", base.Add(time.Minute))
	gone := h.storeBotConv("panel-gone0001", store.PlatformPanel, conloomapi.ConvPrivate, "panel_gone")
	h.seedChatMessage(gone, "panel_"+testUser, "小黑", base.Add(4*time.Minute))
	unwritten := h.panelChat(red.ID, "阿蓝")
	written := h.panelChat(red.ID, "阿紫")
	h.seedChatMessage(written, "panel_"+testUser, "阿紫", base.Add(5*time.Minute))

	type item struct {
		key, title, platform string
		last                 *time.Time
	}
	check := func(want map[string][]item) {
		t.Helper()
		var resp chatsResp
		h.getJSON("/panel/api/chats", &resp)
		var ids []string
		for _, b := range resp.Bots {
			ids = append(ids, b.ID)
			got := make([]item, 0, len(b.Chats))
			for _, c := range b.Chats {
				got = append(got, item{c.Key, c.Title, c.Platform, c.LastMessageAt})
			}
			if w := want[b.ID]; len(got) != len(w) {
				t.Errorf("bot %s: %d chats, want %d", b.ID, len(got), len(w))
			} else {
				for i := range w {
					if got[i].key != w[i].key || got[i].title != w[i].title || got[i].platform != w[i].platform ||
						!sameTime(got[i].last, w[i].last) {
						t.Errorf("bot %s chat %d = %+v, want %+v", b.ID, i, got[i], w[i])
					}
				}
			}
		}
		if !slices.Equal(ids, []string{dataBot, red.ID, green.ID}) {
			t.Fatalf("groups = %v, want the directory order %s, %s, %s", ids, dataBot, red.ID, green.ID)
		}
		if b := resp.Bots[0]; b.Name != "织语" || b.Source != "config" {
			t.Errorf("config group = %s %s", b.Name, b.Source)
		}
		if b := resp.Bots[1]; b.Name != "小红" || b.Source != "panel" {
			t.Errorf("panel group = %s %s", b.Name, b.Source)
		}
		if b := resp.Bots[2]; b.Chats == nil {
			t.Error("a bot without chats has chats null, want []")
		}
	}
	at := func(d time.Duration) *time.Time { return new(base.Add(d)) }
	configChats := []item{
		{configPanel.Key, "白白", store.PlatformPanel, at(time.Minute)},
		{configQuiet.Key, "小灰", store.PlatformPanel, nil},
	}
	redChats := []item{
		{written.Key, "阿紫", store.PlatformPanel, at(5 * time.Minute)},
		{unwritten.Key, "阿蓝", store.PlatformPanel, nil},
	}
	check(map[string][]item{dataBot: configChats, red.ID: redChats, green.ID: {}})

	// At most 200 panel chats a bot, cut after sorting and leaving out the
	// platform chats: the quiet ones by creation, the newest first, so the one
	// created first is left out.
	capped := []item{configChats[0]}
	for i := range 199 {
		id := fmt.Sprintf("panel_%d", 21000+i)
		conv := h.storeBotConv(dataBot, store.PlatformPanel, conloomapi.ConvPrivate, id)
		capped = slices.Insert(capped, 1, item{conv.Key, id, store.PlatformPanel, nil})
	}
	check(map[string][]item{dataBot: capped, red.ID: redChats, green.ID: {}})
	if b := h.bots().Bots[0]; b.Chats != 203 || b.LastMessageAt == nil || !b.LastMessageAt.Equal(base.Add(3*time.Minute)) {
		t.Errorf("config bot = %+v, want 203 private chats, the last message at %s", b, base.Add(3*time.Minute))
	}
}

func TestChatsAPI_OnlyPanelChats(t *testing.T) {
	h := newBotsHarness(t)
	base := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	chat := h.panelChat(dataBot, "小白")
	h.seedChatMessage(chat, "panel_"+testUser, "白白", base)
	qq := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(qq, "在吗", base.Add(time.Minute), false)
	tg := h.seedConv("telegram", conloomapi.ConvPrivate, "20002")
	h.seedMessage(tg, "在吗", base.Add(2*time.Minute), false)

	var resp chatsResp
	body := h.getJSON("/panel/api/chats", &resp)
	if len(resp.Bots) != 1 || resp.Bots[0].ID != dataBot {
		t.Fatalf("groups = %+v, want the config bot", resp.Bots)
	}
	if chats := resp.Bots[0].Chats; len(chats) != 1 || chats[0].Key != chat.Key || chats[0].Platform != store.PlatformPanel {
		t.Errorf("chats = %+v, want only the panel chat %s", chats, chat.Key)
	}
	if strings.Contains(string(body), "takeover") {
		t.Errorf("chats body %s has a takeover field", body)
	}
}

func TestChatsAPI_NewChatFirst(t *testing.T) {
	h := newBotsHarness(t)
	bot := h.createBot("小红", "cat")
	past := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	older := h.panelChat(bot.ID, "阿蓝")
	h.seedChatMessage(older, "panel_"+testUser, "阿蓝", past)
	recent := h.panelChat(bot.ID, "阿紫")
	h.seedChatMessage(recent, "panel_"+testUser, "阿紫", past.Add(30*time.Minute))
	configOlder := h.panelChat(dataBot, "小灰")
	h.seedChatMessage(configOlder, "panel_"+testUser, "小灰", past.Add(40*time.Minute))

	fresh := h.panelChat(bot.ID, "阿绿")
	configFresh := h.panelChat(dataBot, "小白")

	var resp chatsResp
	h.getJSON("/panel/api/chats", &resp)
	keys := map[string][]string{}
	for _, b := range resp.Bots {
		for _, c := range b.Chats {
			keys[b.ID] = append(keys[b.ID], c.Key)
			if c.Key == fresh.Key && (c.LastMessageAt != nil || c.Title != "阿绿") {
				t.Errorf("new chat = %+v, want titled 阿绿 without a last message", c)
			}
		}
	}
	if want := []string{fresh.Key, recent.Key, older.Key}; !slices.Equal(keys[bot.ID], want) {
		t.Errorf("panel bot chats = %v, want the new empty chat first: %v", keys[bot.ID], want)
	}
	if want := []string{configFresh.Key, configOlder.Key}; !slices.Equal(keys[dataBot], want) {
		t.Errorf("config bot chats = %v, want the new empty chat first: %v", keys[dataBot], want)
	}

	list := h.bots()
	if b := list.Bots[1]; b.ID != bot.ID || b.Chats != 3 || !sameTime(b.LastMessageAt, new(past.Add(30*time.Minute))) {
		t.Errorf("panel bot = %+v, want 3 chats, the last message at %s", b, past.Add(30*time.Minute))
	}
}

func TestChatsAPI_LastMessageAndAvatar(t *testing.T) {
	h := newBotsHarness(t)
	up := decodeUpload(t, h.upload(pngData(t, testImage(64, 64, false)), "image/png"))
	doc := lighthousePersona()
	doc.AppearanceImage = up.ID
	decodeTemplate(t, h.putTemplate("with-image", doc, ""), http.StatusCreated)
	pictured := h.createBot("小红", "with-image")
	plain := h.createBot("小绿", "cat")

	base := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	talked := h.panelChat(dataBot, "小明")
	h.seedMessage(talked, "在吗", base, false)
	reply := h.seedMessage(talked, "第一行\n第二行"+strings.Repeat("好", 70), base.Add(time.Minute), true)
	fresh := h.panelChat(dataBot, "小白")

	var resp chatsResp
	body := h.getJSON("/panel/api/chats", &resp)
	avatars := map[string]string{}
	for _, b := range resp.Bots {
		avatars[b.ID] = b.Avatar
	}
	if want := map[string]string{dataBot: "", pictured.ID: up.ID, plain.ID: ""}; !reflect.DeepEqual(avatars, want) {
		t.Errorf("avatars = %v, want %v", avatars, want)
	}

	chats := resp.Bots[0].Chats
	if len(chats) != 2 || chats[0].Key != talked.Key || chats[1].Key != fresh.Key {
		t.Fatalf("config bot chats = %+v, want %s then %s", chats, talked.Key, fresh.Key)
	}
	c := chats[0]
	if c.MessageCount != 2 || c.Last == nil {
		t.Fatalf("chat with messages = %+v, want 2 messages and the last one", c)
	}
	if !c.Last.FromBot || c.Last.Seq != reply.Seq || c.Last.SenderID != reply.PlatformUserID || !c.Last.At.Equal(base.Add(time.Minute)) {
		t.Errorf("last = %+v, want the bot's reply %d from %s at %s", *c.Last, reply.Seq, reply.PlatformUserID, base.Add(time.Minute))
	}
	if text := c.Last.Text; strings.Contains(text, "\n") || !strings.HasPrefix(text, "第一行 第二行") ||
		utf8.RuneCountInString(text) != 61 || !strings.HasSuffix(text, "…") {
		t.Errorf("last text = %q, want one line of 60 characters and …", text)
	}
	if c := chats[1]; c.MessageCount != 0 || c.Last != nil {
		t.Errorf("new chat = %+v, want no messages and no last", c)
	}

	var raw struct {
		Bots []struct {
			Chats []map[string]json.RawMessage `json:"chats"`
		} `json:"bots"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if got := raw.Bots[0].Chats[1]; string(got["last"]) != "null" || string(got["message_count"]) != "0" {
		t.Errorf("new chat JSON = last %s, message_count %s; want null and 0", got["last"], got["message_count"])
	}
}

func TestChatsAPI_AvatarPrefersAvatarImage(t *testing.T) {
	h := newBotsHarness(t)
	look := decodeUpload(t, h.upload(pngData(t, testImage(64, 32, false)), "image/png"))
	face := decodeUpload(t, h.upload(pngData(t, testImage(64, 64, false)), "image/png"))
	both := lighthousePersona()
	both.AppearanceImage, both.AvatarImage = look.ID, face.ID
	decodeTemplate(t, h.putTemplate("both", both, ""), http.StatusCreated)
	onlyAvatar := lighthousePersona()
	onlyAvatar.AvatarImage = face.ID
	decodeTemplate(t, h.putTemplate("only-avatar", onlyAvatar, ""), http.StatusCreated)
	onlyLook := lighthousePersona()
	onlyLook.AppearanceImage = look.ID
	decodeTemplate(t, h.putTemplate("only-look", onlyLook, ""), http.StatusCreated)
	withBoth, withAvatar, withLook := h.createBot("小红", "both"), h.createBot("小蓝", "only-avatar"), h.createBot("小绿", "only-look")

	var resp chatsResp
	h.getJSON("/panel/api/chats", &resp)
	avatars := map[string]string{}
	for _, b := range resp.Bots {
		avatars[b.ID] = b.Avatar
	}
	if want := map[string]string{dataBot: "", withBoth.ID: face.ID, withAvatar.ID: face.ID, withLook.ID: look.ID}; !reflect.DeepEqual(avatars, want) {
		t.Errorf("avatars = %v, want %v", avatars, want)
	}
}

func TestUsage_DeletedBotName(t *testing.T) {
	h := newBotsHarness(t)
	gone := h.createBot("小红", "cat")
	live := h.createBot("小白", "default")
	decodeBot(t, h.write(http.MethodPut, botPath(gone.ID), map[string]any{"name": "小绿", "persona_template": "cat", "skills": []string{}}),
		http.StatusOK)
	var calls []store.ModelCall
	for _, bot := range []string{dataBot, gone.ID, live.ID} {
		c := usageTestCall(h.now.Add(-time.Hour), "qwen3", 100, 10)
		c.BotInstanceID, c.ConversationKey = bot, ""
		calls = append(calls, c)
	}
	if err := h.db.InsertModelCalls(context.Background(), calls); err != nil {
		t.Fatal(err)
	}
	if w := h.write(http.MethodDelete, botPath(gone.ID), map[string]string{"confirm_name": "小绿"}); w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, body %s", w.Code, w.Body)
	}

	type usageBot struct {
		Name    string `json:"name"`
		Deleted bool   `json:"deleted"`
	}
	var rep struct {
		usage.Report
		Bots map[string]usageBot `json:"bots"`
	}
	h.getJSON("/panel/api/usage?group=bot", &rep)
	var keys []string
	for _, r := range rep.Totals {
		keys = append(keys, r.Key)
	}
	slices.Sort(keys)
	if want := slices.Sorted(slices.Values([]string{dataBot, gone.ID, live.ID})); !slices.Equal(keys, want) {
		t.Errorf("totals keys = %v, want %v", keys, want)
	}
	want := map[string]usageBot{dataBot: {"织语", false}, gone.ID: {"小绿", true}, live.ID: {"小白", false}}
	if !reflect.DeepEqual(rep.Bots, want) {
		t.Errorf("bots = %+v, want %+v", rep.Bots, want)
	}
}

func TestBotLists_IncludePanelBots(t *testing.T) {
	h := newBotsHarness(t)
	bot := h.createBot("小红", "cat")

	var conv struct {
		Bots []botView `json:"bots"`
	}
	h.getJSON("/panel/api/conversations", &conv)
	wantConv := []botView{{ID: dataBot, Name: "织语", Platform: "qq", Source: "config"}, {ID: bot.ID, Name: "小红", Platform: store.PlatformPanel, Source: "panel"}}
	if !reflect.DeepEqual(conv.Bots, wantConv) {
		t.Errorf("conversation list bots = %+v, want %+v", conv.Bots, wantConv)
	}

	var o overviewResponse
	h.getJSON("/panel/api/overview", &o)
	wantOverview := []overviewBot{{ID: dataBot, Platform: "qq", Name: "织语", Source: "config"}, {ID: bot.ID, Platform: store.PlatformPanel, Name: "小红", Source: "panel"}}
	if !reflect.DeepEqual(o.Bots, wantOverview) {
		t.Errorf("overview bots = %+v, want %+v", o.Bots, wantOverview)
	}

	chat := h.panelChat(bot.ID, "小明")
	if w := h.write(http.MethodPost, convPath(chat.Key, "messages"), map[string]string{"text": "你好"}); w.Code != http.StatusAccepted {
		t.Fatalf("message: status %d, body %s", w.Code, w.Body)
	}
	var tasks struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	h.getJSON("/panel/api/tasks?bot="+bot.ID, &tasks)
	if len(tasks.Tasks) != 1 {
		t.Fatalf("tasks of the panel bot = %+v, want one", tasks.Tasks)
	}
	var task struct {
		Task struct {
			BotInstanceID string `json:"bot_instance_id"`
		} `json:"task"`
	}
	h.getJSON("/panel/api/tasks/"+tasks.Tasks[0].ID, &task)
	if task.Task.BotInstanceID != bot.ID {
		t.Errorf("task detail bot = %q, want %s", task.Task.BotInstanceID, bot.ID)
	}
}
