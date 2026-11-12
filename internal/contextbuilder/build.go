package contextbuilder

import (
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
)

const replyTargetMaxRunes = 100

// Input describes one reply's context. History is ordered oldest to newest
// and excludes Current. MaxRecent <= 0 means no count limit on History.
type Input struct {
	SystemPrompt, ConversationType string
	History, Current               []store.MessageRecord
	ReplyTarget                    *store.MessageRecord
	Budget, MaxRecent              int
	TokenFactor                    float64
	Location                       *time.Location
}

type Output struct {
	Messages                                         []openai.ChatMessage
	EstimatedTokens, IncludedHistory, DroppedHistory int
}

var defaultLocation = sync.OnceValue(func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.UTC
	}
	return loc
})

// BaseTokens estimates the system prompt plus the current input (including
// the reply target), which are always sent regardless of Budget.
func BaseTokens(in Input) int {
	n := Estimate(renderCurrent(in), in.TokenFactor) + MessageOverhead
	if in.SystemPrompt != "" {
		n += Estimate(in.SystemPrompt, in.TokenFactor) + MessageOverhead
	}
	return n
}

// Build fills History from newest to oldest into the budget left after
// BaseTokens and returns the messages in chronological order. The system
// prompt and current input are never dropped.
func Build(in Input) Output {
	loc := in.Location
	if loc == nil {
		loc = defaultLocation()
	}

	type entry struct{ role, content string }
	remaining := in.Budget - BaseTokens(in)
	start := len(in.History)
	var picked []entry
	for i := len(in.History) - 1; i >= 0; i-- {
		if in.MaxRecent > 0 && len(picked) >= in.MaxRecent {
			break
		}
		role, content := renderHistory(in.History[i], loc)
		cost := Estimate(content, in.TokenFactor) + MessageOverhead
		if cost > remaining {
			break
		}
		remaining -= cost
		picked = append(picked, entry{role, content})
		start = i
	}

	type group struct {
		role  string
		lines []string
		count int
	}
	var groups []group
	for i := len(picked) - 1; i >= 0; i-- {
		e := picked[i]
		if n := len(groups); n > 0 && groups[n-1].role == e.role {
			groups[n-1].lines = append(groups[n-1].lines, e.content)
			groups[n-1].count++
			continue
		}
		groups = append(groups, group{role: e.role, lines: []string{e.content}, count: 1})
	}

	included := len(in.History) - start
	if len(groups) > 0 && groups[0].role == "assistant" {
		included -= groups[0].count
		groups = groups[1:]
	}

	var out Output
	if in.SystemPrompt != "" {
		out.Messages = append(out.Messages, openai.ChatMessage{Role: "system", Content: in.SystemPrompt})
	}
	for _, g := range groups {
		out.Messages = append(out.Messages, openai.ChatMessage{Role: g.role, Content: strings.Join(g.lines, "\n")})
	}
	out.Messages = append(out.Messages, openai.ChatMessage{Role: "user", Content: renderCurrent(in)})

	for _, m := range out.Messages {
		out.EstimatedTokens += Estimate(m.Content, in.TokenFactor) + MessageOverhead
	}
	out.IncludedHistory = included
	out.DroppedHistory = len(in.History) - included
	return out
}

func renderHistory(rec store.MessageRecord, loc *time.Location) (role, content string) {
	if rec.FromBot {
		return "assistant", renderBody(rec)
	}
	return "user", "[" + rec.OccurredAt.In(loc).Format("15:04") + "] " + displayName(rec) + "：" + renderBody(rec)
}

func renderCurrent(in Input) string {
	speaker := ""
	if len(in.Current) > 0 {
		speaker = displayName(in.Current[0])
	}
	lines := []string{"【当前消息】发送者：" + speaker}
	if in.ReplyTarget != nil {
		body := []rune(renderBody(*in.ReplyTarget))
		if len(body) > replyTargetMaxRunes {
			body = body[:replyTargetMaxRunes]
		}
		lines = append(lines, "（回复 "+displayName(*in.ReplyTarget)+" 的消息："+string(body)+"）")
	}
	for _, rec := range in.Current {
		lines = append(lines, renderBody(rec))
	}
	return strings.Join(lines, "\n")
}

func renderBody(rec store.MessageRecord) string {
	var b strings.Builder
	b.WriteString(rec.Text)
	for _, a := range rec.Attachments {
		if a.Type == "image" {
			b.WriteString("[图片]")
		} else {
			b.WriteString("[文件]")
		}
	}
	if b.Len() == 0 {
		return "[空消息]"
	}
	return b.String()
}

func displayName(rec store.MessageRecord) string {
	if rec.DisplayName != "" {
		return rec.DisplayName
	}
	return rec.PlatformUserID
}
