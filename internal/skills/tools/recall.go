package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	recallQueryRunes   = 100
	recallMaxKeywords  = 8
	recallKeywordRunes = 20
	// recallKeywordHits and recallVectorHits are how many messages and
	// stretches each search path returns for fusion.
	recallKeywordHits = 50
	recallVectorHits  = 20
	// recallRRFK is the k of reciprocal rank fusion, and recallRecencyWeight
	// the most a hit gains for being the newest among the candidates.
	recallRRFK          = 60
	recallRecencyWeight = 0.02
	// recallHits is how many candidates are shown, each as a group.
	recallHits    = 6
	recallContext = 2
	// recallMargin extra messages are loaded on each side of a hit so that
	// filtering still leaves its context; looking back for where no command
	// is pending loads recallLookback messages at a time, recallLookbackMax
	// at most.
	recallMargin      = 10
	recallLookback    = 50
	recallLookbackMax = 500
	recallMaxRunes    = 1500
	recallBodyRunes   = 200
	recallNameRunes   = 20
	recallTurnWindow  = 2 * time.Minute
	recallReplyWindow = 2 * time.Minute
	recallDateLayout  = "2006-01-02"
	recallNotFound    = "没有找到相关的聊天记录。"
	recallNoName      = "一位群友"
	recallBadDate     = "after 和 before 需要是 YYYY-MM-DD 格式的日期。"
)

// recallVectorTimeout bounds the semantic search of one recall, on top of the
// searcher's own timeout.
var recallVectorTimeout = 3 * time.Second

// RecallStore is the part of store.Store that recall reads.
type RecallStore interface {
	SearchMessages(ctx context.Context, convKey string, terms []string, r search.Range, limit int) ([]store.MessageHit, error)
	MessagesAround(ctx context.Context, convKey string, fromSeq, toSeq int64, before, after int) ([]store.MessageRecord, error)
	ListMemberMemories(ctx context.Context, convKey string) ([]store.MemberMemory, error)
}

// OutputReviewer is the part of review.Reviewer that recall uses.
type OutputReviewer interface {
	Output(text string, pol conloomapi.ReviewPolicy) review.Result
}

// RecallDeps are what recall needs. IsCommand reports whether text is a
// command under the conversation's command prefix (core.ParseCommand); when
// nil, every message starting with the prefix counts as one. Vectors, when
// set, adds semantic search; Metrics, when set, times the keyword search.
type RecallDeps struct {
	Store     RecallStore
	Policies  PolicySource
	Reviewer  OutputReviewer
	IsCommand func(text, prefix string) bool
	Vectors   search.VectorSearcher
	Metrics   *obs.Metrics
}

type recallTool struct {
	d         RecallDeps
	maxGroups int
}

// NewRecall returns the recall tool: a search of the whole history of the
// invocation's conversation when memory is in effect, optionally within a
// range of local dates.
//
// The keyword index gives the best 50 messages and, when Vectors is set,
// semantic search the best 20 stretches; reciprocal rank fusion (k = 60)
// with a slight preference for newer hits ranks them, overlapping hits
// counting together. The best 6 are shown: a message hit with 2 messages of
// context on either side, a stretch whole, groups that touch merged, each
// group under a "—— YYYY-MM-DD ——" line, in time order, as
// "[MM-DD HH:MM] 名字：正文" lines, at most 1500 runes, without any IDs.
//
// Never shown are commands and the bot's replies to them, messages blocked
// by the conversation's review, the current turn (the trigger messages, or
// without them the speaker's messages of the last two minutes) and messages
// hidden by forgetting: in private chats everything before the speaker was
// forgotten, in groups what each forgotten member said before then. The rest
// is shown with contact details redacted (memory.RedactContactOrID) and the
// review's output rules applied, and a message hit only counts when that text
// or its sender's shown name still holds a search term.
func NewRecall(d RecallDeps) skills.Tool { return &recallTool{d: d, maxGroups: recallHits} }

// MaxResultRunes declares recall's limit on what it returns.
func (*recallTool) MaxResultRunes() int { return recallMaxRunes }

