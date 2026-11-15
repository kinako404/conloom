package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// newWakeEnv is a test orchestrator at injectMorning (11:00 on a Thursday in
// Asia/Shanghai) in which everyone may chat privately, whose short model is
// model (nil answers fakeReply) and whose bot enables botSkills; every tool of
// the built-in skills is a fake.
func newWakeEnv(t *testing.T, model handlerFunc, botSkills []string, configure func(*config.Config)) (*testEnv, *injectClock) {
	t.Helper()
	clock := newInjectClock(injectMorning)
	var tools []*recTool
	for _, name := range []string{"now", "set_status", "adjust_buff", "remember", "recall", "get_affinity"} {
		tools = append(tools, &recTool{name: name})
	}
	env := skillEnv(t, testOpts{short: model, now: clock.now, configure: func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 100
		if configure != nil {
			configure(c)
		}
	}}, botSkills, tools[0], tools[1], tools[2], tools[3], tools[4], tools[5])
	return env, clock
}

// seedPrivateMessage stores text from 小明 (20001) in his private chat at the
// clock's time, without a task.
func (e *testEnv) seedPrivateMessage(t *testing.T, clock *injectClock, text string) {
	t.Helper()
	ev := e.privateEvent(text)
	now := clock.now()
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + ev.Message.ID, EventID: ev.EventID,
			Response: []byte("{}"), CreatedAt: now},
		Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: ev.Conversation.ID, Key: privateConvKey,
			Type: conloomapi.ConvPrivate, Platform: "qq", PlatformConversationID: privateUser},
		User: &store.UserRecord{BotInstanceID: testBot, Platform: "qq", PlatformUserID: privateUser, DisplayName: "小明"},
		Message: &store.MessageRecord{ID: ev.Message.ID, BotInstanceID: testBot, ConversationKey: privateConvKey, Platform: "qq",
			PlatformMessageID: ev.Message.PlatformMessageID, ActorID: ev.Actor.ID, PlatformUserID: privateUser,
			DisplayName: "小明", Text: text, OccurredAt: now, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// armWake arms the private chat's timer of kind due at due after the chat's
// newest message and returns it.
func (e *testEnv) armWake(t *testing.T, kind store.TimerKind, due time.Time) store.ConversationTimer {
	t.Helper()
	anchor, err := e.o.newestSeq(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	timer := store.ConversationTimer{ConversationKey: privateConvKey, Kind: kind, BotInstanceID: testBot,
		PlatformUserID: privateUser, DueAt: due, AnchorSeq: anchor}
	if err := e.st.ArmTimer(context.Background(), timer); err != nil {
		t.Fatal(err)
	}
	return timer
}

// setWakeStatus stores activity, from started to until, as the private chat's
// status with its status_return timer, and returns that timer.
func (e *testEnv) setWakeStatus(t *testing.T, activity string, started, until time.Time) store.ConversationTimer {
	t.Helper()
	timer := store.ConversationTimer{ConversationKey: privateConvKey, Kind: store.TimerStatusReturn, BotInstanceID: testBot,
		PlatformUserID: privateUser, DueAt: until}
	err := e.st.SetStatus(context.Background(), store.ConversationStatus{
		ConversationKey: privateConvKey, Activity: activity, TaskID: "task_seed", StartedAt: started, UntilAt: until,
	}, &timer)
	if err != nil {
		t.Fatal(err)
	}
	return timer
}

// startWake fires timer and returns the ID of its wake task.
func (e *testEnv) startWake(t *testing.T, timer store.ConversationTimer) string {
	t.Helper()
	if err := e.o.fireTimer(context.Background(), timer); err != nil {
		t.Fatalf("fireTimer: %v", err)
	}
	id, ok := e.wakeTaskID(t, timer)
	if !ok {
		t.Fatal("fireTimer stored no wake task")
	}
	return id
}

// fireWake fires timer and returns its wake task once its runner, which logs
// and arms timers after storing the task, returned.
func (e *testEnv) fireWake(t *testing.T, timer store.ConversationTimer) conloomapi.Task {
	t.Helper()
	return e.waitWake(t, e.startWake(t, timer))
}

// waitWake returns wake task id once it finished and its runner returned.
func (e *testEnv) waitWake(t *testing.T, id string) conloomapi.Task {
	t.Helper()
	task := e.waitTask(t, id)
	waitUntil(t, "the wake task's runner", func() bool {
		return !slices.ContainsFunc(e.o.sched.Active(privateConvKey), func(q QueuedTask) bool { return q.TaskID == id })
	})
	return task
}

// wakeTaskID returns the ID of the task timer's firing stored, if any.
func (e *testEnv) wakeTaskID(t *testing.T, timer store.ConversationTimer) (string, bool) {
	t.Helper()
	rec, err := e.st.GetEvent(context.Background(), testBot, timerEventKey(timer))
	if errors.Is(err, store.ErrNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	var resp conloomapi.EventAccepted
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.TaskID, true
}

func (e *testEnv) privateLog(t *testing.T, kind string) int {
	t.Helper()
	n, err := e.st.CountProactive(context.Background(), privateConvKey, privateUser, kind, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *testEnv) appendPrivateLog(t *testing.T, kind string, at time.Time) {
	t.Helper()
	err := e.st.AppendProactiveLog(context.Background(), store.ProactiveLogEntry{
		ConversationKey: privateConvKey, PlatformUserID: privateUser, Kind: kind, TaskID: "task_seed", CreatedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) privateTimers(t *testing.T) []store.ConversationTimer {
	t.Helper()
	timers, err := e.st.ListTimers(context.Background(), privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	return timers
}

// wakeCount is conloom_wake_total for kind and result.
func (e *testEnv) wakeCount(t *testing.T, kind store.TimerKind, result string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "conloom_wake_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["kind"] == string(kind) && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// wakeRequest returns the only model request, which must be the wake task's.
func wakeRequest(t *testing.T, env *testEnv) map[string]any {
	t.Helper()
	reqs := env.short.Requests()
	if len(reqs) != 1 {
		t.Fatalf("model requests = %d, want 1", len(reqs))
	}
	return reqs[0]
}

func requireNoStatus(t *testing.T, env *testEnv) {
	t.Helper()
	if st, err := env.st.GetStatus(context.Background(), privateConvKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("status = %+v, %v; want it cleared", st, err)
	}
}

func TestWake_StatusReturnSends(t *testing.T) {
	env, clock := newWakeEnv(t, nil, []string{"status", "buff"}, nil)
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(20 * time.Minute)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)

	task := env.fireWake(t, timer)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want one reply", task)
	}
	if task.Meta.Trigger != TriggerStatusReturn {
		t.Errorf("trigger = %q, want %q", task.Meta.Trigger, TriggerStatusReturn)
	}
	rec, err := env.st.GetOutbox(context.Background(), testBot, task.Actions[0].ActionID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Channel != "" || rec.ConversationKey != privateConvKey || task.Actions[0].ConversationType != conloomapi.ConvPrivate {
		t.Errorf("action = %+v, want it on the private chat's platform channel", rec)
	}

	req := wakeRequest(t, env)
	last := lastContent(req)
	directive := CopyDirectiveStatusReturn("洗澡")
	state := autonomy.RenderState(nil, defaultStateBars(60, 70), env.o.loc)
	if !strings.HasPrefix(last, state+"\n\n") || !strings.HasSuffix(last, "\n"+directive) || !strings.Contains(last, "我去洗个澡") {
		t.Fatalf("last user message = %q, want the state block, the history and then the directive", last)
	}
	if text := requestText(t, req); strings.Contains(text, "【当前消息】") {
		t.Errorf("request has a current message:\n%s", text)
	}
	requireNoStatus(t, env)
	if got := wakeKinds(env.privateTimers(t)); !reflect.DeepEqual(got, []store.TimerKind{store.TimerInitiate}) {
		t.Errorf("timers = %v, want only the initiate planned after the return", got)
	}
	if n := env.wakeCount(t, store.TimerStatusReturn, "sent"); n != 1 {
		t.Errorf("wake sent metric = %v, want 1", n)
	}
	if n := env.privateLog(t, store.ProactivePrivateInitiate) + env.privateLog(t, store.ProactivePrivateSkip); n != 0 {
		t.Errorf("private proactive log entries = %d, want none for a status return", n)
	}
}

// firstSource makes a rand.Rand whose Float64 and IntN (for n up to 2048)
// return 0, so plans pick their first candidate without jitter.
type firstSource struct{}

func (firstSource) Uint64() uint64 { return 1 << 53 }

func TestWake_StatusReturnReplansFromItsMessage(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.o.rand = rand.New(firstSource{})
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(3 * time.Hour)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)

	if task := env.fireWake(t, timer); len(task.Actions) != 1 {
		t.Fatalf("task = %+v, want the return sent", task)
	}
	// The earliest initiate is the level's gap (4 hours at 中) after the
	// return, not after 小明's message.
	if next := env.onlyInitiate(t); !next.DueAt.Equal(until.Add(4 * time.Hour)) {
		t.Fatalf("initiate due %v, want %v", next.DueAt, until.Add(4*time.Hour))
	}
}

func TestWake_StatusReturnCountsAsUnanswered(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) {
		c.DefaultGroupPolicy.Proactive.Private.Level = conloomapi.PrivateLevelLow
	})
	env.o.rand = rand.New(firstSource{})
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(20 * time.Minute)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)
	if task := env.fireWake(t, timer); len(task.Actions) != 1 {
		t.Fatalf("status return = %+v, want it sent", task)
	}
	if got := env.privateLog(t, store.ProactivePrivateReturn); got != 1 {
		t.Errorf("private_return entries = %d, want 1", got)
	}
	st, err := env.o.privateProactiveStatus(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if st.SentToday != 0 || st.UnansweredLeft != 1 {
		t.Errorf("after the return: sent today %d, unanswered left %d; want 0 and 1", st.SentToday, st.UnansweredLeft)
	}

	// The return alone still lets the bot start the chat, even with 低's
	// daily cap of 1.
	initiate := env.onlyInitiate(t)
	clock.set(initiate.DueAt)
	env.o.pollWakes(ctx)
	id, ok := env.wakeTaskID(t, initiate)
	if !ok {
		t.Fatal("the initiate after the return stored no task")
	}
	if task := env.waitWake(t, id); len(task.Actions) != 1 {
		t.Fatalf("initiate = %+v, want it sent", task)
	}
	// The return and the initiate use up the unanswered limit: no follow-up.
	if timers := env.privateTimers(t); len(timers) != 0 {
		t.Fatalf("timers = %+v, want no follow-up", timers)
	}
}

func TestWake_PanelChatStatusReturnNotCounted(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	key, err := env.o.CreatePanelChat(ctx, testBot, "admin", "小红")
	if err != nil {
		t.Fatal(err)
	}
	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "我去洗个澡")).TaskID)
	waitUntil(t, "the panel reply's runner", func() bool { return !env.o.sched.Busy(key) })
	until := clock.now()
	timer := store.ConversationTimer{ConversationKey: key, Kind: store.TimerStatusReturn, BotInstanceID: testBot,
		PlatformUserID: PanelActorID("admin"), DueAt: until}
	if err := env.st.SetStatus(ctx, store.ConversationStatus{ConversationKey: key, Activity: "洗澡", TaskID: "task_seed",
		StartedAt: until.Add(-20 * time.Minute), UntilAt: until}, &timer); err != nil {
		t.Fatal(err)
	}
	if err := env.o.fireTimer(ctx, timer); err != nil {
		t.Fatalf("fireTimer: %v", err)
	}
	id, ok := env.wakeTaskID(t, timer)
	if !ok {
		t.Fatal("the status return stored no task")
	}
	task := env.waitTask(t, id)
	if len(task.Actions) != 1 {
		t.Fatalf("status return = %+v, want it sent", task)
	}
	wantChannels(t, "panel chat status return", env.actionChannels(t, task), store.ChannelPanel)
	waitUntil(t, "the status return's runner", func() bool { return !env.o.sched.Busy(key) })
	if n, err := env.st.CountProactive(ctx, key, "", store.ProactivePrivateReturn, time.Time{}); err != nil || n != 0 {
		t.Fatalf("private_return entries = %d, %v; want none in a panel chat", n, err)
	}
	st, err := env.o.privateProactiveStatus(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if st.UnansweredLeft != 2 {
		t.Errorf("unanswered left = %d, want 2 in a panel chat", st.UnansweredLeft)
	}
}

func TestWake_StatusReturnWithoutStatusIsSkipped(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	task := env.fireWake(t, env.armWake(t, store.TimerStatusReturn, clock.now()))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultSkipped {
		t.Fatalf("task = %+v, want skipped without a reply", task)
	}
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestWake_SkipSendsNothing(t *testing.T) {
	env, clock := newWakeEnv(t, answer(SkipMarker), nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	task := env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultSkipped {
		t.Fatalf("initiate task = %+v, want skipped without a reply", task)
	}
	if got := env.privateLog(t, store.ProactivePrivateSkip); got != 1 {
		t.Errorf("private_skip entries = %d, want 1", got)
	}
	if got := env.privateLog(t, store.ProactivePrivateInitiate); got != 0 {
		t.Errorf("private_initiate entries = %d, want 0", got)
	}
	if got := wakeKinds(env.privateTimers(t)); !reflect.DeepEqual(got, []store.TimerKind{store.TimerInitiate}) {
		t.Errorf("timers = %v, want no follow-up, only the initiate planned again", got)
	}
	if n := env.wakeCount(t, store.TimerInitiate, "skipped"); n != 1 {
		t.Errorf("wake skipped metric = %v, want 1", n)
	}

	// A status return the model skips still ends the status, and is not
	// logged as a skipped start.
	until := clock.now().Add(10 * time.Minute)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)
	if task := env.fireWake(t, timer); len(task.Actions) != 0 {
		t.Fatalf("status return task = %+v, want no reply", task)
	}
	requireNoStatus(t, env)
	if got := env.privateLog(t, store.ProactivePrivateSkip); got != 1 {
		t.Errorf("private_skip entries = %d, want still 1", got)
	}
}

func TestWake_InitiateLogsAndArmsFollowUp(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "今天下午要考试")
	clock.set(clock.now().Add(5 * time.Hour))
	now := clock.now()

	task := env.fireWake(t, env.armWake(t, store.TimerInitiate, now))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Meta.Trigger != TriggerInitiate {
		t.Fatalf("task = %+v, want one reply", task)
	}
	last := lastContent(wakeRequest(t, env))
	directive := CopyDirectiveInitiate("小明", "星期四 16:00", "约 5 小时", true)
	if !strings.HasSuffix(last, "\n"+directive) || !strings.Contains(last, "今天下午要考试") || strings.Contains(last, "【当前消息】") {
		t.Fatalf("last user message = %q, want the history and then\n%s", last, directive)
	}
	if got := env.privateLog(t, store.ProactivePrivateInitiate); got != 1 {
		t.Errorf("private_initiate entries = %d, want 1", got)
	}
	timers := env.privateTimers(t)
	if len(timers) != 1 {
		t.Fatalf("timers = %+v, want one follow-up", timers)
	}
	f := timers[0]
	if f.Kind != store.TimerFollowUp || f.BotInstanceID != testBot || f.PlatformUserID != privateUser ||
		f.DueAt.Before(now.Add(90*time.Minute)) || f.DueAt.After(now.Add(4*time.Hour)) {
		t.Errorf("follow-up = %+v, want one 1.5–4h after %v", f, now)
	}
	if n := env.wakeCount(t, store.TimerInitiate, "sent"); n != 1 {
		t.Errorf("wake sent metric = %v, want 1", n)
	}
}

func TestWake_FollowUpNoFurtherTimer(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "今天下午要考试")
	clock.set(clock.now().Add(5 * time.Hour))
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now())
	clock.set(clock.now().Add(2 * time.Hour))

	task := env.fireWake(t, env.armWake(t, store.TimerFollowUp, clock.now()))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Meta.Trigger != TriggerFollowUp {
		t.Fatalf("task = %+v, want one reply", task)
	}
	if last := lastContent(wakeRequest(t, env)); !strings.HasSuffix(last, "\n"+CopyDirectiveFollowUp("小明", true)) {
		t.Fatalf("last user message = %q, want the follow-up directive", last)
	}
	if got := env.privateLog(t, store.ProactivePrivateFollowUp); got != 1 {
		t.Errorf("private_followup entries = %d, want 1", got)
	}
	if timers := env.privateTimers(t); len(timers) != 0 {
		t.Errorf("timers = %+v, want none after a follow-up", timers)
	}
}

