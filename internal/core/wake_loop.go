package core

import (
	"context"
	"errors"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// wakePollInterval is how often wakeLoop claims due timers, wakeLease how
	// long a claimed timer is left to its poll before it may be claimed again
	// (after a restart, say), and wakeMissedGrace how late a timer may be
	// handled before it counts as missed.
	wakePollInterval = 15 * time.Second
	wakeLease        = 2 * time.Minute
	wakeMissedGrace  = 30 * time.Minute
	// wakeBusyDelay postpones a due initiate or follow-up while its chat is
	// busy; statusReturnDelay a status return, at most wakeMissedGrace past
	// the end of its status.
	wakeBusyDelay     = 2 * time.Minute
	statusReturnDelay = time.Minute
	// wakeClaimLimit is the most timers one poll claims.
	wakeClaimLimit = 100
	// followUpJitterMinutes bounds the random minutes added to a follow-up
	// moved out of quiet hours or to the next day.
	followUpJitterMinutes = 60
)

// wakeAct is what the checks of a due initiate or follow-up decided.
type wakeAct int

const (
	wakeFire wakeAct = iota
	wakeDrop
	wakeReplan
	wakeMove
)

// wakeVerdict is a wakeAct with the check that decided it and, for wakeMove,
// the time the timer moves to.
type wakeVerdict struct {
	act    wakeAct
	reason string
	at     time.Time
}

