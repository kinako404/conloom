package core

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cooldownCfg is a conversation config whose CooldownUntil tests move.
type cooldownCfg struct {
	base  SessionConfig
	until atomic.Int64
}

func (c *cooldownCfg) set(t time.Time) { c.until.Store(t.UnixNano()) }

func (c *cooldownCfg) cfg(string) SessionConfig {
	s := c.base
	if n := c.until.Load(); n != 0 {
		s.CooldownUntil = time.Unix(0, n)
	}
	return s
}

var turnOpts = MergeOptions{Debounce: 20 * time.Millisecond, MaxMergeWindow: 60 * time.Millisecond, Cooldown: true, MaxHold: time.Minute}

func admitWith(t *testing.T, s *Scheduler, actor, msg string, opts MergeOptions) AdmitResult {
	t.Helper()
	res, err := s.AdmitWith("group:10001", actor, msg, AdmitNew, opts, nopPersist)
	if err != nil {
		t.Fatalf("AdmitWith(%s, %s): %v", actor, msg, err)
	}
	return res
}

func heldTasks(s *Scheduler) int {
	n := 0
	for _, t := range s.Active("group:10001") {
		if t.Held {
			n++
		}
	}
	return n
}

func TestScheduler_CooldownHoldsTaskAndKeepsMerging(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	until := time.Now().Add(500 * time.Millisecond)
	c.set(until)
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)

	first := admitWith(t, s, "20001", "m1", turnOpts)
	waitUntil(t, "the turn to be held", func() bool { return heldTasks(s) == 1 })
	time.Sleep(100 * time.Millisecond)
	merged := admitWith(t, s, "20001", "m2", turnOpts)
	if !merged.Merged || merged.TaskID != first.TaskID {
		t.Fatalf("message during the cooldown = %+v, want merged into %s", merged, first.TaskID)
	}
	rec.assertNotStarted(t, time.Until(until)-100*time.Millisecond)
	got := rec.waitStarted(t)
	if time.Now().Before(until) {
		t.Fatalf("held task started %v before the cooldown ended", time.Until(until))
	}
	if got.TaskID != first.TaskID || len(got.TriggerMessageIDs) != 2 {
		t.Fatalf("started %+v, want %s with both messages", got, first.TaskID)
	}
}

func TestScheduler_HeldTaskDoesNotBlockOthers(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	until := time.Now().Add(600 * time.Millisecond)
	c.set(until)
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)

	turn := admitWith(t, s, "20001", "m1", turnOpts)
	waitUntil(t, "the turn to be held", func() bool { return heldTasks(s) == 1 })
	other := admitWith(t, s, "20002", "m2", MergeOptions{Addressed: true})
	if got := rec.waitStarted(t); got.TaskID != other.TaskID || !time.Now().Before(until) {
		t.Fatalf("started %s at %v before the cooldown end, want %s before it", got.TaskID, time.Until(until), other.TaskID)
	}
	if got := rec.waitStarted(t); got.TaskID != turn.TaskID || time.Now().Before(until) {
		t.Fatalf("started %s, want the held turn %s after the cooldown", got.TaskID, turn.TaskID)
	}
}

func TestScheduler_AddressedMergeReleasesHeldTask(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	c.set(time.Now().Add(time.Minute))
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)

	turn := admitWith(t, s, "20001", "m1", turnOpts)
	waitUntil(t, "the turn to be held", func() bool { return heldTasks(s) == 1 })
	mention := admitWith(t, s, "20001", "m2", MergeOptions{Addressed: true})
	if !mention.Merged || mention.TaskID != turn.TaskID {
		t.Fatalf("mention = %+v, want merged into the held turn %s", mention, turn.TaskID)
	}
	got := rec.waitStarted(t)
	if got.TaskID != turn.TaskID || len(got.TriggerMessageIDs) != 2 {
		t.Fatalf("started %+v, want the turn with both messages", got)
	}
	rec.waitFinished(t, 1)
	if n := len(s.Active("group:10001")); n != 0 {
		t.Fatalf("Active = %d tasks after the merged mention ran, want 0", n)
	}
}

