package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// setScopeStatus stores activity, from started to until, as the status of
// scope set in origin, with origin's status_return timer, and returns that
// timer.
func (e *testEnv) setScopeStatus(t *testing.T, scope, origin, activity string, started, until time.Time) store.ConversationTimer {
	t.Helper()
	timer := store.ConversationTimer{ConversationKey: origin, Kind: store.TimerStatusReturn, BotInstanceID: testBot,
		PlatformUserID: PanelActorID("admin"), DueAt: until}
	if err := e.st.SetStatus(context.Background(), store.ConversationStatus{ConversationKey: scope, OriginConversationKey: origin,
		Activity: activity, TaskID: "task_seed", StartedAt: started, UntilAt: until}, &timer); err != nil {
		t.Fatal(err)
	}
	return timer
}

// requireScopeStatus fails unless scope's status is activity set in origin.
func requireScopeStatus(t *testing.T, env *testEnv, scope, origin, activity string) {
	t.Helper()
	st, err := env.st.GetStatus(context.Background(), scope)
	if err != nil || st.Origin() != origin || st.Activity != activity {
		t.Fatalf("scope status = %+v, %v; want %s set in %s", st, err, activity, origin)
	}
}

func TestScope_ReturnToAChatWhoseStatusWasReplacedIsDropped(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	now := clock.now()
	// B's status replaced A's and has ended too; a return of A's is still
	// claimed or queued.
	env.setScopeStatus(t, scope, b, "做饭", now.Add(-30*time.Minute), now.Add(-time.Minute))
	stale := store.ConversationTimer{ConversationKey: a, Kind: store.TimerStatusReturn, BotInstanceID: testBot,
		PlatformUserID: PanelActorID("admin"), DueAt: now}
	if err := env.st.ArmTimer(ctx, stale); err != nil {
		t.Fatal(err)
	}

	if err := env.o.dueStatusReturn(ctx, stale); err != nil {
		t.Fatalf("dueStatusReturn: %v", err)
	}
	if !strings.Contains(env.logs.String(), "reason=other_chat") || env.wakeCount(t, store.TimerStatusReturn, wakeDropped) != 1 {
		t.Errorf("A's due return was not dropped as other_chat:\n%s", env.logs.String())
	}
	if _, ok := env.wakeTaskID(t, stale); ok {
		t.Error("A's due return started a task")
	}
	if got := env.timerKindsOf(t, a); len(got) != 0 {
		t.Errorf("timers of A = %v, want the dropped return gone", got)
	}
	if got := env.timerKindsOf(t, b); len(got) != 1 {
		t.Errorf("timers of B = %v, want its own return kept", got)
	}
	requireScopeStatus(t, env, scope, b, "做饭")

	if err := env.st.ArmTimer(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := env.o.fireTimer(ctx, stale); err != nil {
		t.Fatalf("fireTimer: %v", err)
	}
	id, ok := env.wakeTaskID(t, stale)
	if !ok {
		t.Fatal("fireTimer stored no task")
	}
	task := env.waitTask(t, id)
	waitUntil(t, "the return's runner", func() bool { return !env.o.sched.Busy(a) })
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultSkipped {
		t.Fatalf("return in A = %+v, want it skipped without a message", task)
	}
	if n := env.modelCalls(); n != 0 {
		t.Errorf("model calls = %d, want none", n)
	}
	if !strings.Contains(env.logs.String(), "status return dropped: a status set in another chat replaced it") {
		t.Errorf("logs lack why the return was dropped:\n%s", env.logs.String())
	}
	requireScopeStatus(t, env, scope, b, "做饭")
}

func TestScope_OnlyTheOriginChatEndsTheStatus(t *testing.T) {
	env, clock := newWakeEnv(t, nil, nil, nil)
	ctx := context.Background()
	a, b := env.createChat(t, testBot), env.createChat(t, testBot)
	scope := memory.ScopeKeyOf(a)
	now := clock.now()
	stale := func(convKey string) store.TaskRecord {
		return store.TaskRecord{ID: "task_stale", BotInstanceID: testBot, ConversationKey: convKey,
			Meta: conloomapi.TaskMeta{Trigger: TriggerStatusReturn}, EventOccurredAt: now}
	}

	env.setScopeStatus(t, scope, b, "做饭", now.Add(-30*time.Minute), now.Add(-time.Minute))
	env.o.staleWake(ctx, stale(a))
	requireScopeStatus(t, env, scope, b, "做饭")
	if err := env.o.clearOwnStatus(ctx, a); err != nil {
		t.Fatal(err)
	}
	requireScopeStatus(t, env, scope, b, "做饭")
	if got := env.timerKindsOf(t, b); len(got) != 1 {
		t.Errorf("timers of B after A's attempts = %v, want its return kept", got)
	}

	env.o.staleWake(ctx, stale(b))
	if _, err := env.st.GetStatus(ctx, scope); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("status after B's stale return: %v, want it ended", err)
	}
	if got := env.timerKindsOf(t, b); len(got) != 0 {
		t.Errorf("timers of B = %v, want its return gone with the status", got)
	}

	env.setScopeStatus(t, scope, a, "洗澡", now, now.Add(20*time.Minute))
	if err := env.o.clearOwnStatus(ctx, b); err != nil {
		t.Fatal(err)
	}
	requireScopeStatus(t, env, scope, a, "洗澡")
	if err := env.o.clearOwnStatus(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := env.st.GetStatus(ctx, scope); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("status after clearOwnStatus in A: %v, want it ended", err)
	}
	if got := env.timerKindsOf(t, a); len(got) != 0 {
		t.Errorf("timers of A = %v, want its return gone", got)
	}
	if err := env.o.clearOwnStatus(ctx, a); err != nil {
		t.Errorf("clearOwnStatus without a status: %v", err)
	}
}
