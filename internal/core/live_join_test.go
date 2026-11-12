package core

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// offsetClock is a clock that tests move forward.
type offsetClock struct{ off atomic.Int64 }

func (c *offsetClock) now() time.Time             { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *offsetClock) advance(d time.Duration)    { c.off.Add(int64(d)) }
func (c *offsetClock) stamp(ev *conloomapi.Event) { ev.OccurredAt = c.now().UTC() }

// liveConfig is the live group's policy: a 3 minute session, the default
// join cooldown and minimum messages, and lulls the test triggers itself.
func liveConfig(c *config.Config) {
	c.DefaultGroupPolicy.Reply.ContinuationWindow = conloomapi.Duration{Duration: 3 * time.Minute}
	c.DefaultGroupPolicy.Reply.TurnGap = conloomapi.Duration{Duration: 100 * time.Millisecond}
	c.DefaultGroupPolicy.Reply.TurnMaxWait = conloomapi.Duration{Duration: 400 * time.Millisecond}
	p := &c.DefaultGroupPolicy.Proactive
	p.JoinLull = conloomapi.Duration{Duration: time.Hour}
	p.JoinCooldown = conloomapi.Duration{Duration: 10 * time.Minute}
	p.JoinMinMessages = 4
}

// TestJoin_LiveSequenceAfterRuleExit replays a live sequence: a member's
// first mention gets a reply, they leave the session by replying to another
// member, and once the join cooldown passed the bot joins with their newest
// message as target. A member who never engaged the bot replied to that
// member meanwhile: that reply is never the target, and their follow-ups stay
// observed, while the target's plain follow-up is a turn of the join session.
func TestJoin_LiveSequenceAfterRuleExit(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{configure: liveConfig, now: clock.now})
	say := func(user, text string, opts ...evOpt) (conloomapi.Event, conloomapi.EventAccepted) {
		t.Helper()
		ev := env.groupEvent(user, text, append(opts, clock.stamp)...)
		return ev, env.submit(t, ev)
	}
	inSession := func(user string) bool { return env.o.inSession(testConvKey, user, clock.now(), 3*time.Minute) }

	_, resp := say("20001", "", mentionBot)
	if task := env.waitTask(t, resp.TaskID); len(task.Actions) != 1 {
		t.Fatalf("mention task = %+v", task)
	}
	waitUntil(t, "the mention's session", func() bool { return inSession("20001") })

	clock.advance(90 * time.Second)
	other, _ := say("20003", "今天打什么游戏")
	if _, resp := say("20001", "我也想知道", replyTo(other.Message.ID)); resp.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("reply to another member: %+v, want observe", resp)
	}
	if inSession("20001") || env.sessionActive() {
		t.Fatal("session kept after its only participant replied to another member")
	}

	clock.advance(9 * time.Minute)
	say("20002", "打游戏")
	say("20003", "好玩")
	say("20001", "我在打这个")
	target, _ := say("20001", "难吗")
	if _, resp := say("20002", "厉害。", replyTo(target.Message.ID)); resp.Disposition != conloomapi.DispositionObserve {
		t.Fatalf("reply to another member by a non-participant: %+v, want observe", resp)
	}
	clock.advance(15 * time.Second)

	id, reason := env.o.considerJoin(testConvKey)
	if id == "" {
		t.Fatalf("considerJoin did not join: %s", reason)
	}
	task := env.waitTask(t, id)
	if len(task.Actions) != 1 || joinTarget(t, env, id) != target.Message.ID || task.Actions[0].ReplyToMessageID != target.Message.ID {
		t.Fatalf("join task = %+v, want a reply quoting %s", task, target.Message.ID)
	}
	env.waitIdle(t)

	clock.advance(50 * time.Second)
	for _, text := range []string{"好吧。", "走，打游戏去。"} {
		if _, resp := say("20002", text); resp.Disposition != conloomapi.DispositionObserve || resp.TaskID != "" {
			t.Fatalf("%q from a member who never engaged the bot: %+v, want observe", text, resp)
		}
	}
	if !inSession("20001") || inSession("20002") {
		t.Fatal("want only the join target in the join session")
	}
	_, resp = say("20001", "继续打")
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("join target's follow-up: %+v, want a session turn", resp)
	}
	if task := env.waitTask(t, resp.TaskID); task.Meta.Trigger != conloomapi.TriggerContinuation {
		t.Fatalf("follow-up trigger = %q, want continuation", task.Meta.Trigger)
	}
}
