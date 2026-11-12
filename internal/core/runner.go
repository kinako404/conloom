package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	admissionWait = 60 * time.Second
	leaseSlack    = 10 * time.Second
	cancelWait    = 5 * time.Second

	reviewResultBlocked = "blocked"
)

// outcome is the result of executing a reply task. An empty text means no
// action is sent.
type outcome struct {
	status   string
	code     string
	text     string
	meta     conloomapi.TaskMeta
	conv     store.ConversationRecord
	triggers []store.MessageRecord
}

// run is the scheduler's Runner: it executes one reply task and stores its
// result. A task interrupted by Close keeps its stored status for Recover.
func (o *Orchestrator) run(ctx context.Context, qt QueuedTask) {
	done := o.track(qt.TaskID)
	defer o.untrack(qt.TaskID, done)
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

	out := o.execute(ctx, task)
	if ctx.Err() != nil {
		if o.closing.Load() {
			return
		}
		out.status, out.code, out.text = conloomapi.TaskCancelled, conloomapi.CodeTaskCancelled, ""
	}
	var actions []store.OutboxRecord
	if out.text != "" {
		actions = append(actions, o.newAction(task, out.conv, out.triggers, out.text))
	}
	if err := o.complete(dbctx, task, out.status, out.meta, out.code, actions); err != nil {
		return
	}
	if out.status == conloomapi.TaskSucceeded && len(actions) > 0 {
		o.sched.MarkReplied(task.ConversationKey, task.ActorPlatformUserID, o.now())
	}
	o.log.Info("task finished", "task_id", task.ID, "conversation", task.ConversationKey, "status", out.status,
		"error_code", out.code, "role", out.meta.SelectedModelRole, "model", out.meta.SelectedModel,
		"route_reason", out.meta.RouteReason, "review", out.meta.ReviewResult, "trace_id", out.meta.TraceID,
		"duration_ms", time.Since(start).Milliseconds(), obs.MessageAttr(o.cfg.Observability, out.text))
}

