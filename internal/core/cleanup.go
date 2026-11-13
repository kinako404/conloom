package core

import (
	"time"

	"github.com/kinako404/conloom/internal/config"
)

const (
	defaultCleanupBatch      = 1000
	defaultCleanupMaxBatches = 10
)

// Table labels of conloom_cleanup_deleted_total.
const (
	cleanupTasks     = "tasks"
	cleanupOutbox    = "outbox_actions"
	cleanupAudit     = "audit_events"
	cleanupEvents    = "events"
	cleanupProactive = "proactive_log"
	cleanupUsage     = "model_calls"
	cleanupPanel     = "panel_sessions"
)

// cleanup deletes finished tasks together with their outbox actions, acked or
// expired actions left without a task, old audit events, old model calls and
// ended panel sessions. Tasks and actions are kept for runtime.ops_retention,
// and at least as long as the idempotency records whose stored responses name
// them; audit events for runtime.audit_retention; model calls for
// runtime.usage_retention. Panel sessions go once past their expiry or idle
// for panel.idle_timeout, and all of them without a panel section. Each table
// but the panel sessions is cleaned in transactions of at most cleanupBatch
// rows, at most cleanupMaxBatches per round, so the write lock is never held
// for long; what is left goes in the next round. Messages, memory, policies
// and personas are never touched.
func (o *Orchestrator) cleanup() {
	now := o.now()
	ops := retentionOr(o.cfg.Runtime.OpsRetention.Duration, config.DefaultOpsRetention)
	idem := retentionOr(o.cfg.Runtime.IdempotencyRetention.Duration, defaultIdempotencyRetention)
	opsBefore := now.Add(-max(ops, idem))
	auditBefore := now.Add(-retentionOr(o.cfg.Runtime.AuditRetention.Duration, config.DefaultAuditRetention))
	usageBefore := now.Add(-retentionOr(o.cfg.Runtime.UsageRetention.Duration, config.DefaultUsageRetention))
	idleBefore := now
	if o.cfg.Panel != nil {
		idleBefore = now.Add(-o.cfg.Panel.IdleTimeout.Duration)
	}

	var tasks, actions int
	moreTasks := o.inBatches(cleanupTasks, func(limit int) (int, error) {
		t, a, err := o.st.PurgeFinishedTasks(o.bgCtx, opsBefore, limit)
		tasks += t
		actions += a
		return t, err
	})
	var orphans int
	moreOutbox := o.inBatches(cleanupOutbox, func(limit int) (int, error) {
		n, err := o.st.PurgeOutbox(o.bgCtx, opsBefore, limit)
		orphans += n
		return n, err
	})
	var audits int
	moreAudit := o.inBatches(cleanupAudit, func(limit int) (int, error) {
		n, err := o.st.PurgeAudit(o.bgCtx, auditBefore, limit)
		audits += n
		return n, err
	})
	var calls int
	moreUsage := o.inBatches(cleanupUsage, func(limit int) (int, error) {
		n, err := o.st.PurgeModelCalls(o.bgCtx, usageBefore, limit)
		calls += n
		return n, err
	})
	sessions, err := o.st.PurgePanelSessions(o.bgCtx, now, idleBefore)
	if err != nil && o.bgCtx.Err() == nil {
		o.log.Error("cleanup failed", "table", cleanupPanel, "error", err)
	}
	if o.bgCtx.Err() != nil {
		return
	}
	o.countCleanup(cleanupTasks, tasks)
	o.countCleanup(cleanupOutbox, actions+orphans)
	o.countCleanup(cleanupAudit, audits)
	o.countCleanup(cleanupUsage, calls)
	o.countCleanup(cleanupPanel, sessions)
	o.log.Info("cleaned up operational records", "tasks", tasks, "outbox_actions", actions+orphans,
		"audit_events", audits, "model_calls", calls, "panel_sessions", sessions,
		"more", moreTasks || moreOutbox || moreAudit || moreUsage)
}

// inBatches calls purge with the batch size until a batch deletes fewer rows,
// purge fails or the round's batches are used up. It reports whether the last
// batch was full, that is, rows may be left for the next round.
func (o *Orchestrator) inBatches(table string, purge func(limit int) (int, error)) (more bool) {
	for range o.cleanupMaxBatches {
		if o.bgCtx.Err() != nil {
			return false
		}
		n, err := purge(o.cleanupBatch)
		if err != nil {
			if o.bgCtx.Err() == nil {
				o.log.Error("cleanup failed", "table", table, "error", err)
			}
			return false
		}
		if n < o.cleanupBatch {
			return false
		}
	}
	return true
}

func (o *Orchestrator) countCleanup(table string, n int) {
	if n > 0 {
		o.metrics.CleanupDeleted.WithLabelValues(table).Add(float64(n))
	}
}

// retentionOr returns d, or def when d is not positive.
func retentionOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}
