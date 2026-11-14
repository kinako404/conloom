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
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrDuplicateEvent reports an idempotency key reused with a different request body.
var ErrDuplicateEvent = errors.New("core: idempotency key reused with a different request body")

const (
	defaultSessionIdle = 5 * time.Minute
	// minSessionIdle keeps scheduler sessions (and their pacing) alive at
	// least as long as the largest minimum reply interval policy validation
	// allows.
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
// Asia/Shanghai, then UTC), a nil Now is time.Now, a nil MemoryTrigger
// does nothing, nil Skills offer the model no tools and a nil Usage records
// no model calls.
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

	MemoryTrigger MemoryTrigger
	Skills        *skills.Registry
	Usage         *usage.Recorder
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

	memTrigger MemoryTrigger
	skills     *skills.Registry
	usage      *usage.Recorder

	sched       *Scheduler
	limiter     *RateLimiter
	tokenFactor float64
	actionTTL   time.Duration
	staleAfter  time.Duration
	// ftsRetry is the first wait after a failed keyword index backfill.
	ftsRetry time.Duration

	// cleanupBatch is the most rows one cleanup transaction deletes from a
	// table, cleanupMaxBatches the most such transactions per table per round.
	cleanupBatch, cleanupMaxBatches int

	closing   atomic.Bool
	closeOnce sync.Once
	recovered atomic.Bool
	bgCtx     context.Context
	bgCancel  context.CancelFunc
	bgWG      sync.WaitGroup

	mu     sync.Mutex
	owners map[string]*taskOwner

	// memberLocks serialise read-modify-writes of a member row;
	// proactiveLocks serialise a conversation's proactive budget checks with
	// the evaluation and sent records they depend on.
	memberLocks    keyedLocks
	proactiveLocks keyedLocks
	// sessions holds the groups' ongoing conversations with the bot, lulls
	// their lull timers and joinPlans the plans of queued join tasks.
	sessions  chatSessions
	lulls     lullTimers
	joinPlans joinPlans
}

// taskOwner is the one party in this process allowed to execute and complete
// a task: its runner, the command path, a cancellation or Recover. A party
// completes a task only while owning it, and re-reads the stored task after
// claiming it, so no task is completed twice or produces actions twice.
type taskOwner struct {
	runner bool
	// cancelled is set when a user cancels the runner's task, or a newer
	// message supersedes it.
	cancelled bool
	done      chan struct{}

	// commit serialises a runner storing its task's result with superseding
	// the task (see supersede): committed is set once the result is stored,
	// superseded once a new task replaced it.
	commit                sync.Mutex
	committed, superseded bool
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
		loc: d.Location, now: d.Now, memTrigger: d.MemoryTrigger, skills: d.Skills, usage: d.Usage,
		limiter:  NewRateLimiter(),
		owners:   map[string]*taskOwner{},
		ftsRetry: ftsBackfillRetry,

		cleanupBatch: defaultCleanupBatch, cleanupMaxBatches: defaultCleanupMaxBatches,
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
	o.staleAfter = o.cfg.Runtime.StaleTaskAfter.Duration
	if o.staleAfter <= 0 {
		o.staleAfter = defaultStaleTaskAfter
	}
	idle := o.cfg.Runtime.SessionIdle.Duration
	if idle <= 0 {
		idle = defaultSessionIdle
	}
	o.sched = NewScheduler(o.run, o.sessionConfig, max(idle, minSessionIdle))
	o.sched.OnHold(func(t QueuedTask, until time.Time) {
		if until.IsZero() {
			o.log.Info("turn deferred", "conversation", t.ConversationKey, "task_id", t.TaskID, "waiting_for", "running")
			return
		}
		o.log.Info("turn deferred", "conversation", t.ConversationKey, "task_id", t.TaskID, "waiting_for", "cooldown", "until", until.UTC())
	})
	o.sched.OnDrop(o.dropHeldTurn)
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

// PolicyChanged makes the conversation's scheduler re-read its policy. A
// policy that pauses the conversation (concurrency 0) also ends its session,
// stops its lull timer and cancels its active tasks; one that turns proactive
// replies off stops the lull timer.
func (o *Orchestrator) PolicyChanged(convKey string) {
	ctx := context.Background()
	if pv, err := o.policies.Current(ctx, convKey); err != nil {
		o.log.Error("load changed policy failed", "conversation", convKey, "error", err)
	} else if pol := pv.Policy; pol.Concurrency.MaxInteractiveRequests == 0 {
		o.endSession(convKey, "")
		o.stopLull(convKey)
		o.cancelActive(ctx, convKey)
	} else if !pol.Proactive.Enabled {
		o.stopLull(convKey)
	}
	o.sched.Kick(convKey)
}

// cancelActive silently cancels the conversation's queued and running tasks:
// they end cancelled without any action.
func (o *Orchestrator) cancelActive(ctx context.Context, convKey string) {
	botID, _, _ := strings.Cut(convKey, ":")
	active := o.sched.Active(convKey)
	for _, t := range active {
		if _, err := o.cancel(ctx, botID, convKey, t.TaskID); err != nil {
			o.log.Error("cancel task of paused conversation failed", "task_id", t.TaskID, "conversation", convKey, "error", err)
		}
	}
	if len(active) > 0 {
		o.log.Info("conversation paused; cancelled active tasks", "conversation", convKey, "count", len(active))
	}
}

// Close stops the lull timers, background maintenance and the scheduler and
// ends every session. Running tasks are cancelled but keep their stored
// status, so Recover resumes them after a restart; tasks a user already
// cancelled are still recorded as cancelled.
func (o *Orchestrator) Close() {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closing.Store(true)
		o.mu.Unlock()
		o.closeLulls()
		o.bgCancel()
		o.waitLulls()
		o.bgWG.Wait()
		o.sched.Close()
		o.clearSessions()
		o.joinPlans.clear()
	})
}

