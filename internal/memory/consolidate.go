package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
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

// A batch's output is planned at one token per character: the summary at its
// limit plus consOutputFrame tokens of JSON, and each member's note at its
// limit plus consOutputPerMember tokens for the ref, delta and reason, within
// consOutputShare of the long model's reserved output.
const (
	consOutputShare     = 0.7
	consOutputFrame     = 100
	consOutputPerMember = 60
)

// Failure categories stored as GroupMemory.LastError. consSkipped comes
// before the category of a batch that failBatch moved the cursor past.
const (
	consErrAdmission = "admission_error"
	consErrModel     = "model_error"
	consErrParse     = "parse_error"
	consErrTruncated = "output_truncated"
	consSkipped      = "skipped_"
)

// consMemberCap is how many members a batch may show, at least 1, for a
// model that may output outputBudget tokens when the summary may be
// summaryMax and each note noteMax characters long. An outputBudget of 0
// leaves the output unlimited, and then only maxBatchMessages caps it.
func consMemberCap(outputBudget, summaryMax, noteMax int) int {
	if outputBudget <= 0 {
		return maxBatchMessages
	}
	room := int(float64(outputBudget)*consOutputShare) - consPlannedOutput(0, summaryMax, noteMax)
	return max(consMinBatchMembers, room/(noteMax+consOutputPerMember))
}

// consPlannedOutput is the output planned for a batch of members members.
func consPlannedOutput(members, summaryMax, noteMax int) int {
	return summaryMax + consOutputFrame + members*(noteMax+consOutputPerMember)
}

// consFitLimits is the summary and note lengths a batch's prompt asks for
// when the model may output outputBudget tokens: summaryMax and noteMax when
// a batch of one member fits within consOutputShare of it (or the output is
// unlimited, outputBudget 0), else both scaled by the same factor so that it
// fits, but never below conloomapi.MinSummaryMaxChars and MinNoteMaxChars.
// Smaller batches cannot help here: the summary is rewritten whole. Storage
// keeps the policy's limits, so an echoed longer note (with the remembered
// items at its end) is not cut on every run.
func consFitLimits(outputBudget, summaryMax, noteMax int) (int, int) {
	share := int(float64(outputBudget) * consOutputShare)
	if outputBudget <= 0 || consPlannedOutput(1, summaryMax, noteMax) <= share {
		return summaryMax, noteMax
	}
	room := share - consPlannedOutput(1, 0, 0)
	scale := func(limit, floor int) int {
		return min(limit, max(floor, limit*room/(summaryMax+noteMax)))
	}
	return scale(summaryMax, conloomapi.MinSummaryMaxChars), scale(noteMax, conloomapi.MinNoteMaxChars)
}

// RecommendedOutputTokens is the long model's reserved output that fits a
// consolidation under the default limits of every conversation type.
const RecommendedOutputTokens = 8192

// WarnOutputReservation logs a warning when the group or private default
// policy turns memory on while the long model of cfg reserves fewer than
// RecommendedOutputTokens for output (0, no limit, is fine); consolidations
// then ask for shorter summaries and notes than their limits (see
// consFitLimits). It returns the warning in Chinese for the panel, or "".
func WarnOutputReservation(cfg *config.Config, group, private conloomapi.GroupPolicy, log *slog.Logger) string {
	long, ok := cfg.ModelProfiles[string(router.RoleLong)]
	reserved := long.Context.ReservedOutputTokens
	if !ok || reserved <= 0 || reserved >= RecommendedOutputTokens || !group.Memory.Enabled && !private.Memory.Enabled {
		return ""
	}
	log.Warn("long_context reserved_output_tokens is low for memory consolidation; summaries and notes will be kept shorter than their limits",
		"reserved_output_tokens", reserved, "recommended", RecommendedOutputTokens)
	return fmt.Sprintf("长模型（long_context）给输出预留的令牌数 %d 低于建议的 %d，记忆整理时摘要和笔记会比上限短", reserved, RecommendedOutputTokens)
}