func (*recallTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "recall",
		Description: "在当前会话以前的全部聊天记录里查找。用户问到以前聊过、但眼前的对话和你记得的事里都没有的内容时调用，" +
			"查不到就老实说不记得。除了 query，最好在 keywords 里再给几个同义或相关的说法，" +
			"比如找“旅游”时给“旅游 / 旅行 / 出去玩”，这样换了说法的记录也能找到。" +
			"用户提到时间（比如“上个月”“去年夏天”“3 月初”）时，用 after 和 before 限定日期范围（本地日期，两头都包含）。" +
			"返回相关的几段聊天记录：每段前面一行“—— 年-月-日 ——”是那段的日期，" +
			"每行是“[月-日 时:分] 名字：内容”，“你”是你自己说的话。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"要查找的关键词或短句，例如“草莓 蛋糕”，1 到 100 字","minLength":1,"maxLength":100},` +
			`"keywords":{"type":"array","description":"可选：同义或相关的说法，每个整词匹配，例如 [\"旅行\",\"出去玩\"]",` +
			`"items":{"type":"string","minLength":1,"maxLength":20},"maxItems":8},` +
			`"after":{"type":"string","description":"可选：只查这一天及以后的记录，格式 YYYY-MM-DD","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},` +
			`"before":{"type":"string","description":"可选：只查这一天及以前的记录，格式 YYYY-MM-DD","pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}},` +
			`"required":["query"]}`),
	}
}

func (t *recallTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	query, ok := stringArg(args, "query")
	if !ok {
		return "需要提供要查找的内容（query）。", nil
	}
	query = strings.Join(strings.Fields(query), " ")
	switch n := utf8.RuneCountInString(query); {
	case n == 0:
		return "要查找的内容为空。", nil
	case n > recallQueryRunes:
		return "要查找的内容超过 100 字。", nil
	}
	keywords, problem := keywordsArg(args)
	if problem != "" {
		return problem, nil
	}
	loc := inv.Location
	if loc == nil {
		loc = time.UTC
	}
	rng, problem := dateRangeArg(args, loc)
	if problem != "" {
		return problem, nil
	}
	pv, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆。", nil
	}
	terms := search.Terms(query, keywords)
	keyword, err := t.keywordHits(ctx, inv.ConversationKey, terms, rng)
	if err != nil {
		return "", err
	}
	candidates := fuse(keyword, t.vectorHits(ctx, inv.ConversationKey, query, rng))
	if len(candidates) == 0 {
		return recallNotFound, nil
	}
	members, err := t.d.Store.ListMemberMemories(ctx, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	var groups []recallGroup
	for _, c := range candidates {
		if len(groups) == t.maxGroups {
			break
		}
		g, ok, err := t.expand(ctx, inv, pv.Policy, members, terms, c)
		if err != nil {
			return "", err
		}
		if ok {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return recallNotFound, nil
	}
	return renderGroups(mergeGroups(groups), loc), nil
}

// keywordsArg returns the optional keywords argument with whitespace
// collapsed, or a sentence saying what is wrong with it.
func keywordsArg(args json.RawMessage) ([]string, string) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return nil, ""
	}
	raw, found := obj["keywords"]
	if !found || string(raw) == "null" {
		return nil, ""
	}
	var keywords []string
	if err := json.Unmarshal(raw, &keywords); err != nil || keywords == nil {
		return nil, "keywords 需要是字符串列表。"
	}
	if len(keywords) > recallMaxKeywords {
		return nil, "keywords 最多 8 个。"
	}
	for i, k := range keywords {
		k = strings.Join(strings.Fields(k), " ")
		if n := utf8.RuneCountInString(k); n == 0 || n > recallKeywordRunes {
			return nil, "keywords 里每个词要 1 到 20 字。"
		}
		keywords[i] = k
	}
	return keywords, ""
}

// dateRangeArg returns the range of the optional after and before arguments,
// local dates in loc that both belong to the range, or a sentence saying what
// is wrong with them. A missing, null or empty date leaves its side open.
func dateRangeArg(args json.RawMessage, loc *time.Location) (search.Range, string) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return search.Range{}, ""
	}
	date := func(name string) (time.Time, bool) {
		raw, found := obj[name]
		if !found || string(raw) == "null" {
			return time.Time{}, true
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return time.Time{}, false
		}
		if s = strings.TrimSpace(s); s == "" {
			return time.Time{}, true
		}
		d, err := time.ParseInLocation(recallDateLayout, s, loc)
		return d, err == nil
	}
	after, okAfter := date("after")
	before, okBefore := date("before")
	switch {
	case !okAfter || !okBefore:
		return search.Range{}, recallBadDate
	case !after.IsZero() && !before.IsZero() && after.After(before):
		return search.Range{}, "after 不能晚于 before。"
	}
	r := search.Range{After: after}
	if !before.IsZero() {
		r.Before = before.AddDate(0, 0, 1)
	}
	return r, ""
}

