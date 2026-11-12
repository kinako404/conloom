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
}

type QueuedTask struct {
	TaskID            string
	ConversationKey   string
	ActorID           string
	TriggerMessageIDs []string
	EnqueuedAt        time.Time
	ReadyAt           time.Time

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
type MergeOptions struct {
	Debounce, MaxMergeWindow time.Duration
	Addressed                bool
}

func (m MergeOptions) orConfig(cfg SessionConfig) MergeOptions {
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

// AdmitPlan tells persist what Admit is about to do. Both fields empty means
// the message is only observed.
type AdmitPlan struct {
	NewTaskID       string
	MergeIntoTaskID string
}

type AdmitResult struct {
	TaskID  string
	Merged  bool
	Created bool
}

// Scheduler runs tasks per conversation: FIFO, with merge windows, a
// concurrency limit, and a minimum interval between a task finishing and the
// next one starting. Each active conversation has its own goroutine, so
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
// the same actor has not reached its ReadyAt; a merge keeps the merge window
// the task was created with, except as opts.Addressed describes. Creating a
// task when MaxQueued tasks are already waiting returns ErrQueueFull without
// calling persist.
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

	if i := sess.openWindow(actorID, now); i >= 0 {
		t := &sess.pending[i]
		if err := persist(AdmitPlan{MergeIntoTaskID: t.TaskID}); err != nil {
			return AdmitResult{}, err
		}
		t.TriggerMessageIDs = append(t.TriggerMessageIDs, messageID)
		if m := t.merge.orConfig(cfg); opts.Addressed && m.Debounce > cfg.Debounce {
			t.ReadyAt = earliest(t.ReadyAt, now.Add(cfg.Debounce))
		} else {
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
	m.Addressed = false
	t := QueuedTask{
		TaskID:            ids.New("task"),
		ConversationKey:   convKey,
		ActorID:           actorID,
		TriggerMessageIDs: []string{messageID},
		EnqueuedAt:        now,
		ReadyAt:           now.Add(m.Debounce),
		merge:             m,
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
// order, then queued ones in FIFO order.
func (s *Scheduler) Active(convKey string) []QueuedTask {
	sess := s.lockSession(convKey, false)
	if sess == nil {
		return nil
	}
	defer sess.mu.Unlock()
	out := make([]QueuedTask, 0, len(sess.running)+len(sess.pending))
	for _, rt := range sess.running {
		out = append(out, cloneTask(rt.task))
	}
	for _, t := range sess.pending {
		out = append(out, cloneTask(t))
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
		wait, idle := s.dispatchLocked(sess, cfg, time.Now())
		if idle {
			sess.dead = true
		}
		sess.mu.Unlock()
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

// dispatchLocked starts every task that may start now. It returns how long to
// wait before re-evaluating (0: until woken) and whether the session has been
// idle long enough to be reclaimed.
func (s *Scheduler) dispatchLocked(sess *session, cfg SessionConfig, now time.Time) (wait time.Duration, idle bool) {
	for len(sess.pending) > 0 {
		if s.ctx.Err() != nil {
			return 0, false
		}
		head := sess.pending[0]
		if d := head.ReadyAt.Sub(now); d > 0 {
			return d, false
		}
		if len(sess.running) >= cfg.MaxConcurrent {
			return 0, false
		}
		if !sess.lastFinish.IsZero() {
			if d := sess.lastFinish.Add(cfg.MinReplyInterval).Sub(now); d > 0 {
				return d, false
			}
		}
		sess.pending = slices.Delete(sess.pending, 0, 1)
		s.startLocked(sess, head)
	}
	if len(sess.running) > 0 {
		return 0, false
	}
	if d := sess.lastActivity.Add(s.idle).Sub(now); d > 0 {
		return d, false
	}
	return 0, true
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
// whose merge window is still open at now, or -1.
func (sess *session) openWindow(actorID string, now time.Time) int {
	for i := len(sess.pending) - 1; i >= 0; i-- {
		if t := sess.pending[i]; t.ActorID == actorID && now.Before(t.ReadyAt) {
			return i
		}
	}
	return -1
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
