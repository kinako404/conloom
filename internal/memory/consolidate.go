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

	consMinBatchMessages = 20
	consMinBatchMembers  = 1

	consFailureLimit  = 3
	consBackoff       = time.Hour
	consAdmissionWait = 60 * time.Second
	consLeaseSlack    = 10 * time.Second
)

// A batch's output is planned as consOutputBase tokens for the summary plus
// consOutputPerMember per member, within consOutputShare of the long model's
// reserved output.
const (
	consOutputShare     = 0.7
	consOutputBase      = 800
	consOutputPerMember = 260
)

// Failure categories stored as GroupMemory.LastError.
const (
	consErrAdmission = "admission_error"
	consErrModel     = "model_error"
	consErrParse     = "parse_error"
	consErrTruncated = "output_truncated"
)

// consMemberCap is how many members a batch may show for a model that may
// output outputBudget tokens: at least 1.
func consMemberCap(outputBudget int) int {
	return max(consMinBatchMembers, (int(float64(outputBudget)*consOutputShare)-consOutputBase)/consOutputPerMember)
}

// consLimits are a conversation's reduced batch limits after truncated or
// timed-out model calls; zero fields mean the defaults.
type consLimits struct {
	messages, members int
}

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
// unrequested runs for consBackoff. A truncated or timed-out model call
// halves the conversation's next batch limits (messages and members, down to
// consMinBatchMessages and consMinBatchMembers); each written batch doubles
// them back towards the defaults.
type Consolidator struct {
	d             ConsolidatorDeps
	admissionWait time.Duration

	mu       sync.Mutex
	failures map[string]int
	backoff  map[string]time.Time
	limits   map[string]consLimits
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
	return &Consolidator{d: d, admissionWait: consAdmissionWait,
		failures: map[string]int{}, backoff: map[string]time.Time{}, limits: map[string]consLimits{}}
}

