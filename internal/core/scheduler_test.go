package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const waitLimit = 3 * time.Second

type schedRun struct {
	task       QueuedTask
	start, end time.Time
	ctxErr     error
}

// schedRecorder is a Runner that records every run. If hold is non-nil the
// runner blocks until hold is closed or its ctx is cancelled; otherwise it
// sleeps for sleep.
type schedRecorder struct {
	mu      sync.Mutex
	runs    []schedRun
	started chan QueuedTask
	sleep   time.Duration
	hold    chan struct{}
}

func newSchedRecorder() *schedRecorder {
	return &schedRecorder{started: make(chan QueuedTask, 64)}
}

func (r *schedRecorder) run(ctx context.Context, t QueuedTask) {
	start := time.Now()
	r.started <- t
	if r.hold != nil {
		select {
		case <-r.hold:
		case <-ctx.Done():
		}
	} else {
		time.Sleep(r.sleep)
	}
	r.mu.Lock()
	r.runs = append(r.runs, schedRun{task: t, start: start, end: time.Now(), ctxErr: ctx.Err()})
	r.mu.Unlock()
}

func (r *schedRecorder) finished() []schedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.runs)
}

func (r *schedRecorder) waitFinished(t *testing.T, n int) []schedRun {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		if runs := r.finished(); len(runs) >= n {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d finished runs, have %d", n, len(r.finished()))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *schedRecorder) waitStarted(t *testing.T) QueuedTask {
	t.Helper()
	select {
	case qt := <-r.started:
		return qt
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting for a task to start")
		return QueuedTask{}
	}
}

func (r *schedRecorder) assertNotStarted(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case qt := <-r.started:
		t.Fatalf("task %s started unexpectedly", qt.TaskID)
	case <-time.After(d):
	}
}

func fixedCfg(c SessionConfig) func(string) SessionConfig {
	return func(string) SessionConfig { return c }
}

func nopPersist(AdmitPlan) error { return nil }

func newTestScheduler(t *testing.T, run Runner, cfg func(string) SessionConfig) *Scheduler {
	t.Helper()
	s := NewScheduler(run, cfg, time.Minute)
	t.Cleanup(s.Close)
	return s
}

func mustAdmit(t *testing.T, s *Scheduler, conv, actor, msg string, mode AdmitMode) AdmitResult {
	t.Helper()
	res, err := s.Admit(conv, actor, msg, mode, nopPersist)
	if err != nil {
		t.Fatalf("Admit(%s, %s, %s): %v", conv, actor, msg, err)
	}
	return res
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestScheduler_SerialWithinConversation(t *testing.T) {
	rec := newSchedRecorder()
	rec.sleep = 30 * time.Millisecond
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	r2 := mustAdmit(t, s, "group:10001", "20002", "m2", AdmitNew)
	if !r1.Created || !r2.Created || r1.TaskID == r2.TaskID {
		t.Fatalf("expected two created tasks, got %+v %+v", r1, r2)
	}

	runs := rec.waitFinished(t, 2)
	if runs[0].task.TaskID != r1.TaskID || runs[1].task.TaskID != r2.TaskID {
		t.Fatalf("run order = %s,%s; want %s,%s", runs[0].task.TaskID, runs[1].task.TaskID, r1.TaskID, r2.TaskID)
	}
	if runs[1].start.Before(runs[0].end) {
		t.Fatalf("runs overlap: t2 started %v before t1 ended", runs[0].end.Sub(runs[1].start))
	}
}

func TestScheduler_ConcurrencyTwoOverlaps(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 2, MaxQueued: 10}))

	mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	mustAdmit(t, s, "group:10001", "20002", "m2", AdmitNew)

	rec.waitStarted(t)
	rec.waitStarted(t)
	if n := len(rec.finished()); n != 0 {
		t.Fatalf("%d runs finished before release; want both running together", n)
	}
	if n := len(s.Active("group:10001")); n != 2 {
		t.Fatalf("Active = %d tasks, want 2", n)
	}
	close(rec.hold)
	rec.waitFinished(t, 2)
}

