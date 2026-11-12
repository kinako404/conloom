package core

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// turnClock records when each turn request reached the model.
type turnClock struct {
	mu  sync.Mutex
	at  []time.Time
	req []string
}

func (c *turnClock) handler(req map[string]any) (int, string) {
	if content := lastContent(req); strings.Contains(content, turnDirective) {
		c.mu.Lock()
		c.at, c.req = append(c.at, time.Now()), append(c.req, content)
		c.mu.Unlock()
	}
	return 200, fakeReply
}

func (c *turnClock) turns() ([]time.Time, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.at), slices.Clone(c.req)
}

func withTurnCooldown(d time.Duration, f func(c *config.Config)) func(*config.Config) {
	return func(c *config.Config) {
		withContinuation(c)
		c.DefaultGroupPolicy.Reply.TurnCooldown = conloomapi.Duration{Duration: d}
		if f != nil {
			f(c)
		}
	}
}

func (e *testEnv) heldTurn() bool {
	return slices.ContainsFunc(e.o.sched.Active(testConvKey), func(q QueuedTask) bool { return q.Held })
}

func TestSession_TurnWaitsForCooldownAndMerges(t *testing.T) {
	const cooldown = 800 * time.Millisecond
	clock := &turnClock{}
	env := newTestOrchestrator(t, testOpts{short: clock.handler, long: clock.handler, configure: withTurnCooldown(cooldown, nil)})
	env.startContinuation(t, "20001")
	spoke := env.o.lastBotSpoke(testConvKey)

	first := env.submit(t, env.groupEvent("20001", "今天去爬山了"))
	if first.Disposition != conloomapi.DispositionReply || first.TaskID == "" || first.Merged {
		t.Fatalf("first sentence: %+v, want a new turn", first)
	}
	waitUntil(t, "the turn to be deferred", env.heldTurn)
	time.Sleep(500 * time.Millisecond)
	second := env.submit(t, env.groupEvent("20001", "山上好冷"))
	if !second.Merged || second.TaskID != first.TaskID {
		t.Fatalf("sentence after turn_max_wait during the cooldown: %+v, want merged into %s", second, first.TaskID)
	}
	task := env.waitTask(t, first.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("turn = %+v", task)
	}
	at, reqs := clock.turns()
	if len(at) != 1 {
		t.Fatalf("turn model calls = %d, want 1", len(at))
	}
	if at[0].Before(spoke.Add(cooldown)) {
		t.Fatalf("turn reached the model %v before the cooldown ended", spoke.Add(cooldown).Sub(at[0]))
	}
	if !strings.Contains(reqs[0], "今天去爬山了") || !strings.Contains(reqs[0], "山上好冷") {
		t.Fatalf("turn request = %q, want both sentences", reqs[0])
	}
	var deferred []string
	for _, l := range strings.Split(env.logs.String(), "\n") {
		if strings.Contains(l, "turn deferred") {
			deferred = append(deferred, l)
		}
	}
	if len(deferred) != 1 || !strings.Contains(deferred[0], "task_id="+first.TaskID) || !strings.Contains(deferred[0], "waiting_for=cooldown") ||
		!strings.Contains(deferred[0], "until=") || !strings.Contains(deferred[0], "conversation=") {
		t.Fatalf("turn deferred log lines = %q, want one for %s", deferred, first.TaskID)
	}
}

func TestSession_HeldTurnDoesNotBlockOthers(t *testing.T) {
	const cooldown = 1500 * time.Millisecond
	clock := &turnClock{}
	env := newTestOrchestrator(t, testOpts{short: clock.handler, long: clock.handler, configure: withTurnCooldown(cooldown, nil)})
	env.startContinuation(t, "20001")
	spoke := env.o.lastBotSpoke(testConvKey)

	turn := env.submit(t, env.groupEvent("20001", "今天去爬山了"))
	waitUntil(t, "the turn to be deferred", env.heldTurn)
	mention := env.submit(t, env.groupEvent("20002", "在吗", mentionBot))
	if task := env.waitTask(t, mention.TaskID); len(task.Actions) != 1 {
		t.Fatalf("mention task = %+v", task)
	}
	if !time.Now().Before(spoke.Add(cooldown)) {
		t.Fatal("the mention was answered only after the held turn's cooldown")
	}
	if got := env.command(t, "20003", "/help"); got == "" {
		t.Fatal("command got no reply")
	}
	mentionSpoke := env.o.lastBotSpoke(testConvKey)
	if task := env.waitTask(t, turn.TaskID); len(task.Actions) != 1 {
		t.Fatalf("turn = %+v", task)
	}
	at, _ := clock.turns()
	if len(at) != 1 || at[0].Before(mentionSpoke.Add(cooldown)) {
		t.Fatalf("turn calls at %v, want one after the cooldown following the last reply (%v)", at, mentionSpoke.Add(cooldown))
	}
}