// wakeLoop polls for due timers (pollWakes) at once and then every
// wakeEvery until ctx is done.
func (o *Orchestrator) wakeLoop(ctx context.Context) {
	ticker := time.NewTicker(o.wakeEvery)
	defer ticker.Stop()
	for {
		o.pollWakes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollWakes claims the timers due at o.now(), at most wakeClaimLimit, and
// handles each in turn (dueStatusReturn, dueStart). It claims nothing once
// the orchestrator is closing and stops between timers then; a claimed timer
// left behind is claimed again once its lease ends. A failed check is logged
// and leaves the timer to that next claim.
func (o *Orchestrator) pollWakes(ctx context.Context) {
	if o.closing.Load() {
		return
	}
	timers, err := o.st.ClaimDueTimers(ctx, o.now(), wakeLease, wakeClaimLimit)
	if err != nil {
		if ctx.Err() == nil {
			o.log.Error("claim due wake timers failed", "error", err)
		}
		return
	}
	for _, t := range timers {
		if o.closing.Load() || ctx.Err() != nil {
			return
		}
		handle := o.dueStart
		if t.Kind == store.TimerStatusReturn {
			handle = o.dueStatusReturn
		}
		if err := handle(ctx, t); err != nil && ctx.Err() == nil {
			o.log.Error("wake timer check failed; retried once its lease ends", "conversation", t.ConversationKey,
				"kind", string(t.Kind), "error", err)
		}
	}
}

// dueStart handles a due initiate or follow-up t (spec §7.4). One more than
// wakeMissedGrace late is missed: an initiate is planned again, a follow-up
// dropped. Otherwise checkStart decides, and t is fired unless it says
// otherwise; while fireTimer finds the chat busy, t is postponed by
// wakeBusyDelay.
func (o *Orchestrator) dueStart(ctx context.Context, t store.ConversationTimer) error {
	now := o.now()
	if now.Sub(t.DueAt) > wakeMissedGrace {
		o.wakeOutcome(t, wakeMissed, "missed")
		if t.Kind == store.TimerInitiate {
			o.replanInitiate(ctx, t.ConversationKey)
			return nil
		}
		return o.deleteTimer(ctx, t)
	}
	v, err := o.checkStart(ctx, t, now)
	if err != nil {
		return err
	}
	switch v.act {
	case wakeDrop:
		o.wakeOutcome(t, wakeDropped, v.reason)
		return o.deleteTimer(ctx, t)
	case wakeReplan:
		o.wakeOutcome(t, wakePostponed, v.reason)
		o.replanInitiate(ctx, t.ConversationKey)
		return nil
	case wakeMove:
		o.wakeOutcome(t, wakePostponed, v.reason, "until", v.at.UTC())
		return o.moveTimer(ctx, t, v.at)
	}
	err = o.fireTimer(ctx, t)
	if errors.Is(err, errBusy) || errors.Is(err, errUserSpeaking) {
		at := now.Add(wakeBusyDelay)
		o.wakeOutcome(t, wakePostponed, "busy", "until", at.UTC())
		return o.moveTimer(ctx, t, at)
	}
	return err
}

// checkStart runs the checks of a due initiate or follow-up t in order: the
// chat must still be a private chat, not one started in the panel (else
// drop); the bot must still be allowed to start messages in it (mayStart,
// else drop); the user, if ever forgotten, must have written since (else
// drop: a forget racing the plan that armed t leaves no message to start
// from); the chat must have no status still lasting (else an initiate is
// planned again, after the status, and a follow-up dropped); now must be
// outside its quiet hours and today below the daily cap (else an initiate is
// planned again and a follow-up moved by followUpAgain); the messages the bot
// sent on its own since the user's last one must leave room for t
// (unansweredAllows, else drop); the user must not have written since t was
// armed (else drop); and the chat must have no queued or running task and the
// user's newest message must be private_max_wait old (else postpone by
// wakeBusyDelay).
func (o *Orchestrator) checkStart(ctx context.Context, t store.ConversationTimer, now time.Time) (wakeVerdict, error) {
	drop := func(reason string) (wakeVerdict, error) { return wakeVerdict{act: wakeDrop, reason: reason}, nil }
	key := t.ConversationKey
	if convType(key) != conloomapi.ConvPrivate {
		return drop("not_private")
	}
	conv, err := o.conversation(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return drop("no_conversation")
	case err != nil:
		return wakeVerdict{}, err
	case conv.Platform == store.PlatformPanel:
		return drop("panel")
	}
	pv, err := o.policies.Current(ctx, key)
	if err != nil {
		return wakeVerdict{}, err
	}
	pol := pv.Policy
	if !o.mayStart(pol, t.BotInstanceID, conv.Platform, t.PlatformUserID) {
		return drop("not_allowed")
	}

	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(key), []string{t.PlatformUserID})
	if err != nil {
		return wakeVerdict{}, err
	}
	forgotten := rows[t.PlatformUserID].ForgottenAt
	if !forgotten.IsZero() {
		last, ok, err := o.lastUserMessage(ctx, key)
		if err != nil {
			return wakeVerdict{}, err
		}
		if !ok || forgotten.After(last) {
			return drop("forgotten")
		}
	}
	switch st, err := o.st.GetStatus(ctx, key); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return wakeVerdict{}, err
	case st.UntilAt.After(now):
		if t.Kind == store.TimerInitiate {
			return wakeVerdict{act: wakeReplan, reason: "status"}, nil
		}
		return drop("status")
	}
	q := o.quietFor(ctx, key, forgotten)
	today, err := o.privateStarted(ctx, key, o.dayStart(now))
	if err != nil {
		return wakeVerdict{}, err
	}
	quiet, atCap := q.Contains(now, o.loc), today >= autonomy.DailyCap(pol.Proactive.Private.EffectiveLevel())
	if quiet || atCap {
		reason := "quiet"
		if !quiet {
			reason = "daily_cap"
		}
		if t.Kind == store.TimerInitiate {
			return wakeVerdict{act: wakeReplan, reason: reason}, nil
		}
		return wakeVerdict{act: wakeMove, reason: reason, at: o.followUpAgain(now, q, atCap)}, nil
	}

	started, total, err := o.unanswered(ctx, key)
	if err != nil {
		return wakeVerdict{}, err
	}
	if !unansweredAllows(t.Kind, started, total) {
		return drop("unanswered")
	}
	msgs, err := o.st.RecentMessages(ctx, key, wakeRecentScan)
	if err != nil {
		return wakeVerdict{}, err
	}
	for _, m := range msgs {
		if !m.FromBot && m.Seq > t.AnchorSeq {
			return drop("user_message")
		}
	}
	if o.chatBusy(key, msgs, pol, now) {
		return wakeVerdict{act: wakeMove, reason: "busy", at: now.Add(wakeBusyDelay)}, nil
	}
	return wakeVerdict{act: wakeFire}, nil
}