func TestScheduler_ConversationsIndependent(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	recA := newSchedRecorder()
	recA.hold = hold
	recB := newSchedRecorder()
	run := func(ctx context.Context, qt QueuedTask) {
		if qt.ConversationKey == "group:A" {
			recA.run(ctx, qt)
			return
		}
		recB.run(ctx, qt)
	}
	s := newTestScheduler(t, run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	mustAdmit(t, s, "group:A", "20001", "a1", AdmitNew)
	recA.waitStarted(t)

	rb := mustAdmit(t, s, "group:B", "20001", "b1", AdmitNew)
	runs := recB.waitFinished(t, 1)
	if runs[0].task.TaskID != rb.TaskID {
		t.Fatalf("B ran %s, want %s", runs[0].task.TaskID, rb.TaskID)
	}
	if n := len(recA.finished()); n != 0 {
		t.Fatalf("A finished %d runs while held", n)
	}
}

func TestScheduler_DebounceMergesSameActor(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 1, MaxQueued: 10, Debounce: 200 * time.Millisecond, MaxMergeWindow: time.Second,
	}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	var plan AdmitPlan
	r2, err := s.Admit("group:10001", "20001", "m2", AdmitNew, func(p AdmitPlan) error {
		plan = p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.Created || r1.Merged {
		t.Fatalf("first result = %+v, want Created", r1)
	}
	if !r2.Merged || r2.Created || r2.TaskID != r1.TaskID {
		t.Fatalf("second result = %+v, want Merged into %s", r2, r1.TaskID)
	}
	if plan != (AdmitPlan{MergeIntoTaskID: r1.TaskID}) {
		t.Fatalf("persist plan = %+v, want merge into %s", plan, r1.TaskID)
	}

	runs := rec.waitFinished(t, 1)
	if got := runs[0].task.TriggerMessageIDs; !slices.Equal(got, []string{"m1", "m2"}) {
		t.Fatalf("TriggerMessageIDs = %v, want [m1 m2]", got)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(rec.finished()); n != 1 {
		t.Fatalf("runner ran %d times, want 1", n)
	}
}

func TestScheduler_DifferentActorNotMerged(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 2, MaxQueued: 10, Debounce: 100 * time.Millisecond, MaxMergeWindow: time.Second,
	}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	r2 := mustAdmit(t, s, "group:10001", "20002", "m2", AdmitNew)
	if !r1.Created || !r2.Created || r2.Merged || r1.TaskID == r2.TaskID {
		t.Fatalf("expected two separate tasks, got %+v %+v", r1, r2)
	}

	runs := rec.waitFinished(t, 2)
	got := map[string][]string{}
	for _, r := range runs {
		got[r.task.ActorID] = r.task.TriggerMessageIDs
	}
	if !slices.Equal(got["20001"], []string{"m1"}) || !slices.Equal(got["20002"], []string{"m2"}) {
		t.Fatalf("triggers by actor = %v", got)
	}
}

func TestScheduler_MergeOnlyWithoutWindowIsObserve(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 0, MaxQueued: 10, Debounce: time.Second, MaxMergeWindow: 2 * time.Second,
	}))

	calls := 0
	var plan AdmitPlan
	persist := func(p AdmitPlan) error {
		calls++
		plan = p
		return nil
	}

	res, err := s.Admit("group:10001", "20001", "m1", AdmitMergeOnly, persist)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || plan != (AdmitPlan{}) {
		t.Fatalf("persist calls=%d plan=%+v, want 1 call with empty plan", calls, plan)
	}
	if res != (AdmitResult{}) {
		t.Fatalf("result = %+v, want zero (observe)", res)
	}
	if n := len(s.Active("group:10001")); n != 0 {
		t.Fatalf("Active = %d tasks, want 0", n)
	}

	r1 := mustAdmit(t, s, "group:10001", "20001", "m2", AdmitNew)
	res, err = s.Admit("group:10001", "20002", "m3", AdmitMergeOnly, persist)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || plan != (AdmitPlan{}) || res.TaskID != "" {
		t.Fatalf("other actor merge-only: calls=%d plan=%+v res=%+v, want observe", calls, plan, res)
	}
	res, err = s.Admit("group:10001", "20001", "m4", AdmitMergeOnly, persist)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged || res.TaskID != r1.TaskID || plan.MergeIntoTaskID != r1.TaskID {
		t.Fatalf("same actor merge-only: res=%+v plan=%+v, want merged into %s", res, plan, r1.TaskID)
	}
}

