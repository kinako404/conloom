package core

import (
	"cmp"
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
	// copyStateStepWake replaces copyStateStep when every turn is a wake turn,
	// whose messages follow stateWakeHeading.
	copyStateStepWake = "【状态更新】这一步不是回复，不会发给对方。你刚才主动给对方发了下面的消息，对方还没有回。判断你的状态有没有因此变化：有就调用相应的工具，没有就什么都不调用。不要写回复的文字，写了也不会发出去。"
	stateWakeHeading  = "你刚才发的："
)

// Outcomes of a state step, logged as outcome.
const (
	stateOutcomeDone        = "done"
	stateOutcomeCancelled   = "cancelled"
	stateOutcomeForgotten   = "forgotten"
	stateOutcomeNoRoom      = "no_room"
	stateOutcomeUnavailable = "unavailable"
	stateOutcomeFailed      = "failed"
	stateOutcomeError       = "error"
)

var (
	// errStateForgotten stops a state step whose speaker was forgotten since
	// its first turn: none of its calls run any more.
	errStateForgotten = errors.New("core: speaker forgotten since the turn")
	// errStateNoRoom stops a state step whose profile has no room for a tool
	// round.
	errStateNoRoom = errors.New("core: no room for a tool round")
	// errStateStore stops a state step that could not read the store: no
	// other model would do better, so it is not re-routed.
	errStateStore = errors.New("core: state step store read failed")
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
// dropped is how many turns that entry lost to stateMaxTurns.
func (s *stateSteps) stopLocked(st *stateStep, drop string) (dropped int) {
	st.cancel()
	if st.dropped {
		return 0
	}
	st.dropped = true
	if st.called.Load() || s.closed {
		return 0
	}
	turns := slices.DeleteFunc(slices.Clone(st.entry.turns), func(t stateTurn) bool { return t.task.ID == drop })
	if len(turns) == 0 {
		return 0
	}
	if e := s.entryLocked(st.entry.key, st.entry.convKey); e != nil {
		e.turns, dropped = newestTurns(append(turns, e.turns...))
		return dropped
	}
	s.pending = slices.Insert(s.pending, 0, &stateEntry{key: st.entry.key, convKey: st.entry.convKey, turns: turns})
	return 0
}

// newestTurns keeps the newest stateMaxTurns of turns; dropped is how many
// older ones it left out.
func newestTurns(turns []stateTurn) (kept []stateTurn, dropped int) {
	n := len(turns)
	if n <= stateMaxTurns {
		return turns, 0
	}
	return slices.Clone(turns[n-stateMaxTurns:]), n - stateMaxTurns
}

// logDroppedTurns logs that convKey's pending state turns lost dropped turns
// to stateMaxTurns, if any.
func (o *Orchestrator) logDroppedTurns(convKey string, dropped int) {
	if dropped > 0 {
		o.log.Info("state turns dropped", "conversation", convKey, "dropped", dropped)
	}
}

// holdState marks task's key busy, so its pending entries wait; its running
// step goes on (a reply that may have a state turn cancels it, see
// preemptState). release adds turn (nil: none) to its entry (merging, keeping
// the newest stateMaxTurns), drops the hold and starts what may run; a turn
// whose reply was abandoned meanwhile (abandonStateTurn) is not added. Only
// the first call of release counts.
func (o *Orchestrator) holdState(task store.TaskRecord) (release func(turn *stateTurn)) {
	s := o.states
	key := stateKeyOf(task)
	s.mu.Lock()
	s.holds[key]++
	s.held[task.ID] = false
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
			dropped := 0
			if turn != nil && !abandoned && !s.closed {
				if e := s.entryLocked(key, task.ConversationKey); e != nil {
					e.turns, dropped = newestTurns(append(e.turns, *turn))
				} else {
					s.pending = append(s.pending, &stateEntry{key: key, convKey: task.ConversationKey, turns: []stateTurn{*turn}})
				}
			}
			s.mu.Unlock()
			o.logDroppedTurns(task.ConversationKey, dropped)
			o.kickStateSteps()
		})
	}
}

