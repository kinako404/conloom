package autonomy

import (
	"slices"
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

// StateStatus is the bot's current activity, from StartedAt to UntilAt.
type StateStatus struct {
	Activity           string
	StartedAt, UntilAt time.Time
}

// StateBar is a hidden bar with its current value.
type StateBar struct {
	Name, Description string
	Value             float64
}

// StateEffect is a passing condition of the bot that runs out at UntilAt.
type StateEffect struct {
	Name, Description string
	UntilAt           time.Time
}

// RenderState renders the hidden state block injected into private replies:
// the status line when st is set, then a line per bar showing its band, then
// one line with the effects still running at now and the time each has left,
// with times in loc. The activity and descriptions are flattened to one line
// without 【】 or control characters. It returns "" when there is nothing to
// show. Over 300 runes it drops all descriptions, then trailing effects,
// then trailing bars, then shortens the activity.
func RenderState(st *StateStatus, bars []StateBar, effects []StateEffect, now time.Time, loc *time.Location) string {
	var status *StateStatus
	if st != nil {
		s := *st
		s.Activity = oneLine(s.Activity)
		if s.Activity != "" {
			status = &s
		}
	}
	bars = slices.Clone(bars)
	for i := range bars {
		bars[i].Description = oneLine(bars[i].Description)
	}
	var running []StateEffect
	for _, e := range effects {
		if e.UntilAt.After(now) {
			e.Description = oneLine(e.Description)
			running = append(running, e)
		}
	}
	effects = running

	block := renderState(status, bars, effects, true, now, loc)
	fits := func() bool { return utf8.RuneCountInString(block) <= maxStateRunes }
	if fits() {
		return block
	}
	block = renderState(status, bars, effects, false, now, loc)
	for !fits() && len(effects) > 0 {
		effects = effects[:len(effects)-1]
		block = renderState(status, bars, effects, false, now, loc)
	}
	for !fits() && len(bars) > 0 {
		bars = bars[:len(bars)-1]
		block = renderState(status, bars, nil, false, now, loc)
	}
	if !fits() && status != nil {
		activity := []rune(status.Activity)
		over := utf8.RuneCountInString(block) - maxStateRunes
		status.Activity = strings.TrimSpace(string(activity[:max(len(activity)-over, 0)]))
		block = renderState(status, nil, nil, false, now, loc)
	}
	return block
}

func renderState(st *StateStatus, bars []StateBar, effects []StateEffect, withDescriptions bool, now time.Time,
	loc *time.Location) string {
	if st == nil && len(bars) == 0 && len(effects) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(stateHeader + "\n")
	if st != nil {
		b.WriteString("正在：" + st.Activity +
			"（" + st.StartedAt.In(loc).Format("15:04") + " 开始，预计 " + st.UntilAt.In(loc).Format("15:04") +
			" 结束；这期间对方找你照常回，但要符合正在做的事）\n")
	}
	for _, bar := range bars {
		b.WriteString(bar.Name + "：" + Band(bar.Value))
		if withDescriptions && bar.Description != "" {
			b.WriteString("（" + bar.Description + "）")
		}
		b.WriteString("\n")
	}
	for i, e := range effects {
		if i == 0 {
			b.WriteString("效果：")
		} else {
			b.WriteString("、")
		}
		b.WriteString(e.Name + "（" + timeLeft(e.UntilAt.Sub(now)))
		if withDescriptions && e.Description != "" {
			b.WriteString("；" + e.Description)
		}
		b.WriteString("）")
	}
	if len(effects) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(stateFooter)
	return b.String()
}

// timeLeft words d, a positive time an effect has left, rounded up to the
// minute below a day and to the hour from a day on: "还剩约 1 小时 20 分",
// "还剩约 30 分", "还剩约 2 天 3 小时".
func timeLeft(d time.Duration) string {
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
