package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/interest"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// topicText is what a member talks to the bot about; matchText shares four
	// terms with it and otherText none.
	topicText = "我在研究咖啡豆烘焙"
	matchText = "咖啡豆烘焙真难"
	otherText = "晚上吃火锅去"
	topicTerm = "咖啡"
)

func withProactive(f func(p *conloomapi.ProactivePolicy)) func(*config.Config) {
	return func(c *config.Config) { f(&c.DefaultGroupPolicy.Proactive) }
}

// engage has user mention the bot with text and waits for the reply.
func (e *testEnv) engage(t *testing.T, user, text string) {
	t.Helper()
	task := e.waitTask(t, e.submit(t, e.groupEvent(user, text, mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("mention task = %+v", task)
	}
}

// seedMember stores user as engaged at engagedAt with a profile built from
// topicText.
func (e *testEnv) seedMember(t *testing.T, user, level string, engagedAt time.Time) {
	t.Helper()
	err := e.st.PutMember(context.Background(), store.MemberRecord{
		ConversationKey: testConvKey, PlatformUserID: user, LastEngagedAt: engagedAt.UTC(),
		ProactiveLevel: level, Interests: interest.Update(nil, topicText), UpdatedAt: engagedAt.UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) member(t *testing.T, user string) (store.MemberRecord, bool) {
	t.Helper()
	m, err := e.st.GetMember(context.Background(), testConvKey, user)
	if errors.Is(err, store.ErrNotFound) {
		return m, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return m, true
}

func (e *testEnv) logProactive(t *testing.T, user, kind string, at time.Time) {
	t.Helper()
	err := e.st.AppendProactiveLog(context.Background(), store.ProactiveLogEntry{
		ConversationKey: testConvKey, PlatformUserID: user, Kind: kind, TaskID: "task_seed", CreatedAt: at.UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) proactiveCount(t *testing.T, user, kind string) int {
	t.Helper()
	n, err := e.st.CountProactive(context.Background(), testConvKey, user, kind, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *testEnv) modelCalls() int { return e.short.Calls() + e.long.Calls() }

// requireNoProactive submits ev and requires it to be only observed, without
// an evaluation or a model call.
func (e *testEnv) requireNoProactive(t *testing.T, ev conloomapi.Event) {
	t.Helper()
	calls, evaluated := e.modelCalls(), e.proactiveCount(t, "", store.ProactiveEvaluated)
	resp := e.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
		t.Fatalf("%q from %s: response %+v, want observe", ev.Message.Text, ev.Actor.PlatformUserID, resp)
	}
	time.Sleep(80 * time.Millisecond)
	if n := e.proactiveCount(t, "", store.ProactiveEvaluated); n != evaluated {
		t.Fatalf("%q from %s: evaluations %d -> %d, want unchanged", ev.Message.Text, ev.Actor.PlatformUserID, evaluated, n)
	}
	if n := e.modelCalls(); n != calls {
		t.Fatalf("%q from %s: model calls %d -> %d, want unchanged", ev.Message.Text, ev.Actor.PlatformUserID, calls, n)
	}
}

// requireProactive submits ev and requires a new proactive task, returning it
// once finished.
func (e *testEnv) requireProactive(t *testing.T, ev conloomapi.Event) conloomapi.Task {
	t.Helper()
	resp := e.submit(t, ev)
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" || resp.Merged {
		t.Fatalf("%q from %s: response %+v, want a new proactive task", ev.Message.Text, ev.Actor.PlatformUserID, resp)
	}
	task := e.waitTask(t, resp.TaskID)
	if task.Meta.Trigger != conloomapi.TriggerProactive {
		t.Fatalf("task trigger = %q, want proactive", task.Meta.Trigger)
	}
	return task
}

func skipOnProactive(req map[string]any) (int, string) {
	if strings.Contains(lastContent(req), "【提示】没有人 @ 你") {
		return 200, SkipMarker
	}
	return 200, fakeReply
}

func TestEngagement_MentionCreatesMemberWithDefaultLevel(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withProactive(func(p *conloomapi.ProactivePolicy) {
		p.DefaultLevel = conloomapi.ProactiveMid
	})})
	before := time.Now()
	env.engage(t, "20001", topicText)
	m, ok := env.member(t, "20001")
	if !ok {
		t.Fatal("no member row after a mention")
	}
	if m.ProactiveLevel != conloomapi.ProactiveMid || m.LevelSetByUser {
		t.Errorf("member = %+v, want default level mid not set by user", m)
	}
	if m.LastEngagedAt.Before(before.Add(-time.Second)) || m.LastEngagedAt.After(time.Now()) {
		t.Errorf("last engaged at %v, want about now", m.LastEngagedAt)
	}
	if m.Interests[topicTerm] != 1 || m.Interests["烘焙"] != 1 {
		t.Errorf("interests = %v, want terms of the mention", m.Interests)
	}

	env.engage(t, "20002", "这个我知道")
	env.submit(t, env.groupEvent("20002", "你说咖啡怎么烘焙", replyTo("msg_none")))
	env.submit(t, env.groupEvent("20003", "咖啡豆烘焙大家都聊"))
	env.waitTask(t, env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "我在研究咖啡")).TaskID)
	if _, ok := env.member(t, "20003"); ok {
		t.Error("an observed message created a member row")
	}
	if _, err := env.st.GetMember(context.Background(),
		store.ConversationKey(testBot, "qq", conloomapi.ConvPrivate, "private_29999"), "29999"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("private chat member lookup err = %v, want ErrNotFound", err)
	}
	if m2, ok := env.member(t, "20002"); !ok || m2.Interests[topicTerm] != 0 {
		t.Errorf("member 20002 = %+v (found %v), want a row without terms of the observed message", m2, ok)
	}

	env.engage(t, "20001", "咖啡还是茶")
	m, _ = env.member(t, "20001")
	if got, want := m.Interests[topicTerm], 1*interest.Decay+1; got != want {
		t.Errorf("weight of %s = %v, want %v", topicTerm, got, want)
	}
}

func TestEngagement_UserSetLevelPreserved(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.engage(t, "20001", topicText)
	if got := env.command(t, "20001", "/主动 高"); got != CopyProactiveSet(conloomapi.ProactiveHigh) {
		t.Fatalf("/主动 高 = %q", got)
	}
	env.engage(t, "20001", "再聊聊咖啡")
	m, _ := env.member(t, "20001")
	if m.ProactiveLevel != conloomapi.ProactiveHigh || !m.LevelSetByUser {
		t.Fatalf("member = %+v, want user-set high kept", m)
	}

	env.engage(t, "20002", topicText)
	if m, _ := env.member(t, "20002"); m.ProactiveLevel != conloomapi.ProactiveLow || m.LevelSetByUser {
		t.Fatalf("member 20002 = %+v, want default low", m)
	}
}

func TestEngagement_CommandsDoNotCount(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.command(t, "20001", "/help")
	env.command(t, "20001", "/主动")
	ev := env.groupEvent("20001", "/persona", mentionBot)
	env.getTask(t, env.submit(t, ev).TaskID)
	if m, ok := env.member(t, "20001"); ok {
		t.Fatalf("commands created a member row: %+v", m)
	}
}

func TestProactive_MatchingTopicCreatesTask(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation})
	env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
	m, _ := env.member(t, "20001")

	ev := env.groupEvent("20001", matchText)
	task := env.requireProactive(t, ev)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("proactive task = %+v", task)
	}
	a := task.Actions[0]
	if a.Content.Text != fakeReply || a.ReplyToMessageID != ev.Message.ID || a.ReplyToPlatformMessageID != ev.Message.PlatformMessageID {
		t.Errorf("action = %+v, want a quoted reply to %s", a, ev.Message.ID)
	}
	if n := env.modelCalls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
	last := lastContent(env.short.Requests()[0])
	want := matchText + "\n\n" + CopyDirectiveProactive(interest.Top(m.Interests, 5))
	if !strings.HasSuffix(last, want) {
		t.Errorf("proactive request current = %q, want suffix %q", last, want)
	}
	if env.proactiveCount(t, "20001", store.ProactiveEvaluated) != 1 || env.proactiveCount(t, "20001", store.ProactiveSent) != 1 {
		t.Errorf("proactive log: evaluated %d sent %d, want 1 and 1",
			env.proactiveCount(t, "20001", store.ProactiveEvaluated), env.proactiveCount(t, "20001", store.ProactiveSent))
	}
	waitUntil(t, "sent metric", func() bool { return env.counter(t, "conloom_proactive_total", "result", "sent") == 1 })
	if n := env.counter(t, "conloom_proactive_total", "result", "evaluated"); n != 1 {
		t.Errorf("evaluated metric = %v, want 1", n)
	}
	if !env.inContinuation("20001") {
		t.Error("a proactive reply did not open the continuation window")
	}
	if strings.Contains(env.logs.String(), topicTerm) || strings.Contains(env.logs.String(), "烘焙") {
		t.Error("interest terms or message text leaked into logs")
	}
}

