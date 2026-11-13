package tools

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

// AffinityStore is the part of store.Store that get_affinity reads.
type AffinityStore interface {
	GetMemberMemories(ctx context.Context, convKey string, userIDs []string) (map[string]store.MemberMemory, error)
}

// AffinityDeps are what get_affinity needs.
type AffinityDeps struct {
	Store    AffinityStore
	Policies PolicySource
}

type affinityTool struct{ d AffinityDeps }

// NewGetAffinity returns the get_affinity tool: the speaker's affinity tier
// and value in the conversation, such as "好感：熟悉（52/100）". It takes no
// arguments and ignores any it is given.
func NewGetAffinity(d AffinityDeps) skills.Tool { return &affinityTool{d: d} }

// MaxResultRunes declares that get_affinity answers with one sentence.
func (*affinityTool) MaxResultRunes() int { return shortResultRunes }

func (*affinityTool) Spec() skills.Spec {
	return skills.Spec{
		Name:        "get_affinity",
		Description: "查看你对当前说话的用户的好感度（等级和 0–100 的数值）。用户问你对他的好感、你们有多熟时调用。没有参数，只能查当前说话的人。",
		Parameters:  noArguments,
	}
}

func (t *affinityTool) Call(ctx context.Context, inv skills.Invocation, _ json.RawMessage) (string, error) {
	pv, on, err := memoryOn(ctx, t.d.Policies, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if !on {
		return "这个会话没有开启记忆。", nil
	}
	if !pv.Policy.Memory.Affinity {
		return "这个会话没有开启好感度。", nil
	}
	rows, err := t.d.Store.GetMemberMemories(ctx, inv.ConversationKey, []string{inv.PlatformUserID})
	if err != nil {
		return "", err
	}
	m, ok := rows[inv.PlatformUserID]
	if !ok || m.Affinity == nil {
		return "你们还不太熟，还没有好感度。", nil
	}
	return "好感：" + memory.TierName(*m.Affinity) + "（" + strconv.Itoa(*m.Affinity) + "/100）", nil
}
