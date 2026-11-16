package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const panelBotActor = "panel:admin"

var panelBotIDPattern = regexp.MustCompile(`^panel-[a-z0-9]{8}$`)

// panelBotEnv is a test orchestrator whose skill registry has the built-in
// skills, with fake tools, plus grouponly (group chats only) and broken (its
// tool is not registered), and whose persona templates add gentle.
func panelBotEnv(t *testing.T, opts testOpts) *testEnv {
	t.Helper()
	loaded, err := skills.Load("")
	if err != nil {
		t.Fatal(err)
	}
	loaded["grouponly"] = skills.Skill{Name: "grouponly", Description: "只在群里用", Tools: []string{"now"},
		Conversations: []string{conloomapi.ConvGroup}, Body: "- 群里用 now。"}
	loaded["broken"] = skills.Skill{Name: "broken", Description: "工具没有注册", Tools: []string{"boom"},
		Conversations: []string{conloomapi.ConvPrivate}, Body: "- 用 boom。"}
	var tools []skills.Tool
	for _, name := range []string{"now", "set_status", "adjust_buff", "add_buff", "remove_buff", "remember", "recall", "get_affinity"} {
		tools = append(tools, &recTool{name: name})
	}
	reg, err := skills.NewRegistry(loaded, tools)
	if err != nil {
		t.Fatal(err)
	}
	opts.skills = reg
	configure := opts.configure
	opts.configure = func(c *config.Config) {
		bigWindows(c)
		gentle := conloomapi.DefaultPersona()
		gentle.PersonaName = "温柔人格"
		c.LegacyPersonaTemplates["gentle"] = gentle
		if configure != nil {
			configure(c)
		}
	}
	return newTestOrchestrator(t, opts)
}

func (e *testEnv) createPanelBot(t *testing.T, in PanelBotInput) config.BotConfig {
	t.Helper()
	bot, err := e.o.PanelBots().Create(context.Background(), in, panelBotActor)
	if err != nil {
		t.Fatalf("Create(%+v): %v", in, err)
	}
	return bot
}

func (e *testEnv) createChat(t *testing.T, botID string) string {
	t.Helper()
	key, err := e.o.CreatePanelChat(context.Background(), botID, "admin", "小明")
	if err != nil {
		t.Fatalf("CreatePanelChat(%s): %v", botID, err)
	}
	return key
}

// panelBotMessage sends text in the panel chat convKey of botID and returns
// the ID of the task answering it.
func (e *testEnv) panelBotMessage(t *testing.T, botID, convKey, text string) string {
	t.Helper()
	id := e.submitPanel(t, convKey, "admin", text)
	rec, err := e.st.GetEvent(context.Background(), botID, strings.TrimPrefix(id, "msg_"))
	if err != nil {
		t.Fatalf("event of %s: %v", id, err)
	}
	var resp conloomapi.EventAccepted
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("response to %q = %+v, want a reply task", text, resp)
	}
	return resp.TaskID
}

func (e *testEnv) waitBotTask(t *testing.T, botID, taskID string) conloomapi.Task {
	t.Helper()
	var task conloomapi.Task
	waitUntil(t, "task "+taskID+" to finish", func() bool {
		var err error
		task, err = e.o.GetTask(context.Background(), botID, taskID)
		if err != nil {
			t.Fatalf("GetTask(%s): %v", taskID, err)
		}
		return taskFinished(task.Status)
	})
	return task
}

// lastRequest returns the newest model request.
func (e *testEnv) lastRequest(t *testing.T) map[string]any {
	t.Helper()
	reqs := append(e.short.Requests(), e.long.Requests()...)
	if len(reqs) == 0 {
		t.Fatal("no model request")
	}
	return reqs[len(reqs)-1]
}

func systemPrompt(t *testing.T, req map[string]any) string {
	t.Helper()
	sys, _ := requestMessages(t, req)[0]["content"].(string)
	return sys
}

func (e *testEnv) storedPanelBots(t *testing.T) []store.PanelBot {
	t.Helper()
	bots, err := e.st.ListPanelBots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return bots
}