func TestCopyDirectiveProactive(t *testing.T) {
	got := CopyDirectiveProactive([]string{"咖啡", "烘焙", "go"})
	want := "【提示】没有人 @ 你，这是你在主动搭话。这个人以前常和你聊：咖啡、烘焙、go。只有当你能自然、简短地接上这个话题时才回复（一两句）；如果不合适、话题无关、或对方明显在和别人聊，只输出 [[SKIP]]，不要输出其他内容。"
	if got != want {
		t.Fatalf("CopyDirectiveProactive = %q, want %q", got, want)
	}
}

func TestProactive_NotEligibleExpiredOrOff(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-721*time.Hour))
	env.seedMember(t, "20002", conloomapi.ProactiveOff, time.Now().Add(-time.Hour))
	env.requireNoProactive(t, env.groupEvent("20001", matchText))
	env.requireNoProactive(t, env.groupEvent("20002", matchText))
	env.requireNoProactive(t, env.groupEvent("20003", matchText))
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-719*time.Hour))
	if task := env.requireProactive(t, env.groupEvent("20001", matchText)); len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
}

func TestProactive_FiltersMentionsRepliesCommandsShortText(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	other := env.groupEvent("20002", "随便说一句")
	env.submit(t, other)

	env.requireNoProactive(t, env.groupEvent("20001", "咖啡豆"))
	env.requireNoProactive(t, env.groupEvent("20001", matchText, mentionUser("user_20002")))
	env.requireNoProactive(t, env.groupEvent("20001", matchText, func(ev *conloomapi.Event) {
		ev.Message.Mentions = append(ev.Message.Mentions, conloomapi.Mention{Type: "all", ID: "all"})
	}))
	env.requireNoProactive(t, env.groupEvent("20001", matchText, replyTo(other.Message.ID)))
	env.requireNoProactive(t, env.groupEvent("20001", matchText, replyTo("msg_unknown")))
	env.requireNoProactive(t, env.groupEvent("20001", "/"+matchText))
	env.requireNoProactive(t, env.groupEvent("20001", otherText))

	own := env.groupEvent("20001", "今晚加班")
	env.submit(t, own)
	if task := env.requireProactive(t, env.groupEvent("20001", matchText, replyTo(own.Message.ID))); len(task.Actions) != 1 {
		t.Fatalf("reply to own message: task = %+v", task)
	}
}

