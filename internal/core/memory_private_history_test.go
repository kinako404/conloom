package core

import (
	"strings"
	"testing"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestPrivateHistory_DropsMessagesBeforeForget(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		env := newTestOrchestrator(t, testOpts{configure: privMemConfig(enabled)})
		say := func(text string) conloomapi.Task {
			t.Helper()
			resp := env.submit(t, env.event(conloomapi.ConvPrivate, "20001", "20001", text, withName("小明")))
			return env.injectDone(t, resp.TaskID)
		}
		first := say("我偷偷喜欢甜食")
		quoted := env.event(conloomapi.ConvPrivate, "20001", "20001", "第二句旧话", withName("小明"))
		env.injectDone(t, env.submit(t, quoted).TaskID)
		if got := env.privateCommand(t, "20001", "/忘记我"); got != CopyMemoryForgottenPrivate {
			t.Fatalf("enabled=%v /忘记我 = %q", enabled, got)
		}
		ask := env.event(conloomapi.ConvPrivate, "20001", "20001", "还记得我吗", withName("小明"), replyTo(quoted.Message.ID))
		task := env.injectDone(t, env.submit(t, ask).TaskID)
		if first.Status != conloomapi.TaskSucceeded || task.Status != conloomapi.TaskSucceeded {
			t.Fatalf("enabled=%v tasks = %+v, %+v", enabled, first, task)
		}
		reqs := env.short.Requests()
		all := requestText(t, reqs[len(reqs)-1])
		for _, old := range []string{"甜食", "第二句旧话"} {
			if strings.Contains(all, old) {
				t.Errorf("enabled=%v request after /忘记我 still shows %q:\n%s", enabled, old, all)
			}
		}
		if !strings.Contains(all, "还记得我吗") {
			t.Errorf("enabled=%v request lacks the new message:\n%s", enabled, all)
		}
		if before := requestText(t, reqs[1]); !strings.Contains(before, "甜食") {
			t.Errorf("enabled=%v request before /忘记我 lacks the earlier message:\n%s", enabled, before)
		}
	}
}

func TestPrivateHistory_GroupUnaffected(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	env.submit(t, env.groupEvent("20001", "我偷偷喜欢甜食", withName("小明")))
	if got := env.command(t, "20001", "/忘记我"); got != CopyMemoryForgotten {
		t.Fatalf("/忘记我 = %q", got)
	}
	task := env.injectDone(t, env.submit(t, env.groupEvent("20001", "在吗", withName("小明"), mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	if all := requestText(t, env.short.Requests()[0]); !strings.Contains(all, "甜食") {
		t.Errorf("group history lost a message after /忘记我:\n%s", all)
	}
}
