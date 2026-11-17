package core

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// testAdultAppearance is the adult appearance of the adult appearance tests.
const testAdultAppearance = "占位的成年外观文字。"

// adultAppearancePersona is a persona with an appearance and
// testAdultAppearance, adult when adult is set.
func adultAppearancePersona(adult bool) conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.Adult = adult
	p.Appearance = "短发，常穿一件蓝色雨衣。"
	p.AdultAppearance = testAdultAppearance
	return p
}

// adultAppearanceRequest sets p as convKey's persona, then returns the reply
// request of adultAppearanceReply.
func (e *testEnv) adultAppearanceRequest(t *testing.T, convKey string, p conloomapi.PersonaDoc) map[string]any {
	t.Helper()
	bot, _ := e.cfg.Bot(testBot)
	if _, err := e.o.personas.Update(context.Background(), convKey, bot.TemplateFor(convType(convKey)),
		conloomapi.PersonaPut{Persona: &p, Reason: "test"}, "test"); err != nil {
		t.Fatal(err)
	}
	return e.adultAppearanceReply(t, convKey)
}

// adultAppearanceReply has a member send a message in convKey, a group or
// 29999's private chat, and returns the reply request.
func (e *testEnv) adultAppearanceReply(t *testing.T, convKey string) map[string]any {
	t.Helper()
	ev := e.groupEvent("20001", "你好", mentionBot)
	if convType(convKey) == conloomapi.ConvPrivate {
		ev = e.event(conloomapi.ConvPrivate, "29999", "29999", "在吗")
	}
	task := e.waitTask(t, e.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	return e.short.Requests()[0]
}

func TestAdultAppearance_OpenPrivate(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	req := env.adultAppearanceRequest(t, privateKey("29999"), adultAppearancePersona(true))
	want := "\n\n外貌：\n短发，常穿一件蓝色雨衣。\n\n私密场景下的外貌（只在这段私聊里用）：\n" + testAdultAppearance
	if system := systemContent(t, req); !strings.Contains(system, want) {
		t.Errorf("open private system message lacks the adult appearance after the appearance %q:\n%s", want, system)
	}
}

func TestAdultAppearance_NeverProtected(t *testing.T) {
	check := func(t *testing.T, req map[string]any) {
		t.Helper()
		all := requestText(t, req)
		for _, text := range []string{testAdultAppearance, "私密场景下的外貌"} {
			if strings.Contains(all, text) {
				t.Errorf("protected request contains %q:\n%s", text, all)
			}
		}
		if system := systemContent(t, req); !strings.Contains(system, "\n\n外貌：\n短发，常穿一件蓝色雨衣。") {
			t.Errorf("protected system message lacks the appearance:\n%s", system)
		}
	}
	t.Run("group adult", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{})
		check(t, env.adultAppearanceRequest(t, testConvKey, adultAppearancePersona(true)))
	})
	t.Run("private not adult", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{})
		check(t, env.adultAppearanceRequest(t, privateKey("29999"), adultAppearancePersona(false)))
	})
	t.Run("private minor", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{})
		key := privateKey("29999")
		minor := adultAppearancePersona(true)
		minor.Minor = true
		if _, err := env.st.AppendPersonaVersion(context.Background(), store.PersonaVersion{
			ConversationKey: key, TemplateName: "default", Persona: minor,
			Reason: "edited in the database", UpdatedBy: "system",
		}); err != nil {
			t.Fatalf("AppendPersonaVersion: %v", err)
		}
		check(t, env.adultAppearanceReply(t, key))
	})
}
