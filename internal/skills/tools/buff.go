package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// buffMaxReasonRunes bounds a change's reason, after CleanActivity.
const buffMaxReasonRunes = 40

var (
	buffBadDelta   = "delta 需要是不为 0 的整数，不超过这一条的单次上限（默认 ±" + strconv.Itoa(conloomapi.DefaultBarMaxStep) + "）。"
	buffNoReason   = "需要简单写一下原因（reason），1 到 " + strconv.Itoa(buffMaxReasonRunes) + " 字。"
	buffLongReason = "原因（reason）超过 " + strconv.Itoa(buffMaxReasonRunes) + " 字，写短一点。"
)

const buffDuplicate = "这一条这次已经调过了。"

// errDayCapped stops a change that the daily limit cuts to nothing.
var errDayCapped = errors.New("tools: the bar's daily change limit is reached")

type adjustBuffTool struct {
	st       BuffStore
	personas PersonaSource
}

// NewAdjustBuff returns the adjust_buff tool. In a private chat it changes
// one of the bars of the chat's memory scope (memory.ScopeKey, shared by a
// bot's panel chats), the persona's or one the bot added (scopeBars), by a
// non-zero delta, cut to the bar's EffectiveMaxStep, on top of its value
// decayed to now with the bar's baseline and half-life (its baseline when
// none is stored), keeping the bar's net change on the local day within its
// EffectiveMaxDay and its value within 0–100. The limits, like the baseline
// and half-life, come from the persona of the conversation being answered; a
// bar the bot added has the defaults 10 and 30. A task adjusts each bar at
// most once. The answer names the bar's band before and after; a cut delta
// is reported with the limit that cut it and the change applied, and a delta
// the daily limit cuts to nothing is refused naming that limit. The answer
// never shows a value; an unknown name is answered with the bars there are.
func NewAdjustBuff(st BuffStore, personas PersonaSource) skills.Tool {
	return &adjustBuffTool{st: st, personas: personas}
}

// MaxResultRunes declares that adjust_buff answers with one sentence.
func (*adjustBuffTool) MaxResultRunes() int { return shortResultRunes }

func (*adjustBuffTool) ChangesState() bool { return true }

func (*adjustBuffTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "adjust_buff",
		Description: "调整你自己的一条状态条（比如心情、精力，或你自己加的）。聊天里发生了真正影响你的事时调用：" +
			"小事调一点，大事调多一些，最多到这一条的单次上限（默认 ±10；状态里写了“单次最多”的按那个数）；不要每句都调。" +
			"外在状态可以在动作和外表描写里自然表现，其他的只影响语气；都不要向对方说出数字，也不要提状态条或工具。返回调整前后的大致档位。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"状态条的名字，要和你的状态里写的完全一致，例如“心情”"},` +
			`"delta":{"type":"integer","description":"变化量，不能为 0；绝对值不超过这一条的单次上限（默认 10），超过的按上限算","minimum":-100,"maximum":100},` +
			`"reason":{"type":"string","description":"为什么变，1 到 40 字","minLength":1,"maxLength":40}},` +
			`"required":["name","delta","reason"]}`),
	}
}

