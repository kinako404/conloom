package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func newAddBuff(st store.Store, policies *policy.Service, doc conloomapi.PersonaDoc) skills.Tool {
	return NewAddBuff(st, policies, fakePersonas{doc: doc}, review.New(nil, review.Options{}))
}

func customBarsOf(t *testing.T, st store.Store, convKey string) []store.CustomBar {
	t.Helper()
	bars, err := st.ListCustomBars(context.Background(), convKey)
	if err != nil {
		t.Fatalf("ListCustomBars: %v", err)
	}
	return bars
}

func effectsOf(t *testing.T, st store.Store, convKey string, now time.Time) []store.Effect {
	t.Helper()
	effects, err := st.ListEffects(context.Background(), convKey, now)
	if err != nil {
		t.Fatalf("ListEffects: %v", err)
	}
	return effects
}

func names[T any](items []T, name func(T) string) []string {
	var out []string
	for _, it := range items {
		out = append(out, name(it))
	}
	return out
}

func barNames(bars []store.CustomBar) []string {
	return names(bars, func(b store.CustomBar) string { return b.Name })
}

func effectNames(effects []store.Effect) []string {
	return names(effects, func(e store.Effect) string { return e.Name })
}

// eventKinds lists convKey's bar changes, oldest first, as "kind name task".
func eventKinds(t *testing.T, st store.Store, convKey string) []string {
	t.Helper()
	evs := buffEvents(t, st, convKey)
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Kind+" "+evs[i].Name+" "+evs[i].TaskID)
	}
	return out
}

// checkNothingStored checks that convKey has no bot-created bars, effects,
// bar values or bar changes.
func checkNothingStored(t *testing.T, st store.Store, convKey string) {
	t.Helper()
	buffs, err := st.GetBuffs(context.Background(), convKey)
	if err != nil {
		t.Fatal(err)
	}
	if bars, effects, events := customBarsOf(t, st, convKey), effectsOf(t, st, convKey, time.Time{}),
		buffEvents(t, st, convKey); len(bars)+len(effects)+len(events)+len(buffs) != 0 {
		t.Errorf("stored bars %+v, effects %+v, values %+v, changes %+v; want nothing", bars, effects, buffs, events)
	}
}

