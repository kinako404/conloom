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
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
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
// finds the bots using it, as in the app. The config bot is dataBot, whose
// persona template is default, the persona templates are the built-in default
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
		[]config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PrivateTemplate: template, GroupTemplate: template, Skills: []string{"time"}}},
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
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Source          string     `json:"source"`
	PersonaTemplate string     `json:"persona_template"`
	PrivateTemplate string     `json:"private_template"`
	GroupTemplate   string     `json:"group_template"`
	Skills          []string   `json:"skills"`
	Problems        []string   `json:"problems"`
	Chats           int        `json:"chats"`
	LastMessageAt   *time.Time `json:"last_message_at"`
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
	configBot := botResp{ID: dataBot, Name: "织语", Source: "config", PersonaTemplate: "default",
		PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{"time"}, Problems: []string{}}
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
	want := botResp{ID: created.ID, Name: "小红", Source: "panel", PersonaTemplate: "cat",
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
	want = botResp{ID: created.ID, Name: "小绿", Source: "panel", PersonaTemplate: "default",
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

func TestBotsAPI_ConfigBot403(t *testing.T) {
	h := newBotsHarness(t)
	private := h.seedConv("qq", conloomapi.ConvPrivate, "20001")
	h.seedMessage(private, "在吗", h.now.Add(-time.Hour), false)

	body := errorBody(t, h.write(http.MethodPut, botPath(dataBot),
		map[string]any{"name": "冒名", "persona_template": "cat", "skills": []string{}}), http.StatusForbidden, conloomapi.CodeForbidden)
	if body.Message != "bot is defined in the config file" || body.Retryable {
		t.Errorf("PUT config bot: error %+v", body)
	}
	errorBody(t, h.write(http.MethodDelete, botPath(dataBot), map[string]string{"confirm_name": "织语"}),
		http.StatusForbidden, conloomapi.CodeForbidden)

	list := h.bots()
	want := botResp{ID: dataBot, Name: "织语", Source: "config", PersonaTemplate: "default",
		PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{"time"},
		Problems: []string{}, Chats: 1}
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
	for _, kind := range []string{"panel.bot.update", "panel.bot.delete"} {
		if n := len(h.audits(kind)); n != 0 {
			t.Errorf("%s audits = %d after refused changes, want 0", kind, n)
		}
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
		Chats  []struct {
			Key           string     `json:"key"`
			Title         string     `json:"title"`
			Platform      string     `json:"platform"`
			LastMessageAt *time.Time `json:"last_message_at"`
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