func (e *testEnv) botAudits(t *testing.T, kind, botID string) []store.AuditEvent {
	t.Helper()
	got, err := e.st.ListAudit(context.Background(), store.AuditFilter{Kind: kind, BotInstanceID: botID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func botInstanceName(t *testing.T, e *testEnv, id string) (string, bool) {
	t.Helper()
	insts, err := e.st.ListBotInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range insts {
		if b.ID == id {
			return b.Name, true
		}
	}
	return "", false
}

func TestPanelBots_CreateValidates(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()

	bot := env.createPanelBot(t, PanelBotInput{Name: "  小红 ", PersonaTemplate: "gentle", Skills: []string{"time", "status", "time"}})
	if !panelBotIDPattern.MatchString(bot.ID) {
		t.Errorf("ID = %q, want panel- and 8 lowercase letters or digits", bot.ID)
	}
	if bot.Name != "小红" || bot.Platform != store.PlatformPanel || bot.PrivateChat != "admins" || bot.PrivateTemplate != "gentle" ||
		!slices.Equal(bot.Skills, []string{"time", "status"}) || bot.PlatformSelfID != "" || len(bot.Admins) != 0 ||
		len(bot.EnabledConversations) != 0 {
		t.Errorf("created bot = %+v", bot)
	}
	if got, ok := env.o.bots.Bot(bot.ID); !ok || got.Name != "小红" || !env.o.bots.IsPanel(bot.ID) {
		t.Errorf("directory bot = %+v, %v; want the new panel bot", got, ok)
	}
	stored := env.storedPanelBots(t)
	if len(stored) != 1 || stored[0].ID != bot.ID || stored[0].Name != "小红" || stored[0].PersonaTemplate != "gentle" ||
		!slices.Equal(stored[0].Skills, []string{"time", "status"}) || stored[0].CreatedBy != panelBotActor {
		t.Errorf("stored panel bots = %+v", stored)
	}
	if name, ok := botInstanceName(t, env, bot.ID); !ok || name != "小红" {
		t.Errorf("bot instance name = %q, %v", name, ok)
	}
	if got := env.botAudits(t, "panel.bot.create", bot.ID); len(got) != 1 || got[0].Actor != panelBotActor || got[0].Target != bot.ID ||
		!strings.Contains(got[0].Detail, "小红") {
		t.Errorf("create audits = %+v", got)
	}

	plain := env.createPanelBot(t, PanelBotInput{Name: strings.Repeat("名", 32)})
	if plain.PrivateTemplate != "default" || len(plain.Skills) != 0 {
		t.Errorf("bot without template or skills = %+v, want default template and no skills", plain)
	}
	if plain.ID == bot.ID {
		t.Error("two bots got the same ID")
	}

	for _, c := range []struct {
		name, reason string
		in           PanelBotInput
	}{
		{"empty name", "名字不能为空", PanelBotInput{Name: " \t "}},
		{"name too long", "名字最多 32 个字", PanelBotInput{Name: strings.Repeat("名", 33)}},
		{"newline", "控制字符或换行", PanelBotInput{Name: "小\n红"}},
		{"control character", "控制字符或换行", PanelBotInput{Name: "小\x07红"}},
		{"line separator", "控制字符或换行", PanelBotInput{Name: "小\u2028红"}},
		{"zero-width space", "不可见的格式字符", PanelBotInput{Name: "小\u200b红"}},
		{"bidi override", "不可见的格式字符", PanelBotInput{Name: "\u202e红小"}},
		{"only format characters", "不可见的格式字符", PanelBotInput{Name: "\u200b\u200d\ufeff"}},
		{"unknown template", `人设模板“nope”不存在`, PanelBotInput{Name: "小红", PersonaTemplate: "nope"}},
		{"unknown skill", `技能“nope”不存在`, PanelBotInput{Name: "小红", Skills: []string{"time", "nope"}}},
		{"group-only skill", `技能“grouponly”不能在私聊里用`, PanelBotInput{Name: "小红", Skills: []string{"grouponly"}}},
		{"skill without its tool", `技能“broken”缺少工具`, PanelBotInput{Name: "小红", Skills: []string{"broken"}}},
	} {
		_, err := svc.Create(ctx, c.in, panelBotActor)
		if !errors.Is(err, ErrInvalidBot) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: err = %v, want ErrInvalidBot saying %q", c.name, err, c.reason)
		}
	}
	if n := len(env.storedPanelBots(t)); n != 2 {
		t.Fatalf("panel bots after invalid creates = %d, want 2", n)
	}
	if _, err := svc.Update(ctx, bot.ID, PanelBotInput{Name: "小\u2066红"}, panelBotActor); !errors.Is(err, ErrInvalidBot) ||
		!strings.Contains(err.Error(), "不可见的格式字符") {
		t.Errorf("rename with an isolate: err = %v, want ErrInvalidBot", err)
	}
	if got, _ := env.o.bots.Bot(bot.ID); got.Name != "小红" {
		t.Errorf("name after refused rename = %q, want 小红", got.Name)
	}

	for len(env.storedPanelBots(t)) < maxPanelBots {
		env.createPanelBot(t, PanelBotInput{Name: "小明"})
	}
	if _, err := svc.Create(ctx, PanelBotInput{Name: "小明"}, panelBotActor); !errors.Is(err, ErrInvalidBot) ||
		!strings.Contains(err.Error(), "最多 50 个") {
		t.Errorf("51st bot: err = %v, want ErrInvalidBot saying at most 50", err)
	}
	if n := len(env.storedPanelBots(t)); n != maxPanelBots {
		t.Errorf("panel bots = %d, want %d", n, maxPanelBots)
	}
	if n := len(env.o.bots.All()); n != maxPanelBots+1 {
		t.Errorf("directory bots = %d, want the config bot and %d panel bots", n, maxPanelBots)
	}
}

func TestPanelBots_CreateSkipsTakenIDs(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	live := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	gone := env.createPanelBot(t, PanelBotInput{Name: "小绿"})
	if err := svc.Delete(ctx, gone.ID, "小绿", panelBotActor); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	candidates := []string{live.ID, gone.ID, "panel-fresh001"}
	svc.newID = func() string {
		id := candidates[0]
		candidates = candidates[1:]
		return id
	}
	bot := env.createPanelBot(t, PanelBotInput{Name: "小蓝"})
	if bot.ID != "panel-fresh001" {
		t.Errorf("ID = %q, want the first one no bot has had", bot.ID)
	}
	if name, _ := botInstanceName(t, env, gone.ID); name != "小绿" {
		t.Errorf("deleted bot's instance name = %q, want kept", name)
	}

	svc.newID = func() string { return live.ID }
	if _, err := svc.Create(ctx, PanelBotInput{Name: "小紫"}, panelBotActor); err == nil {
		t.Error("Create succeeded with every candidate ID taken")
	}
	if n := len(env.storedPanelBots(t)); n != 2 {
		t.Errorf("panel bots = %d, want 2", n)
	}
}

func TestPanelBots_CreateSkipsInvalidIDs(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	svc := env.o.PanelBots()
	candidates := []string{"panel-k3x9:2ma", "panel-k3x9q2ma"}
	svc.newID = func() string {
		id := candidates[0]
		candidates = candidates[1:]
		return id
	}
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	if bot.ID != "panel-k3x9q2ma" {
		t.Errorf("ID = %q, want the first candidate without a colon", bot.ID)
	}
}

func TestPanelBots_CreateThenChat(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红", PersonaTemplate: "gentle", Skills: []string{"time"}})
	key := env.createChat(t, bot.ID)
	if parts := strings.Split(key, ":"); len(parts) != 4 || parts[0] != bot.ID || parts[1] != store.PlatformPanel ||
		parts[2] != conloomapi.ConvPrivate {
		t.Fatalf("chat key = %q, want %s:panel:private:<id>", key, bot.ID)
	}
	task := env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "你好"))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v", task)
	}
	req := env.lastRequest(t)
	if sys := systemPrompt(t, req); !strings.HasPrefix(sys, persona.CorePrompt("小红")) {
		t.Errorf("system prompt does not call the bot 小红:\n%s", sys)
	}
	if got := offeredTools(req); !slices.Equal(got, []string{"now"}) {
		t.Errorf("tools offered = %v, want the time skill's now", got)
	}
	pv, err := env.st.LatestPersona(ctx, key)
	if err != nil || pv.TemplateName != "gentle" || pv.Persona.PersonaName != "温柔人格" {
		t.Errorf("chat persona = %+v, %v; want the gentle template", pv, err)
	}
}

