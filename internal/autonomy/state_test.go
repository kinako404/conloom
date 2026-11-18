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
		if got := RenderState(nil, tt.st, tt.bars, nil, time.Time{}, loc); got != tt.want {
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
		got := RenderState(nil, bath, bars, nil, time.Time{}, loc)
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
		got := RenderState(nil, bath, bars, nil, time.Time{}, loc)
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
		got := RenderState(nil, bath, bars, nil, time.Time{}, loc)
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
		got := RenderState(nil, &st, []StateBar{{Name: "心情", Value: 65}}, nil, time.Time{}, loc)
		if n := utf8.RuneCountInString(got); n != 300 {
			t.Errorf("RenderState() has %d runes, want exactly 300:\n%s", n, got)
		}
		if !strings.HasPrefix(got, wantHeader+"\n正在：忙忙忙") || !strings.HasSuffix(got, "）\n"+wantFooter) {
			t.Errorf("RenderState() =\n%s\nwant the status line shortened inside the block", got)
		}
	})
}

func TestRenderState_Effects(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, loc)
	bath := &StateStatus{Activity: "洗澡", StartedAt: now.Add(-10 * time.Minute), UntilAt: now.Add(20 * time.Minute)}
	bars := []StateBar{{Name: "心情", Description: "高=开心放松，低=低落烦躁", Value: 65}}
	effects := []StateEffect{
		{Name: "着凉", Description: "有点冷，想喝热水", UntilAt: now.Add(80 * time.Minute)},
		{Name: "吃饱了", UntilAt: now.Add(30 * time.Minute)},
	}
	statusLine := "正在：洗澡（19:50 开始，预计 20:20 结束；这期间对方找你照常回，但要符合正在做的事）"
	barLine := "心情：偏高（高=开心放松，低=低落烦躁）"
	effectsLine := "效果：着凉（还剩约 1 小时 20 分；有点冷，想喝热水）、吃饱了（还剩约 30 分）"

	tests := []struct {
		name    string
		st      *StateStatus
		bars    []StateBar
		effects []StateEffect
		want    string
	}{
		{"status, bars and effects", bath, bars, effects,
			wantHeader + "\n" + statusLine + "\n" + barLine + "\n" + effectsLine + "\n" + wantFooter},
		{"effects only", nil, nil, effects, wantHeader + "\n" + effectsLine + "\n" + wantFooter},
		{"run-out effects left out", nil, bars,
			[]StateEffect{{Name: "头晕", UntilAt: now}, {Name: "着凉", UntilAt: now.Add(-time.Minute)}},
			wantHeader + "\n" + barLine + "\n" + wantFooter},
		{"only run-out effects", nil, nil, []StateEffect{{Name: "头晕", UntilAt: now}}, ""},
		{"description flattened", nil, nil,
			[]StateEffect{{Name: "头晕", Description: " 站起来\n就晕【眼前发黑】\x07 ", UntilAt: now.Add(time.Hour)}},
			wantHeader + "\n效果：头晕（还剩约 1 小时；站起来 就晕眼前发黑）\n" + wantFooter},
	}
	for _, tt := range tests {
		if got := RenderState(nil, tt.st, tt.bars, tt.effects, now, loc); got != tt.want {
			t.Errorf("%s: RenderState() =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
	if effects[0].Description != "有点冷，想喝热水" {
		t.Errorf("RenderState changed the caller's effects: %#v", effects)
	}
}

func TestRenderState_EffectTimeLeft(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, loc)
	for _, tt := range []struct {
		left time.Duration
		want string
	}{
		{20 * time.Second, "还剩约 1 分"},
		{29*time.Minute + 30*time.Second, "还剩约 30 分"},
		{30 * time.Minute, "还剩约 30 分"},
		{59 * time.Minute, "还剩约 59 分"},
		{time.Hour, "还剩约 1 小时"},
		{80 * time.Minute, "还剩约 1 小时 20 分"},
		{23*time.Hour + 59*time.Minute, "还剩约 23 小时 59 分"},
		{24 * time.Hour, "还剩约 1 天"},
		{26*time.Hour + 30*time.Minute, "还剩约 1 天 3 小时"},
		{7 * 24 * time.Hour, "还剩约 7 天"},
	} {
		got := RenderState(nil, nil, nil, []StateEffect{{Name: "着凉", UntilAt: now.Add(tt.left)}}, now, loc)
		if want := wantHeader + "\n效果：着凉（" + tt.want + "）\n" + wantFooter; got != want {
			t.Errorf("%v left: RenderState() =\n%s\nwant\n%s", tt.left, got, want)
		}
	}
}

func TestRenderState_EffectsCap(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 10, 0, 0, loc)
	bath := &StateStatus{Activity: "洗澡", StartedAt: now, UntilAt: now.Add(20 * time.Minute)}
	statusLine := "正在：洗澡（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）"
	long := strings.Repeat("长", 40)

	t.Run("drops all descriptions first", func(t *testing.T) {
		bars := []StateBar{
			{Name: "心情", Description: long, Value: 65},
			{Name: "精力", Description: long, Value: 30},
			{Name: "饥饿", Description: long, Value: 50},
		}
		effects := []StateEffect{
			{Name: "着凉", Description: long, UntilAt: now.Add(80 * time.Minute)},
			{Name: "吃饱了", Description: long, UntilAt: now.Add(30 * time.Minute)},
		}
		got := RenderState(nil, bath, bars, effects, now, loc)
		want := wantHeader + "\n" + statusLine + "\n心情：偏高\n精力：偏低\n饥饿：中等\n" +
			"效果：着凉（还剩约 1 小时 20 分）、吃饱了（还剩约 30 分）\n" + wantFooter
		if got != want {
			t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("then drops trailing effects", func(t *testing.T) {
		var bars []StateBar
		for range 10 {
			bars = append(bars, StateBar{Name: "很长的名字啊", Description: "说明", Value: 50})
		}
		var effects []StateEffect
		for _, name := range []string{"第一个效果啊", "第二个效果啊", "第三个效果啊", "第四个效果啊", "第五个效果啊", "第六个效果啊"} {
			effects = append(effects, StateEffect{Name: name, Description: "说明", UntilAt: now.Add(23*time.Hour + 59*time.Minute)})
		}
		got := RenderState(nil, bath, bars, effects, now, loc)
		n := utf8.RuneCountInString(got)
		if n > 300 {
			t.Fatalf("RenderState() has %d runes, want at most 300:\n%s", n, got)
		}
		lines := strings.Split(got, "\n")
		if len(lines) != 14 || lines[0] != wantHeader || lines[1] != statusLine || lines[13] != wantFooter {
			t.Fatalf("RenderState() =\n%s\nwant the header, status, every bar, an effects line and the footer", got)
		}
		for _, l := range lines[2:12] {
			if l != "很长的名字啊：中等" {
				t.Fatalf("RenderState() bar line %q, want every bar without its description", l)
			}
		}
		items := strings.Split(strings.TrimPrefix(lines[12], "效果："), "、")
		if !strings.HasPrefix(lines[12], "效果：") || len(items) == 0 || len(items) == len(effects) {
			t.Fatalf("RenderState() effects line %q, want some trailing effects dropped", lines[12])
		}
		for i, item := range items {
			if want := effects[i].Name + "（还剩约 23 小时 59 分）"; item != want {
				t.Fatalf("RenderState() effect %d = %q, want %q", i, item, want)
			}
		}
		next := "、" + effects[len(items)].Name + "（还剩约 23 小时 59 分）"
		if n+utf8.RuneCountInString(next) <= 300 {
			t.Errorf("RenderState() dropped an effect that fits (%d runes with it)", n+utf8.RuneCountInString(next))
		}
	})

	t.Run("drops every effect before a bar", func(t *testing.T) {
		var bars []StateBar
		for range 30 {
			bars = append(bars, StateBar{Name: "很长很长的名字", Value: 50})
		}
		effects := []StateEffect{{Name: "着凉", UntilAt: now.Add(time.Hour)}}
		got := RenderState(nil, bath, bars, effects, now, loc)
		if n := utf8.RuneCountInString(got); n > 300 {
			t.Fatalf("RenderState() has %d runes, want at most 300:\n%s", n, got)
		}
		if strings.Contains(got, "效果：") {
			t.Fatalf("RenderState() kept the effects line while dropping bars:\n%s", got)
		}
		lines := strings.Split(got, "\n")
		if kept := len(lines) - 3; kept == 0 || kept == len(bars) || lines[len(lines)-1] != wantFooter {
			t.Fatalf("RenderState() =\n%s\nwant some trailing bars dropped", got)
		}
	})

	t.Run("shortens an overlong activity last", func(t *testing.T) {
		st := *bath
		st.Activity = strings.Repeat("忙", 400)
		got := RenderState(nil, &st, []StateBar{{Name: "心情", Value: 65}}, []StateEffect{{Name: "着凉", UntilAt: now.Add(time.Hour)}}, now, loc)
		if n := utf8.RuneCountInString(got); n != 300 {
			t.Errorf("RenderState() has %d runes, want exactly 300:\n%s", n, got)
		}
		if !strings.HasPrefix(got, wantHeader+"\n正在：忙忙忙") || !strings.HasSuffix(got, "）\n"+wantFooter) ||
			strings.Contains(got, "心情") || strings.Contains(got, "着凉") {
			t.Errorf("RenderState() =\n%s\nwant only the status line, shortened inside the block", got)
		}
	})
}

const (
	wantOnline       = "场景：线上（你们隔着聊天软件发消息，不在一起）"
	wantInPersonTail = "（你们在同一个地方；可以偶尔用括号写一个简短的动作或神态）"
)

func TestRenderState_SceneLine(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, loc)
	bath := &StateStatus{Activity: "洗澡", StartedAt: now.Add(-10 * time.Minute), UntilAt: now.Add(20 * time.Minute)}
	bars := []StateBar{{Name: "心情", Description: "高=开心放松，低=低落烦躁", Value: 65}}
	statusLine := "正在：洗澡（19:50 开始，预计 20:20 结束；这期间对方找你照常回，但要符合正在做的事）"
	barLine := "心情：偏高（高=开心放松，低=低落烦躁）"

	tests := []struct {
		name  string
		scene *StateScene
		st    *StateStatus
		bars  []StateBar
		want  string
	}{
		{"online alone", &StateScene{}, nil, nil, wantHeader + "\n" + wantOnline + "\n" + wantFooter},
		{"online ignores a place", &StateScene{Place: "食堂"}, nil, nil, wantHeader + "\n" + wantOnline + "\n" + wantFooter},
		{"in person with a status", &StateScene{InPerson: true, Place: "食堂"}, bath, bars,
			wantHeader + "\n场景：面对面，在食堂" + wantInPersonTail + "\n" + statusLine + "\n" + barLine + "\n" + wantFooter},
		{"in person without a place", &StateScene{InPerson: true}, nil, nil,
			wantHeader + "\n场景：面对面" + wantInPersonTail + "\n" + wantFooter},
		{"in person with a blank place", &StateScene{InPerson: true, Place: " 【】\n"}, nil, nil,
			wantHeader + "\n场景：面对面" + wantInPersonTail + "\n" + wantFooter},
		{"no scene", nil, bath, bars, wantHeader + "\n" + statusLine + "\n" + barLine + "\n" + wantFooter},
		{"nothing", nil, nil, nil, ""},
	}
	for _, tt := range tests {
		if got := RenderState(tt.scene, tt.st, tt.bars, nil, now, loc); got != tt.want {
			t.Errorf("%s: RenderState() =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
}

func TestRenderState_SceneLineSurvivesCap(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 10, 0, 0, loc)
	place := strings.Repeat("场", 16)
	st := &StateStatus{Activity: strings.Repeat("忙", 400), StartedAt: now, UntilAt: now.Add(20 * time.Minute)}
	long := strings.Repeat("长", 40)
	bars := []StateBar{
		{Name: "心情", Description: long, Value: 65},
		{Name: "精力", Description: long, Value: 30},
		{Name: "饥饿", Description: long, Value: 50},
		{Name: "专注", Description: long, Value: 90},
	}
	effects := []StateEffect{{Name: "着凉", Description: long, UntilAt: now.Add(time.Hour)}}

	got := RenderState(&StateScene{InPerson: true, Place: place}, st, bars, effects, now, loc)
	if n := utf8.RuneCountInString(got); n != 300 {
		t.Errorf("RenderState() has %d runes, want exactly 300:\n%s", n, got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[0] != wantHeader || lines[1] != "场景：面对面，在"+place+wantInPersonTail ||
		!strings.HasPrefix(lines[2], "正在：忙忙忙") || lines[3] != wantFooter {
		t.Fatalf("RenderState() =\n%s\nwant the header, the whole scene line, a status line and the footer", got)
	}
	without := RenderState(nil, st, bars, effects, now, loc)
	statusOf := func(block string) string { return strings.Split(block, "\n")[1] }
	if a, b := utf8.RuneCountInString(lines[2]), utf8.RuneCountInString(statusOf(without)); a >= b {
		t.Errorf("status line has %d runes with the scene, %d without; want it shortened by the scene line", a, b)
	}
}

func TestRenderState_ScenePlaceFlattened(t *testing.T) {
	loc := shanghai(t)
	got := RenderState(&StateScene{InPerson: true, Place: "食\n堂【x】"}, nil, nil, nil, time.Time{}, loc)
	if want := wantHeader + "\n场景：面对面，在食 堂x" + wantInPersonTail + "\n" + wantFooter; got != want {
		t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(got, "在食 堂x（") {
		t.Errorf("RenderState() = %q, want the place flattened", got)
	}
}
