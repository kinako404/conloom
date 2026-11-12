package core

import (
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// questionConfig needs 4 window messages for a join.
func questionConfig(lull time.Duration) func(*config.Config) {
	return joinConfig(lull, func(c *config.Config) { c.DefaultGroupPolicy.Proactive.JoinMinMessages = 4 })
}

func TestJoin_UnansweredQuestionWaivesMinMessages(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []line
	}{
		{"one question", []line{{"20001", "这个好用吗"}}},
		{"repeated question", []line{{"20001", "这个好用吗"}, {"20001", "这个好用吗"}, {"20001", "这个好用吗"}}},
		{"question after other talk", []line{{"20002", "今天好热"}, {"20001", "有没有推荐的"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: questionConfig(time.Hour)})
			env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
			evs := env.chat(t, c.lines...)
			task := env.requireJoin(t)
			if len(task.Actions) != 1 || joinTarget(t, env, task.TaskID) != evs[len(evs)-1].Message.ID {
				t.Fatalf("join task = %+v, want a reply to the question %s", task, evs[len(evs)-1].Message.ID)
			}
			if n := env.proactiveCount(t, "", store.ProactiveEvaluated); n != 1 {
				t.Fatalf("evaluations = %d, want 1", n)
			}
			if n := env.modelCalls(); n != 1 {
				t.Fatalf("model calls = %d, want 1", n)
			}
		})
	}
}

func TestJoin_MinMessagesKeptWithoutUnansweredQuestion(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, env *testEnv)
	}{
		{"lone non-question", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20001", matchText})
		}},
		{"question answered by another member", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20001", "这个好用吗"}, line{"20002", "挺好用的"})
		}},
		{"question from a member without eligibility", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20003", "这个好用吗"})
		}},
		{"question from a member without eligibility after an eligible one", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20001", matchText}, line{"20003", "有人知道吗"})
		}},
		{"question from a member out of budget", func(t *testing.T, env *testEnv) {
			env.logProactive(t, "20001", store.ProactiveSent, time.Now().Add(-5*time.Minute))
			env.chat(t, line{"20001", "这个好用吗"})
		}},
		{"question addressed to another member", func(t *testing.T, env *testEnv) {
			requireObserved(t, env, env.groupEvent("20001", "你觉得呢", mentionUser("user_20002")), "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: questionConfig(time.Hour)})
			env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
			c.setup(t, env)
			env.requireNoJoin(t, joinNoFewMessages)
			if n := env.modelCalls(); n != 0 {
				t.Fatalf("model calls = %d, want 0", n)
			}
		})
	}
}

func TestJoin_LullLogsSkipReason(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: questionConfig(100 * time.Millisecond)})
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	env.chat(t, line{"20001", "晚上吃火锅去"})
	waitUntil(t, "the join skipped log line", func() bool { return strings.Contains(env.logs.String(), "join skipped") })
	logs := env.logs.String()
	var skipped string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "join skipped") {
			skipped = l
		}
	}
	if !strings.Contains(skipped, "level=INFO") || !strings.Contains(skipped, "reason="+joinNoFewMessages) ||
		!strings.Contains(skipped, "window_messages=1") {
		t.Fatalf("skip log line = %q, want INFO with the reason and window size", skipped)
	}
	if strings.Contains(logs, "火锅") || strings.Contains(logs, "用户20001") {
		t.Fatalf("message text or names leaked into logs: %s", logs)
	}
	if n := env.modelCalls(); n != 0 {
		t.Fatalf("model calls = %d, want 0", n)
	}
}
