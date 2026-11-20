package core

import (
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// wakeStepTools are the fake tools of wakeStepEnv: now a lookup, the others
// state tools.
type wakeStepTools struct {
	now                                      *recTool
	adjust, add, remove, setStatus, setScene stateTool
}

func (w wakeStepTools) stateCalls() map[string]int {
	return map[string]int{"adjust_buff": w.adjust.count(), "add_buff": w.add.count(), "remove_buff": w.remove.count(),
		"set_status": w.setStatus.count(), "set_scene": w.setScene.count()}
}

// wakeStepEnv is newWakeEnv in state step mode mode whose bot enables the
// skills buff, status and scene, whose state tools are stateTools and whose
// profiles turn thinking on; its status return of 洗澡 after 小明's
// "我去洗个澡" is due at the clock's time.
func wakeStepEnv(t *testing.T, mode string, model func(req map[string]any) fakeopenai.Reply) (*testEnv, *injectClock, wakeStepTools) {
	t.Helper()
	clock := newInjectClock(injectMorning)
	tools := wakeStepTools{now: &recTool{name: "now"}}
	for name, tool := range map[string]*stateTool{"adjust_buff": &tools.adjust, "add_buff": &tools.add,
		"remove_buff": &tools.remove, "set_status": &tools.setStatus, "set_scene": &tools.setScene} {
		*tool = stateTool{&recTool{name: name}}
	}
	env := skillEnv(t, testOpts{shortTools: model, now: clock.now, configure: func(c *config.Config) {
		c.Bots[0].PrivateChat = "all"
		c.DefaultGroupPolicy.Reply.UserRateLimitPerMinute = 100
		for _, role := range []string{"short_context", "long_context"} {
			p := c.ModelProfiles[role]
			p.ExtraBody = map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}
			c.ModelProfiles[role] = p
		}
		c.Skills.StateStep = mode
	}}, []string{"buff", "status", "scene"}, tools.now, tools.adjust, tools.add, tools.remove, tools.setStatus, tools.setScene)
	return env, clock, tools
}

// armStatusReturn stores 小明's "我去洗个澡" and the status 洗澡 and moves the
// clock to its end, returning its status_return timer.
func (e *testEnv) armStatusReturn(t *testing.T, clock *injectClock) store.ConversationTimer {
	t.Helper()
	e.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(20 * time.Minute)
	timer := e.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)
	return timer
}

func TestStateStep_WakeTurnBuffToolsOnly(t *testing.T) {
	buffTools := []string{"adjust_buff", "add_buff", "remove_buff"}
	env, clock, tools := wakeStepEnv(t, config.StateStepAfterReply, func(req map[string]any) fakeopenai.Reply {
		switch {
		case !isStateRequest(t, req):
			return fakeopenai.Reply{Content: fakeReply}
		case hasToolResult(req):
			return fakeopenai.Reply{Content: "随便写点"}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "s1", Name: "adjust_buff", Arguments: `{"name":"心情","delta":5}`}}}
	})
	wake := env.fireWake(t, env.armStatusReturn(t, clock))
	if wake.Status != conloomapi.TaskSucceeded || len(wake.Actions) != 1 || wake.Actions[0].Content.Text != fakeReply {
		t.Fatalf("wake task = %+v, want one reply", wake)
	}
	waitUntil(t, "adjust_buff called", func() bool { return tools.adjust.count() == 1 })
	waitStateIdle(t, env)

	reqs := env.short.Requests()
	if len(reqs) < 2 || isStateRequest(t, reqs[0]) {
		t.Fatalf("requests = %d, want the wake reply first", len(reqs))
	}
	reply := reqs[0]
	if got := offeredTools(reply); len(got) != 0 {
		t.Errorf("wake reply tools = %v, want none", got)
	}
	if got := thinking(reply); got != false {
		t.Errorf("wake reply enable_thinking = %v, want false", got)
	}
	loaded, err := skills.Load("")
	if err != nil {
		t.Fatal(err)
	}
	sys := systemText(t, reply)
	if !strings.Contains(sys, loaded["buff"].Body+"\n"+copyReplyStepNote) {
		t.Errorf("wake reply system prompt lacks the buff section and the note: %q", sys)
	}
	if strings.Contains(sys, loaded["status"].Body) || strings.Contains(sys, loaded["scene"].Body) {
		t.Errorf("wake reply system prompt describes other skills: %q", sys)
	}

	states := reqs[1:]
	if n := len(stateRequests(t, states)); n != len(states) || len(firstRounds(states)) != 1 {
		t.Fatalf("after the wake reply: %d requests, %d state requests, %d first rounds; want one state step",
			len(states), n, len(firstRounds(states)))
	}
	directive := copyStateStepWake + "\n\n你刚才发的：\n「" + fakeReply + "」"
	for i, req := range states {
		if got := offeredTools(req); !reflect.DeepEqual(got, buffTools) {
			t.Errorf("state request %d tools = %v, want %v", i, got, buffTools)
		}
		if got := thinking(req); got != true {
			t.Errorf("state request %d enable_thinking = %v, want true", i, got)
		}
		last := lastUserText(t, req)
		if last != directive && !strings.HasSuffix(last, "\n"+directive) {
			t.Errorf("state request %d last user message = %q, want it to end with %q", i, last, directive)
		}
		if text := requestText(t, req); strings.Contains(text, "【当前消息】") || strings.Contains(text, stateRepliesHeading) {
			t.Errorf("state request %d has a current message or the reply heading:\n%s", i, text)
		}
	}

	if got, want := tools.stateCalls(), map[string]int{"adjust_buff": 1, "add_buff": 0, "remove_buff": 0, "set_status": 0, "set_scene": 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("state tool calls = %v, want %v", got, want)
	}
	invs := tools.adjust.invocations()
	if len(invs) != 1 {
		t.Fatalf("adjust_buff invocations = %+v", invs)
	}
	if inv := invs[0]; len(inv.TriggerMessageIDs) != 0 || inv.DisplayName != "小明" || inv.TaskID != wake.TaskID ||
		inv.ConversationKey != privateConvKey || inv.PlatformUserID != privateUser {
		t.Errorf("adjust_buff invocation = %+v, want the wake task's without triggers", inv)
	}
	if got := env.getTask(t, wake.TaskID); !reflect.DeepEqual(got.Actions, wake.Actions) {
		t.Errorf("actions after the state step = %+v, want %+v", got.Actions, wake.Actions)
	}
}

