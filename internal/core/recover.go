package core

import (
	"context"
	"fmt"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	errorCodeStale = "STALE"

	defaultStaleTaskAfter       = 2 * time.Minute
	defaultIdempotencyRetention = 168 * time.Hour
	expireOutboxEvery           = 30 * time.Second
	purgeEventsEvery            = time.Hour
)

// Recover resumes tasks left queued or running by a previous process: tasks
// whose event is younger than StaleTaskAfter run again (commands at once,
// replies through the scheduler); older ones fail with STALE and no action.
// It then starts background maintenance, which expires undelivered outbox
// actions every 30s and purges old idempotency records every hour until Close.
func (o *Orchestrator) Recover(ctx context.Context) error {
	if o.closing.Load() {
		return ErrSchedulerClosed
	}
	tasks, err := o.st.ListUnfinishedTasks(ctx)
	if err != nil {
		return fmt.Errorf("core: list unfinished tasks: %w", err)
	}
	staleAfter := o.cfg.Runtime.StaleTaskAfter.Duration
	if staleAfter <= 0 {
		staleAfter = defaultStaleTaskAfter
	}
	now := o.now()
	requeued, stale := 0, 0
	for _, t := range tasks {
		switch {
		case now.Sub(t.EventOccurredAt) >= staleAfter:
			if err := o.complete(ctx, t, conloomapi.TaskFailed, t.Meta, errorCodeStale, nil); err != nil {
				return fmt.Errorf("core: fail stale task %s: %w", t.ID, err)
			}
			stale++
		case t.Command != "":
			if err := o.runStoredCommand(ctx, t); err != nil {
				return fmt.Errorf("core: recover command task %s: %w", t.ID, err)
			}
			requeued++
		default:
			if err := o.st.SetTaskStatus(ctx, t.ID, conloomapi.TaskQueued); err != nil {
				return fmt.Errorf("core: requeue task %s: %w", t.ID, err)
			}
			at := time.Now()
			o.sched.Restore(QueuedTask{
				TaskID: t.ID, ConversationKey: t.ConversationKey, ActorID: t.ActorPlatformUserID,
				TriggerMessageIDs: t.TriggerMessageIDs, EnqueuedAt: at, ReadyAt: at,
			})
			requeued++
		}
	}
	o.log.Info("recovered unfinished tasks", "requeued", requeued, "stale", stale)
	o.maintOnce.Do(func() {
		o.bgWG.Add(1)
		go o.maintain()
	})
	return nil
}

func (o *Orchestrator) maintain() {
	defer o.bgWG.Done()
	expire := time.NewTicker(expireOutboxEvery)
	defer expire.Stop()
	purge := time.NewTicker(purgeEventsEvery)
	defer purge.Stop()
	o.expireOutbox()
	o.purgeEvents()
	for {
		select {
		case <-o.bgCtx.Done():
			return
		case <-expire.C:
			o.expireOutbox()
		case <-purge.C:
			o.purgeEvents()
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
