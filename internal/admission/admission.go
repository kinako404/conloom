package admission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

type Priority int

const (
	PriorityInteractive Priority = 0
	PriorityRetry       Priority = 1
	PriorityBackground  Priority = 2
)

type Limits struct {
	MaxConcurrency int
	TokenBudget    int // 0 means unlimited
}

type Request struct {
	ID, Profile string
	Tokens      int
	Priority    Priority
	TTL         time.Duration
}

type Stats struct {
	InFlight, TokensInUse, Waiting int
}

var (
	ErrTooLarge       = errors.New("admission: request exceeds token budget")
	ErrUnknownProfile = errors.New("admission: unknown profile")
)

type Controller struct {
	mu       sync.Mutex
	profiles map[string]*profileState
}

type profileState struct {
	limits      Limits
	inFlight    int
	tokensInUse int
	waiters     []*waiter
}

type waiter struct {
	req   Request
	ready chan struct{}
	lease *Lease
}

type Lease struct {
	c        *Controller
	st       *profileState
	tokens   int
	timer    *time.Timer
	released bool
}

func New(limits map[string]Limits) *Controller {
	c := &Controller{profiles: make(map[string]*profileState, len(limits))}
	for name, l := range limits {
		c.profiles[name] = &profileState{limits: l}
	}
	return c
}

// Acquire blocks until req can be admitted under its profile's limits.
// Waiters are served strictly in (Priority, arrival) order: only the head of
// the queue may be admitted, so a large request is never starved by smaller
// ones behind it.
func (c *Controller) Acquire(ctx context.Context, req Request) (*Lease, error) {
	c.mu.Lock()
	st, ok := c.profiles[req.Profile]
	if !ok {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrUnknownProfile, req.Profile)
	}
	if st.limits.TokenBudget > 0 && req.Tokens > st.limits.TokenBudget {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %d > %d", ErrTooLarge, req.Tokens, st.limits.TokenBudget)
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	w := &waiter{req: req, ready: make(chan struct{})}
	i := slices.IndexFunc(st.waiters, func(o *waiter) bool { return o.req.Priority > req.Priority })
	if i < 0 {
		i = len(st.waiters)
	}
	st.waiters = slices.Insert(st.waiters, i, w)
	c.dispatchLocked(st)
	c.mu.Unlock()

	select {
	case <-w.ready:
		return w.lease, nil
	case <-ctx.Done():
	}

	c.mu.Lock()
	if w.lease != nil {
		c.releaseLocked(w.lease)
	} else {
		st.waiters = slices.DeleteFunc(st.waiters, func(o *waiter) bool { return o == w })
		c.dispatchLocked(st)
	}
	c.mu.Unlock()
	return nil, ctx.Err()
}

func (c *Controller) Stats(profile string) Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.profiles[profile]
	if !ok {
		return Stats{}
	}
	return Stats{InFlight: st.inFlight, TokensInUse: st.tokensInUse, Waiting: len(st.waiters)}
}

// Release returns the lease's slot and tokens. It is idempotent and is also
// invoked automatically when the request's TTL elapses.
func (l *Lease) Release() {
	l.c.mu.Lock()
	l.c.releaseLocked(l)
	l.c.mu.Unlock()
}

func (c *Controller) dispatchLocked(st *profileState) {
	for len(st.waiters) > 0 {
		w := st.waiters[0]
		if st.inFlight >= st.limits.MaxConcurrency {
			return
		}
		if st.limits.TokenBudget > 0 && st.tokensInUse+w.req.Tokens > st.limits.TokenBudget {
			return
		}
		st.waiters = st.waiters[1:]
		st.inFlight++
		st.tokensInUse += w.req.Tokens
		l := &Lease{c: c, st: st, tokens: w.req.Tokens}
		if w.req.TTL > 0 {
			l.timer = time.AfterFunc(w.req.TTL, l.Release)
		}
		w.lease = l
		close(w.ready)
	}
}

func (c *Controller) releaseLocked(l *Lease) {
	if l.released {
		return
	}
	l.released = true
	if l.timer != nil {
		l.timer.Stop()
	}
	l.st.inFlight--
	l.st.tokensInUse -= l.tokens
	c.dispatchLocked(l.st)
}
