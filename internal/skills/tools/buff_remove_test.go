package tools

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func newRemoveBuff(st store.Store, doc conloomapi.PersonaDoc) skills.Tool {
	return NewRemoveBuff(st, fakePersonas{doc: doc})
}

func TestRemoveBuff_PersonaBarRefused(t *testing.T) {
	st := newStore(t)
	tool := newRemoveBuff(st, conloomapi.DefaultPersona())
	checkSpec(t, tool.Spec(), "remove_buff", "name")
	seedBuff(t, st, privateKey, "心情", 80, "2026-11-13", 5, testNow)
	inv := invocation(privateKey, "20001", "小明")
	for _, name := range []string{"心情", " 精力 "} {
		if got, want := call(t, tool, inv, `{"name":"`+name+`"}`), "这一条是固定的，不能删。"; got != want {
			t.Errorf("remove_buff(%q) = %q, want %q", name, got, want)
		}
	}
	checkBuff(t, st, privateKey, "心情", 80, "2026-11-13", 5)
	if n := len(buffEvents(t, st, privateKey)); n != 1 {
		t.Errorf("changes = %d, want only the seed", n)
	}

	// 好奇 was added by the bot before the persona gained a bar of that name:
	// the persona's bar wins, so the bot cannot remove it.
	if err := st.AddCustomBar(context.Background(), store.CustomBar{ConversationKey: privateKey, Name: "好奇", TaskID: "task_old",
		Baseline: 10, HalfLife: time.Hour, CreatedAt: testNow.Add(-time.Hour)}, 10, 6); err != nil {
		t.Fatal(err)
	}
	custom := conloomapi.DefaultPersona()
	custom.Bars = []conloomapi.PersonaBar{{Name: "好奇", Baseline: 50, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}}}
	tool = newRemoveBuff(st, custom)
	if got, want := call(t, tool, inv, `{"name":"好奇"}`), "这一条是固定的，不能删。"; got != want {
		t.Errorf("remove_buff of the persona's bar = %q, want %q", got, want)
	}
	if got := barNames(customBarsOf(t, st, privateKey)); !reflect.DeepEqual(got, []string{"好奇"}) {
		t.Errorf("custom bars = %v, want the shadowed 好奇 kept", got)
	}
	if _, ok := buffOf(t, st, privateKey, "好奇"); !ok {
		t.Error("the persona bar's value was removed")
	}
	if got, want := call(t, tool, inv, `{"name":"心情"}`), "没有这一条。现在没有自建的状态条或效果。"; got != want {
		t.Errorf("remove_buff of a bar this persona lacks = %q, want %q", got, want)
	}
	checkBuff(t, st, privateKey, "心情", 80, "2026-11-13", 5)
}