func TestProactive_PendingTaskOfUserBlocksEvaluation(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 300 * time.Millisecond}
	}})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	first := env.submit(t, env.groupEvent("20001", "在吗", mentionBot))
	second := env.submit(t, env.groupEvent("20001", matchText))
	if !second.Merged || second.TaskID != first.TaskID {
		t.Fatalf("message during the user's pending task = %+v, want merged into %s", second, first.TaskID)
	}
	env.waitTask(t, first.TaskID)
	if n := env.proactiveCount(t, "", store.ProactiveEvaluated); n != 0 {
		t.Fatalf("evaluations = %d, want 0", n)
	}
}

func TestProactive_DailyCooldownAndGroupLimits(t *testing.T) {
	noCooldown := func(p *conloomapi.ProactivePolicy) { p.UserCooldown = conloomapi.Duration{} }

	t.Run("daily", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withProactive(noCooldown)})
		env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
		day := env.o.dayStart(time.Now())
		env.logProactive(t, "20001", store.ProactiveSent, day.Add(-time.Second))
		env.logProactive(t, "20001", store.ProactiveSent, day.Add(-time.Minute))
		env.requireProactive(t, env.groupEvent("20001", matchText))
		env.requireProactive(t, env.groupEvent("20001", matchText))
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
		if n := env.proactiveCount(t, "20001", store.ProactiveSent); n != 4 {
			t.Fatalf("sent = %d, want 4 (2 yesterday, 2 today)", n)
		}
	})

	t.Run("cooldown", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{})
		env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		env.seedMember(t, "20002", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		env.logProactive(t, "20001", store.ProactiveSent, time.Now().Add(-29*time.Minute))
		env.logProactive(t, "20002", store.ProactiveSent, time.Now().Add(-31*time.Minute))
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
		env.requireProactive(t, env.groupEvent("20002", matchText))
		env.requireNoProactive(t, env.groupEvent("20002", matchText))
	})

	t.Run("group hourly", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withProactive(func(p *conloomapi.ProactivePolicy) {
			noCooldown(p)
			p.GroupHourlyLimit = 2
		})})
		env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		env.logProactive(t, "20002", store.ProactiveSent, time.Now().Add(-61*time.Minute))
		env.logProactive(t, "20003", store.ProactiveSent, time.Now().Add(-62*time.Minute))
		env.requireProactive(t, env.groupEvent("20001", matchText))
		env.logProactive(t, "20002", store.ProactiveSent, time.Now().Add(-5*time.Minute))
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
	})
}

