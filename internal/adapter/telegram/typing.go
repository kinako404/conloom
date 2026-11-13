package telegram

import (
	"context"
	"sync"
	"time"
)

// typing shows "typing…" in a chat while a reply is being prepared: start
// sends the chat action every interval until stop is called for the chat or
// limit has passed since the latest start.
type typing struct {
	ctx      context.Context
	send     func(ctx context.Context, chatID int64)
	interval time.Duration
	limit    time.Duration

	mu        sync.Mutex
	runs      map[int64]*typingRun
	stoppedAt map[int64]time.Time
	wg        sync.WaitGroup
}

type typingRun struct {
	stop  chan struct{}
	until time.Time
}

func newTyping(ctx context.Context, interval, limit time.Duration, send func(ctx context.Context, chatID int64)) *typing {
	return &typing{
		ctx: ctx, send: send, interval: interval, limit: limit,
		runs: map[int64]*typingRun{}, stoppedAt: map[int64]time.Time{},
	}
}

// start shows typing in chatID for a message submitted at since. It does
// nothing if a reply was sent to the chat after since, which happens when
// the reply arrives before the submission's response.
func (t *typing) start(chatID int64, since time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for id, at := range t.stoppedAt {
		if now.Sub(at) > t.limit {
			delete(t.stoppedAt, id)
		}
	}
	if at, ok := t.stoppedAt[chatID]; ok && at.After(since) {
		return
	}
	if r, ok := t.runs[chatID]; ok {
		r.until = now.Add(t.limit)
		return
	}
	r := &typingRun{stop: make(chan struct{}), until: now.Add(t.limit)}
	t.runs[chatID] = r
	t.wg.Add(1)
	go t.loop(chatID, r)
}

// stop ends typing in chatID: a reply has been sent there.
func (t *typing) stop(chatID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stoppedAt[chatID] = time.Now()
	if r, ok := t.runs[chatID]; ok {
		close(r.stop)
		delete(t.runs, chatID)
	}
}

func (t *typing) loop(chatID int64, r *typingRun) {
	defer t.wg.Done()
	for {
		t.send(t.ctx, chatID)
		timer := time.NewTimer(t.interval)
		select {
		case <-t.ctx.Done():
			timer.Stop()
			return
		case <-r.stop:
			timer.Stop()
			return
		case <-timer.C:
		}
		t.mu.Lock()
		if t.runs[chatID] != r {
			t.mu.Unlock()
			return
		}
		if !time.Now().Before(r.until) {
			delete(t.runs, chatID)
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

// wait returns once every typing loop has ended; the loops end with ctx.
func (t *typing) wait() { t.wg.Wait() }
