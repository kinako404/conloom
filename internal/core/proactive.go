package core

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kinako404/conloom/internal/interest"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	TriggerProactive = conloomapi.TriggerProactive

	proactiveDirectiveTerms = 5
	proactiveLogRetention   = 7 * 24 * time.Hour

	proactiveEvaluated = "evaluated"
	proactiveSent      = "sent"
	proactiveSkipped   = "skipped"
)

// keyedLocks serialises work per key with a fixed set of mutexes; keys that
// share a mutex are merely serialised with each other.
type keyedLocks struct{ mu [64]sync.Mutex }

func (l *keyedLocks) lock(key string) (unlock func()) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	m := &l.mu[h.Sum32()%uint32(len(l.mu))]
	m.Lock()
	return m.Unlock
}

func memberKey(convKey, userID string) string { return convKey + "\x00" + userID }

// engages reports whether the accepted message counts as the sender engaging
// the bot: a group message that mentions or replies to the bot, or continues a
// conversation with it. Commands never count. Only mentions and replies renew
// eligibility (see recordEngagement).
func engages(s *submission, d Decision) bool {
	if s.ev.Conversation.Type != conloomapi.ConvGroup || d.Disposition != conloomapi.DispositionReply || d.Command != "" {
		return false
	}
	switch d.Trigger {
	case conloomapi.TriggerMention, conloomapi.TriggerReply, TriggerContinuation:
		return true
	}
	return false
}

// recordEngagement updates the sender's interest profile and, for a mention
// or a reply to the bot (not a continuation), renews their eligibility. A
// new member gets the group's default level, which also replaces the level
// of a member who never chose one; a level the member set is kept.
func (o *Orchestrator) recordEngagement(ctx context.Context, s *submission, d Decision, pol conloomapi.GroupPolicy) {
	user := s.ev.Actor.PlatformUserID
	unlock := o.memberLocks.lock(memberKey(s.convKey, user))
	defer unlock()
	m, err := o.st.GetMember(ctx, s.convKey, user)
	switch {
	case errors.Is(err, store.ErrNotFound):
		m = store.MemberRecord{ConversationKey: s.convKey, PlatformUserID: user}
	case err != nil:
		o.log.Error("load member failed; engagement not recorded", "conversation", s.convKey, "error", err)
		return
	}
	if !m.LevelSetByUser {
		m.ProactiveLevel = pol.Proactive.DefaultLevel
	}
	if at := s.occurredAt(); d.Trigger != TriggerContinuation && at.After(m.LastEngagedAt) {
		m.LastEngagedAt = at
	}
	m.Interests = interest.Update(m.Interests, s.ev.Message.Text)
	m.UpdatedAt = s.now.UTC()
	if err := o.st.PutMember(ctx, m); err != nil {
		o.log.Error("store member failed; engagement not recorded", "conversation", s.convKey, "error", err)
	}
}

// proactiveTarget returns user's member record and whether the bot may speak
// up to them: proactive replies are on, user is eligible and their level is
// not off.
func (o *Orchestrator) proactiveTarget(ctx context.Context, convKey, user string, p conloomapi.ProactivePolicy, now time.Time) (store.MemberRecord, bool, error) {
	if !p.Enabled {
		return store.MemberRecord{}, false, nil
	}
	m, err := o.st.GetMember(ctx, convKey, user)
	if errors.Is(err, store.ErrNotFound) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	return m, eligible(m, p, now) && m.ProactiveLevel != conloomapi.ProactiveOff, nil
}

// eligible reports whether m engaged the bot within the eligibility TTL.
func eligible(m store.MemberRecord, p conloomapi.ProactivePolicy, now time.Time) bool {
	return !m.LastEngagedAt.IsZero() && now.Sub(m.LastEngagedAt) < p.EligibilityTTL.Duration
}

// userSendBudget reports whether the bot may still speak up to user today:
// today's replies to them are below their level's daily limit and the last
// one is at least the user cooldown ago.
func (o *Orchestrator) userSendBudget(ctx context.Context, convKey, user, level string, p conloomapi.ProactivePolicy, now time.Time) (bool, error) {
	if ok, err := o.underLimit(ctx, convKey, user, store.ProactiveSent, o.dayStart(now), p.DailyLimit(level)); err != nil || !ok {
		return false, err
	}
	if cd := p.UserCooldown.Duration; cd > 0 {
		return o.underLimit(ctx, convKey, user, store.ProactiveSent, now.Add(-cd), 1)
	}
	return true, nil
}

