package memory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// CheckEvery is how often the scheduler looks for groups due for consolidation.
	CheckEvery        = 2 * time.Minute
	revisionRetention = 30 * 24 * time.Hour
)

// SchedulerDeps are the Scheduler's collaborators. Store, Policies and Runner
// are required. Bots, the bot directory, decides which conversations may be
// consolidated: those of its bots, and of their private chats those
// config.BotConfig.PrivateAllowed allows; without it all may. Nil
// Metrics are registered on a private registry, a nil Logger discards, a nil
// Location is UTC and a nil Now is time.Now.
type SchedulerDeps struct {
	Store    store.Store
	Policies *policy.Service
	Runner   Runner
	Bots     *botdir.Directory
	Metrics  *obs.Metrics
	Logger   *slog.Logger
	Location *time.Location
	Now      func() time.Time
}

// Scheduler runs consolidations one at a time on a single worker goroutine:
// the memory scopes of group and private conversations that are due on every
// check, each once, scopes an admin triggered, and the daily affinity decay
// and revision purge. A conversation is its own scope but for panel chats,
// which share their bot's (see ScopeKey); a shared scope follows the policy of
// its newest chat.
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

// Trigger queues a manual consolidation of the memory scope of convKey
// (ScopeKeyOf) without waiting for it; triggers of a scope that is already
// queued are merged.
func (s *Scheduler) Trigger(convKey string) {
	convKey = ScopeKeyOf(convKey)
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

// PolicyChanged marks the cursor of convKey's memory scope to skip ahead to
// its backlog when the conversation's newest policy version turns memory on
// and the one before had it off or had no memory section. The worker applies
// the skip before the scope's next run.
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
	s.jump[ScopeKeyOf(convKey)] = true
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

// tick consolidates every memory scope of group and private conversations
// that is due.
func (s *Scheduler) tick(ctx context.Context) {
	scopes, err := s.scopes(ctx)
	if err != nil {
		s.logError(ctx, "memory: list conversations failed", "error", err)
		return
	}
	for _, key := range scopes {
		if ctx.Err() != nil {
			return
		}
		s.consolidate(ctx, key, false)
	}
}

// consolidate runs the Runner for the memory scope convKey if its bot is in
// the directory, memory is on, the conversation is not paused and, for a
// private chat, the bot still chats with that user privately, a shared scope
// deciding by the policy of its newest chat; a scheduled run also needs the
// scope to be due.
func (s *Scheduler) consolidate(ctx context.Context, convKey string, manual bool) {
	if !s.botServes(ctx, convKey) {
		return
	}
	sc, err := resolveScope(ctx, s.d.Store, convKey)
	if err != nil {
		s.logError(ctx, "memory: list the scope's chats failed", "conversation", convKey, "error", err)
		return
	}
	lead, err := sc.lead(ctx, s.d.Store)
	if err != nil || lead == "" {
		if err != nil {
			s.logError(ctx, "memory: find the scope's newest chat failed", "conversation", convKey, "error", err)
		}
		return
	}
	pv, err := s.d.Policies.Current(ctx, lead)
	if err != nil {
		s.logError(ctx, "memory: read policy failed", "conversation", convKey, "error", err)
		return
	}
	p := pv.Policy
	if !memoryOn(p) || p.Concurrency.MaxInteractiveRequests == 0 {
		return
	}
	g, err := s.ensureCursor(ctx, sc, p.Memory.Backlog)
	if err != nil {
		s.logError(ctx, "memory: prepare cursor failed", "conversation", convKey, "error", err)
		return
	}
	if !manual {
		pending, err := sc.countAfter(ctx, s.d.Store, g.CursorSeq)
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
	level := slog.LevelInfo
	if res.Skipped == "backoff" {
		level = slog.LevelDebug
	}
	s.d.Logger.Log(ctx, level, "memory: consolidation finished", "conversation", convKey, "manual", manual,
		"batches", res.Batches, "messages", res.Messages, "members", res.MembersUpdated, "skipped", res.Skipped)
}

// ensureCursor returns the memory row of sc. A scope without one gets a new
// row whose cursor leaves backlog messages of its chats to consolidate; a
// scope whose memory was just turned back on has its cursor moved up to that
// point.
func (s *Scheduler) ensureCursor(ctx context.Context, sc scope, backlog int) (store.GroupMemory, error) {
	g, err := s.d.Store.GetGroupMemory(ctx, sc.key)
	fresh := errors.Is(err, store.ErrNotFound)
	if err != nil && !fresh {
		return store.GroupMemory{}, err
	}
	s.mu.Lock()
	jump := s.jump[sc.key]
	s.mu.Unlock()
	if !fresh && !jump {
		return g, nil
	}
	cursor, err := sc.backlogCursor(ctx, s.d.Store, backlog)
	if err != nil {
		return store.GroupMemory{}, err
	}
	if fresh {
		g = store.GroupMemory{ConversationKey: sc.key, CursorSeq: cursor}
	} else {
		g.CursorSeq = max(g.CursorSeq, cursor)
	}
	if err := s.d.Store.PutGroupMemory(ctx, g); err != nil {
		return store.GroupMemory{}, err
	}
	s.mu.Lock()
	delete(s.jump, sc.key)
	s.mu.Unlock()
	return g, nil
}

// due reports whether a conversation with pending new messages, last
// consolidated at lastRun (zero for never), should be consolidated now: at
// ConsolidateEvery messages, or at ConsolidateMin once ConsolidateInterval
// has passed.
func due(pending int, lastRun time.Time, p conloomapi.MemoryPolicy, now time.Time) bool {
	if pending >= p.ConsolidateEvery {
		return true
	}
	return pending >= p.ConsolidateMin && (lastRun.IsZero() || now.Sub(lastRun) >= p.ConsolidateInterval.Duration)
}

func (s *Scheduler) dailyIfNewDay(ctx context.Context) {
	day := LocalDay(s.d.Now(), s.d.Location)
	if day == s.lastDay {
		return
	}
	s.lastDay = day
	s.daily(ctx)
}

// daily applies affinity decay in every memory scope with memory and affinity
// on, then purges revisions older than 30 days.
func (s *Scheduler) daily(ctx context.Context) {
	now := s.d.Now()
	today := LocalDay(now, s.d.Location)
	scopes, err := s.scopes(ctx)
	if err != nil {
		s.logError(ctx, "memory: list conversations failed", "error", err)
	}
	for _, key := range scopes {
		if ctx.Err() != nil {
			return
		}
		s.decayGroup(ctx, key, today)
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

// conversations lists the group conversations, then the private ones.
func (s *Scheduler) conversations(ctx context.Context) ([]store.ConversationRecord, error) {
	var all []store.ConversationRecord
	for _, t := range []string{conloomapi.ConvGroup, conloomapi.ConvPrivate} {
		convs, err := s.d.Store.ListConversations(ctx, t)
		if err != nil {
			return all, err
		}
		all = append(all, convs...)
	}
	return all, nil
}

// scopes lists the memory scopes of the group conversations, then of the
// private ones, each once, as far as conversations listed them.
func (s *Scheduler) scopes(ctx context.Context) ([]string, error) {
	convs, err := s.conversations(ctx)
	seen := make(map[string]bool, len(convs))
	keys := make([]string, 0, len(convs))
	for _, c := range convs {
		if key := ScopeKey(c); !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, err
}

// decayGroup applies affinity decay to the members of the memory scope
// convKey when its policy, a shared scope's newest chat's, has memory and
// affinity on.
func (s *Scheduler) decayGroup(ctx context.Context, convKey, today string) {
	sc, err := resolveScope(ctx, s.d.Store, convKey)
	if err != nil {
		s.logError(ctx, "memory: list the scope's chats failed", "conversation", convKey, "error", err)
		return
	}
	lead, err := sc.lead(ctx, s.d.Store)
	if err != nil || lead == "" {
		if err != nil {
			s.logError(ctx, "memory: find the scope's newest chat failed", "conversation", convKey, "error", err)
		}
		return
	}
	pv, err := s.d.Policies.Current(ctx, lead)
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

// botServes reports whether convKey's bot is in the directory and, for a
// private chat, its private_chat setting still allows the chat's user; panel
// chats, and so a shared panel scope, are always allowed. A conversation of a
// bot no longer in the directory, such as a deleted panel bot, is skipped
// quietly.
func (s *Scheduler) botServes(ctx context.Context, convKey string) bool {
	parts := strings.SplitN(convKey, ":", 4)
	if s.d.Bots == nil || len(parts) != 4 {
		return true
	}
	bot, ok := s.d.Bots.Bot(parts[0])
	if !ok {
		s.d.Logger.Debug("memory: skipped a conversation of a bot no longer in the directory", "conversation", convKey)
		return false
	}
	if parts[2] != conloomapi.ConvPrivate || IsSharedScope(convKey) {
		return true
	}
	conv, err := s.d.Store.GetConversation(ctx, parts[0], parts[3])
	if err != nil {
		s.logError(ctx, "memory: read private conversation failed", "conversation", convKey, "error", err)
		return false
	}
	return bot.PrivateAllowed(conv.Platform, conv.PlatformConversationID)
}

func (s *Scheduler) logError(ctx context.Context, msg string, args ...any) {
	if ctx.Err() == nil {
		s.d.Logger.Error(msg, args...)
	}
}

func memoryOn(p conloomapi.GroupPolicy) bool {
	return p.Memory.Enabled && p.Concurrency.AllowLongRequests
}
