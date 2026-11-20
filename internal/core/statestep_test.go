package core

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// lastUserText returns the content of the last user message of req.
func lastUserText(t *testing.T, req map[string]any) string {
	t.Helper()
	msgs := requestMessages(t, req)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			c, _ := msgs[i]["content"].(string)
			return c
		}
	}
	return ""
}

// isStateRequest reports whether req belongs to a state step: its last user
// message carries the state step instruction.
func isStateRequest(t *testing.T, req map[string]any) bool {
	t.Helper()
	return strings.Contains(lastUserText(t, req), "【状态更新】")
}

// stateRequests returns the state step requests of reqs.
func stateRequests(t *testing.T, reqs []map[string]any) []map[string]any {
	t.Helper()
	return slices.DeleteFunc(slices.Clone(reqs), func(req map[string]any) bool { return !isStateRequest(t, req) })
}

// firstRounds returns the requests of reqs without tool results, i.e. the
// first request of each model call of a step.
func firstRounds(reqs []map[string]any) []map[string]any {
	return slices.DeleteFunc(slices.Clone(reqs), hasToolResult)
}

// waitStateIdle waits until no state step runs or is pending and no reply
// task holds one back.
func waitStateIdle(t *testing.T, env *testEnv) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !env.o.states.idle() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the state steps to finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// pendingStateEntries returns how many state step entries wait to run.
func pendingStateEntries(env *testEnv) int {
	s := env.o.states
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func (r *recTool) invocations() []skills.Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.invs)
}

// gate blocks fake handlers until opened.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate() *gate  { return &gate{ch: make(chan struct{})} }
func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }
func (g *gate) wait() { <-g.ch }

// waitClosed waits up to waitLimit for ch to close.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// stateModel answers a state request with a call to mark in its first round
// and with text after it, and any other request with reply.
func stateModel(t *testing.T, reply func(req map[string]any) fakeopenai.Reply) func(req map[string]any) fakeopenai.Reply {
	return func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) {
			if hasToolResult(req) {
				return fakeopenai.Reply{Content: "随便写点"}
			}
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "s1", Name: "mark", Arguments: "{}"}}}
		}
		return reply(req)
	}
}

func textReply(content string) func(map[string]any) fakeopenai.Reply {
	return func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Content: content} }
}

func noInvTime(inv skills.Invocation) skills.Invocation {
	inv.Now = time.Time{}
	return inv
}

func TestStateStep_RunsAfterReplyWithStateToolsOnly(t *testing.T) {
	env, now, mark := stepEnv(t, testOpts{shortTools: stateModel(t, func(req map[string]any) fakeopenai.Reply {
		if hasToolResult(req) {
			return fakeopenai.Reply{Content: fakeReply}
		}
		return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "c1", Name: "now", Arguments: "{}"}}}
	})}, config.StateStepAfterReply)
	written := recordUsage(t, env)
	task := env.waitTask(t, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v", task)
	}
	waitUntil(t, "mark called", func() bool { return mark.count() == 1 })
	waitStateIdle(t, env)

	reqs := env.short.Requests()
	states := stateRequests(t, reqs)
	if len(states) != 2 || len(reqs) != 4 || !isStateRequest(t, reqs[2]) || !isStateRequest(t, reqs[3]) {
		t.Fatalf("requests = %d, state requests = %d; want 2 reply requests followed by 2 state requests", len(reqs), len(states))
	}
	for i, req := range states {
		if got := offeredTools(req); !reflect.DeepEqual(got, []string{"mark"}) {
			t.Errorf("state request %d tools = %v, want [mark]", i, got)
		}
		if got := thinking(req); got != true {
			t.Errorf("state request %d enable_thinking = %v, want true", i, got)
		}
		sys := systemText(t, req)
		if !strings.Contains(sys, mixedBody) || strings.Contains(sys, copyReplyStepNote) {
			t.Errorf("state request %d system prompt = %q, want the section without the reply step note", i, sys)
		}
		last := lastUserText(t, req)
		if !strings.Contains(last, "我先去洗澡了") || !strings.HasSuffix(last, "你刚才的回复：\n「"+fakeReply+"」") {
			t.Errorf("state request %d last user message = %q", i, last)
		}
	}
	nowInv, markInv := now.invocations(), mark.invocations()
	if len(nowInv) != 1 || len(markInv) != 1 || !reflect.DeepEqual(noInvTime(markInv[0]), noInvTime(nowInv[0])) {
		t.Fatalf("mark invocations = %+v, want the reply's %+v", markInv, nowInv)
	}
	if inv := markInv[0]; inv.TaskID != task.TaskID || inv.ConversationKey != privateConvKey || inv.PlatformUserID != privateUser ||
		inv.DisplayName != "用户"+privateUser || len(inv.TriggerMessageIDs) != 1 || inv.IsAdmin {
		t.Errorf("mark invocation = %+v", inv)
	}

	if got := env.getTask(t, task.TaskID); !reflect.DeepEqual(got.Actions, task.Actions) {
		t.Errorf("actions after the state step = %+v, want %+v", got.Actions, task.Actions)
	}
	leased, err := env.st.LeaseOutbox(context.Background(), testBot, 10, time.Now().UTC(), time.Minute)
	if err != nil || len(leased) != 1 || leased[0].Action.Content.Text != fakeReply {
		t.Errorf("outbox = %+v, %v; want only the reply", leased, err)
	}

	var purposes []string
	for _, c := range written() {
		if c.TaskID != task.TaskID {
			t.Errorf("call %+v not attributed to the reply task", c)
		}
		purposes = append(purposes, c.Purpose)
	}
	if want := []string{store.PurposeChat, store.PurposeChat, store.PurposeState, store.PurposeState}; !slices.Equal(purposes, want) {
		t.Errorf("call purposes = %v, want %v", purposes, want)
	}
	if !strings.Contains(env.logs.String(), `msg="state step"`) || !strings.Contains(env.logs.String(), "outcome=done") {
		t.Errorf("logs lack the state step line")
	}
}

