package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/interest"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// joinScanLimit bounds the newest messages read when deciding whether to join
// a group's conversation (and to learn when the bot last spoke there after a
// restart).
const joinScanLimit = 500

// Reasons considerJoin gives for not joining, logged at debug level.
const (
	joinNoClosed      = "closed"
	joinNoError       = "error"
	joinNoDisabled    = "disabled"
	joinNoPaused      = "paused"
	joinNoSession     = "session"
	joinNoBusy        = "busy"
	joinNoCooldown    = "cooldown"
	joinNoBudget      = "budget"
	joinNoFewMessages = "few_messages"
	joinNoStale       = "stale"
	joinNoEligible    = "no_eligible"
	joinNoRelevance   = "irrelevant"
	joinNoTarget      = "no_target"
)

// lullTimers holds each group's lull timer: it fires once the group has been
// quiet for the policy's join lull.
type lullTimers struct {
	mu     sync.Mutex
	closed bool
	timers map[string]*lullTimer
	// running counts lull checks in progress, which Close waits for.
	running sync.WaitGroup
}

type lullTimer struct{ t *time.Timer }

// joinPlan is what a join task needs besides its stored record: the eligible
// members in the conversation it joins, with their display names.
type joinPlan struct {
	users []string
	names []string
}

// joinPlans holds the plans of join tasks until they run; a plan lives in
// memory only, so a join task recovered after a restart addresses its target
// alone.
type joinPlans struct {
	mu     sync.Mutex
	byTask map[string]joinPlan
}

func (p *joinPlans) put(taskID string, plan joinPlan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byTask == nil {
		p.byTask = map[string]joinPlan{}
	}
	p.byTask[taskID] = plan
}

// take removes and returns the plan of taskID.
func (p *joinPlans) take(taskID string) (joinPlan, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	plan, ok := p.byTask[taskID]
	delete(p.byTask, taskID)
	return plan, ok
}

func (p *joinPlans) clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byTask = nil
}

// armLull restarts the group's lull timer after a stored member message. A
// paused group or one with proactive replies off has no timer.
func (o *Orchestrator) armLull(ctx context.Context, convKey string) {
	pv, err := o.policies.Current(ctx, convKey)
	if err != nil {
		o.log.Error("load policy failed; lull timer not armed", "conversation", convKey, "error", err)
		return
	}
	pol := pv.Policy
	lull := pol.Proactive.JoinLull.Duration
	if !pol.Proactive.Enabled || pol.Concurrency.MaxInteractiveRequests == 0 || lull <= 0 {
		o.stopLull(convKey)
		return
	}
	o.lulls.mu.Lock()
	defer o.lulls.mu.Unlock()
	if o.lulls.closed {
		return
	}
	if old := o.lulls.timers[convKey]; old != nil {
		old.t.Stop()
	}
	if o.lulls.timers == nil {
		o.lulls.timers = map[string]*lullTimer{}
	}
	lt := &lullTimer{}
	lt.t = time.AfterFunc(lull, func() { o.lullFired(convKey, lt) })
	o.lulls.timers[convKey] = lt
}

// lullFired runs the join check of a lull timer that is still current.
func (o *Orchestrator) lullFired(convKey string, lt *lullTimer) {
	o.lulls.mu.Lock()
	if o.lulls.closed || o.lulls.timers[convKey] != lt {
		o.lulls.mu.Unlock()
		return
	}
	delete(o.lulls.timers, convKey)
	o.lulls.running.Add(1)
	o.lulls.mu.Unlock()
	defer o.lulls.running.Done()
	if taskID, reason := o.considerJoin(convKey); taskID == "" {
		o.log.Debug("not joining after a lull", "conversation", convKey, "reason", reason)
	}
}

// stopLull stops the group's lull timer.
func (o *Orchestrator) stopLull(convKey string) {
	o.lulls.mu.Lock()
	defer o.lulls.mu.Unlock()
	if lt := o.lulls.timers[convKey]; lt != nil {
		lt.t.Stop()
		delete(o.lulls.timers, convKey)
	}
}