// keywordHits searches the keyword index, timing it as path "keyword".
func (t *recallTool) keywordHits(ctx context.Context, convKey string, terms []string, r search.Range) ([]store.MessageHit, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	start := time.Now()
	hits, err := t.d.Store.SearchMessages(ctx, convKey, terms, r, recallKeywordHits)
	if t.d.Metrics != nil {
		t.d.Metrics.SearchSeconds.WithLabelValues("keyword").Observe(time.Since(start).Seconds())
	}
	return hits, err
}

// vectorHits asks Vectors, when set, for the stretches closest to query. A
// failure or timeout leaves recall to the keyword hits.
func (t *recallTool) vectorHits(ctx context.Context, convKey, query string, r search.Range) []search.Hit {
	if t.d.Vectors == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, recallVectorTimeout)
	defer cancel()
	hits, err := t.d.Vectors.Search(ctx, convKey, query, r, recallVectorHits)
	if err != nil {
		return nil
	}
	return hits
}

// recallCandidate is a hit to show: the messages from..to, of which anchors
// are keyword hits and, when vector is set, all of which are a stretch found
// by semantic search.
type recallCandidate struct {
	from, to int64
	anchors  []int64
	vector   bool
	score    float64
}

// fuse ranks keyword and vector hits by reciprocal rank fusion: a hit at
// rank i of its list scores 1/(recallRRFK+i+1), and hits whose seq ranges
// overlap become one candidate with their scores added. Each candidate's
// score is then raised by up to recallRecencyWeight for being newer than the
// others. Candidates come best first, newer first among equals.
func fuse(keyword []store.MessageHit, vector []search.Hit) []recallCandidate {
	rrf := func(rank int) float64 { return 1 / float64(recallRRFK+rank+1) }
	var cands []recallCandidate
	for i, h := range keyword {
		cands = append(cands, recallCandidate{from: h.Seq, to: h.Seq, anchors: []int64{h.Seq}, score: rrf(i)})
	}
	for i, h := range vector {
		from, to := min(h.FromSeq, h.ToSeq), max(h.FromSeq, h.ToSeq)
		cands = append(cands, recallCandidate{from: from, to: to, vector: true, score: rrf(i)})
	}
	slices.SortStableFunc(cands, func(a, b recallCandidate) int { return cmp.Compare(a.from, b.from) })
	var merged []recallCandidate
	for _, c := range cands {
		if n := len(merged); n > 0 && c.from <= merged[n-1].to {
			last := &merged[n-1]
			last.to = max(last.to, c.to)
			last.anchors = append(last.anchors, c.anchors...)
			last.vector = last.vector || c.vector
			last.score += c.score
			continue
		}
		merged = append(merged, c)
	}
	if len(merged) == 0 {
		return nil
	}
	lo := slices.MinFunc(merged, func(a, b recallCandidate) int { return cmp.Compare(a.to, b.to) }).to
	hi := slices.MaxFunc(merged, func(a, b recallCandidate) int { return cmp.Compare(a.to, b.to) }).to
	for i := range merged {
		if hi > lo {
			merged[i].score *= 1 + recallRecencyWeight*float64(merged[i].to-lo)/float64(hi-lo)
		}
	}
	slices.SortStableFunc(merged, func(a, b recallCandidate) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.to, a.to))
	})
	return merged
}

// recallEntry is a message as recall may show it: name and body cleaned,
// redacted and reviewed, and search the lower-cased name and body without
// spaces, for checking terms.
type recallEntry struct {
	seq                int64
	at                 time.Time
	name, body, search string
}

// recallGroup is what is shown for one or more candidates: entries in Seq
// order, hits among them kept first when cutting, and the seqs of the
// visible messages just before and after it (0 when unknown).
type recallGroup struct {
	entries    []recallEntry
	hits       []int64
	score      float64
	prev, next int64
}

