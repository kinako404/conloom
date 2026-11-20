package core

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// blockingStateEnv is a private stepEnv whose first state request waits until
// open is called and then asks for mark; entered closes when it arrives.
func blockingStateEnv(t *testing.T, configure func(*config.Config)) (env *testEnv, mark stateTool, entered <-chan struct{}, open func()) {
	t.Helper()
	g, in := newGate(), make(chan struct{})
	var states atomic.Int32
	env, _, mark = stepEnv(t, testOpts{configure: configure, shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) && states.Add(1) == 1 {
			close(in)
			g.wait()
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "late", Name: "mark", Arguments: "{}"}}}
		}
		return stateModel(t, textReply(fakeReply))(req)
	}}, config.StateStepAfterReply)
	t.Cleanup(g.open)
	return env, mark, in, g.open
}

func TestStateStep_CloseCancelsRunningStep(t *testing.T) {
	env, mark, entered, open := blockingStateEnv(t, nil)
	requireOneReply(t, env, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
	waitClosed(t, entered, "the state request")
	closed := make(chan struct{})
	go func() {
		env.o.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return while a state step was in the model")
	}
	open()
	if mark.count() != 0 {
		t.Errorf("mark called %d times after Close", mark.count())
	}
	if !env.o.states.idle() {
		t.Errorf("state steps not idle after Close")
	}
	if !strings.Contains(env.logs.String(), "outcome=cancelled") {
		t.Errorf("logs lack the cancelled step")
	}
}

func TestStateStep_CancelledByCommands(t *testing.T) {
	const user = "29999"
	for _, command := range []string{"/stop", "/pause", "/忘记我"} {
		t.Run(command, func(t *testing.T) {
			env, mark, entered, open := blockingStateEnv(t, privMemConfig(true))
			requireOneReply(t, env, env.submit(t, env.event(conloomapi.ConvPrivate, user, user, "我先去洗澡了")).TaskID)
			waitClosed(t, entered, "the state request")
			resp := env.submit(t, env.event(conloomapi.ConvPrivate, user, user, command))
			if task := env.getTask(t, resp.TaskID); task.Status != conloomapi.TaskSucceeded {
				t.Fatalf("command task = %+v", task)
			}
			if command == "/忘记我" && (!env.o.states.idle() || !strings.Contains(env.logs.String(), "outcome=cancelled")) {
				t.Errorf("the state step was not cancelled and stopped before the forget")
			}
			waitStateIdle(t, env)
			open()
			if mark.count() != 0 {
				t.Errorf("mark called %d times", mark.count())
			}
			if n := len(stateRequests(t, env.short.Requests())); n != 1 {
				t.Errorf("state requests = %d, want only the cancelled one", n)
			}
			if !strings.Contains(env.logs.String(), "outcome=cancelled") {
				t.Errorf("logs lack the cancelled step")
			}
		})
	}
}

func TestStateStep_ForgetThroughAdminStopsIt(t *testing.T) {
	env, mark, entered, open := blockingStateEnv(t, nil)
	requireOneReply(t, env, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
	waitClosed(t, entered, "the state request")
	ctx := context.Background()
	conv, err := env.o.conversation(ctx, privateConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.o.memAdmin.ForgetMember(ctx, conv, privateUser, "panel", "test"); err != nil {
		t.Fatal(err)
	}
	open()
	waitUntil(t, "the step to end", func() bool { return strings.Contains(env.logs.String(), `msg="state step"`) })
	waitStateIdle(t, env)
	if mark.count() != 0 {
		t.Errorf("mark called %d times after the forget", mark.count())
	}
	if !strings.Contains(env.logs.String(), "outcome=forgotten") {
		t.Errorf("logs lack the forgotten outcome")
	}
}

func TestStateStep_DeletePanelChatWaitsForIt(t *testing.T) {
	g, entered := newGate(), make(chan struct{})
	var inTool atomic.Bool
	mark := blockingMark(g, entered, &inTool)
	env := stepEnvOf(t, testOpts{shortTools: stateModel(t, textReply(fakeReply))}, config.StateStepAfterReply, &recTool{name: "now"}, mark)
	t.Cleanup(g.open)
	key := env.createChat(t, testBot)
	env.waitTask(t, env.panelResponse(t, env.submitPanel(t, key, "admin", "我先去洗澡了")).TaskID)
	waitClosed(t, entered, "the step's call")
	deleted := make(chan error, 1)
	go func() { deleted <- env.o.DeletePanelChat(context.Background(), key) }()
	select {
	case err := <-deleted:
		t.Fatalf("DeletePanelChat returned %v while the chat's state step was in a call", err)
	case <-time.After(100 * time.Millisecond):
	}
	g.open()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatalf("DeletePanelChat = %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("DeletePanelChat did not return after the step's call ended")
	}
	if !env.o.states.idle() {
		t.Errorf("state step still running after the chat was deleted")
	}
	if mark.count() != 1 {
		t.Errorf("mark called %d times, want only the one that was running", mark.count())
	}
	if n := len(stateRequests(t, env.short.Requests())); n != 1 {
		t.Errorf("state requests = %d, want 1", n)
	}
}

func TestStateStep_UnaddressedTurnDoesNotCancelIt(t *testing.T) {
	g, entered := newGate(), make(chan struct{})
	var states atomic.Int32
	env, _, mark := stepEnv(t, testOpts{configure: withContinuation, shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) && states.Add(1) == 1 {
			close(entered)
			g.wait()
			return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "late", Name: "mark", Arguments: "{}"}}}
		}
		return stateModel(t, textReply(fakeReply))(req)
	}}, config.StateStepAfterReply)
	t.Cleanup(g.open)
	env.startContinuation(t, "20001")
	waitClosed(t, entered, "the mention's state request")
	turn := env.waitTask(t, env.submit(t, env.groupEvent("20001", "晚饭吃什么好呢")).TaskID)
	if turn.Status != conloomapi.TaskSucceeded || turn.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Fatalf("turn task = %+v", turn)
	}
	g.open()
	waitUntil(t, "mark called", func() bool { return mark.count() == 1 })
	waitUntil(t, "tasks to stop", func() bool { return !env.o.Busy(testConvKey) })
	waitStateIdle(t, env)
	if n := len(firstRounds(stateRequests(t, env.short.Requests()))); n != 1 {
		t.Errorf("state steps = %d, want only the mention's, never restarted", n)
	}
	if strings.Contains(env.logs.String(), "outcome=cancelled") {
		t.Errorf("the turn cancelled the mention's state step")
	}
}

