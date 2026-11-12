package memory

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// CheckEvery is how often the scheduler looks for groups due for consolidation.
	CheckEvery = 2 * time.Minute
	// MinIntervalMessages is the fewest new messages consolidated because
	// ConsolidateInterval has passed.
	MinIntervalMessages = 20
	revisionRetention   = 30 * 24 * time.Hour
)

// SchedulerDeps are the Scheduler's collaborators. Store, Policies and Runner
// are required. Nil Metrics are registered on a private registry, a nil Logger
// discards, a nil Location is UTC and a nil Now is time.Now.
type SchedulerDeps struct {
	Store    store.Store
	Policies *policy.Service
	Runner   Runner
	Metrics  *obs.Metrics
	Logger   *slog.Logger
	Location *time.Location
	Now      func() time.Time
}

// Scheduler runs consolidations one at a time on a single worker goroutine:
// groups that are due on every check, groups an admin triggered, and the daily
// affinity decay and revision purge.
type Scheduler struct {
	d SchedulerDeps

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	started   bool
	wake      chan struct{}

	mu        sync.Mutex
	triggered []string
	queued    map[string]bool
	jump      map[string]bool

	lastDay string
}

func NewScheduler(d SchedulerDeps) *Scheduler {
	if d.Metrics == nil {
		d.Metrics = obs.NewMetrics(prometheus.NewRegistry())
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Location == nil {
		d.Location = time.UTC
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		d: d, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
		queued: map[string]bool{}, jump: map[string]bool{},
	}
}

// Start starts the worker goroutine, which runs the daily tasks at once and
// then checks for due groups every CheckEvery. Later calls do nothing.
func (s *Scheduler) Start() {
	s.startOnce.Do(func() {
		s.mu.Lock()
		s.started = true
		s.mu.Unlock()
		go s.loop()
	})
}

// Close cancels the running consolidation and waits for the worker to exit.
func (s *Scheduler) Close() {
	s.cancel()
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
}

// Trigger queues a manual consolidation of convKey without waiting for it;
// triggers of a conversation that is already queued are merged.
func (s *Scheduler) Trigger(convKey string) {
	s.mu.Lock()
	if !s.queued[convKey] {
		s.queued[convKey] = true
		s.triggered = append(s.triggered, convKey)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// PolicyChanged marks convKey's cursor to skip ahead to its backlog when the
// newest policy version turns memory on and the one before had it off or had
// no memory section. The worker applies the skip before the group's next run.
func (s *Scheduler) PolicyChanged(convKey string) {
	versions, err := s.d.Store.ListPolicyVersions(s.ctx, convKey)
	if err != nil {
		s.logError(s.ctx, "memory: list policy versions failed", "conversation", convKey, "error", err)
		return
	}
	if len(versions) == 0 || !memoryOn(versions[0].Policy) || len(versions) > 1 && memoryOn(versions[1].Policy) {
		return
	}
	s.mu.Lock()
	s.jump[convKey] = true
	s.mu.Unlock()
}

func (s *Scheduler) loop() {
	defer close(s.done)
	check := time.NewTicker(CheckEvery)
	defer check.Stop()
	s.dailyIfNewDay(s.ctx)
	for {
		s.runTriggered(s.ctx)
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-check.C:
			s.dailyIfNewDay(s.ctx)
			s.tick(s.ctx)
		}
	}
}

// runTriggered runs the queued manual consolidations until none are left.
func (s *Scheduler) runTriggered(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.Lock()
		if len(s.triggered) == 0 {
			s.mu.Unlock()
			return
		}
		convKey := s.triggered[0]
		s.triggered = s.triggered[1:]
		delete(s.queued, convKey)
		s.mu.Unlock()
		s.consolidate(ctx, convKey, true)
	}
}

// tick consolidates every group that is due.
func (s *Scheduler) tick(ctx context.Context) {
	convs, err := s.d.Store.ListConversations(ctx, conloomapi.ConvGroup)
	if err != nil {
		s.logError(ctx, "memory: list conversations failed", "error", err)
		return
	}
	for _, c := range convs {
		if ctx.Err() != nil {
			return
		}
		s.consolidate(ctx, c.Key, false)
	}
}

// consolidate runs the Runner for convKey if memory is on and the group is
// not paused; a scheduled run also needs the group to be due.
func (s *Scheduler) consolidate(ctx context.Context, convKey string, manual bool) {
	pv, err := s.d.Policies.Current(ctx, convKey)
	if err != nil {
		s.logError(ctx, "memory: read policy failed", "conversation", convKey, "error", err)
		return
	}
	p := pv.Policy
	if !memoryOn(p) || p.Concurrency.MaxInteractiveRequests == 0 {
		return
	}
	g, err := s.ensureCursor(ctx, convKey, p.Memory.Backlog)
	if err != nil {
		s.logError(ctx, "memory: prepare cursor failed", "conversation", convKey, "error", err)
		return
	}
	if !manual {
		pending, err := s.d.Store.CountMessagesAfter(ctx, convKey, g.CursorSeq)
		if err != nil {
			s.logError(ctx, "memory: count new messages failed", "conversation", convKey, "error", err)
			return
		}
		if !due(pending, g.LastRunAt, p.Memory, s.d.Now()) {
			return
		}
	}
	res, err := s.d.Runner.Run(ctx, convKey, manual)
	if err != nil {
		s.logError(ctx, "memory: consolidation failed", "conversation", convKey, "manual", manual, "error", err)
		return
	}
	s.d.Logger.Info("memory: consolidation finished", "conversation", convKey, "manual", manual,
		"batches", res.Batches, "messages", res.Messages, "members", res.MembersUpdated, "skipped", res.Skipped)
}

// ensureCursor returns convKey's memory row. A group without one gets a new
// row whose cursor leaves backlog messages to consolidate; a group whose
// memory was just turned back on has its cursor moved up to that point.
func (s *Scheduler) ensureCursor(ctx context.Context, convKey string, backlog int) (store.GroupMemory, error) {
	g, err := s.d.Store.GetGroupMemory(ctx, convKey)
	fresh := errors.Is(err, store.ErrNotFound)
	if err != nil && !fresh {
		return store.GroupMemory{}, err
	}
	s.mu.Lock()
	jump := s.jump[convKey]
	s.mu.Unlock()
	if !fresh && !jump {
		return g, nil
	}
	cursor, err := s.d.Store.BacklogCursor(ctx, convKey, backlog)
	if err != nil {
		return store.GroupMemory{}, err
	}
	if fresh {
		g = store.GroupMemory{ConversationKey: convKey, CursorSeq: cursor}
	} else {
		g.CursorSeq = max(g.CursorSeq, cursor)
	}
	if err := s.d.Store.PutGroupMemory(ctx, g); err != nil {
		return store.GroupMemory{}, err
	}
	s.mu.Lock()
	delete(s.jump, convKey)
	s.mu.Unlock()
	return g, nil
}

// due reports whether a group with pending new messages, last consolidated at
// lastRun (zero for never), should be consolidated now.
func due(pending int, lastRun time.Time, p conloomapi.MemoryPolicy, now time.Time) bool {
	if pending >= p.ConsolidateEvery {
		return true
	}
	return pending >= MinIntervalMessages && (lastRun.IsZero() || now.Sub(lastRun) >= p.ConsolidateInterval.Duration)
}

func (s *Scheduler) dailyIfNewDay(ctx context.Context) {
	day := LocalDay(s.d.Now(), s.d.Location)
	if day == s.lastDay {
		return
	}
	s.lastDay = day
	s.daily(ctx)
}

// daily applies affinity decay in every group with memory and affinity on,
// then purges revisions older than 30 days.
func (s *Scheduler) daily(ctx context.Context) {
	now := s.d.Now()
	today := LocalDay(now, s.d.Location)
	convs, err := s.d.Store.ListConversations(ctx, conloomapi.ConvGroup)
	if err != nil {
		s.logError(ctx, "memory: list conversations failed", "error", err)
	}
	for _, c := range convs {
		if ctx.Err() != nil {
			return
		}
		s.decayGroup(ctx, c.Key, today)
	}
	n, err := s.d.Store.PurgeMemoryRevisions(ctx, now.Add(-revisionRetention))
	if err != nil {
		s.logError(ctx, "memory: purge revisions failed", "error", err)
		return
	}
	if n > 0 {
		s.d.Logger.Info("memory: purged revisions", "count", n)
	}
}

func (s *Scheduler) decayGroup(ctx context.Context, convKey, today string) {
	pv, err := s.d.Policies.Current(ctx, convKey)
	if err != nil {
		s.logError(ctx, "memory: read policy failed", "conversation", convKey, "error", err)
		return
	}
	if !memoryOn(pv.Policy) || !pv.Policy.Memory.Affinity {
		return
	}
	members, err := s.d.Store.ListMemberMemories(ctx, convKey)
	if err != nil {
		s.logError(ctx, "memory: list members failed", "conversation", convKey, "error", err)
		return
	}
	for _, m := range members {
		if _, changed := Decay(m, today); !changed {
			continue
		}
		u, err := UpdateMember(ctx, s.d.Store, convKey, m.PlatformUserID, store.RevisionRule,
			func(m *store.MemberMemory) (string, bool) {
				next, changed := Decay(*m, today)
				*m = next
				return ReasonDecay, changed
			})
		if errors.Is(err, ErrUpdateConflict) {
			s.d.Logger.Warn("memory: decay skipped a member that kept changing", "conversation", convKey)
			continue
		}
		if err != nil {
			s.logError(ctx, "memory: decay failed", "conversation", convKey, "error", err)
			continue
		}
		if u.AffinityChanged() {
			s.d.Metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionRule).Inc()
		}
	}
}

func (s *Scheduler) logError(ctx context.Context, msg string, args ...any) {
	if ctx.Err() == nil {
		s.d.Logger.Error(msg, args...)
	}
}

func memoryOn(p conloomapi.GroupPolicy) bool {
	return p.Memory.Enabled && p.Concurrency.AllowLongRequests
}