func TestStateStep_WithheldWakeHasNoStep(t *testing.T) {
	g, entered := newGate(), make(chan struct{})
	var calls atomic.Int32
	env, clock, tools := wakeStepEnv(t, config.StateStepAfterReply, func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) {
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "s1", Name: "adjust_buff", Arguments: "{}"}}}
		}
		if calls.Add(1) == 1 {
			close(entered)
			g.wait()
		}
		return fakeopenai.Reply{Content: fakeReply}
	})
	t.Cleanup(g.open)
	wakeID := env.startWake(t, env.armStatusReturn(t, clock))
	waitClosed(t, entered, "the wake reply in the model")
	clock.set(clock.now().Add(time.Second))
	env.seedPrivateMessage(t, clock, "我回来了")
	g.open()

	env.waitWake(t, wakeID)
	requireCancelledWithoutActions(t, env, wakeID)
	if !strings.Contains(env.logs.String(), "reason=user_message") {
		t.Errorf("logs lack the withheld wake reply")
	}
	waitStateIdle(t, env)
	if n := len(stateRequests(t, env.short.Requests())); n != 0 {
		t.Errorf("state requests = %d, want none", n)
	}
	for name, n := range tools.stateCalls() {
		if n != 0 {
			t.Errorf("%s called %d times", name, n)
		}
	}
}

func TestStateStep_WakeInlineUnchanged(t *testing.T) {
	env, clock, tools := wakeStepEnv(t, config.StateStepInline, func(map[string]any) fakeopenai.Reply {
		return fakeopenai.Reply{Content: fakeReply}
	})
	wake := env.fireWake(t, env.armStatusReturn(t, clock))
	if wake.Status != conloomapi.TaskSucceeded || len(wake.Actions) != 1 {
		t.Fatalf("wake task = %+v, want one reply", wake)
	}
	waitStateIdle(t, env)
	req := wakeRequest(t, env)
	if got, want := offeredTools(req), []string{"adjust_buff", "add_buff", "remove_buff"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wake reply tools = %v, want %v", got, want)
	}
	if got := thinking(req); got != true {
		t.Errorf("wake reply enable_thinking = %v, want true", got)
	}
	if sys := systemText(t, req); strings.Contains(sys, copyReplyStepNote) {
		t.Errorf("wake reply system prompt has the reply step note: %q", sys)
	}
	for name, n := range tools.stateCalls() {
		if n != 0 {
			t.Errorf("%s called %d times", name, n)
		}
	}
}

func TestStateDirective_WakeCopy(t *testing.T) {
	const wakeInstruction = "【状态更新】这一步不是回复，不会发给对方。你刚才主动给对方发了下面的消息，对方还没有回。判断你的状态有没有因此变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。"
	if copyStateStepWake != wakeInstruction {
		t.Errorf("copyStateStepWake = %q", copyStateStepWake)
	}
	if got := stateDirective([]stateTurn{{reply: "回来啦", wake: true}}); got != wakeInstruction+"\n\n你刚才发的：\n「回来啦」" {
		t.Errorf("one wake turn: %q", got)
	}
	if got := stateDirective([]stateTurn{{reply: "回来啦", wake: true}, {reply: "在呢", wake: true}}); got != wakeInstruction+"\n\n你刚才发的：\n「回来啦」\n「在呢」" {
		t.Errorf("two wake turns: %q", got)
	}
	if got := stateDirective([]stateTurn{{reply: "回来啦", wake: true}, {reply: "好呀"}}); got != copyStateStep+"\n\n你刚才的回复：\n「回来啦」\n「好呀」" {
		t.Errorf("a wake turn and a reply: %q", got)
	}
}
