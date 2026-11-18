package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// outerPersonas is a PersonaSource giving each conversation its own persona.
type outerPersonas map[string]conloomapi.PersonaDoc

func (p outerPersonas) Persona(_ context.Context, _, convKey string) (conloomapi.PersonaDoc, error) {
	return p[convKey], nil
}

// outerBuffPersona has 心情 with the default limits, an outer 肚子 moving up
// to 40 per call and 100 per day and an inner 脸红 moving up to 20 per call.
func outerBuffPersona() conloomapi.PersonaDoc {
	doc := conloomapi.DefaultPersona()
	doc.Bars = []conloomapi.PersonaBar{
		conloomapi.DefaultBars()[0],
		{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}, Kind: conloomapi.BarKindOuter,
			MaxStep: 40, MaxDay: 100},
		{Name: "脸红", Baseline: 0, HalfLife: conloomapi.Duration{Duration: time.Hour}, MaxStep: 20},
	}
	return doc
}

func TestAdjustBuff_PerBarLimits(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, outerBuffPersona())
	inv := invocation(privateKey, "20001", "小明")
	day := testNow.In(shanghai).Format(time.DateOnly)
	var results []string
	steps := 0
	step := func(args, want string) {
		t.Helper()
		steps++
		inv.TaskID = "task_" + string(rune('a'+steps))
		got := call(t, tool, inv, args)
		results = append(results, got)
		if got != want {
			t.Errorf("step %d adjust_buff(%s) = %q, want %q", steps, args, got, want)
		}
	}

	step(`{"name":"肚子","delta":35,"reason":"吃了午饭"}`, "肚子：偏低 → 中等")
	checkBuff(t, st, privateKey, "肚子", 55, day, 35)
	step(`{"name":"肚子","delta":60,"reason":"又吃了一大碗"}`, "肚子：中等 → 很高（超过单次上限 ±40，按 +40 调整）")
	checkBuff(t, st, privateKey, "肚子", 95, day, 75)
	step(`{"name":"肚子","delta":40,"reason":"吃了甜点"}`, "肚子：很高 → 很高（今天的变化快到上限 ±100，只调整了 +25）")
	checkBuff(t, st, privateKey, "肚子", 100, day, 100)
	events := len(buffEvents(t, st, privateKey))
	step(`{"name":"肚子","delta":1,"reason":"喝了口水"}`, "肚子今天的变化已经到上限（每天最多 ±100），这次没有调整。")
	checkBuff(t, st, privateKey, "肚子", 100, day, 100)
	if n := len(buffEvents(t, st, privateKey)); n != events {
		t.Errorf("a capped change recorded an event: %d events, want %d", n, events)
	}
	step(`{"name":"肚子","delta":-500,"reason":"跑了很久"}`, "肚子：很高 → 偏高（超过单次上限 ±40，按 -40 调整）")
	checkBuff(t, st, privateKey, "肚子", 60, day, 60)

	step(`{"name":"脸红","delta":20,"reason":"被夸了"}`, "脸红：很低 → 偏低")
	step(`{"name":"脸红","delta":20,"reason":"又被夸了"}`, "脸红：偏低 → 偏低（今天的变化快到上限 ±30，只调整了 +10）")
	checkBuff(t, st, privateKey, "脸红", 30, day, 30)

	step(`{"name":"心情","delta":40,"reason":"好消息"}`, "心情：偏高 → 偏高（超过单次上限 ±10，按 +10 调整）")
	checkBuff(t, st, privateKey, "心情", 70, day, 10)
	seedBuff(t, st, privateKey, "心情", 50, day, 25, testNow)
	step(`{"name":"心情","delta":40,"reason":"大好消息"}`, "心情：中等 → 中等（今天的变化快到上限 ±30，只调整了 +5）")
	checkBuff(t, st, privateKey, "心情", 55, day, 30)

	step(`{"name":"心情","delta":0,"reason":"没事"}`, "delta 需要是不为 0 的整数，不超过这一条的单次上限（默认 ±10）。")

	long := conloomapi.DefaultPersona()
	long.Bars = []conloomapi.PersonaBar{{Name: "肚子饱不饱呀", Baseline: 50, HalfLife: conloomapi.Duration{Duration: time.Hour},
		Kind: conloomapi.BarKindOuter, MaxStep: 100, MaxDay: 100}}
	tool = newAdjustBuff(st, long)
	step(`{"name":"肚子饱不饱呀","delta":-100,"reason":"饿了"}`, "肚子饱不饱呀：中等 → 很低")
	seedBuff(t, st, privateKey, "肚子饱不饱呀", 100, day, -1, testNow)
	step(`{"name":"肚子饱不饱呀","delta":-100,"reason":"饿了"}`, "肚子饱不饱呀：很高 → 很低（今天的变化快到上限 ±100，只调整了 -99）")
	seedBuff(t, st, privateKey, "肚子饱不饱呀", 100, day, 0, testNow)
	step(`{"name":"肚子饱不饱呀","delta":-500,"reason":"饿了"}`, "肚子饱不饱呀：很高 → 很低（超过单次上限 ±100，按 -100 调整）")
	seedBuff(t, st, privateKey, "肚子饱不饱呀", 100, day, -99, testNow)
	step(`{"name":"肚子饱不饱呀","delta":-500,"reason":"饿了"}`, "肚子饱不饱呀：很高 → 很高（今天的变化快到上限 ±100，只调整了 -1）")
	seedBuff(t, st, privateKey, "肚子饱不饱呀", 0, day, -100, testNow)
	step(`{"name":"肚子饱不饱呀","delta":-100,"reason":"饿了"}`, "肚子饱不饱呀今天的变化已经到上限（每天最多 ±100），这次没有调整。")

	for _, r := range results {
		if n := utf8.RuneCountInString(r); n > shortResultRunes {
			t.Errorf("result %q has %d runes, over %d", r, n, shortResultRunes)
		}
	}
}