func TestScheduler_MaxMergeWindowCaps(t *testing.T) {
	const (
		debounce = 200 * time.Millisecond
		maxMerge = 500 * time.Millisecond
	)
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 1, MaxQueued: 10, Debounce: debounce, MaxMergeWindow: maxMerge,
	}))

	before := time.Now()
	r1 := mustAdmit(t, s, "group:10001", "20001", "m0", AdmitNew)

	var started QueuedTask
	deadline := time.After(waitLimit)
loop:
	for i := 1; ; i++ {
		select {
		case started = <-rec.started:
			break loop
		case <-deadline:
			t.Fatal("task never started while messages kept arriving")
		case <-time.After(debounce / 2):
			mustAdmit(t, s, "group:10001", "20001", "m"+string(rune('0'+i%10)), AdmitMergeOnly)
		}
	}
	elapsed := time.Since(before)

	if started.TaskID != r1.TaskID {
		t.Fatalf("started %s, want %s", started.TaskID, r1.TaskID)
	}
	if elapsed < maxMerge {
		t.Fatalf("task started after %v, before MaxMergeWindow %v", elapsed, maxMerge)
	}
	if elapsed > maxMerge+time.Second {
		t.Fatalf("task started after %v, far beyond MaxMergeWindow %v", elapsed, maxMerge)
	}
	if n := len(started.TriggerMessageIDs); n < 3 {
		t.Fatalf("task merged %d messages, want several", n)
	}
	if got := started.ReadyAt.Sub(started.EnqueuedAt); got != maxMerge {
		t.Fatalf("ReadyAt-EnqueuedAt = %v, want capped at %v", got, maxMerge)
	}
}

func TestScheduler_MinReplyIntervalDelays(t *testing.T) {
	const interval = 100 * time.Millisecond
	rec := newSchedRecorder()
	rec.sleep = 10 * time.Millisecond
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 1, MaxQueued: 10, MinReplyInterval: interval,
	}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	r2 := mustAdmit(t, s, "group:10001", "20002", "m2", AdmitNew)

	runs := rec.waitFinished(t, 2)
	if runs[0].task.TaskID != r1.TaskID || runs[1].task.TaskID != r2.TaskID {
		t.Fatalf("run order wrong: %s,%s", runs[0].task.TaskID, runs[1].task.TaskID)
	}
	if gap := runs[1].start.Sub(runs[0].end); gap < interval {
		t.Fatalf("second task started %v after first ended, want >= %v", gap, interval)
	}
}

func TestScheduler_PausedHoldsThenKickRuns(t *testing.T) {
	var maxConc atomic.Int32
	cfg := func(string) SessionConfig {
		return SessionConfig{MaxConcurrent: int(maxConc.Load()), MaxQueued: 10}
	}
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, cfg)

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	rec.assertNotStarted(t, 100*time.Millisecond)
	active := s.Active("group:10001")
	if len(active) != 1 || active[0].TaskID != r1.TaskID {
		t.Fatalf("Active = %+v, want the paused task", active)
	}

	maxConc.Store(1)
	s.Kick("group:10001")
	if got := rec.waitStarted(t); got.TaskID != r1.TaskID {
		t.Fatalf("started %s, want %s", got.TaskID, r1.TaskID)
	}
}

func TestScheduler_QueueFull(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 0, MaxQueued: 1, Debounce: time.Second, MaxMergeWindow: 2 * time.Second,
	}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)

	called := false
	_, err := s.Admit("group:10001", "20002", "m2", AdmitNew, func(AdmitPlan) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	if called {
		t.Fatal("persist called for a full queue")
	}
	if n := len(s.Active("group:10001")); n != 1 {
		t.Fatalf("Active = %d tasks, want 1", n)
	}

	res := mustAdmit(t, s, "group:10001", "20001", "m3", AdmitNew)
	if !res.Merged || res.TaskID != r1.TaskID {
		t.Fatalf("merge into open window with full queue = %+v, want merged into %s", res, r1.TaskID)
	}
}