func TestAddBuff_BarAndEffect(t *testing.T) {
	st := newStore(t)
	tool := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	checkSpec(t, tool.Spec(), "add_buff", "kind", "name")
	inv := invocation(privateKey, "20001", "小明")

	if got, want := call(t, tool, inv,
		`{"kind":"bar","name":"饥饿","description":"【高=很饿】\n低=很饱","baseline":30,"half_life_hours":3,"value":65}`),
		"已加上状态条：饥饿，现在偏高。"; got != want {
		t.Errorf("add_buff bar = %q, want %q", got, want)
	}
	inv.TaskID = "task_b"
	if got, want := call(t, tool, inv, `{"kind":"bar","name":" 困意 ","baseline":"20"}`), "已加上状态条：困意，现在偏低。"; got != want {
		t.Errorf("add_buff bar with defaults = %q, want %q", got, want)
	}
	inv.TaskID = "task_c"
	if got, want := call(t, tool, inv, `{"kind":"bar","name":"口渴","baseline":90,"half_life_hours":"0.5","value":0}`),
		"已加上状态条：口渴，现在很低。"; got != want {
		t.Errorf("add_buff bar with value 0 = %q, want %q", got, want)
	}
	want := []store.CustomBar{
		{ConversationKey: privateKey, Name: "饥饿", Description: "高=很饿 低=很饱", TaskID: "task_test", Baseline: 30,
			HalfLife: 3 * time.Hour, CreatedAt: testNow},
		{ConversationKey: privateKey, Name: "困意", TaskID: "task_b", Baseline: 20, HalfLife: 6 * time.Hour, CreatedAt: testNow},
		{ConversationKey: privateKey, Name: "口渴", TaskID: "task_c", Baseline: 90, HalfLife: 30 * time.Minute, CreatedAt: testNow},
	}
	if got := customBarsOf(t, st, privateKey); !reflect.DeepEqual(got, want) {
		t.Errorf("custom bars = %+v, want %+v", got, want)
	}
	checkBuff(t, st, privateKey, "饥饿", 65, "", 0)
	checkBuff(t, st, privateKey, "困意", 20, "", 0)
	checkBuff(t, st, privateKey, "口渴", 0, "", 0)
	ev := buffEvents(t, st, privateKey)[2]
	if ev.Kind != store.BuffKindBarAdd || ev.Name != "饥饿" || ev.Source != store.BuffSourceModel || ev.TaskID != "task_test" ||
		ev.Before != 65 || ev.After != 65 || !ev.CreatedAt.Equal(testNow) {
		t.Errorf("bar_add event = %+v", ev)
	}

	inv.TaskID = "task_d"
	if got, want := call(t, tool, inv, `{"kind":"effect","name":"着凉","description":"有点冷，\n想喝热水","minutes":80}`),
		"已加上效果：着凉，还剩约 1 小时 20 分。"; got != want {
		t.Errorf("add_buff effect = %q, want %q", got, want)
	}
	inv.TaskID, inv.Now = "task_e", testNow.Add(time.Minute)
	if got, want := call(t, tool, inv, `{"kind":"effect","name":"吃饱了","minutes":"10080"}`), "已加上效果：吃饱了，还剩约 7 天。"; got != want {
		t.Errorf("add_buff effect of 7 days = %q, want %q", got, want)
	}
	wantEffects := []store.Effect{
		{ConversationKey: privateKey, Name: "着凉", Description: "有点冷， 想喝热水", TaskID: "task_d", StartedAt: testNow,
			UntilAt: testNow.Add(80 * time.Minute)},
		{ConversationKey: privateKey, Name: "吃饱了", TaskID: "task_e", StartedAt: testNow.Add(time.Minute),
			UntilAt: testNow.Add(time.Minute + 7*24*time.Hour)},
	}
	if got := effectsOf(t, st, privateKey, testNow.Add(time.Minute)); !reflect.DeepEqual(got, wantEffects) {
		t.Errorf("effects = %+v, want %+v", got, wantEffects)
	}
	ev = buffEvents(t, st, privateKey)[1]
	if ev.Kind != store.BuffKindEffectAdd || ev.Name != "着凉" || ev.Source != store.BuffSourceModel || ev.TaskID != "task_d" ||
		!ev.CreatedAt.Equal(testNow) {
		t.Errorf("effect_add event = %+v", ev)
	}
}

func TestAddBuff_NoNumbersInBarResults(t *testing.T) {
	st := newStore(t)
	tool := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for i, args := range []string{
		`{"kind":"bar","name":"饥饿","baseline":38,"value":19.0}`,
		`{"kind":"bar","name":"困意","baseline":61}`,
		`{"kind":"bar","name":"心情","baseline":50}`,
		`{"kind":"bar","name":"饥饿","baseline":50}`,
	} {
		inv.TaskID = "task_" + string(rune('a'+i))
		if got := call(t, tool, inv, args); strings.ContainsAny(got, "0123456789０１２３４５６７８９") {
			t.Errorf("add_buff(%s) = %q shows a number", args, got)
		}
	}
}