// closeLulls stops every lull timer for good; waitLulls waits for the lull
// checks already running.
func (o *Orchestrator) closeLulls() {
	o.lulls.mu.Lock()
	defer o.lulls.mu.Unlock()
	o.lulls.closed = true
	for _, lt := range o.lulls.timers {
		lt.t.Stop()
	}
	o.lulls.timers = nil
}

func (o *Orchestrator) waitLulls() { o.lulls.running.Wait() }

// joinSpeaker is an eligible member who spoke in the window.
type joinSpeaker struct {
	user, name string
	member     store.MemberRecord
}

// considerJoin decides, after a lull, whether the bot joins the group's
// conversation, checking the cheap in-memory conditions before reading the
// database: proactive replies on and the group not paused; no session and no
// unfinished task; the bot last spoke at least the join cooldown ago; the
// group's join evaluations and proactive replies in the last hour below their
// limits; enough recent member messages, the newest not stale; at least one
// eligible member among the speakers who is not out of their own budget; and
// the window matching such a member's interests or containing a question. It
// then creates a join task replying to the newest message of an eligible
// speaker that addresses nobody else, records the evaluation and returns the
// task ID, or returns "" and the reason it did not join.
func (o *Orchestrator) considerJoin(convKey string) (taskID, reason string) {
	ctx := o.bgCtx
	if o.closing.Load() || ctx.Err() != nil {
		return "", joinNoClosed
	}
	pv, err := o.policies.Current(ctx, convKey)
	if err != nil {
		o.log.Error("load policy failed; not joining", "conversation", convKey, "error", err)
		return "", joinNoError
	}
	pol := pv.Policy
	p := pol.Proactive
	now := o.now()
	switch {
	case !p.Enabled:
		return "", joinNoDisabled
	case pol.Concurrency.MaxInteractiveRequests == 0:
		return "", joinNoPaused
	case o.sessionActive(convKey, now, pol.Reply.ContinuationWindow.Duration):
		return "", joinNoSession
	case len(o.sched.Active(convKey)) > 0:
		return "", joinNoBusy
	case now.Sub(o.lastBotSpoke(convKey)) < p.JoinCooldown.Duration:
		return "", joinNoCooldown
	}

	unlock := o.proactiveLocks.lock(convKey)
	defer unlock()
	fail := func(err error) (string, string) {
		if ctx.Err() == nil {
			o.log.Error("join check failed; not joining", "conversation", convKey, "error", err)
		}
		return "", joinNoError
	}
	ok, err := o.underLimit(ctx, convKey, "", store.ProactiveEvaluated, now.Add(-time.Hour), p.JoinEvaluationsPerHour)
	if err == nil && ok {
		ok, err = o.groupSendBudget(ctx, convKey, p, now)
	}
	if err != nil {
		return fail(err)
	}
	if !ok {
		return "", joinNoBudget
	}
	msgs, err := o.st.RecentMessages(ctx, convKey, joinScanLimit)
	if err != nil {
		return fail(err)
	}
	o.seedBotSpoke(convKey, msgs, len(msgs) >= joinScanLimit)
	if now.Sub(o.lastBotSpoke(convKey)) < p.JoinCooldown.Duration {
		return "", joinNoCooldown
	}
	window := joinWindow(msgs, now.Add(-p.JoinWindow.Duration))
	if len(window) < p.JoinMinMessages {
		return "", joinNoFewMessages
	}
	if now.Sub(window[len(window)-1].OccurredAt) >= o.staleAfter {
		return "", joinNoStale
	}
	speakers, err := o.joinSpeakers(ctx, convKey, window, p, now)
	if err != nil {
		return fail(err)
	}
	if len(speakers) == 0 {
		return "", joinNoEligible
	}
	if !joinRelevant(window, speakers) {
		return "", joinNoRelevance
	}
	target, ok := o.joinTarget(ctx, window, msgs, speakers)
	if !ok {
		return "", joinNoTarget
	}
	plan := joinPlan{}
	for _, sp := range speakers {
		plan.users, plan.names = append(plan.users, sp.user), append(plan.names, sp.name)
	}
	taskID, err = o.createJoinTask(ctx, convKey, target, plan, now)
	if err != nil {
		return fail(err)
	}
	if err := o.st.AppendProactiveLog(ctx, store.ProactiveLogEntry{
		ConversationKey: convKey, PlatformUserID: target.PlatformUserID, Kind: store.ProactiveEvaluated,
		TaskID: taskID, CreatedAt: now.UTC(),
	}); err != nil {
		// Without the record the evaluation budget cannot hold, so the
		// evaluation does not happen.
		o.log.Error("record join evaluation failed; cancelling the task", "task_id", taskID, "error", err)
		botID, _, _ := strings.Cut(convKey, ":")
		if _, cerr := o.cancel(ctx, botID, convKey, taskID); cerr != nil {
			o.log.Error("cancel unrecorded join task failed", "task_id", taskID, "error", cerr)
		}
		return "", joinNoError
	}
	o.metrics.Proactive.WithLabelValues(proactiveEvaluated).Inc()
	o.log.Info("join evaluation", "conversation", convKey, "task_id", taskID, "eligible_speakers", len(speakers),
		"window_messages", len(window))
	return taskID, ""
}