func TestScheduler_CancelPendingAndRunning(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	defer close(rec.hold)
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	r1 := mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	r2 := mustAdmit(t, s, "group:10001", "20002", "m2", AdmitNew)
	rec.waitStarted(t)

	if !s.Cancel("group:10001", r2.TaskID) {
		t.Fatal("Cancel(pending) = false")
	}
	if !s.Cancel("group:10001", r1.TaskID) {
		t.Fatal("Cancel(running) = false")
	}
	runs := rec.waitFinished(t, 1)
	if runs[0].task.TaskID != r1.TaskID || runs[0].ctxErr == nil {
		t.Fatalf("running task finished with ctx.Err()=%v, want cancelled", runs[0].ctxErr)
	}

	rec.assertNotStarted(t, 100*time.Millisecond)
	waitUntil(t, "no active tasks", func() bool { return len(s.Active("group:10001")) == 0 })
	if s.Cancel("group:10001", r2.TaskID) {
		t.Fatal("Cancel of a removed task = true")
	}
	if s.Cancel("group:none", "task_x") {
		t.Fatal("Cancel in unknown conversation = true")
	}
}

func TestScheduler_PersistErrorLeavesNoState(t *testing.T) {
	boom := errors.New("boom")
	failing := func(AdmitPlan) error { return boom }
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 1, MaxQueued: 10, Debounce: 50 * time.Millisecond, MaxMergeWindow: time.Second,
	}))

	res, err := s.Admit("group:10001", "20001", "m1", AdmitNew, failing)
	if !errors.Is(err, boom) || res != (AdmitResult{}) {
		t.Fatalf("Admit = %+v, %v; want zero result and boom", res, err)
	}
	if n := len(s.Active("group:10001")); n != 0 {
		t.Fatalf("Active = %d tasks after failed persist, want 0", n)
	}
	rec.assertNotStarted(t, 150*time.Millisecond)

	s2 := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 0, MaxQueued: 10, Debounce: time.Second, MaxMergeWindow: 2 * time.Second,
	}))
	r1 := mustAdmit(t, s2, "group:10001", "20001", "m1", AdmitNew)
	before := s2.Active("group:10001")
	if _, err := s2.Admit("group:10001", "20001", "m2", AdmitNew, failing); !errors.Is(err, boom) {
		t.Fatalf("merge Admit err = %v, want boom", err)
	}
	after := s2.Active("group:10001")
	if len(after) != 1 || after[0].TaskID != r1.TaskID ||
		!slices.Equal(after[0].TriggerMessageIDs, []string{"m1"}) || !after[0].ReadyAt.Equal(before[0].ReadyAt) {
		t.Fatalf("task changed by failed merge: before %+v after %+v", before, after)
	}
}

func TestScheduler_AdmitWithMergeOptions(t *testing.T) {
	const (
		debounce = 20 * time.Millisecond
		gap      = 300 * time.Millisecond
		maxWait  = 800 * time.Millisecond
	)
	rec := newSchedRecorder()
	rec.sleep = 200 * time.Millisecond
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 1, MaxQueued: 10, Debounce: debounce, MaxMergeWindow: time.Minute,
	}))
	pending := func(taskID string) QueuedTask {
		t.Helper()
		for _, qt := range s.Active("group:10001") {
			if qt.TaskID == taskID {
				return qt
			}
		}
		t.Fatalf("task %s not active", taskID)
		return QueuedTask{}
	}

	turn, err := s.AdmitWith("group:10001", "20001", "t1", AdmitNew, MergeOptions{Debounce: gap, MaxMergeWindow: maxWait}, nopPersist)
	if err != nil || !turn.Created {
		t.Fatalf("AdmitWith = %+v, %v; want a created task", turn, err)
	}
	if qt := pending(turn.TaskID); qt.ReadyAt.Sub(qt.EnqueuedAt) != gap {
		t.Fatalf("ReadyAt-EnqueuedAt = %v, want the task's gap %v", qt.ReadyAt.Sub(qt.EnqueuedAt), gap)
	}
	time.Sleep(4 * debounce)
	// A later merge keeps using the gap the task was created with, even when
	// the caller passes none.
	merged := mustAdmit(t, s, "group:10001", "20001", "t2", AdmitMergeOnly)
	if !merged.Merged || merged.TaskID != turn.TaskID {
		t.Fatalf("merge after the conversation's debounce = %+v, want merged into %s", merged, turn.TaskID)
	}
	qt := pending(turn.TaskID)
	if wait := time.Until(qt.ReadyAt); wait < gap-100*time.Millisecond || wait > gap {
		t.Fatalf("ReadyAt in %v after a merge, want about the gap %v", wait, gap)
	}

	var started QueuedTask
	deadline := time.After(waitLimit)
