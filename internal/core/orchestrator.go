package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrDuplicateEvent reports an idempotency key reused with a different request body.
var ErrDuplicateEvent = errors.New("core: idempotency key reused with a different request body")

const (
	defaultSessionIdle = 5 * time.Minute
	// minSessionIdle keeps scheduler sessions (and their continuation marks
	// and pacing) alive at least as long as the largest continuation window
	// and minimum reply interval policy validation allows.
	minSessionIdle   = time.Minute
	defaultActionTTL = 10 * time.Minute
	defaultTimezone  = "Asia/Shanghai"
)

// Notifier is told when a bot has new outbox actions.
type Notifier interface{ Notify(botID string) }

// Deps are the Orchestrator's collaborators. Store, Router, Admission,
// Personas, Policies, Reviewer and Config are required. A nil Notifier does
// nothing, nil Metrics are registered on a private registry, a nil Logger
// discards, a nil Location is Config.Server.Timezone (falling back to
// Asia/Shanghai, then UTC) and a nil Now is time.Now.
type Deps struct {
	Store     store.Store
	Router    *router.Router
	Admission *admission.Controller
	Personas  *persona.Service
	Policies  *policy.Service
	Reviewer  *review.Reviewer
	Notifier  Notifier
	Metrics   *obs.Metrics
	Logger    *slog.Logger
	Config    *config.Config
	Location  *time.Location
	Now       func() time.Time
}

// Orchestrator accepts events, runs commands and reply tasks, and recovers
// unfinished tasks after a restart.
type Orchestrator struct {
	st       store.Store
	router   *router.Router
	adm      *admission.Controller
	personas *persona.Service
	policies *policy.Service
	reviewer *review.Reviewer
	notifier Notifier
	metrics  *obs.Metrics
	log      *slog.Logger
	cfg      *config.Config
	loc      *time.Location
	now      func() time.Time

	sched       *Scheduler
	limiter     *RateLimiter
	tokenFactor float64
	actionTTL   time.Duration

	closing   atomic.Bool
	closeOnce sync.Once
	maintOnce sync.Once
	bgCtx     context.Context
	bgCancel  context.CancelFunc
	bgWG      sync.WaitGroup

	mu       sync.Mutex
	inflight map[string]chan struct{}
}

type nopNotifier struct{}

func (nopNotifier) Notify(string) {}

func New(d Deps) (*Orchestrator, error) {
	for _, r := range []struct {
		name    string
		missing bool
	}{
		{"Store", d.Store == nil}, {"Router", d.Router == nil}, {"Admission", d.Admission == nil},
		{"Personas", d.Personas == nil}, {"Policies", d.Policies == nil}, {"Reviewer", d.Reviewer == nil},
		{"Config", d.Config == nil},
	} {
		if r.missing {
			return nil, fmt.Errorf("core: Deps.%s is required", r.name)
		}
	}
	o := &Orchestrator{
		st: d.Store, router: d.Router, adm: d.Admission, personas: d.Personas, policies: d.Policies,
		reviewer: d.Reviewer, notifier: d.Notifier, metrics: d.Metrics, log: d.Logger, cfg: d.Config,
		loc: d.Location, now: d.Now,
		limiter:  NewRateLimiter(),
		inflight: map[string]chan struct{}{},
	}
	if o.notifier == nil {
		o.notifier = nopNotifier{}
	}
	if o.metrics == nil {
		o.metrics = obs.NewMetrics(prometheus.NewRegistry())
	}
	if o.log == nil {
		o.log = slog.New(slog.DiscardHandler)
	}
	if o.loc == nil {
		o.loc = displayLocation(o.cfg.Server.Timezone)
	}
	if o.now == nil {
		o.now = time.Now
	}
	o.tokenFactor = maxTokenFactor(o.cfg.ModelProfiles)
	o.actionTTL = o.cfg.Runtime.ActionTTL.Duration
	if o.actionTTL <= 0 {
		o.actionTTL = defaultActionTTL
	}
	idle := o.cfg.Runtime.SessionIdle.Duration
	if idle <= 0 {
		idle = defaultSessionIdle
	}
	o.sched = NewScheduler(o.run, o.sessionConfig, max(idle, minSessionIdle))
	o.bgCtx, o.bgCancel = context.WithCancel(context.Background())
	o.policies.OnChange(o.PolicyChanged)
	if lvl := o.cfg.DefaultGroupPolicy.Review.Level; !review.LevelImplemented(lvl) {
		o.log.Warn("model review is not implemented yet; this review level behaves like basic", "level", lvl)
	}
	return o, nil
}

