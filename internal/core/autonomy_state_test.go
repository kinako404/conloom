package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const stateHeader = "【你的状态"

// stateEnv is a test orchestrator at injectMorning in which everyone may chat
// privately and whose bot enables botSkills (set_status, adjust_buff and now
// are fake tools).
func stateEnv(t *testing.T, botSkills []string, configure func(*config.Config)) (*testEnv, *injectClock) {
	t.Helper()
	clock := newInjectClock(injectMorning)
	env := skillEnv(t, testOpts{now: clock.now, configure: func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		if configure != nil {
			configure(c)
		}
	}}, botSkills, &recTool{name: "set_status"}, &recTool{name: "adjust_buff"}, &recTool{name: "now"})
	return env, clock
}

// seedStatus stores activity, from started to until, as the conversation's
// status.
func (e *testEnv) seedStatus(t *testing.T, convKey, activity string, started, until time.Time) {
	t.Helper()
	err := e.st.SetStatus(context.Background(), store.ConversationStatus{
		ConversationKey: convKey, Activity: activity, TaskID: "task_seed", StartedAt: started, UntilAt: until,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// seedBar stores v as the conversation's bar name, written at at.
func (e *testEnv) seedBar(t *testing.T, convKey, name string, v float64, at time.Time) {
	t.Helper()
	_, err := e.st.UpdateBuff(context.Background(), convKey, name,
		store.BuffEvent{Source: store.BuffSourcePanel, Reason: "测试", CreatedAt: at},
		func(store.BuffValue, bool) (store.BuffValue, float64, error) {
			return store.BuffValue{Value: v, Day: at.In(e.o.loc).Format(time.DateOnly), UpdatedAt: at}, 0, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

// statePrivateRequest sends text from 小明 (20001) in his private chat and
// returns the model request of the reply.
func (e *testEnv) statePrivateRequest(t *testing.T, clock *injectClock, text string) map[string]any {
	t.Helper()
	ev := e.event(conloomapi.ConvPrivate, "20001", "20001", text, withName("小明"), clock.stamp)
	task := e.waitTask(t, e.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want the reply sent", task)
	}
	reqs := e.short.Requests()
	return reqs[len(reqs)-1]
}

// defaultStateBars are the default bars at values.
func defaultStateBars(values ...float64) []autonomy.StateBar {
	var out []autonomy.StateBar
	for i, b := range conloomapi.DefaultBars() {
		out = append(out, autonomy.StateBar{Name: b.Name, Description: b.Description, Value: values[i]})
	}
	return out
}

func TestState_InjectedInPrivate(t *testing.T) {
	env, clock := stateEnv(t, []string{"status", "buff"}, nil)
	key := privKeyOf("20001")
	now := clock.now()
	started, until := now.Add(-10*time.Minute), now.Add(20*time.Minute)
	env.seedStatus(t, key, "洗澡", started, until)
	// Six hours (the half-life) ago 82, decayed to 71 now.
	env.seedBar(t, key, "心情", 82, now.Add(-6*time.Hour))

	req := env.statePrivateRequest(t, clock, "洗完了吗")
	want := autonomy.RenderState(&autonomy.StateStatus{Activity: "洗澡", StartedAt: started, UntilAt: until},
		defaultStateBars(autonomy.Decay(82, 60, 6*time.Hour, 6*time.Hour), 70), env.o.loc)
	for _, line := range []string{"正在：洗澡（10:50 开始，预计 11:20 结束", "心情：偏高", "精力：偏高"} {
		if !strings.Contains(want, line) {
			t.Fatalf("test block lacks %q:\n%s", line, want)
		}
	}
	last := lastContent(req)
	if !strings.HasPrefix(last, want+"\n\n【当前消息】") {
		t.Fatalf("last user message = %q, want it to start with the state block\n%s", last, want)
	}
	if sys := requestMessages(t, req)[0]["content"].(string); strings.Contains(sys, stateHeader) || strings.Contains(sys, "正在：") {
		t.Errorf("system prompt holds the state:\n%s", sys)
	}
}

func TestState_ExpiredStatusHidden(t *testing.T) {
	env, clock := stateEnv(t, []string{"status"}, nil)
	now := clock.now()
	env.seedStatus(t, privKeyOf("20001"), "洗澡", now.Add(-20*time.Minute), now)

	if text := requestText(t, env.statePrivateRequest(t, clock, "在吗")); strings.Contains(text, stateHeader) || strings.Contains(text, "正在：") {
		t.Errorf("request shows a status that ended:\n%s", text)
	}
}

func TestState_BarsNeedBuffSkill(t *testing.T) {
	env, clock := stateEnv(t, []string{"status"}, nil)
	key := privKeyOf("20001")
	now := clock.now()
	started, until := now.Add(-10*time.Minute), now.Add(20*time.Minute)
	env.seedStatus(t, key, "洗澡", started, until)
	env.seedBar(t, key, "心情", 10, now)

	req := env.statePrivateRequest(t, clock, "在吗")
	want := autonomy.RenderState(&autonomy.StateStatus{Activity: "洗澡", StartedAt: started, UntilAt: until}, nil, env.o.loc)
	if last := lastContent(req); !strings.HasPrefix(last, want+"\n\n【当前消息】") {
		t.Fatalf("last user message = %q, want it to start with the status only\n%s", last, want)
	}
	if text := requestText(t, req); strings.Contains(text, "心情：") || strings.Contains(text, "精力：") {
		t.Errorf("request shows bars without the buff skill:\n%s", text)
	}
}

func TestState_IgnoresRemovedBars(t *testing.T) {
	bars := []conloomapi.PersonaBar{
		{Name: "耐心", Baseline: 50, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}, Description: "高=有耐心，低=急躁"},
		{Name: "精力", Baseline: 70, HalfLife: conloomapi.Duration{Duration: 4 * time.Hour}, Description: "高=有精神，低=累、困"},
	}
	env, clock := stateEnv(t, []string{"buff"}, func(c *config.Config) {
		doc := conloomapi.DefaultPersona()
		doc.Bars = bars
		c.PersonaTemplates["default"] = doc
	})
	key := privKeyOf("20001")
	now := clock.now()
	env.seedBar(t, key, "饥饿", 10, now)
	env.seedBar(t, key, "精力", 15, now)

	req := env.statePrivateRequest(t, clock, "在吗")
	want := autonomy.RenderState(nil, []autonomy.StateBar{
		{Name: "耐心", Description: bars[0].Description, Value: 50},
		{Name: "精力", Description: bars[1].Description, Value: 15},
	}, env.o.loc)
	if last := lastContent(req); !strings.HasPrefix(last, want+"\n\n【当前消息】") {
		t.Fatalf("last user message = %q, want it to start with the persona's bars in its order\n%s", last, want)
	}
	if text := requestText(t, req); strings.Contains(text, "饥饿") || strings.Contains(text, "心情：") {
		t.Errorf("request shows a bar the persona does not have:\n%s", text)
	}
}

func TestState_NeverInGroups(t *testing.T) {
	env, clock := stateEnv(t, []string{"status", "buff"}, nil)
	now := clock.now()
	env.seedStatus(t, testConvKey, "洗澡", now.Add(-10*time.Minute), now.Add(20*time.Minute))
	env.seedBar(t, testConvKey, "心情", 90, now)

	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "在吗", withName("小明"), mentionBot, clock.stamp)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if text := requestText(t, env.short.Requests()[0]); strings.Contains(text, stateHeader) || strings.Contains(text, "正在：") ||
		strings.Contains(text, "心情：") {
		t.Errorf("group request shows the state:\n%s", text)
	}
}

// stateFailStore fails the status or the bar reads.
type stateFailStore struct {
	store.Store
	status, bars bool
}

func (s *stateFailStore) GetStatus(ctx context.Context, convKey string) (store.ConversationStatus, error) {
	if s.status {
		return store.ConversationStatus{}, errors.New("test: status unavailable")
	}
	return s.Store.GetStatus(ctx, convKey)
}

func (s *stateFailStore) GetBuffs(ctx context.Context, convKey string) ([]store.BuffValue, error) {
	if s.bars {
		return nil, errors.New("test: bars unavailable")
	}
	return s.Store.GetBuffs(ctx, convKey)
}

func TestState_ReadErrorsLeaveBlockOut(t *testing.T) {
	for _, c := range []struct {
		name string
		fail stateFailStore
	}{
		{"status", stateFailStore{status: true}},
		{"bars", stateFailStore{bars: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := stateEnv(t, []string{"status", "buff"}, nil)
			key := privKeyOf("20001")
			now := clock.now()
			env.seedStatus(t, key, "洗澡", now.Add(-10*time.Minute), now.Add(20*time.Minute))
			env.seedBar(t, key, "心情", 90, now)
			fail := c.fail
			fail.Store = env.o.st
			env.o.st = &fail

			if text := requestText(t, env.statePrivateRequest(t, clock, "在吗")); strings.Contains(text, stateHeader) {
				t.Errorf("request shows a state that could not be read:\n%s", text)
			}
			if logs := env.logs.String(); !strings.Contains(logs, `msg="load hidden state failed"`) {
				t.Errorf("logs lack the read failure:\n%s", logs)
			}
		})
	}
}

func TestSkills_StatusAndBuffPrivateOnly(t *testing.T) {
	env, clock := stateEnv(t, []string{"time", "status", "buff"}, nil)
	env.waitTask(t, env.submit(t, env.groupEvent("20001", "去洗澡吧", withName("小明"), mentionBot, clock.stamp)).TaskID)
	group := env.short.Requests()[0]
	if got := offeredTools(group); !reflect.DeepEqual(got, []string{"now"}) {
		t.Errorf("group request tools = %v, want [now]", got)
	}
	if sys := requestMessages(t, group)[0]["content"].(string); strings.Contains(sys, "set_status") || strings.Contains(sys, "adjust_buff") {
		t.Errorf("group system prompt describes private-only skills:\n%s", sys)
	}

	private := env.statePrivateRequest(t, clock, "去洗澡吧")
	if got := offeredTools(private); !reflect.DeepEqual(got, []string{"now", "set_status", "adjust_buff"}) {
		t.Errorf("private request tools = %v, want [now set_status adjust_buff]", got)
	}
}
