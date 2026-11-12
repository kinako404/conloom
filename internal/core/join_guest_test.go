package core

import (
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// guestSession starts a session with 20001 (so the bot just spoke) and seeds
// 20002 as an eligible high-level member outside it.
func guestSession(t *testing.T, f func(c *config.Config)) *testEnv {
	t.Helper()
	env := newTestOrchestrator(t, testOpts{configure: joinConfig(time.Hour, func(c *config.Config) {
		withContinuation(c)
		if f != nil {
			f(c)
		}
	})})
	env.seedMember(t, "20002", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	env.startContinuation(t, "20001")
	env.waitIdle(t)
	return env
}

func TestJoin_GuestQuestionDuringSession(t *testing.T) {
	env := guestSession(t, nil)
	q := env.chat(t, line{"20002", "猫娘可爱吗"})[0]
	task := env.requireJoin(t)
	if len(task.Actions) != 1 || joinTarget(t, env, task.TaskID) != q.Message.ID {
		t.Fatalf("guest join task = %+v, want a reply to %s", task, q.Message.ID)
	}
	if n := env.proactiveCount(t, "", store.ProactiveEvaluated); n != 1 {
		t.Fatalf("evaluations = %d, want 1", n)
	}
	if n := env.modelCalls(); n != 2 {
		t.Fatalf("model calls = %d, want 2 (mention, guest join)", n)
	}
	want := "猫娘可爱吗\n\n" + CopyDirectiveJoin(conloomapi.ProactiveHigh, "用户20002", []string{"用户20002"}, topTerms(t, env, "20002"))
	if c := lastContent(env.short.Requests()[1]); !strings.HasSuffix(c, want) {
		t.Fatalf("guest join request current = %q, want suffix %q", c, want)
	}
	waitUntil(t, "the guest joining the session", func() bool { return env.inContinuation("20002") })
	if !env.inContinuation("20001") {
		t.Fatal("the session's participant left when the guest joined")
	}
	if u, _, _ := env.sessionCounts(); u != 1 {
		t.Fatalf("session unprompted = %d, want 1", u)
	}
	var line string
	for _, l := range strings.Split(env.logs.String(), "\n") {
		if strings.Contains(l, "join evaluation") {
			line = l
		}
	}
	if !strings.Contains(line, "guest=true") {
		t.Fatalf("join evaluation log = %q, want guest=true", line)
	}
	if strings.Contains(env.logs.String(), "猫娘") {
		t.Fatal("message text leaked into logs")
	}
	if task := env.turn(t, "20002", "真的很可爱"); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("guest turn = %+v", task)
	}
}

func TestJoin_GuestQuestionNotAnswered(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, env *testEnv)
		want  string
	}{
		{"not a question", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20002", "猫娘可爱！"})
		}, joinNoSession},
		{"loose question", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20002", "我在吃饭呢"})
		}, joinNoSession},
		{"guest without eligibility", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20003", "猫娘可爱吗"})
		}, joinNoSession},
		{"guest out of their own budget", func(t *testing.T, env *testEnv) {
			env.logProactive(t, "20002", store.ProactiveSent, time.Now().Add(-5*time.Minute))
			env.chat(t, line{"20002", "猫娘可爱吗"})
		}, joinNoSession},
		{"question answered by another member", func(t *testing.T, env *testEnv) {
			env.chat(t, line{"20002", "猫娘可爱吗"}, line{"20003", "可爱"})
		}, joinNoSession},
		{"hourly evaluations used up", func(t *testing.T, env *testEnv) {
			for range 4 {
				env.logProactive(t, "20009", store.ProactiveEvaluated, time.Now().Add(-10*time.Minute))
			}
			env.chat(t, line{"20002", "猫娘可爱吗"})
		}, joinNoBudget},
		{"group hourly replies used up", func(t *testing.T, env *testEnv) {
			for range 4 {
				env.logProactive(t, "20009", store.ProactiveSent, time.Now().Add(-10*time.Minute))
			}
			env.chat(t, line{"20002", "猫娘可爱吗"})
		}, joinNoBudget},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := guestSession(t, nil)
			c.setup(t, env)
			env.requireNoJoin(t, c.want)
			if n := env.modelCalls(); n != 1 {
				t.Fatalf("model calls = %d, want 1 (the mention)", n)
			}
			if env.inContinuation("20002") {
				t.Fatal("guest joined the session without a reply")
			}
		})
	}
}

func TestJoin_ParticipantQuestionIsATurn(t *testing.T) {
	env := guestSession(t, nil)
	env.seedMember(t, "20001", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	if task := env.turn(t, "20001", "这个好用吗"); task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("participant turn = %+v", task)
	}
	env.waitIdle(t)
	env.requireNoJoin(t, joinNoSession)
	if n := env.proactiveCount(t, "", store.ProactiveEvaluated); n != 0 {
		t.Fatalf("evaluations = %d, want 0", n)
	}
}

func TestJoin_GuestQuestionDuringCooldown(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: joinConfig(time.Hour, nil)})
	env.seedMember(t, "20002", conloomapi.ProactiveHigh, time.Now().Add(-time.Hour))
	env.engage(t, "20001", "在吗")
	env.waitIdle(t)
	env.chat(t, line{"20002", "猫娘可爱"})
	env.requireNoJoin(t, joinNoCooldown)
	q := env.chat(t, line{"20002", "猫娘可爱吗"})[0]
	if task := env.requireJoin(t); joinTarget(t, env, task.TaskID) != q.Message.ID {
		t.Fatalf("guest join task = %+v, want a reply to %s", task, q.Message.ID)
	}
}
