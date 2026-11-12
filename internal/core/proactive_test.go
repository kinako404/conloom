package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// joinDirective is the start of the hint appended to join requests.
const joinDirective = "【提示】群里大家正在聊天"

func skipOnProactive(req map[string]any) (int, string) {
	if strings.Contains(lastContent(req), joinDirective) {
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

func TestEngagement_ContinuationKeepsEligibilityTime(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withContinuation})
	engaged := time.Now().Add(-100 * time.Hour).UTC()
	env.seedMember(t, "20001", conloomapi.ProactiveLow, engaged)
	env.o.directReplied(testConvKey, "20001", time.Now())
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