func TestProactive_SendBudgetRecheckedAtSendTime(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	handler := func(map[string]any) (int, string) {
		started <- struct{}{}
		<-release
		return 200, fakeReply
	}
	env := newTestOrchestrator(t, testOpts{short: handler, long: handler, configure: withProactive(func(p *conloomapi.ProactivePolicy) {
		p.GroupHourlyLimit = 1
	})})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	resp := env.submit(t, env.groupEvent("20001", matchText))
	if resp.TaskID == "" {
		t.Fatalf("response = %+v, want a proactive task", resp)
	}
	<-started
	env.logProactive(t, "20002", store.ProactiveSent, time.Now())
	close(release)
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
		t.Fatalf("task over the group limit at send time = %+v, want no action", task)
	}
	if n := env.proactiveCount(t, "20001", store.ProactiveSent); n != 0 {
		t.Fatalf("sent = %d, want 0", n)
	}
}

func TestProactive_LevelOffWhileQueuedSendsNothing(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	handler := func(map[string]any) (int, string) {
		started <- struct{}{}
		<-release
		return 200, fakeReply
	}
	env := newTestOrchestrator(t, testOpts{short: handler, long: handler})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	resp := env.submit(t, env.groupEvent("20001", matchText))
	<-started
	if got := env.command(t, "20001", "/主动 关"); got != CopyProactiveSet(conloomapi.ProactiveOff) {
		t.Fatalf("/主动 关 = %q", got)
	}
	close(release)
	task := env.waitTask(t, resp.TaskID)
	if len(task.Actions) != 0 {
		t.Fatalf("task = %+v, want no action after the user turned proactive off", task)
	}
}

func TestProactive_EvaluationBudgetCapsModelCalls(t *testing.T) {
	flood := func(t *testing.T, env *testEnv, users ...string) {
		t.Helper()
		for i := range 30 {
			resp := env.submit(t, env.groupEvent(users[i%len(users)], matchText+strconv.Itoa(i)))
			if resp.TaskID != "" {
				env.waitTask(t, resp.TaskID)
			}
		}
	}

	t.Run("per user per day", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{short: skipOnProactive, long: skipOnProactive})
		env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
		flood(t, env, "20001")
		if n := env.modelCalls(); n != 6 {
			t.Fatalf("model calls = %d, want 6 (3 x daily limit 2)", n)
		}
		if n := env.proactiveCount(t, "20001", store.ProactiveEvaluated); n != 6 {
			t.Fatalf("evaluations = %d, want 6", n)
		}
		if n := env.counter(t, "conloom_proactive_total", "result", "skipped"); n != 6 {
			t.Errorf("skipped metric = %v, want 6", n)
		}
	})

	t.Run("group per hour", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{short: skipOnProactive, long: skipOnProactive,
			configure: withProactive(func(p *conloomapi.ProactivePolicy) { p.GroupHourlyLimit = 1 })})
		env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		env.seedMember(t, "20002", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		flood(t, env, "20001", "20002")
		if n := env.modelCalls(); n != 3 {
			t.Fatalf("model calls = %d, want 3 (3 x group hourly limit 1)", n)
		}
	})

	t.Run("burst", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{short: skipOnProactive, long: skipOnProactive})
		env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
		for i := range 30 {
			env.submit(t, env.groupEvent("20001", matchText+strconv.Itoa(i)))
		}
		waitUntil(t, "tasks to finish", func() bool { return len(env.o.sched.Active(testConvKey)) == 0 })
		if n := env.modelCalls(); n > 12 {
			t.Fatalf("model calls = %d for 30 messages, want at most 12", n)
		}
	})
}

func TestProactive_PausedOrStaleNeverEvaluates(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	if got := env.command(t, "29999", "/pause"); got != CopyPaused {
		t.Fatalf("/pause = %q", got)
	}
	env.requireNoProactive(t, env.groupEvent("20001", matchText))
	if got := env.command(t, "29999", "/resume"); got != CopyResumed {
		t.Fatalf("/resume = %q", got)
	}
	env.requireNoProactive(t, env.groupEvent("20001", matchText, occurredAt(time.Now().Add(-5*time.Minute))))
	env.requireProactive(t, env.groupEvent("20001", matchText))
}

