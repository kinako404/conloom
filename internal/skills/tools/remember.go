package tools

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

// rememberCues are what the speaker's own trigger message must contain for
// remember to write: words of asking to be remembered.
var rememberCues = []string{"记住", "记一下", "记着", "记下", "别忘", "帮我记", "remember"}

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
			DisplayName: inv.DisplayName, Content: content, Review: pv.Policy.Review, Reason: memory.ReasonRememberTool})
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
// was sent by the speaker in its conversation and contains a rememberCue, so
// that what others said, or text the model was led by, cannot make it write.
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
		text := strings.ToLower(m.Text)
		for _, cue := range rememberCues {
			if strings.Contains(text, cue) {
				return true, nil
			}
		}
	}
	return false, nil
}
