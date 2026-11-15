package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// replanInitiate plans when the bot next starts the private chat convKey
// (spec §7.2): it cancels the chat's initiate and follow-up and arms a new
// initiate (nextInitiate), or none. With exactly one message started since
// the user's last one, the follow-up of that initiate is kept and no initiate
// armed. It runs under the conversation's proactive lock, which its callers
// must not hold, and does nothing for a group or once the orchestrator is
// closing. Failures are logged.
func (o *Orchestrator) replanInitiate(ctx context.Context, convKey string) {
	o.replanAfter(ctx, convKey, time.Time{})
}

// replanAfter is replanInitiate for a chat in which the bot spoke at spoke
// with a message not stored yet (one is stored once delivered): the level's
// gap counts from spoke when that is later than the chat's newest message.
func (o *Orchestrator) replanAfter(ctx context.Context, convKey string, spoke time.Time) {
	if convType(convKey) != conloomapi.ConvPrivate || o.closing.Load() {
		return
	}
	unlock := o.proactiveLocks.lock(convKey)
	defer unlock()
	next, keepFollowUp, reason, err := o.nextInitiate(ctx, convKey, spoke)
	if err != nil {
		o.log.Error("plan initiate failed; none planned", "conversation", convKey, "error", err)
		reason = "error"
	}
	kinds := []store.TimerKind{store.TimerInitiate}
	if !keepFollowUp {
		kinds = append(kinds, store.TimerFollowUp)
	}
	if _, err := o.st.CancelTimers(ctx, convKey, kinds...); err != nil {
		o.log.Error("cancel initiate before planning failed", "conversation", convKey, "error", err)
		return
	}
	if next == nil {
		o.log.Debug("no initiate planned", "conversation", convKey, "reason", reason)
		return
	}
	if err := o.st.ArmTimer(ctx, *next); err != nil {
		o.log.Error("arm initiate failed", "conversation", convKey, "error", err)
		return
	}
	o.log.Info("wake timer armed", "conversation", convKey, "kind", string(store.TimerInitiate), "due_at", next.DueAt.UTC())
}

// nextInitiate returns the initiate to arm in the private chat convKey, or
// nil with the reason none is: the chat is gone or was started in the panel;
// the bot may not start messages in it (mayStart); it already sent
// autonomy.MaxUnanswered messages on its own since the user's last one
// (unanswered, status returns included), or started an initiate, whose
// follow-up is then kept (keepFollowUp); the user wrote nothing within
// proactiveLogRetention, the time started messages stay logged, or since they
// were forgotten; or autonomy.PlanInitiate finds no time. The plan counts the
// level's gap from the chat's newest message, from either side, from spoke,
// or from the end of the chat's status still lasting, whichever is latest,
// uses the quiet hours and hourly rhythm of the user's messages (userTimes)
// and today's started messages, and anchors the timer at that newest message.
func (o *Orchestrator) nextInitiate(ctx context.Context, convKey string, spoke time.Time) (next *store.ConversationTimer, keepFollowUp bool, reason string, err error) {
	conv, err := o.conversation(ctx, convKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, false, "no_conversation", nil
	case err != nil:
		return nil, false, "", err
	case conv.Platform == store.PlatformPanel:
		return nil, false, "panel", nil
	}
	user := conv.PlatformConversationID
	pv, err := o.policies.Current(ctx, convKey)
	if err != nil {
		return nil, false, "", err
	}
	pol := pv.Policy
	if !o.mayStart(pol, conv.BotInstanceID, conv.Platform, user) {
		return nil, false, "not_allowed", nil
	}
	switch started, total, err := o.unanswered(ctx, convKey); {
	case err != nil:
		return nil, false, "", err
	case total >= autonomy.MaxUnanswered:
		return nil, false, "unanswered", nil
	case started > 0:
		return nil, true, "follow_up", nil
	}
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(convKey), []string{user})
	if err != nil {
		return nil, false, "", err
	}
	times, err := o.userTimes(ctx, convKey, rows[user].ForgottenAt)
	if err != nil {
		return nil, false, "", err
	}
	now := o.now()
	if len(times) == 0 || now.Sub(times[0]) > proactiveLogRetention {
		return nil, false, "silent", nil
	}
	newest, err := o.st.RecentMessages(ctx, convKey, 1)
	if err != nil {
		return nil, false, "", err
	}
	if len(newest) == 0 {
		return nil, false, "silent", nil
	}
	today, err := o.privateStarted(ctx, convKey, o.dayStart(now))
	if err != nil {
		return nil, false, "", err
	}
	last := newest[0].OccurredAt
	if spoke.After(last) {
		last = spoke
	}
	switch st, err := o.st.GetStatus(ctx, convKey); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, false, "", err
	case st.UntilAt.After(now) && st.UntilAt.After(last):
		last = st.UntilAt
	}
	// Started messages are logged at o.now(), so no later day has any.
	day := now.In(o.loc).Format(time.DateOnly)
	due, ok := autonomy.PlanInitiate(autonomy.PlanInput{
		Now: now, Last: last, Level: pol.Proactive.Private.EffectiveLevel(),
		Quiet: autonomy.ResolveQuiet(pol.Proactive.Private, times, o.loc),
		Hist:  autonomy.HistOf(times, o.loc), Enough: autonomy.EnoughData(times, o.loc),
		SentOn: func(d string) int {
			if d == day {
				return today
			}
			return 0
		},
		Loc: o.loc, Rand: o.rand,
	})
	if !ok {
		return nil, false, "no_time", nil
	}
	return &store.ConversationTimer{
		ConversationKey: convKey, Kind: store.TimerInitiate, BotInstanceID: conv.BotInstanceID, PlatformUserID: user,
		DueAt: due, AnchorSeq: newest[0].Seq, CreatedAt: now.UTC(),
	}, false, "", nil
}

// privateProactiveStatus describes how the bot starts the private chat
// convKey (see conloomapi.AutonomyProactive): the policy's switch and
// effective level, today's started messages and the level's cap, the quiet
// hours in effect (quietFor), how many more messages it may start before the
// user writes and the chat's timers.
func (o *Orchestrator) privateProactiveStatus(ctx context.Context, convKey string) (conloomapi.AutonomyProactive, error) {
	if convType(convKey) != conloomapi.ConvPrivate {
		return conloomapi.AutonomyProactive{}, fmt.Errorf("core: %s is not a private chat", convKey)
	}
	conv, err := o.conversation(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	pv, err := o.policies.Current(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	user := conv.PlatformConversationID
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(convKey), []string{user})
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	sent, err := o.privateStarted(ctx, convKey, o.dayStart(o.now()))
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	_, n, err := o.unanswered(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	timers, err := o.st.ListTimers(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyProactive{}, err
	}
	q := o.quietFor(ctx, convKey, rows[user].ForgottenAt)
	level := pv.Policy.Proactive.Private.EffectiveLevel()
	out := conloomapi.AutonomyProactive{
		Enabled: pv.Policy.Proactive.Enabled, Level: level, SentToday: sent, DailyCap: autonomy.DailyCap(level),
		QuietStart: minuteClock(q.Start), QuietEnd: minuteClock(q.End), QuietInferred: q.Inferred,
		UnansweredLeft: max(autonomy.MaxUnanswered-n, 0), Timers: make([]conloomapi.AutonomyTimer, 0, len(timers)),
	}
	for _, t := range timers {
		out.Timers = append(out.Timers, conloomapi.AutonomyTimer{Kind: string(t.Kind), DueAt: t.DueAt})
	}
	return out, nil
}

// minuteClock formats minutes of the day as "HH:MM".
func minuteClock(m int) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}
