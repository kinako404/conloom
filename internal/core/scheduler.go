package core

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/ids"
)

var (
	ErrQueueFull       = errors.New("conversation queue is full")
	ErrSchedulerClosed = errors.New("scheduler is closed")
)

// SessionConfig is the scheduling policy of one conversation. It is re-read
// whenever the conversation is re-evaluated, so changes take effect on the
// next Admit or Kick.
type SessionConfig struct {
	// MaxConcurrent is the number of tasks that may run at once; 0 pauses
	// the conversation (tasks stay queued).
	MaxConcurrent int
	// MaxQueued bounds the number of not-yet-started tasks.
	MaxQueued        int
	Debounce         time.Duration
	MaxMergeWindow   time.Duration
	MinReplyInterval time.Duration
	// CooldownUntil is when tasks created with MergeOptions.Cooldown may
	// start (see MergeOptions).
	CooldownUntil time.Time
}

type QueuedTask struct {
	TaskID            string
	ConversationKey   string
	ActorID           string
	TriggerMessageIDs []string
	EnqueuedAt        time.Time
	ReadyAt           time.Time
	// Held reports, in tasks returned by Active, that the task is held (see
	// MergeOptions.Cooldown): it waits for the conversation's cooldown, for a
	// running task or, once held, for its sender's pause.
	Held bool

	// addressed is set once a message addressed to the bot merged into the
	// task; deferred once it was first held (and reported to onHold);
	// ignoreCooldown once a message with MergeOptions.IgnoreCooldown created
	// or merged into it.
	addressed, deferred, ignoreCooldown bool
	// merge is the merge window the task was created with; zero fields (as
	// in restored tasks) follow the conversation's SessionConfig.
	merge MergeOptions
}

// MergeOptions set a new task's merge window: each merged message moves its
// ReadyAt to Debounce later, but never past MaxMergeWindow after it was
// enqueued. Zero fields take the conversation's SessionConfig values.
//
// Addressed marks a message that mentions or replies to the bot. Merged into
// a task whose Debounce is longer than the conversation's, it makes the task
// ready within the conversation's Debounce instead, and never later than the
// task's current ReadyAt.
//
// Cooldown makes a new task wait until the conversation's
// SessionConfig.CooldownUntil has passed and no task of the conversation runs,
// unless an Addressed message merged into it. While it waits it is held: its
// merge window stays open, and the tasks queued behind it may start before
// it. Once held, each merged message moves its ReadyAt to Debounce later
// without the MaxMergeWindow cap, and the task stays held until its ReadyAt
// too, so it starts only after its sender paused (and after MinReplyInterval).
// MaxHold, counted from when the task was enqueued, bounds all this: then its
// merge window closes, and the task becomes ready at once if the cooldown has
// passed and no task runs, or else is dropped (see Scheduler.OnDrop).
//
// IgnoreCooldown marks a message that frees its Cooldown task from waiting
// for SessionConfig.CooldownUntil, from then on. The task still waits for
// running tasks and, once held, for its sender's pause, and its merge window
// still closes at MaxHold, but it is never dropped: past MaxHold it starts
// once no task runs. Unlike Addressed it keeps the task's merge window.
//
// Interrupt marks a message that restarts its sender's turn. It merges into
// the sender's newest not-yet-started task even after that task's merge
// window closed. If the sender has no such task, it supersedes the sender's
// newest running task that has not stored its result (see
// AdmitPlan.Supersedes): that task's context is cancelled and a new task,
// carrying its trigger messages and this one, waits for the sender's pause
// again. A task created with Interrupt
// takes zero Debounce and MaxMergeWindow as they are, not from the
// SessionConfig.
type MergeOptions struct {
	Debounce, MaxMergeWindow time.Duration
	Addressed                bool
	Cooldown                 bool
	MaxHold                  time.Duration
	IgnoreCooldown           bool
	Interrupt                bool
}

func (m MergeOptions) orConfig(cfg SessionConfig) MergeOptions {
	if m.Interrupt {
		return m
	}
	if m.Debounce == 0 {
		m.Debounce = cfg.Debounce
	}
	if m.MaxMergeWindow == 0 {
		m.MaxMergeWindow = cfg.MaxMergeWindow
	}
	return m
}

// Runner executes a task. The scheduler considers the task finished when
// Runner returns.
type Runner func(ctx context.Context, t QueuedTask)

type AdmitMode int

const (
	// AdmitNew merges into the actor's open merge window or creates a task.
	AdmitNew AdmitMode = iota
	// AdmitMergeOnly merges only into the actor's open merge window.
	AdmitMergeOnly
)

