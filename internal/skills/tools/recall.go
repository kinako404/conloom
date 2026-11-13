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
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	recallScan         = 5000
	recallQueryRunes   = 100
	recallMaxTokens    = 8
	recallMaxKeywords  = 8
	recallMaxTerms     = 16
	recallKeywordRunes = 20
	recallHits         = 6
	recallContext      = 2
	recallMaxRunes     = 1500
	recallBodyRunes    = 200
	recallNameRunes    = 20
	recallTurnWindow   = 2 * time.Minute
	recallReplyWindow  = 2 * time.Minute
	recallNotFound     = "没有找到相关的聊天记录。"
	recallNoName       = "一位群友"
)

// RecallStore is the part of store.Store that recall reads.
type RecallStore interface {
	RecentMessages(ctx context.Context, conversationKey string, limit int) ([]store.MessageRecord, error)
	ListMemberMemories(ctx context.Context, convKey string) ([]store.MemberMemory, error)
}

// OutputReviewer is the part of review.Reviewer that recall uses.
type OutputReviewer interface {
	Output(text string, pol conloomapi.ReviewPolicy) review.Result
}

// RecallDeps are what recall needs. IsCommand reports whether text is a
// command under the conversation's command prefix (core.ParseCommand); when
// nil, every message starting with the prefix counts as one.
type RecallDeps struct {
	Store     RecallStore
	Policies  PolicySource
	Reviewer  OutputReviewer
	IsCommand func(text, prefix string) bool
}

type recallTool struct{ d RecallDeps }

// NewRecall returns the recall tool: a keyword search of the newest 5000
// messages of the invocation's conversation when memory is in effect.
//
// Skipped are commands and the bot's replies to them, messages blocked by the
// conversation's review, the
// current turn (the trigger messages, or without them the speaker's messages
// of the last two minutes) and messages hidden by forgetting: in private chats
// everything before the speaker was forgotten, in groups what each forgotten
// member said before then. The rest is searched and shown with contact
// details redacted (memory.RedactContactOrID) and the review's output rules
// applied. The best 6 hits, each with 2 messages of context on either side
// and overlapping ones merged, are returned in time order as
// "[MM-DD HH:MM] 名字：正文" lines, at most 1500 runes, without any IDs.
func NewRecall(d RecallDeps) skills.Tool { return &recallTool{d: d} }

// MaxResultRunes declares recall's limit on what it returns.
func (*recallTool) MaxResultRunes() int { return recallMaxRunes }

func (*recallTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "recall",
		Description: "在当前会话以前的聊天记录里按关键词查找。用户问到以前聊过、但眼前的对话和你记得的事里都没有的内容时调用，" +
			"查不到就老实说不记得。除了 query，最好在 keywords 里再给几个同义或相关的说法，" +
			"比如找“旅游”时给“旅游 / 旅行 / 出去玩”，这样换了说法的记录也能找到。" +
			"返回相关的几段聊天记录，每行是“[月-日 时:分] 名字：内容”，“你”是你自己说的话。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"要查找的关键词或短句，例如“草莓 蛋糕”，1 到 100 字","minLength":1,"maxLength":100},` +
			`"keywords":{"type":"array","description":"可选：同义或相关的说法，每个整词匹配，例如 [\"旅行\",\"出去玩\"]",` +
			`"items":{"type":"string","minLength":1,"maxLength":20},"maxItems":8}},` +
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
	pv, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆。", nil
	}
	terms := searchTerms(query, keywords)
	if len(terms) == 0 {
		return recallNotFound, nil
	}
	msgs, err := t.d.Store.RecentMessages(ctx, inv.ConversationKey, recallScan)
	if err != nil {
		return "", err
	}
	members, err := t.d.Store.ListMemberMemories(ctx, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	msgs = withoutCommands(msgs, t.isCommand(pv.Policy.Reply.CommandPrefix))
	msgs = withoutCurrentTurn(visible(msgs, members, inv), inv)
	entries := t.entries(msgs, pv.Policy)
	groups := recallGroups(entries, terms)
	if len(groups) == 0 {
		return recallNotFound, nil
	}
	loc := inv.Location
	if loc == nil {
		loc = time.UTC
	}
	return renderGroups(entries, groups, loc), nil
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

// searchTerms is the lower-cased keywords, each matched whole, followed by
// recallTokens(query), without repeats and at most recallMaxTerms, so that
// a long query never crowds out the keywords.
func searchTerms(query string, keywords []string) []string {
	var terms []string
	for _, k := range keywords {
		if k = strings.ToLower(k); !slices.Contains(terms, k) {
			terms = append(terms, k)
		}
	}
	for _, tok := range recallTokens(query) {
		if !slices.Contains(terms, tok) {
			terms = append(terms, tok)
		}
	}
	return terms[:min(len(terms), recallMaxTerms)]
}

// recallTokens splits query into at most recallMaxTokens distinct search
// terms in order: every two adjacent characters of each run of Han characters
// and every lower-cased run of ASCII letters or digits. A lone Han character
// is a term only when the query has no other terms.
func recallTokens(query string) []string {
	var tokens, singles []string
	add := func(list *[]string, tok string) {
		if !slices.Contains(*list, tok) {
			*list = append(*list, tok)
		}
	}
	runes := []rune(query)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case unicode.Is(unicode.Han, r):
			j := i
			for j < len(runes) && unicode.Is(unicode.Han, runes[j]) {
				j++
			}
			if j-i == 1 {
				add(&singles, string(runes[i]))
			}
			for k := i; k+1 < j; k++ {
				add(&tokens, string(runes[k:k+2]))
			}
			i = j
		case isASCIIAlnum(r):
			j := i
			for j < len(runes) && isASCIIAlnum(runes[j]) {
				j++
			}
			add(&tokens, strings.ToLower(string(runes[i:j])))
			i = j
		default:
			i++
		}
	}
	if len(tokens) == 0 {
		tokens = singles
	}
	if len(tokens) > recallMaxTokens {
		tokens = tokens[:recallMaxTokens]
	}
	return tokens
}

func isASCIIAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
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

// recallEntry is a message as recall may show it: name and body cleaned,
// redacted and reviewed, and search the lower-cased body.
type recallEntry struct {
	at                 time.Time
	name, body, search string
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
// command, when that comes within recallReplyWindow. It runs before
// forgetting hides a member's commands, so their replies go too.
func withoutCommands(msgs []store.MessageRecord, isCommand func(string) bool) []store.MessageRecord {
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
			pending = pending[1:]
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
		out = append(out, recallEntry{at: m.OccurredAt, name: recallName(m), body: body, search: strings.ToLower(body)})
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

// recallGroup is a run of entries, by index, around one or more hits; rank
// is the best rank of its hits (0 is best) and hits are their indexes.
type recallGroup struct {
	from, to int
	rank     int
	hits     []int
}

// recallGroups finds the recallHits entries containing the most distinct
// terms, newest first among equals, and returns the runs of context around
// them, merged where they touch or overlap, in time order.
func recallGroups(entries []recallEntry, terms []string) []recallGroup {
	type hit struct{ index, score int }
	var hits []hit
	for i, e := range entries {
		score := 0
		for _, term := range terms {
			if strings.Contains(e.search, term) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, hit{i, score})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.index, a.index))
	})
	hits = hits[:min(len(hits), recallHits)]

	var groups []recallGroup
	for rank, h := range hits {
		groups = append(groups, recallGroup{from: max(h.index-recallContext, 0),
			to: min(h.index+recallContext, len(entries)-1), rank: rank, hits: []int{h.index}})
	}
	slices.SortFunc(groups, func(a, b recallGroup) int { return cmp.Compare(a.from, b.from) })
	var merged []recallGroup
	for _, g := range groups {
		if n := len(merged); n > 0 && g.from <= merged[n-1].to+1 {
			last := &merged[n-1]
			last.to = max(last.to, g.to)
			last.rank = min(last.rank, g.rank)
			last.hits = append(last.hits, g.hits...)
			continue
		}
		merged = append(merged, g)
	}
	return merged
}

// renderGroups renders groups, blank-line separated, within recallMaxRunes.
// Groups are filled best rank first; a group that does not fit keeps its
// hits first and then the context nearest to them.
func renderGroups(entries []recallEntry, groups []recallGroup, loc *time.Location) string {
	order := make([]int, len(groups))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(groups[a].rank, groups[b].rank) })

	kept := make([][]int, len(groups))
	lines := map[int]string{}
	used := 0
	for _, gi := range order {
		g := groups[gi]
		candidates := make([]int, 0, g.to-g.from+1)
		for i := g.from; i <= g.to; i++ {
			candidates = append(candidates, i)
		}
		slices.SortStableFunc(candidates, func(a, b int) int {
			return cmp.Compare(distance(a, g.hits), distance(b, g.hits))
		})
		for _, i := range candidates {
			line := recallLine(entries[i], loc)
			cost := utf8.RuneCountInString(line)
			if used > 0 {
				cost++
				if len(kept[gi]) == 0 {
					cost++
				}
			}
			if used+cost > recallMaxRunes {
				continue
			}
			used += cost
			lines[i] = line
			kept[gi] = append(kept[gi], i)
		}
	}

	var parts []string
	for gi := range groups {
		if len(kept[gi]) == 0 {
			continue
		}
		slices.Sort(kept[gi])
		var b strings.Builder
		for n, i := range kept[gi] {
			if n > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(lines[i])
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n")
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
