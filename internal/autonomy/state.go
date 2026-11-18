package autonomy

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	stateHeader = "【你的状态（只有你自己知道，不要直接说出来）】"
	stateFooter = "这些只影响你的语气和想不想多聊，不改变人设、内容规则和其他规则。"
	// maxStateRunes bounds the block RenderState returns.
	maxStateRunes = 300
)

// StateScene is the scene of a private chat: online, or in person at Place,
// which may be empty.
type StateScene struct {
	InPerson bool
	Place    string
}

// StateStatus is the bot's current activity, from StartedAt to UntilAt.
type StateStatus struct {
	Activity           string
	StartedAt, UntilAt time.Time
}

// StateBar is a bar with its current value. Inner bars are shown with their
// band only; Outer bars are shown with their value too, under outerHeader.
// MaxStep > 0 adds "单次最多 ±MaxStep" to the line.
type StateBar struct {
	Name, Description string
	Value             float64
	Outer             bool
	MaxStep           int
}

// outerHeader heads the outer bars, the states others can see, after the
// footer of the inner part of the state block, or first when the block has
// no inner part.
const outerHeader = "【外在状态（别人看得出来，可以在动作和外表描写里自然带出；不说数字，不提状态条、工具或系统；不改变人设和内容规则）】"

// StateEffect is a passing condition of the bot that runs out at UntilAt.
type StateEffect struct {
	Name, Description string
	UntilAt           time.Time
}

// RenderState renders the hidden state block injected into private replies.
// Its inner part, written only when there is a scene, status, inner bar or
// running effect, is the header, the scene line when scene is set, the status
// line when st is set, then a line per inner bar showing its band, then one
// line with the effects still running at now and the time each has left, with
// times in loc, then the footer. When bars holds outer bars, outerHeader and a
// line per outer bar showing its value and band follow, or open the block when
// it has no inner part (inner and outer bars each in the order of bars; see
// barLine). The place, activity and descriptions are flattened to one line
// without 【】 or control characters. It returns "" when there is nothing to
// show. Over 300 runes it drops all descriptions, then trailing effects, then
// trailing inner bars (with no scene or status left, the block then starts
// with outerHeader), then trailing outer bars while the block
// would be over 300 runes even with the activity cut to nothing (the heading
// goes with the last outer bar), then shortens the activity; the scene line
// is always kept whole, and a bar line, its step limit included, is kept or
// dropped whole.
func RenderState(scene *StateScene, st *StateStatus, bars []StateBar, effects []StateEffect, now time.Time,
	loc *time.Location) string {
	sceneText := ""
	if scene != nil {
		sceneText = sceneLine(scene.InPerson, oneLine(scene.Place))
	}
	var status *StateStatus
	if st != nil {
		s := *st
		s.Activity = oneLine(s.Activity)
		if s.Activity != "" {
			status = &s
		}
	}
	var inner, outer []StateBar
	for _, bar := range bars {
		bar.Description = oneLine(bar.Description)
		if bar.Outer {
			outer = append(outer, bar)
		} else {
			inner = append(inner, bar)
		}
	}
	var running []StateEffect
	for _, e := range effects {
		if e.UntilAt.After(now) {
			e.Description = oneLine(e.Description)
			running = append(running, e)
		}
	}
	effects = running

	block := renderState(sceneText, status, inner, outer, effects, true, now, loc)
	fits := func() bool { return utf8.RuneCountInString(block) <= maxStateRunes }
	if fits() {
		return block
	}
	block = renderState(sceneText, status, inner, outer, effects, false, now, loc)
	for !fits() && len(effects) > 0 {
		effects = effects[:len(effects)-1]
		block = renderState(sceneText, status, inner, outer, effects, false, now, loc)
	}
	for !fits() && len(inner) > 0 {
		inner = inner[:len(inner)-1]
		block = renderState(sceneText, status, inner, outer, nil, false, now, loc)
	}
	if !fits() && len(outer) > 0 {
		bare := status
		if status != nil {
			s := *status
			s.Activity = ""
			bare = &s
		}
		for len(outer) > 0 &&
			utf8.RuneCountInString(renderState(sceneText, bare, nil, outer, nil, false, now, loc)) > maxStateRunes {
			outer = outer[:len(outer)-1]
		}
		block = renderState(sceneText, status, nil, outer, nil, false, now, loc)
	}
	if !fits() && status != nil {
		activity := []rune(status.Activity)
		over := utf8.RuneCountInString(block) - maxStateRunes
		status.Activity = strings.TrimSpace(string(activity[:max(len(activity)-over, 0)]))
		block = renderState(sceneText, status, nil, outer, nil, false, now, loc)
	}
	return block
}