// dueStatusReturn handles a due status return t (spec §7.4), which does not
// look at the daily cap. Without a status, or with a newer one still lasting,
// t is dropped. Otherwise the status ends, without a message, when it ended
// more than wakeMissedGrace ago, when the chat is gone, proactive replies are
// off, the chat is paused, its user may no longer chat privately or now is in
// the chat's quiet hours (quietFor), when the user is asleep. While the chat
// is busy (chatBusy, or fireTimer refusing) t is postponed by
// statusReturnDelay, and the status ends once that would take it past
// wakeMissedGrace after the status ended.
func (o *Orchestrator) dueStatusReturn(ctx context.Context, t store.ConversationTimer) error {
	now, key := o.now(), t.ConversationKey
	st, err := o.st.GetStatus(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		o.wakeOutcome(t, wakeDropped, "no_status")
		return o.deleteTimer(ctx, t)
	case err != nil:
		return err
	case st.UntilAt.After(now):
		o.wakeOutcome(t, wakeDropped, "status_lasts")
		return o.deleteTimer(ctx, t)
	}
	ended := t.DueAt
	if st.UntilAt.Before(ended) {
		ended = st.UntilAt
	}
	end := func(result, reason string) error {
		o.wakeOutcome(t, result, reason)
		cleared, err := o.st.ClearStatus(ctx, key, st.UntilAt)
		if err != nil || cleared {
			return err
		}
		return o.deleteTimer(ctx, t)
	}
	if now.Sub(ended) > wakeMissedGrace {
		return end(wakeMissed, "missed")
	}
	conv, err := o.conversation(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return end(wakeDropped, "no_conversation")
	case err != nil:
		return err
	}
	pv, err := o.policies.Current(ctx, key)
	if err != nil {
		return err
	}
	pol := pv.Policy
	bot, ok := o.bots.Bot(t.BotInstanceID)
	if !ok || !pol.Proactive.Enabled || pol.Concurrency.MaxInteractiveRequests == 0 ||
		!privateAllowed(bot, conv.Platform, t.PlatformUserID) {
		return end(wakeDropped, "not_allowed")
	}
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(key), []string{t.PlatformUserID})
	if err != nil {
		return err
	}
	if o.quietFor(ctx, key, rows[t.PlatformUserID].ForgottenAt).Contains(now, o.loc) {
		return end(wakeDropped, "quiet")
	}
	postpone := func() error {
		at := now.Add(statusReturnDelay)
		if at.After(ended.Add(wakeMissedGrace)) {
			return end(wakeDropped, "busy")
		}
		o.wakeOutcome(t, wakePostponed, "busy", "until", at.UTC())
		return o.moveTimer(ctx, t, at)
	}
	msgs, err := o.st.RecentMessages(ctx, key, wakeRecentScan)
	if err != nil {
		return err
	}
	if o.chatBusy(key, msgs, pol, now) {
		return postpone()
	}
	err = o.fireTimer(ctx, t)
	if errors.Is(err, errBusy) || errors.Is(err, errUserSpeaking) {
		return postpone()
	}
	return err
}

// chatBusy reports whether the private chat has queued or running tasks, or
// its user's newest message among msgs was received less than the policy's
// private_max_wait before now.
func (o *Orchestrator) chatBusy(convKey string, msgs []store.MessageRecord, pol conloomapi.GroupPolicy, now time.Time) bool {
	if o.sched.Busy(convKey) {
		return true
	}
	for _, m := range msgs {
		if !m.FromBot && now.Sub(m.CreatedAt) < pol.Reply.PrivateMaxWait.Duration {
			return true
		}
	}
	return false
}

// followUpAgain is when a follow-up due now in quiet hours q, or on a day the
// bot already started its daily cap of messages (atCap), is tried again: when
// atCap, when the next local day opens after q (Quiet.Morning: the end of the
// quiet hours of that night or morning); else past the end of q; plus up to
// followUpJitterMinutes random minutes that keep it outside q.
func (o *Orchestrator) followUpAgain(now time.Time, q autonomy.Quiet, atCap bool) time.Time {
	at := now
	if atCap {
		at = q.Morning(o.dayStart(now).AddDate(0, 0, 1), o.loc)
	}
	if q.Contains(at, o.loc) {
		at = q.EndAfter(at, o.loc)
	}
	if jittered := at.Add(time.Duration(o.rand.IntN(followUpJitterMinutes)) * time.Minute); !q.Contains(jittered, o.loc) {
		return jittered
	}
	return at
}

// wakeOutcome counts and logs what the poll did with t.
func (o *Orchestrator) wakeOutcome(t store.ConversationTimer, result, reason string, attrs ...any) {
	o.metrics.Wake.WithLabelValues(string(t.Kind), result).Inc()
	o.log.Info("wake timer "+result, append([]any{"conversation", t.ConversationKey, "kind", string(t.Kind),
		"due_at", t.DueAt.UTC(), "reason", reason}, attrs...)...)
}

// deleteTimer deletes t unless it was cancelled, re-armed or postponed since
// it was claimed.
func (o *Orchestrator) deleteTimer(ctx context.Context, t store.ConversationTimer) error {
	_, err := o.st.DeleteTimer(ctx, timerRef(t))
	return err
}

// moveTimer moves t to at, unclaimed, unless it was cancelled, re-armed or
// postponed since it was claimed.
func (o *Orchestrator) moveTimer(ctx context.Context, t store.ConversationTimer, at time.Time) error {
	_, err := o.st.PostponeTimer(ctx, timerRef(t), at)
	return err
}
