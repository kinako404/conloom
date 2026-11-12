package core

import (
	"context"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Reasons a member leaves, or a session ends, counted in
// conloom_continuation_exits_total.
const (
	sessionExitRule  = "rule"
	sessionExitModel = "model"
	sessionExitLimit = "limit"
	sessionExitIdle  = "idle"
)

// chatSession is the bot's ongoing conversation in a group: the members it is
// talking with, when the conversation was last active, the replies the bot
// gave since it was last addressed directly and the turns in a row it chose
// not to answer.
type chatSession struct {
	participants map[string]bool
	lastActivity time.Time
	unprompted   int
	skips        int
}

// chatSessions holds each group's session and when the bot last spoke there.
// It lives in memory only, so a restart ends every session.
type chatSessions struct {
	mu       sync.Mutex
	byConv   map[string]*chatSession
	botSpoke map[string]time.Time
}

// liveLocked returns the conversation's session, or nil if there is none. A
// session whose last activity is idle or longer ago (or any session when idle
// <= 0) is ended as idle. The caller holds o.sessions.mu.
func (o *Orchestrator) liveLocked(convKey string, now time.Time, idle time.Duration) *chatSession {
	s := o.sessions.byConv[convKey]
	if s == nil {
		return nil
	}
	if idle <= 0 || now.Sub(s.lastActivity) >= idle {
		o.endLocked(convKey, sessionExitIdle)
		return nil
	}
	return s
}

// ensureLocked returns the conversation's session, starting one at now if
// there is none. The caller holds o.sessions.mu.
func (o *Orchestrator) ensureLocked(convKey string, now time.Time) *chatSession {
	s := o.sessions.byConv[convKey]
	if s == nil {
		if o.sessions.byConv == nil {
			o.sessions.byConv = map[string]*chatSession{}
		}
		s = &chatSession{participants: map[string]bool{}, lastActivity: now}
		o.sessions.byConv[convKey] = s
	}
	if now.After(s.lastActivity) {
		s.lastActivity = now
	}
	return s
}

// endLocked ends the conversation's session, counting reason unless it is
// empty. The caller holds o.sessions.mu.
func (o *Orchestrator) endLocked(convKey, reason string) {
	if _, ok := o.sessions.byConv[convKey]; !ok {
		return
	}
	delete(o.sessions.byConv, convKey)
	if reason != "" {
		o.metrics.ContinuationExits.WithLabelValues(reason).Inc()
	}
}

// inSession reports whether user takes part in the conversation's session,
// which lasts idle after its last activity.
func (o *Orchestrator) inSession(convKey, user string, now time.Time, idle time.Duration) bool {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.liveLocked(convKey, now, idle)
	return s != nil && s.participants[user]
}

// sessionActive reports whether the conversation has a session that lasts
// idle after its last activity.
func (o *Orchestrator) sessionActive(convKey string, now time.Time, idle time.Duration) bool {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	return o.liveLocked(convKey, now, idle) != nil
}

// directReplied records that the bot answered user addressing it directly:
// user takes part in the session, which starts if needed, and the counts of
// unprompted replies and skips start over.
func (o *Orchestrator) directReplied(convKey, user string, now time.Time) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.ensureLocked(convKey, now)
	s.participants[user] = true
	s.unprompted, s.skips = 0, 0
}

// startJoinSession records that the bot joined the group's conversation with
// users: they take part in the session, and the bot's message counts as an
// unprompted reply.
func (o *Orchestrator) startJoinSession(convKey string, users []string, now time.Time) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.ensureLocked(convKey, now)
	for _, u := range users {
		s.participants[u] = true
	}
	s.unprompted, s.skips = 1, 0
}

// touchSession records a participant's activity in the conversation's
// session.
func (o *Orchestrator) touchSession(convKey string, now time.Time) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	if s := o.sessions.byConv[convKey]; s != nil && now.After(s.lastActivity) {
		s.lastActivity = now
	}
}