// AdmitPlan tells persist what Admit is about to do. NewTaskID and
// MergeIntoTaskID both empty means the message is only observed.
//
// Supersedes, with NewTaskID, names the running task the new task replaces
// (see MergeOptions.Interrupt); the new task's trigger messages are that
// task's followed by the message. persist returns ErrSupersedeRefused if that
// task can no longer be superseded, e.g. it has already stored its result;
// AdmitWith then calls persist again with a plain plan.
type AdmitPlan struct {
	NewTaskID       string
	MergeIntoTaskID string
	Supersedes      string
}

// ErrSupersedeRefused is returned by an AdmitWith persist function refusing
// to supersede a task (see AdmitPlan).
var ErrSupersedeRefused = errors.New("core: task cannot be superseded")

type AdmitResult struct {
	TaskID  string
	Merged  bool
	Created bool
}

// Scheduler runs tasks per conversation: FIFO, with merge windows, a
// concurrency limit, and a minimum interval between a task finishing and the
// next one starting; held tasks (see MergeOptions.Cooldown) are passed over
// until they may start. Each active conversation has its own goroutine, so
// conversations never block each other.
type Scheduler struct {
	run    Runner
	cfg    func(convKey string) SessionConfig
	idle   time.Duration
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool
	sessions map[string]*session
	onHold   func(t QueuedTask, until time.Time)
	onDrop   func(t QueuedTask)
}

type session struct {
	key  string
	wake chan struct{}

	mu           sync.Mutex
	dead         bool
	pending      []QueuedTask
	running      []*runningTask
	lastFinish   time.Time
	lastActivity time.Time
}

type runningTask struct {
	task   QueuedTask
	cancel context.CancelFunc
	// settled is set once the task was superseded or refused to be.
	settled bool
}

// NewScheduler returns a scheduler that executes tasks with run and reads
// each conversation's policy from cfg. A conversation with no queued or
// running tasks and no activity for idle has its goroutine and state
// (including pacing) reclaimed, so idle should exceed any MinReplyInterval in
// use.
func NewScheduler(run Runner, cfg func(convKey string) SessionConfig, idle time.Duration) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		run:      run,
		cfg:      cfg,
		idle:     idle,
		ctx:      ctx,
		cancel:   cancel,
		sessions: make(map[string]*session),
	}
}

// Admit is AdmitWith using the conversation's merge window for a new task.
func (s *Scheduler) Admit(convKey, actorID, messageID string, mode AdmitMode, persist func(AdmitPlan) error) (AdmitResult, error) {
	return s.AdmitWith(convKey, actorID, messageID, mode, MergeOptions{}, persist)
}

