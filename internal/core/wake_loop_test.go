package core

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// localAt is 2026-11-12 (the Thursday of injectMorning) plus days, at h:m
// in the orchestrator's location.
func localAt(env *testEnv, days, h, m int) time.Time {
	return time.Date(2026, 11, 12+days, h, m, 0, 0, env.o.loc)
}

// requireNoWakeTask checks that firing timer stored no task.
func requireNoWakeTask(t *testing.T, env *testEnv, timer store.ConversationTimer) {
	t.Helper()
	if id, ok := env.wakeTaskID(t, timer); ok {
		t.Fatalf("timer stored wake task %s", id)
	}
}

func TestWakeLoop_FiresDue(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "今天下午要考试")
	env.o.replanInitiate(ctx, privateConvKey)
	initiate := env.onlyInitiate(t)

	clock.set(initiate.DueAt.Add(-time.Second))
	env.o.pollWakes(ctx)
	requireNoWakeTask(t, env, initiate)

	clock.set(initiate.DueAt)
	env.o.pollWakes(ctx)
	id, ok := env.wakeTaskID(t, initiate)
	if !ok {
		t.Fatal("the due initiate stored no task")
	}
	task := env.waitWake(t, id)
	if task.Status != conloomapi.TaskSucceeded || task.Meta.Trigger != TriggerInitiate || len(task.Actions) != 1 || task.Actions[0].Content.Text != fakeReply {
		t.Fatalf("task = %+v, want the initiate answered", task)
	}
	if got := wakeKinds(env.privateTimers(t)); !reflect.DeepEqual(got, []store.TimerKind{store.TimerFollowUp}) {
		t.Fatalf("timers = %v, want the follow-up", got)
	}
	if got := env.privateLog(t, store.ProactivePrivateInitiate); got != 1 {
		t.Errorf("private_initiate entries = %d, want 1", got)
	}
}