func (o *Orchestrator) sessionConfig(convKey string) SessionConfig {
	pol := o.cfg.DefaultGroupPolicy
	if pv, err := o.policies.Current(context.Background(), convKey); err != nil {
		o.log.Error("load policy failed; scheduling with defaults", "conversation", convKey, "error", err)
	} else {
		pol = pv.Policy
	}
	cfg := SessionConfig{
		MaxConcurrent:    pol.Concurrency.MaxInteractiveRequests,
		MaxQueued:        pol.Concurrency.MaxQueuedRequests,
		Debounce:         pol.Reply.Debounce.Duration,
		MaxMergeWindow:   o.cfg.Runtime.MaxMergeWindow.Duration,
		MinReplyInterval: pol.Reply.MinReplyInterval.Duration,
	}
	if spoke := o.lastBotSpoke(convKey); !spoke.IsZero() {
		// Turns wait for the turn cooldown after the bot last spoke. The
		// scheduler runs on the wall clock, so the cooldown's end is moved
		// there from o.now.
		cfg.CooldownUntil = time.Now().Add(spoke.Add(pol.Reply.TurnCooldown.Duration).Sub(o.now()))
	}
	return cfg
}

// dropHeldTurn ends a turn the scheduler dropped at its hold limit: it is
// stored as skipped without a model call and counts as no skip of the
// session.
func (o *Orchestrator) dropHeldTurn(qt QueuedTask) {
	ow := o.claim(qt.TaskID, false)
	if ow == nil {
		return
	}
	defer o.release(qt.TaskID, ow)
	ctx := context.WithoutCancel(o.bgCtx)
	botID, _, _ := strings.Cut(qt.ConversationKey, ":")
	task, err := o.st.GetTask(ctx, botID, qt.TaskID)
	if err != nil {
		o.log.Error("load dropped turn failed", "task_id", qt.TaskID, "error", err)
		return
	}
	if taskFinished(task.Status) {
		return
	}
	meta := task.Meta
	meta.ReviewResult = reviewResultSkipped
	if err := o.complete(ctx, task, conloomapi.TaskSucceeded, meta, "", nil); err == nil {
		o.log.Info("turn dropped", "conversation", qt.ConversationKey, "task_id", qt.TaskID, "reason", "held_too_long")
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
// returns store.ErrDuplicateMessage. A reply or command trigger that occurred
// StaleTaskAfter or longer ago is only observed, with reason stale.
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

// submit decides and stores the event; a group message engaging the bot then
// refreshes the sender's member record, and any stored group message restarts
// the group's lull timer.
func (o *Orchestrator) submit(ctx context.Context, s *submission) (conloomapi.EventAccepted, error) {
	d, pol, err := o.decide(ctx, s)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	resp, err := o.dispatch(ctx, s, d, pol)
	if err != nil {
		return resp, err
	}
	if engages(s, d) {
		o.recordEngagement(ctx, s, d, pol)
	}
	if s.ev.Conversation.Type == conloomapi.ConvGroup && d.Disposition != conloomapi.DispositionIgnore {
		o.armLull(ctx, s.convKey)
	}
	return resp, nil
}

func (o *Orchestrator) dispatch(ctx context.Context, s *submission, d Decision, pol conloomapi.GroupPolicy) (conloomapi.EventAccepted, error) {
	if d.Disposition == conloomapi.DispositionReply && s.now.Sub(s.occurredAt()) >= o.staleAfter {
		return o.observeOnly(ctx, s, conloomapi.SuppressedStale)
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
		if s.ev.Conversation.Type == conloomapi.ConvPrivate && endsTurn(d.Command) {
			o.sched.EndTurn(s.convKey, s.ev.Actor.PlatformUserID)
		}
		if !s.isAdmin {
			if reason := o.withheld(s, pol); reason != "" {
				return o.observeOnly(ctx, s, reason)
			}
		}
		return o.submitCommand(ctx, s, d.Command)
	case d.Disposition == conloomapi.DispositionReply && d.Trigger == TriggerContinuation:
		return o.submitTurn(ctx, s, pol)
	case d.Disposition == conloomapi.DispositionReply && s.ev.Conversation.Type == conloomapi.ConvPrivate:
		return o.submitPrivate(ctx, s, d.Trigger, pol)
	case d.Disposition == conloomapi.DispositionReply:
		if reason := o.withheld(s, pol); reason != "" {
			return o.observeOnly(ctx, s, reason)
		}
		addressed := d.Trigger == conloomapi.TriggerMention || d.Trigger == conloomapi.TriggerReply
		resp, err := o.admitWith(ctx, s, AdmitNew, d.Trigger, MergeOptions{Addressed: addressed}, nil)
		if errors.Is(err, ErrQueueFull) {
			return o.observeOnly(ctx, s, conloomapi.SuppressedQueueFull)
		}
		return resp, err
	case d.EndContinuation:
		// The message is addressed to someone else: it is only observed, never
		// merged into a pending task of the sender, who leaves the session.
		resp, err := o.observeOnly(ctx, s, "")
		if err == nil {
			o.leaveSession(s.convKey, s.ev.Actor.PlatformUserID, sessionExitRule)
		}
		return resp, err
	default:
		return o.admit(ctx, s, AdmitMergeOnly, "")
	}
}

// endsTurn reports whether a private command ends its sender's turn (see
// Scheduler.EndTurn): /stop, /pause and /忘记我 do, so the messages before
// them are never carried into a later turn; other commands leave the turn
// open.
func endsTurn(command string) bool {
	switch command {
	case "stop", "pause", "忘记我", "forgetme":
		return true
	}
	return false
}

// errRateLimited stops a new turn of a sender over the per-user rate limit.
var errRateLimited = errors.New("core: sender is rate limited")

// submitTurn admits a session participant's message into their turn: their
// pending task absorbs it, or else a continuation task starts whose merge
// window is the policy's turn gap, capped at its turn maximum wait. The turn
// starts no earlier than the turn cooldown after the bot last spoke, and only
// once the sender paused, absorbing their messages meanwhile; it waits at most
// the turn cooldown plus the turn maximum wait (see MergeOptions.Cooldown and
// dropHeldTurn). A turn with a clear question (isClearQuestion) skips the
// cooldown and is never dropped (see MergeOptions.IgnoreCooldown). Only a new turn spends one of the sender's rate-limit hits.
func (o *Orchestrator) submitTurn(ctx context.Context, s *submission, pol conloomapi.GroupPolicy) (conloomapi.EventAccepted, error) {
	if pol.Concurrency.MaxInteractiveRequests == 0 {
		return o.observeOnly(ctx, s, conloomapi.SuppressedPaused)
	}
	opts := MergeOptions{
		Debounce: pol.Reply.TurnGap.Duration, MaxMergeWindow: pol.Reply.TurnMaxWait.Duration,
		Cooldown: true, MaxHold: pol.Reply.TurnCooldown.Duration + pol.Reply.TurnMaxWait.Duration,
		IgnoreCooldown: isClearQuestion(s.ev.Message.Text),
	}
	resp, err := o.admitWith(ctx, s, AdmitNew, TriggerContinuation, opts, func() bool {
		return o.limiter.Allow(s.convKey+":"+s.ev.Actor.PlatformUserID, pol.Reply.UserRateLimitPerMinute, s.now)
	})
	switch {
	case errors.Is(err, errRateLimited):
		return o.observeOnly(ctx, s, conloomapi.SuppressedRateLimited)
	case errors.Is(err, ErrQueueFull):
		return o.observeOnly(ctx, s, conloomapi.SuppressedQueueFull)
	case err != nil:
		return conloomapi.EventAccepted{}, err
	}
	o.touchSession(s.convKey, s.now)
	return resp, nil
}

// submitPrivate admits a private message into its sender's turn: their
// not-yet-started task absorbs it, or else a new task starts once they paused
// for the policy's private wait, at most its private maximum wait after the
// task was created. A message arriving while the sender's reply is being
// generated, or after it was stored but before any of it was handed out,
// abandons that reply: the new task carries the replaced task's messages too
// (see MergeOptions.Interrupt and supersede). One arriving after part of the
// reply was handed out cancels the parts not handed out yet (see
// cancelUnsentParts). /stop, /pause and /忘记我 end the sender's turn (see
// endsTurn); other commands leave it open, so a message after /help still
// carries the messages before it. Only a new task, superseding or not, spends
// one of the sender's rate-limit hits; a sender over the limit is only
// observed, and their running task goes on.
func (o *Orchestrator) submitPrivate(ctx context.Context, s *submission, trigger string, pol conloomapi.GroupPolicy) (conloomapi.EventAccepted, error) {
	if pol.Concurrency.MaxInteractiveRequests == 0 {
		return o.observeOnly(ctx, s, conloomapi.SuppressedPaused)
	}
	opts := MergeOptions{
		Debounce: pol.Reply.PrivateWait.Duration, MaxMergeWindow: pol.Reply.PrivateMaxWait.Duration, Interrupt: true,
	}
	resp, err := o.admitWith(ctx, s, AdmitNew, trigger, opts, func() bool {
		return o.limiter.Allow(s.convKey+":"+s.ev.Actor.PlatformUserID, pol.Reply.UserRateLimitPerMinute, s.now)
	})
	switch {
	case errors.Is(err, errRateLimited):
		return o.observeOnly(ctx, s, conloomapi.SuppressedRateLimited)
	case errors.Is(err, ErrQueueFull):
		return o.observeOnly(ctx, s, conloomapi.SuppressedQueueFull)
	}
	return resp, err
}

// errSuperseded stops a runner whose task was superseded (see supersede).
var errSuperseded = errors.New("core: task superseded")

// supersede stores s's message with plan's new task, which replaces the task
// plan.Supersedes: the new task's trigger messages are the replaced task's
// followed by s's message. In the same transaction, a replaced task that has
// not stored its result is stored as cancelled, without any action; its
// runner stores nothing after that (see complete), and a runner not started
// yet finds the task finished. Otherwise all the actions of its reply are
// cancelled, and audited as outbox.superseded. If part of that reply was
// handed out, the task has no actions, it was cancelled, or another party is
// finishing it, supersede stores nothing and returns ErrSupersedeRefused, so
// a cancelled task's messages are never carried.
func (o *Orchestrator) supersede(ctx context.Context, s *submission, trigger string, plan AdmitPlan) (conloomapi.EventAccepted, error) {
	o.mu.Lock()
	ow, owned := o.owners[plan.Supersedes]
	if owned && (!ow.runner || ow.cancelled) {
		o.mu.Unlock()
		return conloomapi.EventAccepted{}, ErrSupersedeRefused
	}
	if !owned {
		ow = &taskOwner{done: make(chan struct{})}
		o.owners[plan.Supersedes] = ow
	}
	o.mu.Unlock()
	if !owned {
		defer o.release(plan.Supersedes, ow)
	}
	ow.commit.Lock()
	defer ow.commit.Unlock()
	old, err := o.st.GetTask(ctx, s.botID, plan.Supersedes)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	resp := conloomapi.EventAccepted{
		EventID: s.ev.EventID, Disposition: conloomapi.DispositionReply, TaskID: plan.NewTaskID, Status: conloomapi.TaskQueued,
	}
	in, err := s.records(resp)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	task := s.task(plan.NewTaskID, "", trigger)
	task.TriggerMessageIDs = append(slices.Clone(old.TriggerMessageIDs), s.ev.Message.ID)
	in.NewTask = &task
	if !ow.committed && !taskFinished(old.Status) {
		in.CancelTaskID = old.ID
		err := o.ingest(ctx, in)
		if err == nil {
			ow.superseded = true
			o.mu.Lock()
			ow.cancelled = true
			o.mu.Unlock()
			o.joinPlans.take(old.ID)
			o.metrics.Tasks.WithLabelValues(conloomapi.TaskCancelled).Inc()
			o.log.Info("task superseded", "conversation", s.convKey, "task_id", old.ID, "superseded_by", plan.NewTaskID)
			return resp, nil
		}
		if !errors.Is(err, store.ErrTaskFinished) {
			return conloomapi.EventAccepted{}, err
		}
		in.CancelTaskID = ""
	}
	pending, err := o.pendingActions(ctx, s.botID, old.ID)
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	if pending < 0 {
		return conloomapi.EventAccepted{}, ErrSupersedeRefused
	}
	in.AbandonReplyOf = old.ID
	err = o.ingest(ctx, in)
	if errors.Is(err, store.ErrReplyHandedOut) {
		return conloomapi.EventAccepted{}, ErrSupersedeRefused
	}
	if err != nil {
		return conloomapi.EventAccepted{}, err
	}
	o.log.Info("reply abandoned", "conversation", s.convKey, "task_id", old.ID, "superseded_by", plan.NewTaskID, "cancelled", pending)
	o.auditSuperseded(ctx, s, old.ID, plan.NewTaskID, pending)
	return resp, nil
}

// pendingActions counts the task's pending actions, or returns -1 if it has
// no actions.
func (o *Orchestrator) pendingActions(ctx context.Context, botID, taskID string) (int, error) {
	actions, err := o.st.ActionsForTask(ctx, taskID)
	if err != nil || len(actions) == 0 {
		return -1, err
	}
	n := 0
	for _, a := range actions {
		rec, err := o.st.GetOutbox(ctx, botID, a.ActionID)
		if err != nil {
			return 0, err
		}
		if rec.Status == store.OutboxPending {
			n++
		}
	}
	return n, nil
}

// cancelUnsentParts cancels the pending actions of the reply of taskID,
// which superseding by the new task newTaskID was refused, and audits them as
// outbox.superseded.
func (o *Orchestrator) cancelUnsentParts(ctx context.Context, s *submission, taskID, newTaskID string) {
	n, err := o.st.CancelPendingActions(ctx, taskID, o.now().UTC())
	if err != nil {
		o.log.Error("cancel unsent reply parts failed", "task_id", taskID, "error", err)
		return
	}
	if n > 0 {
		o.log.Info("unsent reply parts cancelled", "conversation", s.convKey, "task_id", taskID, "superseded_by", newTaskID, "cancelled", n)
		o.auditSuperseded(ctx, s, taskID, newTaskID, n)
	}
}

// auditSuperseded records that the given number of actions of taskID's
// reply were cancelled because the new task newTaskID superseded it.
func (o *Orchestrator) auditSuperseded(ctx context.Context, s *submission, taskID, newTaskID string, cancelled int) {
	detail, _ := json.Marshal(struct {
		TaskID       string `json:"task_id"`
		Cancelled    int    `json:"cancelled"`
		SupersededBy string `json:"superseded_by"`
	}{taskID, cancelled, newTaskID})
	err := o.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: s.botID, Kind: "outbox.superseded", Actor: "system",
		Target: s.convKey, Detail: string(detail), CreatedAt: o.now().UTC(),
	})
	if err != nil {
		o.log.Error("audit superseded reply failed", "task_id", taskID, "error", err)
	}
}