// expand turns a candidate into its group: its stretch whole, and each
// keyword hit whose shown text still holds a term with recallContext
// visible messages on either side. ok is false when nothing of it can be
// shown.
func (t *recallTool) expand(ctx context.Context, inv skills.Invocation, pol conloomapi.GroupPolicy,
	members []store.MemberMemory, terms []string, c recallCandidate) (recallGroup, bool, error) {
	entries, err := t.region(ctx, inv, pol, members, c.from, c.to)
	if err != nil {
		return recallGroup{}, false, err
	}
	g := recallGroup{score: c.score}
	lo, hi := len(entries), -1
	for i, e := range entries {
		if c.vector && e.seq >= c.from && e.seq <= c.to {
			g.hits = append(g.hits, e.seq)
			lo, hi = min(lo, i), max(hi, i)
		}
		if slices.Contains(c.anchors, e.seq) && holdsTerm(e.search, terms) {
			g.hits = append(g.hits, e.seq)
			lo, hi = min(lo, max(i-recallContext, 0)), max(hi, min(i+recallContext, len(entries)-1))
		}
	}
	if hi < 0 {
		return recallGroup{}, false, nil
	}
	g.entries = entries[lo : hi+1]
	if lo > 0 {
		g.prev = entries[lo-1].seq
	}
	if hi+1 < len(entries) {
		g.next = entries[hi+1].seq
	}
	return g, true, nil
}

func holdsTerm(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, strings.ReplaceAll(term, " ", "")) {
			return true
		}
	}
	return false
}

// region loads the messages from..to of the invocation's conversation with
// recallContext+recallMargin more on either side and returns those that may
// be shown, as entries. To tell the bot's replies to commands exactly, it
// first looks back for a quiet point, where no earlier command can still be
// waiting for a reply; without one within recallLookbackMax messages, every
// bot message within recallReplyWindow after a command is dropped.
func (t *recallTool) region(ctx context.Context, inv skills.Invocation, pol conloomapi.GroupPolicy,
	members []store.MemberMemory, from, to int64) ([]recallEntry, error) {
	span := recallContext + recallMargin
	msgs, err := t.d.Store.MessagesAround(ctx, inv.ConversationKey, from, to, span, span)
	if err != nil || len(msgs) == 0 {
		return nil, err
	}
	isCommand := t.isCommand(pol.Reply.CommandPrefix)
	ext, first := msgs, 0
	atStart := slices.IndexFunc(msgs, func(m store.MessageRecord) bool { return m.Seq >= from }) < span
	broad := false
	for !atStart {
		if q, ok := quietPoint(ext, first, isCommand); ok {
			ext, first = ext[q:], first-q
			break
		}
		if first >= recallLookbackMax {
			broad = true
			break
		}
		more, err := t.d.Store.MessagesAround(ctx, inv.ConversationKey, ext[0].Seq, ext[0].Seq, recallLookback, 0)
		if err != nil {
			return nil, err
		}
		more = more[:len(more)-1]
		atStart = len(more) < recallLookback
		ext, first = append(more, ext...), first+len(more)
	}
	kept := withoutCommands(ext, isCommand, broad)
	kept = slices.DeleteFunc(kept, func(m store.MessageRecord) bool { return m.Seq < msgs[0].Seq })
	kept = withoutCurrentTurn(visible(kept, members, inv), inv)
	return t.entries(kept, pol), nil
}

// quietPoint finds the latest index q <= first of msgs such that msgs covers
// the recallReplyWindow before msgs[q] and holds no command within it: no
// command older than msgs[q] can then claim a later bot message.
func quietPoint(msgs []store.MessageRecord, first int, isCommand func(string) bool) (int, bool) {
	for q := first; q >= 0; q-- {
		since := msgs[q].OccurredAt.Add(-recallReplyWindow)
		if msgs[0].OccurredAt.After(since) {
			return 0, false
		}
		quiet := true
		for j := q - 1; j >= 0 && !msgs[j].OccurredAt.Before(since); j-- {
			if !msgs[j].FromBot && isCommand(msgs[j].Text) {
				quiet = false
				break
			}
		}
		if quiet {
			return q, true
		}
	}
	return 0, false
}