func TestScheduler_HeldTaskWaitsForRunningTask(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 2, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	release := make(chan struct{})
	var started sync.Map
	var until atomic.Int64
	run := func(ctx context.Context, qt QueuedTask) {
		started.Store(qt.TaskID, time.Now())
		if qt.ActorID == "20002" {
			<-release
			// The running task's reply starts a new cooldown.
			at := time.Now().Add(300 * time.Millisecond)
			until.Store(at.UnixNano())
			c.set(at)
		}
	}
	s := newTestScheduler(t, run, c.cfg)
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)

	other := admitWith(t, s, "20002", "m1", MergeOptions{})
	waitUntil(t, "the other task to run", func() bool { _, ok := started.Load(other.TaskID); return ok })
	turn := admitWith(t, s, "20001", "m2", turnOpts)
	time.Sleep(150 * time.Millisecond)
	if _, ok := started.Load(turn.TaskID); ok {
		t.Fatal("turn started while another task of the conversation was running")
	}
	if merged := admitWith(t, s, "20001", "m3", turnOpts); merged.TaskID != turn.TaskID {
		t.Fatalf("message while waiting = %+v, want merged into %s", merged, turn.TaskID)
	}
	free()
	waitUntil(t, "the turn to run", func() bool { _, ok := started.Load(turn.TaskID); return ok })
	at, _ := started.Load(turn.TaskID)
	if want := time.Unix(0, until.Load()); at.(time.Time).Before(want) {
		t.Fatalf("turn started %v before the new cooldown ended", want.Sub(at.(time.Time)))
	}
}

func TestScheduler_OnHoldReportedOncePerTask(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	until := time.Now().Add(400 * time.Millisecond)
	c.set(until)
	rec := newSchedRecorder()
	s := NewScheduler(rec.run, c.cfg, time.Minute)
	t.Cleanup(s.Close)
	var mu sync.Mutex
	var holds []QueuedTask
	var holdUntil []time.Time
	s.OnHold(func(qt QueuedTask, at time.Time) {
		mu.Lock()
		defer mu.Unlock()
		holds = append(holds, qt)
		holdUntil = append(holdUntil, at)
	})

	turn := admitWith(t, s, "20001", "m1", turnOpts)
	waitUntil(t, "the turn to be held", func() bool { return heldTasks(s) == 1 })
	admitWith(t, s, "20001", "m2", turnOpts)
	admitWith(t, s, "20002", "m3", MergeOptions{})
	rec.waitFinished(t, 2)
	mu.Lock()
	defer mu.Unlock()
	if len(holds) != 1 || holds[0].TaskID != turn.TaskID || !holdUntil[0].Equal(until) {
		t.Fatalf("holds = %+v until %v, want one for %s until %v", holds, holdUntil, turn.TaskID, until)
	}
}

func TestScheduler_CooldownPassedRunsAsBefore(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	c.set(time.Now().Add(-time.Second))
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)
	turn := admitWith(t, s, "20001", "m1", turnOpts)
	if got := rec.waitStarted(t); got.TaskID != turn.TaskID {
		t.Fatalf("started %s, want %s", got.TaskID, turn.TaskID)
	}
	if n := heldTasks(s); n != 0 {
		t.Fatalf("held tasks = %d, want 0", n)
	}
}

// talk admits a message from 20001 every interval until end and returns the
// results with the time just before each admit.
func talk(t *testing.T, s *Scheduler, opts MergeOptions, interval time.Duration, end time.Time) ([]AdmitResult, []time.Time) {
	t.Helper()
	var res []AdmitResult
	var at []time.Time
	for i := 0; time.Now().Before(end); i++ {
		at = append(at, time.Now())
		res = append(res, admitWith(t, s, "20001", "t"+strconv.Itoa(i), opts))
		time.Sleep(interval)
	}
	return res, at
}

func TestScheduler_HeldTaskWaitsForPauseAfterCooldown(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	start := time.Now()
	c.set(start.Add(300 * time.Millisecond))
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)
	opts := MergeOptions{Debounce: 200 * time.Millisecond, MaxMergeWindow: 60 * time.Millisecond, Cooldown: true, MaxHold: 5 * time.Second}

	res, at := talk(t, s, opts, 80*time.Millisecond, start.Add(600*time.Millisecond))
	run := rec.waitFinished(t, 1)[0]
	got, started := run.task, run.start
	for _, r := range res {
		if r.TaskID != res[0].TaskID {
			t.Fatalf("admits = %+v, want every message in one turn", res)
		}
	}
	if last := at[len(at)-1]; started.Before(last.Add(200 * time.Millisecond)) {
		t.Fatalf("turn started %v after the last message, want at least the 200ms pause", started.Sub(last))
	}
	if len(got.TriggerMessageIDs) != len(res) {
		t.Fatalf("turn has %d messages, want %d", len(got.TriggerMessageIDs), len(res))
	}
}