// requireNoStateStep waits for the state steps to settle and checks that none
// called the model or mark.
func requireNoStateStep(t *testing.T, env *testEnv, mark stateTool) {
	t.Helper()
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(privateConvKey) })
	waitStateIdle(t, env)
	if n := len(stateRequests(t, append(env.short.Requests(), env.long.Requests()...))); n != 0 {
		t.Errorf("state requests = %d, want none", n)
	}
	if mark.count() != 0 {
		t.Errorf("mark called %d times", mark.count())
	}
}

func TestStateStep_NoStepWithoutASentReply(t *testing.T) {
	failing := func(map[string]any) fakeopenai.Reply { return fakeopenai.Reply{Status: 500, Content: "down"} }
	for _, c := range []struct {
		name       string
		opts       testOpts
		status     string
		reviewWord bool
	}{
		{"skip", testOpts{shortTools: stateModel(t, textReply(SkipMarker))}, conloomapi.TaskSucceeded, false},
		{"blocked", testOpts{shortTools: stateModel(t, textReply("这里有禁词")), configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Review.BlockedWords = []string{"禁词"}
		}}, conloomapi.TaskSucceeded, true},
		{"both models fail", testOpts{shortTools: stateModel(t, failing), longTools: stateModel(t, failing)}, conloomapi.TaskFailed, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, _, mark := stepEnv(t, c.opts, config.StateStepAfterReply)
			task := env.waitTask(t, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
			if task.Status != c.status || c.reviewWord && task.Meta.ReviewResult != reviewResultBlocked {
				t.Fatalf("task = %+v", task)
			}
			requireNoStateStep(t, env, mark)
		})
	}

	t.Run("cancelled", func(t *testing.T) {
		g, entered := newGate(), make(chan struct{})
		var calls atomic.Int32
		env, _, mark := stepEnv(t, testOpts{shortTools: stateModel(t, func(map[string]any) fakeopenai.Reply {
			if calls.Add(1) == 1 {
				close(entered)
				g.wait()
			}
			return fakeopenai.Reply{Content: fakeReply}
		})}, config.StateStepAfterReply)
		t.Cleanup(g.open)
		resp := env.submit(t, env.privateEvent("我先去洗澡了"))
		waitClosed(t, entered, "the reply in the model")
		task, err := env.o.CancelTask(context.Background(), testBot, resp.TaskID)
		if err != nil || task.Status != conloomapi.TaskCancelled {
			t.Fatalf("CancelTask = %+v, %v", task, err)
		}
		requireNoStateStep(t, env, mark)
	})

	t.Run("superseded before stored", func(t *testing.T) {
		var armed atomic.Bool
		reached, proceed := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var calls atomic.Int32
		env, _, mark := stepEnv(t, testOpts{
			shortTools: stateModel(t, func(map[string]any) fakeopenai.Reply {
				if calls.Add(1) == 1 {
					armed.Store(true)
				}
				return fakeopenai.Reply{Content: fakeReply}
			}),
			now: func() time.Time {
				if armed.CompareAndSwap(true, false) {
					close(reached)
					<-proceed
				}
				return time.Now()
			},
		}, config.StateStepAfterReply)
		t.Cleanup(func() { once.Do(func() { close(proceed) }) })
		ev1 := env.privateEvent("帮我想个名字")
		first := env.submit(t, ev1)
		waitClosed(t, reached, "the runner to store its reply")
		ev2 := env.privateEvent("给猫起的")
		second := env.submit(t, ev2)
		once.Do(func() { close(proceed) })
		requireCancelledWithoutActions(t, env, first.TaskID)
		requireOneReply(t, env, second.TaskID)
		waitUntil(t, "mark called", func() bool { return mark.count() == 1 })
		waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(privateConvKey) })
		waitStateIdle(t, env)
		if n := len(firstRounds(stateRequests(t, env.short.Requests()))); n != 1 {
			t.Errorf("state steps = %d, want 1", n)
		}
		invs := mark.invocations()
		if len(invs) != 1 || invs[0].TaskID != second.TaskID ||
			!slices.Equal(invs[0].TriggerMessageIDs, []string{ev1.Message.ID, ev2.Message.ID}) {
			t.Errorf("mark invocations = %+v, want one for %s over both messages", invs, second.TaskID)
		}
	})
}

