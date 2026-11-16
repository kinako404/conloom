package core

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
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
