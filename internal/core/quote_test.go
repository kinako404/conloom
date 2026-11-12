package core

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func withQuote(mode string) func(*config.Config) {
	return func(c *config.Config) { c.DefaultGroupPolicy.Reply.Quote = mode }
}

// modelGate holds every model call until release is called (or waitLimit
// passes), signalling on entered when a call arrives.
type modelGate struct {
	entered chan struct{}
	ch      chan struct{}
	once    sync.Once
}

func newModelGate() *modelGate {
	return &modelGate{entered: make(chan struct{}, 8), ch: make(chan struct{})}
}

func (g *modelGate) handler(map[string]any) (int, string) {
	g.entered <- struct{}{}
	select {
	case <-g.ch:
	case <-time.After(waitLimit):
	}
	return 200, fakeReply
}

func (g *modelGate) release() { g.once.Do(func() { close(g.ch) }) }

func (g *modelGate) wait(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for the model call")
	}
}

// gatedMention has 20001 mention the bot, runs between while the model call
// is held, and returns the finished task's single action.
func gatedMention(t *testing.T, opts testOpts, between func(env *testEnv)) (conloomapi.Action, conloomapi.Event, *testEnv) {
	t.Helper()
	g := newModelGate()
	opts.short, opts.long = g.handler, g.handler
	env := newTestOrchestrator(t, opts)
	t.Cleanup(g.release)
	ev := env.groupEvent("20001", "帮我看看这个", mentionBot)
	resp := env.submit(t, ev)
	g.wait(t)
	if between != nil {
		between(env)
	}
	g.release()
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	return task.Actions[0], ev, env
}

func requireQuoted(t *testing.T, a conloomapi.Action, ev conloomapi.Event) {
	t.Helper()
	if a.ReplyToMessageID != ev.Message.ID || a.ReplyToPlatformMessageID != ev.Message.PlatformMessageID {
		t.Fatalf("reply target = %q/%q, want %q/%q", a.ReplyToMessageID, a.ReplyToPlatformMessageID,
			ev.Message.ID, ev.Message.PlatformMessageID)
	}
}

func requireUnquoted(t *testing.T, a conloomapi.Action) {
	t.Helper()
	if a.ReplyToMessageID != "" || a.ReplyToPlatformMessageID != "" {
		t.Fatalf("reply target = %q/%q, want none", a.ReplyToMessageID, a.ReplyToPlatformMessageID)
	}
}

func TestQuote_AutoQuietGroupNoQuote(t *testing.T) {
	a, _, env := gatedMention(t, testOpts{}, nil)
	requireUnquoted(t, a)

	resp := env.submit(t, env.groupEvent("20002", "/help"))
	task := env.getTask(t, resp.TaskID)
	if len(task.Actions) != 1 {
		t.Fatalf("command task = %+v", task)
	}
	requireUnquoted(t, task.Actions[0])
}

func TestQuote_AutoOtherMemberSpokeAfterTrigger(t *testing.T) {
	a, ev, _ := gatedMention(t, testOpts{}, func(env *testEnv) {
		requireObserved(t, env, env.groupEvent("20002", "我也想问"), "")
	})
	requireQuoted(t, a, ev)
}

func TestQuote_AutoSenderFollowUpAndBotMessagesDoNotCount(t *testing.T) {
	a, _, _ := gatedMention(t, testOpts{}, func(env *testEnv) {
		requireObserved(t, env, env.groupEvent("20001", "补充一下"), "")
		env.storeBotMessage(t, time.Now())
		requireObserved(t, env, env.groupEvent("20001", "就是这样"), "")
	})
	requireUnquoted(t, a)
}

func TestQuote_AutoOldTriggerQuoted(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	a, ev, _ := gatedMention(t, testOpts{now: now}, func(*testEnv) {
		offset.Store(int64(61 * time.Second))
	})
	requireQuoted(t, a, ev)
}

func TestQuote_AutoRecentTriggerWithinMinute(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	a, _, _ := gatedMention(t, testOpts{now: now}, func(*testEnv) {
		offset.Store(int64(50 * time.Second))
	})
	requireUnquoted(t, a)
}

func TestQuote_LookupErrorFallsBackToQuote(t *testing.T) {
	a, ev, env := gatedMention(t, testOpts{}, func(env *testEnv) {
		env.hooks.failRecent.Store(true)
	})
	requireQuoted(t, a, ev)
	logs := env.logs.String()
	if !strings.Contains(logs, "recent messages unavailable") {
		t.Errorf("lookup error not logged: %s", logs)
	}
	if strings.Contains(logs, "帮我看看这个") {
		t.Errorf("message text leaked into logs: %s", logs)
	}
}

func TestQuote_AlwaysAndNever(t *testing.T) {
	t.Run("always quotes in a quiet group", func(t *testing.T) {
		a, ev, _ := gatedMention(t, testOpts{configure: withQuote(conloomapi.QuoteAlways)}, nil)
		requireQuoted(t, a, ev)
	})
	t.Run("never quotes even after other members spoke", func(t *testing.T) {
		var offset atomic.Int64
		now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
		a, _, _ := gatedMention(t, testOpts{configure: withQuote(conloomapi.QuoteNever), now: now}, func(env *testEnv) {
			requireObserved(t, env, env.groupEvent("20002", "我也想问"), "")
			offset.Store(int64(2 * time.Minute))
		})
		requireUnquoted(t, a)
	})
}

func TestQuote_PrivateNeverQuoted(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: withQuote(conloomapi.QuoteAlways)})
	resp := env.submit(t, env.event(conloomapi.ConvPrivate, "29999", "29999", "在吗"))
	task := env.waitTask(t, resp.TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	requireUnquoted(t, task.Actions[0])
}

func TestQuote_JoinUnderAuto(t *testing.T) {
	for _, c := range []struct {
		name    string
		between bool
	}{{"quiet after target", false}, {"member spoke after target", true}} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: joinConfig(time.Hour, nil)})
			env.seedMember(t, "20001", conloomapi.ProactiveLow, time.Now().Add(-time.Hour))
			target := env.storeMessage(t, "20001", matchText, "")
			if c.between {
				env.storeMessage(t, "20002", "好的", "")
			}
			id, err := env.o.createJoinTask(context.Background(), testConvKey, target, joinPlan{}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			task := env.waitTask(t, id)
			if len(task.Actions) != 1 {
				t.Fatalf("join task = %+v", task)
			}
			a := task.Actions[0]
			if !c.between {
				requireUnquoted(t, a)
				return
			}
			if a.ReplyToMessageID != target.ID || a.ReplyToPlatformMessageID != target.PlatformMessageID {
				t.Fatalf("join reply target = %q/%q, want %q", a.ReplyToMessageID, a.ReplyToPlatformMessageID, target.ID)
			}
		})
	}
}
