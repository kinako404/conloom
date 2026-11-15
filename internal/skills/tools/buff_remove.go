package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	removeBuffNoName  = "需要提供名字（name）。"
	removeBuffFixed   = "这一条是固定的，不能删。"
	removeBuffTwice   = "这一条这次已经删过了。"
	removeBuffNothing = "没有这一条。现在没有自建的状态条或效果。"
)

type removeBuffTool struct {
	st       BuffStore
	personas PersonaSource
}

// NewRemoveBuff returns the remove_buff tool. In a private chat it removes,
// from the chat's memory scope (memory.ScopeKey, shared by a bot's panel
// chats), the bar the bot added under the name, with its value, or else the
// effect of that name still active; one that has run out counts as gone. The
// persona's bars are refused. A task removes each name at most once, so a
// re-run task does not remove what another task added back. An unknown name
// is answered with the bot's bars and active effects.
func NewRemoveBuff(st BuffStore, personas PersonaSource) skills.Tool {
	return &removeBuffTool{st: st, personas: personas}
}

// MaxResultRunes declares that remove_buff answers with one sentence.
func (*removeBuffTool) MaxResultRunes() int { return shortResultRunes }

func (*removeBuffTool) Spec() skills.Spec {
	return skills.Spec{
		Name:        "remove_buff",
		Description: "删掉你自己加的一条状态条，或提前去掉一个还在生效的效果。不再相关时调用；人设里固定的状态条不能删。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"要删的状态条或效果的名字，要和你的状态里写的完全一致","minLength":1,"maxLength":6}},` +
			`"required":["name"]}`),
	}
}

func (t *removeBuffTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	if inv.ConversationType != conloomapi.ConvPrivate {
		return privateOnly, nil
	}
	name, _ := stringArg(args, "name")
	if name = strings.TrimSpace(name); name == "" {
		return removeBuffNoName, nil
	}
	doc, err := t.personas.Persona(ctx, inv.BotInstanceID, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	if isPersonaBar(doc, name) {
		return removeBuffFixed, nil
	}
	now, _ := clock(inv)
	scope := memory.ScopeKeyOf(inv.ConversationKey)
	removed, err := t.st.RemoveCustomBar(ctx, scope, name, store.BuffSourceModel, inv.TaskID, now)
	switch {
	case errors.Is(err, store.ErrBuffDuplicate):
		return removeBuffTwice, nil
	case err != nil:
		return "", err
	case removed:
		return "已删掉状态条：" + name + "。", nil
	}
	effects, err := t.st.ListEffects(ctx, scope, now)
	if err != nil {
		return "", err
	}
	if slices.ContainsFunc(effects, func(e store.Effect) bool { return e.Name == name }) {
		removed, err := t.st.RemoveEffect(ctx, scope, name, store.BuffSourceModel, inv.TaskID, now)
		switch {
		case errors.Is(err, store.ErrBuffDuplicate):
			return removeBuffTwice, nil
		case err != nil:
			return "", err
		case removed:
			return "已去掉效果：" + name + "。", nil
		}
	}
	return t.unknown(ctx, doc, scope, now)
}

// unknown answers a name the scope has neither as a bar the bot added nor as
// an active effect at now, naming those it has.
func (t *removeBuffTool) unknown(ctx context.Context, doc conloomapi.PersonaDoc, scope string, now time.Time) (string, error) {
	bars, err := scopeBars(ctx, t.st, doc, scope)
	if err != nil {
		return "", err
	}
	effects, err := t.st.ListEffects(ctx, scope, now)
	if err != nil {
		return "", err
	}
	var parts []string
	if custom := bars[len(doc.EffectiveBars()):]; len(custom) > 0 {
		parts = append(parts, "自建状态条："+joinBarNames(custom))
	}
	if len(effects) > 0 {
		names := make([]string, len(effects))
		for i, e := range effects {
			names[i] = e.Name
		}
		parts = append(parts, "效果："+strings.Join(names, "、"))
	}
	if len(parts) == 0 {
		return removeBuffNothing, nil
	}
	return "没有这一条。" + strings.Join(parts, "；") + "。", nil
}
