package autonomy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	wantHeader = "【你的状态（只有你自己知道，不要直接说出来）】"
	wantFooter = "这些只影响你的语气和想不想多聊，不改变人设、内容规则和其他规则。"
)

func TestRenderState(t *testing.T) {
	loc := shanghai(t)
	bath := &StateStatus{
		Activity:  "洗澡",
		StartedAt: time.Date(2026, 11, 14, 12, 10, 0, 0, time.UTC),
		UntilAt:   time.Date(2026, 11, 14, 20, 30, 0, 0, loc),
	}
	bars := []StateBar{
		{Name: "心情", Description: "高=开心放松，低=低落烦躁", Value: 65},
		{Name: "精力", Description: "高=有精神，低=累、困", Value: 30},
	}
	statusLine := "正在：洗澡（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）"

	tests := []struct {
		name string
		st   *StateStatus
		bars []StateBar
		want string
	}{
		{"status and bars", bath, bars, wantHeader + "\n" + statusLine + "\n" +
			"心情：偏高（高=开心放松，低=低落烦躁）\n" +
			"精力：偏低（高=有精神，低=累、困）\n" + wantFooter},
		{"status only", bath, nil, wantHeader + "\n" + statusLine + "\n" + wantFooter},
		{"bars only", nil, bars, wantHeader + "\n" +
			"心情：偏高（高=开心放松，低=低落烦躁）\n" +
			"精力：偏低（高=有精神，低=累、困）\n" + wantFooter},
		{"empty", nil, nil, ""},
		{"empty activity", &StateStatus{Activity: " 【】\n", StartedAt: bath.StartedAt, UntilAt: bath.UntilAt}, nil, ""},
		{"bar without description", nil, []StateBar{{Name: "饥饿", Value: 80}}, wantHeader + "\n饥饿：很高\n" + wantFooter},
		{
			"flattened to one line",
			&StateStatus{Activity: "洗\n澡【】\x00", StartedAt: bath.StartedAt, UntilAt: bath.UntilAt},
			[]StateBar{{Name: "心情", Description: " 高=开心\r\n\n低=【烦躁】\x07\u2028放松 ", Value: 50}},
			wantHeader + "\n" +
				"正在：洗 澡（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）\n" +
				"心情：中等（高=开心 低=烦躁 放松）\n" + wantFooter,
		},
	}
	for _, tt := range tests {
		if got := RenderState(tt.st, tt.bars, loc); got != tt.want {
			t.Errorf("%s: RenderState() =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
	if bars[0].Description != "高=开心放松，低=低落烦躁" {
		t.Errorf("RenderState changed the caller's bars: %#v", bars)
	}
}

func TestRenderState_Cap(t *testing.T) {
	loc := shanghai(t)
	bath := &StateStatus{
		Activity:  "洗澡",
		StartedAt: time.Date(2026, 11, 14, 20, 10, 0, 0, loc),
		UntilAt:   time.Date(2026, 11, 14, 20, 30, 0, 0, loc),
	}
	long := strings.Repeat("长", 60)

	t.Run("drops descriptions first", func(t *testing.T) {
		bars := []StateBar{
			{Name: "心情", Description: long, Value: 65},
			{Name: "精力", Description: long, Value: 30},
			{Name: "饥饿", Description: long, Value: 50},
			{Name: "专注", Description: long, Value: 90},
		}
		got := RenderState(bath, bars, loc)
		want := wantHeader + "\n" +
			"正在：洗澡（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）\n" +
			"心情：偏高\n精力：偏低\n饥饿：中等\n专注：很高\n" + wantFooter
		if got != want {
			t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("keeps descriptions that fit", func(t *testing.T) {
		bars := []StateBar{
			{Name: "心情", Description: strings.Repeat("长", 40), Value: 65},
			{Name: "精力", Description: strings.Repeat("长", 40), Value: 30},
		}
		got := RenderState(bath, bars, loc)
		if !strings.Contains(got, "心情：偏高（"+strings.Repeat("长", 40)+"）") {
			t.Errorf("RenderState() dropped a description that fits:\n%s", got)
		}
		if n := utf8.RuneCountInString(got); n > 300 {
			t.Errorf("RenderState() has %d runes, want at most 300", n)
		}
	})

	t.Run("then drops trailing bars", func(t *testing.T) {
		var bars []StateBar
		for range 30 {
			bars = append(bars, StateBar{Name: "很长很长的名字", Description: "说明", Value: 50})
		}
		got := RenderState(bath, bars, loc)
		if n := utf8.RuneCountInString(got); n > 300 {
			t.Fatalf("RenderState() has %d runes, want at most 300:\n%s", n, got)
		}
		lines := strings.Split(got, "\n")
		if lines[0] != wantHeader || lines[len(lines)-1] != wantFooter || !strings.HasPrefix(lines[1], "正在：洗澡（") {
			t.Fatalf("RenderState() lost the header, status or footer:\n%s", got)
		}
		kept := lines[2 : len(lines)-1]
		if len(kept) == 0 || len(kept) == len(bars) {
			t.Fatalf("RenderState() kept %d of %d bars, want some dropped", len(kept), len(bars))
		}
		for _, l := range kept {
			if l != "很长很长的名字：中等" {
				t.Fatalf("RenderState() bar line %q, want no description", l)
			}
		}
		// Adding one more line would pass the cap.
		if n := utf8.RuneCountInString(got) + utf8.RuneCountInString("\n很长很长的名字：中等"); n <= 300 {
			t.Errorf("RenderState() dropped a bar that fits (%d runes with it)", n)
		}
	})

	t.Run("shortens an overlong activity last", func(t *testing.T) {
		st := *bath
		st.Activity = strings.Repeat("忙", 400)
		got := RenderState(&st, []StateBar{{Name: "心情", Value: 65}}, loc)
		if n := utf8.RuneCountInString(got); n != 300 {
			t.Errorf("RenderState() has %d runes, want exactly 300:\n%s", n, got)
		}
		if !strings.HasPrefix(got, wantHeader+"\n正在：忙忙忙") || !strings.HasSuffix(got, "）\n"+wantFooter) {
			t.Errorf("RenderState() =\n%s\nwant the status line shortened inside the block", got)
		}
	})
}