func TestAdjustBuff_CustomBarKeepsDefaults(t *testing.T) {
	st := newStore(t)
	add := newAddBuff(st, newPolicies(st, true, true), outerBuffPersona())
	tool := newAdjustBuff(st, outerBuffPersona())
	inv := invocation(privateKey, "20001", "小明")
	call(t, add, inv, `{"kind":"bar","name":"饥饿","baseline":20,"half_life_hours":2,"value":50}`)
	if got, want := call(t, tool, inv, `{"name":"饥饿","delta":40,"reason":"一天没吃饭"}`),
		"饥饿：中等 → 偏高（超过单次上限 ±10，按 +10 调整）"; got != want {
		t.Errorf("adjust_buff of a bar the bot added = %q, want %q", got, want)
	}
	checkBuff(t, st, privateKey, "饥饿", 60, testNow.In(shanghai).Format(time.DateOnly), 10)
}

func TestAdjustBuff_PanelChatsUseTheirPersonasLimits(t *testing.T) {
	st := newStore(t)
	a := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	scope := memory.ScopeKeyOf(a)
	inner := conloomapi.DefaultPersona()
	inner.Bars = []conloomapi.PersonaBar{{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}}}
	tool := NewAdjustBuff(st, outerPersonas{a: outerBuffPersona(), b: inner})
	day := testNow.In(shanghai).Format(time.DateOnly)
	steps := 0
	step := func(convKey, args, want string) {
		t.Helper()
		steps++
		inv := invocation(convKey, "20001", "小明")
		inv.TaskID = "task_" + string(rune('a'+steps))
		if got := call(t, tool, inv, args); got != want {
			t.Errorf("step %d adjust_buff(%s) in %s = %q, want %q", steps, args, convKey, got, want)
		}
	}

	step(a, `{"name":"肚子","delta":40,"reason":"吃了一大碗"}`, "肚子：偏低 → 偏高")
	checkBuff(t, st, scope, "肚子", 60, day, 40)
	step(b, `{"name":"肚子","delta":5,"reason":"吃了点心"}`, "肚子今天的变化已经到上限（每天最多 ±30），这次没有调整。")
	step(b, `{"name":"肚子","delta":-15,"reason":"跑步"}`, "肚子：偏高 → 中等（超过单次上限 ±10，按 -10 调整）")
	checkBuff(t, st, scope, "肚子", 50, day, 30)
	step(a, `{"name":"肚子","delta":40,"reason":"又吃了一大碗"}`, "肚子：中等 → 很高")
	checkBuff(t, st, scope, "肚子", 90, day, 70)

	for key, want := range map[string]int{scope: 1, a: 0, b: 0} {
		if buffs, err := st.GetBuffs(context.Background(), key); err != nil || len(buffs) != want {
			t.Errorf("buffs under %s = %+v, %v; want %d", key, buffs, err, want)
		}
	}
}

func TestAdjustBuff_SpecWidened(t *testing.T) {
	spec := NewAdjustBuff(nil, nil).Spec()
	var schema struct {
		Properties struct {
			Delta struct {
				Minimum *int `json:"minimum"`
				Maximum *int `json:"maximum"`
			} `json:"delta"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if d := schema.Properties.Delta; d.Minimum == nil || *d.Minimum != -conloomapi.MaxBarLimit || d.Maximum == nil ||
		*d.Maximum != conloomapi.MaxBarLimit {
		t.Errorf("delta schema in %s, want -100 to 100", spec.Parameters)
	}
	for _, s := range []string{"默认 ±10", "外在"} {
		if !strings.Contains(spec.Description, s) {
			t.Errorf("description %q lacks %q", spec.Description, s)
		}
	}
	for _, s := range []string{"隐藏", "只有你自己知道"} {
		if strings.Contains(spec.Description, s) {
			t.Errorf("description %q has %q", spec.Description, s)
		}
	}
}