func TestPanelBots_OnlineTemplate(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	if _, err := svc.Create(ctx, PanelBotInput{Name: "小红", PersonaTemplate: "online_cat"}, panelBotActor); !errors.Is(err, ErrInvalidBot) {
		t.Fatalf("Create with a template not created yet err = %v, want ErrInvalidBot", err)
	}
	cat := conloomapi.DefaultPersona()
	cat.PersonaName, cat.CharacterName = "猫人格", "小猫"
	if err := env.o.personas.PutTemplate(ctx, "online_cat", cat, panelBotActor); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红", PersonaTemplate: "online_cat"})
	key := env.createChat(t, bot.ID)
	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "你好"))
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.HasPrefix(sys, persona.CorePrompt("小猫")) || !strings.Contains(sys, persona.Render(cat)) {
		t.Errorf("system prompt of the bot's new chat does not use the online template:\n%s", sys)
	}

	users, err := env.o.personas.DeleteTemplate(ctx, "online_cat")
	if err != nil || !slices.Equal(users, []string{bot.ID}) {
		t.Fatalf("DeleteTemplate of the bot's template = %q, %v; want it deleted, used by %s", users, err, bot.ID)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"人设模板“online_cat”已不存在，暂用 default"}
	if b, _ := env.o.bots.Bot(bot.ID); b.PrivateTemplate != "default" || !slices.Equal(env.o.bots.Problems(bot.ID), want) {
		t.Errorf("bot after its template was deleted = %+v, problems %q; want default and %q", b, env.o.bots.Problems(bot.ID), want)
	}

	// A new chat starts from default; the chat that copied the template keeps it.
	fresh := env.createChat(t, bot.ID)
	if task := env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, fresh, "你好")); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("new chat's task after the template was deleted = %+v", task)
	}
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.HasPrefix(sys, persona.CorePrompt("小红")) ||
		!strings.Contains(sys, persona.Render(conloomapi.DefaultPersona())) {
		t.Errorf("system prompt of a new chat after the template was deleted does not use default:\n%s", sys)
	}
	if task := env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "还在吗")); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("old chat's task after the template was deleted = %+v", task)
	}
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.HasPrefix(sys, persona.CorePrompt("小猫")) || !strings.Contains(sys, persona.Render(cat)) {
		t.Errorf("system prompt of the chat that copied the deleted template changed:\n%s", sys)
	}
}