func TestWakeLoop_Checks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
		// prepare seeds the chat and returns the due timer to poll.
		prepare func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer
		// check runs after the poll.
		check  func(t *testing.T, env *testEnv, clock *injectClock, timer store.ConversationTimer)
		result string
	}{
		{
			name: "quiet hours re-plan an initiate outside them",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(localAt(env, 1, 2, 0))
				return env.armWake(t, store.TimerInitiate, clock.now())
			},
			check: func(t *testing.T, env *testEnv, clock *injectClock, _ store.ConversationTimer) {
				requireInitiatePlanned(t, env, clock.now(), 0, autonomy.DefaultQuiet)
			},
			result: wakePostponed,
		},
		{
			name:      "daily cap re-plans an initiate on the next day",
			configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Private.Level = conloomapi.PrivateLevelLow },
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(-time.Hour))
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(localAt(env, 0, 20, 0))
				return env.armWake(t, store.TimerInitiate, clock.now())
			},
			check: func(t *testing.T, env *testEnv, clock *injectClock, _ store.ConversationTimer) {
				next := requireInitiatePlanned(t, env, clock.now(), 0, autonomy.DefaultQuiet)
				if day := next.DueAt.In(env.o.loc).Day(); day != 13 {
					t.Errorf("re-planned initiate due %v, want on the next day", next.DueAt.In(env.o.loc))
				}
			},
			result: wakePostponed,
		},
		{
			name: "quiet hours move a follow-up to after them",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, localAt(env, 0, 23, 0))
				clock.set(localAt(env, 1, 2, 0))
				return env.armWake(t, store.TimerFollowUp, clock.now())
			},
			check: func(t *testing.T, env *testEnv, _ *injectClock, _ store.ConversationTimer) {
				timers := env.privateTimers(t)
				from := localAt(env, 1, 8, 0)
				if len(timers) != 1 || timers[0].Kind != store.TimerFollowUp || timers[0].DueAt.Before(from) || !timers[0].DueAt.Before(from.Add(time.Hour)) {
					t.Fatalf("timers = %+v, want the follow-up at 08:00–08:59", timers)
				}
			},
			result: wakePostponed,
		},
		{
			name:      "daily cap moves a follow-up to the next day",
			configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Private.Level = conloomapi.PrivateLevelLow },
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, localAt(env, 0, 12, 0))
				clock.set(localAt(env, 0, 15, 0))
				return env.armWake(t, store.TimerFollowUp, clock.now())
			},
			check: func(t *testing.T, env *testEnv, _ *injectClock, _ store.ConversationTimer) {
				requireFollowUpBetween(t, env, localAt(env, 1, 8, 0), localAt(env, 1, 9, 0))
			},
			result: wakePostponed,
		},
		{
			name: "daily cap moves a follow-up past the next night's quiet hours",
			configure: func(c *config.Config) {
				c.DefaultGroupPolicy.Proactive.Private = conloomapi.PrivateProactivePolicy{
					Level: conloomapi.PrivateLevelLow, QuietStart: "23:00", QuietEnd: "07:00"}
			},
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, localAt(env, 0, 12, 0))
				clock.set(localAt(env, 0, 15, 0))
				return env.armWake(t, store.TimerFollowUp, clock.now())
			},
			check: func(t *testing.T, env *testEnv, _ *injectClock, _ store.ConversationTimer) {
				requireFollowUpBetween(t, env, localAt(env, 1, 7, 0), localAt(env, 1, 8, 0))
			},
			result: wakePostponed,
		},
		{
			name: "a busy chat postpones by 2 minutes",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(clock.now().Add(5 * time.Hour))
				env.o.sched.Restore(QueuedTask{TaskID: "task_busy", ConversationKey: privateConvKey, ActorID: privateUser,
					EnqueuedAt: time.Now(), ReadyAt: time.Now().Add(time.Hour)})
				t.Cleanup(func() { env.o.sched.Cancel(privateConvKey, "task_busy") })
				return env.armWake(t, store.TimerInitiate, clock.now())
			},
			check: func(t *testing.T, env *testEnv, clock *injectClock, timer store.ConversationTimer) {
				requirePostponed(t, env, timer, clock.now().Add(2*time.Minute))
			},
			result: wakePostponed,
		},
		{
			name: "a message being accepted postpones by 2 minutes",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(3*time.Hour))
				clock.set(clock.now().Add(5 * time.Hour))
				t.Cleanup(env.o.speakers.begin(privateConvKey))
				return env.armWake(t, store.TimerFollowUp, clock.now())
			},
			check: func(t *testing.T, env *testEnv, clock *injectClock, timer store.ConversationTimer) {
				requirePostponed(t, env, timer, clock.now().Add(2*time.Minute))
			},
			result: wakePostponed,
		},
		{
			name: "a user message after arming drops it",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(clock.now().Add(5 * time.Hour))
				timer := env.armWake(t, store.TimerInitiate, clock.now())
				env.seedPrivateMessage(t, clock, "我醒了")
				return timer
			},
			result: wakeDropped,
		},
		{
			name: "a follow-up without an initiate is dropped",
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(clock.now().Add(5 * time.Hour))
				return env.armWake(t, store.TimerFollowUp, clock.now())
			},
			result: wakeDropped,
		},
		{
			name:      "switch off drops it",
			configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Enabled = false },
			prepare: func(t *testing.T, env *testEnv, clock *injectClock) store.ConversationTimer {
				env.seedPrivateMessage(t, clock, "晚安")
				clock.set(clock.now().Add(5 * time.Hour))
				return env.armWake(t, store.TimerInitiate, clock.now())
			},
			result: wakeDropped,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, c.configure)
			timer := c.prepare(t, env, clock)
			env.o.pollWakes(ctx)
			requireNoWakeTask(t, env, timer)
			if c.check != nil {
				c.check(t, env, clock, timer)
			} else if timers := env.privateTimers(t); len(timers) != 0 {
				t.Fatalf("timers = %+v, want the timer dropped", timers)
			}
			if n := env.wakeCount(t, timer.Kind, c.result); n != 1 {
				t.Errorf("wake %s metric = %v, want 1", c.result, n)
			}
			if n := env.modelCalls(); n != 0 {
				t.Errorf("model calls = %d, want 0", n)
			}
		})
	}
}

// requireFollowUpBetween checks that the private chat's only timer is a
// follow-up due at or after from and before to.
func requireFollowUpBetween(t *testing.T, env *testEnv, from, to time.Time) {
	t.Helper()
	timers := env.privateTimers(t)
	if len(timers) != 1 || timers[0].Kind != store.TimerFollowUp || timers[0].DueAt.Before(from) || !timers[0].DueAt.Before(to) {
		t.Fatalf("timers = %+v, want the follow-up at %v–%v", timers, from, to)
	}
}

