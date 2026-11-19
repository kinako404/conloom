package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/contextbuilder"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// statePassTimeout bounds a state step, from its start to its last call.
	statePassTimeout = 90 * time.Second
	// stateMaxTurns is how many not-yet-covered turns of one speaker in one
	// conversation a state step covers at most; older ones are dropped.
	stateMaxTurns = 8

	// copyStateStep is the state step's instruction, followed by the replies
	// it judges (see stateDirective).
	copyStateStep       = "【状态更新】这一步不是回复，不会发给对方。根据最近这一轮（上面的当前消息和你下面的回复）判断你的状态、场景或要记住的事有没有变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。"
	stateRepliesHeading = "你刚才的回复："
)

// Outcomes of a state step, logged as outcome.
const (
	stateOutcomeDone        = "done"
	stateOutcomeCancelled   = "cancelled"
	stateOutcomeForgotten   = "forgotten"
	stateOutcomeNoRoom      = "no_room"
	stateOutcomeUnavailable = "unavailable"
	stateOutcomeFailed      = "failed"
)

var (
	// errStateForgotten stops a state step whose speaker was forgotten since
	// its first turn: none of its calls run any more.
	errStateForgotten = errors.New("core: speaker forgotten since the turn")
	// errStateNoRoom stops a state step whose profile has no room for a tool
	// round.
	errStateNoRoom = errors.New("core: no room for a tool round")
)

// stateTurn is a reply whose state changes a state step judges, snapshotted
// when the reply was accepted.
type stateTurn struct {
	task  store.TaskRecord      // the reply task
	bot   config.BotConfig      // its bot
	doc   conloomapi.PersonaDoc // persona of the reply (state block); zero when it failed to load
	tools skills.Toolset        // skillRun.state
	inv   skills.Invocation     // the reply run's invocation
	in    contextbuilder.Input  // the reply's context without skills
	req   router.Request        // the reply's routing request, Exclude cleared
	reply string                // the parts sent, joined by "\n"
	wake  bool                  // a turn the bot started itself
	at    time.Time             // CreatedAt of its first trigger, or the task's without triggers
}

// stateKey is whose state steps coalesce: the memory scope
// (memory.ScopeKeyOf of the conversation) and the speaker
// (task.ActorPlatformUserID).
type stateKey struct{ scope, speaker string }

// stateEntry is the not-yet-covered turns of one speaker in one
// conversation, oldest first.
type stateEntry struct {
	key     stateKey
	convKey string
	turns   []stateTurn
}

// stateStep is a running state step over entry. called is set once it began
// a tool round; dropped once it was cancelled and its turns carried back or
// dropped, so that is done only once; done closes when it returned.
type stateStep struct {
	entry   *stateEntry
	ctx     context.Context
	cancel  context.CancelFunc
	called  atomic.Bool
	dropped bool
	done    chan struct{}
}

// stateSteps keeps at most one running step per memory scope; pending
// entries, FIFO, one per (key, convKey); holds counting the reply tasks
// running per key, which keep that key's entries from starting; held marking
// the reply tasks holding a key, true once their turn was abandoned.
type stateSteps struct {
	mu      sync.Mutex
	closed  bool
	wg      sync.WaitGroup
	running map[string]*stateStep
	pending []*stateEntry
	holds   map[stateKey]int
	held    map[string]bool
}

func newStateSteps() *stateSteps {
	return &stateSteps{running: map[string]*stateStep{}, holds: map[stateKey]int{}, held: map[string]bool{}}
}

// idle reports whether no state step runs or is pending and no reply task
// holds one back.
func (s *stateSteps) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) == 0 && len(s.pending) == 0 && len(s.holds) == 0
}

// close cancels the running steps, drops the pending ones and refuses new
// ones; wg then waits for the running ones.
func (s *stateSteps) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, st := range s.running {
		st.dropped = true
		st.cancel()
	}
	s.pending = nil
}