func TestStateStep_NextReplyPreemptsAndCoalesces(t *testing.T) {
	g, blocked := newGate(), make(chan struct{})
	var states atomic.Int32
	env, _, mark := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) {
			if states.Add(1) == 1 {
				close(blocked)
				g.wait()
				return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "late", Name: "mark", Arguments: "{}"}}}
			}
			return stateModel(t, nil)(req)
		}
		if strings.Contains(lastUserText(t, req), "第二句") {
			return fakeopenai.Reply{Content: "回复二"}
		}
		return fakeopenai.Reply{Content: "回复一"}
	}}, config.StateStepAfterReply)
	t.Cleanup(g.open)
	ev1 := env.groupEvent("20001", "第一句", mentionBot)
	t1 := env.waitTask(t, env.submit(t, ev1).TaskID)
	waitClosed(t, blocked, "the first state request")
	ev2 := env.groupEvent("20001", "第二句", mentionBot)
	t2 := env.waitTask(t, env.submit(t, ev2).TaskID)
	if len(t1.Actions) != 1 || len(t2.Actions) != 1 || t2.Actions[0].Content.Text != "回复二" {
		t.Fatalf("tasks = %+v, %+v", t1, t2)
	}
	g.open()
	waitUntil(t, "mark called", func() bool { return mark.count() == 1 })
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(testConvKey) })
	waitStateIdle(t, env)

	firsts := firstRounds(stateRequests(t, env.short.Requests()))
	if len(firsts) != 2 {
		t.Fatalf("state steps = %d, want the cancelled one and one more", len(firsts))
	}
	last := lastUserText(t, firsts[1])
	if !strings.Contains(last, "第一句") || !strings.Contains(last, "第二句") || !strings.HasSuffix(last, "「回复一」\n「回复二」") {
		t.Errorf("coalesced state request = %q", last)
	}
	invs := mark.invocations()
	if len(invs) != 1 || invs[0].TaskID != t2.TaskID || !slices.Equal(invs[0].TriggerMessageIDs, []string{ev1.Message.ID, ev2.Message.ID}) {
		t.Errorf("mark invocations = %+v", invs)
	}
	if !strings.Contains(env.logs.String(), "outcome=cancelled") {
		t.Errorf("logs lack the cancelled step")
	}
}