func TestWake_InitiateRechecksBudget(t *testing.T) {
	for _, c := range []struct {
		name  string
		level string
		// logged are private log entries stored before the user's message
		// (before) or after it.
		logged []string
		before bool
		kind   store.TimerKind
	}{
		{name: "initiate after an unanswered initiate", logged: []string{store.ProactivePrivateInitiate}, kind: store.TimerInitiate},
		{name: "follow-up without an initiate", kind: store.TimerFollowUp},
		{name: "follow-up after a follow-up",
			logged: []string{store.ProactivePrivateInitiate, store.ProactivePrivateFollowUp}, kind: store.TimerFollowUp},
		{name: "daily cap", level: conloomapi.PrivateLevelLow, logged: []string{store.ProactivePrivateInitiate}, before: true,
			kind: store.TimerInitiate},
		{name: "level off", level: conloomapi.PrivateLevelOff, kind: store.TimerInitiate},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, func(cfg *config.Config) {
				cfg.DefaultGroupPolicy.Proactive.Private.Level = c.level
			})
			if c.before {
				for _, kind := range c.logged {
					env.appendPrivateLog(t, kind, clock.now().Add(-time.Hour))
				}
			}
			env.seedPrivateMessage(t, clock, "晚安")
			clock.set(clock.now().Add(5 * time.Hour))
			if !c.before {
				for _, kind := range c.logged {
					env.appendPrivateLog(t, kind, clock.now().Add(-time.Hour))
				}
			}

			task := env.fireWake(t, env.armWake(t, c.kind, clock.now()))
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultSkipped {
				t.Fatalf("task = %+v, want skipped", task)
			}
			if n := env.modelCalls(); n != 0 {
				t.Errorf("model calls = %d, want 0", n)
			}
			if got := env.privateLog(t, store.ProactivePrivateSkip); got != 1 {
				t.Errorf("private_skip entries = %d, want 1", got)
			}
		})
	}
}

