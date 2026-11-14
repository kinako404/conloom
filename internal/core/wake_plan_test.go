package core

import (
	"context"
	"math/rand/v2"
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

// wakeKinds lists the kinds of timers in order.
func wakeKinds(timers []store.ConversationTimer) []store.TimerKind {
	kinds := make([]store.TimerKind, len(timers))
	for i, t := range timers {
		kinds[i] = t.Kind
	}
	return kinds
}

// onlyInitiate returns the private chat's only timer, which must be an
// initiate.
func (e *testEnv) onlyInitiate(t *testing.T) store.ConversationTimer {
	t.Helper()
	timers := e.privateTimers(t)
	if len(timers) != 1 || timers[0].Kind != store.TimerInitiate {
		t.Fatalf("timers = %+v, want one initiate", timers)
	}
	return timers[0]
}

// requireInitiatePlanned checks that the private chat's only timer is an
// initiate of 小明's armed after the chat's newest message, due no earlier than
// gap after last and within the planning horizon, outside q.
func requireInitiatePlanned(t *testing.T, env *testEnv, last time.Time, gap time.Duration, q autonomy.Quiet) store.ConversationTimer {
	t.Helper()
	timer := env.onlyInitiate(t)
	anchor, err := env.o.newestSeq(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	earliest := last.Add(gap)
	if timer.BotInstanceID != testBot || timer.PlatformUserID != privateUser || timer.AnchorSeq != anchor || anchor == 0 {
		t.Errorf("initiate = %+v, want 小明's armed after message seq %d", timer, anchor)
	}
	if timer.DueAt.Before(earliest) || timer.DueAt.After(earliest.Add(24*time.Hour+15*time.Minute)) {
		t.Errorf("initiate due %v, want within a day after %v", timer.DueAt, earliest)
	}
	if q.Contains(timer.DueAt, env.o.loc) {
		t.Errorf("initiate due %v is in quiet hours %v", timer.DueAt.In(env.o.loc), q)
	}
	return timer
}

func TestReplan_ArmsInitiateAfterUserMessage(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	sent := clock.now()
	ev := env.event(conloomapi.ConvPrivate, privateUser, privateUser, "今天下午要考试", withName("小明"), clock.stamp)
	resp := env.submit(t, ev)
	requireInitiatePlanned(t, env, sent, 4*time.Hour, autonomy.DefaultQuiet)
	requireOneReply(t, env, resp.TaskID)

	// A level's gap applies: high waits 2 hours after the last message.
	if _, _, err := env.policies.SetPrivateLevel(context.Background(), privateConvKey, conloomapi.PrivateLevelHigh, "29999"); err != nil {
		t.Fatal(err)
	}
	requireInitiatePlanned(t, env, sent, 2*time.Hour, autonomy.DefaultQuiet)
}

func TestReplan_UsesInferredQuietHours(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.o.rand = rand.New(rand.NewPCG(1, 2))
	loc := env.o.loc
	first := time.Date(2026, 11, 6, 0, 10, 0, 0, loc)
	// 小明 writes only between midnight and 08:00, every day for six days:
	// his quiet hours are 08:00–16:00, the default ones are when he writes.
	for d := range 6 {
		for h := range 8 {
			clock.set(first.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour))
			env.seedPrivateMessage(t, clock, "在吗")
		}
	}
	clock.set(time.Date(2026, 11, 11, 12, 0, 0, 0, loc))
	inferred := autonomy.Quiet{Start: 8 * 60, End: 16 * 60, Inferred: true}
	inDefault := 0
	for range 20 {
		env.o.replanInitiate(context.Background(), privateConvKey)
		timer := requireInitiatePlanned(t, env, clock.now(), 0, inferred)
		if autonomy.DefaultQuiet.Contains(timer.DueAt, loc) {
			inDefault++
		}
	}
	if inDefault == 0 {
		t.Error("no initiate planned in the hours 小明 writes in, which the default quiet hours cover")
	}
}

func TestReplan_Exclusions(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
		meddle    func(t *testing.T, env *testEnv, clock *injectClock)
	}{
		{name: "switch off", configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Enabled = false }},
		{name: "level off", configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Private.Level = conloomapi.PrivateLevelOff }},
		{name: "paused", meddle: func(t *testing.T, env *testEnv, _ *injectClock) {
			if _, err := env.policies.Pause(context.Background(), privateConvKey, "29999"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "user no longer allowed", configure: func(c *config.Config) { c.Bots[0].PrivateChat = "allowlist" }},
		{name: "two unanswered", meddle: func(t *testing.T, env *testEnv, clock *injectClock) {
			env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(time.Hour))
			env.appendPrivateLog(t, store.ProactivePrivateFollowUp, clock.now().Add(3*time.Hour))
		}},
		{name: "forgotten since", meddle: func(t *testing.T, env *testEnv, clock *injectClock) {
			if err := env.st.ForgetPrivate(context.Background(), privateConvKey, privateUser, clock.now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "silent over a week", meddle: func(t *testing.T, _ *testEnv, clock *injectClock) {
			clock.set(clock.now().Add(proactiveLogRetention + time.Hour))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, c.configure)
			env.seedPrivateMessage(t, clock, "晚安")
			env.armWake(t, store.TimerInitiate, clock.now().Add(5*time.Hour))
			env.armWake(t, store.TimerFollowUp, clock.now().Add(6*time.Hour))
			if c.meddle != nil {
				c.meddle(t, env, clock)
			}
			env.o.replanInitiate(context.Background(), privateConvKey)
			if timers := env.privateTimers(t); len(timers) != 0 {
				t.Fatalf("timers = %+v, want none", timers)
			}
		})
	}

	t.Run("panel chat", func(t *testing.T) {
		env, clock := newWakeEnv(t, nil, nil, nil)
		key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_chat")
		now := clock.now()
		_, err := env.st.Ingest(context.Background(), store.Ingest{
			Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:panel", EventID: "evt_panel", Response: []byte("{}"), CreatedAt: now},
			Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: "panel_chat", Key: key, Type: conloomapi.ConvPrivate,
				Platform: store.PlatformPanel, PlatformConversationID: "panel_chat"},
			Message: &store.MessageRecord{ID: "msg_panel", BotInstanceID: testBot, ConversationKey: key, Platform: store.PlatformPanel,
				PlatformMessageID: "p1", ActorID: "user_panel", PlatformUserID: "panel_chat", Text: "你好", OccurredAt: now, CreatedAt: now},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := env.st.ArmTimer(context.Background(), store.ConversationTimer{ConversationKey: key, Kind: store.TimerInitiate,
			BotInstanceID: testBot, PlatformUserID: "panel_chat", DueAt: now.Add(5 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		env.o.replanInitiate(context.Background(), key)
		if timers, err := env.st.ListTimers(context.Background(), key); err != nil || len(timers) != 0 {
			t.Fatalf("panel chat timers = %+v, %v; want none", timers, err)
		}
	})
}

func TestReplan_OneUnansweredKeepsFollowUp(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(5*time.Hour))
	clock.set(clock.now().Add(5 * time.Hour))
	followUp := env.armWake(t, store.TimerFollowUp, clock.now().Add(2*time.Hour))
	env.armWake(t, store.TimerInitiate, clock.now().Add(3*time.Hour))

	env.o.replanInitiate(context.Background(), privateConvKey)
	timers := env.privateTimers(t)
	if len(timers) != 1 || timers[0].Kind != store.TimerFollowUp || !timers[0].DueAt.Equal(followUp.DueAt) {
		t.Fatalf("timers = %+v, want only the follow-up kept", timers)
	}
}

func TestReplan_UserMessageCancelsFollowUp(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(5*time.Hour))
	clock.set(clock.now().Add(6 * time.Hour))
	env.armWake(t, store.TimerFollowUp, clock.now().Add(time.Hour))

	sent := clock.now()
	resp := env.submit(t, env.event(conloomapi.ConvPrivate, privateUser, privateUser, "早", withName("小明"), clock.stamp))
	requireInitiatePlanned(t, env, sent, 4*time.Hour, autonomy.DefaultQuiet)
	requireOneReply(t, env, resp.TaskID)
}

func TestReplan_IgnoredMessageChangesNothing(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) { c.Bots[0].PrivateChat = "allowlist" })
	env.seedPrivateMessage(t, clock, "晚安")
	followUp := env.armWake(t, store.TimerFollowUp, clock.now().Add(time.Hour))
	resp := env.submit(t, env.event(conloomapi.ConvPrivate, privateUser, privateUser, "早", withName("小明"), clock.stamp))
	if resp.Disposition != conloomapi.DispositionIgnore {
		t.Fatalf("response = %+v, want the message ignored", resp)
	}
	if timers := env.privateTimers(t); len(timers) != 1 || !timers[0].DueAt.Equal(followUp.DueAt) {
		t.Fatalf("timers = %+v, want the follow-up untouched", timers)
	}
}