func stateKeyOf(task store.TaskRecord) stateKey {
	return stateKey{scope: memory.ScopeKeyOf(task.ConversationKey), speaker: task.ActorPlatformUserID}
}

// entryLocked returns the pending entry of (key, convKey), or nil.
func (s *stateSteps) entryLocked(key stateKey, convKey string) *stateEntry {
	for _, e := range s.pending {
		if e.key == key && e.convKey == convKey {
			return e
		}
	}
	return nil
}

// stopLocked cancels st; unless it began a tool round its turns other than
// those of the task drop (if set) go back to the front of their pending
// entry, which is created at the front of the queue if there is none.
func (s *stateSteps) stopLocked(st *stateStep, drop string) {
	st.cancel()
	if st.dropped {
		return
	}
	st.dropped = true
	if st.called.Load() || s.closed {
		return
	}
	turns := slices.DeleteFunc(slices.Clone(st.entry.turns), func(t stateTurn) bool { return t.task.ID == drop })
	if len(turns) == 0 {
		return
	}
	if e := s.entryLocked(st.entry.key, st.entry.convKey); e != nil {
		e.turns = newestTurns(append(turns, e.turns...))
		return
	}
	s.pending = slices.Insert(s.pending, 0, &stateEntry{key: st.entry.key, convKey: st.entry.convKey, turns: turns})
}

// newestTurns keeps the newest stateMaxTurns of turns.
func newestTurns(turns []stateTurn) []stateTurn {
	if n := len(turns); n > stateMaxTurns {
		return slices.Clone(turns[n-stateMaxTurns:])
	}
	return turns
}

// holdState marks task's key busy and cancels its running step, whose turns
// go back to the front of their pending entry unless it already began a tool
// round. release adds turn (nil: none) to its entry (merging, keeping the
// newest stateMaxTurns), drops the hold and starts what may run; a turn whose
// reply was abandoned meanwhile (abandonStateTurn) is not added. Only the
// first call of release counts.
func (o *Orchestrator) holdState(task store.TaskRecord) (release func(turn *stateTurn)) {
	s := o.states
	key := stateKeyOf(task)
	s.mu.Lock()
	s.holds[key]++
	s.held[task.ID] = false
	if st := s.running[key.scope]; st != nil && st.entry.key == key {
		s.stopLocked(st, "")
	}
	s.mu.Unlock()
	var once sync.Once
	return func(turn *stateTurn) {
		once.Do(func() {
			s.mu.Lock()
			if s.holds[key]--; s.holds[key] <= 0 {
				delete(s.holds, key)
			}
			abandoned := s.held[task.ID]
			delete(s.held, task.ID)
			if turn != nil && !abandoned && !s.closed {
				if e := s.entryLocked(key, task.ConversationKey); e != nil {
					e.turns = newestTurns(append(e.turns, *turn))
				} else {
					s.pending = append(s.pending, &stateEntry{key: key, convKey: task.ConversationKey, turns: []stateTurn{*turn}})
				}
			}
			s.mu.Unlock()
			o.kickStateSteps()
		})
	}
}

// abandonStateTurn drops taskID's turn from pending; a running step covering
// it is cancelled and its other turns carried back (unless it began a tool
// round). A turn not added yet, its task still holding its key, is not added.
func (o *Orchestrator) abandonStateTurn(taskID string) {
	s := o.states
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.held[taskID]; ok {
		s.held[taskID] = true
	}
	covers := func(turns []stateTurn) bool {
		return slices.ContainsFunc(turns, func(t stateTurn) bool { return t.task.ID == taskID })
	}
	for _, st := range s.running {
		if covers(st.entry.turns) {
			s.stopLocked(st, taskID)
		}
	}
	for _, e := range s.pending {
		e.turns = slices.DeleteFunc(e.turns, func(t stateTurn) bool { return t.task.ID == taskID })
	}
	s.pending = slices.DeleteFunc(s.pending, func(e *stateEntry) bool { return len(e.turns) == 0 })
}

