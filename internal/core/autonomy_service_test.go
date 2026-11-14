package core

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
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

func TestAutonomyService_State(t *testing.T) {
	bars := []conloomapi.PersonaBar{
		{Name: "精力", Baseline: 70, HalfLife: conloomapi.Duration{Duration: 4 * time.Hour}, Description: "高=有精神，低=累、困"},
		{Name: "心情", Baseline: 60, HalfLife: conloomapi.Duration{Duration: 6 * time.Hour}},
	}
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Bars = bars
		c.PersonaTemplates["default"] = doc
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
		{Name: "饥饿", Source: store.BuffSourcePanel, Reason: "测试", Before: 0, After: 10, At: now.Add(-time.Hour)},
		{Name: "心情", Source: store.BuffSourcePanel, Reason: "测试", Before: 0, After: 82, At: now.Add(-6 * time.Hour)},
	}
	if len(got.Events) != len(wantEvents) {
		t.Fatalf("events = %+v, want %+v", got.Events, wantEvents)
	}
	for i, ev := range got.Events {
		w := wantEvents[i]
		if ev.Name != w.Name || ev.Source != w.Source || ev.Reason != w.Reason || ev.Before != w.Before || ev.After != w.After ||
			!ev.At.Equal(w.At) {
			t.Errorf("events[%d] = %+v, want %+v", i, ev, w)
		}
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
	if got.Status != nil || got.Events == nil || len(got.Events) != 0 || got.Proactive.Timers == nil {
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
