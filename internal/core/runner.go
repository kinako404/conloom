package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	admissionWait = 60 * time.Second
	leaseSlack    = 10 * time.Second
	cancelWait    = 5 * time.Second

	reviewResultBlocked = "blocked"
	reviewResultSkipped = "skipped"
)

// SkipMarker is the model's answer for "do not reply". An output containing
// it anywhere, or a variant of it (see isSkip), is dropped and never reaches
// the outbox.
const SkipMarker = "[[SKIP]]"

// errEchoOnly is a model output made only of the prompt's transcript format
// (see contextbuilder.StripTranscriptPrefix).
var errEchoOnly = fmt.Errorf("%w: only the transcript format", openai.ErrEmptyOutput)

// outcome is the result of executing a reply task. An empty text means no
// action is sent; skipped means the model answered SkipMarker (or, for a task
// not addressed to the bot, only echoed the transcript format, or wrote only
// part separators). proactive is set for a proactive task none of whose
// messages is addressed to the bot; turn for a continuation task none of whose
// messages is; addressed for a task with a message that mentions or replies to
// the bot. turnSession is the session a turn was reserved in before calling
// the model.
type outcome struct {
	status      string
	code        string
	text        string
	skipped     bool
	proactive   bool
	turn        bool
	addressed   bool
	turnSession *chatSession
	// join is the plan of a join task.
	join     joinPlan
	meta     conloomapi.TaskMeta
	conv     store.ConversationRecord
	triggers []store.MessageRecord
	// maxParts is how many parts a model reply in text is split into (see
	// splitParts); zero for any other text, which is sent whole.
	maxParts int
	// skillCalls counts the task's tool calls by tool.
	skillCalls map[string]int
}

// run is the scheduler's Runner: it executes one reply task and stores its
// result. A task owned by another party (e.g. cancelled while being
// dispatched) is left to that party. A task interrupted by Close, and not
// cancelled by a user, keeps its stored status for Recover.
func (o *Orchestrator) run(ctx context.Context, qt QueuedTask) {
	ow := o.claim(qt.TaskID, true)
	if ow == nil {
		o.log.Debug("task owned elsewhere; not running it", "task_id", qt.TaskID)
		return
	}
	defer o.release(qt.TaskID, ow)
	start := time.Now()
	dbctx := context.WithoutCancel(ctx)
	botID, _, _ := strings.Cut(qt.ConversationKey, ":")
	task, err := o.st.GetTask(dbctx, botID, qt.TaskID)
	if err != nil {
		o.log.Error("load task failed", "task_id", qt.TaskID, "error", err)
		return
	}
	if taskFinished(task.Status) {
		return
	}
	if err := o.st.SetTaskStatus(dbctx, task.ID, conloomapi.TaskRunning); err != nil {
		o.log.Error("mark task running failed", "task_id", task.ID, "error", err)
		return
	}
	task.TriggerMessageIDs = qt.TriggerMessageIDs
	ctx = usage.WithAttribution(ctx, usage.Attribution{BotInstanceID: task.BotInstanceID, ConversationKey: task.ConversationKey, TaskID: task.ID})

	plan, _ := o.joinPlans.take(task.ID)
	out := o.execute(ctx, task, plan)
	defer o.releaseTurn(out.turnSession)
	if ctx.Err() != nil {
		if o.closing.Load() && !o.cancelledByUser(ow) {
			return
		}
		out.status, out.code, out.text, out.skipped = conloomapi.TaskCancelled, conloomapi.CodeTaskCancelled, "", false
	}
	sent, err := o.finish(dbctx, task, &out)
	if err != nil {
		return
	}
	o.updateSession(dbctx, task, out, sent)
	o.noteInteraction(dbctx, task, out, sent)
	if out.proactive {
		switch {
		case sent:
			o.metrics.Proactive.WithLabelValues(proactiveSent).Inc()
		case out.skipped:
			o.metrics.Proactive.WithLabelValues(proactiveSkipped).Inc()
		}
	}
	attrs := []any{"task_id", task.ID, "conversation", task.ConversationKey, "status", out.status,
		"error_code", out.code, "role", out.meta.SelectedModelRole, "model", out.meta.SelectedModel,
		"route_reason", out.meta.RouteReason, "review", out.meta.ReviewResult, "trace_id", out.meta.TraceID,
		"duration_ms", time.Since(start).Milliseconds(), obs.MessageAttr(o.cfg.Observability, out.text)}
	if len(out.skillCalls) > 0 {
		attrs = append(attrs, "skill_calls", skillCallSummary(out.skillCalls))
	}
	o.log.Info("task finished", attrs...)
}

