package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	maxBatchesPerRun = 10
	maxBatchMessages = 1000

	consFailureLimit  = 3
	consBackoff       = time.Hour
	consAdmissionWait = 60 * time.Second
	consLeaseSlack    = 10 * time.Second
)

// Failure categories stored as GroupMemory.LastError.
const (
	consErrAdmission = "admission_error"
	consErrModel     = "model_error"
	consErrParse     = "parse_error"
)

// ConsolidatorDeps are a Consolidator's collaborators. A nil Metrics, Logger
// or Now uses a private registry, a discarding logger or time.Now; a nil
// Location is UTC.
type ConsolidatorDeps struct {
	Store     store.Store
	Router    *router.Router
	Admission *admission.Controller
	Personas  *persona.Service
	Policies  *policy.Service
	Config    *config.Config
	Metrics   *obs.Metrics
	Logger    *slog.Logger
	Location  *time.Location
	Now       func() time.Time
}

// Consolidator updates a group's summary, member notes and affinity from its
// new messages with the long-context model. It counts consecutive failures
// per conversation in memory and, after consFailureLimit of them, skips
// unrequested runs for consBackoff.
type Consolidator struct {
	d ConsolidatorDeps

	mu       sync.Mutex
	failures map[string]int
	backoff  map[string]time.Time
}

func NewConsolidator(d ConsolidatorDeps) *Consolidator {
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
	return &Consolidator{d: d, failures: map[string]int{}, backoff: map[string]time.Time{}}
}

// Run consolidates up to maxBatchesPerRun batches of the conversation's
// messages after its cursor, advancing the cursor after each batch is
// written. It skips the run when memory is off, during a failure backoff
// (unless manual), when there is nothing new, when no long-context model is
// available or when admission stays busy. A failed model call or unreadable
// output keeps the cursor, records the failure category and returns an
// error; a cancelled ctx keeps the cursor and is not a failure.
func (c *Consolidator) Run(ctx context.Context, convKey string, manual bool) (Result, error) {
	start := time.Now()
	res, promptTokens, err := c.run(ctx, convKey, manual)
	runs := c.d.Metrics.MemoryConsolidations
	switch {
	case err != nil && ctx.Err() != nil:
	case err != nil:
		runs.WithLabelValues("error").Inc()
	case res.Skipped != "":
		runs.WithLabelValues("skipped").Inc()
	default:
		elapsed := time.Since(start)
		runs.WithLabelValues("ok").Inc()
		c.d.Metrics.MemoryConsolidationSeconds.Observe(elapsed.Seconds())
		c.d.Logger.Info("memory consolidated", "conversation", convKey, "batches", res.Batches, "messages", res.Messages,
			"prompt_tokens", promptTokens, "members_updated", res.MembersUpdated, "duration_ms", elapsed.Milliseconds())
	}
	return res, err
}

func (c *Consolidator) run(ctx context.Context, convKey string, manual bool) (Result, int, error) {
	pv, err := c.d.Policies.Current(ctx, convKey)
	if err != nil {
		return Result{}, 0, err
	}
	mp := pv.Policy.Memory
	if !mp.Enabled || !pv.Policy.Concurrency.AllowLongRequests {
		return Result{Skipped: "disabled"}, 0, nil
	}
	if !manual && c.inBackoff(convKey) {
		return Result{Skipped: "backoff"}, 0, nil
	}
	g, err := c.d.Store.GetGroupMemory(ctx, convKey)
	if errors.Is(err, store.ErrNotFound) {
		g, err = store.GroupMemory{ConversationKey: convKey}, nil
	}
	if err != nil {
		return Result{}, 0, err
	}
	botName, err := c.botName(ctx, convKey)
	if err != nil {
		return Result{}, 0, err
	}

	var res Result
	promptTokens := 0
	for res.Batches < maxBatchesPerRun {
		msgs, err := c.d.Store.MessagesAfter(ctx, convKey, g.CursorSeq, maxBatchMessages)
		if err != nil {
			return res, promptTokens, err
		}
		if len(msgs) == 0 {
			break
		}
		out, err := c.batch(ctx, &g, mp, botName, msgs)
		if err != nil {
			return res, promptTokens, err
		}
		if out.skipped != "" {
			if res.Batches == 0 {
				return Result{Skipped: out.skipped}, 0, nil
			}
			break
		}
		res.Batches++
		res.Messages += out.messages
		res.MembersUpdated += out.membersUpdated
		promptTokens += out.promptTokens
	}
	if res.Batches == 0 {
		return Result{Skipped: "no_messages"}, 0, nil
	}
	c.clearFailures(convKey)
	return res, promptTokens, nil
}

