package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// buffMaxDelta bounds one change of a bar.
	buffMaxDelta = 10
	// buffMaxDayDelta bounds a bar's net change on one local day.
	buffMaxDayDelta = 30
	// buffMaxReasonRunes bounds a change's reason, after CleanActivity.
	buffMaxReasonRunes = 40
)

var (
	buffBadDelta   = "delta 需要是不为 0 的整数，单次最多 ±" + strconv.Itoa(buffMaxDelta) + "。"
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
// one of the persona's hidden bars (conloomapi.PersonaDoc.EffectiveBars) by a
// non-zero delta, cut to ±10, on top of its value decayed to now (its
// baseline when none is stored), keeping the bar's net change on the local
// day within ±30 and its value within 0–100. A task changes each bar at most
// once. The answer names the bar's band before and after, never a number; an
// unknown name is answered with the persona's bars.
func NewAdjustBuff(st BuffStore, personas PersonaSource) skills.Tool {
	return &adjustBuffTool{st: st, personas: personas}
}

// MaxResultRunes declares that adjust_buff answers with one sentence.
func (*adjustBuffTool) MaxResultRunes() int { return shortResultRunes }

func (*adjustBuffTool) Spec() skills.Spec {
	return skills.Spec{
		Name: "adjust_buff",
		Description: "小幅调整你自己的一条隐藏状态条（比如心情、精力），只有你自己知道。聊天里发生了真正影响你的事时调用：" +
			"日常小事调 ±2 到 5，大事调 ±10；不要每句都调，也不要向对方提起。返回调整前后的大致档位。",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"状态条的名字，要和你的状态里写的完全一致，例如“心情”"},` +
			`"delta":{"type":"integer","description":"变化量，-10 到 10，不能为 0","minimum":-10,"maximum":10},` +
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
	delta = min(max(delta, -buffMaxDelta), buffMaxDelta)
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
	bars := doc.EffectiveBars()
	name, _ := stringArg(args, "name")
	name = strings.TrimSpace(name)
	i := slices.IndexFunc(bars, func(b conloomapi.PersonaBar) bool { return b.Name == name })
	if i < 0 {
		names := make([]string, len(bars))
		for j, b := range bars {
			names[j] = b.Name
		}
		if name == "" {
			return "需要提供条名（name）。现有：" + strings.Join(names, "、") + "。", nil
		}
		return "没有这一条。现有：" + strings.Join(names, "、") + "。", nil
	}
	bar := bars[i]
	now, loc := clock(inv)
	day := now.In(loc).Format(time.DateOnly)
	var before float64
	after, err := t.st.UpdateBuff(ctx, inv.ConversationKey, bar.Name,
		store.BuffEvent{TaskID: inv.TaskID, Source: store.BuffSourceModel, Before: float64(bar.Baseline), Reason: reason, CreatedAt: now},
		func(cur store.BuffValue, found bool) (store.BuffValue, error) {
			before = float64(bar.Baseline)
			dayDelta := 0
			if found {
				before = autonomy.Decay(cur.Value, bar.Baseline, bar.HalfLife.Duration, now.Sub(cur.UpdatedAt))
				if cur.Day == day {
					dayDelta = cur.DayDelta
				}
			}
			d := dayCapped(delta, dayDelta)
			if d == 0 {
				return store.BuffValue{}, errDayCapped
			}
			return store.BuffValue{Value: autonomy.Clamp(before + float64(d)), Day: day, DayDelta: dayDelta + d, UpdatedAt: now}, nil
		})
	switch {
	case errors.Is(err, store.ErrBuffDuplicate):
		return buffDuplicate, nil
	case errors.Is(err, errDayCapped):
		return bar.Name + "今天的变化已经到上限，这次没有调整。", nil
	case err != nil:
		return "", err
	}
	return bar.Name + "：" + autonomy.Band(before) + " → " + autonomy.Band(after.Value), nil
}

// dayCapped cuts delta so that a bar whose net change today is dayDelta
// stays within ±buffMaxDayDelta; it never turns delta around.
func dayCapped(delta, dayDelta int) int {
	if delta > 0 {
		return max(0, min(delta, buffMaxDayDelta-dayDelta))
	}
	return min(0, max(delta, -buffMaxDayDelta-dayDelta))
}