// protectedWord is the first entry of the built-in protected lexicon,
// decoded at run time so that no plaintext entry appears in the source.
func protectedWord(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../review/protected_words.b64")
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	w, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	if err != nil || len(w) == 0 {
		t.Fatalf("decode the first lexicon entry: %v", err)
	}
	return string(w)
}

// withPersona makes the default persona template minor or adult.
func withPersona(minor, adult bool) func(*config.Config) {
	return func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Minor, doc.Adult = minor, adult
		c.PersonaTemplates["default"] = doc
	}
}

const noRomanceLine = "不要表现出恋爱或暧昧的想念。"

func TestWake_ProtectedDirectiveAndReview(t *testing.T) {
	word := protectedWord(t)
	env, clock := newWakeEnv(t, answer("想你了，"+word), nil, withPersona(true, false))
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	task := env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	if !strings.Contains(lastContent(wakeRequest(t, env)), noRomanceLine) {
		t.Errorf("protected directive lacks %q", noRomanceLine)
	}
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultBlocked {
		t.Fatalf("task = %+v, want the reply blocked", task)
	}
	audits, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "review.block", BotInstanceID: testBot, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits) != 1 || !strings.Contains(audits[0].Detail, review.ReasonProtectedContent) {
		t.Fatalf("review.block audits = %+v, want one for protected content", audits)
	}
	if got := env.privateLog(t, store.ProactivePrivateSkip); got != 1 {
		t.Errorf("private_skip entries = %d, want 1", got)
	}
}