// AdmitWith decides, under the conversation's lock, whether messageID merges
// into actorID's open merge window, creates a new task with the merge window
// opts, or is only observed; calls persist with that plan; and applies it only
// if persist succeeds. A merge window is open while a not-yet-started task of
// the same actor has not reached its ReadyAt, or is held, up to its MaxHold; a
// merge keeps the merge window the task was created with, except as
// opts.Addressed and MergeOptions.Cooldown describe. opts.Interrupt widens
// merging and may supersede a running task (see MergeOptions.Interrupt).
// Creating a task when MaxQueued tasks are already waiting returns
// ErrQueueFull without calling persist.
func (s *Scheduler) AdmitWith(convKey, actorID, messageID string, mode AdmitMode, opts MergeOptions, persist func(AdmitPlan) error) (AdmitResult, error) {
	if s.isClosed() {
		return AdmitResult{}, ErrSchedulerClosed
	}
	cfg := s.cfg(convKey)
	sess := s.lockSession(convKey, mode == AdmitNew)
	if sess == nil {
		if mode == AdmitNew {
			return AdmitResult{}, ErrSchedulerClosed
		}
		return AdmitResult{}, persist(AdmitPlan{})
	}
	defer sess.mu.Unlock()
	now := time.Now()

	if i := sess.openWindow(actorID, cfg, now, opts.Interrupt); i >= 0 {
		t := &sess.pending[i]
		if err := persist(AdmitPlan{MergeIntoTaskID: t.TaskID}); err != nil {
			return AdmitResult{}, err
		}
		t.TriggerMessageIDs = append(t.TriggerMessageIDs, messageID)
		// Whether the task is held is judged before this message changes it.
		held := t.deferred || sess.heldLocked(*t, cfg, now)
		t.addressed = t.addressed || opts.Addressed
		t.ignoreCooldown = t.ignoreCooldown || opts.IgnoreCooldown
		switch m := t.merge.orConfig(cfg); {
		case opts.Addressed && m.Debounce > cfg.Debounce:
			t.ReadyAt = earliest(t.ReadyAt, now.Add(cfg.Debounce))
		case t.paced() && held:
			t.ReadyAt = now.Add(m.Debounce)
		default:
			t.ReadyAt = earliest(now.Add(m.Debounce), t.EnqueuedAt.Add(m.MaxMergeWindow))
		}
		sess.lastActivity = now
		sess.signal()
		return AdmitResult{TaskID: t.TaskID, Merged: true}, nil
	}

	if mode == AdmitMergeOnly {
		return AdmitResult{}, persist(AdmitPlan{})
	}
	if len(sess.pending) >= cfg.MaxQueued {
		return AdmitResult{}, ErrQueueFull
	}
	m := opts.orConfig(cfg)
	m.Addressed, m.IgnoreCooldown = false, false
	t := QueuedTask{
		TaskID:            ids.New("task"),
		ConversationKey:   convKey,
		ActorID:           actorID,
		TriggerMessageIDs: []string{messageID},
		EnqueuedAt:        now,
		ReadyAt:           now.Add(m.Debounce),
		merge:             m,
		ignoreCooldown:    opts.IgnoreCooldown,
	}
	if rt := sess.supersedable(actorID, opts.Interrupt); rt != nil {
		err := persist(AdmitPlan{NewTaskID: t.TaskID, Supersedes: rt.task.TaskID})
		switch {
		case err == nil:
			rt.settled = true
			rt.cancel()
			t.TriggerMessageIDs = append(slices.Clone(rt.task.TriggerMessageIDs), messageID)
			sess.pending = append(sess.pending, t)
			sess.lastActivity = now
			sess.signal()
			return AdmitResult{TaskID: t.TaskID, Created: true}, nil
		case errors.Is(err, ErrSupersedeRefused):
			rt.settled = true
		default:
			return AdmitResult{}, err
		}
	}
	if err := persist(AdmitPlan{NewTaskID: t.TaskID}); err != nil {
		return AdmitResult{}, err
	}
	sess.pending = append(sess.pending, t)
	sess.lastActivity = now
	sess.signal()
	return AdmitResult{TaskID: t.TaskID, Created: true}, nil
}

// Restore enqueues a task recovered after a restart, keeping its ReadyAt.
func (s *Scheduler) Restore(t QueuedTask) {
	sess := s.lockSession(t.ConversationKey, true)
	if sess == nil {
		return
	}
	defer sess.mu.Unlock()
	t.TriggerMessageIDs = slices.Clone(t.TriggerMessageIDs)
	sess.pending = append(sess.pending, t)
	sess.lastActivity = time.Now()
	sess.signal()
}

// Cancel removes a not-yet-started task or cancels the context of a running
// one. It reports whether the task was found.
func (s *Scheduler) Cancel(convKey, taskID string) bool {
	sess := s.lockSession(convKey, false)
	if sess == nil {
		return false
	}
	defer sess.mu.Unlock()
	for i, t := range sess.pending {
		if t.TaskID == taskID {
			sess.pending = slices.Delete(sess.pending, i, i+1)
			sess.signal()
			return true
		}
	}
	for _, rt := range sess.running {
		if rt.task.TaskID == taskID {
			rt.cancel()
			return true
		}
	}
	return false
}

// Active returns the conversation's unfinished tasks: running ones in start
// order, then queued ones in FIFO order, with Held set on those held now.
func (s *Scheduler) Active(convKey string) []QueuedTask {
	cfg := s.cfg(convKey)
	sess := s.lockSession(convKey, false)
	if sess == nil {
		return nil
	}
	defer sess.mu.Unlock()
	now := time.Now()
	out := make([]QueuedTask, 0, len(sess.running)+len(sess.pending))
	for _, rt := range sess.running {
		out = append(out, cloneTask(rt.task))
	}
	for _, t := range sess.pending {
		c := cloneTask(t)
		c.Held = sess.heldLocked(t, cfg, now)
		out = append(out, c)
	}
	return out
}

// Kick makes the conversation re-evaluate its queue against its current
// SessionConfig, e.g. after MaxConcurrent changed from 0.
func (s *Scheduler) Kick(convKey string) {
	s.mu.Lock()
	sess := s.sessions[convKey]
	s.mu.Unlock()
	if sess != nil {
		sess.signal()
	}
}

