package admission

import (
	"context"
	"errors"
	"testing"
	"time"
)

const testProfile = "short"

func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", desc)
		}
		time.Sleep(time.Millisecond)
	}
}

func mustAcquire(t *testing.T, c *Controller, req Request) *Lease {
	t.Helper()
	l, err := c.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", req.ID, err)
	}
	return l
}

type result struct {
	id    string
	lease *Lease
	err   error
}

func acquireAsync(c *Controller, ctx context.Context, req Request) <-chan result {
	ch := make(chan result, 1)
	go func() {
		l, err := c.Acquire(ctx, req)
		ch <- result{id: req.ID, lease: l, err: err}
	}()
	return ch
}

func expectBlocked(t *testing.T, ch <-chan result) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("request %s admitted early (err=%v)", r.id, r.err)
	case <-time.After(20 * time.Millisecond):
	}
}

func receive(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for admission")
		return result{}
	}
}

func TestAcquire_RespectsMaxConcurrency(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 2}})
	l1 := mustAcquire(t, c, Request{ID: "r1", Profile: testProfile, Tokens: 100})
	mustAcquire(t, c, Request{ID: "r2", Profile: testProfile, Tokens: 100})

	ch := acquireAsync(c, context.Background(), Request{ID: "r3", Profile: testProfile, Tokens: 100})
	waitFor(t, "r3 to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })
	expectBlocked(t, ch)
	if got := c.Stats(testProfile); got.InFlight != 2 {
		t.Fatalf("InFlight = %d, want 2", got.InFlight)
	}

	l1.Release()
	r := receive(t, ch)
	if r.err != nil {
		t.Fatalf("r3: %v", r.err)
	}
	if got := c.Stats(testProfile); got != (Stats{InFlight: 2, TokensInUse: 200, Waiting: 0}) {
		t.Fatalf("Stats = %+v", got)
	}
}

func TestAcquire_TokenBudget(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 4, TokenBudget: 49152}})
	l1 := mustAcquire(t, c, Request{ID: "a", Profile: testProfile, Tokens: 30000})

	ch := acquireAsync(c, context.Background(), Request{ID: "b", Profile: testProfile, Tokens: 30000})
	waitFor(t, "b to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })
	expectBlocked(t, ch)

	l1.Release()
	rb := receive(t, ch)
	if rb.err != nil {
		t.Fatalf("b: %v", rb.err)
	}

	mustAcquire(t, c, Request{ID: "c", Profile: testProfile, Tokens: 10000})
	if got := c.Stats(testProfile); got != (Stats{InFlight: 2, TokensInUse: 40000, Waiting: 0}) {
		t.Fatalf("Stats = %+v", got)
	}
}

func TestAcquire_TooLarge(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 2, TokenBudget: 49152}})
	l, err := c.Acquire(context.Background(), Request{ID: "big", Profile: testProfile, Tokens: 60000})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if l != nil {
		t.Fatal("lease returned for too-large request")
	}
	if got := c.Stats(testProfile); got != (Stats{}) {
		t.Fatalf("Stats = %+v, want zero", got)
	}
}

func TestAcquire_UnknownProfile(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 1}})
	_, err := c.Acquire(context.Background(), Request{ID: "x", Profile: "missing", Tokens: 1})
	if !errors.Is(err, ErrUnknownProfile) {
		t.Fatalf("err = %v, want ErrUnknownProfile", err)
	}
}

func TestAcquire_PriorityOrder(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 1}})
	held := mustAcquire(t, c, Request{ID: "held", Profile: testProfile, Tokens: 1})

	bg := acquireAsync(c, context.Background(), Request{ID: "bg", Profile: testProfile, Tokens: 1, Priority: PriorityBackground})
	waitFor(t, "bg to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })
	in := acquireAsync(c, context.Background(), Request{ID: "in", Profile: testProfile, Tokens: 1, Priority: PriorityInteractive})
	waitFor(t, "in to queue", func() bool { return c.Stats(testProfile).Waiting == 2 })

	held.Release()
	ri := receive(t, in)
	if ri.err != nil {
		t.Fatalf("in: %v", ri.err)
	}
	expectBlocked(t, bg)
	if got := c.Stats(testProfile); got.Waiting != 1 || got.InFlight != 1 {
		t.Fatalf("Stats = %+v, want InFlight 1 Waiting 1", got)
	}

	ri.lease.Release()
	rb := receive(t, bg)
	if rb.err != nil {
		t.Fatalf("bg: %v", rb.err)
	}
}