func TestScheduler_HoldLimitReleasesWhenFree(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	start := time.Now()
	c.set(start.Add(150 * time.Millisecond))
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)
	opts := MergeOptions{Debounce: 400 * time.Millisecond, MaxMergeWindow: 60 * time.Millisecond, Cooldown: true, MaxHold: 400 * time.Millisecond}

	res, at := talk(t, s, opts, 100*time.Millisecond, start.Add(700*time.Millisecond))
	first := res[0].TaskID
	run := rec.waitFinished(t, 1)[0]
	if run.task.TaskID != first {
		t.Fatalf("first run = %s, want %s", run.task.TaskID, first)
	}
	started := run.start
	if d := started.Sub(start); d < 400*time.Millisecond || d >= 750*time.Millisecond {
		t.Fatalf("turn started %v after its first message, want at the 400ms hold limit, before its pause ended", d)
	}
	for i, r := range res {
		if at[i].After(start.Add(450*time.Millisecond)) && r.TaskID == first {
			t.Fatalf("message %d after the hold limit merged into the held turn", i)
		}
	}
	if res[len(res)-1].TaskID == first {
		t.Fatal("no new turn after the hold limit")
	}
}

func TestScheduler_HoldLimitDropsWhileCoolingDown(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	c.set(time.Now().Add(time.Minute))
	rec := newSchedRecorder()
	s := NewScheduler(rec.run, c.cfg, time.Minute)
	t.Cleanup(s.Close)
	dropped := make(chan QueuedTask, 4)
	s.OnDrop(func(qt QueuedTask) { dropped <- qt })
	opts := MergeOptions{Debounce: 50 * time.Millisecond, MaxMergeWindow: 60 * time.Millisecond, Cooldown: true, MaxHold: 300 * time.Millisecond}

	turn := admitWith(t, s, "20001", "m1", opts)
	select {
	case qt := <-dropped:
		if qt.TaskID != turn.TaskID {
			t.Fatalf("dropped %s, want %s", qt.TaskID, turn.TaskID)
		}
	case <-time.After(waitLimit):
		t.Fatal("held turn not dropped at its hold limit")
	}
	if n := len(s.Active("group:10001")); n != 0 {
		t.Fatalf("Active = %d tasks after the drop, want 0", n)
	}
	if next := admitWith(t, s, "20001", "m2", opts); !next.Created || next.TaskID == turn.TaskID {
		t.Fatalf("message after the drop = %+v, want a new turn", next)
	}
	rec.assertNotStarted(t, 100*time.Millisecond)
}

func TestScheduler_KickReleasesAfterCooldownShortened(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	c.set(time.Now().Add(time.Minute))
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)
	turn := admitWith(t, s, "20001", "m1", turnOpts)
	waitUntil(t, "the turn to be held", func() bool { return heldTasks(s) == 1 })
	c.set(time.Now().Add(-time.Millisecond))
	s.Kick("group:10001")
	if got := rec.waitStarted(t); got.TaskID != turn.TaskID {
		t.Fatalf("started %s, want %s", got.TaskID, turn.TaskID)
	}
}

func TestScheduler_HeldTaskRespectsMinReplyInterval(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 1, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second,
		MinReplyInterval: 400 * time.Millisecond}}
	rec := newSchedRecorder()
	s := newTestScheduler(t, rec.run, c.cfg)
	admitWith(t, s, "20002", "m1", MergeOptions{})
	end := rec.waitFinished(t, 1)[0].end
	c.set(time.Now().Add(100 * time.Millisecond))
	turn := admitWith(t, s, "20001", "m2", turnOpts)
	got := rec.waitStarted(t)
	got = rec.waitStarted(t)
	if got.TaskID != turn.TaskID || time.Since(end) < 400*time.Millisecond {
		t.Fatalf("turn %s started %v after the previous task, want %s after at least 400ms", got.TaskID, time.Since(end), turn.TaskID)
	}
}

func TestScheduler_OnHoldWithoutCooldownReportsRunning(t *testing.T) {
	c := &cooldownCfg{base: SessionConfig{MaxConcurrent: 2, MaxQueued: 10, Debounce: 20 * time.Millisecond, MaxMergeWindow: time.Second}}
	rec := newSchedRecorder()
	rec.hold = make(chan struct{})
	s := NewScheduler(rec.run, c.cfg, time.Minute)
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(rec.hold) })
	holds := make(chan time.Time, 4)
	s.OnHold(func(_ QueuedTask, until time.Time) { holds <- until })
	admitWith(t, s, "20002", "m1", MergeOptions{})
	rec.waitStarted(t)
	admitWith(t, s, "20001", "m2", turnOpts)
	select {
	case until := <-holds:
		if !until.IsZero() {
			t.Fatalf("hold until %v, want zero while only waiting for a running task", until)
		}
	case <-time.After(waitLimit):
		t.Fatal("hold not reported")
	}
}