// cancelStateSteps drops convKey's pending turns (only speaker's unless
// speaker is "") and cancels its running step, whose turns are dropped too;
// the channel closes when that step returned, nil when none ran.
func (o *Orchestrator) cancelStateSteps(convKey, speaker string) <-chan struct{} {
	s := o.states
	s.mu.Lock()
	defer s.mu.Unlock()
	match := func(e *stateEntry) bool {
		return e.convKey == convKey && (speaker == "" || e.key.speaker == speaker)
	}
	s.pending = slices.DeleteFunc(s.pending, match)
	st := s.running[memory.ScopeKeyOf(convKey)]
	if st == nil || !match(st.entry) {
		return nil
	}
	st.dropped = true
	st.cancel()
	return st.done
}

// kickStateSteps starts, per idle scope, the first pending entry whose key no
// reply task holds.
func (o *Orchestrator) kickStateSteps() {
	s := o.states
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for i := 0; i < len(s.pending); {
		e := s.pending[i]
		if s.running[e.key.scope] != nil || s.holds[e.key] > 0 {
			i++
			continue
		}
		s.pending = slices.Delete(s.pending, i, i+1)
		ctx, cancel := context.WithTimeout(o.bgCtx, statePassTimeout)
		st := &stateStep{entry: e, ctx: ctx, cancel: cancel, done: make(chan struct{})}
		s.running[e.key.scope] = st
		s.wg.Add(1)
		go o.runStateStep(st)
	}
}

// endStateStep lets st's scope run its next step.
func (o *Orchestrator) endStateStep(st *stateStep) {
	s := o.states
	st.cancel()
	s.mu.Lock()
	if s.running[st.entry.key.scope] == st {
		delete(s.running, st.entry.key.scope)
	}
	s.mu.Unlock()
	close(st.done)
	o.kickStateSteps()
	s.wg.Done()
}

// runStateStep runs the state step st: it offers the newest turn's state
// tools with the invocation of all its turns (stateInvocation) on the context
// of stateInput, routed like that turn's reply, and re-routes once when the
// model fails (the progress is kept, so no call runs twice). It sends
// nothing. It logs "state step" with its outcome.
func (o *Orchestrator) runStateStep(st *stateStep) {
	defer o.endStateStep(st)
	turns := st.entry.turns
	newest := turns[len(turns)-1]
	start := time.Now()
	ctx := usage.WithAttribution(st.ctx, usage.Attribution{
		BotInstanceID: newest.task.BotInstanceID, ConversationKey: newest.task.ConversationKey, TaskID: newest.task.ID,
	})
	task := stateTask(newest.task)
	run := newToolRun(newest.tools, stateInvocation(turns))
	role := ""
	var err error
	defer func() {
		outcome := stateOutcome(ctx, err)
		attrs := []any{"task_id", task.ID, "conversation", task.ConversationKey, "turns", len(turns), "role", role,
			"rounds", run.rounds, "calls", skillCallSummary(run.counts), "duration_ms", time.Since(start).Milliseconds(),
			"outcome", outcome}
		if outcome == stateOutcomeFailed || outcome == stateOutcomeUnavailable {
			attrs = append(attrs, "error", err)
		}
		o.log.Info("state step", attrs...)
	}()
	if err = o.stateForgotten(ctx, task, turns[0].at); err != nil {
		return
	}
	run.deadline = o.now().Add(statePassTimeout)
	in := o.stateInput(ctx, turns)
	req := newest.req
	req.Exclude = nil
	req.BaseTokens = contextbuilder.BaseTokens(withSkills(in, run))
	d, rerr := o.router.Route(req)
	if rerr != nil {
		err = fmt.Errorf("%w: %w", errStateUnrouted, rerr)
		return
	}
	role = string(d.Profile.Role)
	err = o.stateCall(ctx, task, d, in, run)
	if err == nil || ctx.Err() != nil || errors.Is(err, errStateForgotten) || errors.Is(err, errStateNoRoom) {
		return
	}
	o.log.Warn("state step model call failed; re-routing", "task_id", task.ID, "role", d.Profile.Role, "error", err)
	req.Exclude = []router.Role{d.Profile.Role}
	if d, rerr = o.router.Route(req); rerr != nil {
		err = fmt.Errorf("%w: %w", errStateUnrouted, rerr)
		return
	}
	role = string(d.Profile.Role)
	err = o.stateCall(ctx, task, d, in, run)
}