// preemptState cancels the running step of task's key, as a reply task that
// holds it (holdState) and may have a state turn does: unless the step already
// began a tool round, its turns go back to the front of their pending entry,
// to be judged with the reply's turn.
func (o *Orchestrator) preemptState(task store.TaskRecord) {
	s := o.states
	key := stateKeyOf(task)
	s.mu.Lock()
	dropped := 0
	if st := s.running[key.scope]; st != nil && st.entry.key == key {
		dropped = s.stopLocked(st, "")
	}
	s.mu.Unlock()
	o.logDroppedTurns(task.ConversationKey, dropped)
}

// abandonStateTurn drops taskID's turn from pending; a running step covering
// it is cancelled and its other turns carried back (unless it began a tool
// round). A turn not added yet, its task still holding its key, is not added.
func (o *Orchestrator) abandonStateTurn(taskID string) {
	s := o.states
	s.mu.Lock()
	if _, ok := s.held[taskID]; ok {
		s.held[taskID] = true
	}
	covers := func(turns []stateTurn) bool {
		return slices.ContainsFunc(turns, func(t stateTurn) bool { return t.task.ID == taskID })
	}
	type drop struct {
		convKey string
		n       int
	}
	var drops []drop
	for _, st := range s.running {
		if covers(st.entry.turns) {
			drops = append(drops, drop{st.entry.convKey, s.stopLocked(st, taskID)})
		}
	}
	for _, e := range s.pending {
		e.turns = slices.DeleteFunc(e.turns, func(t stateTurn) bool { return t.task.ID == taskID })
	}
	s.pending = slices.DeleteFunc(s.pending, func(e *stateEntry) bool { return len(e.turns) == 0 })
	s.mu.Unlock()
	for _, d := range drops {
		o.logDroppedTurns(d.convKey, d.n)
	}
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

// runStateStep runs the state step st over its turns not forgotten since
// (stateUnforgotten): it offers stateTools of those turns with the invocation
// of all of them (stateInvocation) on the context of stateInput, routed like
// the newest one's reply, and re-routes once when the model fails (the
// progress is kept, so no call runs twice); not when the store fails. It
// sends nothing. It logs "state step" with its outcome, as a warning when
// the store failed.
func (o *Orchestrator) runStateStep(st *stateStep) {
	defer o.endStateStep(st)
	start := time.Now()
	turns := st.entry.turns
	task := stateTask(turns[len(turns)-1].task)
	var run *skillRun
	role := ""
	var err error
	defer func() {
		outcome := stateOutcome(st.ctx, err)
		rounds, calls := 0, ""
		if run != nil {
			rounds, calls = run.rounds, skillCallSummary(run.counts)
		}
		attrs := []any{"task_id", task.ID, "conversation", task.ConversationKey, "turns", len(turns), "role", role,
			"rounds", rounds, "calls", calls, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome}
		switch outcome {
		case stateOutcomeError:
			o.log.Warn("state step", append(attrs, "error", err)...)
		case stateOutcomeFailed, stateOutcomeUnavailable:
			o.log.Info("state step", append(attrs, "error", err)...)
		default:
			o.log.Info("state step", attrs...)
		}
	}()
	kept, err := o.stateUnforgotten(st)
	if err != nil {
		return
	}
	turns = kept
	newest := turns[len(turns)-1]
	task = stateTask(newest.task)
	ctx := usage.WithAttribution(st.ctx, usage.Attribution{
		BotInstanceID: task.BotInstanceID, ConversationKey: task.ConversationKey, TaskID: task.ID,
	})
	run = newToolRun(stateTools(turns), stateInvocation(turns))
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
	err = o.stateCall(ctx, st, task, d, in, run)
	if err == nil || ctx.Err() != nil || errors.Is(err, errStateForgotten) || errors.Is(err, errStateNoRoom) || errors.Is(err, errStateStore) {
		return
	}
	o.log.Warn("state step model call failed; re-routing", "task_id", task.ID, "role", d.Profile.Role, "error", err)
	req.Exclude = []router.Role{d.Profile.Role}
	if d, rerr = o.router.Route(req); rerr != nil {
		err = fmt.Errorf("%w: %w", errStateUnrouted, rerr)
		return
	}
	role = string(d.Profile.Role)
	err = o.stateCall(ctx, st, task, d, in, run)
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
	case errors.Is(err, errStateStore):
		return stateOutcomeError
	}
	return stateOutcomeFailed
}