func TestStateStep_PreemptedAfterACallIsNotRepeated(t *testing.T) {
	g, blocked := newGate(), make(chan struct{})
	var seconds atomic.Int32
	env, _, mark := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) && hasToolResult(req) && seconds.Add(1) == 1 {
			close(blocked)
			g.wait()
			return fakeopenai.Reply{Content: "晚了"}
		}
		return stateModel(t, textReply(fakeReply))(req)
	}}, config.StateStepAfterReply)
	t.Cleanup(g.open)
	ev1 := env.groupEvent("20001", "第一句", mentionBot)
	env.waitTask(t, env.submit(t, ev1).TaskID)
	waitClosed(t, blocked, "the first step's second round")
	ev2 := env.groupEvent("20001", "第二句", mentionBot)
	t2 := env.waitTask(t, env.submit(t, ev2).TaskID)
	g.open()
	waitUntil(t, "mark called twice", func() bool { return mark.count() == 2 })
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(testConvKey) })
	waitStateIdle(t, env)
	invs := mark.invocations()
	if len(invs) != 2 || !slices.Equal(invs[0].TriggerMessageIDs, []string{ev1.Message.ID}) ||
		invs[1].TaskID != t2.TaskID || !slices.Equal(invs[1].TriggerMessageIDs, []string{ev2.Message.ID}) {
		t.Errorf("mark invocations = %+v, want one per step, the second over the second message only", invs)
	}
}

func TestStateStep_AbandonedReplyCancelsItsStep(t *testing.T) {
	g, blocked := newGate(), make(chan struct{})
	var states atomic.Int32
	env, _, mark := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) && states.Add(1) == 1 {
			close(blocked)
			g.wait()
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "late", Name: "mark", Arguments: "{}"}}}
		}
		return stateModel(t, textReply(fakeReply))(req)
	}}, config.StateStepAfterReply)
	t.Cleanup(g.open)
	ev1 := env.privateEvent("推荐一本书")
	first := env.submit(t, ev1)
	requireOneReply(t, env, first.TaskID)
	waitClosed(t, blocked, "the first state request")
	ev2 := env.privateEvent("要科幻的")
	second := env.submit(t, ev2)
	if second.TaskID == first.TaskID || second.Disposition != conloomapi.DispositionReply {
		t.Fatalf("follow-up response = %+v", second)
	}
	requireSupersededAudit(t, env, first.TaskID, second.TaskID, 1)
	requireOneReply(t, env, second.TaskID)
	g.open()
	waitUntil(t, "mark called", func() bool { return mark.count() == 1 })
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(privateConvKey) })
	waitStateIdle(t, env)
	if mark.count() != 1 {
		t.Errorf("mark called %d times, want 1", mark.count())
	}
	invs := mark.invocations()
	if len(invs) != 1 || invs[0].TaskID != second.TaskID ||
		!slices.Equal(invs[0].TriggerMessageIDs, []string{ev1.Message.ID, ev2.Message.ID}) {
		t.Errorf("mark invocations = %+v", invs)
	}
	firsts := firstRounds(stateRequests(t, env.short.Requests()))
	if len(firsts) != 2 {
		t.Fatalf("state steps = %d, want 2", len(firsts))
	}
	if last := lastUserText(t, firsts[1]); strings.Count(last, "「") != 1 || strings.Count(last, "推荐一本书") != 1 ||
		!strings.HasSuffix(last, "「"+fakeReply+"」") {
		t.Errorf("new step's last user message = %q", last)
	}
}

// blockingMark is mark whose first call blocks until g opens; entered closes
// when it starts, and inTool is set while it blocks.
func blockingMark(g *gate, entered chan struct{}, inTool *atomic.Bool) stateTool {
	var first atomic.Bool
	return stateTool{&recTool{name: "mark", fn: func(json.RawMessage) (string, error) {
		if first.CompareAndSwap(false, true) {
			inTool.Store(true)
			close(entered)
			g.wait()
			inTool.Store(false)
		}
		return "记下了", nil
	}}}
}

// overlapModel is stateModel that sets overlap when a state request arrives
// while inTool is set.
func overlapModel(t *testing.T, inTool, overlap *atomic.Bool) func(map[string]any) fakeopenai.Reply {
	model := stateModel(t, textReply(fakeReply))
	return func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) && inTool.Load() {
			overlap.Store(true)
		}
		return model(req)
	}
}