func TestRemoveBuff_CustomBarAndEffect(t *testing.T) {
	st := newStore(t)
	add := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	tool := newRemoveBuff(st, conloomapi.DefaultPersona())
	inv := invocation(privateKey, "20001", "小明")
	call(t, add, inv, `{"kind":"bar","name":"饥饿","baseline":30,"value":60}`)
	call(t, add, inv, `{"kind":"bar","name":"困意","baseline":30}`)
	call(t, add, inv, `{"kind":"effect","name":"着凉","minutes":60}`)
	call(t, add, inv, `{"kind":"effect","name":"头晕","minutes":5}`)

	inv.TaskID = "task_rm"
	if got, want := call(t, tool, inv, `{"name":"饥饿"}`), "已删掉状态条：饥饿。"; got != want {
		t.Errorf("remove_buff of a bar = %q, want %q", got, want)
	}
	if got := barNames(customBarsOf(t, st, privateKey)); !reflect.DeepEqual(got, []string{"困意"}) {
		t.Errorf("custom bars = %v, want [困意]", got)
	}
	if _, ok := buffOf(t, st, privateKey, "饥饿"); ok {
		t.Error("the removed bar's value is still stored")
	}
	ev := buffEvents(t, st, privateKey)[0]
	if ev.Kind != store.BuffKindBarRemove || ev.Name != "饥饿" || ev.Source != store.BuffSourceModel || ev.TaskID != "task_rm" ||
		ev.Before != 60 || !ev.CreatedAt.Equal(testNow) {
		t.Errorf("bar_remove event = %+v", ev)
	}
	if got, want := call(t, tool, inv, `{"name":"着凉"}`), "已去掉效果：着凉。"; got != want {
		t.Errorf("remove_buff of an effect = %q, want %q", got, want)
	}
	if got := effectNames(effectsOf(t, st, privateKey, testNow)); !reflect.DeepEqual(got, []string{"头晕"}) {
		t.Errorf("effects = %v, want [头晕]", got)
	}
	ev = buffEvents(t, st, privateKey)[0]
	if ev.Kind != store.BuffKindEffectRemove || ev.Name != "着凉" || ev.Source != store.BuffSourceModel || ev.TaskID != "task_rm" {
		t.Errorf("effect_remove event = %+v", ev)
	}
	if got, want := call(t, tool, inv, `{"name":"饿了"}`), "没有这一条。自建状态条：困意；效果：头晕。"; got != want {
		t.Errorf("remove_buff of an unknown name = %q, want %q", got, want)
	}

	// 头晕 has run out: it counts as gone. 口渴 ran out as an effect and was
	// then added as a bar, which is what remove_buff removes.
	inv.Now = testNow.Add(5 * time.Minute)
	inv.TaskID = "task_later"
	if got, want := call(t, tool, inv, `{"name":"头晕"}`), "没有这一条。自建状态条：困意。"; got != want {
		t.Errorf("remove_buff of a run-out effect = %q, want %q", got, want)
	}
	inv.TaskID = "task_thirst"
	call(t, add, inv, `{"kind":"effect","name":"口渴","minutes":5}`)
	inv.Now, inv.TaskID = testNow.Add(10*time.Minute), "task_thirst_bar"
	if got := call(t, add, inv, `{"kind":"bar","name":"口渴","baseline":40}`); got != "已加上状态条：口渴，现在中等。" {
		t.Fatalf("add_buff of a bar named like a run-out effect = %q", got)
	}
	if got, want := call(t, tool, inv, `{"name":"口渴"}`), "已删掉状态条：口渴。"; got != want {
		t.Errorf("remove_buff of a bar named like a run-out effect = %q, want %q", got, want)
	}
	for _, e := range buffEvents(t, st, privateKey) {
		if e.Kind == store.BuffKindEffectRemove && e.Name != "着凉" {
			t.Errorf("unexpected effect removal %+v", e)
		}
	}

	// A re-run task removes a bar once, even if another task added it back.
	inv.TaskID = "task_sleepy"
	call(t, tool, inv, `{"name":"困意"}`)
	inv.TaskID = "task_back"
	call(t, add, inv, `{"kind":"bar","name":"困意","baseline":30}`)
	inv.TaskID = "task_sleepy"
	if got, want := call(t, tool, inv, `{"name":"困意"}`), "这一条这次已经删过了。"; got != want {
		t.Errorf("second remove_buff in the task = %q, want %q", got, want)
	}
	if got := barNames(customBarsOf(t, st, privateKey)); !reflect.DeepEqual(got, []string{"困意"}) {
		t.Errorf("custom bars = %v, want 困意 added back", got)
	}

	for _, c := range []struct {
		inv        skills.Invocation
		args, want string
	}{
		{inv, `{}`, "需要提供名字（name）。"},
		{inv, `{"name":"  "}`, "需要提供名字（name）。"},
		{invocation(groupKey, "20001", "小明"), `{"name":"困意"}`, privateOnly},
	} {
		if got := call(t, tool, c.inv, c.args); got != c.want {
			t.Errorf("remove_buff(%s) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestBuffTools_PanelChatsShareTheScope(t *testing.T) {
	st := newStore(t)
	a := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	scope := memory.ScopeKeyOf(a)
	add := newAddBuff(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	adjust := newAdjustBuff(st, conloomapi.DefaultPersona())
	remove := newRemoveBuff(st, conloomapi.DefaultPersona())
	inA, inB := invocation(a, "20001", "小明"), invocation(b, "20001", "小明")
	inB.TaskID = "task_b"

	call(t, add, inA, `{"kind":"bar","name":"饥饿","baseline":30}`)
	call(t, add, inA, `{"kind":"effect","name":"着凉","minutes":60}`)
	if got := call(t, add, inB, `{"kind":"effect","name":"饥饿","minutes":60}`); got != "已经有叫“饥饿”的状态条或效果了，换个名字。" {
		t.Errorf("add_buff in B of A's bar = %q", got)
	}
	if got, want := call(t, adjust, inB, `{"name":"饥饿","delta":5,"reason":"饿了"}`), "饥饿：偏低 → 偏低"; got != want {
		t.Errorf("adjust_buff in B of A's bar = %q, want %q", got, want)
	}
	checkBuff(t, st, scope, "饥饿", 35, testNow.In(shanghai).Format(time.DateOnly), 5)
	if got, want := call(t, remove, inB, `{"name":"着凉"}`), "已去掉效果：着凉。"; got != want {
		t.Errorf("remove_buff in B of A's effect = %q, want %q", got, want)
	}
	if got := barNames(customBarsOf(t, st, scope)); !reflect.DeepEqual(got, []string{"饥饿"}) {
		t.Errorf("scope bars = %v", got)
	}
	for _, key := range []string{a, b} {
		if bars, effects, events := customBarsOf(t, st, key), effectsOf(t, st, key, time.Time{}), buffEvents(t, st, key); len(bars)+
			len(effects)+len(events) != 0 {
			t.Errorf("stored under the chat %s: %+v %+v %+v", key, bars, effects, events)
		}
	}
}
