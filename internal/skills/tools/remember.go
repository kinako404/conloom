package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

// rememberCues are the imperatives one of the speaker's own trigger messages
// must use, outside a question, for remember to write.
var rememberCues = []string{"记住", "帮我记", "记一下", "记下来", "别忘了", "remember"}

// clauseEnds end a clause.
const clauseEnds = "。！!？?；;，,～~…\n"

// Question tones: after a cue (and 了, 过 or 得) they make it a question
// about remembering, as in "记住了吗" or "记住没有", rather than a request.
// Ending a clause, they make one that mentions recallTerms, or ends in 了
// and a tone ("你记住我说的话了吗"), a question about what was remembered,
// unless requestWords ask for it ("帮我记着…好吗", "能记一下我结婚了吗").
var (
	cueAspects     = []string{"了", "过", "得"}
	questionTones  = []string{"吗", "嘛", "么", "呢", "没", "?", "？"}
	recallingAsked = []string{"记不记", "记没记", "do you remember", "did you remember", "don't you remember"}
	recallTerms    = []string{"记得", "记着"}
	closingTones   = []string{"吗", "嘛", "么", "呢", "没", "没有"}
)

// cueNegators right before a cue, past cueFillers such as 帮我 or 再, ask not
// to remember, as in "别记住" or "不用帮我记"; "没让你记住" says it was never
// asked.
var (
	cueNegators = []string{"别", "不要", "不用", "不必", "无需", "无须", "不需要", "没必要", "用不着", "没", "没有", "不让",
		"don't", "don’t", "dont", "do not", "never", "no need to"}
	cueFillers = []string{"帮我", "给我", "替我", "你", "再", "去", "让"}
)

// requestWords right before a cue or a 记着, past cueFillers, make a polite
// request of it; notRequests end like one but are not ("你可能记住了吗").
var (
	requestWords = []string{"能", "可以", "帮我", "麻烦", "请"}
	notRequests  = []string{"可能", "不能", "才能", "只能", "不可以"}
)

// asksToRemember reports whether text has a clause that uses a rememberCue
// as a request: "记住我喜欢草莓" or the polite "帮我记一下好吗" and
// "你能记住我生日是5月1日吗", but not "记住了吗", "你记不记得", "别记住这个"
// or "你还记得我让你记住的事吗".
func asksToRemember(text string) bool {
	text = strings.ToLower(text)
	for len(text) > 0 {
		end := strings.IndexAny(text, clauseEnds)
		clause, rest := text, ""
		if end >= 0 {
			_, size := utf8.DecodeRuneInString(text[end:])
			clause, rest = text[:end+size], text[end+size:]
		}
		text = rest
		if requestsToRemember(clause) {
			return true
		}
	}
	return false
}

// requestsToRemember reports whether clause uses a rememberCue that is not
// asking about remembering: the clause asks neither "记不记得" nor about what
// was remembered (asksRecall; a clause ending in 了 and a question only
// does so unless the cue follows requestWords), and the cue is not negated
// (negated), not followed, after an optional 了, 过 or 得, by a question tone,
// and not followed by 的, which makes it describe a thing ("记住的事").
func requestsToRemember(clause string) bool {
	if slices.ContainsFunc(recallingAsked, func(q string) bool { return strings.Contains(clause, q) }) {
		return false
	}
	recall, endsInLe := asksRecall(clause)
	if recall {
		return false
	}
	for _, cue := range rememberCues {
		for start := 0; ; {
			i := strings.Index(clause[start:], cue)
			if i < 0 {
				break
			}
			before := clause[:start+i]
			start += i + len(cue)
			if after := clause[start:]; !negated(before) && !(endsInLe && !requested(before)) && !asksAbout(after) &&
				!strings.HasPrefix(strings.TrimLeft(after, " "), "的") {
				return true
			}
		}
	}
	return false
}

// asksAbout reports whether after, the text right after a cue, turns it into a
// question: an optional aspect particle, then a question tone.
func asksAbout(after string) bool {
	after = strings.TrimLeft(after, " ")
	for _, a := range cueAspects {
		if rest, ok := strings.CutPrefix(after, a); ok {
			after = strings.TrimLeft(rest, " ")
			break
		}
	}
	return slices.ContainsFunc(questionTones, func(t string) bool { return strings.HasPrefix(after, t) })
}

// asksRecall reports, for a clause ending in a question mark or a closing
// tone, whether it is a question about what was remembered because it
// mentions one of recallTerms ("你还记得…吗"; a 记着 after requestWords, as in
// "帮我记着…好吗", does not count), and whether it ends in 了 and a question
// mark or a closing tone other than 呢 ("…了吗", "…了？"; "…了呢" is mostly
// said, not asked), which asks about what was remembered unless it is a
// request (see requestsToRemember).
func asksRecall(clause string) (recall, endsInLe bool) {
	clause = strings.TrimRight(clause, " ")
	marked := strings.HasSuffix(clause, "?") || strings.HasSuffix(clause, "？")
	body := strings.TrimRight(clause, clauseEnds+" ")
	tone := slices.IndexFunc(closingTones, func(t string) bool { return strings.HasSuffix(body, t) })
	if !marked && tone < 0 {
		return false, false
	}
	if mentionsRecall(clause) {
		return true, false
	}
	if tone >= 0 {
		if closingTones[tone] == "呢" {
			return false, false
		}
		body = strings.TrimSuffix(body, closingTones[tone])
	}
	return false, strings.HasSuffix(body, "了")
}

// mentionsRecall reports whether clause has one of recallTerms other than a
// 记着 right after requestWords ("帮我记着").
func mentionsRecall(clause string) bool {
	for _, term := range recallTerms {
		for start := 0; ; {
			i := strings.Index(clause[start:], term)
			if i < 0 {
				break
			}
			before := clause[:start+i]
			start += i + len(term)
			if term != "记着" || !requested(before) {
				return true
			}
		}
	}
	return false
}