// turnReplied records that the bot answered a turn nobody addressed to it. The
// session ends, and turnReplied reports true, once the bot gave maxUnprompted
// such replies since it was last addressed directly.
func (o *Orchestrator) turnReplied(convKey string, now time.Time, maxUnprompted int) (ended bool) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.sessions.byConv[convKey]
	if s == nil {
		return false
	}
	s.unprompted++
	s.skips = 0
	if now.After(s.lastActivity) {
		s.lastActivity = now
	}
	if s.unprompted >= maxUnprompted {
		o.endLocked(convKey, sessionExitLimit)
		return true
	}
	return false
}

// turnSkipped records that the bot chose not to answer a turn. The session
// ends, and turnSkipped reports true, after maxSkips such turns in a row.
func (o *Orchestrator) turnSkipped(convKey string, maxSkips int) (ended bool) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.sessions.byConv[convKey]
	if s == nil {
		return false
	}
	s.skips++
	if s.skips >= maxSkips {
		o.endLocked(convKey, sessionExitModel)
		return true
	}
	return false
}

// leaveSession takes user out of the conversation's session, counting reason;
// the session ends when its last participant leaves.
func (o *Orchestrator) leaveSession(convKey, user, reason string) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	s := o.sessions.byConv[convKey]
	if s == nil || !s.participants[user] {
		return
	}
	delete(s.participants, user)
	o.metrics.ContinuationExits.WithLabelValues(reason).Inc()
	if len(s.participants) == 0 {
		o.endLocked(convKey, "")
	}
}

// endSession ends the conversation's session, counting reason unless it is
// empty.
func (o *Orchestrator) endSession(convKey, reason string) {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	o.endLocked(convKey, reason)
}

// clearSessions ends every session without counting them.
func (o *Orchestrator) clearSessions() {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	o.sessions.byConv = nil
}

// lastBotSpoke returns when the bot last sent a message in the group, or the
// zero time.
func (o *Orchestrator) lastBotSpoke(convKey string) time.Time {
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	return o.sessions.botSpoke[convKey]
}

// noteBotSpoke records that the bot sent a message in the conversation at at;
// private chats are not recorded.
func (o *Orchestrator) noteBotSpoke(convKey string, at time.Time) {
	if convType(convKey) != conloomapi.ConvGroup {
		return
	}
	o.sessions.mu.Lock()
	defer o.sessions.mu.Unlock()
	if o.sessions.botSpoke == nil {
		o.sessions.botSpoke = map[string]time.Time{}
	}
	if at.After(o.sessions.botSpoke[convKey]) {
		o.sessions.botSpoke[convKey] = at
	}
}

// updateSession applies a finished group reply task to the conversation's
// session: a reply to the bot's addressee makes them a participant, a turn
// answered or skipped counts towards the session's limits, and a proactive
// reply starts a session with its user. Nothing changes while the
// conversation is paused or sessions are off (continuation window 0).
func (o *Orchestrator) updateSession(ctx context.Context, task store.TaskRecord, out outcome, sent bool) {
	key := task.ConversationKey
	if convType(key) != conloomapi.ConvGroup {
		return
	}
	now := o.now()
	if sent {
		o.noteBotSpoke(key, now)
	}
	pv, err := o.policies.Current(ctx, key)
	if err != nil {
		o.log.Error("load policy failed; session not updated", "task_id", task.ID, "error", err)
		return
	}
	r := pv.Policy.Reply
	window := r.ContinuationWindow.Duration
	if window <= 0 || pv.Policy.Concurrency.MaxInteractiveRequests == 0 {
		return
	}
	// A lapsed session ends as idle before this task's outcome applies.
	o.sessionActive(key, now, window)
	user := task.ActorPlatformUserID
	ended := false
	switch {
	case out.addressed:
		if sent && out.status == conloomapi.TaskSucceeded {
			o.directReplied(key, user, now)
		}
	case out.turn:
		if sent {
			ended = o.turnReplied(key, now, r.MaxUnpromptedReplies)
		} else if out.status == conloomapi.TaskSucceeded {
			ended = o.turnSkipped(key, r.MaxConsecutiveSkips)
		}
	case out.proactive:
		if sent {
			o.startJoinSession(key, []string{user}, now)
		}
	}
	if ended {
		o.contacts.set(memberKey(key, user), now)
	}
}