func TestProactive_GroupSwitchOff(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withProactive(func(p *conloomapi.ProactivePolicy) {
		p.UserCooldown = conloomapi.Duration{}
		p.DefaultLevel = conloomapi.ProactiveMid
	})})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	ctx := context.Background()
	before, err := env.policies.Current(ctx, testConvKey)
	if err != nil {
		t.Fatal(err)
	}

	if got := env.command(t, "29999", "/主动 全群 关"); got != CopyProactiveGroupOff {
		t.Fatalf("/主动 全群 关 = %q", got)
	}
	pv, err := env.policies.Current(ctx, testConvKey)
	if err != nil {
		t.Fatal(err)
	}
	want := before.Policy
	want.Proactive.Enabled = false
	if pv.Version != 1 || pv.Policy.Proactive != want.Proactive || pv.Policy.Reply != want.Reply || pv.Policy.Concurrency != want.Concurrency {
		t.Fatalf("policy after switch off = %+v (version %d), want %+v", pv.Policy, pv.Version, want)
	}
	env.requireNoProactive(t, env.groupEvent("20001", matchText))

	if got := env.command(t, "29999", "/proactive group on"); got != CopyProactiveGroupOn {
		t.Fatalf("/proactive group on = %q", got)
	}
	if pv, _ := env.policies.Current(ctx, testConvKey); pv.Version != 2 || pv.Policy.Proactive != before.Policy.Proactive {
		t.Fatalf("policy after switch on = %+v (version %d)", pv.Policy, pv.Version)
	}
	env.requireProactive(t, env.groupEvent("20001", matchText))
}

func TestProactive_SkipRecordsNoSent(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: skipOnProactive, long: skipOnProactive, configure: withContinuation})
	env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
	task := env.requireProactive(t, env.groupEvent("20001", matchText))
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
		t.Fatalf("skipped proactive task = %+v", task)
	}
	if env.proactiveCount(t, "20001", store.ProactiveEvaluated) != 1 || env.proactiveCount(t, "20001", store.ProactiveSent) != 0 {
		t.Fatal("want 1 evaluation and no sent record")
	}
	waitUntil(t, "skipped metric", func() bool { return env.counter(t, "conloom_proactive_total", "result", "skipped") == 1 })
	if n := env.counter(t, "conloom_proactive_total", "result", "sent"); n != 0 {
		t.Errorf("sent metric = %v, want 0", n)
	}
	if env.notifier.count(testBot) != 0 {
		t.Error("notifier called for a skipped proactive task")
	}
	if env.inContinuation("20001") {
		t.Error("a skipped proactive task opened the continuation window")
	}
}

func TestProactive_ModelFailureSendsNothing(t *testing.T) {
	fail := func(map[string]any) (int, string) { return 500, "boom" }
	env := newTestOrchestrator(t, testOpts{short: fail, long: fail})
	env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
	task := env.requireProactive(t, env.groupEvent("20001", matchText))
	if task.Status != conloomapi.TaskFailed || len(task.Actions) != 0 {
		t.Fatalf("failed proactive task = %+v, want no action", task)
	}
	if n := env.proactiveCount(t, "20001", store.ProactiveSent); n != 0 {
		t.Fatalf("sent = %d, want 0", n)
	}
}

func TestProactive_MentionMergedIntoProactiveTaskIsAnsweredNormally(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: skipOnProactive, long: skipOnProactive, configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 300 * time.Millisecond}
	}})
	env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
	first := env.submit(t, env.groupEvent("20001", matchText))
	second := env.submit(t, env.groupEvent("20001", "你怎么看", mentionBot))
	if first.TaskID == "" || !second.Merged || second.TaskID != first.TaskID {
		t.Fatalf("responses %+v / %+v, want the mention merged into the proactive task", first, second)
	}
	task := env.waitTask(t, first.TaskID)
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want a normal reply", task)
	}
	if c := lastContent(env.short.Requests()[0]); strings.Contains(c, "【提示】") {
		t.Errorf("request addressed to the bot carries a directive: %q", c)
	}
}