loop:
	for i := 3; ; i++ {
		select {
		case started = <-rec.started:
			break loop
		case <-deadline:
			t.Fatal("turn never started while messages kept arriving")
		case <-time.After(gap / 3):
			mustAdmit(t, s, "group:10001", "20001", "t"+strconv.Itoa(i), AdmitMergeOnly)
		}
	}
	if started.TaskID != turn.TaskID {
		t.Fatalf("started %s, want %s", started.TaskID, turn.TaskID)
	}
	if got := started.ReadyAt.Sub(started.EnqueuedAt); got != maxWait {
		t.Fatalf("ReadyAt-EnqueuedAt = %v, want capped at the task's max wait %v", got, maxWait)
	}
	rec.waitFinished(t, 1)

	plain := mustAdmit(t, s, "group:10001", "20002", "p1", AdmitNew)
	if qt := pending(plain.TaskID); qt.ReadyAt.Sub(qt.EnqueuedAt) != debounce {
		t.Fatalf("Admit ReadyAt-EnqueuedAt = %v, want the conversation's debounce %v", qt.ReadyAt.Sub(qt.EnqueuedAt), debounce)
	}
	half, err := s.AdmitWith("group:10001", "20003", "h1", AdmitNew, MergeOptions{Debounce: gap}, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	if qt := pending(half.TaskID); qt.ReadyAt.Sub(qt.EnqueuedAt) != gap {
		t.Fatalf("partial options ReadyAt-EnqueuedAt = %v, want %v", qt.ReadyAt.Sub(qt.EnqueuedAt), gap)
	}
	rec.waitFinished(t, 3)
}

func TestScheduler_AddressedMergeReadyWithinDebounce(t *testing.T) {
	const debounce = 100 * time.Millisecond
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{
		MaxConcurrent: 3, MaxQueued: 10, Debounce: debounce, MaxMergeWindow: time.Minute,
	}))
	pending := func(taskID string) QueuedTask {
		t.Helper()
		for _, qt := range s.Active("group:10001") {
			if qt.TaskID == taskID {
				return qt
			}
		}
		t.Fatalf("task %s not active", taskID)
		return QueuedTask{}
	}
	addressed := func(actor, msg string) AdmitResult {
		t.Helper()
		res, err := s.AdmitWith("group:10001", actor, msg, AdmitNew, MergeOptions{Addressed: true}, nopPersist)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	turn, err := s.AdmitWith("group:10001", "20001", "t1", AdmitNew, MergeOptions{Debounce: 5 * time.Second, MaxMergeWindow: 10 * time.Second}, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	if res := addressed("20001", "m1"); !res.Merged || res.TaskID != turn.TaskID {
		t.Fatalf("mention = %+v, want merged into the turn %s", res, turn.TaskID)
	}
	if wait := time.Until(pending(turn.TaskID).ReadyAt); wait > debounce {
		t.Fatalf("turn ready in %v after a merged mention, want within the debounce %v", wait, debounce)
	}
	if started := rec.waitStarted(t); started.TaskID != turn.TaskID || !slices.Equal(started.TriggerMessageIDs, []string{"t1", "m1"}) {
		t.Fatalf("started %+v, want the turn with both messages", started)
	}

	soon, err := s.AdmitWith("group:10001", "20002", "t2", AdmitNew, MergeOptions{Debounce: 2 * debounce, MaxMergeWindow: time.Second}, nopPersist)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(debounce * 6 / 5)
	before := pending(soon.TaskID).ReadyAt
	addressed("20002", "m2")
	if after := pending(soon.TaskID).ReadyAt; !after.Equal(before) {
		t.Fatalf("merged mention moved ReadyAt from %v to %v, want it kept", before, after)
	}
	rec.waitStarted(t)

	plain := mustAdmit(t, s, "group:10001", "20003", "p1", AdmitNew)
	time.Sleep(debounce / 2)
	before = pending(plain.TaskID).ReadyAt
	addressed("20003", "p2")
	if after := pending(plain.TaskID).ReadyAt; !after.After(before) {
		t.Fatalf("merged mention into a debounce task: ReadyAt %v -> %v, want extended as before", before, after)
	}
	rec.waitFinished(t, 3)
}

func TestScheduler_RestoreRunsWithGivenReadyAt(t *testing.T) {
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}))

	now := time.Now()
	qt := QueuedTask{
		TaskID: "task_restored", ConversationKey: "group:10001", ActorID: "20001",
		TriggerMessageIDs: []string{"m1"}, EnqueuedAt: now.Add(-time.Minute), ReadyAt: now.Add(80 * time.Millisecond),
	}
	s.Restore(qt)
	got := rec.waitStarted(t)
	if got.TaskID != qt.TaskID || !slices.Equal(got.TriggerMessageIDs, qt.TriggerMessageIDs) {
		t.Fatalf("started %+v, want %+v", got, qt)
	}
	if time.Now().Before(qt.ReadyAt) {
		t.Fatal("restored task started before its ReadyAt")
	}
}

