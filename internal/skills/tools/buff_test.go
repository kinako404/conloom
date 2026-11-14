package tools

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func newAdjustBuff(st store.Store, doc conloomapi.PersonaDoc) skills.Tool {
	return NewAdjustBuff(st, fakePersonas{doc: doc})
}

// seedBuff stores value as convKey's bar name at at, outside any task.
func seedBuff(t *testing.T, st store.Store, convKey, name string, value float64, day string, dayDelta int, at time.Time) {
	t.Helper()
	if _, err := st.UpdateBuff(context.Background(), convKey, name,
		store.BuffEvent{Source: store.BuffSourcePanel, Reason: "seed", CreatedAt: at},
		func(store.BuffValue, bool) (store.BuffValue, float64, error) {
			return store.BuffValue{Value: value, Day: day, DayDelta: dayDelta, UpdatedAt: at}, value, nil
		}); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
}

func buffOf(t *testing.T, st store.Store, convKey, name string) (store.BuffValue, bool) {
	t.Helper()
	buffs, err := st.GetBuffs(context.Background(), convKey)
	if err != nil {
		t.Fatalf("GetBuffs: %v", err)
	}
	for _, b := range buffs {
		if b.Name == name {
			return b, true
		}
	}
	return store.BuffValue{}, false
}

// checkBuff checks the stored value, day and daily change of convKey's bar.
func checkBuff(t *testing.T, st store.Store, convKey, name string, value float64, day string, dayDelta int) {
	t.Helper()
	b, ok := buffOf(t, st, convKey, name)
	if !ok || math.Abs(b.Value-value) > 1e-9 || b.Day != day || b.DayDelta != dayDelta {
		t.Errorf("%s = %+v (found %v), want %v on %s with daily change %d", name, b, ok, value, day, dayDelta)
	}
}

func buffEvents(t *testing.T, st store.Store, convKey string) []store.BuffEvent {
	t.Helper()
	evs, err := st.ListBuffEvents(context.Background(), convKey, 100)
	if err != nil {
		t.Fatalf("ListBuffEvents: %v", err)
	}
	return evs
}