func TestCommand_ProactiveShowSetAndPermissions(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 100
	}})
	if got, want := env.command(t, "20001", "/主动"), CopyProactiveStatus("", false, 0, 0, true); got != want {
		t.Fatalf("/主动 before engaging = %q, want %q", got, want)
	}
	if got := env.command(t, "20001", "/主动 高"); got != CopyProactiveNotEligible {
		t.Fatalf("/主动 高 before engaging = %q", got)
	}
	if got := env.command(t, "20001", "/proactive high"); got != CopyProactiveNotEligible {
		t.Fatalf("/proactive high before engaging = %q", got)
	}
	if _, ok := env.member(t, "20001"); ok {
		t.Fatal("a refused level command created a member row")
	}

	env.engage(t, "20001", topicText)
	if got, want := env.command(t, "20001", "/主动"), CopyProactiveStatus(conloomapi.ProactiveLow, true, 0, 2, true); got != want {
		t.Fatalf("/主动 = %q, want %q", got, want)
	}
	for _, c := range []struct{ text, level string }{
		{"/主动 关", conloomapi.ProactiveOff},
		{"/主动 低", conloomapi.ProactiveLow},
		{"/主动 中", conloomapi.ProactiveMid},
		{"/proactive OFF", conloomapi.ProactiveOff},
		{"/proactive high", conloomapi.ProactiveHigh},
	} {
		if got := env.command(t, "20001", c.text); got != CopyProactiveSet(c.level) {
			t.Fatalf("%s = %q, want %q", c.text, got, CopyProactiveSet(c.level))
		}
		if m, _ := env.member(t, "20001"); m.ProactiveLevel != c.level || !m.LevelSetByUser {
			t.Fatalf("after %s member = %+v", c.text, m)
		}
	}
	env.logProactive(t, "20001", store.ProactiveSent, time.Now())
	if got, want := env.command(t, "20001", "/proactive"), CopyProactiveStatus(conloomapi.ProactiveHigh, true, 1, 8, true); got != want {
		t.Fatalf("/proactive = %q, want %q", got, want)
	}
	if got := env.command(t, "20001", "/主动 超高"); got != CopyProactiveUsage {
		t.Fatalf("/主动 超高 = %q", got)
	}

	env.seedMember(t, "20002", conloomapi.ProactiveLow, time.Now().Add(-800*time.Hour))
	if got := env.command(t, "20002", "/主动 低"); got != CopyProactiveNotEligible {
		t.Fatalf("expired /主动 低 = %q", got)
	}

	for _, text := range []string{"/主动 全群 关", "/proactive group off"} {
		if got := env.command(t, "20001", text); got != CopyAdminOnly {
			t.Fatalf("non-admin %s = %q", text, got)
		}
	}
	if pv, _ := env.policies.Current(context.Background(), testConvKey); pv.Version != 0 || !pv.Policy.Proactive.Enabled {
		t.Fatalf("policy after non-admin switch = %+v (version %d)", pv.Policy.Proactive, pv.Version)
	}
	if got := env.command(t, "29999", "/主动 全群 开"); got != CopyProactiveGroupOn {
		t.Fatalf("/主动 全群 开 when already on = %q", got)
	}
	if pv, _ := env.policies.Current(context.Background(), testConvKey); pv.Version != 0 {
		t.Fatalf("switching on an enabled group stored version %d", pv.Version)
	}
	if got := env.command(t, "29999", "/主动 全群"); got != CopyProactiveUsage {
		t.Fatalf("/主动 全群 = %q", got)
	}

	for _, text := range []string{"/主动", "/主动 高", "/proactive group off"} {
		resp := env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", text))
		task := env.getTask(t, resp.TaskID)
		if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyProactivePrivateOnly {
			t.Fatalf("private %s = %+v", text, task)
		}
	}
	if !strings.Contains(CopyHelp("织语"), "/主动") {
		t.Errorf("help copy does not mention /主动: %q", CopyHelp("织语"))
	}
	if CopyProactiveNotEligible != "先 @ 我聊几句，才能开启主动搭话。" || CopyProactivePrivateOnly != "主动搭话只在群聊里使用。" {
		t.Error("fixed copy differs from the design")
	}
}

func TestMaintenance_PurgesOldProactiveLog(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.logProactive(t, "20001", store.ProactiveSent, time.Now().Add(-8*24*time.Hour))
	env.logProactive(t, "20001", store.ProactiveSent, time.Now().Add(-time.Hour))
	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "old proactive log purged", func() bool { return env.proactiveCount(t, "20001", store.ProactiveSent) == 1 })
}

// backdateContact sets the bot's last contact with user to d ago.
func (e *testEnv) backdateContact(user string, d time.Duration) {
	e.o.contacts.set(memberKey(testConvKey, user), time.Now().Add(-d))
}

func withContactCooldown(d time.Duration) func(*conloomapi.ProactivePolicy) {
	return func(p *conloomapi.ProactivePolicy) { p.ContactCooldown = conloomapi.Duration{Duration: d} }
}