func TestStateStep_PanelChatsShareAScope(t *testing.T) {
	g, entered := newGate(), make(chan struct{})
	var inTool, overlap atomic.Bool
	mark := blockingMark(g, entered, &inTool)
	env := stepEnvOf(t, testOpts{shortTools: overlapModel(t, &inTool, &overlap)}, config.StateStepAfterReply, &recTool{name: "now"}, mark)
	t.Cleanup(g.open)
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	ma := env.submitPanel(t, a, "admin", "我在这边")
	ta := env.waitTask(t, env.panelResponse(t, ma).TaskID)
	waitClosed(t, entered, "the first step's call")
	mb := env.submitPanel(t, b, "admin", "我在那边")
	tb := env.waitTask(t, env.panelResponse(t, mb).TaskID)
	if ta.Status != conloomapi.TaskSucceeded || tb.Status != conloomapi.TaskSucceeded {
		t.Fatalf("tasks = %+v, %+v", ta, tb)
	}
	waitUntil(t, "the second chat's step to wait", func() bool { return pendingStateEntries(env) == 1 })
	g.open()
	waitUntil(t, "mark called twice", func() bool { return mark.count() == 2 })
	waitStateIdle(t, env)
	if overlap.Load() {
		t.Errorf("a state step ran while another of the scope was in a call")
	}
	invs := mark.invocations()
	if len(invs) != 2 || invs[0].ConversationKey != a || !slices.Equal(invs[0].TriggerMessageIDs, []string{ma}) ||
		invs[1].ConversationKey != b || !slices.Equal(invs[1].TriggerMessageIDs, []string{mb}) || invs[1].TaskID != tb.TaskID {
		t.Errorf("mark invocations = %+v", invs)
	}
}

func TestStateStep_GroupSpeakersRunInTurn(t *testing.T) {
	g, entered := newGate(), make(chan struct{})
	var inTool, overlap atomic.Bool
	mark := blockingMark(g, entered, &inTool)
	env := stepEnvOf(t, testOpts{shortTools: overlapModel(t, &inTool, &overlap)}, config.StateStepAfterReply, &recTool{name: "now"}, mark)
	t.Cleanup(g.open)
	ev1 := env.groupEvent("20001", "我是小明", mentionBot)
	env.waitTask(t, env.submit(t, ev1).TaskID)
	waitClosed(t, entered, "the first step's call")
	ev2 := env.groupEvent("29999", "我是小红", mentionBot)
	t2 := env.waitTask(t, env.submit(t, ev2).TaskID)
	if t2.Status != conloomapi.TaskSucceeded || len(t2.Actions) != 1 {
		t.Fatalf("second task = %+v", t2)
	}
	waitUntil(t, "the second speaker's step to wait", func() bool { return pendingStateEntries(env) == 1 })
	g.open()
	waitUntil(t, "mark called twice", func() bool { return mark.count() == 2 })
	waitStateIdle(t, env)
	if overlap.Load() {
		t.Errorf("a state step ran while another of the group was in a call")
	}
	invs := mark.invocations()
	if len(invs) != 2 || invs[0].PlatformUserID != "20001" || !slices.Equal(invs[0].TriggerMessageIDs, []string{ev1.Message.ID}) ||
		invs[1].PlatformUserID != "29999" || !slices.Equal(invs[1].TriggerMessageIDs, []string{ev2.Message.ID}) {
		t.Errorf("mark invocations = %+v", invs)
	}
}

func TestStateStep_ReRoutesOnModelFailure(t *testing.T) {
	env, _, mark := stepEnv(t, testOpts{
		shortTools: func(req map[string]any) fakeopenai.Reply {
			if isStateRequest(t, req) && hasToolResult(req) {
				return fakeopenai.Reply{Status: 500, Content: "down"}
			}
			return stateModel(t, textReply(fakeReply))(req)
		},
		longTools: textReply("没什么变化"),
	}, config.StateStepAfterReply)
	written := recordUsage(t, env)
	task := env.waitTask(t, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
	waitUntil(t, "the long model asked", func() bool { return env.long.Calls() == 1 })
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(privateConvKey) })
	waitStateIdle(t, env)
	if mark.count() != 1 {
		t.Errorf("mark called %d times, want 1", mark.count())
	}
	long := env.long.Requests()
	if len(long) != 1 || !isStateRequest(t, long[0]) {
		t.Fatalf("long requests = %d, want the re-routed state request", len(long))
	}
	if msgs := toolMessages(long[0]); len(msgs) != 1 || msgs[0]["content"] != fromTool(nowResult) {
		t.Errorf("re-routed request tool messages = %v, want the earlier mark result", msgs)
	}
	type call struct{ role, outcome string }
	var got []call
	for _, c := range written() {
		if c.TaskID != task.TaskID {
			t.Errorf("call %+v not attributed to the reply task", c)
		}
		if c.Purpose == store.PurposeState {
			got = append(got, call{c.Role, c.Outcome})
		}
	}
	want := []call{{"short_context", store.OutcomeOK}, {"short_context", store.OutcomeError}, {"long_context", store.OutcomeOK}}
	if !slices.Equal(got, want) {
		t.Errorf("state calls = %v, want %v", got, want)
	}
	if logs := env.logs.String(); !strings.Contains(logs, "outcome=done") || !strings.Contains(logs, "role=long_context") {
		t.Errorf("logs lack the re-routed step")
	}
}