func TestPanelBots_TemplateDeletedBeforeReloadStillReplies(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	cat := conloomapi.DefaultPersona()
	cat.PersonaName, cat.CharacterName = "猫人格", "小猫"
	if err := env.o.personas.PutTemplate(ctx, "online_cat", cat, panelBotActor); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红", PersonaTemplate: "online_cat"})
	if _, err := env.o.personas.DeleteTemplate(ctx, "online_cat"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	// The bots are not published again, as while the delete's reload is
	// pending or after it failed: the directory still names the template.
	if b, _ := env.o.bots.Bot(bot.ID); b.PrivateTemplate != "online_cat" {
		t.Fatalf("bot before the reload = %+v, want it still on online_cat", b)
	}

	key := env.createChat(t, bot.ID)
	task := env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "你好"))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("first reply of a new chat whose template is gone = %+v, want it answered", task)
	}
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.HasPrefix(sys, persona.CorePrompt("小红")) ||
		!strings.Contains(sys, persona.Render(conloomapi.DefaultPersona())) {
		t.Errorf("system prompt of the new chat does not use default:\n%s", sys)
	}
	if pv, err := env.st.LatestPersona(ctx, key); err != nil || pv.Version != 1 || pv.TemplateName != "default" {
		t.Errorf("new chat's persona = %+v, %v; want version 1 from default", pv, err)
	}
	if !slices.ContainsFunc(strings.Split(env.logs.String(), "\n"), func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, "online_cat") && strings.Contains(l, key)
	}) {
		t.Errorf("no warning about the missing template in the log:\n%s", env.logs)
	}
}

func TestPanelBots_UpdateTakesEffectNextReply(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	key := env.createChat(t, bot.ID)
	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "你好"))
	first := env.lastRequest(t)
	if sys := systemPrompt(t, first); !strings.HasPrefix(sys, persona.CorePrompt("小红")) {
		t.Errorf("first system prompt does not call the bot 小红:\n%s", sys)
	}
	if got := offeredTools(first); len(got) != 0 {
		t.Errorf("tools offered before the update = %v, want none", got)
	}

	updated, err := svc.Update(ctx, bot.ID, PanelBotInput{Name: " 小蓝", PersonaTemplate: "gentle", Skills: []string{"time"}}, panelBotActor)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != bot.ID || updated.Name != "小蓝" || updated.PrivateTemplate != "gentle" || !slices.Equal(updated.Skills, []string{"time"}) ||
		updated.Platform != store.PlatformPanel {
		t.Errorf("updated bot = %+v", updated)
	}
	if got, _ := env.o.bots.Bot(bot.ID); got.Name != "小蓝" {
		t.Errorf("directory name = %q, want 小蓝", got.Name)
	}
	if name, _ := botInstanceName(t, env, bot.ID); name != "小蓝" {
		t.Errorf("bot instance name = %q, want 小蓝", name)
	}
	if stored := env.storedPanelBots(t); len(stored) != 1 || stored[0].CreatedBy != panelBotActor || stored[0].Name != "小蓝" {
		t.Errorf("stored = %+v", stored)
	}
	if got := env.botAudits(t, "panel.bot.update", bot.ID); len(got) != 1 || got[0].Actor != panelBotActor ||
		!strings.Contains(got[0].Detail, "小蓝") {
		t.Errorf("update audits = %+v", got)
	}

	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "还在吗"))
	second := env.lastRequest(t)
	if sys := systemPrompt(t, second); !strings.HasPrefix(sys, persona.CorePrompt("小蓝")) {
		t.Errorf("system prompt after the rename does not call the bot 小蓝:\n%s", sys)
	}
	if got := offeredTools(second); !slices.Equal(got, []string{"now"}) {
		t.Errorf("tools offered after the update = %v, want now", got)
	}
	if pv, err := env.st.LatestPersona(ctx, key); err != nil || pv.TemplateName != "default" || pv.Version != 1 {
		t.Errorf("existing chat's persona = %+v, %v; want its first version from default kept", pv, err)
	}
	fresh := env.createChat(t, bot.ID)
	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, fresh, "你好"))
	if pv, err := env.st.LatestPersona(ctx, fresh); err != nil || pv.TemplateName != "gentle" {
		t.Errorf("new chat's persona = %+v, %v; want the new default template gentle", pv, err)
	}

	if _, err := svc.Update(ctx, bot.ID, PanelBotInput{Name: "小蓝", Skills: []string{"grouponly"}}, panelBotActor); !errors.Is(err, ErrInvalidBot) {
		t.Errorf("update with a group-only skill: err = %v, want ErrInvalidBot", err)
	}
	if _, err := svc.Update(ctx, "panel-zzzzzzzz", PanelBotInput{Name: "小蓝"}, panelBotActor); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update of an unknown bot: err = %v, want store.ErrNotFound", err)
	}
	if got, _ := env.o.bots.Bot(bot.ID); got.Name != "小蓝" || !slices.Equal(got.Skills, []string{"time"}) {
		t.Errorf("directory bot after refused updates = %+v", got)
	}
}