// withheld returns why a reply trigger is not answered: the conversation is
// paused or the sender exceeded the per-user rate limit. It returns "" and
// spends one of the sender's rate-limit hits otherwise.
func (o *Orchestrator) withheld(s *submission, pol conloomapi.GroupPolicy) string {
	if pol.Concurrency.MaxInteractiveRequests == 0 {
		return conloomapi.SuppressedPaused
	}
	if !o.limiter.Allow(s.convKey+":"+s.ev.Actor.PlatformUserID, pol.Reply.UserRateLimitPerMinute, s.now) {
		return conloomapi.SuppressedRateLimited
	}
	return ""
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
		PrivateListed:    privateListed(s.bot, ev.Actor.PlatformUserID),
		MentionsOthers:   mentionsOthers(ev.Message.Mentions),
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
		case err == nil && m.ConversationKey != s.convKey:
			// A target in another conversation is treated as not stored.
		case err == nil:
			in.ReplyToIsBot = m.FromBot
			in.ReplyToOther = !m.FromBot && m.PlatformUserID != ev.Actor.PlatformUserID
		case !errors.Is(err, store.ErrNotFound):
			return Decision{}, conloomapi.GroupPolicy{}, err
		}
	}
	if w := pol.Reply.ContinuationWindow.Duration; w > 0 {
		in.InContinuation = o.inSession(s.convKey, ev.Actor.PlatformUserID, s.now, w)
	}
	return Decide(in), pol, nil
}