// groupSendBudget reports whether the group's proactive replies in the last
// hour are below its hourly limit.
func (o *Orchestrator) groupSendBudget(ctx context.Context, convKey string, p conloomapi.ProactivePolicy, now time.Time) (bool, error) {
	return o.underLimit(ctx, convKey, "", store.ProactiveSent, now.Add(-time.Hour), p.GroupHourlyLimit)
}

// underLimit reports whether fewer than limit proactive log entries of kind
// (for user, or anyone when user is "") were recorded since since.
func (o *Orchestrator) underLimit(ctx context.Context, convKey, user, kind string, since time.Time, limit int) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	n, err := o.st.CountProactive(ctx, convKey, user, kind, since)
	if err != nil {
		return false, err
	}
	return n < limit, nil
}

// dayStart is the most recent local midnight at or before now.
func (o *Orchestrator) dayStart(now time.Time) time.Time {
	t := now.In(o.loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, o.loc)
}

// proactiveSendAllowed re-checks, before a join task calls the model and
// just before its reply is stored, that the bot may still speak up: the group
// is not paused, proactive replies are on, the task's target is still a
// target within the send budget, no session started, and the bot did not
// speak in the group since the task was created nor within the join cooldown
// (looked up in the store first if this process does not know it yet, as for
// a task recovered after a restart). A guest join skips the session and bot
// speaking checks, as considerJoin did, but is not allowed once its target
// takes part in the session (a direct reply to them added them meanwhile).
// The caller of the send-time check holds the conversation's proactive lock.
func (o *Orchestrator) proactiveSendAllowed(ctx context.Context, task store.TaskRecord, guest bool) (bool, error) {
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return false, err
	}
	pol, now := pv.Policy, o.now()
	if err := o.ensureBotSpokeSeeded(ctx, task.ConversationKey); err != nil {
		return false, err
	}
	p := pol.Proactive
	spoke := o.lastBotSpoke(task.ConversationKey)
	if pol.Concurrency.MaxInteractiveRequests == 0 {
		return false, nil
	}
	window := pol.Reply.ContinuationWindow.Duration
	if guest {
		if o.inSession(task.ConversationKey, task.ActorPlatformUserID, now, window) {
			return false, nil
		}
	} else if o.sessionActive(task.ConversationKey, now, window) || spoke.After(task.CreatedAt) || now.Sub(spoke) < p.JoinCooldown.Duration {
		return false, nil
	}
	m, ok, err := o.proactiveTarget(ctx, task.ConversationKey, task.ActorPlatformUserID, p, now)
	if err != nil || !ok {
		return false, err
	}
	if ok, err := o.userSendBudget(ctx, task.ConversationKey, task.ActorPlatformUserID, m.ProactiveLevel, p, now); err != nil || !ok {
		return false, err
	}
	return o.groupSendBudget(ctx, task.ConversationKey, p, now)
}

// skipTrim is the punctuation allowed around a bare "SKIP" or "[SKIP]".
const skipTrim = `。．.!！?？、,，~～…：:；;"'“”‘’`

