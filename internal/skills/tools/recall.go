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

	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	recallScan       = 5000
	recallQueryRunes = 100
	recallMaxTokens  = 8
	recallHits       = 6
	recallContext    = 2
	recallMaxRunes   = 1500
	recallBodyRunes  = 200
	recallNotFound   = "没有找到相关的聊天记录。"
	recallNoName     = "一位群友"
)

// RecallStore is the part of store.Store that recall reads.
type RecallStore interface {
	RecentMessages(ctx context.Context, conversationKey string, limit int) ([]store.MessageRecord, error)
	ListMemberMemories(ctx context.Context, convKey string) ([]store.MemberMemory, error)
}

// RecallDeps are what recall needs.
type RecallDeps struct {
	Store    RecallStore
	Policies PolicySource
}

type recallTool struct{ d RecallDeps }

// NewRecall returns the recall tool: a keyword search of the newest 5000
// messages of the invocation's conversation when memory is in effect. Messages
// hidden by forgetting are skipped: in private chats everything before the
// speaker was forgotten, in groups what each forgotten member said before
// then. So is the current turn, the speaker's messages after the bot's last
// one. The best 6 hits, each with 2 messages of context on either side and
// overlapping ones merged, are returned in time order as
// "[MM-DD HH:MM] 名字：正文" lines, at most 1500 runes, without any IDs.
func NewRecall(d RecallDeps) skills.Tool { return &recallTool{d: d} }

func (*recallTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "recall",
		Description: "在当前会话以前的聊天记录里按关键词查找。用户问到以前聊过、但眼前的对话和你记得的事里都没有的内容时调用，" +
			"查不到就老实说不记得。返回相关的几段聊天记录，每行是“[月-日 时:分] 名字：内容”，“你”是你自己说的话。",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string",` +
			`"description":"要查找的关键词或短句，例如“草莓 蛋糕”，1 到 100 字","minLength":1,"maxLength":100}},` +
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
	_, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆。", nil
	}
	tokens := recallTokens(query)
	if len(tokens) == 0 {
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
	msgs = withoutCurrentTurn(visible(msgs, members, inv), inv.PlatformUserID)
	groups := recallGroups(msgs, tokens)
	if len(groups) == 0 {
		return recallNotFound, nil
	}
	loc := inv.Location
	if loc == nil {
		loc = time.UTC
	}
	return renderGroups(msgs, groups, loc), nil
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

// withoutCurrentTurn drops the speaker's messages after the bot's last one:
// the turn being answered, which the model already sees. Without a bot
// message only the speaker's trailing run of messages is dropped.
func withoutCurrentTurn(msgs []store.MessageRecord, userID string) []store.MessageRecord {
	start := len(msgs)
	for start > 0 && !msgs[start-1].FromBot {
		start--
	}
	if start == 0 {
		for start = len(msgs); start > 0 && msgs[start-1].PlatformUserID == userID; start-- {
		}
	}
	turn := msgs[start:]
	kept := slices.DeleteFunc(slices.Clone(turn), func(m store.MessageRecord) bool {
		return !m.FromBot && m.PlatformUserID == userID
	})
	return append(msgs[:start:start], kept...)
}

// recallGroup is a run of messages, by index, around one or more hits; rank
// is the best rank of its hits (0 is best) and hits are their indexes.
type recallGroup struct {
	from, to int
	rank     int
	hits     []int
}

// recallGroups finds the recallHits messages containing the most distinct
// tokens, newest first among equals, and returns the runs of context around
// them, merged where they touch or overlap, in time order.
func recallGroups(msgs []store.MessageRecord, tokens []string) []recallGroup {
	type hit struct{ index, score int }
	var hits []hit
	for i, m := range msgs {
		text := strings.ToLower(m.Text)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(text, tok) {
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
			to: min(h.index+recallContext, len(msgs)-1), rank: rank, hits: []int{h.index}})
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
func renderGroups(msgs []store.MessageRecord, groups []recallGroup, loc *time.Location) string {
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
			line := recallLine(msgs[i], loc)
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

// recallLine renders m as "[MM-DD HH:MM] 名字：正文" on one line: the bot is
// "你", a member without a usable name recallNoName, and the body is cut to
// recallBodyRunes runes.
func recallLine(m store.MessageRecord, loc *time.Location) string {
	name := "你"
	if !m.FromBot {
		name = strings.Join(strings.Fields(m.DisplayName), " ")
		if name == "" || name == m.PlatformUserID || name == m.ActorID {
			name = recallNoName
		}
	}
	body := m.Text
	for _, a := range m.Attachments {
		if a.Type == "image" {
			body += "[图片]"
		} else {
			body += "[文件]"
		}
	}
	body = strings.Join(strings.Fields(body), " ")
	if body == "" {
		body = "（没有文字内容）"
	}
	if r := []rune(body); len(r) > recallBodyRunes {
		body = string(r[:recallBodyRunes]) + "…"
	}
	return "[" + m.OccurredAt.In(loc).Format("01-02 15:04") + "] " + name + "：" + body
}
