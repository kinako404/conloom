package core

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// seedBuffDay stores v as the private chat's bar name, written at at, with
// the model's net change dayDelta on day.
func (e *testEnv) seedBuffDay(t *testing.T, name string, v float64, day string, dayDelta int, at time.Time) {
	t.Helper()
	_, err := e.st.UpdateBuff(context.Background(), privateConvKey, name,
		store.BuffEvent{Source: store.BuffSourceModel, Reason: "测试", CreatedAt: at},
		func(store.BuffValue, bool) (store.BuffValue, float64, error) {
			return store.BuffValue{Value: v, Day: day, DayDelta: dayDelta, UpdatedAt: at}, v, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// seedGroupConv stores the test group conversation.
func (e *testEnv) seedGroupConv(t *testing.T, at time.Time) {
	t.Helper()
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:group", EventID: "evt_seed_group",
			Response: []byte("{}"), CreatedAt: at},
		Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: testConvID, Key: testConvKey,
			Type: conloomapi.ConvGroup, Platform: "qq", PlatformConversationID: "10001"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) buffs(t *testing.T) map[string]store.BuffValue {
	t.Helper()
	rows, err := e.st.GetBuffs(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.BuffValue{}
	for _, b := range rows {
		out[b.Name] = b
	}
	return out
}

func (e *testEnv) newestBuffEvent(t *testing.T) store.BuffEvent {
	t.Helper()
	events, err := e.st.ListBuffEvents(context.Background(), privateConvKey, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListBuffEvents = %+v, %v; want one", events, err)
	}
	return events[0]
}

func (e *testEnv) audits(t *testing.T, kind string) []store.AuditEvent {
	t.Helper()
	events, err := e.st.ListAudit(context.Background(), store.AuditFilter{Kind: kind, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestAutonomyService_BarsOfOnlineTemplate(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "晚安")
	doc := conloomapi.DefaultPersona()
	doc.Bars = []conloomapi.PersonaBar{{Name: "精力", Baseline: 70, HalfLife: conloomapi.Duration{Duration: 4 * time.Hour}}}
	if err := env.o.personas.PutTemplate(ctx, "default", doc, "panel:admin"); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	got, err := env.o.Autonomy().State(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []conloomapi.AutonomyBar{{Name: "精力", Value: 70, Band: "偏高", Baseline: 70}}
	if !reflect.DeepEqual(got.Bars, want) {
		t.Errorf("bars without a persona version = %+v, want the online template's %+v", got.Bars, want)
	}
}

func TestAutonomyService_State(t *testing.T) {
	bars := []conloomapi.PersonaBar{
		{Name: "精力", Baseline: 70, HalfLife: conloomapi.Duration{Duration: 4 * time.Hour}, Description: "高=有精神，低=累、困"},
		{Name: "心情", Baseline: 60, HalfLife: conloomapi.Duration{Duration: 6 * time.Hour}},
	}
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Bars = bars
		c.LegacyPersonaTemplates["default"] = doc
	})
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "晚安")
	now := clock.now()
	// Six hours (心情's half-life) ago 82, decayed to 71 now.
	env.seedBar(t, privateConvKey, "心情", 82, now.Add(-6*time.Hour))
	env.seedBar(t, privateConvKey, "饥饿", 10, now.Add(-time.Hour))
	started, until := now.Add(-10*time.Minute), now.Add(20*time.Minute)
	env.setWakeStatus(t, "洗澡", started, until)
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, now.Add(-time.Hour))
	followUp := env.armWake(t, store.TimerFollowUp, now.Add(2*time.Hour))

	got, err := env.o.Autonomy().State(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == nil || got.Status.Activity != "洗澡" || !got.Status.StartedAt.Equal(started) || !got.Status.UntilAt.Equal(until) {
		t.Errorf("status = %+v, want 洗澡 from %v until %v", got.Status, started, until)
	}
	wantBars := []conloomapi.AutonomyBar{
		{Name: "精力", Value: 70, Band: "偏高", Baseline: 70, Description: "高=有精神，低=累、困"},
		{Name: "心情", Value: 71, Band: "偏高", Baseline: 60},
	}
	if !reflect.DeepEqual(got.Bars, wantBars) {
		t.Errorf("bars = %+v, want the persona's in its order, decayed, without 饥饿: %+v", got.Bars, wantBars)
	}
	wantEvents := []conloomapi.AutonomyBarEvent{
		{Name: "饥饿", Kind: store.BuffKindAdjust, Source: store.BuffSourcePanel, Reason: "测试", Before: 0, After: 10, At: now.Add(-time.Hour)},
		{Name: "心情", Kind: store.BuffKindAdjust, Source: store.BuffSourcePanel, Reason: "测试", Before: 0, After: 82, At: now.Add(-6 * time.Hour)},
	}
	if !sameAutonomyEvents(got.Events, wantEvents) {
		t.Errorf("events = %+v, want %+v", got.Events, wantEvents)
	}
	wantProactive, err := env.o.privateProactiveStatus(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if !sameProactiveStatus(got.Proactive, wantProactive) || got.Proactive.SentToday != 1 || len(got.Proactive.Timers) != 2 ||
		got.Proactive.Timers[0].Kind != string(store.TimerStatusReturn) || !got.Proactive.Timers[1].DueAt.Equal(followUp.DueAt) {
		t.Errorf("proactive = %+v, want %+v with today's message and both timers", got.Proactive, wantProactive)
	}
	if versions, err := env.st.ListPersonaVersions(ctx, privateConvKey); err != nil || len(versions) != 0 {
		t.Errorf("State wrote persona versions %+v, %v", versions, err)
	}

	// An ended status is left out.
	clock.set(until.Add(time.Minute))
	if got, err := env.o.Autonomy().State(ctx, privateConvKey); err != nil || got.Status != nil {
		t.Errorf("State after the status ended = %+v, %v; want no status", got.Status, err)
	}
}

func TestAutonomyService_StateEmptyLists(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "在吗")
	got, err := env.o.Autonomy().State(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != nil || got.Events == nil || len(got.Events) != 0 || got.Effects == nil || len(got.Effects) != 0 ||
		got.ShadowedBars == nil || len(got.ShadowedBars) != 0 || got.Proactive.Timers == nil {
		t.Errorf("state = %+v, want no status and empty, non-null lists", got)
	}
	want := []conloomapi.AutonomyBar{
		{Name: "心情", Value: 60, Band: "偏高", Baseline: 60, Description: "高=开心放松，低=低落烦躁"},
		{Name: "精力", Value: 70, Band: "偏高", Baseline: 70, Description: "高=有精神，低=累、困"},
	}
	if !reflect.DeepEqual(got.Bars, want) {
		t.Errorf("bars = %+v, want the default bars at their baselines %+v", got.Bars, want)
	}
}

func TestAutonomyService_SetBarBypassesCaps(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()
	day := now.In(env.o.loc).Format(time.DateOnly)
	// The model already raised 心情 by the daily limit: 90 six hours ago, 75 now.
	env.seedBuffDay(t, "心情", 90, day, 30, now.Add(-6*time.Hour))

	if err := svc.SetBar(ctx, privateConvKey, "心情", 5, "  管理员调整  ", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	b := env.buffs(t)["心情"]
	if b.Value != 5 || b.Day != day || b.DayDelta != 30 || !b.UpdatedAt.Equal(now) {
		t.Errorf("心情 = %+v, want 5 now, keeping the model's change of 30 today", b)
	}
	ev := env.newestBuffEvent(t)
	if ev.Name != "心情" || ev.Source != store.BuffSourcePanel || ev.TaskID != "" || ev.Reason != "管理员调整" ||
		math.Abs(ev.Before-75) > 1e-9 || ev.After != 5 || !ev.CreatedAt.Equal(now) {
		t.Errorf("event = %+v, want panel 75 → 5 with the trimmed reason", ev)
	}

	// A bar never stored starts from its baseline.
	if err := svc.SetBar(ctx, privateConvKey, "精力", 100, "", store.BuffSourceAPI, "memory-main"); err != nil {
		t.Fatal(err)
	}
	if b := env.buffs(t)["精力"]; b.Value != 100 || b.Day != day || b.DayDelta != 0 {
		t.Errorf("精力 = %+v, want 100 with no model change today", b)
	}
	if ev := env.newestBuffEvent(t); ev.Name != "精力" || ev.Source != store.BuffSourceAPI || ev.Before != 70 || ev.After != 100 || ev.Reason != "" {
		t.Errorf("event = %+v, want api 70 → 100", ev)
	}

	// The next day the model's change of the day before no longer counts.
	clock.set(now.Add(24 * time.Hour))
	if err := svc.SetBar(ctx, privateConvKey, "心情", 0, "重置", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	next := clock.now().In(env.o.loc).Format(time.DateOnly)
	if b := env.buffs(t)["心情"]; b.Value != 0 || b.Day != next || b.DayDelta != 0 {
		t.Errorf("心情 the next day = %+v, want 0 on %s with no model change", b, next)
	}
	if ev := env.newestBuffEvent(t); math.Abs(ev.Before-autonomy.Decay(5, 60, 6*time.Hour, 24*time.Hour)) > 1e-9 {
		t.Errorf("event before = %v, want 5 decayed over a day", ev.Before)
	}

	audits := env.audits(t, "autonomy.buff")
	if len(audits) != 3 {
		t.Fatalf("autonomy.buff audits = %+v, want 3", audits)
	}
	var first store.AuditEvent
	for _, a := range audits {
		if a.Actor == "panel:admin" && strings.Contains(a.Detail, `"before":75`) {
			first = a
		}
	}
	var detail struct {
		Name, Source, Reason string
		Before, After        int
	}
	if err := json.Unmarshal([]byte(first.Detail), &detail); err != nil || first.BotInstanceID != testBot ||
		first.Target != privateConvKey || detail.Name != "心情" || detail.Source != store.BuffSourcePanel ||
		detail.Reason != "管理员调整" || detail.Before != 75 || detail.After != 5 {
		t.Errorf("audit = %+v (detail %+v, %v)", first, detail, err)
	}
	if !strings.Contains(audits[0].Actor+audits[1].Actor+audits[2].Actor, "memory-main") {
		t.Errorf("audits %+v lack the API actor", audits)
	}
}

func TestAutonomyService_RejectsGroupAndUnknownBar(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	env.seedGroupConv(t, clock.now())

	if _, err := svc.State(ctx, testConvKey); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("State of a group err = %v, want ErrNotPrivate", err)
	}
	if err := svc.SetBar(ctx, testConvKey, "心情", 50, "", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("SetBar of a group err = %v, want ErrNotPrivate", err)
	}
	if err := svc.ClearStatus(ctx, testConvKey, "panel:admin"); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("ClearStatus of a group err = %v, want ErrNotPrivate", err)
	}

	missing := store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_29999")
	if _, err := svc.State(ctx, missing); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("State of an unknown chat err = %v, want ErrNotFound", err)
	}
	if err := svc.SetBar(ctx, missing, "心情", 50, "", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetBar of an unknown chat err = %v, want ErrNotFound", err)
	}
	if err := svc.ClearStatus(ctx, missing, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ClearStatus of an unknown chat err = %v, want ErrNotFound", err)
	}

	err := svc.SetBar(ctx, privateConvKey, "饥饿", 50, "", store.BuffSourcePanel, "panel:admin")
	if !errors.Is(err, ErrInvalidBar) || !strings.Contains(err.Error(), "心情") || !strings.Contains(err.Error(), "精力") {
		t.Errorf("SetBar of an unknown bar err = %v, want ErrInvalidBar listing the bars", err)
	}
	for _, v := range []int{-1, 101} {
		if err := svc.SetBar(ctx, privateConvKey, "心情", v, "", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrInvalidBar) {
			t.Errorf("SetBar to %d err = %v, want ErrInvalidBar", v, err)
		}
	}
	if err := svc.SetBar(ctx, privateConvKey, "心情", 50, strings.Repeat("长", 41), store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrInvalidBar) {
		t.Errorf("SetBar with a long reason err = %v, want ErrInvalidBar", err)
	}
	if err := svc.SetBar(ctx, privateConvKey, "心情", 50, "", store.BuffSourceModel, "panel:admin"); err == nil || errors.Is(err, ErrInvalidBar) {
		t.Errorf("SetBar as the model err = %v, want an internal error", err)
	}

	if b := env.buffs(t); len(b) != 0 {
		t.Errorf("refused changes stored bars %+v", b)
	}
	if n := len(env.audits(t, "autonomy.buff")) + len(env.audits(t, "autonomy.status.clear")); n != 0 {
		t.Errorf("refused changes wrote %d audits", n)
	}
}

func TestAutonomyService_ClearStatus(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "去洗澡了")
	now := clock.now()
	until := now.Add(20 * time.Minute)
	env.setWakeStatus(t, "洗澡", now.Add(-10*time.Minute), until)
	initiate := env.armWake(t, store.TimerInitiate, now.Add(3*time.Hour))

	if err := svc.ClearStatus(ctx, privateConvKey, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.GetStatus(ctx, privateConvKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("status after ClearStatus err = %v, want ErrNotFound", err)
	}
	if timers := env.privateTimers(t); len(timers) != 1 || timers[0].Kind != store.TimerInitiate || !timers[0].DueAt.Equal(initiate.DueAt) {
		t.Errorf("timers = %+v, want only the initiate", timers)
	}
	audits := env.audits(t, "autonomy.status.clear")
	if len(audits) != 1 || audits[0].Actor != "panel:admin" || audits[0].Target != privateConvKey || audits[0].BotInstanceID != testBot ||
		!strings.Contains(audits[0].Detail, `"cleared":true`) || !strings.Contains(audits[0].Detail, "洗澡") {
		t.Fatalf("autonomy.status.clear audits = %+v", audits)
	}

	// Clearing again finds nothing; it is still recorded.
	if err := svc.ClearStatus(ctx, privateConvKey, "memory-main"); err != nil {
		t.Fatal(err)
	}
	audits = env.audits(t, "autonomy.status.clear")
	if len(audits) != 2 || !strings.Contains(audits[0].Detail+audits[1].Detail, `"cleared":false`) {
		t.Errorf("audits after clearing nothing = %+v", audits)
	}
}

// sameAutonomyEvents compares bar and effect changes, their times by Equal.
func sameAutonomyEvents(got, want []conloomapi.AutonomyBarEvent) bool {
	return slices.EqualFunc(got, want, func(a, b conloomapi.AutonomyBarEvent) bool {
		at := a.At
		a.At = b.At
		return a == b && at.Equal(b.At)
	})
}

// seedCustomState gives the private chat, with the default persona's bars
// 心情 and 精力, the bot's bars 精力 (since shadowed by the persona's),
// 饥饿 and 困意, an active effect 着凉 and a run-out effect 吃饱了.
func (e *testEnv) seedCustomState(t *testing.T, now time.Time) {
	t.Helper()
	e.seedCustomBar(t, privateConvKey, "精力", "自建的精力", 10, time.Hour, 10, now.Add(-3*time.Hour))
	// 饥饿 was 80 two hours, its half-life, ago and is 55 now.
	e.seedCustomBar(t, privateConvKey, "饥饿", "高=很饿", 30, 2*time.Hour, 80, now.Add(-2*time.Hour))
	e.seedEffect(t, privateConvKey, "吃饱了", "", now.Add(-2*time.Hour), now.Add(-time.Hour))
	e.seedCustomBar(t, privateConvKey, "困意", "", 20, 6*time.Hour, 20, now.Add(-time.Hour))
	e.seedEffect(t, privateConvKey, "着凉", "有点冷", now.Add(-10*time.Minute), now.Add(80*time.Minute))
}

func TestAutonomyService_StateCustomBarsAndEffects(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()
	env.seedCustomState(t, now)

	got, err := env.o.Autonomy().State(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	// 精力's stored value is the bot's bar's 10, decayed with the persona's
	// baseline 70 and half-life of 4 hours.
	energy := autonomy.Decay(10, 70, 4*time.Hour, 3*time.Hour)
	wantBars := []conloomapi.AutonomyBar{
		{Name: "心情", Value: 60, Band: "偏高", Baseline: 60, Description: "高=开心放松，低=低落烦躁"},
		{Name: "精力", Value: roundBar(energy), Band: autonomy.Band(energy), Baseline: 70, Description: "高=有精神，低=累、困"},
		{Name: "饥饿", Value: 55, Band: "中等", Baseline: 30, Description: "高=很饿", Custom: true},
		{Name: "困意", Value: 20, Band: "偏低", Baseline: 20, Custom: true},
	}
	if !reflect.DeepEqual(got.Bars, wantBars) {
		t.Errorf("bars = %+v, want the persona's, then the bot's in the order added, without the shadowed 精力: %+v", got.Bars, wantBars)
	}
	if !slices.Equal(got.ShadowedBars, []string{"精力"}) {
		t.Errorf("shadowed bars = %q, want the bot's 精力", got.ShadowedBars)
	}
	if len(got.Effects) != 1 || got.Effects[0].Name != "着凉" || got.Effects[0].Description != "有点冷" ||
		!got.Effects[0].StartedAt.Equal(now.Add(-10*time.Minute)) || !got.Effects[0].UntilAt.Equal(now.Add(80*time.Minute)) {
		t.Errorf("effects = %+v, want only the active 着凉", got.Effects)
	}
	wantEvents := []conloomapi.AutonomyBarEvent{
		{Name: "着凉", Kind: store.BuffKindEffectAdd, Source: store.BuffSourceModel, At: now.Add(-10 * time.Minute)},
		{Name: "困意", Kind: store.BuffKindBarAdd, Source: store.BuffSourceModel, Before: 20, After: 20, At: now.Add(-time.Hour)},
		{Name: "吃饱了", Kind: store.BuffKindEffectAdd, Source: store.BuffSourceModel, At: now.Add(-2 * time.Hour)},
		{Name: "饥饿", Kind: store.BuffKindBarAdd, Source: store.BuffSourceModel, Before: 80, After: 80, At: now.Add(-2 * time.Hour)},
		{Name: "精力", Kind: store.BuffKindBarAdd, Source: store.BuffSourceModel, Before: 10, After: 10, At: now.Add(-3 * time.Hour)},
	}
	if !sameAutonomyEvents(got.Events, wantEvents) {
		t.Errorf("events = %+v, want %+v", got.Events, wantEvents)
	}

	// The effect has run out: the list is empty, not null.
	clock.set(now.Add(2 * time.Hour))
	if got, err := env.o.Autonomy().State(context.Background(), privateConvKey); err != nil || got.Effects == nil || len(got.Effects) != 0 {
		t.Errorf("effects after 着凉 ran out = %#v, %v; want an empty list", got.Effects, err)
	}
}

func TestAutonomyService_SetBarOfTheBot(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	now := clock.now()
	env.seedCustomState(t, now)

	if err := svc.SetBar(ctx, privateConvKey, "饥饿", 90, "饿了", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if b := env.buffs(t)["饥饿"]; b.Value != 90 || !b.UpdatedAt.Equal(now) {
		t.Errorf("饥饿 = %+v, want 90 now", b)
	}
	if ev := env.newestBuffEvent(t); ev.Name != "饥饿" || ev.Kind != store.BuffKindAdjust || ev.Source != store.BuffSourcePanel ||
		math.Abs(ev.Before-55) > 1e-9 || ev.After != 90 {
		t.Errorf("event = %+v, want panel 55 (decayed with the bar's own baseline and half-life) → 90", ev)
	}

	// The persona's 精力 wins the name: its baseline and half-life count.
	if err := svc.SetBar(ctx, privateConvKey, "精力", 50, "", store.BuffSourceAPI, "memory-main"); err != nil {
		t.Fatal(err)
	}
	if ev := env.newestBuffEvent(t); ev.Name != "精力" || math.Abs(ev.Before-autonomy.Decay(10, 70, 4*time.Hour, 3*time.Hour)) > 1e-9 {
		t.Errorf("event = %+v, want 精力 decayed toward the persona's baseline", ev)
	}

	err := svc.SetBar(ctx, privateConvKey, "口渴", 50, "", store.BuffSourcePanel, "panel:admin")
	if !errors.Is(err, ErrInvalidBar) || !strings.Contains(err.Error(), "心情, 精力, 饥饿, 困意") {
		t.Errorf("SetBar of an unknown bar err = %v, want ErrInvalidBar listing every bar", err)
	}
	if n := len(env.audits(t, "autonomy.buff")); n != 2 {
		t.Errorf("autonomy.buff audits = %d, want 2", n)
	}
}

func TestAutonomyService_RemoveBar(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	env.seedGroupConv(t, clock.now())
	now := clock.now()
	env.seedCustomState(t, now)
	events := func() int {
		t.Helper()
		evs, err := env.st.ListBuffEvents(ctx, privateConvKey, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(evs)
	}
	before := events()

	// The persona's bars cannot be removed; nor can unknown bars, effects,
	// bars of groups or of unknown chats.
	if err := svc.RemoveBar(ctx, privateConvKey, "心情", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrInvalidBar) ||
		!strings.Contains(err.Error(), "心情") {
		t.Errorf("RemoveBar of the persona's bar err = %v, want ErrInvalidBar", err)
	}
	for _, name := range []string{"口渴", "着凉", ""} {
		if err := svc.RemoveBar(ctx, privateConvKey, name, store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RemoveBar of %q err = %v, want ErrNotFound", name, err)
		}
	}
	if err := svc.RemoveBar(ctx, testConvKey, "饥饿", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("RemoveBar in a group err = %v, want ErrNotPrivate", err)
	}
	missing := store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_29999")
	if err := svc.RemoveBar(ctx, missing, "饥饿", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RemoveBar in an unknown chat err = %v, want ErrNotFound", err)
	}
	if err := svc.RemoveBar(ctx, privateConvKey, "饥饿", store.BuffSourceModel, "panel:admin"); err == nil || errors.Is(err, ErrInvalidBar) {
		t.Errorf("RemoveBar as the model err = %v, want an internal error", err)
	}
	if n := events(); n != before || len(env.audits(t, "autonomy.bar.remove")) != 0 {
		t.Fatalf("refused removals wrote %d events and audits %+v", n-before, env.audits(t, "autonomy.bar.remove"))
	}

	if err := svc.RemoveBar(ctx, privateConvKey, "饥饿", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.buffs(t)["饥饿"]; ok {
		t.Errorf("饥饿's value is still stored")
	}
	if ev := env.newestBuffEvent(t); ev.Name != "饥饿" || ev.Kind != store.BuffKindBarRemove || ev.Source != store.BuffSourcePanel ||
		ev.TaskID != "" || ev.Before != 80 || !ev.CreatedAt.Equal(now) {
		t.Errorf("event = %+v, want the panel's removal of 饥饿 now", ev)
	}
	if err := svc.RemoveBar(ctx, privateConvKey, "饥饿", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second RemoveBar err = %v, want ErrNotFound", err)
	}

	// The bot's 精力, shadowed by the persona's: its definition goes, the
	// persona's 精力 keeps its value.
	stored := env.buffs(t)["精力"]
	if err := svc.RemoveBar(ctx, privateConvKey, "精力", store.BuffSourceAPI, "memory-main"); err != nil {
		t.Fatal(err)
	}
	if b := env.buffs(t)["精力"]; b != stored {
		t.Errorf("精力 = %+v after removing the bot's 精力, want %+v kept", b, stored)
	}
	custom, err := env.st.ListCustomBars(ctx, privateConvKey)
	if err != nil || len(custom) != 1 || custom[0].Name != "困意" {
		t.Errorf("bot's bars = %+v, %v; want only 困意", custom, err)
	}
	if err := svc.RemoveBar(ctx, privateConvKey, "精力", store.BuffSourceAPI, "memory-main"); !errors.Is(err, ErrInvalidBar) {
		t.Errorf("RemoveBar of the persona's 精力 err = %v, want ErrInvalidBar", err)
	}
	if st, err := svc.State(ctx, privateConvKey); err != nil || st.ShadowedBars == nil || len(st.ShadowedBars) != 0 {
		t.Errorf("shadowed bars after the removal = %#v, %v; want an empty list", st.ShadowedBars, err)
	}

	audits := env.audits(t, "autonomy.bar.remove")
	if len(audits) != 2 {
		t.Fatalf("autonomy.bar.remove audits = %+v, want 2", audits)
	}
	for _, a := range audits {
		want := map[string]string{"panel:admin": `{"name":"饥饿"}`, "memory-main": `{"name":"精力"}`}[a.Actor]
		if a.Detail != want || a.Target != privateConvKey || a.BotInstanceID != testBot {
			t.Errorf("audit = %+v, want detail %s", a, want)
		}
	}
}

func TestAutonomyService_RemoveEffect(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	svc := env.o.Autonomy()
	env.seedPrivateMessage(t, clock, "在吗")
	env.seedGroupConv(t, clock.now())
	now := clock.now()
	env.seedCustomState(t, now)

	// A run-out effect is left to the hourly purge; bars are no effects.
	for _, name := range []string{"吃饱了", "饥饿", "心情", "口渴"} {
		if err := svc.RemoveEffect(ctx, privateConvKey, name, store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RemoveEffect of %q err = %v, want ErrNotFound", name, err)
		}
	}
	if err := svc.RemoveEffect(ctx, testConvKey, "着凉", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("RemoveEffect in a group err = %v, want ErrNotPrivate", err)
	}
	if err := svc.RemoveEffect(ctx, privateConvKey, "着凉", store.BuffSourceModel, "panel:admin"); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Errorf("RemoveEffect as the model err = %v, want an internal error", err)
	}
	if len(env.audits(t, "autonomy.effect.remove")) != 0 || env.newestBuffEvent(t).Name != "着凉" {
		t.Fatal("refused removals wrote audits or events")
	}

	if err := svc.RemoveEffect(ctx, privateConvKey, "着凉", store.BuffSourcePanel, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	if active, err := env.st.ListEffects(ctx, privateConvKey, now); err != nil || len(active) != 0 {
		t.Errorf("active effects = %+v, %v; want none", active, err)
	}
	if ev := env.newestBuffEvent(t); ev.Name != "着凉" || ev.Kind != store.BuffKindEffectRemove || ev.Source != store.BuffSourcePanel ||
		ev.TaskID != "" || !ev.CreatedAt.Equal(now) {
		t.Errorf("event = %+v, want the panel's removal of 着凉 now", ev)
	}
	if err := svc.RemoveEffect(ctx, privateConvKey, "着凉", store.BuffSourcePanel, "panel:admin"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second RemoveEffect err = %v, want ErrNotFound", err)
	}
	audits := env.audits(t, "autonomy.effect.remove")
	if len(audits) != 1 || audits[0].Detail != `{"name":"着凉"}` || audits[0].Actor != "panel:admin" ||
		audits[0].Target != privateConvKey || audits[0].BotInstanceID != testBot {
		t.Errorf("autonomy.effect.remove audits = %+v", audits)
	}
}