// visible drops the messages hidden by forgetting: in a private chat every
// message before the speaker was forgotten, in a group each forgotten
// member's messages from before then.
func visible(msgs []store.MessageRecord, members []store.MemberMemory, inv skills.Invocation) []store.MessageRecord {
	forgotten := make(map[string]time.Time, len(members))
	for _, m := range members {
		if !m.ForgottenAt.IsZero() {
			forgotten[m.PlatformUserID] = m.ForgottenAt
		}
	}
	if inv.ConversationType == conloomapi.ConvPrivate {
		at, ok := forgotten[inv.PlatformUserID]
		if !ok {
			return msgs
		}
		return slices.DeleteFunc(msgs, func(m store.MessageRecord) bool { return m.OccurredAt.Before(at) })
	}
	return slices.DeleteFunc(msgs, func(m store.MessageRecord) bool {
		at, ok := forgotten[m.PlatformUserID]
		return !m.FromBot && ok && m.OccurredAt.Before(at)
	})
}

// withoutCurrentTurn drops the turn being answered, which the model already
// sees: the invocation's trigger messages, or without them the speaker's
// messages from recallTurnWindow before the invocation's time on.
func withoutCurrentTurn(msgs []store.MessageRecord, inv skills.Invocation) []store.MessageRecord {
	if len(inv.TriggerMessageIDs) > 0 {
		return slices.DeleteFunc(msgs, func(m store.MessageRecord) bool { return slices.Contains(inv.TriggerMessageIDs, m.ID) })
	}
	now := inv.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := now.Add(-recallTurnWindow)
	return slices.DeleteFunc(msgs, func(m store.MessageRecord) bool {
		return !m.FromBot && m.PlatformUserID == inv.PlatformUserID && !m.OccurredAt.Before(since)
	})
}

// isCommand reports whether a member's text is a command under prefix:
// IsCommand when given, otherwise any text starting with prefix.
func (t *recallTool) isCommand(prefix string) func(text string) bool {
	if t.d.IsCommand != nil {
		return func(text string) bool { return t.d.IsCommand(text, prefix) }
	}
	return func(text string) bool { return prefix != "" && strings.HasPrefix(strings.TrimSpace(text), prefix) }
}

// withoutCommands drops members' commands and the bot's replies to them,
// which may show memory that was since forgotten. msgs are in Seq order; each
// command claims the first bot message after it not claimed by an earlier
// command, when that comes within recallReplyWindow. With broad set, every
// bot message within recallReplyWindow after a command is dropped instead,
// for when the commands before msgs are unknown.
func withoutCommands(msgs []store.MessageRecord, isCommand func(string) bool, broad bool) []store.MessageRecord {
	var pending []time.Time
	drop := make([]bool, len(msgs))
	for i, m := range msgs {
		if !m.FromBot {
			if isCommand(m.Text) {
				drop[i] = true
				pending = append(pending, m.OccurredAt)
			}
			continue
		}
		pending = slices.DeleteFunc(pending, func(at time.Time) bool { return m.OccurredAt.Sub(at) > recallReplyWindow })
		if len(pending) > 0 {
			drop[i] = true
			if !broad {
				pending = pending[1:]
			}
		}
	}
	kept := msgs[:0:0]
	for i, m := range msgs {
		if !drop[i] {
			kept = append(kept, m)
		}
	}
	return kept
}

// entries prepares msgs for searching and showing, dropping messages that
// the review blocks.
func (t *recallTool) entries(msgs []store.MessageRecord, pol conloomapi.GroupPolicy) []recallEntry {
	out := make([]recallEntry, 0, len(msgs))
	for _, m := range msgs {
		body := oneLine(m.Text)
		for _, a := range m.Attachments {
			if a.Type == "image" {
				body += "[图片]"
			} else {
				body += "[文件]"
			}
		}
		if body == "" {
			body = "（没有文字内容）"
		}
		res := t.d.Reviewer.Output(memory.RedactContactOrID(body), pol.Review)
		if res.Verdict == review.VerdictBlock {
			continue
		}
		body = oneLine(res.Text)
		name := recallName(m)
		out = append(out, recallEntry{seq: m.Seq, at: m.OccurredAt, name: name, body: body,
			search: strings.ToLower(strings.ReplaceAll(name+body, " ", ""))})
	}
	return out
}

