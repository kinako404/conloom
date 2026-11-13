package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var (
	schedConvB = store.ConversationKey("bot_test", "qq", conloomapi.ConvGroup, "conv_10002")
	schedConvC = store.ConversationKey("bot_test", "qq", conloomapi.ConvGroup, "conv_10003")
)

type schedCall struct {
	convKey string
	manual  bool
	pending int
}

// schedRunner is a fake Runner that records each call with the number of
// messages after the group's cursor, optionally blocks until released or
// cancelled, and optionally consolidates by moving the cursor to the newest
// message.
type schedRunner struct {
	st      store.Store
	advance bool
	skipped string
	block   chan struct{}
	started chan string

	active, maxActive atomic.Int32

	mu      sync.Mutex
	calls   []schedCall
	ctxErrs []error
}

func (r *schedRunner) Run(ctx context.Context, convKey string, manual bool) (Result, error) {
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		m := r.maxActive.Load()
		if n <= m || r.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	g, err := r.st.GetGroupMemory(ctx, convKey)
	if err != nil {
		return Result{}, err
	}
	pending, err := r.st.CountMessagesAfter(ctx, convKey, g.CursorSeq)
	if err != nil {
		return Result{}, err
	}
	r.mu.Lock()
	r.calls = append(r.calls, schedCall{convKey: convKey, manual: manual, pending: pending})
	r.mu.Unlock()
	if r.started != nil {
		r.started <- convKey
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			r.mu.Lock()
			r.ctxErrs = append(r.ctxErrs, ctx.Err())
			r.mu.Unlock()
			return Result{}, ctx.Err()
		}
	} else {
		time.Sleep(5 * time.Millisecond)
	}
	if r.advance {
		recent, err := r.st.RecentMessages(context.Background(), convKey, 1)
		if err != nil {
			return Result{}, err
		}
		if len(recent) > 0 {
			g.CursorSeq = recent[0].Seq
		}
		g.LastRunAt = time.Now()
		if err := r.st.PutGroupMemory(context.Background(), g); err != nil {
			return Result{}, err
		}
	}
	if r.skipped != "" {
		return Result{Skipped: r.skipped}, nil
	}
	return Result{Batches: 1, Messages: pending}, nil
}

func (r *schedRunner) takeCalls() []schedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

type schedEnv struct {
	st       store.Store
	policies *policy.Service
	runner   *schedRunner
	sched    *Scheduler
	msgs     int
}

func newSchedEnv(t *testing.T) *schedEnv {
	t.Helper()
	st := newTestStore(t)
	e := &schedEnv{st: st, policies: policy.NewService(st, conloomapi.DefaultGroupPolicy()), runner: &schedRunner{st: st}}
	e.sched = e.newScheduler()
	return e
}

func (e *schedEnv) newScheduler() *Scheduler {
	return NewScheduler(SchedulerDeps{Store: e.st, Policies: e.policies, Runner: e.runner})
}

// seed stores n messages, cycling through convKeys one message at a time.
func (e *schedEnv) seed(t *testing.T, n int, convKeys ...string) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		key := convKeys[i%len(convKeys)]
		e.msgs++
		pmid := fmt.Sprintf("%d", 500000+e.msgs)
		id := key[len("bot_test:qq:group:"):]
		_, err := e.st.Ingest(ctx, store.Ingest{
			Event: store.EventRecord{BotInstanceID: "bot_test", IdempotencyKey: "seed:" + pmid, EventID: "evt_" + pmid},
			Conversation: &store.ConversationRecord{
				BotInstanceID: "bot_test", ID: id, Key: key, Type: conloomapi.ConvGroup,
				Platform: "qq", PlatformConversationID: id[len("conv_"):],
			},
			Message: &store.MessageRecord{
				ID: "msg_" + pmid, BotInstanceID: "bot_test", ConversationKey: key, Platform: "qq",
				PlatformMessageID: pmid, ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
				Text: "你好", OccurredAt: time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
}

func (e *schedEnv) setMemory(t *testing.T, convKey string, enabled bool, backlog int) {
	t.Helper()
	pv, err := e.policies.Current(context.Background(), convKey)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	p := pv.Policy
	p.Concurrency.AllowLongRequests = true
	p.Memory.Enabled = enabled
	p.Memory.Backlog = backlog
	if _, err := e.policies.Update(context.Background(), convKey, conloomapi.PolicyPut{Policy: p}, "admin_29999"); err != nil {
		t.Fatalf("Update policy: %v", err)
	}
}

func (e *schedEnv) cursor(t *testing.T, convKey string) int64 {
	t.Helper()
	g, err := e.st.GetGroupMemory(context.Background(), convKey)
	if err != nil {
		t.Fatalf("GetGroupMemory(%s): %v", convKey, err)
	}
	return g.CursorSeq
}

func (e *schedEnv) latestSeq(t *testing.T, convKey string) int64 {
	t.Helper()
	recent, err := e.st.RecentMessages(context.Background(), convKey, 1)
	if err != nil || len(recent) != 1 {
		t.Fatalf("RecentMessages(%s) = %v, %v", convKey, recent, err)
	}
	return recent[0].Seq
}

func wantCalls(t *testing.T, got []schedCall, want ...schedCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("runner calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runner calls = %+v, want %+v", got, want)
		}
	}
}