// errStateUnrouted is a state step no profile could take.
var errStateUnrouted = errors.New("core: no profile for the state step")

// stateOutcome is the logged outcome of a state step that ended with err.
func stateOutcome(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return stateOutcomeDone
	case errors.Is(err, errStateForgotten):
		return stateOutcomeForgotten
	case errors.Is(ctx.Err(), context.Canceled):
		return stateOutcomeCancelled
	case errors.Is(err, errStateNoRoom):
		return stateOutcomeNoRoom
	case errors.Is(err, errStateUnrouted):
		return stateOutcomeUnavailable
	}
	return stateOutcomeFailed
}

// stateForgotten returns errStateForgotten when task's speaker was forgotten
// in its memory scope after since, or the error reading that.
func (o *Orchestrator) stateForgotten(ctx context.Context, task store.TaskRecord, since time.Time) error {
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(task.ConversationKey), []string{task.ActorPlatformUserID})
	if err != nil {
		return fmt.Errorf("load member memory: %w", err)
	}
	if since.Before(rows[task.ActorPlatformUserID].ForgottenAt) {
		return errStateForgotten
	}
	return nil
}

// beginStateRound is checked before the state step of task runs the calls of
// a tool round: none run once the speaker was forgotten since the step's
// first turn or ctx ended. Otherwise the running step is marked as having
// called, under the same lock a cancelling reply checks it, so its turns are
// then never carried over to run again.
func (o *Orchestrator) beginStateRound(ctx context.Context, task store.TaskRecord) error {
	s := o.states
	scope := memory.ScopeKeyOf(task.ConversationKey)
	s.mu.Lock()
	st := s.running[scope]
	s.mu.Unlock()
	since := task.CreatedAt
	if st != nil {
		since = st.entry.turns[0].at
	}
	if err := o.stateForgotten(ctx, task, since); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if st != nil {
		st.called.Store(true)
	}
	return nil
}

