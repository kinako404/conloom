package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/interest"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// joinScanLimit bounds the newest messages read when deciding whether to join
// a group's conversation (and to learn when the bot last spoke there after a
// restart).
const joinScanLimit = 500

// Reasons considerJoin gives for not joining, logged with "join skipped".
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
	o.considerJoin(convKey)
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
// the window matching such a member's interests or containing a question. An
// unanswered question waives the minimum number of messages: the newest
// message is a clear question (isClearQuestion) and would itself be the join
// target; a short window whose newest clear question is not the target gives
// joinNoTarget. It then creates
// a join task replying to the newest message of an eligible speaker that
// addresses nobody else, records the evaluation and returns the task ID, or
// returns "" and the reason it did not join, which it logs at info level
// without any message text or names.
func (o *Orchestrator) considerJoin(convKey string) (taskID, reason string) {
	windowSize, eligibleSpeakers := -1, -1
	defer func() {
		if taskID != "" {
			return
		}
		attrs := []any{"conversation", convKey, "reason", reason}
		if windowSize >= 0 {
			attrs = append(attrs, "window_messages", windowSize)
		}
		if eligibleSpeakers >= 0 {
			attrs = append(attrs, "eligible_speakers", eligibleSpeakers)
		}
		o.log.Info("join skipped", attrs...)
	}()
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
	window := joinWindow(msgs, now.Add(-p.JoinWindow.Duration), pol.Reply.CommandPrefix)
	windowSize = len(window)
	// A short window can only be joined for an unanswered clear question.
	short := len(window) < p.JoinMinMessages
	if len(window) == 0 || short && !isClearQuestion(window[len(window)-1].Text) {
		return "", joinNoFewMessages
	}
	newest := window[len(window)-1]
	if now.Sub(newest.OccurredAt) >= o.staleAfter {
		return "", joinNoStale
	}
	speakers, err := o.joinSpeakers(ctx, convKey, window, p, now)
	if err != nil {
		return fail(err)
	}
	eligibleSpeakers = len(speakers)
	var target store.MessageRecord
	if short {
		if target, ok = o.joinTarget(ctx, window, msgs, speakers); !ok || target.ID != newest.ID {
			return "", joinNoTarget
		}
	} else {
		if len(speakers) == 0 {
			return "", joinNoEligible
		}
		if !joinRelevant(window, speakers) {
			return "", joinNoRelevance
		}
		if target, ok = o.joinTarget(ctx, window, msgs, speakers); !ok {
			return "", joinNoTarget
		}
	}
	plan := joinPlan{}
	for _, sp := range speakers {
		plan.users, plan.names = append(plan.users, sp.user), append(plan.names, sp.name)
	}
	taskID, err = o.createJoinTask(ctx, convKey, target, plan, now)
	if reason, ok := joinNotCreated(err); ok {
		return "", reason
	}
	if err != nil {
		return fail(err)
	}
	o.metrics.Proactive.WithLabelValues(proactiveEvaluated).Inc()
	o.log.Info("join evaluation", "conversation", convKey, "task_id", taskID, "eligible_speakers", len(speakers),
		"window_messages", len(window))
	return taskID, ""
}