func TestProactive_ContactCooldownDelaysProactive(t *testing.T) {
	t.Run("rule exit", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
			withContinuation(c)
			withContactCooldown(30 * time.Minute)(&c.DefaultGroupPolicy.Proactive)
		}})
		env.engage(t, "20001", topicText)
		waitUntil(t, "continuation", func() bool { return env.inContinuation("20001") })
		env.requireNoProactive(t, env.groupEvent("20001", "你看这个", mentionUser("user_20002")))
		if env.inContinuation("20001") {
			t.Fatal("continuation not ended by the rule exit")
		}
		env.requireNoProactive(t, env.groupEvent("20001", matchText))

		// The exit itself, not only the earlier reply, starts the cooldown.
		env.engage(t, "20001", "在吗")
		waitUntil(t, "continuation", func() bool { return env.inContinuation("20001") })
		env.backdateContact("20001", 31*time.Minute)
		env.requireNoProactive(t, env.groupEvent("20001", "你看这个", mentionUser("user_20002")))
		env.requireNoProactive(t, env.groupEvent("20001", matchText))

		env.backdateContact("20001", 31*time.Minute)
		env.requireProactive(t, env.groupEvent("20001", matchText))
		if n := env.modelCalls(); n != 3 {
			t.Fatalf("model calls = %d, want 3 (two mentions, one proactive)", n)
		}
	})

	t.Run("window lapse", func(t *testing.T) {
		window := 200 * time.Millisecond
		env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Reply.ContinuationWindow = conloomapi.Duration{Duration: window}
			withContactCooldown(30 * time.Minute)(&c.DefaultGroupPolicy.Proactive)
		}})
		env.engage(t, "20001", topicText)
		time.Sleep(window + 100*time.Millisecond)
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
		if n := env.modelCalls(); n != 1 {
			t.Fatalf("model calls = %d, want 1", n)
		}
		env.backdateContact("20001", 31*time.Minute)
		env.requireProactive(t, env.groupEvent("20001", matchText))
	})

	t.Run("contact cooldown, not user cooldown", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withProactive(func(p *conloomapi.ProactivePolicy) {
			p.UserCooldown = conloomapi.Duration{Duration: 30 * time.Minute}
			p.ContactCooldown = conloomapi.Duration{Duration: 10 * time.Minute}
		})})
		env.engage(t, "20001", topicText)
		env.backdateContact("20001", 9*time.Minute)
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
		env.backdateContact("20001", 10*time.Minute)
		env.requireProactive(t, env.groupEvent("20001", matchText))
	})
}

func TestProactive_DefaultsAllowProactiveRightAfterConversation(t *testing.T) {
	if cd := conloomapi.DefaultGroupPolicy().Proactive.ContactCooldown.Duration; cd != 0 {
		t.Fatalf("default contact_cooldown = %v, want 0", cd)
	}

	t.Run("window lapse", func(t *testing.T) {
		window := 200 * time.Millisecond
		env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Reply.ContinuationWindow = conloomapi.Duration{Duration: window}
		}})
		env.engage(t, "20001", topicText)
		time.Sleep(window + 100*time.Millisecond)
		task := env.requireProactive(t, env.groupEvent("20001", matchText))
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("proactive task = %+v, want one reply", task)
		}
		if n := env.modelCalls(); n != 2 {
			t.Fatalf("model calls = %d, want 2 (mention, proactive)", n)
		}
	})

	t.Run("rule exit", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{configure: withContinuation})
		env.engage(t, "20001", topicText)
		waitUntil(t, "continuation", func() bool { return env.inContinuation("20001") })
		env.requireNoProactive(t, env.groupEvent("20001", "你看这个", mentionUser("user_20002")))
		task := env.requireProactive(t, env.groupEvent("20001", matchText))
		if len(task.Actions) != 1 {
			t.Fatalf("proactive task = %+v, want one reply", task)
		}
	})

	t.Run("user cooldown still spaces proactive replies", func(t *testing.T) {
		env := newTestOrchestrator(t, testOpts{})
		env.engage(t, "20001", topicText)
		env.requireProactive(t, env.groupEvent("20001", matchText))
		env.requireNoProactive(t, env.groupEvent("20001", matchText))
	})
}

func TestProactive_RecheckedBeforeModelCall(t *testing.T) {
	queued := func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 300 * time.Millisecond}
		c.DefaultGroupPolicy.Proactive.UserCooldown = conloomapi.Duration{}
	}
	for _, c := range []struct{ name, user, command string }{
		{"level off", "20001", "/主动 关"},
		{"group switch off", "29999", "/主动 全群 关"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: queued})
			env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
			resp := env.submit(t, env.groupEvent("20001", matchText))
			if resp.TaskID == "" {
				t.Fatalf("response = %+v, want a proactive task", resp)
			}
			env.command(t, c.user, c.command)
			task := env.waitTask(t, resp.TaskID)
			if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "skipped" {
				t.Fatalf("task = %+v, want skipped without action", task)
			}
			if n := env.modelCalls(); n != 0 {
				t.Fatalf("model calls = %d, want 0", n)
			}
			if n := env.counter(t, "conloom_proactive_total", "result", "skipped"); n != 1 {
				t.Errorf("skipped metric = %v, want 1", n)
			}
		})
	}
}