// Close cancels all running tasks and waits for every scheduler goroutine,
// including running Runners, to return. Queued tasks are not started.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// OnHold sets fn to be called, outside the scheduler's locks, the first time
// each task is held, with the conversation's CooldownUntil, or the zero time
// if the cooldown has passed and the task waits for a running task.
func (s *Scheduler) OnHold(fn func(t QueuedTask, until time.Time)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onHold = fn
}

// OnDrop sets fn to be called, outside the scheduler's locks, for each held
// task dropped at its MaxHold; fn must finish the task.
func (s *Scheduler) OnDrop(fn func(t QueuedTask)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onDrop = fn
}

func (s *Scheduler) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// lockSession returns the live session for key with its mutex held. If none
// exists it creates one and starts its goroutine when create is set and the
// scheduler is open; otherwise it returns nil.
func (s *Scheduler) lockSession(key string, create bool) *session {
	for {
		s.mu.Lock()
		sess := s.sessions[key]
		if sess == nil {
			if !create || s.closed {
				s.mu.Unlock()
				return nil
			}
			sess = &session{key: key, wake: make(chan struct{}, 1), lastActivity: time.Now()}
			sess.mu.Lock()
			s.sessions[key] = sess
			s.wg.Add(1)
			go s.loop(sess)
			s.mu.Unlock()
			return sess
		}
		s.mu.Unlock()
		sess.mu.Lock()
		if !sess.dead {
			return sess
		}
		sess.mu.Unlock()
		s.forget(sess)
	}
}

func (s *Scheduler) forget(sess *session) {
	s.mu.Lock()
	if s.sessions[sess.key] == sess {
		delete(s.sessions, sess.key)
	}
	s.mu.Unlock()
}

func (s *Scheduler) loop(sess *session) {
	defer s.wg.Done()
	timer := time.NewTimer(0)
	timer.Stop()
	defer timer.Stop()
	for {
		cfg := s.cfg(sess.key)
		sess.mu.Lock()
		wait, idle, held, dropped := s.dispatchLocked(sess, cfg, time.Now())
		if idle {
			sess.dead = true
		}
		sess.mu.Unlock()
		s.mu.Lock()
		onHold, onDrop := s.onHold, s.onDrop
		s.mu.Unlock()
		for _, h := range held {
			if onHold != nil {
				onHold(h.task, h.until)
			}
		}
		for _, t := range dropped {
			if onDrop != nil {
				onDrop(t)
			}
		}
		if idle {
			s.forget(sess)
			return
		}

		var fire <-chan time.Time
		if wait > 0 {
			timer.Reset(wait)
			fire = timer.C
		} else {
			timer.Stop()
		}
		select {
		case <-sess.wake:
		case <-fire:
		case <-s.ctx.Done():
			return
		}
	}
}

// heldTask is a task held for the first time, with the cooldown end it waits
// for (zero if it only waits for a running task).
type heldTask struct {
	task  QueuedTask
	until time.Time
}

// dispatchLocked releases or drops the held tasks at their MaxHold, then
// starts every task that may start now, in FIFO order but passing over held
// tasks. It returns how long to wait before re-evaluating (0: until woken),
// whether the session has been idle long enough to be reclaimed, the tasks
// held for the first time and the dropped tasks.
func (s *Scheduler) dispatchLocked(sess *session, cfg SessionConfig, now time.Time) (wait time.Duration, idle bool, held []heldTask, dropped []QueuedTask) {
	for i := 0; i < len(sess.pending); {
		if t := &sess.pending[i]; t.paced() && t.pastHold(now) {
			if !t.ignoreCooldown && sess.blockedLocked(*t, cfg, now) {
				dropped = append(dropped, cloneTask(*t))
				sess.pending = slices.Delete(sess.pending, i, i+1)
				continue
			}
			t.ReadyAt = earliest(t.ReadyAt, now)
		}
		i++
	}
	for {
		if s.ctx.Err() != nil {
			return 0, false, nil, dropped
		}
		i := slices.IndexFunc(sess.pending, func(t QueuedTask) bool { return !sess.heldLocked(t, cfg, now) })
		if i < 0 {
			break
		}
		head := sess.pending[i]
		if d := head.ReadyAt.Sub(now); d > 0 {
			wait = d
			break
		}
		if len(sess.running) >= cfg.MaxConcurrent {
			break
		}
		if !sess.lastFinish.IsZero() {
			if d := sess.lastFinish.Add(cfg.MinReplyInterval).Sub(now); d > 0 {
				wait = d
				break
			}
		}
		sess.pending = slices.Delete(sess.pending, i, i+1)
		s.startLocked(sess, head)
	}
	// Held tasks are re-evaluated at the earliest of their wake times, or
	// when a running task finishes and signals.
	wake := func(at time.Time) {
		if d := at.Sub(now); d > 0 && (wait == 0 || d < wait) {
			wait = d
		}
	}
	for i := range sess.pending {
		t := &sess.pending[i]
		if !sess.heldLocked(*t, cfg, now) {
			continue
		}
		if !t.deferred {
			t.deferred = true
			h := heldTask{task: cloneTask(*t)}
			if !t.ignoreCooldown && now.Before(cfg.CooldownUntil) {
				h.until = cfg.CooldownUntil
			}
			held = append(held, h)
		}
		wake(cfg.CooldownUntil)
		wake(t.ReadyAt)
		if t.merge.MaxHold > 0 {
			wake(t.EnqueuedAt.Add(t.merge.MaxHold))
		}
	}
	if len(sess.pending) > 0 || len(sess.running) > 0 {
		return wait, false, held, dropped
	}
	if d := sess.lastActivity.Add(s.idle).Sub(now); d > 0 {
		return d, false, held, dropped
	}
	return 0, true, held, dropped
}

