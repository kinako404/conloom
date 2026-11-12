package core

import (
	"context"
	"errors"
	"hash/fnv"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/interest"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	TriggerProactive = conloomapi.TriggerProactive

	proactiveMinRunes      = 4
	proactiveDirectiveTerm = 5
	// proactiveEvalFactor bounds model calls: evaluations per user per day
	// and per group per hour are this many times the matching send limit.
	proactiveEvalFactor   = 3
	proactiveLogRetention = 7 * 24 * time.Hour
	// contactRetention is the longest user cooldown policy validation allows.
	contactRetention = 24 * time.Hour

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

// contactTimes remembers, per conversation member, when the bot last replied
// to them or their continuation ended. It lives in memory only.
type contactTimes struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (c *contactTimes) set(key string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at == nil {
		c.at = map[string]time.Time{}
	}
	c.at[key] = at
}

func (c *contactTimes) get(key string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.at[key]
	return at, ok
}

// prune forgets contacts before cutoff and returns how many it forgot.
func (c *contactTimes) prune(cutoff time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, at := range c.at {
		if at.Before(cutoff) {
			delete(c.at, k)
			n++
		}
	}
	return n
}

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

// proactiveCandidate applies the in-memory proactive filters to an observed
// message: a fresh group message long enough, not a command, mentioning
// nobody, not replying to another member, in an unpaused group with proactive
// replies on, from a sender with no unfinished task whose last contact with
// the bot (a reply or the end of a continuation) is at least the user
// cooldown ago.
func (o *Orchestrator) proactiveCandidate(s *submission, pol conloomapi.GroupPolicy) bool {
	text := strings.TrimSpace(s.ev.Message.Text)
	switch {
	case s.ev.Conversation.Type != conloomapi.ConvGroup, !pol.Proactive.Enabled,
		pol.Concurrency.MaxInteractiveRequests == 0,
		s.now.Sub(s.occurredAt()) >= o.staleAfter,
		utf8.RuneCountInString(text) < proactiveMinRunes,
		pol.Reply.CommandPrefix != "" && strings.HasPrefix(text, pol.Reply.CommandPrefix),
		len(s.ev.Message.Mentions) > 0,
		s.replyToOther:
		return false
	}
	user := s.ev.Actor.PlatformUserID
	if at, ok := o.contacts.get(memberKey(s.convKey, user)); ok && s.now.Sub(at) < pol.Proactive.UserCooldown.Duration {
		return false
	}
	return !slices.ContainsFunc(o.sched.Active(s.convKey), func(t QueuedTask) bool { return t.ActorID == user })
}

// hasPendingProactive reports whether the sender has an unfinished proactive
// task in the conversation.
func (o *Orchestrator) hasPendingProactive(ctx context.Context, s *submission) bool {
	user := s.ev.Actor.PlatformUserID
	for _, t := range o.sched.Active(s.convKey) {
		if t.ActorID != user {
			continue
		}
		rec, err := o.st.GetTask(ctx, s.botID, t.TaskID)
		if err != nil {
			o.log.Warn("load task failed", "task_id", t.TaskID, "error", err)
			continue
		}
		if rec.Meta.Trigger == TriggerProactive {
			return true
		}
	}
	return false
}

// submitProactive handles an observed proactive candidate: if the sender is
// a proactive target, the message matches their interests and the send and
// evaluation budgets allow it, it creates a proactive task and records the
// evaluation; otherwise the message is admitted like any observed message.
// The conversation's proactive lock makes the budget check and the
// evaluation record atomic.
func (o *Orchestrator) submitProactive(ctx context.Context, s *submission, pol conloomapi.GroupPolicy) (conloomapi.EventAccepted, error) {
	unlock := o.proactiveLocks.lock(s.convKey)
	defer unlock()
	user := s.ev.Actor.PlatformUserID
	ok, err := o.proactiveEvaluable(ctx, s.convKey, user, s.ev.Message.Text, pol.Proactive, s.now)
	if err != nil {
		o.log.Error("proactive check failed", "conversation", s.convKey, "error", err)
	}
	if !ok {
		return o.admit(ctx, s, AdmitMergeOnly, "")
	}
	resp, err := o.admit(ctx, s, AdmitNew, TriggerProactive)
	if errors.Is(err, ErrQueueFull) {
		return o.observeOnly(ctx, s, "")
	}
	if err != nil || resp.TaskID == "" || resp.Merged {
		return resp, err
	}
	if err := o.st.AppendProactiveLog(ctx, store.ProactiveLogEntry{
		ConversationKey: s.convKey, PlatformUserID: user, Kind: store.ProactiveEvaluated,
		TaskID: resp.TaskID, CreatedAt: s.now.UTC(),
	}); err != nil {
		// Without the record the evaluation budget cannot hold, so the
		// evaluation does not happen.
		o.log.Error("record proactive evaluation failed; cancelling the task", "task_id", resp.TaskID, "error", err)
		if _, cerr := o.cancel(ctx, s.botID, s.convKey, resp.TaskID); cerr != nil {
			o.log.Error("cancel unrecorded proactive task failed", "task_id", resp.TaskID, "error", cerr)
		}
		return resp, nil
	}
	o.metrics.Proactive.WithLabelValues(proactiveEvaluated).Inc()
	o.log.Info("proactive evaluation", "conversation", s.convKey, "task_id", resp.TaskID,
		obs.MessageAttr(o.cfg.Observability, s.ev.Message.Text))
	return resp, nil
}

// proactiveEvaluable reports whether the model may evaluate a proactive reply
// to user's text: user is a proactive target, text matches their interests,
// and both the send and the evaluation budgets have room.
func (o *Orchestrator) proactiveEvaluable(ctx context.Context, convKey, user, text string, p conloomapi.ProactivePolicy, now time.Time) (bool, error) {
	m, ok, err := o.proactiveTarget(ctx, convKey, user, p, now)
	if err != nil || !ok {
		return false, err
	}
	if _, ok := interest.Match(m.Interests, text, m.ProactiveLevel); !ok {
		return false, nil
	}
	return o.proactiveBudget(ctx, convKey, user, m.ProactiveLevel, p, now, true)
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

// proactiveBudget reports whether the send budget has room: today's replies
// to user are below their level's daily limit, the last one is at least the
// user cooldown ago, and the group spoke up fewer than its hourly limit in
// the last hour. With evaluate it also checks the evaluation budget, which is
// proactiveEvalFactor times the daily and hourly limits.
func (o *Orchestrator) proactiveBudget(ctx context.Context, convKey, user, level string, p conloomapi.ProactivePolicy, now time.Time, evaluate bool) (bool, error) {
	type budget struct {
		user, kind string
		since      time.Time
		limit      int
	}
	daily := p.DailyLimit(level)
	day, hour := o.dayStart(now), now.Add(-time.Hour)
	checks := []budget{
		{user, store.ProactiveSent, day, daily},
		{"", store.ProactiveSent, hour, p.GroupHourlyLimit},
	}
	if cd := p.UserCooldown.Duration; cd > 0 {
		checks = append(checks, budget{user, store.ProactiveSent, now.Add(-cd), 1})
	}
	if evaluate {
		checks = append(checks,
			budget{user, store.ProactiveEvaluated, day, proactiveEvalFactor * daily},
			budget{"", store.ProactiveEvaluated, hour, proactiveEvalFactor * p.GroupHourlyLimit})
	}
	for _, c := range checks {
		if c.limit <= 0 {
			return false, nil
		}
		n, err := o.st.CountProactive(ctx, convKey, c.user, c.kind, c.since)
		if err != nil {
			return false, err
		}
		if n >= c.limit {
			return false, nil
		}
	}
	return true, nil
}

// dayStart is the most recent local midnight at or before now.
func (o *Orchestrator) dayStart(now time.Time) time.Time {
	t := now.In(o.loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, o.loc)
}

// proactiveSendAllowed re-checks, just before a proactive reply is stored,
// that the bot may still speak up to the task's user within the send budget.
// The caller holds the conversation's proactive lock.
func (o *Orchestrator) proactiveSendAllowed(ctx context.Context, task store.TaskRecord) (bool, error) {
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return false, err
	}
	p, now := pv.Policy.Proactive, o.now()
	if pv.Policy.Concurrency.MaxInteractiveRequests == 0 {
		return false, nil
	}
	m, ok, err := o.proactiveTarget(ctx, task.ConversationKey, task.ActorPlatformUserID, p, now)
	if err != nil || !ok {
		return false, err
	}
	return o.proactiveBudget(ctx, task.ConversationKey, task.ActorPlatformUserID, m.ProactiveLevel, p, now, false)
}

// proactiveDirective is the hint for a proactive task, naming the user's top
// interest terms.
func (o *Orchestrator) proactiveDirective(ctx context.Context, task store.TaskRecord) string {
	m, err := o.st.GetMember(ctx, task.ConversationKey, task.ActorPlatformUserID)
	if err != nil {
		o.log.Warn("load member for proactive directive failed", "task_id", task.ID, "error", err)
	}
	return CopyDirectiveProactive(interest.Top(m.Interests, proactiveDirectiveTerm))
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

// pruneContacts forgets last contacts older than any user cooldown can be.
func (o *Orchestrator) pruneContacts() {
	if n := o.contacts.prune(o.now().Add(-contactRetention)); n > 0 {
		o.log.Debug("pruned last contacts", "count", n)
	}
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
