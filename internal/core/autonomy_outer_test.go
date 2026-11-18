package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// outerBelly is the bar 肚子, outer with the step limits 40 and 100.
func outerBelly() conloomapi.PersonaBar {
	return conloomapi.PersonaBar{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour},
		Description: "高=很饱", Kind: conloomapi.BarKindOuter, MaxStep: 40, MaxDay: 100}
}

func TestAutonomyService_BarKindAndLimits(t *testing.T) {
	mood := conloomapi.DefaultBars()[0]
	blush := conloomapi.PersonaBar{Name: "脸红", Baseline: 10, HalfLife: conloomapi.Duration{Duration: time.Hour}, MaxStep: 20}
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Bars = []conloomapi.PersonaBar{mood, outerBelly(), blush}
		c.LegacyPersonaTemplates["default"] = doc
	})
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()
	env.seedCustomBar(t, privateConvKey, "饥饿", "高=很饿", 30, 2*time.Hour, 30, now)

	got, err := env.o.Autonomy().State(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []conloomapi.AutonomyBar{
		{Name: "心情", Value: 60, Band: "偏高", Baseline: 60, Description: "高=开心放松，低=低落烦躁"},
		{Name: "肚子", Value: 20, Band: "偏低", Baseline: 20, Description: "高=很饱", Kind: conloomapi.BarKindOuter, MaxStep: 40, MaxDay: 100},
		{Name: "脸红", Value: 10, Band: "很低", Baseline: 10, MaxStep: 20},
		{Name: "饥饿", Value: 30, Band: "偏低", Baseline: 30, Description: "高=很饿", Custom: true},
	}
	if !reflect.DeepEqual(got.Bars, want) {
		t.Fatalf("bars = %+v, want the persona's kinds and limits as written, none for the bot's 饥饿: %+v", got.Bars, want)
	}
	raw, err := json.Marshal(got.Bars[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"kind"`, `"max_step"`, `"max_day"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("JSON of the default 心情 = %s, want no %s", raw, key)
		}
	}
}

func TestAutonomyService_BarKindPerChat(t *testing.T) {
	env, _ := scopeAutonomyEnv(t)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	inner := outerBelly()
	inner.Kind, inner.MaxStep, inner.MaxDay = "", 0, 0
	docA, docB := conloomapi.DefaultPersona(), conloomapi.DefaultPersona()
	docA.Bars = []conloomapi.PersonaBar{conloomapi.DefaultBars()[0], outerBelly()}
	docB.Bars = []conloomapi.PersonaBar{conloomapi.DefaultBars()[0], inner}
	for key, doc := range map[string]conloomapi.PersonaDoc{a: docA, b: docB} {
		if _, err := env.st.AppendPersonaVersion(ctx, store.PersonaVersion{ConversationKey: key, TemplateName: "default",
			Persona: doc, Reason: "init", UpdatedBy: "system"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.o.Autonomy().SetBar(ctx, a, "肚子", 80, "测试", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}

	belly := func(key string) conloomapi.AutonomyBar {
		t.Helper()
		s, err := env.o.Autonomy().State(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Bars) != 2 || s.Bars[1].Name != "肚子" {
			t.Fatalf("bars of %s = %+v, want 心情 and 肚子", key, s.Bars)
		}
		return s.Bars[1]
	}
	ba, bb := belly(a), belly(b)
	if ba.Kind != conloomapi.BarKindOuter || ba.MaxStep != 40 || ba.MaxDay != 100 {
		t.Errorf("A's 肚子 = %+v, want outer, 40, 100 from A's persona", ba)
	}
	if bb.Kind != "" || bb.MaxStep != 0 || bb.MaxDay != 0 {
		t.Errorf("B's 肚子 = %+v, want no kind or limits from B's persona", bb)
	}
	if ba.Value != 80 || bb.Value != 80 {
		t.Errorf("values = %d in A, %d in B; want the shared 80 in both", ba.Value, bb.Value)
	}
}
