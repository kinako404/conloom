package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	errorCodeStale = "STALE"

	defaultStaleTaskAfter       = 2 * time.Minute
	defaultIdempotencyRetention = 168 * time.Hour
	expireOutboxEvery           = 30 * time.Second
	purgeEventsEvery            = time.Hour

	// The keyword index is backfilled in batches of ftsBackfillBatch messages
	// with ftsBackfillPause between them, checked again every ftsBackfillEvery
	// once caught up, and retried after ftsBackfillRetry, doubling up to
	// ftsBackfillMaxRetry, when a batch fails.
	ftsBackfillBatch    = 500
	ftsBackfillPause    = 200 * time.Millisecond
	ftsBackfillEvery    = time.Hour
	ftsBackfillRetry    = 5 * time.Second
	ftsBackfillMaxRetry = 10 * time.Minute
)

// ErrAlreadyRecovered is returned by every Recover call after the first.
var ErrAlreadyRecovered = errors.New("core: Recover already called")

type recovery int

const (
	recoverSkipped recovery = iota
	recoverRan
	recoverRequeued
	recoverStale
)

// Recover resumes tasks left queued or running by a previous process: tasks
// whose event is younger than StaleTaskAfter run again (commands at once,
// replies through the scheduler); older ones fail with STALE and no action.
// It then starts background maintenance, which expires undelivered outbox
// actions every 30s and, every hour until Close, purges old idempotency
// records and proactive log entries older than 7 days, and the keyword index
// backfill (backfillMessageIndex).
//
// Recover runs once per Orchestrator and should be called before the first
// Submit; later calls return ErrAlreadyRecovered. Tasks this process already
// schedules or executes are left alone, so overlapping live traffic never
// runs a task twice.
func (o *Orchestrator) Recover(ctx context.Context) error {
	if o.closing.Load() {
		return ErrSchedulerClosed
	}
	if !o.recovered.CompareAndSwap(false, true) {
		return ErrAlreadyRecovered
	}
	tasks, err := o.st.ListUnfinishedTasks(ctx)
	if err != nil {
		return fmt.Errorf("core: list unfinished tasks: %w", err)
	}
	now := o.now()
	counts := map[recovery]int{}
	for _, t := range tasks {
		r, err := o.recoverTask(ctx, t, now, o.staleAfter)
		if err != nil {
			return fmt.Errorf("core: recover task %s: %w", t.ID, err)
		}
		counts[r]++
	}
	o.log.Info("recovered unfinished tasks", "requeued", counts[recoverRan]+counts[recoverRequeued],
		"stale", counts[recoverStale], "skipped", counts[recoverSkipped])
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closing.Load() {
		return ErrSchedulerClosed
	}
	o.bgWG.Add(2)
	go o.maintain()
	go o.backfillMessageIndex()
	return nil
}

// backfillMessageIndex adds messages missing from the keyword index, oldest
// first, until Close: batch after batch with a pause between them while
// behind, then once an hour, which also picks up messages written by an
// older binary. Failed batches are retried with a doubling wait. The
// remaining count is exported as conloom_fts_backfill_remaining.
func (o *Orchestrator) backfillMessageIndex() {
	defer o.bgWG.Done()
	retry, total := time.Duration(0), 0
	for {
		wait := ftsBackfillEvery
		n, remaining, err := o.st.BackfillMessageIndex(o.bgCtx, ftsBackfillBatch)
		switch {
		case err != nil:
			if o.bgCtx.Err() != nil {
				return
			}
			retry = min(max(2*retry, o.ftsRetry), ftsBackfillMaxRetry)
			wait = retry
			o.log.Error("keyword index backfill failed", "error", err, "retry_in", retry)
		default:
			retry = 0
			total += n
			o.metrics.FTSBackfillRemaining.Set(float64(remaining))
			if remaining > 0 {
				wait = ftsBackfillPause
			} else if total > 0 {
				o.log.Info("keyword index backfilled", "messages", total)
				total = 0
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-o.bgCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// recoverTask resumes or fails one unfinished task unless this process
// already schedules or owns it. Only a task outside the scheduler is claimed,
// so the claim never turns away the task's own runner.
func (o *Orchestrator) recoverTask(ctx context.Context, t store.TaskRecord, now time.Time, staleAfter time.Duration) (recovery, error) {
	if slices.ContainsFunc(o.sched.Active(t.ConversationKey), func(q QueuedTask) bool { return q.TaskID == t.ID }) {
		return recoverSkipped, nil
	}
	ow := o.claim(t.ID, false)
	if ow == nil {
		return recoverSkipped, nil
	}
	r, rec, err := o.recoverOwned(ctx, t, now, staleAfter)
	o.release(t.ID, ow)
	if r == recoverRequeued {
		at := time.Now()
		o.sched.Restore(QueuedTask{
			TaskID: rec.ID, ConversationKey: rec.ConversationKey, ActorID: rec.ActorPlatformUserID,
			TriggerMessageIDs: rec.TriggerMessageIDs, EnqueuedAt: at, ReadyAt: at,
		})
	}
	return r, err
}

func (o *Orchestrator) recoverOwned(ctx context.Context, t store.TaskRecord, now time.Time, staleAfter time.Duration) (recovery, store.TaskRecord, error) {
	rec, err := o.st.GetTask(ctx, t.BotInstanceID, t.ID)
	if err != nil {
		return recoverSkipped, rec, err
	}
	switch {
	case taskFinished(rec.Status):
		return recoverSkipped, rec, nil
	case now.Sub(rec.EventOccurredAt) >= staleAfter:
		return recoverStale, rec, o.complete(ctx, rec, conloomapi.TaskFailed, rec.Meta, errorCodeStale, nil)
	case rec.Command != "":
		return recoverRan, rec, o.runStoredCommand(ctx, rec)
	}
	if err := o.st.SetTaskStatus(ctx, rec.ID, conloomapi.TaskQueued); err != nil {
		return recoverSkipped, rec, err
	}
	return recoverRequeued, rec, nil
}

func (o *Orchestrator) maintain() {
	defer o.bgWG.Done()
	expire := time.NewTicker(expireOutboxEvery)
	defer expire.Stop()
	purge := time.NewTicker(purgeEventsEvery)
	defer purge.Stop()
	o.expireOutbox()
	o.purgeEvents()
	o.purgeProactiveLog()
	for {
		select {
		case <-o.bgCtx.Done():
			return
		case <-expire.C:
			o.expireOutbox()
		case <-purge.C:
			o.purgeEvents()
			o.purgeProactiveLog()
		}
	}
}

func (o *Orchestrator) expireOutbox() {
	n, err := o.st.ExpireOutbox(o.bgCtx, o.now())
	if err != nil {
		if o.bgCtx.Err() == nil {
			o.log.Error("expire outbox failed", "error", err)
		}
		return
	}
	if n > 0 {
		o.metrics.OutboxExpired.Add(float64(n))
		o.log.Info("expired undelivered actions", "count", n)
	}
}

func (o *Orchestrator) purgeEvents() {
	retention := o.cfg.Runtime.IdempotencyRetention.Duration
	if retention <= 0 {
		retention = defaultIdempotencyRetention
	}
	n, err := o.st.PurgeEvents(o.bgCtx, o.now().Add(-retention))
	if err != nil {
		if o.bgCtx.Err() == nil {
			o.log.Error("purge idempotency records failed", "error", err)
		}
		return
	}
	if n > 0 {
		o.log.Info("purged idempotency records", "count", n)
	}
}
