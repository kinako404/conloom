package core

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/outbox"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
)

// panelStepEnv is a stepEnv with two panel chats of the same user, a and b.
// The first reply to b's "我是乙" waits until holdB opens (bEntered closes
// when it arrives); replies to a's "我是甲" are "旧回复" and then "新回复";
// the first state request waits until holdStep opens (stateEntered closes
// when it arrives) and then asks for mark.
type panelStepEnv struct {
	*testEnv
	mark                   stateTool
	d                      *outbox.PanelDeliverer
	a, b                   string
	bEntered, stateEntered chan struct{}
	holdB, holdStep        *gate
}

func newPanelStepEnv(t *testing.T) *panelStepEnv {
	t.Helper()
	e := &panelStepEnv{bEntered: make(chan struct{}), stateEntered: make(chan struct{}), holdB: newGate(), holdStep: newGate()}
	var bCalls, aCalls, states atomic.Int32
	env, _, mark := stepEnv(t, testOpts{shortTools: func(req map[string]any) fakeopenai.Reply {
		if isStateRequest(t, req) {
			if states.Add(1) == 1 {
				close(e.stateEntered)
				e.holdStep.wait()
				return fakeopenai.Reply{ToolCalls: []fakeopenai.ToolCall{{ID: "late", Name: "mark", Arguments: "{}"}}}
			}
			return stateModel(t, nil)(req)
		}
		if strings.Contains(lastUserText(t, req), "我是乙") {
			if bCalls.Add(1) == 1 {
				close(e.bEntered)
				e.holdB.wait()
			}
			return fakeopenai.Reply{Content: "回复乙"}
		}
		if aCalls.Add(1) == 1 {
			return fakeopenai.Reply{Content: "旧回复"}
		}
		return fakeopenai.Reply{Content: "新回复"}
	}}, config.StateStepAfterReply)
	t.Cleanup(e.holdB.open)
	t.Cleanup(e.holdStep.open)
	e.testEnv, e.mark = env, mark
	e.a, e.b = env.createChat(t, testBot), env.createChat(t, testBot)
	e.d = env.startPanelDeliverer(t)
	return e
}

// replyA has the user write "我是甲" in chat a and waits for the reply, whose
// state step is then running, in the model, or, when pending, waiting behind
// a reply of the same user in chat b that is in the model. It returns the
// user's message and the reply.
func (e *panelStepEnv) replyA(t *testing.T, pending bool) (user, reply store.MessageRecord) {
	t.Helper()
	if pending {
		e.holdStep.open()
		e.submitPanel(t, e.b, "admin", "我是乙")
		waitClosed(t, e.bEntered, "chat b's reply in the model")
	}
	id := e.submitPanel(t, e.a, "admin", "我是甲")
	e.waitTask(t, e.panelResponse(t, id).TaskID)
	user = e.panelMessage(t, id)
	reply = e.waitPanelReply(t, e.d, e.a, user.Seq)
	waitUntil(t, "chat a to be idle", func() bool { return !e.o.Busy(e.a) })
	if pending {
		waitUntil(t, "chat a's state step to wait", func() bool { return pendingStateEntries(e.testEnv) == 1 })
	} else {
		waitClosed(t, e.stateEntered, "chat a's state request")
	}
	return user, reply
}

// settle opens the gates and waits for the tasks and state steps to end.
func (e *panelStepEnv) settle(t *testing.T) {
	t.Helper()
	e.holdB.open()
	e.holdStep.open()
	waitUntil(t, "tasks to stop", func() bool { return !e.o.Busy(e.a) && !e.o.Busy(e.b) })
	waitStateIdle(t, e.testEnv)
}

// stateStepsOf returns the first request of each state step over a message
// containing text.
func (e *panelStepEnv) stateStepsOf(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	for _, req := range firstRounds(stateRequests(t, e.short.Requests())) {
		if last := lastUserText(t, req); strings.Contains(last, text) {
			out = append(out, last)
		}
	}
	return out
}

// marksIn returns mark's invocations in conversation convKey.
func (e *panelStepEnv) marksIn(convKey string) [][]string {
	var out [][]string
	for _, inv := range e.mark.invocations() {
		if inv.ConversationKey == convKey {
			out = append(out, inv.TriggerMessageIDs)
		}
	}
	return out
}

func TestStateStep_RegeneratedReplyIsNotJudged(t *testing.T) {
	for _, c := range []struct {
		name    string
		pending bool
		steps   int
	}{{"running", false, 2}, {"pending", true, 1}} {
		t.Run(c.name, func(t *testing.T) {
			e := newPanelStepEnv(t)
			user, reply := e.replyA(t, c.pending)
			newID, err := e.o.RegeneratePanel(context.Background(), e.a, reply.Seq)
			if err != nil {
				t.Fatalf("RegeneratePanel: %v", err)
			}
			if task := e.waitTask(t, newID); len(task.Actions) != 1 || task.Actions[0].Content.Text != "新回复" {
				t.Fatalf("regenerated task = %+v", task)
			}
			e.settle(t)

			steps := e.stateStepsOf(t, "我是甲")
			if len(steps) != c.steps {
				t.Fatalf("chat a's state steps = %d, want %d", len(steps), c.steps)
			}
			last := steps[len(steps)-1]
			if strings.Contains(last, "旧回复") || strings.Count(last, "我是甲") != 1 || !strings.HasSuffix(last, stateRepliesHeading+"\n「新回复」") {
				t.Errorf("chat a's state request = %q, want only the new reply over the message once", last)
			}
			if marks := e.marksIn(e.a); len(marks) != 1 || !slices.Equal(marks[0], []string{user.ID}) {
				t.Errorf("chat a's mark triggers = %v, want [[%s]]", marks, user.ID)
			}
		})
	}
}

func TestStateStep_WithdrawnMessagesAreNotJudged(t *testing.T) {
	for _, c := range []struct {
		name    string
		pending bool
		steps   int
	}{{"running", false, 1}, {"pending", true, 0}} {
		t.Run(c.name, func(t *testing.T) {
			e := newPanelStepEnv(t)
			user, _ := e.replyA(t, c.pending)
			if _, err := e.o.WithdrawPanel(context.Background(), e.a, user.Seq); err != nil {
				t.Fatalf("WithdrawPanel: %v", err)
			}
			e.settle(t)

			if steps := e.stateStepsOf(t, "我是甲"); len(steps) != c.steps {
				t.Errorf("chat a's state steps = %d, want %d (only the one cancelled by the withdrawal)", len(steps), c.steps)
			}
			if marks := e.marksIn(e.a); len(marks) != 0 {
				t.Errorf("chat a's mark triggers = %v, want none", marks)
			}
			if c.pending {
				if marks := e.marksIn(e.b); len(marks) != 1 {
					t.Errorf("chat b's mark triggers = %v, want its own step", marks)
				}
			} else if !strings.Contains(e.logs.String(), "outcome=cancelled") {
				t.Errorf("logs lack the cancelled step")
			}
		})
	}
}