func TestWake_OpenDirectiveNoProtectedLine(t *testing.T) {
	word := protectedWord(t)
	env, clock := newWakeEnv(t, answer("想你了，"+word), nil, withPersona(false, true))
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	task := env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	last := lastContent(wakeRequest(t, env))
	if strings.Contains(last, noRomanceLine) || !strings.HasSuffix(last, CopyDirectiveInitiate("小明", "星期四 16:00", "约 5 小时", false)) {
		t.Errorf("open directive = %q, want the initiate directive without %q", last, noRomanceLine)
	}
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v, want the reply sent in open mode", task)
	}
}

func TestWake_ToolsOnlyBuff(t *testing.T) {
	env, clock := newWakeEnv(t, nil, []string{"time", "status", "buff", "memory"}, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	req := wakeRequest(t, env)
	if got := offeredTools(req); !reflect.DeepEqual(got, []string{"adjust_buff"}) {
		t.Errorf("wake request tools = %v, want [adjust_buff]", got)
	}
	if sys := requestMessages(t, req)[0]["content"].(string); strings.Contains(sys, "set_status") || strings.Contains(sys, "remember") {
		t.Errorf("wake system prompt describes other skills:\n%s", sys)
	}

	env2, clock2 := newWakeEnv(t, nil, []string{"time", "status", "memory"}, nil)
	env2.seedPrivateMessage(t, clock2, "晚安")
	clock2.set(clock2.now().Add(5 * time.Hour))
	env2.fireWake(t, env2.armWake(t, store.TimerInitiate, clock2.now()))
	if got := offeredTools(wakeRequest(t, env2)); len(got) != 0 {
		t.Errorf("wake request tools without buff = %v, want none", got)
	}
}

func TestWake_NoAffinityBump(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, privMemConfig(true))
	ev := env.event(conloomapi.ConvPrivate, privateUser, privateUser, "你好", withName("小明"), clock.stamp)
	requireOneReply(t, env, env.submit(t, ev).TaskID)
	var before map[string]store.MemberMemory
	waitUntil(t, "the reply's affinity", func() bool {
		var err error
		before, err = env.st.GetMemberMemories(context.Background(), privateConvKey, []string{privateUser})
		return err == nil && before[privateUser].Affinity != nil
	})

	waitUntil(t, "the reply's runner", func() bool { return !env.o.sched.Busy(privateConvKey) })
	clock.set(clock.now().Add(30 * time.Hour))
	until := clock.now()
	timer := env.setWakeStatus(t, "午睡", until.Add(-time.Hour), until)
	if task := env.fireWake(t, timer); len(task.Actions) != 1 {
		t.Fatalf("wake task = %+v, want a reply", task)
	}
	time.Sleep(50 * time.Millisecond)
	after, err := env.st.GetMemberMemories(context.Background(), privateConvKey, []string{privateUser})
	if err != nil {
		t.Fatal(err)
	}
	b, a := before[privateUser], after[privateUser]
	if *a.Affinity != *b.Affinity || a.LastInteractionDay != b.LastInteractionDay || a.Version != b.Version {
		t.Errorf("member memory after the wake reply = %+v, want it unchanged from %+v", a, b)
	}
}