func TestBuffResults_FitTheLimit(t *testing.T) {
	st := newStore(t)
	persona := conloomapi.DefaultPersona()
	sixRunes := func(base rune, i int) string { return strings.Repeat(string(base+rune(i)), conloomapi.MaxBarNameRunes) }
	for i := range conloomapi.MaxPersonaBars {
		persona.Bars = append(persona.Bars, conloomapi.PersonaBar{Name: sixRunes('甲', i), Baseline: 50,
			HalfLife: conloomapi.Duration{Duration: time.Hour}})
	}
	add := newAddBuff(st, newPolicies(st, true, true), persona)
	inv := invocation(privateKey, "20001", "小明")
	var results []string
	for i := range MaxCustomBars + 1 {
		inv.TaskID = "task_bar_" + string(rune('a'+i))
		results = append(results, call(t, add, inv, `{"kind":"bar","name":"`+sixRunes('子', i)+`","baseline":50}`))
	}
	for i := range MaxEffects + 1 {
		inv.TaskID = "task_effect_" + string(rune('a'+i))
		results = append(results, call(t, add, inv, `{"kind":"effect","name":"`+sixRunes('春', i)+`","minutes":10080}`))
	}
	results = append(results,
		call(t, newRemoveBuff(st, persona), inv, `{"name":"没有"}`),
		call(t, newAdjustBuff(st, persona), inv, `{"name":"没有","delta":1,"reason":"测试"}`),
		call(t, newAdjustBuff(st, persona), inv, `{"delta":1,"reason":"测试"}`))
	for i, prefix := range map[int]string{
		MaxCustomBars:                  "自建的状态条已经满了",
		MaxCustomBars + 1 + MaxEffects: "生效中的效果已经满了",
		len(results) - 3:               "没有这一条。自建状态条",
		len(results) - 2:               "没有这一条。现有",
		len(results) - 1:               "需要提供条名",
	} {
		if !strings.HasPrefix(results[i], prefix) {
			t.Errorf("result %q, want it to start with %q", results[i], prefix)
		}
	}
	for _, r := range results {
		if n := utf8.RuneCountInString(r); n > shortResultRunes {
			t.Errorf("result %q has %d runes, over %d", r, n, shortResultRunes)
		}
	}
}

func TestAddBuff_IdempotentPerTask(t *testing.T) {
	st := newStore(t)
	tool := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	for _, args := range []string{
		`{"kind":"bar","name":"饥饿","baseline":30,"value":65}`,
		`{"kind":"bar","name":"饥饿","baseline":30,"value":65}`,
	} {
		if got, want := call(t, tool, inv, args), "已加上状态条：饥饿，现在偏高。"; got != want {
			t.Errorf("add_buff(%s) in the same task = %q, want %q", args, got, want)
		}
	}
	for range 2 {
		if got, want := call(t, tool, inv, `{"kind":"effect","name":"着凉","minutes":30}`), "已加上效果：着凉，还剩约 30 分。"; got != want {
			t.Errorf("add_buff effect in the same task = %q, want %q", got, want)
		}
	}
	if got := barNames(customBarsOf(t, st, privateKey)); !reflect.DeepEqual(got, []string{"饥饿"}) {
		t.Errorf("custom bars = %v, want one", got)
	}
	if got := effectNames(effectsOf(t, st, privateKey, testNow)); !reflect.DeepEqual(got, []string{"着凉"}) {
		t.Errorf("effects = %v, want one", got)
	}
	want := []string{"bar_add 饥饿 task_test", "effect_add 着凉 task_test"}
	if got := eventKinds(t, st, privateKey); !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %q, want %q", got, want)
	}

	inv.TaskID = "task_next"
	for _, c := range []struct{ args, want string }{
		{`{"kind":"bar","name":"饥饿","baseline":30}`, "已经有叫“饥饿”的状态条或效果了，换个名字。"},
		{`{"kind":"effect","name":"饥饿","minutes":30}`, "已经有叫“饥饿”的状态条或效果了，换个名字。"},
		{`{"kind":"effect","name":"着凉","minutes":60}`, "已经有叫“着凉”的状态条或效果了，换个名字。"},
		{`{"kind":"bar","name":"着凉","baseline":30}`, "已经有叫“着凉”的状态条或效果了，换个名字。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("add_buff(%s) in another task = %q, want %q", c.args, got, c.want)
		}
	}
	if got := eventKinds(t, st, privateKey); !reflect.DeepEqual(got, want) {
		t.Errorf("changes after refused adds = %q, want %q", got, want)
	}
	if e := effectsOf(t, st, privateKey, testNow); len(e) != 1 || !e[0].UntilAt.Equal(testNow.Add(30*time.Minute)) {
		t.Errorf("effects = %+v, want 着凉 for 30 minutes", e)
	}
}