func TestAdjustBuff_DecayThenDelta(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	checkSpec(t, tool.Spec(), "adjust_buff", "name", "delta", "reason")
	seedBuff(t, st, privateKey, "心情", 90, "2026-11-13", 0, testNow.Add(-6*time.Hour))
	inv := invocation(privateKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{"name":"心情","delta":5,"reason":"聊得开心"}`), "心情：偏高 → 很高"; got != want {
		t.Errorf("adjust_buff = %q, want %q", got, want)
	}
	checkBuff(t, st, privateKey, "心情", 80, "2026-11-13", 5)
	if b, _ := buffOf(t, st, privateKey, "心情"); !b.UpdatedAt.Equal(testNow) {
		t.Errorf("updated at %v, want %v", b.UpdatedAt, testNow)
	}
	ev := buffEvents(t, st, privateKey)[0]
	if ev.Name != "心情" || ev.Source != store.BuffSourceModel || ev.TaskID != "task_test" || ev.Reason != "聊得开心" ||
		!ev.CreatedAt.Equal(testNow) {
		t.Errorf("event = %+v", ev)
	}
	if math.Abs(ev.Before-75) > 1e-9 || math.Abs(ev.After-80) > 1e-9 {
		t.Errorf("event records %v → %v, want the decayed 75 → 80", ev.Before, ev.After)
	}

	inv.TaskID = "task_next"
	if got, want := call(t, tool, inv, `{"name":"精力","delta":-3,"reason":"有点累"}`), "精力：偏高 → 偏高"; got != want {
		t.Errorf("adjust_buff of an unstored bar = %q, want %q", got, want)
	}
	checkBuff(t, st, privateKey, "精力", 67, "2026-11-13", -3)
	if ev := buffEvents(t, st, privateKey)[0]; ev.Name != "精力" || ev.Before != 70 || ev.After != 67 || ev.TaskID != "task_next" {
		t.Errorf("event of an unstored bar = %+v, want 70 → 67 from the baseline", ev)
	}
}

func TestAdjustBuff_Caps(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	steps := 0
	step := func(at time.Time, args, want string) {
		t.Helper()
		steps++
		inv.TaskID, inv.Now = "task_"+string(rune('a'+steps)), at
		if got := call(t, tool, inv, args); got != want {
			t.Errorf("step %d adjust_buff(%s) = %q, want %q", steps, args, got, want)
		}
	}
	const capped = "心情今天的变化已经到上限，这次没有调整。"

	step(testNow, `{"name":"心情","delta":15,"reason":"好消息"}`, "心情：偏高 → 偏高")
	checkBuff(t, st, privateKey, "心情", 70, "2026-11-13", 10)
	step(testNow, `{"name":"心情","delta":"-15","reason":"坏消息"}`, "心情：偏高 → 偏高")
	checkBuff(t, st, privateKey, "心情", 60, "2026-11-13", 0)

	step(testNow, `{"name":"心情","delta":10,"reason":"好消息"}`, "心情：偏高 → 偏高")
	step(testNow, `{"name":"心情","delta":10,"reason":"好消息"}`, "心情：偏高 → 很高")
	step(testNow, `{"name":"心情","delta":10,"reason":"好消息"}`, "心情：很高 → 很高")
	checkBuff(t, st, privateKey, "心情", 90, "2026-11-13", 30)
	events := len(buffEvents(t, st, privateKey))
	step(testNow, `{"name":"心情","delta":5,"reason":"又一个好消息"}`, capped)
	checkBuff(t, st, privateKey, "心情", 90, "2026-11-13", 30)
	if n := len(buffEvents(t, st, privateKey)); n != events {
		t.Errorf("a capped change recorded an event: %d events, want %d", n, events)
	}
	step(testNow, `{"name":"心情","delta":-4,"reason":"小失望"}`, "心情：很高 → 很高")
	checkBuff(t, st, privateKey, "心情", 86, "2026-11-13", 26)
	step(testNow, `{"name":"心情","delta":10,"reason":"好消息"}`, "心情：很高 → 很高")
	checkBuff(t, st, privateKey, "心情", 90, "2026-11-13", 30)

	// 2026-11-13 23:00 UTC is already 2026-11-14 in Asia/Shanghai.
	nextDay := time.Date(2026, 11, 13, 23, 0, 0, 0, time.UTC)
	step(nextDay, `{"name":"心情","delta":5,"reason":"早上好"}`, "心情：偏高 → 偏高")
	want := autonomy.Decay(90, 60, 6*time.Hour, nextDay.Sub(testNow)) + 5
	checkBuff(t, st, privateKey, "心情", want, "2026-11-14", 5)

	seedBuff(t, st, privateKey, "精力", 97, "2026-11-13", 0, testNow)
	step(testNow, `{"name":"精力","delta":10,"reason":"睡饱了"}`, "精力：很高 → 很高")
	checkBuff(t, st, privateKey, "精力", 100, "2026-11-13", 10)
	seedBuff(t, st, privateKey, "精力", 4, "2026-11-13", 0, testNow)
	step(testNow, `{"name":"精力","delta":-10,"reason":"熬夜"}`, "精力：很低 → 很低")
	checkBuff(t, st, privateKey, "精力", 0, "2026-11-13", -10)
	step(testNow, `{"name":"精力","delta":-10,"reason":"熬夜"}`, "精力：很低 → 很低")
	step(testNow, `{"name":"精力","delta":-10,"reason":"熬夜"}`, "精力：很低 → 很低")
	step(testNow, `{"name":"精力","delta":-1,"reason":"熬夜"}`, "精力今天的变化已经到上限，这次没有调整。")
	checkBuff(t, st, privateKey, "精力", 0, "2026-11-13", -30)
}

func TestAdjustBuff_BadArguments(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	const badDelta = "delta 需要是不为 0 的整数，单次最多 ±10。"
	const badReason = "需要简单写一下原因（reason），1 到 40 字。"
	for _, c := range []struct{ args, want string }{
		{`{"name":"心情","delta":0,"reason":"没事"}`, badDelta},
		{`{"name":"心情","delta":2.5,"reason":"没事"}`, badDelta},
		{`{"name":"心情","delta":"很多","reason":"没事"}`, badDelta},
		{`{"name":"心情","reason":"没事"}`, badDelta},
		{`{"name":"心情","delta":3}`, badReason},
		{`{"name":"心情","delta":3,"reason":" \n "}`, badReason},
		{`{"name":"心情","delta":3,"reason":"` + strings.Repeat("字", 41) + `"}`, "原因（reason）超过 40 字，写短一点。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("adjust_buff(%s) = %q, want %q", c.args, got, c.want)
		}
	}
	if buffs, _ := st.GetBuffs(context.Background(), privateKey); len(buffs) != 0 {
		t.Errorf("bad arguments stored %+v", buffs)
	}
	if got := call(t, tool, inv, `{"name":"心情","delta":3,"reason":"`+strings.Repeat("字", 40)+`"}`); got != "心情：偏高 → 偏高" {
		t.Errorf("adjust_buff with a 40-rune reason = %q", got)
	}
	if ev := buffEvents(t, st, privateKey)[0]; ev.Reason != strings.Repeat("字", 40) {
		t.Errorf("reason = %q", ev.Reason)
	}
}

func TestAdjustBuff_OncePerTask(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	if got := call(t, tool, inv, `{"name":"心情","delta":3,"reason":"开心"}`); got != "心情：偏高 → 偏高" {
		t.Errorf("first adjust_buff = %q", got)
	}
	if got := call(t, tool, inv, `{"name":"心情","delta":-2,"reason":"又不开心了"}`); got != "这一条这次已经调过了。" {
		t.Errorf("second adjust_buff of the bar in the task = %q", got)
	}
	checkBuff(t, st, privateKey, "心情", 63, "2026-11-13", 3)
	if got := call(t, tool, inv, `{"name":"精力","delta":2,"reason":"喝了咖啡"}`); got != "精力：偏高 → 偏高" {
		t.Errorf("adjust_buff of another bar in the task = %q", got)
	}
	inv.TaskID = "task_next"
	if got := call(t, tool, inv, `{"name":"心情","delta":-2,"reason":"又不开心了"}`); got != "心情：偏高 → 偏高" {
		t.Errorf("adjust_buff in the next task = %q", got)
	}
	checkBuff(t, st, privateKey, "心情", 61, "2026-11-13", 1)
}

func TestAdjustBuff_UnknownBarListsCurrent(t *testing.T) {
	st := newStore(t)
	seedBuff(t, st, privateKey, "饥饿", 50, "2026-11-13", 0, testNow)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{"name":"饥饿","delta":3,"reason":"饿了"}`), "没有这一条。现有：心情、精力。"; got != want {
		t.Errorf("adjust_buff of a removed bar = %q, want %q", got, want)
	}
	checkBuff(t, st, privateKey, "饥饿", 50, "2026-11-13", 0)
	if n := len(buffEvents(t, st, privateKey)); n != 1 {
		t.Errorf("a refused change recorded an event: %d events", n)
	}

	custom := conloomapi.DefaultPersona()
	custom.Bars = []conloomapi.PersonaBar{
		{Name: "好奇", Baseline: 50, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}},
		{Name: "耐心", Baseline: 40, HalfLife: conloomapi.Duration{Duration: 3 * time.Hour}},
	}
	tool = newAdjustBuff(st, custom)
	for _, c := range []struct{ args, want string }{
		{`{"name":"心情","delta":3,"reason":"开心"}`, "没有这一条。现有：好奇、耐心。"},
		{`{"name":"饥饿","delta":3,"reason":"饿了"}`, "没有这一条。现有：好奇、耐心。"},
		{`{"delta":3,"reason":"开心"}`, "需要提供条名（name）。现有：好奇、耐心。"},
		{`{"name":"好奇","delta":3,"reason":"新鲜事"}`, "好奇：中等 → 中等"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("adjust_buff(%s) = %q, want %q", c.args, got, c.want)
		}
	}
	checkBuff(t, st, privateKey, "好奇", 53, "2026-11-13", 3)
	if _, ok := buffOf(t, st, privateKey, "心情"); ok {
		t.Error("a bar the persona does not have was stored")
	}
}

