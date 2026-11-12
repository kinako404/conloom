package core

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestRunner_UsesHistoryAndPersona(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	env.submit(t, env.groupEvent("20002", "今天天气不错"))
	resp := env.submit(t, env.groupEvent("20001", "你觉得呢", mentionBot))
	if task := env.waitTask(t, resp.TaskID); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	msgs := requestMessages(t, env.short.Requests()[0])
	if sys, _ := msgs[0]["content"].(string); msgs[0]["role"] != "system" || !strings.Contains(sys, "你是织语") {
		t.Fatalf("first message = %v", msgs[0])
	}
	var all strings.Builder
	for _, m := range msgs {
		c, _ := m["content"].(string)
		all.WriteString(c + "\n")
	}
	if !strings.Contains(all.String(), "今天天气不错") {
		t.Errorf("observed message missing from request: %q", all.String())
	}
	if n := strings.Count(all.String(), "你觉得呢"); n != 1 {
		t.Errorf("trigger text appears %d times, want 1", n)
	}
	if last, _ := msgs[len(msgs)-1]["content"].(string); !strings.Contains(last, "你觉得呢") {
		t.Errorf("last message = %q", last)
	}
}

func TestRunner_ContextTooLargeSendsCopy(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		long := c.ModelProfiles["long_context"]
		long.Context = c.ModelProfiles["short_context"].Context
		c.ModelProfiles["long_context"] = long
	}})
	ev := env.groupEvent("20001", strings.Repeat("长", 3900), mentionBot)
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != conloomapi.CodeContextTooLarge {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyTooLong {
		t.Fatalf("actions = %+v", task.Actions)
	}
	if task.Actions[0].ReplyToMessageID != ev.Message.ID {
		t.Errorf("copy reply target = %q", task.Actions[0].ReplyToMessageID)
	}
	if n := env.short.Calls() + env.long.Calls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}

func TestRunner_ShortFailsFallsBackToLong(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) { return 500, "boom" },
		long:  answer("长模型回复"),
	})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].Content.Text != "长模型回复" {
		t.Fatalf("task = %+v", task)
	}
	if task.Meta.SelectedModelRole != "long_context" || task.Meta.SelectedModel != "long-model" {
		t.Errorf("meta = %+v", task.Meta)
	}
	if s, l := env.short.Calls(), env.long.Calls(); s != 1 || l != 1 {
		t.Errorf("calls short=%d long=%d, want 1 and 1", s, l)
	}
}

func TestRunner_EmptyModelOutputSendsUnavailableCopy(t *testing.T) {
	think := answer("<think>想一想</think>")
	env := newTestOrchestrator(t, testOpts{short: think, long: think})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskFailed || task.Error == nil || task.Error.Code != conloomapi.CodeModelUnavailable {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Actions) != 1 || task.Actions[0].Content.Text != CopyUnavailable {
		t.Fatalf("actions = %+v", task.Actions)
	}
	if s, l := env.short.Calls(), env.long.Calls(); s != 1 || l != 1 {
		t.Errorf("calls short=%d long=%d, want 1 and 1", s, l)
	}
}

func TestRunner_OutputSanitized(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer("大家好[CQ:at,qq=all]")})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if text := task.Actions[0].Content.Text; strings.Contains(text, "[CQ:") || text != "大家好" {
		t.Fatalf("action text = %q", text)
	}
	if task.Meta.ReviewResult != "sanitize" {
		t.Errorf("review result = %q", task.Meta.ReviewResult)
	}
}

func TestRunner_BlockedOutputHasNoAction(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: answer("这里有禁词"),
		configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Review.BlockedWords = []string{"禁词"}
		},
	})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "blocked" {
		t.Fatalf("task = %+v", task)
	}
	if strings.Contains(env.logs.String(), "禁词") {
		t.Errorf("blocked text leaked into logs")
	}
}

func TestRunner_ConcurrencyTwoBothReply(t *testing.T) {
	var inflight, peak atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	handler := func(map[string]any) (int, string) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		}
		return 200, "收到"
	}
	env := newTestOrchestrator(t, testOpts{short: handler, configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 2
	}})
	ev1 := env.groupEvent("20001", "问题一", mentionBot)
	ev2 := env.groupEvent("20002", "问题二", mentionBot)
	r1, r2 := env.submit(t, ev1), env.submit(t, ev2)
	for _, c := range []struct {
		taskID, msgID string
	}{{r1.TaskID, ev1.Message.ID}, {r2.TaskID, ev2.Message.ID}} {
		task := env.waitTask(t, c.taskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].ReplyToMessageID != c.msgID {
			t.Fatalf("task %s = %+v", c.taskID, task)
		}
	}
	if p := peak.Load(); p != 2 {
		t.Fatalf("peak concurrent model calls = %d, want 2", p)
	}
}