func wakeAudits(t *testing.T, env *testEnv) []store.AuditEvent {
	t.Helper()
	events, err := env.st.ListAudit(context.Background(), store.AuditFilter{Kind: "wake.cancelled", BotInstanceID: testBot, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestWake_UserSpeaksDuringWakeCancels(t *testing.T) {
	model := newGatedModel()
	env, clock := newWakeEnv(t, model.handle, nil, nil)
	t.Cleanup(model.open)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	wakeID := env.startWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	waitUntil(t, "the wake task's model call", func() bool { return model.calls.Load() == 1 })
	reply := env.submit(t, env.event(conloomapi.ConvPrivate, privateUser, privateUser, "在吗", withName("小明"), clock.stamp))

	requireCancelledWithoutActions(t, env, wakeID)
	requireOneReply(t, env, reply.TaskID)
	model.open()
	time.Sleep(50 * time.Millisecond)
	if n := liveActions(t, env, wakeID, reply.TaskID); n != 1 {
		t.Fatalf("replies = %d, want 1", n)
	}
	audits := wakeAudits(t, env)
	if len(audits) != 1 || !strings.Contains(audits[0].Detail, wakeID) || !strings.Contains(audits[0].Detail, `"reason":"user_message"`) {
		t.Fatalf("wake.cancelled audits = %+v, want one for %s", audits, wakeID)
	}
	if n := env.privateLog(t, store.ProactivePrivateInitiate) + env.privateLog(t, store.ProactivePrivateSkip); n != 0 {
		t.Errorf("private proactive log entries = %d, want none for a cancelled initiate", n)
	}
	if n := env.wakeCount(t, store.TimerInitiate, "cancelled"); n != 1 {
		t.Errorf("wake cancelled metric = %v, want 1", n)
	}
}

func TestWake_UserSpeaksAfterWakeStoredCancelsIt(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	entered, release := env.blockFirstCompletion(t, func(c store.TaskCompletion) bool { return c.Meta.Trigger == TriggerInitiate })

	wakeID := env.startWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	waitEntered(t, entered)
	ev := env.event(conloomapi.ConvPrivate, privateUser, privateUser, "在吗", withName("小明"), clock.stamp)
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	type submitted struct {
		resp conloomapi.EventAccepted
		err  error
	}
	done := make(chan submitted, 1)
	go func() {
		resp, _, err := env.o.Submit(context.Background(), testBot, "test:"+ev.Message.ID, body, ev)
		done <- submitted{resp, err}
	}()
	time.Sleep(50 * time.Millisecond)
	release()
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	reply := res.resp

	requireOneReply(t, env, reply.TaskID)
	wake := env.waitTask(t, wakeID)
	if len(wake.Actions) != 1 || outboxStatusOf(t, env, wake.Actions[0].ActionID) != store.OutboxCancelled {
		t.Fatalf("wake task = %+v, want its stored reply cancelled", wake)
	}
	if n := liveActions(t, env, wakeID, reply.TaskID); n != 1 {
		t.Fatalf("replies = %d, want 1", n)
	}
	audits := wakeAudits(t, env)
	if len(audits) != 1 || !strings.Contains(audits[0].Detail, wakeID) || !strings.Contains(audits[0].Detail, `"cancelled":1`) {
		t.Fatalf("wake.cancelled audits = %+v, want one cancelling the reply of %s", audits, wakeID)
	}
}

func TestWake_CancelledStoredReplyNotCounted(t *testing.T) {
	for _, handedOut := range []bool{false, true} {
		name := "not handed out"
		if handedOut {
			name = "handed out"
		}
		t.Run(name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, nil)
			ctx := context.Background()
			env.seedPrivateMessage(t, clock, "今天下午要考试")
			clock.set(clock.now().Add(5 * time.Hour))
			wake := env.fireWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
			if len(wake.Actions) != 1 || env.privateLog(t, store.ProactivePrivateInitiate) != 1 {
				t.Fatalf("wake task = %+v, want its reply stored and logged", wake)
			}
			if got := wakeKinds(env.privateTimers(t)); !reflect.DeepEqual(got, []store.TimerKind{store.TimerFollowUp}) {
				t.Fatalf("timers = %v, want the follow-up", got)
			}
			if handedOut {
				if leased, err := env.st.LeaseOutbox(ctx, testBot, 10, clock.now(), time.Minute); err != nil || len(leased) != 1 {
					t.Fatalf("LeaseOutbox = %+v, %v; want the wake reply", leased, err)
				}
			}

			clock.set(clock.now().Add(time.Minute))
			sent := clock.now()
			reply := env.submit(t, env.event(conloomapi.ConvPrivate, privateUser, privateUser, "考完了", withName("小明"), clock.stamp))
			requireOneReply(t, env, reply.TaskID)
			requireInitiatePlanned(t, env, sent, 4*time.Hour, autonomy.DefaultQuiet)
			want, status := 0, store.OutboxCancelled
			if handedOut {
				want, status = 1, store.OutboxLeased
			}
			if got := outboxStatusOf(t, env, wake.Actions[0].ActionID); got != status {
				t.Errorf("wake reply status = %s, want %s", got, status)
			}
			if got := env.privateLog(t, store.ProactivePrivateInitiate); got != want {
				t.Errorf("private_initiate entries = %d, want %d", got, want)
			}
		})
	}
}

func TestWake_WithheldWhenUserWroteOrWasForgotten(t *testing.T) {
	for _, c := range []struct {
		name   string
		meddle func(t *testing.T, env *testEnv, clock *injectClock)
	}{
		{"user message", func(t *testing.T, env *testEnv, clock *injectClock) {
			env.seedPrivateMessage(t, clock, "我回来了")
		}},
		{"forgotten", func(t *testing.T, env *testEnv, clock *injectClock) {
			if err := env.st.ForgetPrivate(context.Background(), privateConvKey, privateUser, clock.now()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			model := newGatedModel()
			env, clock := newWakeEnv(t, model.handle, nil, nil)
			t.Cleanup(model.open)
			env.seedPrivateMessage(t, clock, "晚安")
			clock.set(clock.now().Add(5 * time.Hour))

			wakeID := env.startWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
			waitUntil(t, "the wake task's model call", func() bool { return model.calls.Load() == 1 })
			clock.set(clock.now().Add(time.Second))
			c.meddle(t, env, clock)
			model.open()

			env.waitWake(t, wakeID)
			requireCancelledWithoutActions(t, env, wakeID)
			if n := env.privateLog(t, store.ProactivePrivateInitiate) + env.privateLog(t, store.ProactivePrivateSkip); n != 0 {
				t.Errorf("private proactive log entries = %d, want none", n)
			}
		})
	}
}

func TestWake_FireTwiceOneTask(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(20 * time.Minute)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)

	first := env.fireWake(t, timer)
	if err := env.o.fireTimer(context.Background(), timer); err != nil {
		t.Fatalf("second fireTimer: %v", err)
	}
	if id, _ := env.wakeTaskID(t, timer); id != first.TaskID {
		t.Fatalf("event task = %s, want %s", id, first.TaskID)
	}

	// Armed again for the time it already fired at, the timer is dropped
	// rather than claimed again and again.
	env.setWakeStatus(t, "洗澡", until.Add(-20*time.Minute), until)
	if err := env.o.fireTimer(context.Background(), timer); err != nil {
		t.Fatalf("third fireTimer: %v", err)
	}
	if timers := env.privateTimers(t); slices.ContainsFunc(timers, func(t store.ConversationTimer) bool { return t.Kind == store.TimerStatusReturn }) {
		t.Errorf("timers = %+v, want the re-armed status return dropped", timers)
	}
	time.Sleep(50 * time.Millisecond)
	if n := env.modelCalls(); n != 1 {
		t.Errorf("model calls = %d, want 1", n)
	}
	if tasks, err := env.st.ListUnfinishedTasks(context.Background()); err != nil || len(tasks) != 0 {
		t.Errorf("unfinished tasks = %+v, %v; want none", tasks, err)
	}
}

func TestWake_FireAfterForgetIsGone(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	env.armWake(t, store.TimerInitiate, clock.now())
	ctx := context.Background()
	claimed, err := env.st.ClaimDueTimers(ctx, clock.now(), 2*time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, %v; want the initiate", claimed, err)
	}
	if err := env.st.ForgetPrivate(ctx, privateConvKey, privateUser, clock.now()); err != nil {
		t.Fatal(err)
	}

	if err := env.o.fireTimer(ctx, claimed[0]); err != nil {
		t.Fatalf("fireTimer: %v", err)
	}
	if id, ok := env.wakeTaskID(t, claimed[0]); ok {
		t.Fatalf("a forgotten chat's timer stored task %s", id)
	}
	if tasks, err := env.st.ListUnfinishedTasks(ctx); err != nil || len(tasks) != 0 {
		t.Errorf("unfinished tasks = %+v, %v; want none", tasks, err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestWake_NotFiredWhileUserMessageAccepted(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	timer := env.armWake(t, store.TimerInitiate, clock.now())

	end := env.o.speakers.begin(privateConvKey)
	if err := env.o.fireTimer(context.Background(), timer); !errors.Is(err, errUserSpeaking) {
		t.Fatalf("fireTimer while a message is accepted = %v, want errUserSpeaking", err)
	}
	if _, ok := env.wakeTaskID(t, timer); ok || len(env.privateTimers(t)) != 1 {
		t.Fatal("fireTimer stored a task or dropped the timer while a message was accepted")
	}
	end()
	if task := env.fireWake(t, timer); len(task.Actions) != 1 {
		t.Fatalf("task = %+v, want a reply once the message was accepted", task)
	}
}

func TestWake_NotFiredWhileBusy(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	timer := env.armWake(t, store.TimerInitiate, clock.now())

	env.o.sched.Restore(QueuedTask{TaskID: "task_busy", ConversationKey: privateConvKey, ActorID: privateUser,
		EnqueuedAt: time.Now(), ReadyAt: time.Now().Add(time.Hour)})
	if err := env.o.fireTimer(context.Background(), timer); !errors.Is(err, errBusy) {
		t.Fatalf("fireTimer while the chat is busy = %v, want errBusy", err)
	}
	if _, ok := env.wakeTaskID(t, timer); ok || len(env.privateTimers(t)) != 1 {
		t.Fatal("fireTimer stored a task or dropped the timer while the chat was busy")
	}
	env.o.sched.Cancel(privateConvKey, "task_busy")
	if task := env.fireWake(t, timer); len(task.Actions) != 1 {
		t.Fatalf("task = %+v, want a reply once the chat is idle", task)
	}
}

func TestWake_GroupTimerRefused(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	timer := store.ConversationTimer{ConversationKey: testConvKey, Kind: store.TimerInitiate, BotInstanceID: testBot,
		PlatformUserID: privateUser, DueAt: clock.now()}
	if err := env.o.fireTimer(context.Background(), timer); err == nil {
		t.Fatal("fireTimer of a group timer succeeded")
	}
	if _, ok := env.wakeTaskID(t, timer); ok {
		t.Fatal("a group timer stored a task")
	}
}

// storeWakeTask stores a queued wake task of the private chat with trigger,
// as one left by a process that stopped before running it.
func (e *testEnv) storeWakeTask(t *testing.T, trigger string, now time.Time) string {
	t.Helper()
	id := "task_wake_left"
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "conloom:timer:left", EventID: "timer_left",
			Response: []byte("{}"), CreatedAt: now},
		NewTask: &store.TaskRecord{ID: id, BotInstanceID: testBot, ConversationKey: privateConvKey, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: privateUser, Meta: conloomapi.TaskMeta{Trigger: trigger},
			EventOccurredAt: now, CreatedAt: now, UpdatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWake_RecoverKeepsWakeActor(t *testing.T) {
	model := newGatedModel()
	env, clock := newWakeEnv(t, model.handle, nil, nil)
	t.Cleanup(model.open)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))
	wakeID := env.storeWakeTask(t, TriggerInitiate, clock.now())
	clock.set(clock.now().Add(time.Minute))

	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the recovered wake task's model call", func() bool { return model.calls.Load() == 1 })
	active := env.o.sched.Active(privateConvKey)
	if len(active) != 1 || active[0].TaskID != wakeID || active[0].ActorID != wakeActor(privateUser) {
		t.Fatalf("active tasks = %+v, want %s under %s", active, wakeID, wakeActor(privateUser))
	}
	reply := env.submit(t, env.event(conloomapi.ConvPrivate, privateUser, privateUser, "在吗", withName("小明"), clock.stamp))
	requireCancelledWithoutActions(t, env, wakeID)
	requireOneReply(t, env, reply.TaskID)
}

func TestWake_RecoverStale(t *testing.T) {
	for _, c := range []struct {
		name    string
		trigger string
		// statusFor is how long before the task's creation a status ended;
		// a negative value is a status still lasting then, none without one.
		statusFor  time.Duration
		hasStatus  bool
		wantStatus bool
		wantKinds  []store.TimerKind
	}{
		{name: "initiate planned again", trigger: TriggerInitiate, wantKinds: []store.TimerKind{store.TimerInitiate}},
		{name: "follow-up dropped", trigger: TriggerFollowUp},
		{name: "status return ends its status", trigger: TriggerStatusReturn, statusFor: time.Minute, hasStatus: true},
		{name: "status return keeps a newer status", trigger: TriggerStatusReturn, statusFor: -time.Hour, hasStatus: true,
			wantStatus: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, nil)
			ctx := context.Background()
			env.seedPrivateMessage(t, clock, "晚安")
			clock.set(clock.now().Add(10 * time.Minute))
			created := clock.now()
			if c.hasStatus {
				until := created.Add(-c.statusFor)
				if err := env.st.SetStatus(ctx, store.ConversationStatus{ConversationKey: privateConvKey, Activity: "洗澡",
					TaskID: "task_seed", StartedAt: until.Add(-20 * time.Minute), UntilAt: until}, nil); err != nil {
					t.Fatal(err)
				}
			}
			id := env.storeWakeTask(t, c.trigger, created)
			clock.set(created.Add(3 * time.Minute))

			if err := env.o.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			task := env.getTask(t, id)
			if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != errorCodeStale || len(task.Actions) != 0 {
				t.Fatalf("task = %+v, want failed as stale", task)
			}
			if got := wakeKinds(env.privateTimers(t)); !slices.Equal(got, c.wantKinds) {
				t.Fatalf("timers = %v, want %v", got, c.wantKinds)
			}
			if len(c.wantKinds) > 0 {
				requireInitiatePlanned(t, env, created.Add(-10*time.Minute), 4*time.Hour, autonomy.DefaultQuiet)
			}
			_, err := env.st.GetStatus(ctx, privateConvKey)
			if kept := err == nil; kept != c.wantStatus || err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("status kept = %v (%v), want %v", kept, err, c.wantStatus)
			}
		})
	}
}