func TestStateStep_BackgroundAdmission(t *testing.T) {
	task := store.TaskRecord{ID: "task_1", BotInstanceID: testBot, ConversationKey: privateConvKey, Status: conloomapi.TaskRunning,
		ActorPlatformUserID: privateUser, TriggerMessageIDs: []string{"msg_1"}, Priority: int(admission.PriorityInteractive),
		Meta: conloomapi.TaskMeta{Trigger: conloomapi.TriggerPrivate}, CreatedAt: time.Now()}
	want := task
	want.Priority = int(admission.PriorityBackground)
	if got := stateTask(task); !reflect.DeepEqual(got, want) {
		t.Errorf("stateTask = %+v, want %+v", got, want)
	}
}

func TestStateInvocation_CoalescesTriggersInOrder(t *testing.T) {
	turns := []stateTurn{
		{inv: skills.Invocation{TaskID: "task_a", DisplayName: "小明", TriggerMessageIDs: []string{"a", "b"}}},
		{inv: skills.Invocation{TaskID: "task_b", DisplayName: "小红", ConversationKey: privateConvKey, TriggerMessageIDs: []string{"b", "c"}}},
	}
	got := stateInvocation(turns)
	want := skills.Invocation{TaskID: "task_b", DisplayName: "小红", ConversationKey: privateConvKey, TriggerMessageIDs: []string{"a", "b", "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stateInvocation = %+v, want %+v", got, want)
	}
	if !slices.Equal(turns[0].inv.TriggerMessageIDs, []string{"a", "b"}) || !slices.Equal(turns[1].inv.TriggerMessageIDs, []string{"b", "c"}) {
		t.Errorf("turns changed: %+v", turns)
	}
}

func TestStateDirective_Copy(t *testing.T) {
	const instruction = "【状态更新】这一步不是回复，不会发给对方。根据最近这一轮（上面的当前消息和你下面的回复）判断你的状态、场景或要记住的事有没有变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。"
	if got := stateDirective([]stateTurn{{reply: "好呀"}}); got != instruction+"\n\n你刚才的回复：\n「好呀」" {
		t.Errorf("one turn: %q", got)
	}
	if got := stateDirective([]stateTurn{{reply: "好呀"}, {reply: "第一条\n第二条"}}); got != instruction+"\n\n你刚才的回复：\n「好呀」\n「第一条\n第二条」" {
		t.Errorf("two turns: %q", got)
	}
}

func TestStateStep_CoalescingKeepsNewestTurnsAndLogsDropped(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	taskOf := func(i int) store.TaskRecord {
		return store.TaskRecord{ID: fmt.Sprintf("task_%d", i), BotInstanceID: testBot, ConversationKey: privateConvKey,
			ActorPlatformUserID: privateUser}
	}
	busy := env.o.holdState(taskOf(0))
	for i := 1; i <= stateMaxTurns; i++ {
		env.o.holdState(taskOf(i))(&stateTurn{task: taskOf(i), reply: fmt.Sprint(i)})
	}
	if strings.Contains(env.logs.String(), "state turns dropped") {
		t.Fatalf("logged dropped turns with %d turns:\n%s", stateMaxTurns, env.logs.String())
	}
	for i := stateMaxTurns + 1; i <= stateMaxTurns+2; i++ {
		env.o.holdState(taskOf(i))(&stateTurn{task: taskOf(i), reply: fmt.Sprint(i)})
	}
	s := env.o.states
	s.mu.Lock()
	var replies []string
	for _, e := range s.pending {
		for _, turn := range e.turns {
			replies = append(replies, turn.reply)
		}
	}
	s.mu.Unlock()
	if want := []string{"3", "4", "5", "6", "7", "8", "9", "10"}; !slices.Equal(replies, want) {
		t.Errorf("pending replies = %v, want %v", replies, want)
	}
	logs := env.logs.String()
	if strings.Count(logs, `msg="state turns dropped"`) != 2 || !strings.Contains(logs, "conversation="+privateConvKey+" dropped=1") {
		t.Errorf("logs lack the two dropped turns:\n%s", logs)
	}
	env.o.cancelStateSteps(privateConvKey, "")
	busy(nil)
	waitStateIdle(t, env)
}
