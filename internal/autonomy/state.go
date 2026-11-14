package autonomy

import (
	"slices"
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

// RenderState renders the hidden state block injected into private replies:
// the status line when st is set, then a line per bar showing its band, with
// times in loc. The activity and descriptions are flattened to one line
// without 【】 or control characters. It returns "" when there is nothing to
// show. Over 300 runes it drops all descriptions, then trailing bars, then
// shortens the activity.
func RenderState(st *StateStatus, bars []StateBar, loc *time.Location) string {
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

	block := renderState(status, bars, true, loc)
	fits := func() bool { return utf8.RuneCountInString(block) <= maxStateRunes }
	if fits() {
		return block
	}
	block = renderState(status, bars, false, loc)
	for !fits() && len(bars) > 0 {
		bars = bars[:len(bars)-1]
		block = renderState(status, bars, false, loc)
	}
	if !fits() && status != nil {
		activity := []rune(status.Activity)
		over := utf8.RuneCountInString(block) - maxStateRunes
		status.Activity = strings.TrimSpace(string(activity[:max(len(activity)-over, 0)]))
		block = renderState(status, nil, false, loc)
	}
	return block
}

func renderState(st *StateStatus, bars []StateBar, withDescriptions bool, loc *time.Location) string {
	if st == nil && len(bars) == 0 {
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
	b.WriteString(stateFooter)
	return b.String()
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