func displayLocation(tz string) *time.Location {
	for _, name := range []string{tz, defaultTimezone} {
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// maxTokenFactor is the largest configured estimate factor, used to size the
// base prompt before a profile is chosen.
func maxTokenFactor(profiles map[string]config.ModelProfile) float64 {
	f := 0.0
	for _, p := range profiles {
		f = max(f, p.TokenEstimateFactor)
	}
	if f <= 0 {
		return 1
	}
	return f
}

// PolicyChanged makes the conversation's scheduler re-read its policy.
func (o *Orchestrator) PolicyChanged(convKey string) {
	o.sched.Kick(convKey)
}

// Close stops background maintenance and the scheduler. Running tasks are
// cancelled but keep their stored status, so Recover resumes them after a
// restart.
func (o *Orchestrator) Close() {
	o.closeOnce.Do(func() {
		o.closing.Store(true)
		o.bgCancel()
		o.bgWG.Wait()
		o.sched.Close()
	})
}

func (o *Orchestrator) sessionConfig(convKey string) SessionConfig {
	pol := o.cfg.DefaultGroupPolicy
	if pv, err := o.policies.Current(context.Background(), convKey); err != nil {
		o.log.Error("load policy failed; scheduling with defaults", "conversation", convKey, "error", err)
	} else {
		pol = pv.Policy
	}
	return SessionConfig{
		MaxConcurrent:    pol.Concurrency.MaxInteractiveRequests,
		MaxQueued:        pol.Concurrency.MaxQueuedRequests,
		Debounce:         pol.Reply.Debounce.Duration,
		MaxMergeWindow:   o.cfg.Runtime.MaxMergeWindow.Duration,
		MinReplyInterval: pol.Reply.MinReplyInterval.Duration,
	}
}

// submission is one incoming event being accepted.
type submission struct {
	botID, key, hash string
	body             []byte
	ev               conloomapi.Event
	bot              config.BotConfig
	convKey          string
	isAdmin          bool
	now              time.Time
}

// replayError reports that the idempotency key was stored concurrently.
type replayError struct{ rec store.EventRecord }

func (e *replayError) Error() string { return "core: event already stored" }

// Submit accepts an event. A repeated idempotency key with the same body
// returns the stored response with replayed set; with a different body it
// returns ErrDuplicateEvent. A platform message stored under another key
// returns store.ErrDuplicateMessage.
func (o *Orchestrator) Submit(ctx context.Context, botID, idemKey string, body []byte, ev conloomapi.Event) (resp conloomapi.EventAccepted, replayed bool, err error) {
	if o.closing.Load() {
		return conloomapi.EventAccepted{}, false, ErrSchedulerClosed
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	rec, err := o.st.GetEvent(ctx, botID, idemKey)
	if err == nil {
		return replay(rec, hash)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return conloomapi.EventAccepted{}, false, err
	}
	bot, ok := o.cfg.Bot(botID)
	if !ok {
		return conloomapi.EventAccepted{}, false, fmt.Errorf("core: unknown bot %q", botID)
	}
	s := &submission{
		botID: botID, key: idemKey, hash: hash, body: body, ev: ev, bot: bot,
		convKey: store.ConversationKey(botID, ev.Platform, ev.Conversation.Type, ev.Conversation.ID),
		isAdmin: slices.Contains(bot.Admins, ev.Actor.PlatformUserID),
		now:     o.now(),
	}
	resp, err = o.submit(ctx, s)
	if re := (*replayError)(nil); errors.As(err, &re) {
		return replay(re.rec, hash)
	}
	if err != nil {
		return conloomapi.EventAccepted{}, false, err
	}
	o.metrics.Events.WithLabelValues(resp.Disposition).Inc()
	level := slog.LevelInfo
	if resp.Disposition != conloomapi.DispositionReply && resp.SuppressedReason == "" {
		level = slog.LevelDebug
	}
	o.log.Log(ctx, level, "event accepted", "bot", botID, "conversation", s.convKey, "event_id", ev.EventID,
		"disposition", resp.Disposition, "suppressed_reason", resp.SuppressedReason, "task_id", resp.TaskID,
		"merged", resp.Merged, obs.MessageAttr(o.cfg.Observability, ev.Message.Text))
	return resp, false, nil
}

func replay(rec store.EventRecord, hash string) (conloomapi.EventAccepted, bool, error) {
	if rec.RequestHash != hash {
		return conloomapi.EventAccepted{}, false, ErrDuplicateEvent
	}
	var resp conloomapi.EventAccepted
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		return conloomapi.EventAccepted{}, false, fmt.Errorf("core: decode stored response: %w", err)
	}
	return resp, true, nil
}

func (o *Orchestrator) submit(ctx context.Context, s *submission) (conloomapi.EventAccepted, error) {
	d, pol, err := o.decide(ctx, s)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	switch {
	case d.Disposition == conloomapi.DispositionIgnore:
		resp := conloomapi.EventAccepted{EventID: s.ev.EventID, Disposition: d.Disposition, SuppressedReason: d.Reason}
		// Only the idempotency record is kept: messages of ignored events are
		// never stored, so neither is the raw body containing them.
		evRec, err := s.eventRecord(resp, nil)
		if err != nil {
			return conloomapi.EventAccepted{}, err
		}
		return resp, o.ingest(ctx, store.Ingest{Event: evRec})
	case d.Command != "":
		return o.submitCommand(ctx, s, d.Command)
	case d.Disposition == conloomapi.DispositionReply:
		if pol.Concurrency.MaxInteractiveRequests == 0 {
			return o.observeOnly(ctx, s, conloomapi.SuppressedPaused)
		}
		if !o.limiter.Allow(s.convKey+":"+s.ev.Actor.PlatformUserID, pol.Reply.UserRateLimitPerMinute, s.now) {
			return o.observeOnly(ctx, s, conloomapi.SuppressedRateLimited)
		}
		resp, err := o.admit(ctx, s, AdmitNew)
		if errors.Is(err, ErrQueueFull) {
			return o.observeOnly(ctx, s, conloomapi.SuppressedQueueFull)
		}
		return resp, err
	default:
		return o.admit(ctx, s, AdmitMergeOnly)
	}
}

func (o *Orchestrator) decide(ctx context.Context, s *submission) (Decision, conloomapi.GroupPolicy, error) {
	ev := s.ev
	in := DecideInput{
		BotInstanceID:    s.botID,
		SelfPlatformID:   s.bot.PlatformSelfID,
		ConversationType: ev.Conversation.Type,
		PlatformUserID:   ev.Actor.PlatformUserID,
		Text:             ev.Message.Text,
		PrivateChat:      s.bot.PrivateChat,
		Mentions:         ev.Message.Mentions,
		Enabled:          enabledIn(s.bot, ev.Conversation),
		IsAdmin:          s.isAdmin,
	}
	// Ignore decisions do not depend on policy, so no policy is loaded (and
	// cached) for conversations the bot does not serve.
	if d := Decide(in); d.Disposition == conloomapi.DispositionIgnore {
		return d, conloomapi.GroupPolicy{}, nil
	}
	pv, err := o.policies.Current(ctx, s.convKey)
	if err != nil {
		return Decision{}, conloomapi.GroupPolicy{}, err
	}
	pol := pv.Policy
	in.CommandPrefix = pol.Reply.CommandPrefix
	if ev.Message.ReplyTo != nil && *ev.Message.ReplyTo != "" {
		m, err := o.st.GetMessage(ctx, s.botID, *ev.Message.ReplyTo)
		switch {
		case err == nil:
			in.ReplyToIsBot = m.FromBot
		case !errors.Is(err, store.ErrNotFound):
			return Decision{}, conloomapi.GroupPolicy{}, err
		}
	}
	if w := pol.Reply.ContinuationWindow.Duration; w > 0 {
		in.InContinuation = o.sched.InContinuation(s.convKey, ev.Actor.PlatformUserID, w, s.now)
	}
	return Decide(in), pol, nil
}

func enabledIn(bot config.BotConfig, c conloomapi.Conversation) bool {
	return slices.ContainsFunc(bot.EnabledConversations, func(r conloomapi.ConversationRef) bool {
		return r.Type == c.Type && r.PlatformID == c.PlatformConversationID
	})
}

func (o *Orchestrator) submitCommand(ctx context.Context, s *submission, command string) (conloomapi.EventAccepted, error) {
	task := s.task(ids.New("task"), command)
	resp := conloomapi.EventAccepted{
		EventID: s.ev.EventID, Disposition: conloomapi.DispositionReply, TaskID: task.ID, Status: conloomapi.TaskQueued,
	}
	in, err := s.records(resp)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	in.NewTask = &task
	if err := o.ingest(ctx, in); err != nil {
		return conloomapi.EventAccepted{}, err
	}
	o.runCommand(ctx, task, *in.Conversation, []store.MessageRecord{*in.Message})
	return resp, nil
}

func (o *Orchestrator) observeOnly(ctx context.Context, s *submission, reason string) (conloomapi.EventAccepted, error) {
	resp := conloomapi.EventAccepted{EventID: s.ev.EventID, Disposition: conloomapi.DispositionObserve, SuppressedReason: reason}
	in, err := s.records(resp)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	return resp, o.ingest(ctx, in)
}

// admit lets the scheduler decide between a new task, a merge and plain
// observation, storing the event with the matching response in persist.
func (o *Orchestrator) admit(ctx context.Context, s *submission, mode AdmitMode) (conloomapi.EventAccepted, error) {
	var resp conloomapi.EventAccepted
	_, err := o.sched.Admit(s.convKey, s.ev.Actor.PlatformUserID, s.ev.Message.ID, mode, func(plan AdmitPlan) error {
		resp = conloomapi.EventAccepted{EventID: s.ev.EventID, Disposition: conloomapi.DispositionObserve}
		var task *store.TaskRecord
		switch {
		case plan.NewTaskID != "":
			resp.Disposition, resp.TaskID, resp.Status = conloomapi.DispositionReply, plan.NewTaskID, conloomapi.TaskQueued
			t := s.task(plan.NewTaskID, "")
			task = &t
		case plan.MergeIntoTaskID != "":
			resp.Disposition, resp.TaskID, resp.Status = conloomapi.DispositionReply, plan.MergeIntoTaskID, conloomapi.TaskQueued
			resp.Merged = true
		}
		in, err := s.records(resp)
		if err != nil {
			return err
		}
		in.NewTask = task
		in.MergeIntoTaskID = plan.MergeIntoTaskID
		return o.ingest(ctx, in)
	})
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	return resp, nil
}

// ingest stores in; an event already stored under the same key becomes a
// *replayError.
func (o *Orchestrator) ingest(ctx context.Context, in store.Ingest) error {
	existing, err := o.st.Ingest(ctx, in)
	if err != nil {
		return err
	}
	if existing != nil {
		return &replayError{rec: *existing}
	}
	return nil
}

func (s *submission) occurredAt() time.Time {
	if s.ev.OccurredAt.IsZero() {
		return s.now.UTC()
	}
	return s.ev.OccurredAt.UTC()
}

func (s *submission) eventRecord(resp conloomapi.EventAccepted, raw []byte) (store.EventRecord, error) {
	b, err := json.Marshal(resp)
	if err != nil {
		return store.EventRecord{}, fmt.Errorf("core: encode response: %w", err)
	}
	return store.EventRecord{
		BotInstanceID: s.botID, IdempotencyKey: s.key, RequestHash: s.hash, EventID: s.ev.EventID,
		Response: b, RawEvent: raw, CreatedAt: s.now,
	}, nil
}

// records returns the event, conversation, user and message records of s.
func (s *submission) records(resp conloomapi.EventAccepted) (store.Ingest, error) {
	evRec, err := s.eventRecord(resp, s.body)
	if err != nil {
		return store.Ingest{}, err
	}
	ev := s.ev
	replyTo := ""
	if ev.Message.ReplyTo != nil {
		replyTo = *ev.Message.ReplyTo
	}
	return store.Ingest{
		Event: evRec,
		Conversation: &store.ConversationRecord{
			BotInstanceID: s.botID, ID: ev.Conversation.ID, Key: s.convKey, Type: ev.Conversation.Type,
			Platform: ev.Platform, PlatformConversationID: ev.Conversation.PlatformConversationID,
		},
		User: &store.UserRecord{
			BotInstanceID: s.botID, Platform: ev.Platform, PlatformUserID: ev.Actor.PlatformUserID,
			DisplayName: ev.Actor.DisplayName,
		},
		Message: &store.MessageRecord{
			ID: ev.Message.ID, BotInstanceID: s.botID, ConversationKey: s.convKey, Platform: ev.Platform,
			PlatformMessageID: ev.Message.PlatformMessageID, ActorID: ev.Actor.ID,
			PlatformUserID: ev.Actor.PlatformUserID, DisplayName: ev.Actor.DisplayName, Text: ev.Message.Text,
			ReplyTo: replyTo, Mentions: ev.Message.Mentions, Attachments: ev.Message.Attachments,
			OccurredAt: s.occurredAt(), CreatedAt: s.now,
		},
	}, nil
}

func (s *submission) task(id, command string) store.TaskRecord {
	return store.TaskRecord{
		ID: id, BotInstanceID: s.botID, ConversationKey: s.convKey, Status: conloomapi.TaskQueued,
		ActorPlatformUserID: s.ev.Actor.PlatformUserID, Command: command,
		TriggerMessageIDs: []string{s.ev.Message.ID}, Priority: int(admission.PriorityInteractive),
		EventOccurredAt: s.occurredAt(), CreatedAt: s.now, UpdatedAt: s.now,
	}
}

// GetTask returns the task with its actions; Actions is never nil.
func (o *Orchestrator) GetTask(ctx context.Context, botID, taskID string) (conloomapi.Task, error) {
	rec, err := o.st.GetTask(ctx, botID, taskID)
	if err != nil {
		return conloomapi.Task{}, err
	}
	actions, err := o.st.ActionsForTask(ctx, taskID)
	if err != nil {
		return conloomapi.Task{}, err
	}
	if actions == nil {
		actions = []conloomapi.Action{}
	}
	t := conloomapi.Task{TaskID: rec.ID, Status: rec.Status, Actions: actions, Meta: rec.Meta}
	if rec.ErrorCode != "" {
		t.Error = taskError(rec.ErrorCode, rec.Meta.TraceID)
	}
	return t, nil
}

func taskError(code, traceID string) *conloomapi.ErrorBody {
	b := &conloomapi.ErrorBody{Code: code, TraceID: traceID}
	switch code {
	case conloomapi.CodeContextTooLarge:
		b.Message = "the input is too large for every available model"
	case conloomapi.CodeModelUnavailable:
		b.Message = "no model could produce a reply"
		b.Retryable = true
	case conloomapi.CodeTaskCancelled:
		b.Message = "the task was cancelled"
	case errorCodeStale:
		b.Message = "the task was too old to answer after a restart"
	default:
		b.Message = "the task failed"
	}
	return b
}

// CancelTask cancels a queued or running task and returns its state. For a
// running task it waits briefly for the runner to record the cancellation.
func (o *Orchestrator) CancelTask(ctx context.Context, botID, taskID string) (conloomapi.Task, error) {
	rec, err := o.st.GetTask(ctx, botID, taskID)
	if err != nil {
		return conloomapi.Task{}, err
	}
	if !taskFinished(rec.Status) {
		done, err := o.cancel(ctx, botID, rec.ConversationKey, taskID)
		if err != nil {
			return conloomapi.Task{}, err
		}
		if done != nil {
			t := time.NewTimer(cancelWait)
			select {
			case <-done:
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return conloomapi.Task{}, ctx.Err()
			}
			t.Stop()
		}
	}
	return o.GetTask(ctx, botID, taskID)
}

// cancel cancels taskID. If a runner is executing it, the runner records the
// cancellation and the returned channel closes when it exits; otherwise the
// stored task is marked cancelled here and the channel is nil.
func (o *Orchestrator) cancel(ctx context.Context, botID, convKey, taskID string) (<-chan struct{}, error) {
	o.sched.Cancel(convKey, taskID)
	if done := o.runnerDone(taskID); done != nil {
		return done, nil
	}
	rec, err := o.st.GetTask(ctx, botID, taskID)
	if err != nil || taskFinished(rec.Status) {
		return nil, err
	}
	return nil, o.complete(ctx, rec, conloomapi.TaskCancelled, rec.Meta, conloomapi.CodeTaskCancelled, nil)
}

func (o *Orchestrator) track(taskID string) chan struct{} {
	done := make(chan struct{})
	o.mu.Lock()
	o.inflight[taskID] = done
	o.mu.Unlock()
	return done
}

func (o *Orchestrator) untrack(taskID string, done chan struct{}) {
	o.mu.Lock()
	delete(o.inflight, taskID)
	o.mu.Unlock()
	close(done)
}

func (o *Orchestrator) runnerDone(taskID string) <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if done, ok := o.inflight[taskID]; ok {
		return done
	}
	return nil
}

func taskFinished(status string) bool {
	switch status {
	case conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled:
		return true
	}
	return false
}

// complete stores the task's final state and actions and notifies the bot's
// adapter when there are actions.
func (o *Orchestrator) complete(ctx context.Context, task store.TaskRecord, status string, meta conloomapi.TaskMeta, code string, actions []store.OutboxRecord) error {
	err := o.st.CompleteTask(ctx, store.TaskCompletion{
		TaskID: task.ID, Status: status, Meta: meta, ErrorCode: code, Actions: actions,
	})
	if err != nil {
		o.log.Error("complete task failed", "task_id", task.ID, "status", status, "error", err)
		return err
	}
	o.metrics.Tasks.WithLabelValues(status).Inc()
	if len(actions) > 0 {
		o.notifier.Notify(task.BotInstanceID)
	}
	return nil
}

// newAction builds a text message action for task. Group replies quote the
// newest trigger message; private replies quote nothing.
func (o *Orchestrator) newAction(task store.TaskRecord, conv store.ConversationRecord, triggers []store.MessageRecord, text string) store.OutboxRecord {
	now := o.now().UTC()
	a := conloomapi.Action{
		ActionID:               ids.New("act"),
		Type:                   conloomapi.ActionMessageSend,
		ConversationID:         conv.ID,
		ConversationType:       conv.Type,
		PlatformConversationID: conv.PlatformConversationID,
		Content:                conloomapi.ActionContent{Text: text},
		CreatedAt:              now,
	}
	if conv.Type == conloomapi.ConvGroup && len(triggers) > 0 {
		last := triggers[len(triggers)-1]
		a.ReplyToMessageID, a.ReplyToPlatformMessageID = last.ID, last.PlatformMessageID
	}
	return store.OutboxRecord{
		Action: a, BotInstanceID: task.BotInstanceID, ConversationKey: task.ConversationKey, TaskID: task.ID,
		Platform: conv.Platform, Status: store.OutboxPending, CreatedAt: now, ExpiresAt: now.Add(o.actionTTL),
	}
}

// conversation loads the stored conversation of a "bot:platform:type:id" key.
func (o *Orchestrator) conversation(ctx context.Context, convKey string) (store.ConversationRecord, error) {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) != 4 {
		return store.ConversationRecord{}, fmt.Errorf("core: malformed conversation key %q", convKey)
	}
	return o.st.GetConversation(ctx, parts[0], parts[3])
}