func TestAddBuff_Limits(t *testing.T) {
	st := newStore(t)
	tool := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	n := 0
	add := func(args string) string {
		t.Helper()
		n++
		inv.TaskID = "task_" + string(rune('a'+n))
		return call(t, tool, inv, args)
	}
	bars := []string{"饥饿", "困意", "口渴", "无聊", "紧张", "好奇"}
	for _, name := range bars {
		if got := add(`{"kind":"bar","name":"` + name + `","baseline":50}`); !strings.HasPrefix(got, "已加上状态条") {
			t.Fatalf("add_buff of bar %s = %q", name, got)
		}
	}
	if got, want := add(`{"kind":"bar","name":"烦躁","baseline":50}`),
		"自建的状态条已经满了：饥饿、困意、口渴、无聊、紧张、好奇。先用 remove_buff 删掉不需要的。"; got != want {
		t.Errorf("seventh bar = %q, want %q", got, want)
	}
	if got := barNames(customBarsOf(t, st, privateKey)); !reflect.DeepEqual(got, bars) {
		t.Errorf("custom bars = %v, want %v", got, bars)
	}

	effects := []string{"着凉", "吃饱了", "头晕", "犯困", "开心", "紧张感"}
	for i, name := range effects {
		minutes := "5"
		if i > 0 {
			minutes = "60"
		}
		if got := add(`{"kind":"effect","name":"` + name + `","minutes":` + minutes + `}`); !strings.HasPrefix(got, "已加上效果") {
			t.Fatalf("add_buff of effect %s = %q", name, got)
		}
	}
	if got, want := add(`{"kind":"effect","name":"脚麻","minutes":30}`),
		"生效中的效果已经满了：着凉、吃饱了、头晕、犯困、开心、紧张感。先用 remove_buff 去掉不需要的。"; got != want {
		t.Errorf("seventh effect = %q, want %q", got, want)
	}
	if got := len(buffEvents(t, st, privateKey)); got != 12 {
		t.Errorf("changes = %d, want 12 without the refused adds", got)
	}

	inv.Now = testNow.Add(5 * time.Minute)
	if got, want := add(`{"kind":"effect","name":"脚麻","minutes":30}`), "已加上效果：脚麻，还剩约 30 分。"; got != want {
		t.Errorf("effect once one ran out = %q, want %q", got, want)
	}
	if got, want := effectNames(effectsOf(t, st, privateKey, inv.Now)), append(effects[1:], "脚麻"); !reflect.DeepEqual(got, want) {
		t.Errorf("effects = %v, want %v", got, want)
	}
}