type consOutcome struct {
	skipped                                string
	messages, membersUpdated, promptTokens int
}

// batch consolidates the batch planned from msgs and, once it is written,
// moves *g to the written state.
func (c *Consolidator) batch(ctx context.Context, g *store.GroupMemory, mp conloomapi.MemoryPolicy, botName string, msgs []store.MessageRecord) (consOutcome, error) {
	convKey := g.ConversationKey
	in, err := c.gather(ctx, convKey, msgs)
	if err != nil {
		return consOutcome{}, err
	}
	base := consPromptTokens(buildPrompt(g.Summary, nil, nil), c.d.Config.ModelProfiles[string(router.RoleLong)].TokenEstimateFactor)
	d, err := c.d.Router.Route(router.Request{AllowLong: true, Exclude: []router.Role{router.RoleShort}, BaseTokens: base})
	if err != nil {
		c.d.Logger.Info("memory consolidation skipped: long-context model unavailable", "conversation", convKey, "error", err)
		return consOutcome{skipped: "long_unavailable"}, nil
	}
	p := d.Profile
	in.BotName, in.Location = botName, c.d.Location
	in.BaseTokens, in.Budget, in.TokenFactor = base, d.PromptBudget, p.Config.TokenEstimateFactor
	b := consPlanBatch(in)
	now := c.d.Now()

	if b.MemberMessages == 0 {
		next := *g
		next.CursorSeq, next.LastRunAt, next.LastError = b.LastSeq, now, ""
		if err := c.d.Store.PutGroupMemory(ctx, next); err != nil {
			return consOutcome{}, err
		}
		*g = next
		return consOutcome{messages: b.Messages}, nil
	}

	members := make([]promptMember, len(b.Members))
	for i, m := range b.Members {
		members[i] = consPromptMember(m.Memory, m.Ref, m.Name)
	}
	prompt := buildPrompt(g.Summary, members, b.Lines)
	tokens := consPromptTokens(prompt, p.Config.TokenEstimateFactor) + d.MaxOutputTokens + p.Config.Context.SafetyMarginTokens
	lease, err := c.acquire(ctx, p, convKey, tokens)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return consOutcome{}, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			c.d.Logger.Info("memory consolidation skipped: admission busy", "conversation", convKey)
			return consOutcome{skipped: "busy"}, nil
		}
		return consOutcome{}, c.fail(ctx, *g, consErrAdmission, err)
	}
	resp, err := c.chat(ctx, p, prompt, d.MaxOutputTokens)
	lease.Release()
	if err != nil {
		if ctx.Err() != nil {
			return consOutcome{}, ctx.Err()
		}
		return consOutcome{}, c.fail(ctx, *g, consErrModel, err)
	}
	out, err := parseOutput(resp.Content)
	if err != nil {
		return consOutcome{}, c.fail(ctx, *g, consErrParse, err)
	}

	w, affinityChanged := c.compose(*g, b, out, mp, now)
	conflicts, err := c.d.Store.ApplyMemoryWrite(ctx, w)
	if err != nil {
		return consOutcome{}, err
	}
	if len(conflicts) > 0 {
		c.d.Logger.Info("memory consolidation left members changed during the run", "conversation", convKey,
			"users", strings.Join(conflicts, ","))
	}
	for _, userID := range affinityChanged {
		if !slices.Contains(conflicts, userID) {
			c.d.Metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionConsolidate).Inc()
		}
	}
	*g = *w.Group
	return consOutcome{messages: b.Messages, membersUpdated: len(w.Members) - len(conflicts), promptTokens: resp.PromptTokens}, nil
}