// paced reports whether t waits for the cooldown (see MergeOptions.Cooldown):
// a Cooldown task no message addressed to the bot merged into.
func (t QueuedTask) paced() bool { return t.merge.Cooldown && !t.addressed }

// pastHold reports whether t reached its MaxHold at now.
func (t QueuedTask) pastHold(now time.Time) bool {
	return t.merge.MaxHold > 0 && !now.Before(t.EnqueuedAt.Add(t.merge.MaxHold))
}

// blockedLocked reports whether the paced task t must wait at now: a task of
// the conversation runs or, unless t ignores it, the cooldown has not passed.
func (sess *session) blockedLocked(t QueuedTask, cfg SessionConfig, now time.Time) bool {
	return len(sess.running) > 0 || !t.ignoreCooldown && now.Before(cfg.CooldownUntil)
}

// heldLocked reports whether the pending task t is held at now (see
// MergeOptions.Cooldown and MergeOptions.IgnoreCooldown).
func (sess *session) heldLocked(t QueuedTask, cfg SessionConfig, now time.Time) bool {
	switch {
	case !t.paced():
		return false
	case t.pastHold(now):
		return t.ignoreCooldown && len(sess.running) > 0
	}
	return sess.blockedLocked(t, cfg, now) || t.deferred && now.Before(t.ReadyAt)
}

func (s *Scheduler) startLocked(sess *session, t QueuedTask) {
	ctx, cancel := context.WithCancel(s.ctx)
	rt := &runningTask{task: t, cancel: cancel}
	sess.running = append(sess.running, rt)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.run(ctx, cloneTask(t))
		now := time.Now()
		sess.mu.Lock()
		sess.running = slices.DeleteFunc(sess.running, func(r *runningTask) bool { return r == rt })
		sess.lastFinish = now
		sess.lastActivity = now
		sess.mu.Unlock()
		sess.signal()
	}()
}

// openWindow returns the index of actorID's most recent not-yet-started task
// whose merge window is still open at now (before its ReadyAt, or while it is
// held; never for a paced task past its MaxHold), or -1. With interrupt any
// not-yet-started task of actorID qualifies.
func (sess *session) openWindow(actorID string, cfg SessionConfig, now time.Time, interrupt bool) int {
	for i := len(sess.pending) - 1; i >= 0; i-- {
		t := sess.pending[i]
		if t.ActorID != actorID || t.paced() && t.pastHold(now) {
			continue
		}
		if interrupt || now.Before(t.ReadyAt) || sess.heldLocked(t, cfg, now) {
			return i
		}
	}
	return -1
}

// supersedable returns, with interrupt, actorID's most recently started
// running task that is not settled, or nil.
func (sess *session) supersedable(actorID string, interrupt bool) *runningTask {
	if !interrupt {
		return nil
	}
	for i := len(sess.running) - 1; i >= 0; i-- {
		if rt := sess.running[i]; rt.task.ActorID == actorID && !rt.settled {
			return rt
		}
	}
	return nil
}

func (sess *session) signal() {
	select {
	case sess.wake <- struct{}{}:
	default:
	}
}

func cloneTask(t QueuedTask) QueuedTask {
	t.TriggerMessageIDs = slices.Clone(t.TriggerMessageIDs)
	return t
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