func TestSession_MentionMergedIntoHeldTurnRepliesOnce(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withTurnCooldown(time.Minute, nil)})
	env.startContinuation(t, "20001")
	turn := env.submit(t, env.groupEvent("20001", "今天去爬山了"))
	waitUntil(t, "the turn to be deferred", env.heldTurn)
	mention := env.submit(t, env.groupEvent("20001", "你周末干嘛了", mentionBot))
	if !mention.Merged || mention.TaskID != turn.TaskID {
		t.Fatalf("mention during the cooldown: %+v, want merged into %s", mention, turn.TaskID)
	}
	task := env.waitTask(t, turn.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("turn with a mention = %+v, want one reply", task)
	}
	if n := env.modelCalls(); n != 2 {
		t.Fatalf("model calls = %d, want 2 (mention, merged turn)", n)
	}
	if c := lastContent(env.short.Requests()[1]); strings.Contains(c, turnDirective) {
		t.Fatalf("addressed turn request carries the turn directive: %q", c)
	}
	env.waitIdle(t)
}

func TestJoin_GuestQuestionNotBlockedByHeldTurn(t *testing.T) {
	env := guestSession(t, func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.TurnCooldown = conloomapi.Duration{Duration: time.Minute}
	})
	env.submit(t, env.groupEvent("20001", "今天去爬山了"))
	waitUntil(t, "the turn to be deferred", env.heldTurn)
	q := env.chat(t, line{"20002", "猫娘可爱吗"})[0]
	task := env.requireJoin(t)
	if len(task.Actions) != 1 || joinTarget(t, env, task.TaskID) != q.Message.ID {
		t.Fatalf("guest join during a held turn = %+v, want a reply to %s", task, q.Message.ID)
	}
}

func TestSession_HeldTurnDroppedAtHoldLimit(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(gate) }) }
	handler := func(req map[string]any) (int, string) {
		if strings.Contains(lastContent(req), "等我一下") {
			<-gate
		}
		return 200, fakeReply
	}
	env := newTestOrchestrator(t, testOpts{short: handler, long: handler, configure: withTurnCooldown(200*time.Millisecond, nil)})
	t.Cleanup(free)
	env.startContinuation(t, "20001")
	busy := env.submit(t, env.groupEvent("20002", "等我一下", mentionBot))
	waitUntil(t, "the mention to run", func() bool { return env.getTask(t, busy.TaskID).Status == conloomapi.TaskRunning })

	turn := env.submit(t, env.groupEvent("20001", "今天去爬山了"))
	waitUntil(t, "the turn to be deferred", env.heldTurn)
	// The hold limit is turn_cooldown + turn_max_wait = 600ms; the mention
	// still runs then.
	requireDropped(t, env, turn.TaskID)
	if n := env.modelCalls(); n != 2 {
		t.Fatalf("model calls = %d, want 2 (two mentions)", n)
	}
	if _, skips, ok := env.sessionCounts(); !ok || skips != 0 {
		t.Fatalf("session exists %v skips %d, want no skip for a dropped turn", ok, skips)
	}
	if logs := env.logs.String(); !strings.Contains(logs, "turn dropped") || !strings.Contains(logs, "reason=held_too_long") {
		t.Fatalf("logs lack the held_too_long drop: %s", logs)
	}
	next := env.submit(t, env.groupEvent("20001", "山上好冷"))
	if next.Disposition != conloomapi.DispositionReply || next.Merged || next.TaskID == turn.TaskID {
		t.Fatalf("message after the drop = %+v, want a new turn", next)
	}
	free()
	env.waitTask(t, busy.TaskID)
}
