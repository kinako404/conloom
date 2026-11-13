package tools

import (
	"context"
	"encoding/json"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/skills"
)

// RememberDeps are what remember needs. Metrics may be nil.
type RememberDeps struct {
	Store    memory.MemberStore
	Policies PolicySource
	Reviewer memory.Reviewer
	Metrics  *obs.Metrics
}

type rememberTool struct{ d RememberDeps }

// NewRemember returns the remember tool: it remembers content for the
// speaker exactly as /记住 does (memory.Remember) when memory is in effect,
// and answers with one sentence saying whether it did and why not.
func NewRemember(d RememberDeps) skills.Tool { return &rememberTool{d: d} }

func (*rememberTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "remember",
		Description: "记住当前说话的用户明确让你记住的一件关于他自己的事，以后聊天时你会记得。" +
			"只在用户明确要求你记住时调用；联系方式、证件号、别人的隐私不要记。只能记在当前说话的人名下。",
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
	pv, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆，没有记下。", nil
	}
	status, err := memory.Remember(ctx, memory.RememberDeps{Store: t.d.Store, Reviewer: t.d.Reviewer, Metrics: t.d.Metrics},
		memory.RememberRequest{ConversationKey: inv.ConversationKey, UserID: inv.PlatformUserID,
			DisplayName: inv.DisplayName, Content: content, Review: pv.Policy.Review})
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