// recallName is "你" for the bot, otherwise the member's display name on one
// line, redacted and cut to recallNameRunes runes, or recallNoName without a
// usable one.
func recallName(m store.MessageRecord) string {
	if m.FromBot {
		return "你"
	}
	name := oneLine(m.DisplayName)
	if name == "" || name == m.PlatformUserID || name == m.ActorID {
		return recallNoName
	}
	name = memory.RedactContactOrID(name)
	if r := []rune(name); len(r) > recallNameRunes {
		name = string(r[:recallNameRunes])
	}
	return name
}

// oneLine drops control and format characters (zero-width and direction
// marks among them) from s and collapses its whitespace to single spaces.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// mergeGroups orders groups by time and merges those that overlap or touch,
// adding their scores.
func mergeGroups(groups []recallGroup) []recallGroup {
	slices.SortStableFunc(groups, func(a, b recallGroup) int { return cmp.Compare(a.entries[0].seq, b.entries[0].seq) })
	var merged []recallGroup
	for _, g := range groups {
		n := len(merged)
		if n == 0 {
			merged = append(merged, g)
			continue
		}
		last := &merged[n-1]
		lastSeq, firstSeq := last.entries[len(last.entries)-1].seq, g.entries[0].seq
		if firstSeq > lastSeq && last.next != firstSeq && g.prev != lastSeq {
			merged = append(merged, g)
			continue
		}
		for _, e := range g.entries {
			if !slices.ContainsFunc(last.entries, func(x recallEntry) bool { return x.seq == e.seq }) {
				last.entries = append(last.entries, e)
			}
		}
		slices.SortFunc(last.entries, func(a, b recallEntry) int { return cmp.Compare(a.seq, b.seq) })
		last.hits = append(last.hits, g.hits...)
		last.score += g.score
		if g.entries[len(g.entries)-1].seq >= lastSeq {
			last.next = g.next
		}
	}
	return merged
}

// renderGroups renders groups in time order, each under its date line and
// separated by a blank line, within recallMaxRunes. Groups are filled best
// score first; a group that does not fit keeps its hits first and then the
// lines nearest to them.
func renderGroups(groups []recallGroup, loc *time.Location) string {
	order := make([]int, len(groups))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(groups[b].score, groups[a].score) })

	headerRunes := utf8.RuneCountInString(dateHeader(time.Time{}, loc))
	kept := make([][]int, len(groups))
	used := 0
	for _, gi := range order {
		g := groups[gi]
		hitIdx := make([]int, 0, len(g.hits))
		for i, e := range g.entries {
			if slices.Contains(g.hits, e.seq) {
				hitIdx = append(hitIdx, i)
			}
		}
		candidates := make([]int, len(g.entries))
		for i := range candidates {
			candidates[i] = i
		}
		slices.SortStableFunc(candidates, func(a, b int) int {
			return cmp.Compare(distance(a, hitIdx), distance(b, hitIdx))
		})
		for _, i := range candidates {
			cost := utf8.RuneCountInString(recallLine(g.entries[i], loc)) + 1
			if len(kept[gi]) == 0 {
				cost += headerRunes
				if used > 0 {
					cost += 2
				}
			}
			if used+cost > recallMaxRunes {
				continue
			}
			used += cost
			kept[gi] = append(kept[gi], i)
		}
	}

	var parts []string
	for gi, g := range groups {
		if len(kept[gi]) == 0 {
			continue
		}
		slices.Sort(kept[gi])
		lines := []string{dateHeader(g.entries[kept[gi][0]].at, loc)}
		for _, i := range kept[gi] {
			lines = append(lines, recallLine(g.entries[i], loc))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

// dateHeader is the line above a group: its first message's local date.
func dateHeader(at time.Time, loc *time.Location) string {
	return "—— " + at.In(loc).Format(recallDateLayout) + " ——"
}

func distance(i int, hits []int) int {
	d := -1
	for _, h := range hits {
		if v := max(i-h, h-i); d < 0 || v < d {
			d = v
		}
	}
	return d
}

// recallLine renders e as "[MM-DD HH:MM] 名字：正文" with the body cut to
// recallBodyRunes runes.
func recallLine(e recallEntry, loc *time.Location) string {
	body := e.body
	if r := []rune(body); len(r) > recallBodyRunes {
		body = string(r[:recallBodyRunes]) + "…"
	}
	return "[" + e.at.In(loc).Format("01-02 15:04") + "] " + e.name + "：" + body
}