// requirePostponed checks that the private chat's only timer is timer moved
// to due and unclaimed.
func requirePostponed(t *testing.T, env *testEnv, timer store.ConversationTimer, due time.Time) {
	t.Helper()
	timers := env.privateTimers(t)
	if len(timers) != 1 || timers[0].Kind != timer.Kind || !timers[0].DueAt.Equal(due) || !timers[0].LeaseUntil.IsZero() {
		t.Fatalf("timers = %+v, want the %s postponed to %v", timers, timer.Kind, due)
	}
}

func TestWakeLoop_ForgottenSinceLastMessageDrops(t *testing.T) {
	ctx := context.Background()
	t.Run("initiate armed while the user is forgotten", func(t *testing.T) {
		env, clock := newWakeEnv(t, nil, nil, nil)
		env.seedPrivateMessage(t, clock, "晚安")
		clock.set(clock.now().Add(time.Minute))
		// The panel forgets 小明 after the plan read his messages, before
		// it armed the initiate.
		var once sync.Once
		env.hooks.mu.Lock()
		env.hooks.beforeArm = func(store.ConversationTimer) {
			once.Do(func() {
				if err := env.st.ForgetPrivate(ctx, privateConvKey, privateUser, clock.now()); err != nil {
					t.Error(err)
				}
			})
		}
		env.hooks.mu.Unlock()
		env.o.replanInitiate(ctx, privateConvKey)
		initiate := env.onlyInitiate(t)

		clock.set(initiate.DueAt)
		env.o.pollWakes(ctx)
		requireForgottenDrop(t, env, initiate)
	})
	t.Run("follow-up armed while the user is forgotten", func(t *testing.T) {
		env, clock := newWakeEnv(t, nil, nil, nil)
		env.seedPrivateMessage(t, clock, "晚安")
		env.appendPrivateLog(t, store.ProactivePrivateInitiate, clock.now().Add(time.Hour))
		clock.set(clock.now().Add(2 * time.Hour))
		if err := env.st.ForgetPrivate(ctx, privateConvKey, privateUser, clock.now()); err != nil {
			t.Fatal(err)
		}
		followUp := env.armWake(t, store.TimerFollowUp, clock.now().Add(time.Hour))

		clock.set(followUp.DueAt)
		env.o.pollWakes(ctx)
		requireForgottenDrop(t, env, followUp)
	})
}

// requireForgottenDrop checks that the poll dropped timer without a task or
// a model call.
func requireForgottenDrop(t *testing.T, env *testEnv, timer store.ConversationTimer) {
	t.Helper()
	requireNoWakeTask(t, env, timer)
	if timers := env.privateTimers(t); len(timers) != 0 {
		t.Fatalf("timers = %+v, want the %s dropped", timers, timer.Kind)
	}
	if n := env.wakeCount(t, timer.Kind, wakeDropped); n != 1 {
		t.Errorf("wake dropped metric = %v, want 1", n)
	}
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
	if !strings.Contains(env.logs.String(), "reason=forgotten") {
		t.Errorf("logs lack the forgotten drop:\n%s", env.logs.String())
	}
}

func TestWakeLoop_StatusReturnIgnoresQuietAndCap(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, func(c *config.Config) {
		c.DefaultGroupPolicy.Proactive.Private.Level = conloomapi.PrivateLevelLow
	})
	env.seedPrivateMessage(t, clock, "我去睡一会儿")
	start := localAt(env, 0, 23, 50)
	until := localAt(env, 1, 1, 0)
	// The cap of 低 is already reached on the day the status ends.
	env.appendPrivateLog(t, store.ProactivePrivateInitiate, localAt(env, 1, 0, 10))
	timer := env.setWakeStatus(t, "午睡", start, until)
	clock.set(until)

	env.o.pollWakes(context.Background())
	id, ok := env.wakeTaskID(t, timer)
	if !ok {
		t.Fatal("the status return stored no task")
	}
	if task := env.waitWake(t, id); len(task.Actions) != 1 || task.Meta.Trigger != TriggerStatusReturn {
		t.Fatalf("task = %+v, want the status return answered", task)
	}
	requireNoStatus(t, env)
}