// Run consolidates up to maxBatchesPerRun batches of the conversation's
// messages after its cursor, advancing the cursor after each batch is
// written. It skips the run when memory is off, during a failure backoff
// (unless manual), when there is nothing new, when no long-context model is
// available or when admission stays busy. Batches are limited by the long
// model's input budget and, through consMemberCap, by its reserved output. A
// failed model call or truncated or unreadable output keeps the cursor,
// records the failure category and returns an error; a cancelled ctx keeps
// the cursor and is not a failure.
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
	runStart := c.d.Now()
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
		msgs, err := c.d.Store.MessagesAfter(ctx, convKey, g.CursorSeq, c.messageLimit(convKey))
		if err != nil {
			return res, promptTokens, err
		}
		if len(msgs) == 0 {
			break
		}
		out, err := c.batch(ctx, &g, mp, botName, msgs, runStart)
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
// moves *g to the written state. Writes that are not meant to change the
// summary keep the stored one. In a private conversation, whose summary is
// about its one member, the summary is read afresh for every batch and is not
// written when that member was forgotten since runStart or while the model
// was called (see store.MemoryWrite's SummaryGuards).
func (c *Consolidator) batch(ctx context.Context, g *store.GroupMemory, mp conloomapi.MemoryPolicy, botName string,
	msgs []store.MessageRecord, runStart time.Time) (consOutcome, error) {
	convKey := g.ConversationKey
	private := convTypeOf(convKey) == conloomapi.ConvPrivate
	if private {
		if err := c.refreshSummary(ctx, g); err != nil {
			return consOutcome{}, err
		}
	}
	in, err := c.gather(ctx, convKey, msgs)
	if err != nil {
		return consOutcome{}, err
	}
	style := consStyleFor(convKey)
	base := consPromptTokens(buildPrompt(style.system, g.Summary, nil, nil), c.d.Config.ModelProfiles[string(router.RoleLong)].TokenEstimateFactor)
	d, err := c.d.Router.Route(router.Request{AllowLong: true, Exclude: []router.Role{router.RoleShort}, BaseTokens: base})
	if err != nil {
		c.d.Logger.Info("memory consolidation skipped: long-context model unavailable", "conversation", convKey, "error", err)
		return consOutcome{skipped: "long_unavailable"}, nil
	}
	p := d.Profile
	in.BotName, in.Location = botName, c.d.Location
	in.BaseTokens, in.Budget, in.TokenFactor = base, d.PromptBudget, p.Config.TokenEstimateFactor
	memberCap := consMemberCap(d.MaxOutputTokens)
	in.MaxMembers = c.memberLimit(convKey, memberCap)
	b := consPlanBatch(in)
	now := c.d.Now()

	if b.MemberMessages == 0 {
		next := *g
		next.CursorSeq, next.LastRunAt, next.LastError = b.LastSeq, now, ""
		if err := c.putGroupState(ctx, next); err != nil {
			return consOutcome{}, err
		}
		*g = next
		return consOutcome{messages: b.Messages}, nil
	}

	members := make([]promptMember, len(b.Members))
	for i, m := range b.Members {
		members[i] = consPromptMember(m.Memory, m.Ref, m.Name)
	}
	prompt := buildPrompt(style.system, g.Summary, members, b.Lines)
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
		return consOutcome{}, c.fail(ctx, *g, consErrAdmission, err, openai.ChatResponse{})
	}
	resp, err := c.chat(ctx, p, prompt, d.MaxOutputTokens)
	lease.Release()
	if err != nil {
		if ctx.Err() != nil {
			return consOutcome{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			c.shrink(convKey, b, memberCap)
		}
		return consOutcome{}, c.fail(ctx, *g, consErrModel, err, resp)
	}
	if resp.FinishReason == "length" {
		c.shrink(convKey, b, memberCap)
		return consOutcome{}, c.fail(ctx, *g, consErrTruncated, errors.New("model output reached its token limit"), resp)
	}
	out, err := parseOutput(resp.Content)
	if err != nil {
		return consOutcome{}, c.fail(ctx, *g, consErrParse, err, resp)
	}
	names := make(map[string]string, len(members))
	written := []string{g.Summary}
	for _, m := range members {
		names[m.Ref] = m.Name
		written = append(written, m.Name, m.Note)
	}
	for _, m := range msgs[:b.Messages] {
		written = append(written, m.Text)
	}
	for _, t := range in.ReplyTargets {
		written = append(written, t.Text)
	}
	out = consResolveRefs(out, names, consLiteralCodes(written...), style.summaryMax, style.noteMax)

	keepSummary := false
	if private {
		for _, m := range b.Members {
			keepSummary = keepSummary || !m.Memory.ForgottenAt.IsZero() && !m.Memory.ForgottenAt.Before(runStart)
		}
	}
	day := LocalDay(now, c.d.Location)
	w, entries := c.compose(*g, b, out, mp.Affinity, day, now, keepSummary)
	if private {
		for _, m := range b.Members {
			w.SummaryGuards = append(w.SummaryGuards, store.MemberVersion{PlatformUserID: m.UserID, Version: m.Memory.Version})
		}
	}
	conflicts, err := c.d.Store.ApplyMemoryWrite(ctx, w)
	if err != nil {
		return consOutcome{}, err
	}
	*g = *w.Group
	if private {
		if err := c.refreshSummary(ctx, g); err != nil {
			return consOutcome{}, err
		}
	}
	c.grow(convKey, memberCap)
	updated := c.settle(ctx, convKey, entries, conflicts, mp.Affinity, day)
	return consOutcome{messages: b.Messages, membersUpdated: updated, promptTokens: resp.PromptTokens}, nil
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

// consEntry is the model's entry for a batch member whose row the batch
// writes, and whether it changed the member's affinity.
type consEntry struct {
	member          consBatchMember
	output          modelMember
	affinityChanged bool
}

// consApplyEntry applies the model's entry to m: a non-empty new note, the
// affinity delta (on day) when affinityOn and m has an affinity, and name
// along with either. It reports whether the note and the affinity changed.
func consApplyEntry(m *store.MemberMemory, name string, om modelMember, affinityOn bool, day string) (note, affinity bool) {
	if om.Note != "" && om.Note != m.Note {
		m.Note, note = om.Note, true
	}
	if affinityOn && om.Delta != 0 {
		if after, applied := ApplyModelDelta(*m, om.Delta, day); applied != 0 {
			*m, affinity = after, true
		}
	}
	if (note || affinity) && name != "" {
		m.DisplayName = name
	}
	return note, affinity
}

// compose builds the write of a consolidated batch: the new summary when the
// model gave one, each member's entry applied to the row the batch read (see
// consApplyEntry), a revision per change, and the cursor moved past the
// batch. It also returns the entries of the members it writes.
func (c *Consolidator) compose(g store.GroupMemory, b consBatch, out modelOutput, affinityOn bool, day string, now time.Time,
	keepSummary bool) (store.MemoryWrite, []consEntry) {
	convKey := g.ConversationKey
	w := store.MemoryWrite{ConversationKey: convKey}
	revise := func(userID, field, old, next, reason string) {
		w.Revisions = append(w.Revisions, store.MemoryRevision{ConversationKey: convKey, PlatformUserID: userID,
			Field: field, OldValue: old, NewValue: next, Source: store.RevisionConsolidate, Reason: reason})
	}
	next := g
	next.CursorSeq, next.LastRunAt, next.LastError = b.LastSeq, now, ""
	if !keepSummary && out.Summary != "" && out.Summary != g.Summary {
		revise("", store.FieldSummary, g.Summary, out.Summary, "")
		next.Summary = out.Summary
	}
	w.Group = &next

	byRef := make(map[string]consBatchMember, len(b.Members))
	for _, m := range b.Members {
		byRef[m.Ref] = m
	}
	var entries []consEntry
	for _, om := range out.Members {
		bm, ok := byRef[om.Ref]
		if !ok {
			continue
		}
		m := bm.Memory
		note, affinity := consApplyEntry(&m, bm.Name, om, affinityOn, day)
		if note {
			revise(bm.UserID, store.FieldNote, bm.Memory.Note, m.Note, "")
		}
		if affinity {
			revise(bm.UserID, store.FieldAffinity, strconv.Itoa(*bm.Memory.Affinity), strconv.Itoa(*m.Affinity), om.Reason)
		}
		if note || affinity {
			w.Members = append(w.Members, m)
			entries = append(entries, consEntry{member: bm, output: om, affinityChanged: affinity})
		}
	}
	return w, entries
}

// settle counts the members a batch wrote and re-applies the entries of those
// whose rows changed during the model call (conflicts), but only when their
// note and ForgottenAt are still what the batch read, so a rule's change to
// affinity, day or name is kept while /忘记我 or an edited note wins. It
// returns how many members were updated.
func (c *Consolidator) settle(ctx context.Context, convKey string, entries []consEntry, conflicts []string, affinityOn bool, day string) int {
	updated := 0
	var skipped []string
	for _, e := range entries {
		userID, affinity := e.member.UserID, e.affinityChanged
		if slices.Contains(conflicts, userID) {
			read := e.member.Memory
			u, err := UpdateMember(ctx, c.d.Store, convKey, userID, store.RevisionConsolidate, func(m *store.MemberMemory) (string, bool) {
				if m.Note != read.Note || !m.ForgottenAt.Equal(read.ForgottenAt) {
					return "", false
				}
				note, affinity := consApplyEntry(m, e.member.Name, e.output, affinityOn, day)
				return e.output.Reason, note || affinity
			})
			if err != nil {
				c.d.Logger.Warn("re-applying consolidation to a changed member failed", "conversation", convKey,
					"user", userID, "error", err)
				continue
			}
			if !u.Written {
				skipped = append(skipped, userID)
				continue
			}
			affinity = u.AffinityChanged()
		}
		updated++
		if affinity {
			c.d.Metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionConsolidate).Inc()
		}
	}
	if len(skipped) > 0 {
		c.d.Logger.Info("memory consolidation left members changed during the run", "conversation", convKey,
			"users", strings.Join(skipped, ","))
	}
	return updated
}