func enabledIn(bot config.BotConfig, c conloomapi.Conversation) bool {
	return slices.ContainsFunc(bot.EnabledConversations, func(r conloomapi.ConversationRef) bool {
		return r.Type == c.Type && r.PlatformID == c.PlatformConversationID
	})
}

func privateListed(bot config.BotConfig, platformUserID string) bool {
	return slices.ContainsFunc(bot.EnabledConversations, func(r conloomapi.ConversationRef) bool {
		return r.Type == conloomapi.ConvPrivate && r.PlatformID == platformUserID
	})
}

func (o *Orchestrator) submitCommand(ctx context.Context, s *submission, command string) (conloomapi.EventAccepted, error) {
	task := s.task(ids.New("task"), command, conloomapi.TriggerCommand)
	ow := o.claim(task.ID, false)
	if ow == nil {
		return conloomapi.EventAccepted{}, fmt.Errorf("core: new task %s is already owned", task.ID)
	}
	defer o.release(task.ID, ow)
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
// observation, storing the event with the matching response in persist. A new
// task records trigger in its meta.
func (o *Orchestrator) admit(ctx context.Context, s *submission, mode AdmitMode, trigger string) (conloomapi.EventAccepted, error) {
	return o.admitWith(ctx, s, mode, trigger, MergeOptions{}, nil)
}

// admitWith is admit with the merge window opts for a new task. A non-nil
// allowNew is asked once before a new task is stored or a task superseded;
// if it refuses, nothing is stored and admitWith returns errRateLimited. With
// opts.Interrupt a new task may supersede another (see supersede); if that is
// refused, the other task's unsent reply parts are cancelled once the new
// task is stored (see cancelUnsentParts).
func (o *Orchestrator) admitWith(ctx context.Context, s *submission, mode AdmitMode, trigger string, opts MergeOptions, allowNew func() bool) (conloomapi.EventAccepted, error) {
	var resp conloomapi.EventAccepted
	var allowed bool
	var refused string
	_, err := o.sched.AdmitWith(s.convKey, s.ev.Actor.PlatformUserID, s.ev.Message.ID, mode, opts, func(plan AdmitPlan) error {
		if plan.NewTaskID != "" && allowNew != nil && !allowed {
			if !allowNew() {
				return errRateLimited
			}
			allowed = true
		}
		if plan.Supersedes != "" {
			var err error
			resp, err = o.supersede(ctx, s, trigger, plan)
			if errors.Is(err, ErrSupersedeRefused) {
				refused = plan.Supersedes
			}
			return err
		}
		resp = conloomapi.EventAccepted{EventID: s.ev.EventID, Disposition: conloomapi.DispositionObserve}
		var task *store.TaskRecord
		switch {
		case plan.NewTaskID != "":
			resp.Disposition, resp.TaskID, resp.Status = conloomapi.DispositionReply, plan.NewTaskID, conloomapi.TaskQueued
			t := s.task(plan.NewTaskID, "", trigger)
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
		if err := o.ingest(ctx, in); err != nil {
			return err
		}
		if task != nil && refused != "" {
			o.cancelUnsentParts(ctx, s, refused, task.ID)
		}
		return nil
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

// occurredAt is the event time; an unset or future time counts as now.
func (s *submission) occurredAt() time.Time {
	if s.ev.OccurredAt.IsZero() || s.ev.OccurredAt.After(s.now) {
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

// task builds a new task record. Proactive tasks get background admission
// priority so they never delay replies to messages addressed to the bot.
func (s *submission) task(id, command, trigger string) store.TaskRecord {
	priority := admission.PriorityInteractive
	if trigger == TriggerProactive {
		priority = admission.PriorityBackground
	}
	return store.TaskRecord{
		ID: id, BotInstanceID: s.botID, ConversationKey: s.convKey, Status: conloomapi.TaskQueued,
		ActorPlatformUserID: s.ev.Actor.PlatformUserID, Command: command,
		TriggerMessageIDs: []string{s.ev.Message.ID}, Priority: int(priority),
		Meta:            conloomapi.TaskMeta{Trigger: trigger},
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

// CancelTask cancels a queued or running task and returns its state. If the
// task is being executed (a running reply or a command in progress) it waits
// briefly for the executor to record the outcome.
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

// cancel cancels taskID. If another party owns the task, the returned channel
// closes when it lets go; a runner owner records the cancellation itself.
// Otherwise cancel takes ownership, marks the stored task cancelled and
// returns a nil channel; a runner dispatched meanwhile finds the task owned
// and does not run it.
func (o *Orchestrator) cancel(ctx context.Context, botID, convKey, taskID string) (<-chan struct{}, error) {
	o.mu.Lock()
	ow, owned := o.owners[taskID]
	switch {
	case owned && ow.runner:
		ow.cancelled = true
	case !owned:
		ow = &taskOwner{done: make(chan struct{})}
		o.owners[taskID] = ow
	}
	o.mu.Unlock()
	o.sched.Cancel(convKey, taskID)
	if owned {
		return ow.done, nil
	}
	defer o.release(taskID, ow)
	rec, err := o.st.GetTask(ctx, botID, taskID)
	if err != nil || taskFinished(rec.Status) {
		return nil, err
	}
	return nil, o.complete(ctx, rec, conloomapi.TaskCancelled, rec.Meta, conloomapi.CodeTaskCancelled, nil)
}

// claim makes the caller the only owner of taskID until release. It returns
// nil if another party owns the task, which the caller must then leave alone.
// An owner must re-read the stored task and skip it if it is finished.
func (o *Orchestrator) claim(taskID string, runner bool) *taskOwner {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.owners[taskID]; ok {
		return nil
	}
	ow := &taskOwner{runner: runner, done: make(chan struct{})}
	o.owners[taskID] = ow
	return ow
}

func (o *Orchestrator) release(taskID string, ow *taskOwner) {
	o.mu.Lock()
	delete(o.owners, taskID)
	o.mu.Unlock()
	close(ow.done)
}

// cancelledByUser reports whether a user cancelled the task owned by ow.
func (o *Orchestrator) cancelledByUser(ow *taskOwner) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ow.cancelled
}

func taskFinished(status string) bool {
	switch status {
	case conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled:
		return true
	}
	return false
}

// complete stores the task's final state and actions with record. A runner
// whose task was superseded stores it as cancelled again, without actions,
// and gets errSuperseded.
func (o *Orchestrator) complete(ctx context.Context, task store.TaskRecord, status string, meta conloomapi.TaskMeta, code string, actions []store.OutboxRecord) error {
	o.mu.Lock()
	ow := o.owners[task.ID]
	o.mu.Unlock()
	if ow == nil || !ow.runner {
		return o.record(ctx, task, status, meta, code, actions)
	}
	ow.commit.Lock()
	defer ow.commit.Unlock()
	if ow.superseded {
		// The runner may have marked the task running after it was superseded.
		if meta.Trigger == "" {
			meta.Trigger = task.Meta.Trigger
		}
		err := o.st.CompleteTask(ctx, store.TaskCompletion{
			TaskID: task.ID, Status: conloomapi.TaskCancelled, Meta: meta, ErrorCode: conloomapi.CodeTaskCancelled,
		})
		if err != nil {
			o.log.Error("store superseded task failed", "task_id", task.ID, "error", err)
		}
		return errSuperseded
	}
	if err := o.record(ctx, task, status, meta, code, actions); err != nil {
		return err
	}
	ow.committed = true
	return nil
}

// record stores the task's final state and actions and notifies the bot's
// adapter when there are actions. A meta without a trigger keeps the task's.
// A join plan the task did not use is dropped.
func (o *Orchestrator) record(ctx context.Context, task store.TaskRecord, status string, meta conloomapi.TaskMeta, code string, actions []store.OutboxRecord) error {
	o.joinPlans.take(task.ID)
	if meta.Trigger == "" {
		meta.Trigger = task.Meta.Trigger
	}
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

// newAction builds a text message action for task. A group reply quotes the
// newest trigger message when shouldQuote says so; private replies quote nothing.
func (o *Orchestrator) newAction(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord, triggers []store.MessageRecord, text string) store.OutboxRecord {
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
		if last := triggers[len(triggers)-1]; o.shouldQuote(ctx, task, last, now) {
			a.ReplyToMessageID, a.ReplyToPlatformMessageID = last.ID, last.PlatformMessageID
		}
	}
	return store.OutboxRecord{
		Action: a, BotInstanceID: task.BotInstanceID, ConversationKey: task.ConversationKey, TaskID: task.ID,
		Platform: conv.Platform, Status: store.OutboxPending, CreatedAt: now, ExpiresAt: now.Add(o.actionTTL),
	}
}

// newActions builds one text message action per part of a reply, in order.
// Only the first part may quote (see newAction). Parts of a reply with more
// than one carry their index and count; the first is sent at once and each
// later one partDelay of itself (the time to type it) after the one before.
// All parts expire with the first, so none outlives a part ahead of it.
func (o *Orchestrator) newActions(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord, triggers []store.MessageRecord, parts []string) []store.OutboxRecord {
	if len(parts) == 1 {
		return []store.OutboxRecord{o.newAction(ctx, task, conv, triggers, parts[0])}
	}
	recs := make([]store.OutboxRecord, 0, len(parts))
	var at, expires time.Time
	for i, part := range parts {
		var rec store.OutboxRecord
		if i == 0 {
			rec = o.newAction(ctx, task, conv, triggers, part)
			at, expires = rec.CreatedAt, rec.ExpiresAt
		} else {
			rec = o.newAction(ctx, task, conv, nil, part)
			at = at.Add(partDelay(part))
			rec.NotBefore, rec.ExpiresAt = at, expires
		}
		rec.Action.PartIndex, rec.Action.PartCount = i+1, len(parts)
		recs = append(recs, rec)
	}
	return recs
}

const (
	// quoteMaxAge is the trigger age after which an auto-mode reply quotes it.
	quoteMaxAge = time.Minute
	// quoteLookback is how many of the newest messages are searched for
	// someone else speaking after the trigger.
	quoteLookback = 20
)

// shouldQuote reports whether a group reply quotes last, the newest trigger
// message, under the conversation's reply.quote mode. In auto mode it quotes
// only when last is older than quoteMaxAge or another member spoke after it
// (the sender's own follow-ups and bot messages do not count); with more than
// quoteLookback messages since last it quotes too. When the policy or the
// messages cannot be read it quotes, which keeps the reply unambiguous.
func (o *Orchestrator) shouldQuote(ctx context.Context, task store.TaskRecord, last store.MessageRecord, now time.Time) bool {
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		o.log.Warn("quote check: load policy failed; quoting", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return true
	}
	switch pv.Policy.Reply.Quote {
	case conloomapi.QuoteNever:
		return false
	case conloomapi.QuoteAlways:
		return true
	}
	if now.Sub(last.CreatedAt) > quoteMaxAge {
		return true
	}
	recent, err := o.st.RecentMessages(ctx, task.ConversationKey, quoteLookback)
	if err != nil {
		o.log.Warn("quote check: load recent messages failed; quoting", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return true
	}
	i := slices.IndexFunc(recent, func(m store.MessageRecord) bool { return m.ID == last.ID })
	if i < 0 {
		return true
	}
	return slices.ContainsFunc(recent[i+1:], func(m store.MessageRecord) bool {
		return !m.FromBot && m.PlatformUserID != last.PlatformUserID
	})
}

// conversation loads the stored conversation of a "bot:platform:type:id" key.
func (o *Orchestrator) conversation(ctx context.Context, convKey string) (store.ConversationRecord, error) {
	parts := strings.SplitN(convKey, ":", 4)
	if len(parts) != 4 {
		return store.ConversationRecord{}, fmt.Errorf("core: malformed conversation key %q", convKey)
	}
	return o.st.GetConversation(ctx, parts[0], parts[3])
}