// requested reports whether before, the clause up to a cue or a 记着, ends in
// one of requestWords (or 能不能), past any cueFillers.
func requested(before string) bool {
	for {
		before = strings.TrimRight(before, " ")
		switch {
		case strings.HasSuffix(before, "能不能"):
			return true
		case slices.ContainsFunc(notRequests, func(n string) bool { return strings.HasSuffix(before, n) }):
			return false
		case slices.ContainsFunc(requestWords, func(r string) bool { return strings.HasSuffix(before, r) }):
			return true
		}
		i := slices.IndexFunc(cueFillers, func(f string) bool { return strings.HasSuffix(before, f) })
		if i < 0 {
			return false
		}
		before = strings.TrimSuffix(before, cueFillers[i])
	}
}

// negated reports whether before, the clause up to a cue, ends in one of
// cueNegators, past any cueFillers; adverbs that end like one, such as 特别,
// and words that end like an English one, such as whenever, do not count.
func negated(before string) bool {
	for {
		before = strings.TrimRight(before, " ")
		if strings.HasSuffix(before, "特别") || strings.HasSuffix(before, "分别") {
			return false
		}
		if slices.ContainsFunc(cueNegators, func(n string) bool { return endsWithWord(before, n) }) {
			return true
		}
		i := slices.IndexFunc(cueFillers, func(f string) bool { return strings.HasSuffix(before, f) })
		if i < 0 {
			return false
		}
		before = strings.TrimSuffix(before, cueFillers[i])
	}
}

// endsWithWord reports whether s ends with word and, if word starts with an
// ASCII letter, no ASCII letter comes right before it.
func endsWithWord(s, word string) bool {
	rest, ok := strings.CutSuffix(s, word)
	return ok && (rest == "" || !isASCIILetter(word[0]) || !isASCIILetter(rest[len(rest)-1]))
}

func isASCIILetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// RememberStore is the part of store.Store that remember uses.
type RememberStore interface {
	memory.MemberStore
	GetMessages(ctx context.Context, botID string, ids []string) ([]store.MessageRecord, error)
}

// RememberDeps are what remember needs. Metrics may be nil.
type RememberDeps struct {
	Store    RememberStore
	Policies PolicySource
	Reviewer memory.Reviewer
	Metrics  *obs.Metrics
}

type rememberTool struct{ d RememberDeps }

// NewRemember returns the remember tool: it remembers content for the
// speaker as /记住 does (memory.Remember, with reason
// memory.ReasonRememberTool) when memory is in effect and one of the
// speaker's own trigger messages asks to be remembered, and answers with one
// sentence saying whether it did and why not.
func NewRemember(d RememberDeps) skills.Tool { return &rememberTool{d: d} }

// MaxResultRunes declares that remember answers with one sentence.
func (*rememberTool) MaxResultRunes() int { return shortResultRunes }

func (*rememberTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "remember",
		Description: "记住当前说话的用户明确让你记住的一件关于他自己的事，以后聊天时你会记得。" +
			"只在用户自己明确要求你记住时调用；联系方式、证件号、别人的隐私不要记。只能记在当前说话的人名下。",
		Parameters: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string",` +
			`"description":"要记住的事，用一句简短的话写清楚，1 到 100 字","minLength":1,"maxLength":100}},` +
			`"required":["content"]}`),
	}
}

func (t *rememberTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	content, ok := stringArg(args, "content")
	if !ok {
		return "需要提供要记住的内容（content）。", nil
	}
	switch n := utf8.RuneCountInString(strings.Join(strings.Fields(content), " ")); {
	case n == 0:
		return "内容为空，没有记下。", nil
	case n > memory.RememberMaxRunes:
		return "内容超过 100 字，没有记下。", nil
	}
	pv, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆，没有记下。", nil
	}
	if asked, err := t.askedToRemember(ctx, inv); err != nil {
		return "", err
	} else if !asked {
		return "只有对方自己明确让你记住时才能记。", nil
	}
	status, err := memory.Remember(ctx, memory.RememberDeps{Store: t.d.Store, Reviewer: t.d.Reviewer, Metrics: t.d.Metrics},
		memory.RememberRequest{ConversationKey: inv.ConversationKey, UserID: inv.PlatformUserID,
			DisplayName: inv.DisplayName, Content: content, Review: pv.Policy.Review, Memory: pv.Policy.Memory,
			Reason: memory.ReasonRememberTool})
	if err != nil {
		return "", err
	}
	switch status {
	case memory.RememberEmpty:
		return "内容为空，没有记下。", nil
	case memory.RememberTooLong:
		return "内容超过 100 字，没有记下。", nil
	case memory.RememberSensitive:
		return "联系方式、证件号这类信息不能记，没有记下。", nil
	case memory.RememberBlocked:
		return "这句话不能记，没有记下。", nil
	}
	return "已记住。", nil
}

// askedToRemember reports whether one of the invocation's trigger messages
// was sent by the speaker in its conversation and asks to remember (see
// asksToRemember), so that what others said, questions, or text the model was
// led by cannot make it write.
func (t *rememberTool) askedToRemember(ctx context.Context, inv skills.Invocation) (bool, error) {
	if len(inv.TriggerMessageIDs) == 0 {
		return false, nil
	}
	msgs, err := t.d.Store.GetMessages(ctx, inv.BotInstanceID, inv.TriggerMessageIDs)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		if m.FromBot || m.ConversationKey != inv.ConversationKey || m.PlatformUserID != inv.PlatformUserID {
			continue
		}
		if asksToRemember(m.Text) {
			return true, nil
		}
	}
	return false, nil
}