// finish stores the task's outcome and reports whether an action was stored.
// A proactive task sends only a successful reply, and only if, re-checked
// under the conversation's proactive lock, the user is still a target within
// the send budget; otherwise it ends as skipped. A sent proactive reply is
// recorded under the same lock.
func (o *Orchestrator) finish(ctx context.Context, task store.TaskRecord, out *outcome) (sent bool, err error) {
	// Error copy only answers messages addressed to the bot.
	if (out.proactive || out.turn) && out.status != conloomapi.TaskSucceeded {
		out.text = ""
	}
	if out.proactive && out.text != "" {
		unlock := o.proactiveLocks.lock(task.ConversationKey)
		defer unlock()
		ok, err := o.proactiveSendAllowed(ctx, task, out.join.guest)
		if err != nil {
			o.log.Error("proactive send check failed; not sending", "task_id", task.ID, "error", err)
		}
		if !ok {
			o.log.Info("proactive reply withheld: no longer allowed", "task_id", task.ID, "conversation", task.ConversationKey)
			out.text, out.skipped, out.meta.ReviewResult = "", true, reviewResultSkipped
		}
	}
	var actions []store.OutboxRecord
	if out.text != "" {
		actions = o.newActions(ctx, task, out.conv, out.triggers, splitParts(out.text, out.maxParts))
	}
	if err := o.complete(ctx, task, out.status, out.meta, out.code, actions); err != nil {
		return false, err
	}
	if out.proactive && len(actions) > 0 {
		if err := o.st.AppendProactiveLog(ctx, store.ProactiveLogEntry{
			ConversationKey: task.ConversationKey, PlatformUserID: task.ActorPlatformUserID, Kind: store.ProactiveSent,
			TaskID: task.ID, CreatedAt: o.now().UTC(),
		}); err != nil {
			o.log.Error("record proactive reply failed", "task_id", task.ID, "error", err)
		}
	}
	return len(actions) > 0, nil
}