func TestWake_StopCancelsWake(t *testing.T) {
	model := newGatedModel()
	env, clock := newWakeEnv(t, model.handle, nil, nil)
	t.Cleanup(model.open)
	env.seedPrivateMessage(t, clock, "晚安")
	clock.set(clock.now().Add(5 * time.Hour))

	wakeID := env.startWake(t, env.armWake(t, store.TimerInitiate, clock.now()))
	waitUntil(t, "the wake task's model call", func() bool { return model.calls.Load() == 1 })
	cmd := store.TaskRecord{ID: "task_stop", BotInstanceID: testBot, ConversationKey: privateConvKey, ActorPlatformUserID: privateUser}
	got, err := env.o.stop(context.Background(), cmd, false)
	if err != nil || got != CopyStopDone {
		t.Fatalf("stop = %q, %v; want %q", got, err, CopyStopDone)
	}
	requireCancelledWithoutActions(t, env, wakeID)
	if active := env.o.sched.Active(privateConvKey); slices.ContainsFunc(active, func(q QueuedTask) bool { return q.TaskID == wakeID }) {
		t.Errorf("active tasks = %+v, want the wake task gone", active)
	}
}

func TestWake_Copy(t *testing.T) {
	for _, s := range []string{
		CopyDirectiveInitiate("小明", "星期四 16:00", "约 5 小时", false),
		CopyDirectiveFollowUp("小明", false),
		CopyDirectiveStatusReturn("洗澡"),
	} {
		if !strings.HasPrefix(s, "【提示】") || !strings.Contains(s, SkipMarker) || strings.Contains(s, noRomanceLine) {
			t.Errorf("directive %q", s)
		}
	}
	initiate := CopyDirectiveInitiate("小明", "星期四 16:00", "约 5 小时", true)
	for _, part := range []string{"主动找 小明 说话", "现在是 星期四 16:00，你们上次聊天是约 5 小时前。", "不要提“主动”“系统”“提示”", noRomanceLine} {
		if !strings.Contains(initiate, part) {
			t.Errorf("initiate directive lacks %q:\n%s", part, initiate)
		}
	}
	if s := CopyDirectiveInitiate("小明", "星期四 16:00", "", false); strings.Contains(s, "上次聊天") || !strings.Contains(s, "现在是 星期四 16:00。") {
		t.Errorf("initiate directive without a gap = %q", s)
	}
	if s := CopyDirectiveFollowUp("小明", true); !strings.HasSuffix(s, noRomanceLine) || !strings.Contains(s, "不要催促或抱怨") {
		t.Errorf("protected follow-up directive = %q", s)
	}
	if s := CopyDirectiveStatusReturn("洗澡"); !strings.Contains(s, "你刚做完「洗澡」") {
		t.Errorf("status return directive = %q", s)
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{30 * time.Minute, "不到 1 小时"}, {90 * time.Minute, "约 2 小时"}, {5 * time.Hour, "约 5 小时"}, {47 * time.Hour, "约 47 小时"}, {72 * time.Hour, "约 3 天"}} {
		if got := approxGap(c.d); got != c.want {
			t.Errorf("approxGap(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