// rowsOfBot counts, per table, the rows of the database at path that belong
// to bot id: by bot_instance_id, or by a conversation_key of the bot.
func rowsOfBot(t *testing.T, path, id string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, table := range tables {
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var name string
			if err := cols.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := cols.Close(); err != nil {
			t.Fatal(err)
		}
		var where []string
		var args []any
		if slices.Contains(names, "bot_instance_id") {
			where, args = append(where, "bot_instance_id = ?"), append(args, id)
		}
		if slices.Contains(names, "conversation_key") {
			where, args = append(where, "instr(conversation_key, ?) = 1"), append(args, id+":")
		}
		if table == "bot_instances" || table == "panel_bots" {
			where, args = append(where, "id = ?"), append(args, id)
		}
		if len(where) == 0 {
			continue
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM "`+table+`" WHERE `+strings.Join(where, " OR "), args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n > 0 {
			out[table] = n
		}
	}
	return out
}

func TestPanelBots_DeleteBusyThenRetry(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()

	other := env.createPanelBot(t, PanelBotInput{Name: "小绿"})
	otherKey := env.createChat(t, other.ID)
	otherTask := env.panelBotMessage(t, other.ID, otherKey, "你好")
	env.waitBotTask(t, other.ID, otherTask)
	env.submit(t, env.groupEvent("20001", "大家好"))

	bot := env.createPanelBot(t, PanelBotInput{Name: "小红", Skills: []string{"time"}})
	idle := env.createChat(t, bot.ID)
	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, idle, "你好"))
	busy := env.createChat(t, bot.ID)
	entered, release := env.blockFirstCompletion(t, func(store.TaskCompletion) bool { return true })
	busyTask := env.panelBotMessage(t, bot.ID, busy, "在吗")
	waitEntered(t, entered)
	if len(rowsOfBot(t, env.dbPath, bot.ID)) < 5 {
		t.Fatalf("rows of the bot before the delete = %v, want its chats' data", rowsOfBot(t, env.dbPath, bot.ID))
	}

	env.o.panelDeleteWait = 50 * time.Millisecond
	if err := svc.Delete(ctx, bot.ID, "小红", panelBotActor); !errors.Is(err, ErrChatBusy) {
		t.Fatalf("Delete while a reply runs: err = %v, want ErrChatBusy", err)
	}
	if _, ok := env.o.bots.Bot(bot.ID); !ok {
		t.Error("bot left the directory after a refused delete")
	}
	if !slices.ContainsFunc(env.storedPanelBots(t), func(b store.PanelBot) bool { return b.ID == bot.ID }) {
		t.Error("bot deleted from the store after a refused delete")
	}
	if _, err := env.st.GetConversation(ctx, bot.ID, strings.Split(busy, ":")[3]); err != nil {
		t.Errorf("busy chat after a refused delete: %v", err)
	}
	if got := env.botAudits(t, "panel.bot.delete", bot.ID); len(got) != 0 {
		t.Errorf("delete audits after a refused delete = %+v", got)
	}
	extra := env.createChat(t, bot.ID)

	env.o.panelDeleteWait = waitLimit
	deleted := make(chan error, 1)
	go func() { deleted <- svc.Delete(ctx, bot.ID, " 小红 ", panelBotActor) }()
	waitUntil(t, "the bot to be marked as being deleted", func() bool { return svc.notDeleting(bot.ID) != nil })
	if _, err := env.o.CreatePanelChat(ctx, bot.ID, "admin", "小明"); !errors.Is(err, ErrBotDeleting) {
		t.Errorf("CreatePanelChat during the delete: err = %v, want ErrBotDeleting", err)
	}
	if _, _, err := env.o.SubmitPanel(ctx, busy, "admin", "还在吗"); !errors.Is(err, ErrBotDeleting) {
		t.Errorf("SubmitPanel during the delete: err = %v, want ErrBotDeleting", err)
	}
	if _, err := svc.Update(ctx, bot.ID, PanelBotInput{Name: "小蓝"}, panelBotActor); !errors.Is(err, ErrBotDeleting) {
		t.Errorf("Update during the delete: err = %v, want ErrBotDeleting", err)
	}
	if err := svc.Delete(ctx, bot.ID, "小红", panelBotActor); !errors.Is(err, ErrBotDeleting) {
		t.Errorf("second Delete during the delete: err = %v, want ErrBotDeleting", err)
	}
	if _, _, err := env.o.SubmitPanel(ctx, otherKey, "admin", "还在吗"); err != nil {
		t.Errorf("SubmitPanel to another bot's chat during the delete: %v", err)
	}
	release()
	if err := <-deleted; err != nil {
		t.Fatalf("Delete once the reply stopped: %v", err)
	}

	if _, err := env.st.GetTask(ctx, bot.ID, busyTask); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the reply's task after the delete: %v", err)
	}
	if _, ok := env.o.bots.Bot(bot.ID); ok || env.o.bots.IsPanel(bot.ID) {
		t.Error("deleted bot still in the directory")
	}
	if slices.ContainsFunc(env.storedPanelBots(t), func(b store.PanelBot) bool { return b.ID == bot.ID }) {
		t.Error("deleted bot still stored")
	}
	if name, ok := botInstanceName(t, env, bot.ID); !ok || name != "小红" {
		t.Errorf("deleted bot's instance = %q, %v; want kept with its name", name, ok)
	}
	got := rowsOfBot(t, env.dbPath, bot.ID)
	for table := range got {
		if table != "bot_instances" && table != "model_calls" && table != "audit_events" {
			t.Errorf("rows of the deleted bot left in %s: %v", table, got)
		}
	}
	for _, key := range []string{idle, busy, extra} {
		if _, err := env.st.GetConversation(ctx, bot.ID, strings.Split(key, ":")[3]); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("chat %s after the delete: %v", key, err)
		}
	}
	if audits := env.botAudits(t, "panel.bot.delete", bot.ID); len(audits) != 1 || audits[0].Actor != panelBotActor ||
		audits[0].Target != bot.ID || !strings.Contains(audits[0].Detail, "小红") {
		t.Errorf("delete audits = %+v", audits)
	}
	if _, err := env.o.CreatePanelChat(ctx, bot.ID, "admin", "小明"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreatePanelChat after the delete: err = %v, want store.ErrNotFound", err)
	}
	if err := svc.Delete(ctx, bot.ID, "小红", panelBotActor); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete twice: err = %v, want store.ErrNotFound", err)
	}

	if _, ok := env.o.bots.Bot(other.ID); !ok {
		t.Error("the other panel bot left the directory")
	}
	if _, err := env.o.GetTask(ctx, other.ID, otherTask); err != nil {
		t.Errorf("the other bot's task: %v", err)
	}
	if msgs, err := env.st.RecentMessages(ctx, otherKey, 10); err != nil || len(msgs) != 2 {
		t.Errorf("the other bot's messages = %d, %v; want both kept", len(msgs), err)
	}
	if msgs, err := env.st.RecentMessages(ctx, testConvKey, 10); err != nil || len(msgs) != 1 {
		t.Errorf("the config bot's messages = %d, %v; want kept", len(msgs), err)
	}
}

func TestPanelBots_DeleteWrongName400(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	key := env.createChat(t, bot.ID)

	for _, name := range []string{"小明", "", "小红红"} {
		if err := svc.Delete(ctx, bot.ID, name, panelBotActor); !errors.Is(err, ErrInvalidBot) || !strings.Contains(err.Error(), "名字") {
			t.Errorf("Delete confirmed as %q: err = %v, want ErrInvalidBot about the name", name, err)
		}
	}
	if _, ok := env.o.bots.Bot(bot.ID); !ok || len(env.storedPanelBots(t)) != 1 {
		t.Error("bot gone after a delete with the wrong name")
	}
	if _, err := env.st.GetConversation(ctx, bot.ID, strings.Split(key, ":")[3]); err != nil {
		t.Errorf("chat after a delete with the wrong name: %v", err)
	}
	if err := svc.notDeleting(bot.ID); err != nil {
		t.Errorf("bot marked as being deleted after a refused delete: %v", err)
	}
	env.waitBotTask(t, bot.ID, env.panelBotMessage(t, bot.ID, key, "你好"))
	if got := env.botAudits(t, "panel.bot.delete", bot.ID); len(got) != 0 {
		t.Errorf("delete audits = %+v", got)
	}
	if err := svc.Delete(ctx, "panel-zzzzzzzz", "小红", panelBotActor); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete of an unknown bot: err = %v, want store.ErrNotFound", err)
	}
}

func TestPanelBots_ConfigBotRefused(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	env.submit(t, env.groupEvent("20001", "大家好"))

	if _, err := svc.Update(ctx, testBot, PanelBotInput{Name: "小红"}, panelBotActor); !errors.Is(err, ErrConfigBot) {
		t.Errorf("Update of the config bot: err = %v, want ErrConfigBot", err)
	}
	if err := svc.Delete(ctx, testBot, "织语", panelBotActor); !errors.Is(err, ErrConfigBot) {
		t.Errorf("Delete of the config bot: err = %v, want ErrConfigBot", err)
	}
	if bot, ok := env.o.bots.Bot(testBot); !ok || bot.Name != "织语" || bot.Platform != "qq" {
		t.Errorf("config bot = %+v, %v; want unchanged", bot, ok)
	}
	if msgs, err := env.st.RecentMessages(ctx, testConvKey, 10); err != nil || len(msgs) != 1 {
		t.Errorf("config bot's messages = %d, %v; want kept", len(msgs), err)
	}
	if len(env.storedPanelBots(t)) != 0 || len(env.botAudits(t, "", testBot)) != 0 {
		t.Error("a refused change of the config bot was stored or audited")
	}
}

func TestPanelBots_NonPanelEventRejected(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红"})
	for _, ev := range []conloomapi.Event{
		env.event(conloomapi.ConvPrivate, "20001", "20001", "你好"),
		env.groupEvent("20001", "大家好", mentionBot),
	} {
		ev.BotInstanceID = bot.ID
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := env.o.Submit(ctx, bot.ID, "k:"+ev.Message.ID, body, ev); !errors.Is(err, errPanelBotPlatform) {
			t.Errorf("%s event of the panel bot: err = %v, want errPanelBotPlatform", ev.Conversation.Type, err)
		}
		if _, err := env.st.GetEvent(ctx, bot.ID, "k:"+ev.Message.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s event stored: %v", ev.Conversation.Type, err)
		}
		if _, err := env.st.GetConversation(ctx, bot.ID, ev.Conversation.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s conversation stored: %v", ev.Conversation.Type, err)
		}
	}
}

func TestPanelBots_LoadMarksProblems(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	svc := env.o.PanelBots()
	now := time.Now().UTC()
	for _, b := range []store.PanelBot{
		{ID: "panel-aaaa0001", Name: "小红", PersonaTemplate: "gone", Skills: []string{"time", "nope", "grouponly", "broken"},
			CreatedBy: panelBotActor, CreatedAt: now, UpdatedAt: now},
		{ID: "panel-aaaa0002", Name: "小绿", PersonaTemplate: "gentle", Skills: []string{"status"},
			CreatedBy: panelBotActor, CreatedAt: now.Add(time.Second), UpdatedAt: now},
	} {
		if err := env.st.CreatePanelBot(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := env.o.bots.Bot("panel-aaaa0001"); ok {
		t.Fatal("panel bot in the directory before Load")
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var ids []string
	for _, b := range env.o.bots.All() {
		ids = append(ids, b.ID)
	}
	if !slices.Equal(ids, []string{testBot, "panel-aaaa0001", "panel-aaaa0002"}) {
		t.Errorf("directory = %v, want the config bot, then the panel bots by creation", ids)
	}
	broken, _ := env.o.bots.Bot("panel-aaaa0001")
	if broken.Name != "小红" || broken.Platform != store.PlatformPanel || broken.PrivateTemplate != "default" ||
		!slices.Equal(broken.Skills, []string{"time"}) {
		t.Errorf("bot with stale settings = %+v, want default template and only the time skill", broken)
	}
	problems := env.o.bots.Problems("panel-aaaa0001")
	if len(problems) != 4 {
		t.Errorf("problems = %q, want one per stale setting", problems)
	}
	for _, want := range []string{`人设模板“gone”已不存在`, `技能“nope”不存在`, `技能“grouponly”不能在私聊里用`, `技能“broken”缺少工具`} {
		if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, want) }) {
			t.Errorf("problems %q lack %q", problems, want)
		}
	}
	fine, _ := env.o.bots.Bot("panel-aaaa0002")
	if fine.PrivateTemplate != "gentle" || !slices.Equal(fine.Skills, []string{"status"}) || env.o.bots.Problems("panel-aaaa0002") != nil {
		t.Errorf("bot with valid settings = %+v, problems %q", fine, env.o.bots.Problems("panel-aaaa0002"))
	}
	logs := env.logs.String()
	for _, want := range []string{"gone", "nope", "grouponly", "broken"} {
		if !slices.ContainsFunc(strings.Split(logs, "\n"), func(l string) bool {
			return strings.Contains(l, "level=WARN") && strings.Contains(l, want)
		}) {
			t.Errorf("no warning about %s in the log:\n%s", want, logs)
		}
	}
	if stored := env.storedPanelBots(t); stored[0].PersonaTemplate != "gone" || len(stored[0].Skills) != 4 {
		t.Errorf("Load rewrote the stored bot: %+v", stored[0])
	}

	key := env.createChat(t, "panel-aaaa0001")
	env.waitBotTask(t, "panel-aaaa0001", env.panelBotMessage(t, "panel-aaaa0001", key, "你好"))
	if pv, err := env.st.LatestPersona(ctx, key); err != nil || pv.TemplateName != "default" {
		t.Errorf("chat persona = %+v, %v; want the default template", pv, err)
	}

	if _, err := svc.Update(ctx, "panel-aaaa0001", PanelBotInput{Name: "小红", PersonaTemplate: "gentle", Skills: []string{"time"}}, panelBotActor); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if p := env.o.bots.Problems("panel-aaaa0001"); p != nil {
		t.Errorf("problems after fixing the settings = %q", p)
	}
	if b, _ := env.o.bots.Bot("panel-aaaa0002"); b.Name != "小绿" || b.PrivateTemplate != "gentle" {
		t.Errorf("other bot after an update = %+v", b)
	}
}

func TestPanelBots_LoadMarksConfigBotProblems(t *testing.T) {
	env := panelBotEnv(t, testOpts{configure: func(c *config.Config) { c.Bots[0].PrivateTemplate, c.Bots[0].GroupTemplate = "night-owl", "night-owl" }})
	ctx := context.Background()
	if err := env.o.PanelBots().Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	bot, _ := env.o.bots.Bot(testBot)
	want := []string{"人设模板“night-owl”已不存在，暂用 default"}
	if bot.PrivateTemplate != "default" || !slices.Equal(env.o.bots.Problems(testBot), want) {
		t.Fatalf("config bot with a missing template = %+v, problems %q; want default and %q", bot, env.o.bots.Problems(testBot), want)
	}
	if !slices.ContainsFunc(strings.Split(env.logs.String(), "\n"), func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, "night-owl") && strings.Contains(l, testBot)
	}) {
		t.Errorf("no warning about night-owl in the log:\n%s", env.logs)
	}
	if got, want := env.command(t, "20001", "/persona"), CopyPersonaInfo("默认群友人格", 1, ""); got != want {
		t.Errorf("/persona = %q, want %q: the default template", got, want)
	}

	owl := conloomapi.DefaultPersona()
	owl.PersonaName = "夜猫人格"
	if err := env.o.personas.PutTemplate(ctx, "night-owl", owl, panelBotActor); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	if err := env.o.PanelBots().Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if bot, _ := env.o.bots.Bot(testBot); bot.PrivateTemplate != "night-owl" || env.o.bots.Problems(testBot) != nil {
		t.Fatalf("config bot after its template was created = %+v, problems %q; want night-owl without problems", bot, env.o.bots.Problems(testBot))
	}
	users, err := env.o.personas.DeleteTemplate(ctx, "night-owl")
	if err != nil || !slices.Equal(users, []string{testBot}) {
		t.Fatalf("DeleteTemplate of the config bot's template = %q, %v; want it deleted, used by %s", users, err, testBot)
	}
	if err := env.o.PanelBots().Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if bot, _ := env.o.bots.Bot(testBot); bot.PrivateTemplate != "default" || !slices.Equal(env.o.bots.Problems(testBot), want) {
		t.Errorf("config bot after its template was deleted = %+v, problems %q; want default and %q", bot, env.o.bots.Problems(testBot), want)
	}
}

func TestPanelBots_ImportClearsTemplateProblems(t *testing.T) {
	env := panelBotEnv(t, testOpts{configure: func(c *config.Config) { c.Bots[0].PrivateTemplate, c.Bots[0].GroupTemplate = "night-owl", "night-owl" }})
	ctx := context.Background()
	svc := env.o.PanelBots()
	cat := conloomapi.DefaultPersona()
	cat.PersonaName = "猫人格"
	if err := env.o.personas.PutTemplate(ctx, "online_cat", cat, panelBotActor); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	bot := env.createPanelBot(t, PanelBotInput{Name: "小红", PersonaTemplate: "online_cat"})
	// The template disappears behind the catalog's back, as a stored row
	// found invalid at startup does.
	if _, err := env.st.DeletePersonaTemplate(ctx, "online_cat"); err != nil {
		t.Fatal(err)
	}
	if err := env.o.personas.LoadTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, id := range []string{testBot, bot.ID} {
		if b, _ := env.o.bots.Bot(id); b.PrivateTemplate != "default" || len(env.o.bots.Problems(id)) != 1 {
			t.Fatalf("bot %s with a missing template = %+v, problems %q; want default with a problem", id, b, env.o.bots.Problems(id))
		}
	}

	owl := conloomapi.DefaultPersona()
	owl.PersonaName = "夜猫人格"
	f := persona.ExchangeFile{Format: persona.ExchangeFormat, Version: persona.ExchangeVersion,
		Templates: []persona.ExchangeTemplate{{Name: "night-owl", Persona: owl}, {Name: "online_cat", Persona: cat}}}
	if _, err := env.o.personas.Import(ctx, f, persona.ConflictFail, panelBotActor); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for id, want := range map[string]string{testBot: "night-owl", bot.ID: "online_cat"} {
		if b, _ := env.o.bots.Bot(id); b.PrivateTemplate != want || env.o.bots.Problems(id) != nil {
			t.Errorf("bot %s after its template was imported = %+v, problems %q; want %s without problems", id, b, env.o.bots.Problems(id), want)
		}
	}
}

func TestPanelBots_PrivateSkills(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	var names []string
	for _, s := range env.o.PanelBots().PrivateSkills() {
		if s.Description == "" {
			t.Errorf("skill %s has no description", s.Name)
		}
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"buff", "memory", "status", "time"}) {
		t.Errorf("private skills = %v, want those for private chats whose tools are registered", names)
	}
	none := newTestOrchestrator(t, testOpts{})
	if got := none.o.PanelBots().PrivateSkills(); len(got) != 0 {
		t.Errorf("private skills without a registry = %v", got)
	}
}