// fail records a failed batch: g is stored with category as its last error
// and the failure counts toward the backoff. resp is the model's response,
// if any.
func (c *Consolidator) fail(ctx context.Context, g store.GroupMemory, category string, cause error, resp openai.ChatResponse) error {
	n := c.recordFailure(g.ConversationKey)
	g.LastError = category
	if err := c.putGroupState(ctx, g); err != nil {
		c.d.Logger.Warn("record memory consolidation failure failed", "conversation", g.ConversationKey, "error", err)
	}
	c.d.Logger.Warn("memory consolidation failed", "conversation", g.ConversationKey, "category", category,
		"failures", n, "finish_reason", resp.FinishReason, "completion_tokens", resp.CompletionTokens, "error", cause)
	return fmt.Errorf("memory: consolidate %s: %s: %w", g.ConversationKey, category, cause)
}

// putGroupState stores g's cursor, run time and error, keeping the stored
// summary.
func (c *Consolidator) putGroupState(ctx context.Context, g store.GroupMemory) error {
	_, err := c.d.Store.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: g.ConversationKey, Group: &g, KeepSummary: true})
	return err
}

// refreshSummary sets g's summary to the stored one (empty without a row).
func (c *Consolidator) refreshSummary(ctx context.Context, g *store.GroupMemory) error {
	stored, err := c.d.Store.GetGroupMemory(ctx, g.ConversationKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		g.Summary = ""
	case err != nil:
		return err
	default:
		g.Summary = stored.Summary
	}
	return nil
}

