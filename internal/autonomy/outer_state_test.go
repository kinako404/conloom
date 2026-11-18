package autonomy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const wantOuterHeader = "【外在状态（别人看得出来，可以在动作和外表描写里自然带出；不说数字，不提状态条、工具或系统）】"

func TestRenderState_OuterSection(t *testing.T) {
	loc := shanghai(t)
	mood := StateBar{Name: "心情", Description: "高=开心放松，低=低落烦躁", Value: 65}
	belly := StateBar{Name: "肚子", Description: "高=很饱", Value: 80, Outer: true, MaxStep: 40}
	blush := StateBar{Name: "脸红", Value: 30, Outer: true, MaxStep: 10}
	moodLine := "心情：偏高（高=开心放松，低=低落烦躁）"
	bellyLine := "肚子：80/100（很高，单次最多 ±40）（高=很饱）"
	blushLine := "脸红：30/100（偏低，单次最多 ±10）"

	if wantOuterHeader != outerHeader {
		t.Fatalf("outerHeader = %q, want %q", outerHeader, wantOuterHeader)
	}
	tests := []struct {
		name string
		bars []StateBar
		want string
	}{
		{"inner then outer", []StateBar{mood, belly},
			wantHeader + "\n" + moodLine + "\n" + wantFooter + "\n" + wantOuterHeader + "\n" + bellyLine},
		{"outer first in the input", []StateBar{belly, mood},
			wantHeader + "\n" + moodLine + "\n" + wantFooter + "\n" + wantOuterHeader + "\n" + bellyLine},
		{"two outer bars keep their order", []StateBar{blush, mood, belly},
			wantHeader + "\n" + moodLine + "\n" + wantFooter + "\n" + wantOuterHeader + "\n" + blushLine + "\n" + bellyLine},
		{"outer without a step limit", []StateBar{{Name: "肚子", Description: "高=很饱", Value: 80, Outer: true}},
			wantHeader + "\n" + wantOuterHeader + "\n肚子：80/100（很高）（高=很饱）"},
	}
	for _, tt := range tests {
		if got := RenderState(nil, nil, tt.bars, nil, time.Time{}, loc); got != tt.want {
			t.Errorf("%s: RenderState() =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
}

func TestRenderState_OuterOnly(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, loc)
	belly := []StateBar{{Name: "肚子", Description: "高=很饱", Value: 80, Outer: true, MaxStep: 40}}
	bellyLine := "肚子：80/100（很高，单次最多 ±40）（高=很饱）"

	if got, want := RenderState(nil, nil, belly, nil, now, loc), wantHeader+"\n"+wantOuterHeader+"\n"+bellyLine; got != want {
		t.Errorf("outer only: RenderState() =\n%s\nwant\n%s", got, want)
	}
	bath := &StateStatus{Activity: "洗澡", StartedAt: now.Add(-10 * time.Minute), UntilAt: now.Add(20 * time.Minute)}
	statusLine := "正在：洗澡（19:50 开始，预计 20:20 结束；这期间对方找你照常回，但要符合正在做的事）"
	want := wantHeader + "\n" + statusLine + "\n" + wantFooter + "\n" + wantOuterHeader + "\n" + bellyLine
	if got := RenderState(nil, bath, belly, nil, now, loc); got != want {
		t.Errorf("status and outer: RenderState() =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderState_StepLimitShown(t *testing.T) {
	loc := shanghai(t)
	render := func(bars ...StateBar) string { return RenderState(nil, nil, bars, nil, time.Time{}, loc) }
	inner := func(line string) string { return wantHeader + "\n" + line + "\n" + wantFooter }
	outer := func(line string) string { return wantHeader + "\n" + wantOuterHeader + "\n" + line }

	tests := []struct {
		name, got, want string
	}{
		{"inner with a step", render(StateBar{Name: "心情", Description: "说明", Value: 65, MaxStep: 20}),
			inner("心情：偏高（单次最多 ±20）（说明）")},
		{"inner with a step, no description", render(StateBar{Name: "心情", Value: 65, MaxStep: 20}),
			inner("心情：偏高（单次最多 ±20）")},
		{"inner without a step", render(StateBar{Name: "心情", Description: "说明", Value: 65}), inner("心情：偏高（说明）")},
		{"outer rounded up", render(StateBar{Name: "肚子", Value: 79.5, Outer: true, MaxStep: 10}),
			outer("肚子：80/100（很高，单次最多 ±10）")},
		{"outer at zero", render(StateBar{Name: "肚子", Value: 0, Outer: true, MaxStep: 10}),
			outer("肚子：0/100（很低，单次最多 ±10）")},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: RenderState() =\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
	if got := barLine(StateBar{Name: "心情", Description: "说明", Value: 65, MaxStep: 20}, false); got != "心情：偏高（单次最多 ±20）" {
		t.Errorf("barLine() without descriptions = %q", got)
	}
}

func TestRenderState_OuterBarsCap(t *testing.T) {
	loc := shanghai(t)
	now := time.Date(2026, 11, 16, 20, 10, 0, 0, loc)
	long := strings.Repeat("长", 40)

	t.Run("descriptions, effects and inner bars go first", func(t *testing.T) {
		st := &StateStatus{Activity: "安静地看一本小说", StartedAt: now, UntilAt: now.Add(20 * time.Minute)}
		bars := []StateBar{
			{Name: "肚子", Description: long, Value: 80, Outer: true, MaxStep: 40},
			{Name: "困意", Description: "高=想睡", Value: 50},
			{Name: "脸红", Description: long, Value: 35, Outer: true, MaxStep: 40},
			{Name: "出汗", Description: long, Value: 50, Outer: true, MaxStep: 40},
			{Name: "好奇", Description: "高=想知道更多", Value: 70},
			{Name: "发抖", Description: long, Value: 10, Outer: true, MaxStep: 40},
		}
		effects := []StateEffect{
			{Name: "着凉", Description: long, UntilAt: now.Add(time.Hour)},
			{Name: "吃饱了", UntilAt: now.Add(30 * time.Minute)},
			{Name: "脚麻", Description: "麻麻的", UntilAt: now.Add(10 * time.Minute)},
		}
		got := RenderState(&StateScene{InPerson: true, Place: "图书馆"}, st, bars, effects, now, loc)
		// header 24 + scene 41 + status 55 + one inner bar 6 + footer 33 +
		// heading 48 + four outer lines 91 = 298; the second inner bar would
		// make it 304.
		want := wantHeader + "\n" +
			"场景：面对面，在图书馆" + wantInPersonTail + "\n" +
			"正在：安静地看一本小说（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）\n" +
			"困意：中等\n" +
			wantFooter + "\n" +
			wantOuterHeader + "\n" +
			"肚子：80/100（很高，单次最多 ±40）\n" +
			"脸红：35/100（偏低，单次最多 ±40）\n" +
			"出汗：50/100（中等，单次最多 ±40）\n" +
			"发抖：10/100（很低，单次最多 ±40）"
		if n := utf8.RuneCountInString(want); n != 298 {
			t.Fatalf("expected block has %d runes, want 298", n)
		}
		if got != want {
			t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("outer bars go before the activity is cut", func(t *testing.T) {
		place := strings.Repeat("场", 16)
		activity := strings.Repeat("忙", 16)
		st := &StateStatus{Activity: activity, StartedAt: now, UntilAt: now.Add(20 * time.Minute)}
		var bars []StateBar
		for _, name := range []string{"第一个外在条", "第二个外在条", "第三个外在条", "第四个外在条"} {
			bars = append(bars, StateBar{Name: name, Description: long, Value: 100, Outer: true, MaxStep: 100})
		}
		bars = append(bars, StateBar{Name: "困意", Description: long, Value: 50}, StateBar{Name: "好奇", Value: 70})
		effects := []StateEffect{{Name: "着凉", Description: long, UntilAt: now.Add(time.Hour)}}
		got := RenderState(&StateScene{InPerson: true, Place: place}, st, bars, effects, now, loc)

		// In full (no descriptions, effects or inner bars): header 24 + scene
		// 54 + status 63 + footer 33 + heading 48 + four outer lines 4*28+3 =
		// 337; with an empty activity 321, so the last outer bar goes (292
		// with an empty activity, 308 with the whole one) and the activity
		// loses 8 runes.
		outerLine := func(name string) string { return name + "：100/100（很高，单次最多 ±100）" }
		want := wantHeader + "\n" +
			"场景：面对面，在" + place + wantInPersonTail + "\n" +
			"正在：" + strings.Repeat("忙", 8) + "（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）\n" +
			wantFooter + "\n" +
			wantOuterHeader + "\n" +
			outerLine("第一个外在条") + "\n" +
			outerLine("第二个外在条") + "\n" +
			outerLine("第三个外在条")
		if n := utf8.RuneCountInString(want); n != 300 {
			t.Fatalf("expected block has %d runes, want 300", n)
		}
		if got != want {
			t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
		}

		lines := strings.Split(got, "\n")
		if lines[1] != "场景：面对面，在"+place+wantInPersonTail {
			t.Errorf("scene line = %q, want it whole", lines[1])
		}
		var kept []string
		for _, l := range lines {
			if strings.Contains(l, "/100（") {
				kept = append(kept, l)
			}
		}
		for i, l := range kept {
			if l != outerLine(bars[i].Name) {
				t.Errorf("outer line %d = %q, want %q", i, l, outerLine(bars[i].Name))
			}
		}
		if strings.Contains(got, wantOuterHeader) != (len(kept) > 0) {
			t.Errorf("RenderState() has the outer heading %v with %d outer lines", strings.Contains(got, wantOuterHeader), len(kept))
		}
	})

	t.Run("heading goes with the last outer bar", func(t *testing.T) {
		place := strings.Repeat("场", 16)
		st := &StateStatus{Activity: strings.Repeat("忙", 16), StartedAt: now, UntilAt: now.Add(20 * time.Minute)}
		bars := []StateBar{{Name: strings.Repeat("名", 120), Value: 100, Outer: true, MaxStep: 100}}
		got := RenderState(&StateScene{InPerson: true, Place: place}, st, bars, nil, now, loc)
		want := wantHeader + "\n" +
			"场景：面对面，在" + place + wantInPersonTail + "\n" +
			"正在：" + strings.Repeat("忙", 16) + "（20:10 开始，预计 20:30 结束；这期间对方找你照常回，但要符合正在做的事）\n" +
			wantFooter
		if got != want {
			t.Errorf("RenderState() =\n%s\nwant\n%s", got, want)
		}
	})
}