func TestStateStep_StoreErrorBeforeARoundDoesNotReRoute(t *testing.T) {
	var hooks atomic.Pointer[hookStore]
	env, _, mark := stepEnv(t, testOpts{
		shortTools: func(req map[string]any) fakeopenai.Reply {
			if isStateRequest(t, req) {
				hooks.Load().failMembers.Store(true)
			}
			return stateModel(t, textReply(fakeReply))(req)
		},
		longTools: textReply("没什么变化"),
	}, config.StateStepAfterReply)
	hooks.Store(env.hooks)
	t.Cleanup(func() { env.hooks.failMembers.Store(false) })
	requireOneReply(t, env, env.submit(t, env.privateEvent("我先去洗澡了")).TaskID)
	waitUntil(t, "the step to end", func() bool { return strings.Contains(env.logs.String(), `msg="state step"`) })
	waitStateIdle(t, env)
	if mark.count() != 0 {
		t.Errorf("mark called %d times", mark.count())
	}
	if n := env.long.Calls(); n != 0 {
		t.Errorf("long calls = %d, want none: a store error is not the model's", n)
	}
	if n := len(stateRequests(t, env.short.Requests())); n != 1 {
		t.Errorf("state requests = %d, want 1", n)
	}
	if logs := env.logs.String(); !strings.Contains(logs, `level=WARN msg="state step"`) || !strings.Contains(logs, "outcome=error") {
		t.Errorf("logs lack the warning with outcome=error:\n%s", logs)
	}
}

func TestStateStep_ForgetBetweenTurnsKeepsTheLaterOnes(t *testing.T) {
	env, mark, entered, _ := blockingStateEnv(t, nil)
	requireOneReply(t, env, env.submit(t, env.groupEvent("20001", "第一句", mentionBot)).TaskID)
	waitClosed(t, entered, "the first state request")
	ctx := context.Background()
	conv, err := env.o.conversation(ctx, testConvKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.o.memAdmin.ForgetMember(ctx, conv, "20001", "panel", "test"); err != nil {
		t.Fatal(err)
	}
	ev2 := env.groupEvent("20001", "第二句", mentionBot)
	second := requireOneReply(t, env, env.submit(t, ev2).TaskID)
	waitUntil(t, "the second step to end", func() bool { return strings.Count(env.logs.String(), `msg="state step"`) == 2 })
	waitStateIdle(t, env)
	invs := mark.invocations()
	if len(invs) != 1 || invs[0].TaskID != second.TaskID || !slices.Equal(invs[0].TriggerMessageIDs, []string{ev2.Message.ID}) {
		t.Fatalf("mark invocations = %+v, want one over the second message only", invs)
	}
	firsts := firstRounds(stateRequests(t, env.short.Requests()))
	last := lastUserText(t, firsts[len(firsts)-1])
	if _, current, _ := strings.Cut(last, "【当前消息】"); strings.Contains(current, "第一句") || strings.Count(current, "「") != 1 {
		t.Errorf("state request = %q, want only the turn after the forget", last)
	}
}