func TestWakeLoop_StatusReturnSwitchOffClearsStatus(t *testing.T) {
	for _, c := range []struct {
		name      string
		configure func(*config.Config)
		meddle    func(t *testing.T, env *testEnv)
	}{
		{name: "switch off", configure: func(c *config.Config) { c.DefaultGroupPolicy.Proactive.Enabled = false }},
		{name: "paused", meddle: func(t *testing.T, env *testEnv) {
			if _, err := env.policies.Pause(context.Background(), privateConvKey, "29999"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, clock := newWakeEnv(t, nil, nil, c.configure)
			env.seedPrivateMessage(t, clock, "我去洗个澡")
			until := clock.now().Add(20 * time.Minute)
			timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
			if c.meddle != nil {
				c.meddle(t, env)
			}
			clock.set(until)
			env.o.pollWakes(context.Background())
			requireNoWakeTask(t, env, timer)
			requireNoStatus(t, env)
			if timers := env.privateTimers(t); len(timers) != 0 {
				t.Fatalf("timers = %+v, want none", timers)
			}
			if n := env.wakeCount(t, store.TimerStatusReturn, wakeDropped); n != 1 {
				t.Errorf("wake dropped metric = %v, want 1", n)
			}
		})
	}
}

func TestWakeLoop_StatusReturnBusyPostponesUpToHalfAnHour(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	until := clock.now().Add(20 * time.Minute)
	timer := env.setWakeStatus(t, "洗澡", clock.now(), until)
	t.Cleanup(env.o.speakers.begin(privateConvKey))

	clock.set(until)
	env.o.pollWakes(ctx)
	requirePostponed(t, env, timer, until.Add(time.Minute))

	clock.set(until.Add(29 * time.Minute))
	env.o.pollWakes(ctx)
	requirePostponed(t, env, timer, until.Add(30*time.Minute))

	clock.set(until.Add(30 * time.Minute))
	env.o.pollWakes(ctx)
	requireNoStatus(t, env)
	if timers := env.privateTimers(t); len(timers) != 0 {
		t.Fatalf("timers = %+v, want none", timers)
	}
	if n := env.wakeCount(t, store.TimerStatusReturn, wakePostponed); n != 2 {
		t.Errorf("wake postponed metric = %v, want 2", n)
	}
	if n := env.wakeCount(t, store.TimerStatusReturn, wakeDropped); n != 1 {
		t.Errorf("wake dropped metric = %v, want 1", n)
	}
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestWakeLoop_MissedTimers(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	clock.set(clock.now().Add(5 * time.Hour))
	due := clock.now()
	status := env.setWakeStatus(t, "洗澡", due.Add(-20*time.Minute), due)
	initiate := env.armWake(t, store.TimerInitiate, due)
	followUp := env.armWake(t, store.TimerFollowUp, due)

	clock.set(due.Add(wakeMissedGrace + time.Minute))
	env.o.pollWakes(ctx)
	for _, timer := range []store.ConversationTimer{status, initiate, followUp} {
		requireNoWakeTask(t, env, timer)
		if n := env.wakeCount(t, timer.Kind, wakeMissed); n != 1 {
			t.Errorf("%s missed metric = %v, want 1", timer.Kind, n)
		}
	}
	requireNoStatus(t, env)
	next := requireInitiatePlanned(t, env, clock.now(), 0, autonomy.DefaultQuiet)
	if !next.DueAt.After(clock.now()) {
		t.Errorf("re-planned initiate due %v, want in the future", next.DueAt)
	}
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want 0", n)
	}
}

func TestWakeLoop_StopsOnClose(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	env.o.wakeEvery = 10 * time.Millisecond
	env.seedPrivateMessage(t, clock, "我去洗个澡")
	if err := env.o.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	until := clock.now().Add(20 * time.Minute)
	first := env.setWakeStatus(t, "洗澡", clock.now(), until)
	clock.set(until)
	waitUntil(t, "the loop to fire the status return", func() bool {
		_, ok := env.wakeTaskID(t, first)
		return ok
	})
	id, _ := env.wakeTaskID(t, first)
	env.waitWake(t, id)

	env.o.Close()
	second := env.setWakeStatus(t, "午睡", clock.now(), clock.now().Add(10*time.Minute))
	clock.set(clock.now().Add(10 * time.Minute))
	time.Sleep(100 * time.Millisecond)
	requireNoWakeTask(t, env, second)
	env.o.pollWakes(ctx)
	for _, timer := range env.privateTimers(t) {
		if timer.Kind == store.TimerStatusReturn && (!timer.DueAt.Equal(second.DueAt) || !timer.LeaseUntil.IsZero()) {
			t.Fatalf("status return = %+v, want it unclaimed after Close", timer)
		}
	}
	if timers := env.privateTimers(t); !slices.ContainsFunc(timers, func(t store.ConversationTimer) bool { return t.Kind == store.TimerStatusReturn }) {
		t.Fatalf("timers = %+v, want the status return kept", timers)
	}
}