func TestScheduler_IdleSessionReclaimed(t *testing.T) {
	rec := newSchedRecorder()
	s := NewScheduler(rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}), 30*time.Millisecond)
	defer s.Close()

	mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	rec.waitFinished(t, 1)
	waitUntil(t, "idle session reclaimed", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.sessions) == 0
	})

	mustAdmit(t, s, "group:10001", "20001", "m2", AdmitNew)
	rec.waitFinished(t, 2)
}

func TestScheduler_CloseCancelsRunningAndWaits(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	s := NewScheduler(rec.run, fixedCfg(SessionConfig{MaxConcurrent: 1, MaxQueued: 10}), time.Minute)

	mustAdmit(t, s, "group:10001", "20001", "m1", AdmitNew)
	rec.waitStarted(t)

	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitLimit):
		t.Fatal("Close did not return")
	}
	runs := rec.finished()
	if len(runs) != 1 || runs[0].ctxErr == nil {
		t.Fatalf("after Close runs = %+v, want one cancelled run", runs)
	}
	if _, err := s.Admit("group:10001", "20001", "m2", AdmitNew, nopPersist); !errors.Is(err, ErrSchedulerClosed) {
		t.Fatalf("Admit after Close err = %v, want ErrSchedulerClosed", err)
	}
	s.Close()
}

func TestScheduler_ForgetDropsSession(t *testing.T) {
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	var cfgReads atomic.Int64
	s := newTestScheduler(t, rec.run, func(string) SessionConfig {
		cfgReads.Add(1)
		return SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: time.Hour}
	})
	s.Restore(QueuedTask{TaskID: "task_running", ConversationKey: "c1", ActorID: "u2", TriggerMessageIDs: []string{"m0"}})
	rec.waitStarted(t)
	mustAdmit(t, s, "c1", "u1", "m1", AdmitNew)
	mustAdmit(t, s, "c2", "u1", "m3", AdmitNew)
	if !s.Busy("c1") {
		t.Fatal("Busy(c1) = false with a running and a queued task")
	}

	time.Sleep(20 * time.Millisecond)
	reads := cfgReads.Load()
	s.Forget("c1")
	if s.Busy("c1") {
		t.Error("Busy(c1) after Forget")
	}
	if runs := rec.waitFinished(t, 1); runs[0].ctxErr == nil {
		t.Error("running task not cancelled")
	}
	s.mu.Lock()
	_, kept := s.sessions["c1"]
	_, other := s.sessions["c2"]
	s.mu.Unlock()
	if kept || !other {
		t.Errorf("sessions after Forget: c1 kept %v, c2 kept %v", kept, other)
	}
	rec.assertNotStarted(t, 50*time.Millisecond)
	if !s.Busy("c2") {
		t.Error("Forget(c1) dropped c2's task")
	}
	if n := cfgReads.Load(); n != reads {
		t.Errorf("forgotten session read its config %d more times", n-reads)
	}
	if res := mustAdmit(t, s, "c1", "u1", "m4", AdmitNew); !res.Created || !s.Busy("c1") {
		t.Errorf("Admit after Forget = %+v, want a new task in a new session", res)
	}
}