// joinWindow returns the member messages since since, oldest first.
func joinWindow(msgs []store.MessageRecord, since time.Time) []store.MessageRecord {
	var window []store.MessageRecord
	for _, m := range msgs {
		if !m.FromBot && !m.OccurredAt.Before(since) {
			window = append(window, m)
		}
	}
	return window
}

// joinSpeakers returns the speakers of window, in the order they first spoke,
// whom the bot may speak up to: eligible, level not off, and within their
// daily limit and user cooldown. Each is named by their newest display name.
func (o *Orchestrator) joinSpeakers(ctx context.Context, convKey string, window []store.MessageRecord, p conloomapi.ProactivePolicy, now time.Time) ([]joinSpeaker, error) {
	names := map[string]string{}
	var order []string
	for _, m := range window {
		if _, seen := names[m.PlatformUserID]; !seen {
			order = append(order, m.PlatformUserID)
		}
		names[m.PlatformUserID] = m.DisplayName
	}
	var speakers []joinSpeaker
	for _, user := range order {
		m, ok, err := o.proactiveTarget(ctx, convKey, user, p, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if ok, err = o.userSendBudget(ctx, convKey, user, m.ProactiveLevel, p, now); err != nil {
			return nil, err
		}
		if ok {
			name := names[user]
			if name == "" {
				name = user
			}
			speakers = append(speakers, joinSpeaker{user: user, name: name, member: m})
		}
	}
	return speakers, nil
}

// joinRelevant reports whether window asks a question or its text matches
// the interests of one of speakers at their level.
func joinRelevant(window []store.MessageRecord, speakers []joinSpeaker) bool {
	texts := make([]string, 0, len(window))
	for _, m := range window {
		if isQuestion(m.Text) {
			return true
		}
		texts = append(texts, m.Text)
	}
	text := strings.Join(texts, "\n")
	return slices.ContainsFunc(speakers, func(sp joinSpeaker) bool {
		_, ok := interest.Match(sp.member.Interests, text, sp.member.ProactiveLevel)
		return ok
	})
}

// joinTarget returns the newest message of window sent by one of speakers
// that mentions nobody (the bot already answered a mention of it) and replies
// to no other member. msgs are the messages window was taken from.
func (o *Orchestrator) joinTarget(ctx context.Context, window, msgs []store.MessageRecord, speakers []joinSpeaker) (store.MessageRecord, bool) {
	byID := make(map[string]store.MessageRecord, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	for i := len(window) - 1; i >= 0; i-- {
		m := window[i]
		if !slices.ContainsFunc(speakers, func(sp joinSpeaker) bool { return sp.user == m.PlatformUserID }) ||
			len(m.Mentions) > 0 || o.repliesToOther(ctx, m, byID) {
			continue
		}
		return m, true
	}
	return store.MessageRecord{}, false
}

func mentionsOthers(mentions []conloomapi.Mention) bool {
	return slices.ContainsFunc(mentions, func(m conloomapi.Mention) bool { return m.Type == "user" || m.Type == "all" })
}

// repliesToOther reports whether m replies to a message of another member or
// to a message that is not stored (or cannot be loaded).
func (o *Orchestrator) repliesToOther(ctx context.Context, m store.MessageRecord, byID map[string]store.MessageRecord) bool {
	if m.ReplyTo == "" {
		return false
	}
	to, ok := byID[m.ReplyTo]
	if !ok {
		var err error
		if to, err = o.st.GetMessage(ctx, m.BotInstanceID, m.ReplyTo); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				o.log.Warn("load reply target failed", "message_id", m.ReplyTo, "error", err)
			}
			return true
		}
	}
	return !to.FromBot && to.PlatformUserID != m.PlatformUserID
}

