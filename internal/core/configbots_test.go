package core

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// TestTemplateFor_ByConversationType checks that a config bot's new group
// starts from its group template and a new private chat from its private
// template, on the first reply and in commands.
func TestTemplateFor_ByConversationType(t *testing.T) {
	lighthouse := conloomapi.DefaultPersona()
	lighthouse.PersonaName, lighthouse.CharacterName = "灯塔人格", "小明"
	owl := conloomapi.DefaultPersona()
	owl.PersonaName, owl.CharacterName = "夜猫人格", "小红"
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.LegacyPersonaTemplates["lighthouse"], c.LegacyPersonaTemplates["night-owl"] = lighthouse, owl
		c.Bots[0].PrivateChat = "all"
		c.Bots[0].PrivateTemplate, c.Bots[0].GroupTemplate = "lighthouse", "night-owl"
	}})
	ctx := context.Background()

	group := env.submit(t, env.groupEvent("20001", "你好", mentionBot))
	if task := env.waitTask(t, group.TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("group reply = %+v, want it answered", task)
	}
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.Contains(sys, persona.Render(owl)) || strings.Contains(sys, "灯塔人格") {
		t.Errorf("system prompt of the group's first reply does not use the group template night-owl:\n%s", sys)
	}
	if pv, err := env.st.LatestPersona(ctx, testConvKey); err != nil || pv.Version != 1 || pv.TemplateName != "night-owl" ||
		!reflect.DeepEqual(pv.Persona, owl) {
		t.Errorf("group persona = %+v, %v; want version 1 from night-owl", pv, err)
	}

	privateKey := store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_20001")
	private := env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", "在吗"))
	if task := env.waitTask(t, private.TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("private reply = %+v, want it answered", task)
	}
	if sys := systemPrompt(t, env.lastRequest(t)); !strings.Contains(sys, persona.Render(lighthouse)) || strings.Contains(sys, "夜猫人格") {
		t.Errorf("system prompt of the private chat's first reply does not use the private template lighthouse:\n%s", sys)
	}
	if pv, err := env.st.LatestPersona(ctx, privateKey); err != nil || pv.Version != 1 || pv.TemplateName != "lighthouse" {
		t.Errorf("private persona = %+v, %v; want version 1 from lighthouse", pv, err)
	}

	if got, want := env.command(t, "20002", "/persona"), CopyPersonaInfo("夜猫人格", 1, "小红"); got != want {
		t.Errorf("/persona in the group = %q, want %q", got, want)
	}
	newGroup := store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "group_10002")
	if _, err := env.o.personas.Current(ctx, newGroup, mustBot(t, env).TemplateFor(conloomapi.ConvGroup)); err != nil {
		t.Fatal(err)
	}
	if pv, err := env.st.LatestPersona(ctx, newGroup); err != nil || pv.TemplateName != "night-owl" {
		t.Errorf("another new group's persona = %+v, %v; want night-owl", pv, err)
	}
}

func mustBot(t *testing.T, env *testEnv) config.BotConfig {
	t.Helper()
	b, ok := env.o.bots.Bot(testBot)
	if !ok {
		t.Fatalf("no bot %s", testBot)
	}
	return b
}

// TestPanelBots_LoadPublishesStoredConfigBotSettings checks that Load gives a
// config bot its stored settings, falling back for each missing template and
// leaving out each skill that is not registered or lacks a tool (a group-only
// skill is fine) with a problem, and that the core goes by them at once.
func TestPanelBots_LoadPublishesStoredConfigBotSettings(t *testing.T) {
	env := panelBotEnv(t, testOpts{})
	ctx := context.Background()
	given := mustBot(t, env)
	if err := env.o.PanelBots().Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := mustBot(t, env); !reflect.DeepEqual(got, given) || env.o.bots.Problems(testBot) != nil {
		t.Fatalf("config bot after Load = %+v, problems %v; want its migrated settings %+v", got, env.o.bots.Problems(testBot), given)
	}

	s := store.BotSettings{BotInstanceID: testBot, Name: "小明", Admins: []string{"20001"}, PrivateChat: "allowlist",
		PrivateAllowlist: []string{"20002"}, Groups: []string{"10002"}, PrivateTemplate: "gentle", GroupTemplate: "night-owl",
		Skills: []string{"time", "nope", "grouponly", "broken"}, UpdatedBy: "panel:admin"}
	if err := env.st.PutBotSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := mustBot(t, env); got.Name != given.Name {
		t.Fatalf("config bot before Load = %+v, want the settings it had", got)
	}
	if err := env.o.PanelBots().Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := config.BotConfig{ID: testBot, Platform: "qq", PlatformSelfID: "30001", Name: "小明", Admins: []string{"20001"},
		EnabledConversations: []conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10002"},
			{Type: conloomapi.ConvPrivate, PlatformID: "20002"}},
		PrivateChat: "allowlist", PrivateTemplate: "gentle", GroupTemplate: "default", Skills: []string{"time", "grouponly"}}
	if got := mustBot(t, env); !reflect.DeepEqual(got, want) {
		t.Fatalf("config bot = %+v\nwant %+v", got, want)
	}
	wantProblems := []string{"人设模板“night-owl”已不存在，暂用 default", "技能“nope”不存在", "技能“broken”缺少工具，暂时不能用"}
	if got := env.o.bots.Problems(testBot); !slices.Equal(got, wantProblems) {
		t.Errorf("problems = %q, want %q", got, wantProblems)
	}
	if got := env.o.bots.TemplateUsers("gentle"); !slices.Equal(got, []string{testBot}) {
		t.Errorf("TemplateUsers(gentle) = %v, want the config bot", got)
	}
	if stored, err := env.st.GetBotSettings(ctx, testBot); err != nil || stored.GroupTemplate != "night-owl" || len(stored.Skills) != 4 {
		t.Errorf("stored settings after Load = %+v, %v; want them as stored", stored, err)
	}

	if resp := env.submit(t, env.groupEvent("20001", "你好", mentionBot)); resp.Disposition != conloomapi.DispositionIgnore ||
		resp.SuppressedReason != conloomapi.SuppressedNotEnabled {
		t.Errorf("message in group 10001, no longer enabled = %+v, want ignored", resp)
	}
	if resp := env.submit(t, env.event(conloomapi.ConvPrivate, "20002", "20002", "在吗")); resp.Disposition != conloomapi.DispositionReply {
		t.Errorf("private message from the allowlisted 20002 = %+v, want a reply", resp)
	} else {
		env.waitTask(t, resp.TaskID)
	}
}