// WarnPrivateMemoryNotSensitive logs a warning when the private default policy
// turns memory on while the long model of cfg may not see sensitive content:
// private chats and the panel's shared scope are then never consolidated (see
// Run). It returns the warning in Chinese for the panel, or "".
func WarnPrivateMemoryNotSensitive(cfg *config.Config, private conloomapi.GroupPolicy, log *slog.Logger) string {
	long, ok := cfg.ModelProfiles[string(router.RoleLong)]
	if !ok || long.Privacy.AllowSensitiveContent || !private.Memory.Enabled {
		return ""
	}
	log.Warn("private memory is on but long_context does not allow sensitive content; private and panel chats will not be consolidated",
		"setting", "long_context.privacy.allow_sensitive_content")
	return "私聊记忆已打开，但长模型（long_context）不允许处理敏感内容（privacy.allow_sensitive_content），私聊和面板聊天不会整理记忆"
}

// consLimits are a conversation's reduced batch limits after truncated,
// timed-out, refused or unreadable model calls; zero fields mean the
// defaults.
type consLimits struct {
	messages, members int
}

// consStuck counts how often a conversation's smallest batch was refused or
// answered unreadably at cursor (see failBatch).
type consStuck struct {
	cursor   int64
	failures int
}

// ConsolidatorDeps are a Consolidator's collaborators. Bots is the bot
// directory a conversation's bot is looked up in. A nil Metrics, Logger or
// Now uses a private registry, a discarding logger or time.Now; a nil
// Location is UTC and a nil Usage records no model calls.
type ConsolidatorDeps struct {
	Store     store.Store
	Router    *router.Router
	Admission *admission.Controller
	Personas  *persona.Service
	Policies  *policy.Service
	Config    *config.Config
	Bots      *botdir.Directory
	Metrics   *obs.Metrics
	Logger    *slog.Logger
	Location  *time.Location
	Now       func() time.Time
	Usage     *usage.Recorder
}

// Consolidator updates a group's summary, member notes and affinity from its
// new messages with the long-context model. It counts consecutive failures
// per conversation in memory and, after consFailureLimit of them, skips
// unrequested runs for consBackoff. A truncated, timed-out, refused or
// unreadable model call halves the conversation's next batch limits
// (messages and members, down to consMinBatchMessages and
// consMinBatchMembers); each written batch doubles them back towards the
// defaults. A smallest batch that keeps being refused or answered
// unreadably is skipped (see failBatch).
type Consolidator struct {
	d             ConsolidatorDeps
	admissionWait time.Duration

	mu       sync.Mutex
	failures map[string]int
	backoff  map[string]time.Time
	limits   map[string]consLimits
	stuck    map[string]consStuck
	// fitted holds, per conversation, the last limits consFitLimits reduced
	// and warned about.
	fitted map[string][3]int
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
		failures: map[string]int{}, backoff: map[string]time.Time{}, limits: map[string]consLimits{}, stuck: map[string]consStuck{},
		fitted: map[string][3]int{}}
}

