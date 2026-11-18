package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestState_OuterBarsInjected(t *testing.T) {
	mood := conloomapi.DefaultBars()[0]
	belly := conloomapi.PersonaBar{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour},
		Description: "高=很饱", Kind: conloomapi.BarKindOuter, MaxStep: 40, MaxDay: 100}
	blush := conloomapi.PersonaBar{Name: "脸红", Baseline: 0, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour},
		Description: "高=害羞", MaxStep: 20}
	env, clock := stateEnv(t, []string{"buff"}, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Bars = []conloomapi.PersonaBar{mood, belly, blush}
		c.LegacyPersonaTemplates["default"] = doc
	})
	env.seedBar(t, privKeyOf("20001"), "肚子", 80, clock.now())

	req := env.statePrivateRequest(t, clock, "吃饱了吗")
	want := autonomy.RenderState(nil, nil, []autonomy.StateBar{
		{Name: "心情", Description: mood.Description, Value: 60},
		{Name: "脸红", Description: "高=害羞", Value: 0, MaxStep: 20},
		{Name: "肚子", Description: "高=很饱", Value: 80, Outer: true, MaxStep: 40},
	}, nil, time.Time{}, env.o.loc)
	for _, line := range []string{"肚子：80/100（很高，单次最多 ±40）（高=很饱）", "脸红：很低（单次最多 ±20）", "【外在状态"} {
		if !strings.Contains(want, line) {
			t.Fatalf("test block lacks %q:\n%s", line, want)
		}
	}
	if last := lastContent(req); !strings.HasPrefix(last, want+"\n\n【当前消息】") {
		t.Fatalf("last user message = %q, want it to start with the state block\n%s", last, want)
	}
}

func TestState_ExplicitDefaultsUnchanged(t *testing.T) {
	env, clock := stateEnv(t, []string{"buff"}, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		for _, b := range conloomapi.DefaultBars() {
			b.Kind, b.MaxStep, b.MaxDay = conloomapi.BarKindInner, conloomapi.DefaultBarMaxStep, conloomapi.DefaultBarMaxDay
			doc.Bars = append(doc.Bars, b)
		}
		c.LegacyPersonaTemplates["default"] = doc
	})

	req := env.statePrivateRequest(t, clock, "在吗")
	want := autonomy.RenderState(nil, nil, defaultStateBars(60, 70), nil, time.Time{}, env.o.loc)
	last := lastContent(req)
	if !strings.HasPrefix(last, want+"\n\n【当前消息】") {
		t.Fatalf("last user message = %q, want it to start with today's block\n%s", last, want)
	}
	if strings.Contains(last, "单次最多") || strings.Contains(last, "【外在状态") {
		t.Errorf("state block shows a step limit or an outer section for default bars:\n%s", last)
	}
}

func TestState_OuterBarsPerChatPersona(t *testing.T) {
	env, clock := scopeAutonomyEnv(t)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	belly := conloomapi.PersonaBar{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour},
		Description: "高=很饱"}
	outer, inner := conloomapi.DefaultPersona(), conloomapi.DefaultPersona()
	outerBelly := belly
	outerBelly.Kind = conloomapi.BarKindOuter
	outer.Bars = []conloomapi.PersonaBar{outerBelly}
	inner.Bars = []conloomapi.PersonaBar{belly}
	for key, doc := range map[string]conloomapi.PersonaDoc{a: outer, b: inner} {
		if _, err := env.st.AppendPersonaVersion(ctx, store.PersonaVersion{ConversationKey: key, TemplateName: "default",
			Persona: doc, Reason: "init", UpdatedBy: "system"}); err != nil {
			t.Fatal(err)
		}
	}
	env.seedBar(t, scope, "肚子", 80, clock.now())

	inA := env.panelReply(t, a, "饿不饿")
	if !strings.Contains(inA, "【外在状态") || !strings.Contains(inA, "肚子：80/100（很高，单次最多 ±10）") {
		t.Errorf("A's request lacks its outer 肚子:\n%s", inA)
	}
	inB := env.panelReply(t, b, "饿不饿")
	if !strings.Contains(inB, "肚子：很高") || strings.Contains(inB, "【外在状态") || strings.Contains(inB, "80/100") {
		t.Errorf("B's request shows 肚子 other than as an inner bar:\n%s", inB)
	}
}