func TestDue(t *testing.T) {
	p := conloomapi.DefaultMemoryPolicy()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		pending int
		lastRun time.Time
		want    bool
	}{
		{150, now.Add(-time.Hour), true},
		{149, now.Add(-time.Hour), false},
		{20, now.Add(-6*time.Hour - time.Minute), true},
		{19, now.Add(-7 * time.Hour), false},
		{20, time.Time{}, true},
		{25, now.Add(-5*time.Hour - 59*time.Minute), false},
	} {
		if got := due(c.pending, c.lastRun, p, now); got != c.want {
			t.Errorf("due(%d, %v) = %v, want %v", c.pending, c.lastRun, got, c.want)
		}
	}
	p.ConsolidateMin, p.ConsolidateEvery, p.ConsolidateInterval = 6, 40, conloomapi.Duration{Duration: time.Hour}
	for _, c := range []struct {
		pending int
		lastRun time.Time
		want    bool
	}{
		{6, now.Add(-time.Hour), true},
		{5, now.Add(-2 * time.Hour), false},
		{6, now.Add(-59 * time.Minute), false},
		{40, now.Add(-time.Minute), true},
		{6, time.Time{}, true},
	} {
		if got := due(c.pending, c.lastRun, p, now); got != c.want {
			t.Errorf("consolidate_min 6: due(%d, %v) = %v, want %v", c.pending, c.lastRun, got, c.want)
		}
	}
}

func TestScheduler_TickRunsDueGroupsOneAtATime(t *testing.T) {
	e := newSchedEnv(t)
	e.seed(t, 400, testConvKey, schedConvB)
	e.setMemory(t, testConvKey, true, 1000)
	e.setMemory(t, schedConvB, true, 1000)

	e.sched.tick(context.Background())

	wantCalls(t, e.runner.takeCalls(),
		schedCall{convKey: testConvKey, pending: 200}, schedCall{convKey: schedConvB, pending: 200})
	if m := e.runner.maxActive.Load(); m != 1 {
		t.Fatalf("max concurrent runs = %d, want 1", m)
	}
}

func TestScheduler_SkipsDisabledAndPausedGroups(t *testing.T) {
	e := newSchedEnv(t)
	e.seed(t, 600, testConvKey, schedConvB, schedConvC)
	e.setMemory(t, testConvKey, true, 1000)
	if _, err := e.policies.Pause(context.Background(), testConvKey, "admin_29999"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	e.setMemory(t, schedConvC, true, 1000)
	ctx := context.Background()

	e.sched.tick(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: schedConvC, pending: 200})

	e.sched.Trigger(testConvKey)
	e.sched.Trigger(schedConvB)
	e.sched.runTriggered(ctx)
	wantCalls(t, e.runner.takeCalls())
	for _, key := range []string{testConvKey, schedConvB} {
		if _, err := e.st.GetGroupMemory(ctx, key); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetGroupMemory(%s) err = %v, want ErrNotFound", key, err)
		}
	}
}

func TestScheduler_FirstEnableStartsBacklogBack(t *testing.T) {
	t.Run("backlog 1000", func(t *testing.T) {
		e := newSchedEnv(t)
		e.seed(t, 1200, testConvKey)
		e.setMemory(t, testConvKey, true, 1000)
		e.sched.tick(context.Background())
		wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, pending: 1000})
	})
	t.Run("backlog 0", func(t *testing.T) {
		e := newSchedEnv(t)
		e.seed(t, 1200, testConvKey)
		e.setMemory(t, testConvKey, true, 0)
		e.sched.tick(context.Background())
		wantCalls(t, e.runner.takeCalls())
		if got, want := e.cursor(t, testConvKey), e.latestSeq(t, testConvKey); got != want {
			t.Fatalf("cursor = %d, want latest seq %d", got, want)
		}
	})
}

