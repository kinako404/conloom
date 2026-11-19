package tools

import (
	"context"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var sceneLongPlace = "地点（place）超过 " + strconv.Itoa(conloomapi.ScenePlaceMaxRunes) + " 字，写短一点。"

const (
	sceneBadMode   = "mode 只能是 online（线上）或 in_person（面对面）。"
	sceneRefused   = "这个地点不能用。"
	sceneUnchanged = "场景没有变化。"
)

type setSceneTool struct {
	st       SceneStore
	policies PolicySource
	personas PersonaSource
	reviewer Reviewer
}

// NewSetScene returns the set_scene tool. In a private chat it records
// whether the bot and the person are talking online or in person, with an
// optional place for in person (CleanActivity'd, at most
// conloomapi.ScenePlaceMaxRunes runes, through OutputProtected unless the
// persona's content mode is open, then Output; dropped for online), as the
// scene of the chat's memory scope (memory.ScopeKey, shared by a bot's panel
// chats) set in the chat, from now. Setting the current scene again only
// touches it. Its answer confirms the scene in one short sentence.
func NewSetScene(st SceneStore, pol PolicySource, personas PersonaSource, rv Reviewer) skills.Tool {
	return &setSceneTool{st: st, policies: pol, personas: personas, reviewer: rv}
}

// MaxResultRunes declares that set_scene answers with one sentence.
func (*setSceneTool) MaxResultRunes() int { return shortResultRunes }

func (*setSceneTool) ChangesState() bool { return true }

func (*setSceneTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "set_scene",
		Description: "记下你们现在的场景：online 是隔着聊天软件发消息，in_person 是面对面在一起（可以写在哪里）。" +
			"只在聊天里明确说了见面、到了或者分开时才切换，不要每轮都调用。只在私聊里用。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"mode":{"type":"string","description":"online：隔着聊天软件发消息；in_person：面对面在一起","enum":["online","in_person"]},` +
			`"place":{"type":"string","description":"面对面时在哪里，最多 16 字，例如“食堂”；可以不写，online 时不用写","maxLength":16}},` +
			`"required":["mode"]}`),
	}
}

func (t *setSceneTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	if inv.ConversationType != conloomapi.ConvPrivate {
		return privateOnly, nil
	}
	mode, _ := stringArg(args, "mode")
	if mode != conloomapi.SceneOnline && mode != conloomapi.SceneInPerson {
		return sceneBadMode, nil
	}
	var place string
	if mode == conloomapi.SceneInPerson {
		raw, _ := stringArg(args, "place")
		place = CleanActivity(raw)
	}
	if utf8.RuneCountInString(place) > conloomapi.ScenePlaceMaxRunes {
		return sceneLongPlace, nil
	}
	if place != "" {
		pv, err := t.policies.Current(ctx, inv.ConversationKey)
		if err != nil {
			return "", err
		}
		doc, err := t.personas.Persona(ctx, inv.BotInstanceID, inv.ConversationKey)
		if err != nil {
			return "", err
		}
		check := t.reviewer.OutputProtected
		if conloomapi.ModeFor(doc, inv.ConversationType) == conloomapi.ContentOpen {
			check = t.reviewer.Output
		}
		res := check(place, pv.Policy.Review)
		if place = CleanActivity(res.Text); res.Verdict == review.VerdictBlock || place == "" {
			return sceneRefused, nil
		}
	}
	now, _ := clock(inv)
	changed, err := t.st.SetScene(ctx, store.ConversationScene{ConversationKey: memory.ScopeKeyOf(inv.ConversationKey),
		OriginConversationKey: inv.ConversationKey, Mode: mode, Place: place, SinceAt: now, TouchedAt: now,
		SwitchTaskID: inv.TaskID})
	if err != nil {
		return "", err
	}
	switch {
	case !changed:
		return sceneUnchanged, nil
	case mode == conloomapi.SceneOnline:
		return "已记下：线上。", nil
	case place == "":
		return "已记下：面对面。", nil
	}
	return "已记下：面对面，在" + place + "。", nil
}