// errJoinMerged stops a join task that the scheduler would merge into a task
// of its target created meanwhile.
var errJoinMerged = errors.New("core: a task of the join target appeared")

// createJoinTask stores and schedules a join task replying to target with
// background admission priority. Its merge window closes at once, so no later
// message joins it.
func (o *Orchestrator) createJoinTask(ctx context.Context, convKey string, target store.MessageRecord, plan joinPlan, now time.Time) (string, error) {
	var taskID string
	closed := MergeOptions{Debounce: time.Nanosecond, MaxMergeWindow: time.Nanosecond}
	_, err := o.sched.AdmitWith(convKey, target.PlatformUserID, target.ID, AdmitNew, closed, func(ap AdmitPlan) error {
		if ap.NewTaskID == "" {
			return errJoinMerged
		}
		id := ap.NewTaskID
		task := store.TaskRecord{
			ID: id, BotInstanceID: target.BotInstanceID, ConversationKey: convKey, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: target.PlatformUserID, TriggerMessageIDs: []string{target.ID},
			Priority: int(admission.PriorityBackground), Meta: conloomapi.TaskMeta{Trigger: TriggerProactive},
			EventOccurredAt: target.OccurredAt, CreatedAt: now, UpdatedAt: now,
		}
		resp, err := json.Marshal(conloomapi.EventAccepted{
			Disposition: conloomapi.DispositionReply, TaskID: id, Status: conloomapi.TaskQueued,
		})
		if err != nil {
			return err
		}
		// The store keeps every task with the event that created it; a join
		// task gets an internal one.
		ev := store.EventRecord{
			BotInstanceID: target.BotInstanceID, IdempotencyKey: "conloom:join:" + id, EventID: "join_" + id,
			Response: resp, CreatedAt: now,
		}
		if err := o.ingest(ctx, store.Ingest{Event: ev, NewTask: &task}); err != nil {
			return err
		}
		o.joinPlans.put(id, plan)
		taskID = id
		return nil
	})
	return taskID, err
}

// joinDirective is the hint for a join task: the target's name, and the
// eligible members' names and merged top interest terms.
func (o *Orchestrator) joinDirective(ctx context.Context, task store.TaskRecord, triggers []store.MessageRecord, plan joinPlan) string {
	target := speaker(triggers)
	users, names := plan.users, plan.names
	if len(users) == 0 {
		users, names = []string{task.ActorPlatformUserID}, []string{target}
	}
	merged := map[string]float64{}
	for _, u := range users {
		m, err := o.st.GetMember(ctx, task.ConversationKey, u)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				o.log.Warn("load member for join directive failed", "task_id", task.ID, "error", err)
			}
			continue
		}
		for term, w := range m.Interests {
			merged[term] += w
		}
	}
	return CopyDirectiveJoin(target, names, interest.Top(merged, proactiveDirectiveTerms))
}

// questionWords are the words that make a message a question (design §4.2).
var questionWords = []string{"怎么", "为什么", "为啥", "有没有", "谁知道", "啥", "咋"}

// isQuestion reports whether text asks a question: it contains a question
// mark, ends with 吗, 呢 or 么 (ignoring trailing spaces and punctuation), or
// contains a question word.
func isQuestion(text string) bool {
	if strings.ContainsAny(text, "？?") {
		return true
	}
	trimmed := strings.TrimRight(strings.TrimSpace(text), " 　。.!！~～…")
	for _, end := range []string{"吗", "呢", "么"} {
		if strings.HasSuffix(trimmed, end) {
			return true
		}
	}
	return slices.ContainsFunc(questionWords, func(w string) bool { return strings.Contains(text, w) })
}