func TestAdjustBuff_GroupRefused(t *testing.T) {
	st := newStore(t)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	if got := call(t, tool, invocation(groupKey, "20001", "小明"), `{"name":"心情","delta":3,"reason":"开心"}`); got != "只能在私聊里用。" {
		t.Errorf("adjust_buff in a group = %q", got)
	}
	if buffs, _ := st.GetBuffs(context.Background(), groupKey); len(buffs) != 0 {
		t.Errorf("adjust_buff in a group stored %+v", buffs)
	}
}

func TestAdjustBuff_NoNumbersInResult(t *testing.T) {
	st := newStore(t)
	seedBuff(t, st, privateKey, "心情", 38.5, "2026-11-13", 28, testNow)
	tool := newAdjustBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	var results []string
	for i, args := range []string{
		`{"name":"心情","delta":2,"reason":"开心"}`,
		`{"name":"心情","delta":2,"reason":"开心"}`,
		`{"name":"精力","delta":-7,"reason":"累"}`,
		`{"name":"心情","delta":9,"reason":"开心"}`,
		`{"name":"饥饿","delta":1,"reason":"饿"}`,
	} {
		if i > 1 {
			inv.TaskID = "task_" + string(rune('a'+i))
		}
		results = append(results, call(t, tool, inv, args))
	}
	results = append(results, call(t, tool, invocation(groupKey, "20001", "小明"), `{"name":"心情","delta":2,"reason":"开心"}`))
	for _, r := range results {
		if strings.ContainsAny(r, "0123456789０１２３４５６７８９") {
			t.Errorf("result %q shows a number", r)
		}
	}
	if want := []string{"心情：偏低 → 中等", "这一条这次已经调过了。", "精力：偏高 → 偏高",
		"心情今天的变化已经到上限，这次没有调整。", "没有这一条。现有：心情、精力。", "只能在私聊里用。"}; strings.Join(results, "|") != strings.Join(want, "|") {
		t.Errorf("results = %q, want %q", results, want)
	}
}