// gather loads what planning msgs needs: reply targets in the conversation,
// mentioned actors' user IDs and names, and the stored memory of every member
// involved.
func (c *Consolidator) gather(ctx context.Context, convKey string, msgs []store.MessageRecord) (consBatchInput, error) {
	in := consBatchInput{ConversationKey: convKey, Messages: msgs}
	var replyIDs, actors, userIDs []string
	seen := map[string]bool{}
	add := func(list *[]string, kind, v string) {
		if v != "" && !seen[kind+v] {
			seen[kind+v] = true
			*list = append(*list, v)
		}
	}
	for _, m := range msgs {
		if !m.FromBot {
			add(&userIDs, "u", m.PlatformUserID)
		}
		add(&replyIDs, "r", m.ReplyTo)
		for _, mention := range m.Mentions {
			if mention.Type == "user" {
				add(&actors, "a", mention.ID)
			}
		}
	}
	if len(replyIDs) > 0 {
		found, err := c.d.Store.GetMessages(ctx, consBotID(convKey), replyIDs)
		if err != nil {
			return consBatchInput{}, err
		}
		in.ReplyTargets = make(map[string]store.MessageRecord, len(found))
		for _, t := range found {
			if t.ConversationKey != convKey {
				continue
			}
			in.ReplyTargets[t.ID] = t
			if !t.FromBot {
				add(&userIDs, "u", t.PlatformUserID)
			}
		}
	}
	if len(actors) > 0 {
		var err error
		if in.MentionUsers, err = c.d.Store.UserIDsForActors(ctx, convKey, actors); err != nil {
			return consBatchInput{}, err
		}
		for _, userID := range in.MentionUsers {
			add(&userIDs, "u", userID)
		}
		if in.MentionNames, err = c.d.Store.LatestDisplayNames(ctx, convKey, actors); err != nil {
			return consBatchInput{}, err
		}
	}
	members, err := c.d.Store.GetMemberMemories(ctx, convKey, userIDs)
	if err != nil {
		return consBatchInput{}, err
	}
	in.Members = members
	return in, nil
}

// compose builds the write of a consolidated batch: the new summary when the
// model gave one, a new note when it gave one, the affinity delta for members
// with an affinity when affinity is on, each member's newest name with its
// other changes, a revision per change, and the cursor moved past the batch.
// It also returns the members whose affinity changed.
func (c *Consolidator) compose(g store.GroupMemory, b consBatch, out modelOutput, mp conloomapi.MemoryPolicy, now time.Time) (store.MemoryWrite, []string) {
	convKey := g.ConversationKey
	w := store.MemoryWrite{ConversationKey: convKey}
	revise := func(userID, field, old, next, reason string) {
		w.Revisions = append(w.Revisions, store.MemoryRevision{ConversationKey: convKey, PlatformUserID: userID,
			Field: field, OldValue: old, NewValue: next, Source: store.RevisionConsolidate, Reason: reason})
	}
	next := g
	next.CursorSeq, next.LastRunAt, next.LastError = b.LastSeq, now, ""
	if out.Summary != "" && out.Summary != g.Summary {
		revise("", store.FieldSummary, g.Summary, out.Summary, "")
		next.Summary = out.Summary
	}
	w.Group = &next

	byRef := make(map[string]consBatchMember, len(b.Members))
	for _, m := range b.Members {
		byRef[m.Ref] = m
	}
	day := LocalDay(now, c.d.Location)
	var affinityChanged []string
	for _, om := range out.Members {
		bm, ok := byRef[om.Ref]
		if !ok {
			continue
		}
		m, changed := bm.Memory, false
		if om.Note != "" && om.Note != m.Note {
			revise(bm.UserID, store.FieldNote, m.Note, om.Note, "")
			m.Note, changed = om.Note, true
		}
		if mp.Affinity && om.Delta != 0 {
			if after, applied := ApplyModelDelta(m, om.Delta, day); applied != 0 {
				revise(bm.UserID, store.FieldAffinity, strconv.Itoa(*m.Affinity), strconv.Itoa(*after.Affinity), om.Reason)
				m, changed = after, true
				affinityChanged = append(affinityChanged, bm.UserID)
			}
		}
		if !changed {
			continue
		}
		if bm.Name != "" {
			m.DisplayName = bm.Name
		}
		w.Members = append(w.Members, m)
	}
	return w, affinityChanged
}

