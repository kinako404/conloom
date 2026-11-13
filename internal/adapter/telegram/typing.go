package telegram

import (
	"context"
	"sync"
	"time"
)

// typing shows "typing…" in a chat while a reply is being prepared: start
// sends the chat action every interval until stop is called for the chat, the
// chat's latest task has finished, or limit has passed since the latest
// start.
type typing struct {
	ctx      context.Context
	send     func(ctx context.Context, chatID int64)
	finished func(ctx context.Context, taskID string) bool
	interval time.Duration
	limit    time.Duration

	mu        sync.Mutex
	runs      map[int64]*typingRun
	stoppedAt map[int64]time.Time
	wg        sync.WaitGroup
}

type typingRun struct {
	stop   chan struct{}
	until  time.Time
	taskID string
}

// newTyping returns a typing indicator. finished reports whether a task has
// ended; it is asked before every chat action.
func newTyping(ctx context.Context, interval, limit time.Duration,
	send func(ctx context.Context, chatID int64), finished func(ctx context.Context, taskID string) bool) *typing {
	return &typing{
		ctx: ctx, send: send, finished: finished, interval: interval, limit: limit,
		runs: map[int64]*typingRun{}, stoppedAt: map[int64]time.Time{},
	}
}

// start shows typing in chatID for task taskID, submitted at since. It does
// nothing if a reply was sent to the chat after since, which happens when
// the reply arrives before the submission's response. A chat already typing
// follows the newer task.
func (t *typing) start(chatID int64, since time.Time, taskID string) {
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
		r.until, r.taskID = now.Add(t.limit), taskID
		return
	}
	r := &typingRun{stop: make(chan struct{}), until: now.Add(t.limit), taskID: taskID}
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
		t.mu.Lock()
		taskID := r.taskID
		t.mu.Unlock()
		if t.finished(t.ctx, taskID) {
			t.mu.Lock()
			if t.runs[chatID] == r {
				delete(t.runs, chatID)
			}
			t.mu.Unlock()
			return
		}
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