func (t *adjustBuffTool) Call(ctx context.Context, inv skills.Invocation, args json.RawMessage) (string, error) {
	if inv.ConversationType != conloomapi.ConvPrivate {
		return privateOnly, nil
	}
	delta, ok := intArg(args, "delta")
	if !ok || delta == 0 {
		return buffBadDelta, nil
	}
	raw, _ := stringArg(args, "reason")
	reason := CleanActivity(raw)
	switch n := utf8.RuneCountInString(reason); {
	case n == 0:
		return buffNoReason, nil
	case n > buffMaxReasonRunes:
		return buffLongReason, nil
	}
	doc, err := t.personas.Persona(ctx, inv.BotInstanceID, inv.ConversationKey)
	if err != nil {
		return "", err
	}
	scope := memory.ScopeKeyOf(inv.ConversationKey)
	bars, err := scopeBars(ctx, t.st, doc, scope)
	if err != nil {
		return "", err
	}
	name, _ := stringArg(args, "name")
	name = strings.TrimSpace(name)
	i := slices.IndexFunc(bars, func(b conloomapi.PersonaBar) bool { return b.Name == name })
	if i < 0 {
		if name == "" {
			return "需要提供条名（name）。现有：" + joinBarNames(bars) + "。", nil
		}
		return "没有这一条。现有：" + joinBarNames(bars) + "。", nil
	}
	bar := bars[i]
	step, dayMax := bar.EffectiveMaxStep(), bar.EffectiveMaxDay()
	stepped := min(max(delta, -step), step)
	now, loc := clock(inv)
	day := now.In(loc).Format(time.DateOnly)
	var before float64
	var applied int
	after, err := t.st.UpdateBuff(ctx, scope, bar.Name,
		store.BuffEvent{TaskID: inv.TaskID, Source: store.BuffSourceModel, Reason: reason, CreatedAt: now},
		func(cur store.BuffValue, found bool) (store.BuffValue, float64, error) {
			before = float64(bar.Baseline)
			dayDelta := 0
			if found {
				before = autonomy.Decay(cur.Value, bar.Baseline, bar.HalfLife.Duration, now.Sub(cur.UpdatedAt))
				if cur.Day == day {
					dayDelta = cur.DayDelta
				}
			}
			d := dayCapped(stepped, dayDelta, dayMax)
			if d == 0 {
				return store.BuffValue{}, 0, errDayCapped
			}
			applied = d
			next := store.BuffValue{Value: autonomy.Clamp(before + float64(d)), Day: day, DayDelta: dayDelta + d, UpdatedAt: now}
			return next, before, nil
		})
	switch {
	case errors.Is(err, store.ErrBuffDuplicate):
		return buffDuplicate, nil
	case errors.Is(err, errDayCapped):
		return fmt.Sprintf("%s今天的变化已经到上限（每天最多 ±%d），这次没有调整。", bar.Name, dayMax), nil
	case err != nil:
		return "", err
	}
	res := bar.Name + "：" + autonomy.Band(before) + " → " + autonomy.Band(after.Value)
	switch {
	case applied != stepped:
		res += fmt.Sprintf("（今天的变化快到上限 ±%d，只调整了 %+d）", dayMax, applied)
	case stepped != delta:
		res += fmt.Sprintf("（超过单次上限 ±%d，按 %+d 调整）", step, stepped)
	}
	return res, nil
}

// scopeBars returns the bars of the memory scope scope: doc's
// (conloomapi.PersonaDoc.EffectiveBars), then, in the order they were added,
// the bot-created ones whose name none of doc's has, inner with the default
// limits; doc's bar wins a name.
func scopeBars(ctx context.Context, st BuffStore, doc conloomapi.PersonaDoc, scope string) ([]conloomapi.PersonaBar, error) {
	bars := doc.EffectiveBars()
	custom, err := st.ListCustomBars(ctx, scope)
	if err != nil {
		return nil, err
	}
	for _, c := range custom {
		if !isPersonaBar(doc, c.Name) {
			bars = append(bars, conloomapi.PersonaBar{Name: c.Name, Baseline: c.Baseline,
				HalfLife: conloomapi.Duration{Duration: c.HalfLife}, Description: c.Description})
		}
	}
	return bars, nil
}

// isPersonaBar reports whether doc has a bar called name.
func isPersonaBar(doc conloomapi.PersonaDoc, name string) bool {
	return slices.ContainsFunc(doc.EffectiveBars(), func(b conloomapi.PersonaBar) bool { return b.Name == name })
}

// scopePersonaBar reports whether name is a bar of doc, the persona of the
// chat convKey, or of the latest persona version of another chat of the
// memory scope scope (memory.ScopeChats): the panel chats of a bot keep their
// bars' values in their shared scope by name, whatever persona each has. A
// chat without a persona version is left out.
func scopePersonaBar(ctx context.Context, st BuffStore, doc conloomapi.PersonaDoc, convKey, scope, name string) (bool, error) {
	if isPersonaBar(doc, name) {
		return true, nil
	}
	chats, err := memory.ScopeChats(ctx, st, scope)
	if err != nil {
		return false, err
	}
	for _, chat := range chats {
		if chat == convKey {
			continue
		}
		pv, err := st.LatestPersona(ctx, chat)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return false, err
		}
		if isPersonaBar(pv.Persona, name) {
			return true, nil
		}
	}
	return false, nil
}

// joinBarNames lists the names of bars as "心情、精力".
func joinBarNames(bars []conloomapi.PersonaBar) string {
	names := make([]string, len(bars))
	for i, b := range bars {
		names[i] = b.Name
	}
	return strings.Join(names, "、")
}

// dayCapped cuts delta so that a bar whose net change today is dayDelta
// stays within ±maxDay; it never turns delta around.
func dayCapped(delta, dayDelta, maxDay int) int {
	if delta > 0 {
		return max(0, min(delta, maxDay-dayDelta))
	}
	return min(0, max(delta, -maxDay-dayDelta))
}