// Run consolidates up to maxBatchesPerRun batches of the messages after the
// cursor of the memory scope of convKey (ScopeKeyOf), advancing the cursor
// after each batch is written. A shared panel scope reads the messages of all
// its chats in seq order, each line marked with its chat when there are
// several (see consPlanBatch), under the policy and persona of its newest
// chat at the start of the run; its chats are read afresh before every batch,
// so a chat created during the run is not skipped. It skips the run when the
// conversation's bot is not in the directory (a deleted panel bot), when
// memory is off, during a failure backoff (unless manual), when there is
// nothing new, when no long-context model is available (for a private
// conversation or the shared panel scope, one whose privacy allows sensitive
// content) or when admission stays busy. Batches are limited by the long
// model's input budget and, through consMemberCap, by its reserved output. A
// failed model call or truncated or unreadable output keeps the cursor
// (unless failBatch skips the batch), records the failure category and
// returns an error; a cancelled ctx keeps the cursor and is not a failure.
func (c *Consolidator) Run(ctx context.Context, convKey string, manual bool) (Result, error) {
	start := time.Now()
	convKey = ScopeKeyOf(convKey)
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
	bot, ok := c.d.Bots.Bot(consBotID(convKey))
	if !ok {
		return Result{Skipped: "unknown_bot"}, 0, nil
	}
	sc, err := resolveScope(ctx, c.d.Store, convKey)
	if err != nil {
		return Result{}, 0, err
	}
	lead, err := sc.lead(ctx, c.d.Store)
	if err != nil {
		return Result{}, 0, err
	}
	if lead == "" {
		return Result{Skipped: "no_messages"}, 0, nil
	}
	pv, err := c.d.Policies.Current(ctx, lead)
	if err != nil {
		return Result{}, 0, err
	}
	mp := pv.Policy.Memory
	if !mp.Enabled || !pv.Policy.Concurrency.AllowLongRequests {
		return Result{Skipped: "disabled"}, 0, nil
	}
	private := convTypeOf(convKey) == conloomapi.ConvPrivate
	if private && !c.longAllowsSensitive() {
		c.d.Logger.Debug("memory consolidation skipped: the long-context model may not see private chats", "conversation", convKey)
		return Result{Skipped: "long_unavailable"}, 0, nil
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
	doc, botName, err := c.currentPersona(ctx, lead, bot)
	if err != nil {
		return Result{}, 0, err
	}
	chatStyle := consStyleForPersona(convKey, doc, mp)

	var res Result
	promptTokens := 0
	for res.Batches < maxBatchesPerRun {
		if res.Batches > 0 {
			if sc, err = resolveScope(ctx, c.d.Store, convKey); err != nil {
				return res, promptTokens, err
			}
		}
		style := chatStyle
		if sc.multiChat() {
			style = style.acrossChats()
		}
		if private {
			if err := c.refreshGroup(ctx, &g); err != nil {
				return res, promptTokens, err
			}
		}
		msgs, err := sc.messagesAfter(ctx, c.d.Store, g.CursorSeq, c.messageLimit(convKey))
		if err != nil {
			return res, promptTokens, err
		}
		if len(msgs) == 0 {
			break
		}
		out, err := c.batch(ctx, &g, sc, lead, mp, style, botName, msgs, runStart)
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
// summary keep the stored one. A private conversation is about its one
// member, and forgetting them (memory.Forget) moves its cursor past
// everything said before: run reads its memory row afresh before every
// batch, a batch whose member was forgotten since runStart is abandoned
// before the model call ("forgotten": nothing written, the cursor left
// where it is), and one whose member is forgotten during the call writes no
// summary (store.MemoryWrite's SummaryGuards) and never moves the cursor
// back. sc is g's scope and lead the chat whose policy and persona apply;
// style is the scope's consolidation style under that persona.
func (c *Consolidator) batch(ctx context.Context, g *store.GroupMemory, sc scope, lead string, mp conloomapi.MemoryPolicy,
	style consStyle, botName string, msgs []store.MessageRecord, runStart time.Time) (consOutcome, error) {
	convKey := g.ConversationKey
	private := convTypeOf(convKey) == conloomapi.ConvPrivate
	in, err := c.gather(ctx, sc, msgs)
	if err != nil {
		return consOutcome{}, err
	}
	base := consPromptTokens(buildPrompt(style, g.Summary, nil, nil), c.d.Config.ModelProfiles[string(router.RoleLong)].TokenEstimateFactor)
	d, err := c.d.Router.Route(router.Request{AllowLong: true, Exclude: []router.Role{router.RoleShort}, BaseTokens: base,
		Sensitive: private})
	if err != nil {
		c.d.Logger.Info("memory consolidation skipped: long-context model unavailable", "conversation", convKey, "error", err)
		return consOutcome{skipped: "long_unavailable"}, nil
	}
	p := d.Profile
	askSummary, askNote := consFitLimits(d.MaxOutputTokens, style.summaryMax, style.noteMax)
	if askSummary != style.summaryMax || askNote != style.noteMax {
		c.warnFitted(convKey, d.MaxOutputTokens, style, askSummary, askNote)
		style = style.asking(askSummary, askNote)
	}
	in.BotName, in.Location, in.Unnamed = botName, c.d.Location, style.unnamed
	in.BaseTokens, in.Budget, in.TokenFactor = base, d.PromptBudget, p.Config.TokenEstimateFactor
	memberCap := consMemberCap(d.MaxOutputTokens, askSummary, askNote)
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

	if private && slices.ContainsFunc(b.Members, func(m consBatchMember) bool {
		return !m.Memory.ForgottenAt.IsZero() && !m.Memory.ForgottenAt.Before(runStart)
	}) {
		c.d.Logger.Info("memory consolidation stopped: the member was forgotten during the run", "conversation", convKey)
		return consOutcome{skipped: "forgotten"}, nil
	}

	members := make([]promptMember, len(b.Members))
	for i, m := range b.Members {
		members[i] = consPromptMember(m.Memory, m.Ref, m.Name, style.unnamed)
	}
	prompt := buildPrompt(style, g.Summary, members, b.Lines)
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
	resp, err := c.chat(ctx, p, lead, prompt, d.MaxOutputTokens)
	lease.Release()
	if err != nil {
		if ctx.Err() != nil {
			return consOutcome{}, ctx.Err()
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, openai.ErrEmptyOutput):
			c.shrink(convKey, b, memberCap)
		case consRefused(err):
			return consOutcome{}, c.failBatch(ctx, *g, b, memberCap, consErrModel, err, resp)
		}
		return consOutcome{}, c.fail(ctx, *g, consErrModel, err, resp)
	}
	if resp.FinishReason == "length" {
		c.shrink(convKey, b, memberCap)
		return consOutcome{}, c.fail(ctx, *g, consErrTruncated, errors.New("model output reached its token limit"), resp)
	}
	out, err := parseOutput(resp.Content)
	if err != nil {
		return consOutcome{}, c.failBatch(ctx, *g, b, memberCap, consErrParse, err, resp)
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

	day := LocalDay(now, c.d.Location)
	w, entries := c.compose(*g, b, out, mp.Affinity, day, now)
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
		if err := c.refreshGroup(ctx, g); err != nil {
			return consOutcome{}, err
		}
	}
	c.grow(convKey, memberCap)
	updated := c.settle(ctx, convKey, entries, conflicts, mp.Affinity, day)
	return consOutcome{messages: b.Messages, membersUpdated: updated, promptTokens: resp.PromptTokens}, nil
}

// gather loads what planning msgs needs: reply targets in the scope's chats,
// mentioned actors' user IDs and names (looked up in the chat that mentions
// them), the stored memory of every member involved in the scope and, for a
// scope of several chats, the label of each chat.
func (c *Consolidator) gather(ctx context.Context, sc scope, msgs []store.MessageRecord) (consBatchInput, error) {
	in := consBatchInput{ConversationKey: sc.key, Messages: msgs}
	var replyIDs, userIDs, chats []string
	actors := map[string][]string{}
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
			if mention.Type == "user" && mention.ID != "" && !slices.Contains(actors[m.ConversationKey], mention.ID) {
				add(&chats, "c", m.ConversationKey)
				actors[m.ConversationKey] = append(actors[m.ConversationKey], mention.ID)
			}
		}
	}
	if len(replyIDs) > 0 {
		found, err := c.d.Store.GetMessages(ctx, consBotID(sc.key), replyIDs)
		if err != nil {
			return consBatchInput{}, err
		}
		in.ReplyTargets = make(map[string]store.MessageRecord, len(found))
		for _, t := range found {
			if !sc.has(t.ConversationKey) {
				continue
			}
			in.ReplyTargets[t.ID] = t
			if !t.FromBot {
				add(&userIDs, "u", t.PlatformUserID)
			}
		}
	}
	for _, chat := range chats {
		users, err := c.d.Store.UserIDsForActors(ctx, chat, actors[chat])
		if err != nil {
			return consBatchInput{}, err
		}
		names, err := c.d.Store.LatestDisplayNames(ctx, chat, actors[chat])
		if err != nil {
			return consBatchInput{}, err
		}
		if in.MentionUsers == nil {
			in.MentionUsers, in.MentionNames = users, names
		} else {
			mergeMissing(in.MentionUsers, users)
			mergeMissing(in.MentionNames, names)
		}
		for _, a := range actors[chat] {
			add(&userIDs, "u", users[a])
		}
	}
	members, err := c.d.Store.GetMemberMemories(ctx, sc.key, userIDs)
	if err != nil {
		return consBatchInput{}, err
	}
	in.Members = members
	if sc.multiChat() {
		in.ChatLabels = consChatLabels(msgs)
	}
	return in, nil
}