func (o *Orchestrator) execute(ctx context.Context, task store.TaskRecord, plan joinPlan) outcome {
	out := outcome{
		meta:      conloomapi.TaskMeta{TraceID: ids.New("trace"), Trigger: task.Meta.Trigger},
		proactive: task.Meta.Trigger == TriggerProactive,
		join:      plan,
	}
	fail := func(code, text string, err error) outcome {
		if err != nil {
			o.log.Warn("task failed", "task_id", task.ID, "error_code", code, "trace_id", out.meta.TraceID, "error", err)
		}
		out.status, out.code, out.text = conloomapi.TaskFailed, code, text
		return out
	}

	conv, err := o.conversation(ctx, task.ConversationKey)
	if err != nil {
		return fail(conloomapi.CodeInternal, "", err)
	}
	out.conv = conv
	triggers, err := o.st.GetMessages(ctx, task.BotInstanceID, task.TriggerMessageIDs)
	if err != nil || len(triggers) == 0 {
		return fail(conloomapi.CodeInternal, "", fmt.Errorf("load trigger messages: %v (found %d)", err, len(triggers)))
	}
	out.triggers = triggers
	directive := o.directive(ctx, task, triggers, plan)
	switch task.Meta.Trigger {
	case conloomapi.TriggerMention, conloomapi.TriggerReply:
		out.addressed = true
	case TriggerContinuation:
		out.addressed = directive == ""
	}
	out.turn = task.Meta.Trigger == TriggerContinuation && !out.addressed
	if out.proactive {
		// The switch, the user's level or the budget may have changed while
		// the task was queued (or across a restart): check before any model call.
		ok, err := o.proactiveSendAllowed(ctx, task, out.join.guest)
		if err != nil {
			o.log.Error("proactive check failed; not evaluating", "task_id", task.ID, "error", err)
		}
		if !ok {
			o.log.Info("proactive evaluation dropped: no longer allowed", "task_id", task.ID, "conversation", task.ConversationKey)
			out.status, out.skipped, out.meta.ReviewResult = conloomapi.TaskSucceeded, true, reviewResultSkipped
			return out
		}
	}
	bot, ok := o.cfg.Bot(task.BotInstanceID)
	if !ok {
		return fail(conloomapi.CodeInternal, CopyUnavailable, fmt.Errorf("unknown bot %q", task.BotInstanceID))
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	pol := pv.Policy
	if out.turn {
		// The sender may have left, or the session ended or reached its limit,
		// while the turn was queued: such a turn is dropped without a model call.
		out.turnSession = o.reserveTurn(task.ConversationKey, task.ActorPlatformUserID, o.now(),
			pol.Reply.ContinuationWindow.Duration, pol.Reply.MaxUnpromptedReplies)
		if out.turnSession == nil {
			o.log.Info("turn dropped: sender not in a conversation with room for a reply", "task_id", task.ID, "conversation", task.ConversationKey)
			out.status, out.skipped, out.meta.ReviewResult = conloomapi.TaskSucceeded, true, reviewResultSkipped
			return out
		}
	}
	history, err := o.history(ctx, task.ConversationKey, pol.Context.MaxRecentMessages, triggers)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	var speakerRows map[string]store.MemberMemory
	if conv.Type == conloomapi.ConvPrivate {
		speakerRows = o.privateSpeakerRow(ctx, task)
		history = sinceForgotten(history, speakerRows[task.ActorPlatformUserID].ForgottenAt)
	}
	current := slices.Clone(triggers)
	for i := range current {
		res := o.reviewer.Input(current[i].Text, pol.Review)
		o.metrics.Review.WithLabelValues("input", res.Verdict).Inc()
		current[i].Text = res.Text
	}
	system, err := o.personas.SystemPrompt(ctx, task.ConversationKey, bot.PersonaTemplate, bot.Name)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	targets := o.replyTargets(ctx, task, history, triggers)
	if at := speakerRows[task.ActorPlatformUserID].ForgottenAt; !at.IsZero() {
		maps.DeleteFunc(targets, func(_ string, m store.MessageRecord) bool { return m.OccurredAt.Before(at) })
	}
	names := o.mentionNames(ctx, task, history, triggers)
	in := contextbuilder.Input{
		SystemPrompt:     system.Text + "\n\n" + partsDirective(pol.Reply.MaxParts),
		ConversationType: conv.Type,
		History:          history,
		Current:          current,
		BotName:          system.BotName,
		ReplyTargets:     targets,
		MentionNames:     names,
		MaxRecent:        pol.Context.MaxRecentMessages,
		TokenFactor:      o.tokenFactor,
		Location:         o.loc,
		Directive:        directive,
		Memory:           o.injectMemory(ctx, task, pol, triggers, targets, names, speakerRows),
	}
	run := o.newSkillRun(task, bot, conv, triggers, &out)
	if run != nil {
		in.SystemPrompt += "\n\n" + run.set.Section
		in.ToolSchema = run.schema
		out.skillCalls = run.counts
	}
	req := router.Request{
		Sensitive:             conv.Type == conloomapi.ConvPrivate,
		AllowLong:             pol.Concurrency.AllowLongRequests,
		BaseTokens:            contextbuilder.BaseTokens(in),
		GroupMaxContextTokens: pol.Context.MaxContextTokens,
	}

	d, err := o.router.Route(req)
	if errors.Is(err, router.ErrContextTooLarge) {
		return fail(conloomapi.CodeContextTooLarge, CopyTooLong, err)
	}
	if err != nil {
		return fail(conloomapi.CodeModelUnavailable, CopyUnavailable, err)
	}
	out.meta.SelectedModelRole, out.meta.RouteReason = string(d.Profile.Role), d.Reason
	// A reply to the bot made only of the transcript format fails like an
	// empty model output; other tasks send nothing for it (see below).
	direct := !out.proactive && !out.turn
	// The configured name stays a known speaker: replies stored before a
	// character was set may have echoed it.
	speakers := append(in.SpeakerNames(), bot.Name)
	strip := func(s string) string { return contextbuilder.StripTranscriptPrefix(s, system.BotName, speakers...) }
	call := func(d router.Decision) (openai.ChatResponse, error) {
		resp, err := o.call(ctx, task, d, in, run)
		if err == nil && direct && !isSkip(resp.Content) && strip(resp.Content) == "" {
			o.log.Info("model output only echoed the transcript format", "task_id", task.ID, "role", d.Profile.Role, "output_len", len(resp.Content))
			return openai.ChatResponse{}, fmt.Errorf("model %s: %w", d.Profile.Role, errEchoOnly)
		}
		return resp, err
	}
	resp, err := call(d)
	if err != nil && ctx.Err() == nil {
		o.log.Warn("model call failed; re-routing", "task_id", task.ID, "role", d.Profile.Role, "error", err)
		req.Exclude = []router.Role{d.Profile.Role}
		d2, rerr := o.router.Route(req)
		if rerr != nil {
			return fail(conloomapi.CodeModelUnavailable, CopyUnavailable, rerr)
		}
		d = d2
		out.meta.SelectedModelRole, out.meta.RouteReason = string(d.Profile.Role), d.Reason
		resp, err = call(d)
	}
	if ctx.Err() != nil {
		return out
	}
	if err != nil {
		return fail(conloomapi.CodeModelUnavailable, CopyUnavailable, err)
	}
	out.meta.SelectedModel = resp.Model

	out.status = conloomapi.TaskSucceeded
	if isSkip(resp.Content) {
		out.meta.ReviewResult = reviewResultSkipped
		out.skipped = true
		return out
	}
	text := strip(resp.Content)
	if text == "" {
		o.log.Info("model output only echoed the transcript format; not sending", "task_id", task.ID, "output_len", len(resp.Content))
		out.meta.ReviewResult = reviewResultSkipped
		out.skipped = true
		return out
	}
	// The whole reply is reviewed before it is split into parts. Every mode
	// other than ContentOpen is protected.
	check := o.reviewer.OutputProtected
	if system.Mode == conloomapi.ContentOpen {
		check = o.reviewer.Output
	}
	res := check(text, pol.Review)
	o.metrics.Review.WithLabelValues("output", res.Verdict).Inc()
	if res.Verdict == review.VerdictBlock {
		out.meta.ReviewResult = reviewResultBlocked
		o.auditBlocked(task, res.Reasons)
		return out
	}
	if len(splitParts(res.Text, pol.Reply.MaxParts)) == 0 {
		o.log.Info("model output held only empty parts; not sending", "task_id", task.ID, "output_len", len(resp.Content))
		out.meta.ReviewResult = reviewResultSkipped
		out.skipped = true
		return out
	}
	out.meta.ReviewResult = res.Verdict
	out.text, out.maxParts = res.Text, pol.Reply.MaxParts
	return out
}

// history returns up to maxRecent messages before the newest, excluding the
// trigger messages.
func (o *Orchestrator) history(ctx context.Context, convKey string, maxRecent int, triggers []store.MessageRecord) ([]store.MessageRecord, error) {
	recent, err := o.st.RecentMessages(ctx, convKey, max(maxRecent, 0)+len(triggers))
	if err != nil {
		return nil, err
	}
	isTrigger := make(map[string]bool, len(triggers))
	for _, m := range triggers {
		isTrigger[m.ID] = true
	}
	history := make([]store.MessageRecord, 0, len(recent))
	for _, m := range recent {
		if !isTrigger[m.ID] {
			history = append(history, m)
		}
	}
	return history, nil
}

// privateSpeakerRow reads the memory row of a private task's sender, keyed by
// user ID (empty when there is none): its ForgottenAt cuts the context, and
// injectMemory reuses it. A read error is logged and leaves both alone.
func (o *Orchestrator) privateSpeakerRow(ctx context.Context, task store.TaskRecord) map[string]store.MemberMemory {
	rows, err := o.st.GetMemberMemories(ctx, task.ConversationKey, []string{task.ActorPlatformUserID})
	if err != nil {
		o.log.Warn("load private member memory failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return nil
	}
	return rows
}

// sinceForgotten drops the messages, from either side, that happened before
// at (none when at is zero): a private chat after /忘记我 starts afresh.
func sinceForgotten(history []store.MessageRecord, at time.Time) []store.MessageRecord {
	if at.IsZero() {
		return history
	}
	return slices.DeleteFunc(history, func(m store.MessageRecord) bool { return m.OccurredAt.Before(at) })
}

// replyTargets loads, in one query, the stored messages of the task's
// conversation that member history and triggers reply to, keyed by ID (the
// bot's own reply targets are never shown). Targets that are not stored, or
// belong to another conversation, are absent; on a load error none are
// returned.
func (o *Orchestrator) replyTargets(ctx context.Context, task store.TaskRecord, history, triggers []store.MessageRecord) map[string]store.MessageRecord {
	var ids []string
	for _, m := range slices.Concat(history, triggers) {
		if m.ReplyTo != "" && !m.FromBot && !slices.Contains(ids, m.ReplyTo) {
			ids = append(ids, m.ReplyTo)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	msgs, err := o.st.GetMessages(ctx, task.BotInstanceID, ids)
	if err != nil {
		o.log.Warn("load reply targets failed", "count", len(ids), "error", err)
		return nil
	}
	targets := make(map[string]store.MessageRecord, len(msgs))
	for _, m := range msgs {
		if m.ConversationKey == task.ConversationKey {
			targets[m.ID] = m
		}
	}
	return targets
}

// mentionNames maps the users that history and triggers mention to their
// newest display name in the task's conversation: from the loaded messages
// where possible, else from one store lookup. Users without one are absent;
// on a lookup error only the loaded names are returned.
func (o *Orchestrator) mentionNames(ctx context.Context, task store.TaskRecord, history, triggers []store.MessageRecord) map[string]string {
	loaded := slices.Concat(history, triggers)
	var ids []string
	for _, m := range loaded {
		for _, mention := range m.Mentions {
			if mention.Type == "user" && mention.ID != "" && !slices.Contains(ids, mention.ID) {
				ids = append(ids, mention.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	names := make(map[string]string, len(ids))
	for _, m := range loaded {
		if !m.FromBot && m.DisplayName != "" && slices.Contains(ids, m.ActorID) {
			names[m.ActorID] = m.DisplayName
		}
	}
	var missing []string
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return names
	}
	found, err := o.st.LatestDisplayNames(ctx, task.ConversationKey, missing)
	if err != nil {
		o.log.Warn("load mentioned names failed", "count", len(missing), "error", err)
		return names
	}
	maps.Copy(names, found)
	return names
}

// directive returns the hint appended to the current message of a
// continuation or join task (plan is the join task's). A continuation task
// with a trigger that mentions the bot or replies to it gets none; a join task
// always gets its hint, so it stays under the proactive checks.
func (o *Orchestrator) directive(ctx context.Context, task store.TaskRecord, triggers []store.MessageRecord, plan joinPlan) string {
	switch task.Meta.Trigger {
	case TriggerProactive:
		return o.joinDirective(ctx, task, triggers, plan)
	case TriggerContinuation:
	default:
		return ""
	}
	for _, t := range triggers {
		if slices.ContainsFunc(t.Mentions, func(m conloomapi.Mention) bool {
			return m.Type == "bot" && m.ID == task.BotInstanceID
		}) {
			return ""
		}
		if t.ReplyTo == "" {
			continue
		}
		m, err := o.st.GetMessage(ctx, task.BotInstanceID, t.ReplyTo)
		if err == nil && m.FromBot && m.ConversationKey == task.ConversationKey {
			return ""
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			o.log.Warn("load reply target failed", "message_id", t.ReplyTo, "error", err)
		}
	}
	return CopyDirectiveContinuation(speaker(triggers))
}

// speaker names the sender of the newest trigger message for a directive.
func speaker(triggers []store.MessageRecord) string {
	last := triggers[len(triggers)-1]
	return directiveName(last.DisplayName)
}

// partsDirective tells the model how many messages a reply may be split into
// (see splitParts); it is appended to the system prompt.
func partsDirective(maxParts int) string {
	if maxParts <= 1 {
		return "回复只发一条消息。"
	}
	return fmt.Sprintf("回复可以像聊天那样分成 1 到 %d 条短消息，条与条之间用单独一行的 %s 分隔；不需要分时就只发一条。", maxParts, partSeparator)
}

// call builds the context for the routed profile and calls the model. With
// skills (run not nil) the prompt keeps its usual budget unless the hard
// prompt limit leaves no room for one worst-case tool round on top of it;
// requests offer the tools and the calls the model makes are run, their
// results fed back, after any progress run made with an earlier model. The
// final request offers no tools: it comes after maxToolRounds rounds, after
// toolLoopTimeout, or when another round could overflow the prompt limit.
// Every request waits for its own admission. The response is the model's final
// answer; a tool call written as text, a tool round cut off by the output
// limit and tool calls in the final request are not answers (ErrEmptyOutput).
func (o *Orchestrator) call(ctx context.Context, task store.TaskRecord, d router.Decision, in contextbuilder.Input, run *skillRun) (openai.ChatResponse, error) {
	p := d.Profile
	in.Budget = d.PromptBudget
	in.TokenFactor = p.Config.TokenEstimateFactor
	worst := 0
	if run != nil {
		worst = worstRound(d.MaxOutputTokens, in.TokenFactor, run.maxResult)
		in.Budget = min(d.PromptBudget, d.MaxPromptTokens-worst)
	}
	built := contextbuilder.Build(in)
	req := openai.ChatRequest{Messages: built.Messages, MaxTokens: d.MaxOutputTokens}
	if run == nil {
		return o.chat(ctx, task, d, req, built, built.EstimatedTokens, nil)
	}
	if run.deadline.IsZero() {
		run.deadline = o.now().Add(toolLoopTimeout)
	}
	req.Messages = append(slices.Clip(req.Messages), run.msgs...)
	estimate := built.EstimatedTokens + estimateMessages(run.msgs, in.TokenFactor)
	for {
		final := o.lastRequest(run, estimate, worst, d)
		req.Tools = run.tools
		if final {
			req.Tools = nil
		}
		resp, err := o.chat(ctx, task, d, req, built, estimate, []any{"round", run.rounds, "final", final})
		if err != nil {
			return resp, err
		}
		if reason := notAnAnswer(resp, final); reason != "" {
			o.log.Warn("model output rejected", "task_id", task.ID, "role", p.Role, "round", run.rounds, "reason", reason)
			return openai.ChatResponse{}, fmt.Errorf("model %s: %w: %s", p.Role, openai.ErrEmptyOutput, reason)
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		if !o.now().Before(run.deadline) {
			continue // out of time: these calls are not run; ask for the final answer
		}
		added := o.toolRound(ctx, task, run, resp)
		run.msgs = append(run.msgs, added...)
		run.rounds++
		req.Messages = append(slices.Clip(req.Messages), added...)
		estimate += estimateMessages(added, in.TokenFactor)
	}
}

// chat waits for admission for a request estimated at estimate prompt tokens
// and calls the model. Model errors (not admission errors) count against the
// role's circuit breaker. Every model call, failed or not, is recorded (see
// recordCall). logAttrs are added to the model call log.
func (o *Orchestrator) chat(ctx context.Context, task store.TaskRecord, d router.Decision, req openai.ChatRequest,
	built contextbuilder.Output, estimate int, logAttrs []any) (openai.ChatResponse, error) {
	p := d.Profile
	role := string(p.Role)
	lease, err := o.acquire(ctx, task, p, estimate+d.MaxOutputTokens+p.Config.Context.SafetyMarginTokens)
	if err != nil {
		return openai.ChatResponse{}, err
	}
	defer lease.Release()

	start := time.Now()
	resp, err := p.Client.Chat(ctx, req)
	latency := time.Since(start)
	o.recordCall(task, p, start, latency, estimate, resp, err)
	if err != nil {
		if ctx.Err() == nil {
			o.router.ReportFailure(p.Role)
			o.setBreakerGauge(p.Role)
		}
		return openai.ChatResponse{}, fmt.Errorf("model %s: %w", role, err)
	}
	o.router.ReportSuccess(p.Role)
	o.setBreakerGauge(p.Role)

	o.metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(latency.Seconds())
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	if estimate > 0 && resp.PromptTokens > 0 {
		o.metrics.EstimateRatio.WithLabelValues(role).Observe(float64(resp.PromptTokens) / float64(estimate))
	}
	attrs := []any{"task_id", task.ID, "role", role, "model", resp.Model,
		"latency_ms", latency.Milliseconds(), "estimated_tokens", estimate,
		"prompt_tokens", resp.PromptTokens, "completion_tokens", resp.CompletionTokens,
		"reasoning_tokens", resp.ReasoningTokens, "included_history", built.IncludedHistory,
		"dropped_history", built.DroppedHistory, "finish_reason", resp.FinishReason}
	if logAttrs != nil {
		attrs = append(attrs, logAttrs...)
		attrs = append(attrs, "tool_calls", len(resp.ToolCalls))
	}
	o.log.Info("model call", attrs...)
	return resp, nil
}

// recordCall records a call to p's model for task, made at start, that
// took latency and was estimated at estimate prompt tokens. A failed call
// names the configured model.
func (o *Orchestrator) recordCall(task store.TaskRecord, p *router.Profile, start time.Time, latency time.Duration,
	estimate int, resp openai.ChatResponse, err error) {
	o.usage.Record(store.ModelCall{
		At: start.UTC(), BotInstanceID: task.BotInstanceID, ConversationKey: task.ConversationKey, TaskID: task.ID,
		Purpose: store.PurposeChat, Role: string(p.Role), Model: cmp.Or(resp.Model, p.Config.Model),
		PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens, ReasoningTokens: resp.ReasoningTokens,
		EstimatedTokens: estimate, LatencyMS: latency.Milliseconds(), Outcome: usage.Outcome(err), FinishReason: resp.FinishReason,
	})
}

// acquire waits for an admission lease at the task's stored priority.
func (o *Orchestrator) acquire(ctx context.Context, task store.TaskRecord, p *router.Profile, tokens int) (*admission.Lease, error) {
	rt := p.Config.Runtime
	wctx, cancel := context.WithTimeout(ctx, admissionWait)
	defer cancel()
	o.metrics.QueueDepth.Inc()
	start := time.Now()
	lease, err := o.adm.Acquire(wctx, admission.Request{
		ID:       task.ID,
		Profile:  string(p.Role),
		Tokens:   tokens,
		Priority: admission.Priority(task.Priority),
		TTL:      rt.Timeout.Duration*time.Duration(rt.MaxRetries+1) + leaseSlack,
	})
	o.metrics.QueueDepth.Dec()
	o.metrics.QueueWait.Observe(time.Since(start).Seconds())
	if err != nil {
		if ctx.Err() == nil {
			o.metrics.AdmissionRejects.WithLabelValues(rejectReason(err)).Inc()
		}
		return nil, fmt.Errorf("admission %s: %w", p.Role, err)
	}
	return lease, nil
}

func rejectReason(err error) string {
	switch {
	case errors.Is(err, admission.ErrTooLarge):
		return "too_large"
	case errors.Is(err, admission.ErrUnknownProfile):
		return "unknown_profile"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}

func (o *Orchestrator) setBreakerGauge(role router.Role) {
	v := 0.0
	if o.router.BreakerOpen(role) {
		v = 1
	}
	o.metrics.BreakerOpen.WithLabelValues(string(role)).Set(v)
}

// auditBlocked records that review blocked a reply. The detail holds only the
// task ID and reasons, never message text.
func (o *Orchestrator) auditBlocked(task store.TaskRecord, reasons []string) {
	detail, _ := json.Marshal(struct {
		TaskID  string   `json:"task_id"`
		Reasons []string `json:"reasons"`
	}{task.ID, reasons})
	err := o.st.InsertAudit(context.Background(), store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: task.BotInstanceID, Kind: "review.block", Actor: "system",
		Target: task.ConversationKey, Detail: string(detail), CreatedAt: o.now().UTC(),
	})
	if err != nil {
		o.log.Error("audit review block failed", "task_id", task.ID, "error", err)
	}
}
