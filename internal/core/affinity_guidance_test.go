package core

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestAffinityGuidance_ProtectedGroup(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	env.injectSeed(t, testConvKey, "",
		store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	last := lastContent(env.short.Requests()[0])
	if !strings.Contains(last, "【关系提示】") || !strings.Contains(last, "熟悉") {
		t.Fatalf("request lacks the relationship hint:\n%s", last)
	}
	for _, unwanted := range []string{"内容规则", "对方明确主动"} {
		if strings.Contains(last, unwanted) {
			t.Errorf("protected group request carries open-mode wording %q:\n%s", unwanted, last)
		}
	}
}

func TestAffinityGuidance_OpenPrivate(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	key := privateKey("29999")
	bot, _ := env.cfg.Bot(testBot)
	p := conloomapi.DefaultPersona()
	p.Adult = true
	p.ContentRules = "测试内容规则"
	if _, err := env.o.personas.Update(context.Background(), key, bot.TemplateFor(conloomapi.ConvPrivate),
		conloomapi.PersonaPut{Persona: &p, Reason: "test"}, "test"); err != nil {
		t.Fatal(err)
	}
	env.injectSeed(t, key, "", store.MemberMemory{PlatformUserID: "29999", Note: "喜欢猫", Affinity: injectAffinity(82)})
	task := env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "在吗")).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	last := lastContent(env.short.Requests()[0])
	if !strings.Contains(last, "【关系提示】") || !strings.Contains(last, "很要好") {
		t.Fatalf("request lacks the relationship hint:\n%s", last)
	}
	for _, want := range []string{"对方明确主动并同意", "内容规则"} {
		if !strings.Contains(last, want) {
			t.Errorf("open private request lacks %q:\n%s", want, last)
		}
	}
}

// guidanceSide is the intimacy side of the guidance tests.
var guidanceSide = conloomapi.PersonaIntimacy{Tone: "说话放软一些", Mind: "想靠近对方", Behavior: "会多陪对方聊一会儿"}

// guidancePersona sets a persona with content rules, adult when adult is set
// and with side when it is not nil, on convKey.
func (e *testEnv) guidancePersona(t *testing.T, convKey string, adult bool, side *conloomapi.PersonaIntimacy) {
	t.Helper()
	bot, _ := e.cfg.Bot(testBot)
	p := conloomapi.DefaultPersona()
	p.Adult = adult
	p.ContentRules = "测试内容规则"
	p.Intimacy = side
	if _, err := e.o.personas.Update(context.Background(), convKey, bot.TemplateFor(convType(convKey)),
		conloomapi.PersonaPut{Persona: &p, Reason: "test"}, "test"); err != nil {
		t.Fatal(err)
	}
}

// guidancePrivate has 29999, at affinity, send a message in their private
// chat, whose persona is adult when adult is set and has side when it is not
// nil, and returns the reply request.
func (e *testEnv) guidancePrivate(t *testing.T, affinity int, adult bool, side *conloomapi.PersonaIntimacy) map[string]any {
	t.Helper()
	key := privateKey("29999")
	e.guidancePersona(t, key, adult, side)
	e.injectSeed(t, key, "", store.MemberMemory{PlatformUserID: "29999", Note: "喜欢猫", Affinity: injectAffinity(affinity)})
	task := e.waitTask(t, e.submit(t, e.event(conloomapi.ConvPrivate, "29999", "29999", "在吗")).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	return e.short.Requests()[0]
}

// systemContent returns the content of the request's system message.
func systemContent(t *testing.T, req map[string]any) string {
	t.Helper()
	msgs := requestMessages(t, req)
	if len(msgs) == 0 || msgs[0]["role"] != "system" {
		t.Fatalf("request has no system message: %v", req)
	}
	s, _ := msgs[0]["content"].(string)
	return s
}

func TestAffinityGuidance_IntimacyMajorAtTop(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	side := guidanceSide
	req := env.guidancePrivate(t, 90, true, &side)
	last := lastContent(req)
	for _, want := range []string{"【关系提示】", "【亲密面】", "现在以它为主，但你本来的性格、说话习惯和底线不变。",
		"语气：" + side.Tone, "心理：" + side.Mind, "表现：" + side.Behavior} {
		if !strings.Contains(last, want) {
			t.Errorf("request lacks %q:\n%s", want, last)
		}
	}
	if want := CopyDirectiveAffinity("很要好", true) + "\n" + CopyIntimacySide(conloomapi.IntimacyMajor, side) + "\n\n"; !strings.Contains(last, want) {
		t.Errorf("request lacks the relationship hint followed by the intimacy side %q:\n%s", want, last)
	}
	system := systemContent(t, req)
	for _, text := range []string{"【亲密面】", side.Tone, side.Mind, side.Behavior} {
		if strings.Contains(system, text) {
			t.Errorf("system message contains %q:\n%s", text, system)
		}
	}
}

func TestAffinityGuidance_IntimacyNoneAtAcquaintance(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	side := guidanceSide
	last := lastContent(env.guidancePrivate(t, 30, true, &side))
	if strings.Contains(last, "【亲密面】") {
		t.Errorf("request at 认识 carries the intimacy side:\n%s", last)
	}
	if want := CopyDirectiveAffinity("认识", true) + "\n\n"; !strings.Contains(last, want) {
		t.Errorf("request lacks the relationship hint %q:\n%s", want, last)
	}
}

func TestAffinityGuidance_IntimacyCustomLevels(t *testing.T) {
	side := guidanceSide
	side.Levels = []string{conloomapi.IntimacyNone, conloomapi.IntimacyLight, conloomapi.IntimacyLight,
		conloomapi.IntimacyMajor, conloomapi.IntimacyMajor}
	for _, c := range []struct {
		tier     string
		affinity int
		level    string
		mix      string
		absent   string
	}{
		// The default levels give none at 认识 and half at 亲近.
		{"认识", 30, conloomapi.IntimacyLight, "现在只偶尔流露一点，大部分时候还是平常的你。", ""},
		{"亲近", 70, conloomapi.IntimacyMajor, "现在以它为主，但你本来的性格、说话习惯和底线不变。", "现在平常的你和它大约各占一半。"},
	} {
		t.Run(c.tier, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
			last := lastContent(env.guidancePrivate(t, c.affinity, true, &side))
			if want := CopyDirectiveAffinity(c.tier, true) + "\n" + CopyIntimacySide(c.level, side); !strings.Contains(last, want) {
				t.Errorf("request lacks %q:\n%s", want, last)
			}
			if !strings.Contains(last, c.mix) {
				t.Errorf("request lacks the %s mix wording %q:\n%s", c.level, c.mix, last)
			}
			if c.absent != "" && strings.Contains(last, c.absent) {
				t.Errorf("request carries the default levels' wording %q:\n%s", c.absent, last)
			}
		})
	}
}