// mergeMissing adds the entries of src whose keys dst lacks.
func mergeMissing(dst, src map[string]string) {
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
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
func (c *Consolidator) compose(g store.GroupMemory, b consBatch, out modelOutput, affinityOn bool, day string, now time.Time) (store.MemoryWrite, []consEntry) {
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

// failBatch records a failed batch b that the model refused (consRefused) or
// answered unreadably, which retrying the same batch is likely to repeat.
// Like a timeout it halves the conversation's batch limits. Once a batch of
// at most consMinBatchMessages messages has failed this way consFailureLimit
// times at the same cursor, it is skipped: the cursor moves past it with
// nothing else written, the category is recorded after consSkipped and a
// warning is logged. Either way the failure counts toward the backoff.
func (c *Consolidator) failBatch(ctx context.Context, g store.GroupMemory, b consBatch, memberCap int, category string,
	cause error, resp openai.ChatResponse) error {
	if !c.stuckAt(g.ConversationKey, g.CursorSeq, b) {
		c.shrink(g.ConversationKey, b, memberCap)
		return c.fail(ctx, g, category, cause, resp)
	}
	c.d.Logger.Warn("memory consolidation skipped a batch that keeps failing", "conversation", g.ConversationKey,
		"category", category, "after_seq", g.CursorSeq, "through_seq", b.LastSeq, "messages", b.Messages)
	g.CursorSeq = b.LastSeq
	return c.fail(ctx, g, consSkipped+category, cause, resp)
}

// consRefused reports whether err, from a model call, means the provider
// refused this request rather than failing for every request: 400, 413 or
// 422 (such as a content check or a prompt it will not take). An answer
// without text is not a refusal: a reasoning model may have spent its whole
// output reservation on reasoning, so such a batch is only halved, like one
// that timed out.
func consRefused(err error) bool {
	var se *openai.StatusError
	return errors.As(err, &se) && (se.Code == http.StatusBadRequest ||
		se.Code == http.StatusRequestEntityTooLarge || se.Code == http.StatusUnprocessableEntity)
}

// stuckAt counts a refused or unreadable failure of b at cursor when b is a
// batch of at most consMinBatchMessages messages, and reports whether such
// failures at cursor have reached consFailureLimit, which also resets the
// count.
func (c *Consolidator) stuckAt(convKey string, cursor int64, b consBatch) bool {
	if b.Messages > consMinBatchMessages {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stuck[convKey]
	if s.cursor != cursor {
		s = consStuck{cursor: cursor}
	}
	s.failures++
	if s.failures >= consFailureLimit {
		delete(c.stuck, convKey)
		return true
	}
	c.stuck[convKey] = s
	return false
}

// longAllowsSensitive reports whether the long model may see sensitive
// content, as a private conversation's consolidation needs (the router then
// still checks its breaker and context).
func (c *Consolidator) longAllowsSensitive() bool {
	p, ok := c.d.Router.Profile(router.RoleLong)
	return ok && p.Config.Privacy.AllowSensitiveContent
}

// putGroupState stores g's cursor, run time and error, keeping the stored
// summary.
func (c *Consolidator) putGroupState(ctx context.Context, g store.GroupMemory) error {
	_, err := c.d.Store.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: g.ConversationKey, Group: &g, KeepSummary: true})
	return err
}

// refreshGroup replaces g with the stored row; without one, g's summary is
// emptied and the rest kept.
func (c *Consolidator) refreshGroup(ctx context.Context, g *store.GroupMemory) error {
	stored, err := c.d.Store.GetGroupMemory(ctx, g.ConversationKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		g.Summary = ""
	case err != nil:
		return err
	default:
		*g = stored
	}
	return nil
}

// warnFitted logs that the conversation's prompt asks for summaryMax and
// noteMax characters, less than its limits, for an output reservation of
// reserved tokens, once for each combination of the three.
func (c *Consolidator) warnFitted(convKey string, reserved int, style consStyle, summaryMax, noteMax int) {
	c.mu.Lock()
	key := [3]int{reserved, style.summaryMax, style.noteMax}
	seen := c.fitted[convKey] == key
	c.fitted[convKey] = key
	c.mu.Unlock()
	if seen {
		return
	}
	c.d.Logger.Warn("memory consolidation limits reduced to fit the long model's output reservation", "conversation", convKey,
		"reserved_output_tokens", reserved, "summary_max_chars", style.summaryMax, "note_max_chars", style.noteMax,
		"requested_summary_chars", summaryMax, "requested_note_chars", noteMax,
		"recommended_reserved_output_tokens", RecommendedOutputTokens)
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
	delete(c.stuck, convKey)
}

// acquire waits up to c.admissionWait for a background lease on p.
func (c *Consolidator) acquire(ctx context.Context, p *router.Profile, convKey string, tokens int) (*admission.Lease, error) {
	wctx, cancel := context.WithTimeout(ctx, c.admissionWait)
	defer cancel()
	return c.d.Admission.Acquire(wctx, admission.Request{
		ID:       "memory:" + convKey,
		Profile:  string(p.Role),
		Tokens:   tokens,
		Priority: admission.PriorityBackground,
		TTL:      openai.MaxChatDuration(p.Config.Runtime) + consLeaseSlack,
	})
}

// chat calls p's model for convKey and records the call and model metrics.
// It never reports to the router: the breaker is shared with replies, and
// consolidation failures only count toward its own backoff.
func (c *Consolidator) chat(ctx context.Context, p *router.Profile, convKey string, msgs []openai.ChatMessage, maxTokens int) (openai.ChatResponse, error) {
	start := time.Now()
	resp, err := p.Client.Chat(ctx, openai.ChatRequest{Messages: msgs, MaxTokens: maxTokens})
	latency := time.Since(start)
	c.d.Usage.Record(store.ModelCall{
		At: start.UTC(), BotInstanceID: consBotID(convKey), ConversationKey: convKey,
		Purpose: store.PurposeMemory, Role: string(p.Role), Model: cmp.Or(resp.Model, p.Config.Model),
		PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens, ReasoningTokens: resp.ReasoningTokens,
		LatencyMS: latency.Milliseconds(), Outcome: usage.Outcome(err), FinishReason: resp.FinishReason,
	})
	if err != nil {
		return openai.ChatResponse{}, err
	}
	role := string(p.Role)
	c.d.Metrics.ModelLatency.WithLabelValues(role, resp.Model).Observe(latency.Seconds())
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "prompt").Add(float64(resp.PromptTokens))
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "completion").Add(float64(resp.CompletionTokens))
	c.d.Metrics.ModelTokens.WithLabelValues(role, resp.Model, "reasoning").Add(float64(resp.ReasoningTokens))
	return resp, nil
}

// currentPersona returns the conversation's current persona and the name of
// its bot under it.
func (c *Consolidator) currentPersona(ctx context.Context, convKey string, bot config.BotConfig) (conloomapi.PersonaDoc, string, error) {
	pv, err := c.d.Personas.Current(ctx, convKey, bot.TemplateFor(convTypeOf(convKey)))
	if err != nil {
		return conloomapi.PersonaDoc{}, "", err
	}
	return pv.Persona, persona.BotName(pv.Persona, bot.Name), nil
}

// consBotID is the bot instance part of a "bot:platform:type:id" conversation key.
func consBotID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}