// fail records a failed batch: g is stored with category as its last error
// and the failure counts toward the backoff.
func (c *Consolidator) fail(ctx context.Context, g store.GroupMemory, category string, cause error) error {
	n := c.recordFailure(g.ConversationKey)
	g.LastError = category
	if err := c.d.Store.PutGroupMemory(ctx, g); err != nil {
		c.d.Logger.Warn("record memory consolidation failure failed", "conversation", g.ConversationKey, "error", err)
	}
	c.d.Logger.Warn("memory consolidation failed", "conversation", g.ConversationKey, "category", category,
		"failures", n, "error", cause)
	return fmt.Errorf("memory: consolidate %s: %s: %w", g.ConversationKey, category, cause)
}

func (c *Consolidator) recordFailure(convKey string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[convKey]++
	n := c.failures[convKey]
	if n >= consFailureLimit {
		c.backoff[convKey] = c.d.Now().Add(consBackoff)
	}
	return n
}

func (c *Consolidator) inBackoff(convKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.d.Now().Before(c.backoff[convKey])
}

func (c *Consolidator) clearFailures(convKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failures, convKey)
	delete(c.backoff, convKey)
}

// acquire waits up to consAdmissionWait for a background lease on p.
func (c *Consolidator) acquire(ctx context.Context, p *router.Profile, convKey string, tokens int) (*admission.Lease, error) {
	rt := p.Config.Runtime
	wctx, cancel := context.WithTimeout(ctx, consAdmissionWait)
	defer cancel()
	return c.d.Admission.Acquire(wctx, admission.Request{
		ID:       "memory:" + convKey,
		Profile:  string(p.Role),
		Tokens:   tokens,
		Priority: admission.PriorityBackground,
		TTL:      rt.Timeout.Duration*time.Duration(rt.MaxRetries+1) + consLeaseSlack,
	})
}

// chat calls p's model, reports the outcome to the router (unless ctx ended)
// and records model metrics.
func (c *Consolidator) chat(ctx context.Context, p *router.Profile, msgs []openai.ChatMessage, maxTokens int) (openai.ChatResponse, error) {
	start := time.Now()
	resp, err := p.Client.Chat(ctx, openai.ChatRequest{Messages: msgs, MaxTokens: maxTokens})
	if err != nil {
		if ctx.Err() == nil {
			c.d.Router.ReportFailure(p.Role)
		}
		return openai.ChatResponse{}, err
	}
	c.d.Router.ReportSuccess(p.Role)
	role := string(p.Role)
	c.d.Metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(time.Since(start).Seconds())
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	return resp, nil
}

// botName is the bot's name under the conversation's current persona.
func (c *Consolidator) botName(ctx context.Context, convKey string) (string, error) {
	bot, ok := c.d.Config.Bot(consBotID(convKey))
	if !ok {
		return "", fmt.Errorf("memory: no configured bot for %s", convKey)
	}
	pv, err := c.d.Personas.Current(ctx, convKey, bot.PersonaTemplate)
	if err != nil {
		return "", err
	}
	return persona.BotName(pv.Persona, bot.Name), nil
}

// consBotID is the bot instance part of a "bot:platform:type:id" conversation key.
func consBotID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}