func TestProactive_MessageToOthersNotMergedIntoProactiveTask(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 400 * time.Millisecond}
	}})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	other := env.groupEvent("20002", "随便说一句")
	env.submit(t, other)
	resp := env.submit(t, env.groupEvent("20001", matchText))
	if resp.TaskID == "" {
		t.Fatalf("response = %+v, want a proactive task", resp)
	}
	requireObserved(t, env, env.groupEvent("20001", "小红你说呢", mentionUser("user_20002")), "")
	requireObserved(t, env, env.groupEvent("20001", "我也觉得", replyTo(other.Message.ID)), "")
	if plain := env.submit(t, env.groupEvent("20001", "顺便说一句")); !plain.Merged || plain.TaskID != resp.TaskID {
		t.Fatalf("plain follow-up = %+v, want merged into %s", plain, resp.TaskID)
	}
	env.waitTask(t, resp.TaskID)
	rec, err := env.st.GetTask(context.Background(), testBot, resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.TriggerMessageIDs) != 2 {
		t.Fatalf("proactive task triggers = %v, want the candidate and the plain follow-up", rec.TriggerMessageIDs)
	}
}

func TestEngagement_ContinuationKeepsEligibilityTime(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation})
	engaged := time.Now().Add(-100 * time.Hour).UTC()
	env.seedMember(t, "20001", conloomapi.ProactiveLow, engaged)
	env.o.sched.MarkReplied(testConvKey, "20001", time.Now())
	resp := env.submit(t, env.groupEvent("20001", "我们聊聊拿铁"))
	if task := env.waitTask(t, resp.TaskID); task.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Fatalf("task = %+v, want a continuation task", task)
	}
	m, _ := env.member(t, "20001")
	if !m.LastEngagedAt.Equal(engaged) {
		t.Errorf("last engaged at %v after a continuation message, want unchanged %v", m.LastEngagedAt, engaged)
	}
	if m.Interests["拿铁"] != 1 {
		t.Errorf("interests = %v, want the continuation message's terms", m.Interests)
	}
	env.engage(t, "20001", "你好")
	if m, _ := env.member(t, "20001"); time.Since(m.LastEngagedAt) > time.Minute {
		t.Errorf("last engaged at %v after a mention, want about now", m.LastEngagedAt)
	}
}

func (e *testEnv) gauge(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) > 0 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return 0
}

func TestProactive_LowerAdmissionPriorityThanReplies(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	var mu sync.Mutex
	var order []string
	handler := func(req map[string]any) (int, string) {
		if calls.Add(1) == 1 {
			<-release
		}
		mu.Lock()
		order = append(order, lastContent(req))
		mu.Unlock()
		return 200, fakeReply
	}
	env := newTestOrchestrator(t, testOpts{short: handler, long: handler, configure: func(c *config.Config) {
		for _, name := range []string{"short_context", "long_context"} {
			p := c.ModelProfiles[name]
			p.Runtime.MaxConcurrency = 1
			c.ModelProfiles[name] = p
		}
		c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 2
	}})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))

	hold := env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "先占住模型"))
	waitUntil(t, "the first model call", func() bool { return calls.Load() == 1 })
	pro := env.submit(t, env.groupEvent("20001", matchText))
	waitUntil(t, "the proactive task to wait for admission", func() bool { return env.gauge(t, "conloom_queue_depth") == 1 })
	mention := env.submit(t, env.groupEvent("20002", "你好呀", mentionBot))
	waitUntil(t, "the mention to wait for admission", func() bool { return env.gauge(t, "conloom_queue_depth") == 2 })
	close(release)
	for _, id := range []string{hold.TaskID, pro.TaskID, mention.TaskID} {
		env.waitTask(t, id)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || !strings.Contains(order[1], "你好呀") || !strings.Contains(order[2], matchText) {
		t.Fatalf("model call order = %q, want the mention before the proactive task", order)
	}
	rec, err := env.st.GetTask(context.Background(), testBot, pro.TaskID)
	if err != nil || rec.Priority != int(admission.PriorityBackground) {
		t.Fatalf("proactive task priority = %d, %v; want background", rec.Priority, err)
	}
}

func TestMaintenance_PrunesOldContacts(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.backdateContact("20001", 25*time.Hour)
	env.backdateContact("20002", 23*time.Hour)
	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "old contact pruned", func() bool {
		_, ok := env.o.contacts.get(memberKey(testConvKey, "20001"))
		return !ok
	})
	if _, ok := env.o.contacts.get(memberKey(testConvKey, "20002")); !ok {
		t.Fatal("a contact younger than 24h was pruned")
	}
}