func (c *Consolidator) messageLimit(convKey string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.limits[convKey].messages; l > 0 {
		return l
	}
	return maxBatchMessages
}

func (c *Consolidator) memberLimit(convKey string, memberCap int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l := c.limits[convKey].members; l > 0 && l < memberCap {
		return l
	}
	return memberCap
}

// shrink halves the conversation's batch limits below the size of b, which
// failed, down to consMinBatchMessages and consMinBatchMembers.
func (c *Consolidator) shrink(convKey string, b consBatch, memberCap int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.limits[convKey]
	messages, members := l.messages, l.members
	if messages == 0 {
		messages = maxBatchMessages
	}
	if members == 0 || members > memberCap {
		members = memberCap
	}
	l.messages = max(consMinBatchMessages, min(messages, b.Messages)/2)
	l.members = max(consMinBatchMembers, min(members, len(b.Members))/2)
	c.limits[convKey] = l
	c.d.Logger.Info("memory consolidation batch limits reduced", "conversation", convKey,
		"messages", l.messages, "members", l.members)
}

// grow doubles the conversation's reduced batch limits, up to the defaults.
func (c *Consolidator) grow(convKey string, memberCap int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.limits[convKey]
	if !ok {
		return
	}
	l.messages = min(l.messages*2, maxBatchMessages)
	l.members = min(l.members*2, memberCap)
	if l.messages >= maxBatchMessages && l.members >= memberCap {
		delete(c.limits, convKey)
		return
	}
	c.limits[convKey] = l
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

// acquire waits up to c.admissionWait for a background lease on p.
func (c *Consolidator) acquire(ctx context.Context, p *router.Profile, convKey string, tokens int) (*admission.Lease, error) {
	rt := p.Config.Runtime
	wctx, cancel := context.WithTimeout(ctx, c.admissionWait)
	defer cancel()
	return c.d.Admission.Acquire(wctx, admission.Request{
		ID:       "memory:" + convKey,
		Profile:  string(p.Role),
		Tokens:   tokens,
		Priority: admission.PriorityBackground,
		TTL:      rt.Timeout.Duration*time.Duration(rt.MaxRetries+1) + consLeaseSlack,
	})
}

// chat calls p's model and records model metrics. It never reports to the
// router: the breaker is shared with replies, and consolidation failures only
// count toward its own backoff.
func (c *Consolidator) chat(ctx context.Context, p *router.Profile, msgs []openai.ChatMessage, maxTokens int) (openai.ChatResponse, error) {
	start := time.Now()
	resp, err := p.Client.Chat(ctx, openai.ChatRequest{Messages: msgs, MaxTokens: maxTokens})
	if err != nil {
		return openai.ChatResponse{}, err
	}
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