func TestAddBuff_Reviewed(t *testing.T) {
	st := newStore(t)
	policies := newPolicies(st, true, true)
	tool := newAddBuff(st, policies, conloomapi.DefaultPersona())
	adult := conloomapi.DefaultPersona()
	adult.Adult = true
	open := newAddBuff(st, policies, adult)
	word := protectedWord(t)
	fresh := invocation(privateKey, "20001", "小明")

	for _, args := range []string{
		`{"kind":"bar","name":"` + word + `","baseline":50}`,
		`{"kind":"effect","name":"` + word + `","minutes":30}`,
	} {
		if got := call(t, tool, fresh, args); got != "这个名字不能用。" {
			t.Errorf("protected add_buff(%s) = %q, want the name refused", args, got)
		}
	}
	for _, args := range []string{
		`{"kind":"bar","name":"饥饿","description":"想` + word + `","baseline":50}`,
		`{"kind":"effect","name":"着凉","description":"想` + word + `","minutes":30}`,
	} {
		if got := call(t, tool, fresh, args); got != "这个说明不能用。" {
			t.Errorf("protected add_buff(%s) = %q, want the description refused", args, got)
		}
	}
	checkNothingStored(t, st, privateKey)

	if got, want := call(t, open, fresh, `{"kind":"effect","name":"`+word+`","description":"想`+word+`","minutes":30}`),
		"已加上效果："+word+"，还剩约 30 分。"; got != want {
		t.Errorf("open add_buff = %q, want %q (the protected lexicon does not apply)", got, want)
	}

	pv, err := policies.Current(context.Background(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	inv := fresh
	inv.TaskID = "task_block"
	for _, tl := range []skills.Tool{tool, open} {
		if got := call(t, tl, inv, `{"kind":"bar","name":"坏词","baseline":50}`); got != "这个名字不能用。" {
			t.Errorf("add_buff of a blocked name = %q", got)
		}
		if got := call(t, tl, inv, `{"kind":"bar","name":"饥饿","description":"坏词","baseline":50}`); got != "这个说明不能用。" {
			t.Errorf("add_buff of a blocked description = %q", got)
		}
	}
	if got := barNames(customBarsOf(t, st, privateKey)); len(got) != 0 {
		t.Errorf("refused bars stored: %v", got)
	}

	p.Review.Action = review.VerdictSanitize
	if _, err := policies.Update(context.Background(), privateKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	if got := call(t, tool, inv, `{"kind":"bar","name":"坏词","baseline":50}`); got != "这个名字不能用。" {
		t.Errorf("add_buff of a name review would change = %q, want it refused", got)
	}
	if got, want := call(t, tool, inv, `{"kind":"bar","name":"饥饿","description":"有点坏词","baseline":50}`),
		"已加上状态条：饥饿，现在中等。"; got != want {
		t.Errorf("add_buff of a sanitized description = %q, want %q", got, want)
	}
	if bars := customBarsOf(t, st, privateKey); len(bars) != 1 || bars[0].Description != "有点**" {
		t.Errorf("custom bars = %+v, want 饥饿 with the sanitized description", bars)
	}
}

func TestAddBuff_NameRules(t *testing.T) {
	st := newStore(t)
	tool := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	const badName = "名字（name）需要 1 到 6 个字，不能有空白、冒号、【】或看不见的字符。"
	for _, name := range []string{`""`, `"  "`, `"七个字的名字呀"`, `"饿 了"`, `"饥饿："`, `"饥:饿"`, `"【饥饿】"`,
		`"饥\u200b饿"`, `"饥\u202e饿"`, `"饥\n饿"`, `7`, `null`, `"."`, `".."`, `"......"`} {
		args := `{"kind":"bar","name":` + name + `,"baseline":50}`
		if got := call(t, tool, inv, args); got != badName {
			t.Errorf("add_buff(%s) = %q, want %q", args, got, badName)
		}
	}
	if got := call(t, tool, inv, `{"kind":"effect","minutes":30}`); got != badName {
		t.Errorf("add_buff without a name = %q, want %q", got, badName)
	}
	// A path segment of dots only would never reach the panel's or the API's
	// DELETE.
	if got := call(t, tool, inv, `{"kind":"effect","name":" .. ","minutes":30}`); got != badName {
		t.Errorf("add_buff of an effect named .. = %q, want %q", got, badName)
	}
	for _, c := range []struct{ args, want string }{
		{`{"name":"饥饿","baseline":50}`, "kind 需要是 bar（数值条）或 effect（效果）。"},
		{`{"kind":"buff","name":"饥饿","baseline":50}`, "kind 需要是 bar（数值条）或 effect（效果）。"},
		{`{"kind":"bar","name":"饥饿","description":"` + strings.Repeat("字", 41) + `","baseline":50}`,
			"说明（description）超过 40 字，写短一点。"},
		{`{"kind":"bar","name":"饥饿"}`, "baseline（平常水平）需要是 0 到 100 的整数。"},
		{`{"kind":"bar","name":"饥饿","baseline":-1}`, "baseline（平常水平）需要是 0 到 100 的整数。"},
		{`{"kind":"bar","name":"饥饿","baseline":101}`, "baseline（平常水平）需要是 0 到 100 的整数。"},
		{`{"kind":"bar","name":"饥饿","baseline":2.5}`, "baseline（平常水平）需要是 0 到 100 的整数。"},
		{`{"kind":"bar","name":"饥饿","baseline":50,"half_life_hours":0.4}`, "half_life_hours（回落的半衰期）需要是 0.5 到 168 之间的小时数。"},
		{`{"kind":"bar","name":"饥饿","baseline":50,"half_life_hours":169}`, "half_life_hours（回落的半衰期）需要是 0.5 到 168 之间的小时数。"},
		// In nanoseconds this many hours overflow to about 85 minutes.
		{`{"kind":"bar","name":"饥饿","baseline":50,"half_life_hours":5124097}`, "half_life_hours（回落的半衰期）需要是 0.5 到 168 之间的小时数。"},
		{`{"kind":"bar","name":"饥饿","baseline":50,"half_life_hours":"6小时"}`, "half_life_hours（回落的半衰期）需要是 0.5 到 168 之间的小时数。"},
		{`{"kind":"bar","name":"饥饿","baseline":50,"value":-1}`, "value（现在的值）需要是 0 到 100 的整数。"},
		{`{"kind":"bar","name":"饥饿","baseline":50,"value":101}`, "value（现在的值）需要是 0 到 100 的整数。"},
		{`{"kind":"effect","name":"着凉"}`, "minutes 需要是 5 到 10080 的整数（分钟数，最多 7 天）。"},
		{`{"kind":"effect","name":"着凉","minutes":4}`, "minutes 需要是 5 到 10080 的整数（分钟数，最多 7 天）。"},
		{`{"kind":"effect","name":"着凉","minutes":10081}`, "minutes 需要是 5 到 10080 的整数（分钟数，最多 7 天）。"},
		{`{"kind":"effect","name":"心情","minutes":30}`, "“心情”是固定的状态条，换个名字。"},
		{`{"kind":"bar","name":"精力","baseline":50}`, "“精力”是固定的状态条，换个名字。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("add_buff(%s) = %q, want %q", c.args, got, c.want)
		}
	}
	if got := call(t, tool, invocation(groupKey, "20001", "小明"), `{"kind":"bar","name":"饥饿","baseline":50}`); got != privateOnly {
		t.Errorf("add_buff in a group = %q", got)
	}
	checkNothingStored(t, st, privateKey)
	checkNothingStored(t, st, groupKey)

	custom := conloomapi.DefaultPersona()
	custom.Bars = []conloomapi.PersonaBar{{Name: "好奇", Baseline: 50, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}}}
	other := newAddBuff(st, newPolicies(st, true, true), custom)
	if got, want := call(t, other, inv, `{"kind":"bar","name":"心情","baseline":50}`), "已加上状态条：心情，现在中等。"; got != want {
		t.Errorf("add_buff of a bar this persona lacks = %q, want %q", got, want)
	}
	inv.TaskID = "task_next"
	if got, want := call(t, other, inv, `{"kind":"effect","name":"好奇","minutes":30}`), "“好奇”是固定的状态条，换个名字。"; got != want {
		t.Errorf("add_buff of the persona's bar = %q, want %q", got, want)
	}
	if got, want := call(t, other, inv, `{"kind":"effect","name":"头晕...","minutes":30}`), "已加上效果：头晕...，还剩约 30 分。"; got != want {
		t.Errorf("add_buff of a name with dots and more = %q, want %q", got, want)
	}
}