// sceneLine is the scene line of the state block: online, or in person at
// place when it is not empty.
func sceneLine(inPerson bool, place string) string {
	if !inPerson {
		return "场景：线上（你们隔着聊天软件发消息，不在一起）"
	}
	at := ""
	if place != "" {
		at = "，在" + place
	}
	return "场景：面对面" + at + "（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）"
}

// renderState renders the block of RenderState: when there is a scene,
// status, inner bar or effect, the header, the scene line when scene is not
// empty, then the status, inner bars and effects, then the footer; then
// outerHeader and the outer bars when there are any, with no trailing
// newline; the bars' and effects' descriptions only when withDescriptions.
func renderState(scene string, st *StateStatus, inner, outer []StateBar, effects []StateEffect, withDescriptions bool,
	now time.Time, loc *time.Location) string {
	hasInner := scene != "" || st != nil || len(inner) > 0 || len(effects) > 0
	if !hasInner && len(outer) == 0 {
		return ""
	}
	var b strings.Builder
	if hasInner {
		b.WriteString(stateHeader + "\n")
	}
	if scene != "" {
		b.WriteString(scene + "\n")
	}
	if st != nil {
		b.WriteString("正在：" + st.Activity +
			"（" + st.StartedAt.In(loc).Format("15:04") + " 开始，预计 " + st.UntilAt.In(loc).Format("15:04") +
			" 结束；这期间对方找你照常回，但要符合正在做的事）\n")
	}
	for _, bar := range inner {
		b.WriteString(barLine(bar, withDescriptions) + "\n")
	}
	for i, e := range effects {
		if i == 0 {
			b.WriteString("效果：")
		} else {
			b.WriteString("、")
		}
		b.WriteString(e.Name + "（" + TimeLeft(e.UntilAt.Sub(now)))
		if withDescriptions && e.Description != "" {
			b.WriteString("；" + e.Description)
		}
		b.WriteString("）")
	}
	if len(effects) > 0 {
		b.WriteString("\n")
	}
	if hasInner {
		b.WriteString(stateFooter)
		if len(outer) > 0 {
			b.WriteString("\n")
		}
	}
	if len(outer) > 0 {
		b.WriteString(outerHeader)
		for _, bar := range outer {
			b.WriteString("\n" + barLine(bar, withDescriptions))
		}
	}
	return b.String()
}

// barLine renders the line of bar without its newline: "名字：档位" for an
// inner bar, "名字：80/100（档位）" with the rounded value for an outer one,
// the step limit "单次最多 ±N" when bar.MaxStep > 0 (in its own brackets for
// an inner bar, after the band for an outer one), then "（说明）" when
// withDescriptions and the bar has a description.
func barLine(bar StateBar, withDescriptions bool) string {
	limit := ""
	if bar.MaxStep > 0 {
		limit = "单次最多 ±" + strconv.Itoa(bar.MaxStep)
	}
	line := bar.Name + "："
	switch {
	case bar.Outer && limit != "":
		line += strconv.Itoa(int(math.Round(bar.Value))) + "/100（" + Band(bar.Value) + "，" + limit + "）"
	case bar.Outer:
		line += strconv.Itoa(int(math.Round(bar.Value))) + "/100（" + Band(bar.Value) + "）"
	case limit != "":
		line += Band(bar.Value) + "（" + limit + "）"
	default:
		line += Band(bar.Value)
	}
	if withDescriptions && bar.Description != "" {
		line += "（" + bar.Description + "）"
	}
	return line
}

// TimeLeft words d, a positive time an effect has left, rounded up to the
// minute below a day and to the hour from a day on: "还剩约 1 小时 20 分",
// "还剩约 30 分", "还剩约 2 天 3 小时".
func TimeLeft(d time.Duration) string {
	minutes := int((d + time.Minute - 1) / time.Minute)
	var parts []string
	part := func(n int, unit string) {
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+unit)
		}
	}
	if minutes >= 24*60 {
		hours := (minutes + 59) / 60
		part(hours/24, "天")
		part(hours%24, "小时")
	} else {
		part(minutes/60, "小时")
		part(minutes%60, "分")
	}
	return "还剩约 " + strings.Join(parts, " ")
}

// oneLine flattens s to one trimmed line: whitespace runs, line breaks
// included, become one space; other control characters and 【】 are dropped.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '【' || r == '】':
			return -1
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