func TestAffinityGuidance_IntimacyNeverProtected(t *testing.T) {
	side := guidanceSide
	check := func(t *testing.T, req map[string]any) {
		t.Helper()
		all := requestText(t, req)
		for _, text := range []string{"【亲密面】", side.Tone, side.Mind, side.Behavior} {
			if strings.Contains(all, text) {
				t.Errorf("protected request contains %q:\n%s", text, all)
			}
		}
		if want := CopyDirectiveAffinity("很要好", false) + "\n\n"; !strings.Contains(lastContent(req), want) {
			t.Errorf("protected request lacks the relationship hint %q:\n%s", want, lastContent(req))
		}
	}
	t.Run("group", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
		env.guidancePersona(t, testConvKey, true, &side)
		env.injectSeed(t, testConvKey, "",
			store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(90)})
		task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
		if task.Status != conloomapi.TaskSucceeded {
			t.Fatalf("task = %+v", task)
		}
		check(t, env.short.Requests()[0])
	})
	t.Run("private not adult", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
		check(t, env.guidancePrivate(t, 90, false, &side))
	})
}

func TestAffinityGuidance_NoIntimacyUnchanged(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	last := lastContent(env.guidancePrivate(t, 90, true, nil))
	start := strings.Index(last, "【关系提示】")
	if start < 0 {
		t.Fatalf("request lacks the relationship hint:\n%s", last)
	}
	hint, _, ok := strings.Cut(last[start:], "\n\n")
	if want := CopyDirectiveAffinity("很要好", true); !ok || hint != want {
		t.Errorf("relationship hint = %q (followed by a blank line: %v), want %q", hint, ok, want)
	}
}

func TestAffinityDirective_ReadsMemoryScope(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	ctx := context.Background()
	key := env.createChat(t, testBot)
	const user = "panel_admin"
	scope := memory.ScopeKeyOf(key)
	if scope == key {
		t.Fatalf("panel chat %s is its own memory scope", key)
	}
	env.injectSeed(t, scope, "", store.MemberMemory{PlatformUserID: user, Affinity: injectAffinity(50)})
	if m := env.memberIn(t, key, user); m.Affinity != nil {
		t.Fatalf("member row under the chat itself = %+v, want none", m)
	}
	pv, err := env.o.policies.Current(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if pol := pv.Policy; !pol.Memory.Enabled || !pol.Memory.Affinity || !pol.Concurrency.AllowLongRequests {
		t.Fatalf("panel chat policy has memory or affinity off: %+v", pol)
	}
	task := store.TaskRecord{ID: "task_scope", BotInstanceID: testBot, ConversationKey: key, ActorPlatformUserID: user}
	got := env.o.affinityDirective(ctx, task, pv.Policy, nil, conloomapi.ContentProtected, nil)
	if want := CopyDirectiveAffinity(memory.TierName(50), false); want == "" || got != want {
		t.Errorf("affinityDirective = %q, want %q", got, want)
	}
}

func TestAffinityGuidance_Disabled(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
	}{
		{"affinity off", injectConfig(false)},
		{"memory disabled", func(c *config.Config) {
			injectConfig(true)(c)
			c.DefaultGroupPolicy.Memory.Enabled = false
		}},
		{"long requests off", func(c *config.Config) {
			injectConfig(true)(c)
			c.DefaultGroupPolicy.Concurrency.AllowLongRequests = false
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: c.configure})
			env.injectSeed(t, testConvKey, "",
				store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫", Affinity: injectAffinity(52)})
			task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
			if task.Status != conloomapi.TaskSucceeded {
				t.Fatalf("task = %+v", task)
			}
			if last := lastContent(env.short.Requests()[0]); strings.Contains(last, "【关系提示】") {
				t.Fatalf("request carries a relationship hint while affinity is off:\n%s", last)
			}
		})
	}
}