func TestScheduler_BacklogJumpOnlyOnEnableTransition(t *testing.T) {
	e := newSchedEnv(t)
	e.runner.advance = true
	e.policies.OnChange(e.sched.PolicyChanged)
	ctx := context.Background()

	e.seed(t, 100, testConvKey)
	e.setMemory(t, testConvKey, true, 1000)
	e.sched.Trigger(testConvKey)
	e.sched.runTriggered(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, manual: true, pending: 100})
	if got, want := e.cursor(t, testConvKey), e.latestSeq(t, testConvKey); got != want {
		t.Fatalf("cursor after first run = %d, want %d", got, want)
	}

	e.setMemory(t, testConvKey, false, 1000)
	e.seed(t, 1500, testConvKey)
	e.setMemory(t, testConvKey, true, 1000)
	e.sched.tick(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, pending: 1000})

	e.setMemory(t, testConvKey, true, 0)
	e.seed(t, 150, testConvKey)
	e.sched.tick(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, pending: 150})

	restarted := e.newScheduler()
	e.policies.OnChange(restarted.PolicyChanged)
	e.seed(t, 150, testConvKey)
	restarted.tick(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, pending: 150})
}

func TestScheduler_BacklogCountsOwnConversationOnly(t *testing.T) {
	e := newSchedEnv(t)
	e.policies.OnChange(e.sched.PolicyChanged)
	ctx := context.Background()
	e.seed(t, 2400, testConvKey, schedConvB)
	e.setMemory(t, testConvKey, true, 1000)
	e.setMemory(t, schedConvB, true, 300)
	e.sched.tick(ctx)
	wantCalls(t, e.runner.takeCalls(),
		schedCall{convKey: testConvKey, pending: 1000}, schedCall{convKey: schedConvB, pending: 300})
}

func TestScheduler_TriggerCoalescesAndRunsManual(t *testing.T) {
	e := newSchedEnv(t)
	e.runner.block = make(chan struct{})
	e.runner.started = make(chan string, 10)
	e.seed(t, 10, testConvKey)
	e.setMemory(t, testConvKey, true, 1000)
	e.sched.Start()
	t.Cleanup(e.sched.Close)

	e.sched.Trigger(testConvKey)
	waitStarted(t, e.runner.started)
	for range 3 {
		e.sched.Trigger(testConvKey)
	}
	e.runner.block <- struct{}{}
	waitStarted(t, e.runner.started)
	e.runner.block <- struct{}{}
	select {
	case key := <-e.runner.started:
		t.Fatalf("extra run for %s", key)
	case <-time.After(200 * time.Millisecond):
	}
	wantCalls(t, e.runner.takeCalls(),
		schedCall{convKey: testConvKey, manual: true, pending: 10},
		schedCall{convKey: testConvKey, manual: true, pending: 10})
}

func waitStarted(t *testing.T, started <-chan string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
}