// joinWindow returns the member messages since since that are not commands
// (with commandPrefix), oldest first.
func joinWindow(msgs []store.MessageRecord, since time.Time, commandPrefix string) []store.MessageRecord {
	var window []store.MessageRecord
	for _, m := range msgs {
		if _, _, cmd := ParseCommand(m.Text, commandPrefix); !cmd && !m.FromBot && !m.OccurredAt.Before(since) {
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
			speakers = append(speakers, joinSpeaker{user: user, name: directiveName(names[user], user), member: m})
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
// that mentions nobody and replies to nobody but its sender (the bot already
// answered mentions of it and replies to it). msgs are the messages window was
// taken from.
func (o *Orchestrator) joinTarget(ctx context.Context, window, msgs []store.MessageRecord, speakers []joinSpeaker) (store.MessageRecord, bool) {
	byID := make(map[string]store.MessageRecord, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}
	for i := len(window) - 1; i >= 0; i-- {
		m := window[i]
		if !slices.ContainsFunc(speakers, func(sp joinSpeaker) bool { return sp.user == m.PlatformUserID }) ||
			len(m.Mentions) > 0 || o.repliesToSomeoneElse(ctx, m, byID) {
			continue
		}
		return m, true
	}
	return store.MessageRecord{}, false
}

func mentionsOthers(mentions []conloomapi.Mention) bool {
	return slices.ContainsFunc(mentions, func(m conloomapi.Mention) bool { return m.Type == "user" || m.Type == "all" })
}

// repliesToSomeoneElse reports whether m replies to a message of the bot or of
// another member, or to a message that is not stored in m's conversation (or
// cannot be loaded).
func (o *Orchestrator) repliesToSomeoneElse(ctx context.Context, m store.MessageRecord, byID map[string]store.MessageRecord) bool {
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
	return to.ConversationKey != m.ConversationKey || to.FromBot || to.PlatformUserID != m.PlatformUserID
}

// errJoinMerged stops a join task that the scheduler would merge into a task
// of its target created meanwhile.
var errJoinMerged = errors.New("core: a task of the join target appeared")

// createJoinTask records a join evaluation for target and stores and
// schedules a join task replying to it with background admission priority.
// The evaluation is recorded first, so no model call escapes the hourly
// budget (a task that then fails to be stored leaves an extra record, which
// only tightens the budget). The merge window closes at once, so no later
// message joins the task.
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
		if err := o.st.AppendProactiveLog(ctx, store.ProactiveLogEntry{
			ConversationKey: convKey, PlatformUserID: target.PlatformUserID, Kind: store.ProactiveEvaluated,
			TaskID: id, CreatedAt: now.UTC(),
		}); err != nil {
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

// joinDirective is the hint for a join task: the target's name and proactive
// level, and the eligible members' names and merged top interest terms. The
// target is one of the eligible members, or the only one without a plan; a
// target whose member record cannot be loaded gets the low wording.
func (o *Orchestrator) joinDirective(ctx context.Context, task store.TaskRecord, triggers []store.MessageRecord, plan joinPlan) string {
	target := speaker(triggers)
	users, names := plan.users, plan.names
	if len(users) == 0 {
		users, names = []string{task.ActorPlatformUserID}, []string{target}
	}
	level := conloomapi.ProactiveLow
	merged := map[string]float64{}
	for _, u := range users {
		m, err := o.st.GetMember(ctx, task.ConversationKey, u)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				o.log.Warn("load member for join directive failed", "task_id", task.ID, "error", err)
			}
			continue
		}
		if u == task.ActorPlatformUserID {
			level = m.ProactiveLevel
		}
		for term, w := range m.Interests {
			merged[term] += w
		}
	}
	return CopyDirectiveJoin(level, target, names, interest.Top(merged, proactiveDirectiveTerms))
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

// clearQuestionWords are the words that make a message a clear question.
var clearQuestionWords = []string{"怎么", "为什么", "为啥", "有没有", "谁知道", "是不是", "能不能", "会不会", "要不要", "哪个", "哪里", "多少"}

// isClearQuestion is a stricter isQuestion for the one-message join waiver:
// text contains a question mark, ends with 吗 (ignoring trailing spaces,
// punctuation and tildes), or contains a clear question word. Endings such as
// 呢 and 么 and words such as 啥 and 咋 are too often not questions.
func isClearQuestion(text string) bool {
	if strings.ContainsAny(text, "？?") {
		return true
	}
	trimmed := strings.TrimRightFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || r == '~' || r == '～'
	})
	return strings.HasSuffix(trimmed, "吗") ||
		slices.ContainsFunc(clearQuestionWords, func(w string) bool { return strings.Contains(text, w) })
}

// joinNotCreated maps an error of createJoinTask that only means the group
// became busy meanwhile to the reason for not joining.
func joinNotCreated(err error) (string, bool) {
	if errors.Is(err, errJoinMerged) || errors.Is(err, ErrQueueFull) {
		return joinNoBusy, true
	}
	return "", false
}

// maxDirectiveName bounds a display name quoted in a directive.
const maxDirectiveName = 20

// directiveName makes a member's display name safe to quote in a directive:
// without brackets, line breaks and control characters, trimmed and at most
// maxDirectiveName characters; fallback (the platform user ID) when nothing
// is left.
func directiveName(name, fallback string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case strings.ContainsRune("【】[]", r), unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp):
			continue
		}
		b.WriteRune(r)
	}
	out := []rune(strings.TrimSpace(b.String()))
	if len(out) > maxDirectiveName {
		out = []rune(strings.TrimSpace(string(out[:maxDirectiveName])))
	}
	if len(out) == 0 {
		return fallback
	}
	return string(out)
}