// stateInvocation is the newest turn's invocation whose TriggerMessageIDs are
// all turns', in order, each once.
func stateInvocation(turns []stateTurn) skills.Invocation {
	inv := turns[len(turns)-1].inv
	var ids []string
	for _, t := range turns {
		for _, id := range t.inv.TriggerMessageIDs {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	inv.TriggerMessageIDs = ids
	return inv
}

// stateDirective is copyStateStep followed by the replies of turns, one
// 「reply」 line each, in order.
func stateDirective(turns []stateTurn) string {
	lines := []string{copyStateStep, "", stateRepliesHeading}
	for _, t := range turns {
		lines = append(lines, "「"+t.reply+"」")
	}
	return strings.Join(lines, "\n")
}

// stateInput is the newest turn's context: Current is all turns' Current
// (each message once, by Seq); History its History before the first of those;
// Directive stateDirective; no appearance image; the state block of a private
// chat read again (o.stateBlock with the turn's persona).
func (o *Orchestrator) stateInput(ctx context.Context, turns []stateTurn) contextbuilder.Input {
	newest := turns[len(turns)-1]
	in := newest.in
	var current []store.MessageRecord
	for _, t := range turns {
		for _, m := range t.in.Current {
			if !slices.ContainsFunc(current, func(c store.MessageRecord) bool { return c.ID == m.ID }) {
				current = append(current, m)
			}
		}
	}
	slices.SortStableFunc(current, func(a, b store.MessageRecord) int { return int(a.Seq - b.Seq) })
	in.Current = current
	if len(current) > 0 {
		first := current[0].Seq
		in.History = slices.DeleteFunc(slices.Clone(in.History), func(m store.MessageRecord) bool { return m.Seq >= first })
	}
	in.Directive = stateDirective(turns)
	in.Image = nil
	if convType(newest.task.ConversationKey) == conloomapi.ConvPrivate {
		in.State = o.stateBlock(ctx, newest.task, newest.bot, newest.doc, o.readScene(ctx, newest.task, newest.bot))
	}
	return in
}

// stateTask is t at background admission priority, so a state step never
// delays a reply.
func stateTask(t store.TaskRecord) store.TaskRecord {
	t.Priority = int(admission.PriorityBackground)
	return t
}

// stateCall runs the tool rounds of a state step on d, continuing from run's
// progress: there is no final request: it stops when lastRequest says so or
// the model calls nothing, its text discarded. The calls run with
// context.WithoutCancel(ctx) (each bounded by toolCallTimeout), so a round
// once begun is never cut halfway, but no round begins after ctx ended or the
// speaker was forgotten since the step's first turn (errStateForgotten, see
// beginStateRound). A tool call written as text or a tool round cut off by
// the output limit is ErrEmptyOutput; no room for a tool round within the
// profile's hard prompt limit is errStateNoRoom. Calls are recorded with
// purpose state.
func (o *Orchestrator) stateCall(ctx context.Context, task store.TaskRecord, d router.Decision, in contextbuilder.Input, run *skillRun) error {
	p := d.Profile
	in.Budget = d.PromptBudget
	in.TokenFactor = p.Config.TokenEstimateFactor
	worst := worstRound(d.MaxOutputTokens, in.TokenFactor, run.maxResult)
	with := withSkills(in, run)
	room := d.MaxPromptTokens - worst
	if room-contextbuilder.BaseTokens(with) < minToolHistoryTokens {
		o.log.Warn("no room for a tool round; no state step", "task_id", task.ID, "role", p.Role,
			"max_prompt_tokens", d.MaxPromptTokens, "tool_round_tokens", worst)
		return errStateNoRoom
	}
	in = with
	in.Budget = min(d.PromptBudget, room)
	built := contextbuilder.Build(in)
	msgs := built.Messages
	if !slices.Contains(p.Config.Modalities, "image") {
		msgs = contextbuilder.WithoutImage(msgs)
	}
	req := openai.ChatRequest{Messages: append(slices.Clip(msgs), run.msgs...), MaxTokens: d.MaxOutputTokens, Tools: run.tools}
	estimate := built.EstimatedTokens + estimateMessages(run.msgs, in.TokenFactor)
	for !o.lastRequest(run, estimate, worst, d) {
		resp, err := o.chat(ctx, task, d, req, built, estimate, store.PurposeState, []any{"round", run.rounds, "purpose", store.PurposeState})
		if err != nil {
			return err
		}
		if reason := notAnAnswer(resp, false); reason != "" {
			o.log.Warn("state step output rejected", "task_id", task.ID, "role", p.Role, "round", run.rounds, "reason", reason)
			return fmt.Errorf("model %s: %w: %s", p.Role, openai.ErrEmptyOutput, reason)
		}
		if len(resp.ToolCalls) == 0 {
			return nil
		}
		if err := o.beginStateRound(ctx, task); err != nil {
			return err
		}
		added := o.toolRound(context.WithoutCancel(ctx), task, run, resp)
		run.msgs = append(run.msgs, added...)
		run.rounds++
		req.Messages = append(slices.Clip(req.Messages), added...)
		estimate += estimateMessages(added, in.TokenFactor)
	}
	return nil
}