func (o *Orchestrator) execute(ctx context.Context, task store.TaskRecord) outcome {
	out := outcome{meta: conloomapi.TaskMeta{TraceID: ids.New("trace")}}
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
	bot, ok := o.cfg.Bot(task.BotInstanceID)
	if !ok {
		return fail(conloomapi.CodeInternal, CopyUnavailable, fmt.Errorf("unknown bot %q", task.BotInstanceID))
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	pol := pv.Policy
	history, err := o.history(ctx, task.ConversationKey, pol.Context.MaxRecentMessages, triggers)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	current := slices.Clone(triggers)
	for i := range current {
		res := o.reviewer.Input(current[i].Text, pol.Review)
		o.metrics.Review.WithLabelValues("input", res.Verdict).Inc()
		current[i].Text = res.Text
	}
	system, _, err := o.personas.SystemPrompt(ctx, task.ConversationKey, bot.PersonaTemplate, bot.Name)
	if err != nil {
		return fail(conloomapi.CodeInternal, CopyUnavailable, err)
	}
	in := contextbuilder.Input{
		SystemPrompt:     system,
		ConversationType: conv.Type,
		History:          history,
		Current:          current,
		ReplyTarget:      o.replyTarget(ctx, task.BotInstanceID, triggers),
		MaxRecent:        pol.Context.MaxRecentMessages,
		TokenFactor:      o.tokenFactor,
		Location:         o.loc,
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
	resp, err := o.call(ctx, task, d, in)
	if err != nil && ctx.Err() == nil {
		o.log.Warn("model call failed; re-routing", "task_id", task.ID, "role", d.Profile.Role, "error", err)
		req.Exclude = []router.Role{d.Profile.Role}
		d2, rerr := o.router.Route(req)
		if rerr != nil {
			return fail(conloomapi.CodeModelUnavailable, CopyUnavailable, rerr)
		}
		d = d2
		out.meta.SelectedModelRole, out.meta.RouteReason = string(d.Profile.Role), d.Reason
		resp, err = o.call(ctx, task, d, in)
	}
	if ctx.Err() != nil {
		return out
	}
	if err != nil {
		return fail(conloomapi.CodeModelUnavailable, CopyUnavailable, err)
	}
	out.meta.SelectedModel = resp.Model

	res := o.reviewer.Output(resp.Content, pol.Review)
	o.metrics.Review.WithLabelValues("output", res.Verdict).Inc()
	out.status = conloomapi.TaskSucceeded
	if res.Verdict == review.VerdictBlock {
		out.meta.ReviewResult = reviewResultBlocked
		o.auditBlocked(task, res.Reasons)
		return out
	}
	out.meta.ReviewResult = res.Verdict
	out.text = res.Text
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

// replyTarget returns the message the first replying trigger points at, if stored.
func (o *Orchestrator) replyTarget(ctx context.Context, botID string, triggers []store.MessageRecord) *store.MessageRecord {
	for _, t := range triggers {
		if t.ReplyTo == "" {
			continue
		}
		m, err := o.st.GetMessage(ctx, botID, t.ReplyTo)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				o.log.Warn("load reply target failed", "message_id", t.ReplyTo, "error", err)
			}
			return nil
		}
		return &m
	}
	return nil
}

// call builds the context for the routed profile, waits for admission and
// calls the model. Model errors (not admission errors) count against the
// role's circuit breaker.
func (o *Orchestrator) call(ctx context.Context, task store.TaskRecord, d router.Decision, in contextbuilder.Input) (openai.ChatResponse, error) {
	p := d.Profile
	role := string(p.Role)
	in.Budget = d.PromptBudget
	in.TokenFactor = p.Config.TokenEstimateFactor
	built := contextbuilder.Build(in)

	lease, err := o.acquire(ctx, task.ID, p, built.EstimatedTokens+d.MaxOutputTokens+p.Config.Context.SafetyMarginTokens)
	if err != nil {
		return openai.ChatResponse{}, err
	}
	defer lease.Release()

	start := time.Now()
	resp, err := p.Client.Chat(ctx, openai.ChatRequest{Messages: built.Messages, MaxTokens: d.MaxOutputTokens})
	if err != nil {
		if ctx.Err() == nil {
			o.router.ReportFailure(p.Role)
			o.setBreakerGauge(p.Role)
		}
		return openai.ChatResponse{}, fmt.Errorf("model %s: %w", role, err)
	}
	o.router.ReportSuccess(p.Role)
	o.setBreakerGauge(p.Role)

	latency := time.Since(start)
	o.metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(latency.Seconds())
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	o.metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	if built.EstimatedTokens > 0 && resp.PromptTokens > 0 {
		o.metrics.EstimateRatio.WithLabelValues(role).Observe(float64(resp.PromptTokens) / float64(built.EstimatedTokens))
	}
	o.log.Info("model call", "task_id", task.ID, "role", role, "model", resp.Model,
		"latency_ms", latency.Milliseconds(), "estimated_tokens", built.EstimatedTokens,
		"prompt_tokens", resp.PromptTokens, "completion_tokens", resp.CompletionTokens,
		"reasoning_tokens", resp.ReasoningTokens, "included_history", built.IncludedHistory,
		"dropped_history", built.DroppedHistory, "finish_reason", resp.FinishReason)
	return resp, nil
}

func (o *Orchestrator) acquire(ctx context.Context, taskID string, p *router.Profile, tokens int) (*admission.Lease, error) {
	rt := p.Config.Runtime
	wctx, cancel := context.WithTimeout(ctx, admissionWait)
	defer cancel()
	o.metrics.QueueDepth.Inc()
	start := time.Now()
	lease, err := o.adm.Acquire(wctx, admission.Request{
		ID:       taskID,
		Profile:  string(p.Role),
		Tokens:   tokens,
		Priority: admission.PriorityInteractive,
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