func TestScheduler_DailyDecayAndPurge(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	day := 24 * time.Hour
	now := time.Now().UTC().Truncate(day).Add(12 * time.Hour)
	clock := now
	e.sched = NewScheduler(SchedulerDeps{Store: e.st, Policies: e.policies, Runner: e.runner,
		Now: func() time.Time { return clock }})
	e.seed(t, 2, testConvKey, schedConvB)
	e.setMemory(t, testConvKey, true, 1000)
	lastDay := LocalDay(now.Add(-21*day), nil)
	for _, key := range []string{testConvKey, schedConvB} {
		putMember(t, e.st, store.MemberMemory{ConversationKey: key, PlatformUserID: "20001", DisplayName: "小明",
			Affinity: intPtr(30), LastInteractionDay: lastDay})
	}
	if _, err := e.st.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey, Revisions: []store.MemoryRevision{
		{ConversationKey: testConvKey, PlatformUserID: "20002", Field: store.FieldNote, OldValue: "", NewValue: "旧",
			Source: store.RevisionAPI, Reason: "old", CreatedAt: now.Add(-31 * day)},
		{ConversationKey: testConvKey, PlatformUserID: "20002", Field: store.FieldNote, OldValue: "旧", NewValue: "新",
			Source: store.RevisionAPI, Reason: "recent", CreatedAt: now.Add(-29 * day)},
	}}); err != nil {
		t.Fatalf("seed revisions: %v", err)
	}

	affinity := func(key string) int {
		t.Helper()
		m := getMember(t, e.st, key, "20001")
		if m.Affinity == nil {
			t.Fatalf("affinity of 20001 in %s is nil", key)
		}
		return *m.Affinity
	}

	e.sched.daily(ctx)
	if got := affinity(testConvKey); got != 29 {
		t.Fatalf("affinity after 21 idle days = %d, want 29", got)
	}
	revs, err := e.st.ListMemoryRevisions(ctx, testConvKey, "20001")
	if err != nil || len(revs) != 1 || revs[0].Source != store.RevisionRule || revs[0].Reason != ReasonDecay ||
		revs[0].OldValue != "30" || revs[0].NewValue != "29" {
		t.Fatalf("decay revisions = %+v, %v; want one rule/decay 30→29", revs, err)
	}
	revs, err = e.st.ListMemoryRevisions(ctx, testConvKey, "20002")
	if err != nil || len(revs) != 1 || revs[0].Reason != "recent" {
		t.Fatalf("revisions after purge = %+v, %v; want only the 29-day-old one", revs, err)
	}

	e.sched.daily(ctx)
	if got := affinity(testConvKey); got != 29 {
		t.Fatalf("affinity after second run on the same day = %d, want 29", got)
	}
	clock = now.Add(day)
	e.sched.daily(ctx)
	if got := affinity(testConvKey); got != 29 {
		t.Fatalf("affinity after 22 idle days = %d, want 29", got)
	}
	clock = now.Add(7 * day)
	e.sched.daily(ctx)
	if got := affinity(testConvKey); got != 28 {
		t.Fatalf("affinity after 28 idle days = %d, want 28", got)
	}
	if got := affinity(schedConvB); got != 30 {
		t.Fatalf("affinity in a group without memory = %d, want 30", got)
	}
}

func TestScheduler_CloseCancelsRun(t *testing.T) {
	e := newSchedEnv(t)
	e.runner.block = make(chan struct{})
	e.runner.started = make(chan string, 1)
	e.seed(t, 10, testConvKey)
	e.setMemory(t, testConvKey, true, 1000)
	e.sched.Start()
	e.sched.Trigger(testConvKey)
	waitStarted(t, e.runner.started)

	closed := make(chan struct{})
	go func() {
		e.sched.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	e.runner.mu.Lock()
	defer e.runner.mu.Unlock()
	if len(e.runner.ctxErrs) != 1 || !errors.Is(e.runner.ctxErrs[0], context.Canceled) {
		t.Fatalf("runner ctx errors = %v, want one context.Canceled", e.runner.ctxErrs)
	}
}

func TestScheduler_CloseWithoutStart(t *testing.T) {
	e := newSchedEnv(t)
	done := make(chan struct{})
	go func() {
		e.sched.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close without Start did not return")
	}
}

func TestScheduler_BackoffSkipLogsAtDebug(t *testing.T) {
	for _, c := range []struct {
		skipped   string
		infoLines int
	}{{"backoff", 0}, {"busy", 1}, {"", 1}} {
		e := newSchedEnv(t)
		e.seed(t, 200, testConvKey)
		e.setMemory(t, testConvKey, true, 1000)
		e.runner.skipped = c.skipped
		var info, debug bytes.Buffer
		e.sched = NewScheduler(SchedulerDeps{Store: e.st, Policies: e.policies, Runner: e.runner,
			Logger: slog.New(slog.NewTextHandler(&info, &slog.HandlerOptions{Level: slog.LevelInfo}))})
		e.sched.tick(context.Background())
		if got := strings.Count(info.String(), "consolidation finished"); got != c.infoLines {
			t.Errorf("skipped %q: %d Info lines, want %d:\n%s", c.skipped, got, c.infoLines, info.String())
		}
		e.sched = NewScheduler(SchedulerDeps{Store: e.st, Policies: e.policies, Runner: e.runner,
			Logger: slog.New(slog.NewTextHandler(&debug, &slog.HandlerOptions{Level: slog.LevelDebug}))})
		e.sched.tick(context.Background())
		if c.skipped == "backoff" && !strings.Contains(debug.String(), "skipped=backoff") {
			t.Errorf("backoff skip not logged at Debug:\n%s", debug.String())
		}
	}
}