// isSkip reports whether a model output is the skip marker: after removing
// whitespace and zero-width characters, upper-casing and mapping full-width
// characters and 【】 to ASCII brackets, it contains SkipMarker or, with
// surrounding punctuation trimmed, is exactly "SKIP" or "[SKIP]".
func isSkip(content string) bool {
	var b strings.Builder
	for _, r := range content {
		switch {
		case unicode.IsSpace(r), r >= '\u200b' && r <= '\u200d', r == '\u2060', r == '\ufeff':
			continue
		case r == '【':
			r = '['
		case r == '】':
			r = ']'
		case r >= '！' && r <= '～':
			r -= '！' - '!'
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	s := b.String()
	if strings.Contains(s, SkipMarker) {
		return true
	}
	s = strings.Trim(s, skipTrim)
	return s == "SKIP" || s == "[SKIP]"
}

var proactiveLevelArgs = map[string]string{
	"关": conloomapi.ProactiveOff, "低": conloomapi.ProactiveLow, "中": conloomapi.ProactiveMid, "高": conloomapi.ProactiveHigh,
	conloomapi.ProactiveOff: conloomapi.ProactiveOff, conloomapi.ProactiveLow: conloomapi.ProactiveLow,
	conloomapi.ProactiveMid: conloomapi.ProactiveMid, conloomapi.ProactiveHigh: conloomapi.ProactiveHigh,
}

var proactiveSwitchArgs = map[string]bool{"开": true, "on": true, "关": false, "off": false}

// proactiveCommand runs /主动 (/proactive): no arguments show the sender's
// setting, a level sets it, and 全群 (group) 开|关 (on|off) switches the
// group's proactive replies.
func (o *Orchestrator) proactiveCommand(ctx context.Context, task store.TaskRecord, isAdmin bool, args string) (string, error) {
	if convType(task.ConversationKey) != conloomapi.ConvGroup {
		return CopyProactivePrivateOnly, nil
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.ToLower(args))
	switch {
	case len(fields) == 0:
		return o.proactiveStatus(ctx, task, pv.Policy.Proactive)
	case fields[0] == "全群" || fields[0] == "group":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		on, ok := false, false
		if len(fields) == 2 {
			on, ok = proactiveSwitchArgs[fields[1]]
		}
		if !ok {
			return CopyProactiveUsage, nil
		}
		return o.switchProactive(ctx, task, on)
	case len(fields) == 1:
		if level, ok := proactiveLevelArgs[fields[0]]; ok {
			return o.setProactiveLevel(ctx, task, pv.Policy.Proactive, level)
		}
	}
	return CopyProactiveUsage, nil
}

func (o *Orchestrator) proactiveStatus(ctx context.Context, task store.TaskRecord, p conloomapi.ProactivePolicy) (string, error) {
	m, err := o.st.GetMember(ctx, task.ConversationKey, task.ActorPlatformUserID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	now := o.now()
	sent, err := o.st.CountProactive(ctx, task.ConversationKey, task.ActorPlatformUserID, store.ProactiveSent, o.dayStart(now))
	if err != nil {
		return "", err
	}
	return CopyProactiveStatus(m.ProactiveLevel, eligible(m, p, now), sent, p.DailyLimit(m.ProactiveLevel), p.Enabled), nil
}

// setProactiveLevel stores level as the sender's own choice if they are
// eligible, under the member lock shared with engagement updates.
func (o *Orchestrator) setProactiveLevel(ctx context.Context, task store.TaskRecord, p conloomapi.ProactivePolicy, level string) (string, error) {
	user := task.ActorPlatformUserID
	unlock := o.memberLocks.lock(memberKey(task.ConversationKey, user))
	defer unlock()
	m, err := o.st.GetMember(ctx, task.ConversationKey, user)
	if errors.Is(err, store.ErrNotFound) {
		return CopyProactiveNotEligible, nil
	}
	if err != nil {
		return "", err
	}
	now := o.now()
	if !eligible(m, p, now) {
		return CopyProactiveNotEligible, nil
	}
	m.ProactiveLevel, m.LevelSetByUser, m.UpdatedAt = level, true, now.UTC()
	if err := o.st.PutMember(ctx, m); err != nil {
		return "", err
	}
	return CopyProactiveSet(level), nil
}

// switchProactive switches the group's proactive replies; switching to the
// current state stores nothing.
func (o *Orchestrator) switchProactive(ctx context.Context, task store.TaskRecord, on bool) (string, error) {
	_, changed, err := o.policies.SetProactiveEnabled(ctx, task.ConversationKey, on, "command:"+task.ActorPlatformUserID)
	if err != nil && !changed {
		return "", err
	}
	o.logPolicyAuditError(task, err)
	if on {
		return CopyProactiveGroupOn, nil
	}
	return CopyProactiveGroupOff, nil
}

// convType returns the type part of a "bot:platform:type:id" conversation key.
func convType(convKey string) string {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) != 4 {
		return ""
	}
	return parts[2]
}

func (o *Orchestrator) purgeProactiveLog() {
	n, err := o.st.PurgeProactiveLog(o.bgCtx, o.now().Add(-proactiveLogRetention))
	if err != nil {
		if o.bgCtx.Err() == nil {
			o.log.Error("purge proactive log failed", "error", err)
		}
		return
	}
	if n > 0 {
		o.log.Info("purged proactive log", "count", n)
	}
}