func TestAcquire_HeadOfLineBlocksSmallerLaterRequest(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 4, TokenBudget: 49152}})
	l1 := mustAcquire(t, c, Request{ID: "a", Profile: testProfile, Tokens: 30000})

	big := acquireAsync(c, context.Background(), Request{ID: "big", Profile: testProfile, Tokens: 30000})
	waitFor(t, "big to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })
	small := acquireAsync(c, context.Background(), Request{ID: "small", Profile: testProfile, Tokens: 1000})
	waitFor(t, "small to queue", func() bool { return c.Stats(testProfile).Waiting == 2 })
	expectBlocked(t, small)

	l1.Release()
	if r := receive(t, big); r.err != nil {
		t.Fatalf("big: %v", r.err)
	}
	if r := receive(t, small); r.err != nil {
		t.Fatalf("small: %v", r.err)
	}
}

func TestAcquire_ContextCancelRemovesWaiter(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 1}})
	held := mustAcquire(t, c, Request{ID: "held", Profile: testProfile, Tokens: 1})

	ctx, cancel := context.WithCancel(context.Background())
	ch := acquireAsync(c, ctx, Request{ID: "w", Profile: testProfile, Tokens: 1})
	waitFor(t, "w to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })

	cancel()
	r := receive(t, ch)
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", r.err)
	}
	if r.lease != nil {
		t.Fatal("lease returned for cancelled request")
	}
	if got := c.Stats(testProfile); got != (Stats{InFlight: 1, TokensInUse: 1, Waiting: 0}) {
		t.Fatalf("Stats = %+v", got)
	}

	held.Release()
	if got := c.Stats(testProfile); got != (Stats{}) {
		t.Fatalf("Stats after release = %+v, want zero", got)
	}
}

func TestAcquire_CancelledHeadUnblocksNext(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 4, TokenBudget: 49152}})
	mustAcquire(t, c, Request{ID: "a", Profile: testProfile, Tokens: 30000})

	ctx, cancel := context.WithCancel(context.Background())
	big := acquireAsync(c, ctx, Request{ID: "big", Profile: testProfile, Tokens: 30000})
	waitFor(t, "big to queue", func() bool { return c.Stats(testProfile).Waiting == 1 })
	small := acquireAsync(c, context.Background(), Request{ID: "small", Profile: testProfile, Tokens: 1000})
	waitFor(t, "small to queue", func() bool { return c.Stats(testProfile).Waiting == 2 })

	cancel()
	if r := receive(t, big); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("big err = %v, want context.Canceled", r.err)
	}
	if r := receive(t, small); r.err != nil {
		t.Fatalf("small: %v", r.err)
	}
}

func TestLease_TTLAutoRelease(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 1}})
	l := mustAcquire(t, c, Request{ID: "ttl", Profile: testProfile, Tokens: 5, TTL: 50 * time.Millisecond})
	if got := c.Stats(testProfile).InFlight; got != 1 {
		t.Fatalf("InFlight = %d, want 1", got)
	}

	time.Sleep(100 * time.Millisecond)
	waitFor(t, "TTL release", func() bool { return c.Stats(testProfile) == (Stats{}) })

	mustAcquire(t, c, Request{ID: "next", Profile: testProfile, Tokens: 5})
	l.Release()
	if got := c.Stats(testProfile); got != (Stats{InFlight: 1, TokensInUse: 5}) {
		t.Fatalf("Stats after late Release = %+v, want next lease untouched", got)
	}
}

func TestLease_ReleaseIdempotent(t *testing.T) {
	c := New(map[string]Limits{testProfile: {MaxConcurrency: 2, TokenBudget: 100}})
	l1 := mustAcquire(t, c, Request{ID: "r1", Profile: testProfile, Tokens: 10})
	mustAcquire(t, c, Request{ID: "r2", Profile: testProfile, Tokens: 20})

	l1.Release()
	l1.Release()
	if got := c.Stats(testProfile); got != (Stats{InFlight: 1, TokensInUse: 20}) {
		t.Fatalf("Stats = %+v, want InFlight 1 TokensInUse 20", got)
	}
}