// stateForgottenAt returns when task's speaker was last forgotten in its
// memory scope, zero if never; an error reading it wraps errStateStore.
func (o *Orchestrator) stateForgottenAt(ctx context.Context, task store.TaskRecord) (time.Time, error) {
	rows, err := o.st.GetMemberMemories(ctx, memory.ScopeKeyOf(task.ConversationKey), []string{task.ActorPlatformUserID})
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: load member memory: %w", errStateStore, err)
	}
	return rows[task.ActorPlatformUserID].ForgottenAt, nil
}

// stateUnforgotten drops from the state step st the turns from before their
// speaker was last forgotten (by stateTurn.at), so a forget through the API
// or the panel between its turns keeps the later ones, and returns the turns
// left: errStateForgotten when none is, an errStateStore error when the
// forget cannot be read.
func (o *Orchestrator) stateUnforgotten(st *stateStep) ([]stateTurn, error) {
	turns := st.entry.turns
	forgotten, err := o.stateForgottenAt(st.ctx, turns[0].task)
	if err != nil {
		return nil, err
	}
	kept := slices.DeleteFunc(slices.Clone(turns), func(t stateTurn) bool { return t.at.Before(forgotten) })
	if len(kept) == 0 {
		return nil, errStateForgotten
	}
	if len(kept) < len(turns) {
		o.states.mu.Lock()
		st.entry.turns = kept
		o.states.mu.Unlock()
	}
	return kept, nil
}

// beginStateRound is checked before the state step st of task runs the calls
// of a tool round: none run once the speaker was forgotten since the step's
// first turn or ctx ended. Otherwise st is marked as having called, under the
// same lock a cancelling reply checks it, so its turns are then never carried
// over to run again.
func (o *Orchestrator) beginStateRound(ctx context.Context, st *stateStep, task store.TaskRecord) error {
	s := o.states
	forgotten, err := o.stateForgottenAt(ctx, task)
	if err != nil {
		return err
	}
	if st.entry.turns[0].at.Before(forgotten) {
		return errStateForgotten
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	st.called.Store(true)
	return nil
}

// stateTools is the state tools of the newest turn that is not a wake turn,
// or of the newest turn when all are: a wake turn's wakeTools never narrow
// the tools of a reply it is coalesced with.
func stateTools(turns []stateTurn) skills.Toolset {
	for _, t := range slices.Backward(turns) {
		if !t.wake {
			return t.tools
		}
	}
	return turns[len(turns)-1].tools
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
// 「reply」 line each, in order; copyStateStepWake and stateWakeHeading
// instead when every turn is a wake turn.
func stateDirective(turns []stateTurn) string {
	lines := []string{copyStateStep, "", stateRepliesHeading}
	if !slices.ContainsFunc(turns, func(t stateTurn) bool { return !t.wake }) {
		lines = []string{copyStateStepWake, "", stateWakeHeading}
	}
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
	slices.SortStableFunc(current, func(a, b store.MessageRecord) int { return cmp.Compare(a.Seq, b.Seq) })
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

// stateCall runs the tool rounds of the state step st on d, continuing from run's
// progress: there is no final request: it stops when lastRequest says so or
// the model calls nothing, its text discarded. The calls run with
// context.WithoutCancel(ctx) (each bounded by toolCallTimeout), so a round
// once begun is never cut halfway, but no round begins after ctx ended or the
// speaker was forgotten since the step's first turn (errStateForgotten, see
// beginStateRound). A tool call written as text or a tool round cut off by
// the output limit is ErrEmptyOutput; no room for a tool round within the
// profile's hard prompt limit is errStateNoRoom. Calls are recorded with
// purpose state.
func (o *Orchestrator) stateCall(ctx context.Context, st *stateStep, task store.TaskRecord, d router.Decision, in contextbuilder.Input, run *skillRun) error {
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
		if err := o.beginStateRound(ctx, st, task); err != nil {
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