func TestReplan_SkippedInitiateReplans(t *testing.T) {
	env, clock := newWakeEnv(t, answer(SkipMarker), nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	fired := env.armWake(t, store.TimerInitiate, clock.now())

	if task := env.fireWake(t, fired); len(task.Actions) != 0 {
		t.Fatalf("task = %+v, want skipped", task)
	}
	next := requireInitiatePlanned(t, env, clock.now(), 0, autonomy.DefaultQuiet)
	if !next.DueAt.After(clock.now()) {
		t.Errorf("re-planned initiate due %v, want after now %v", next.DueAt, clock.now())
	}
}

func TestPolicyChanged_PrivateReplans(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "晚安")
	last := clock.now()
	env.o.replanInitiate(ctx, privateConvKey)
	requireInitiatePlanned(t, env, last, 4*time.Hour, autonomy.DefaultQuiet)

	steps := []struct {
		name   string
		change func() error
		gap    time.Duration
	}{
		{"level off", func() error {
			_, _, err := env.policies.SetPrivateLevel(ctx, privateConvKey, conloomapi.PrivateLevelOff, "29999")
			return err
		}, -1},
		{"level high", func() error {
			_, _, err := env.policies.SetPrivateLevel(ctx, privateConvKey, conloomapi.PrivateLevelHigh, "29999")
			return err
		}, 2 * time.Hour},
		{"switch off", func() error {
			_, _, err := env.policies.SetProactiveEnabled(ctx, privateConvKey, false, "29999")
			return err
		}, -1},
		{"switch on", func() error {
			_, _, err := env.policies.SetProactiveEnabled(ctx, privateConvKey, true, "29999")
			return err
		}, 2 * time.Hour},
		{"paused", func() error {
			_, err := env.policies.Pause(ctx, privateConvKey, "29999")
			return err
		}, -1},
		{"resumed", func() error {
			_, err := env.policies.Resume(ctx, privateConvKey, "29999")
			return err
		}, 2 * time.Hour},
	}
	for _, s := range steps {
		if err := s.change(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if s.gap < 0 {
			if timers := env.privateTimers(t); len(timers) != 0 {
				t.Fatalf("after %s timers = %+v, want none", s.name, timers)
			}
			continue
		}
		requireInitiatePlanned(t, env, last, s.gap, autonomy.DefaultQuiet)
	}

	// A group's policy change plans nothing.
	if _, _, err := env.policies.SetProactiveEnabled(ctx, testConvKey, false, "29999"); err != nil {
		t.Fatal(err)
	}
	if timers, err := env.st.ListTimers(ctx, testConvKey); err != nil || len(timers) != 0 {
		t.Fatalf("group timers = %+v, %v; want none", timers, err)
	}
}

func TestPrivateProactiveStatus(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "晚安")
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(-time.Hour))
	env.appendPrivateLog(t, store.ProactivePrivateSkip, clock.now().Add(time.Minute))
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(2*time.Minute))
	clock.set(clock.now().Add(3 * time.Minute))
	followUp := env.armWake(t, store.TimerFollowUp, clock.now().Add(2*time.Hour))
	until := clock.now().Add(20 * time.Minute)
	env.setWakeStatus(t, "洗澡", clock.now(), until)

	got, err := env.o.privateProactiveStatus(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	want := conloomapi.AutonomyProactive{
		Enabled: true, Level: conloomapi.PrivateLevelMid, SentToday: 2, DailyCap: 2,
		QuietStart: "00:30", QuietEnd: "08:00", UnansweredLeft: 1,
		Timers: []conloomapi.AutonomyTimer{
			{Kind: string(store.TimerStatusReturn), DueAt: until},
			{Kind: string(store.TimerFollowUp), DueAt: followUp.DueAt},
		},
	}
	if !sameProactiveStatus(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}

	if _, err := env.policies.Update(ctx, privateConvKey, conloomapi.PolicyPut{Policy: func() conloomapi.GroupPolicy {
		p := env.cfg.DefaultGroupPolicy
		p.Proactive.Enabled = false
		p.Proactive.Private = conloomapi.PrivateProactivePolicy{Level: conloomapi.PrivateLevelLow, QuietStart: "23:00", QuietEnd: "07:00"}
		return p
	}()}, "29999"); err != nil {
		t.Fatal(err)
	}
	got, err = env.o.privateProactiveStatus(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Level != conloomapi.PrivateLevelLow || got.DailyCap != 1 || got.QuietStart != "23:00" ||
		got.QuietEnd != "07:00" || got.QuietInferred || got.Timers == nil {
		t.Fatalf("status with a set policy = %+v", got)
	}

	if _, err := env.o.privateProactiveStatus(ctx, testConvKey); err == nil {
		t.Error("status of a group: no error")
	}
}

func sameProactiveStatus(a, b conloomapi.AutonomyProactive) bool {
	at, bt := a.Timers, b.Timers
	a.Timers, b.Timers = nil, nil
	return reflect.DeepEqual(a, b) && slices.EqualFunc(at, bt, func(x, y conloomapi.AutonomyTimer) bool {
		return x.Kind == y.Kind && x.DueAt.Equal(y.DueAt)
	})
}

func TestPrivateProactiveStatus_InferredQuiet(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	loc := env.o.loc
	first := time.Date(2026, 11, 6, 0, 10, 0, 0, loc)
	for d := range 6 {
		for h := range 8 {
			clock.set(first.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour))
			env.seedPrivateMessage(t, clock, "在吗")
		}
	}
	got, err := env.o.privateProactiveStatus(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.QuietStart != "08:00" || got.QuietEnd != "16:00" || !got.QuietInferred || got.Timers == nil || len(got.Timers) != 0 {
		t.Fatalf("status = %+v, want inferred 08:00–16:00 and no timers", got)
	}
	if !strings.Contains(CopyPrivateProactiveStatus(got.Level, got.SentToday, got.DailyCap, "08:00–16:00", CopyQuietInferred, true), "按你的作息推算") {
		t.Error("inferred quiet hours not named in the status copy")
	}
}
