package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestSplitParts(t *testing.T) {
	tests := []struct {
		name string
		text string
		max  int
		want []string
	}{
		{"split", "第一段\n|||\n第二段\n|||\n第三段", 3, []string{"第一段", "第二段", "第三段"}},
		{"fewer than max", "第一段\n|||\n第二段", 5, []string{"第一段", "第二段"}},
		{"over max merges into last", "一\n|||\n二\n|||\n三\n|||\n四", 3, []string{"一", "二", "三\n四"}},
		{"over max of two", "一\n|||\n二\n|||\n三", 2, []string{"一", "二\n三"}},
		{"empty parts dropped", "|||\n一\n|||\n\n|||\n  \n|||\n二\n|||", 5, []string{"一", "二"}},
		{"only separators", "|||\n|||", 3, nil},
		{"no separator", "  只有一段\n两行  ", 3, []string{"只有一段\n两行"}},
		{"separator with spaces", "一 \n  |||\t\n 二\r\n|||\r\n三", 3, []string{"一", "二", "三"}},
		{"inline separator kept", "一 ||| 二", 3, []string{"一 ||| 二"}},
		{"multi-line part", "一行\n二行\n|||\n三行", 3, []string{"一行\n二行", "三行"}},
		{"max one keeps whole text", "一\n|||\n二", 1, []string{"一\n|||\n二"}},
		{"max zero keeps whole text", "一\n|||\n二", 0, []string{"一\n|||\n二"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitParts(tt.text, tt.max); !slices.Equal(got, tt.want) {
				t.Errorf("splitParts(%q, %d) = %q, want %q", tt.text, tt.max, got, tt.want)
			}
		})
	}
}

func TestPartDelay(t *testing.T) {
	tests := []struct {
		part string
		want time.Duration
	}{
		{"", time.Second},
		{"好", time.Second},
		{"好的呀", time.Second},
		{"好的呀好", 1040 * time.Millisecond},
		{strings.Repeat("字", 10), 1400 * time.Millisecond},
		{strings.Repeat("a", 10), 1400 * time.Millisecond},
		{strings.Repeat("字", 53), 3980 * time.Millisecond},
		{strings.Repeat("字", 54), 4 * time.Second},
		{strings.Repeat("字", 500), 4 * time.Second},
	}
	for _, tt := range tests {
		if got := partDelay(tt.part); got != tt.want {
			t.Errorf("partDelay(%d runes) = %v, want %v", len([]rune(tt.part)), got, tt.want)
		}
	}
}

func withMaxParts(n int) func(*config.Config) {
	return func(c *config.Config) {
		c.DefaultGroupPolicy.Reply.MaxParts = n
		c.DefaultGroupPolicy.Reply.Quote = conloomapi.QuoteAlways
	}
}

func TestRunner_SplitsReplyIntoParts(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)}
	p1, p2, p3, p4 := strings.Repeat("一", 10), strings.Repeat("二", 20), strings.Repeat("三", 30), "四"
	env := newTestOrchestrator(t, testOpts{
		now:       clock.now,
		short:     answer(p1 + "\n|||\n" + p2 + "\n|||\n" + p3 + "\n|||\n" + p4),
		configure: withMaxParts(3),
	})
	ev := env.groupEvent("20001", "你好", mentionBot, occurredAt(clock.now()))
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 3 {
		t.Fatalf("task = %+v", task)
	}
	done := clock.now().UTC()
	// The pause before a part is the time to type it: 20 runes take 2s, the
	// merged 32-rune last part 2.72s.
	want := []struct {
		text      string
		notBefore time.Time
	}{
		{p1, time.Time{}},
		{p2, done.Add(2000 * time.Millisecond)},
		{p3 + "\n" + p4, done.Add(2000*time.Millisecond + 2720*time.Millisecond)},
	}
	var firstExpires time.Time
	for i, a := range task.Actions {
		if a.Content.Text != want[i].text || a.PartIndex != i+1 || a.PartCount != 3 {
			t.Errorf("action %d = text %q part %d/%d, want %q part %d/3", i, a.Content.Text, a.PartIndex, a.PartCount, want[i].text, i+1)
		}
		if quoted := a.ReplyToMessageID != "" || a.ReplyToPlatformMessageID != ""; quoted != (i == 0) {
			t.Errorf("action %d reply target = %q/%q, want one only on the first part", i, a.ReplyToMessageID, a.ReplyToPlatformMessageID)
		}
		rec, err := env.st.GetOutbox(t.Context(), testBot, a.ActionID)
		if err != nil {
			t.Fatal(err)
		}
		if !rec.NotBefore.Equal(want[i].notBefore) {
			t.Errorf("action %d not before = %v, want %v", i, rec.NotBefore, want[i].notBefore)
		}
		if i == 0 {
			firstExpires = rec.ExpiresAt
		}
		if !rec.ExpiresAt.Equal(firstExpires) || !rec.NotBefore.Before(rec.ExpiresAt) {
			t.Errorf("action %d expires at %v (due %v), want the first part's expiry %v, after it is due", i, rec.ExpiresAt, rec.NotBefore, firstExpires)
		}
	}
	sys, _ := requestMessages(t, env.short.Requests()[0])[0]["content"].(string)
	if !strings.Contains(sys, partsDirective(3)) || !strings.Contains(sys, "1 到 3 条") {
		t.Errorf("system prompt lacks the parts directive: %q", sys)
	}
}

func TestRunner_BlockedReplyHasNoParts(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{
		short: answer("第一段\n|||\n这里有禁词"),
		configure: func(c *config.Config) {
			withMaxParts(3)(c)
			c.DefaultGroupPolicy.Review.BlockedWords = []string{"禁词"}
		},
	})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != "blocked" {
		t.Fatalf("task = %+v", task)
	}
}

func TestRunner_OnlySeparatorsSendsNothing(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: answer("|||\n|||"), configure: withMaxParts(3)})
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 0 || task.Meta.ReviewResult != reviewResultSkipped {
		t.Fatalf("task = %+v", task)
	}
}

func TestRunner_SinglePartReplyKeepsShape(t *testing.T) {
	for _, n := range []int{1, 3} {
		text := "第一段\n|||\n第二段"
		if n > 1 {
			text = "只有一段"
		}
		env := newTestOrchestrator(t, testOpts{short: answer(text), configure: withMaxParts(n)})
		task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "你好", mentionBot)).TaskID)
		if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("max_parts=%d: task = %+v", n, task)
		}
		a := task.Actions[0]
		if a.Content.Text != text || a.PartIndex != 0 || a.PartCount != 0 || a.ReplyToMessageID == "" {
			t.Errorf("max_parts=%d: action = %+v", n, a)
		}
		rec, err := env.st.GetOutbox(t.Context(), testBot, a.ActionID)
		if err != nil {
			t.Fatal(err)
		}
		if !rec.NotBefore.IsZero() {
			t.Errorf("max_parts=%d: not before = %v, want none", n, rec.NotBefore)
		}
		sys, _ := requestMessages(t, env.short.Requests()[0])[0]["content"].(string)
		if !strings.Contains(sys, partsDirective(n)) {
			t.Errorf("max_parts=%d: system prompt lacks %q", n, partsDirective(n))
		}
	}
}

func TestPartsDirective(t *testing.T) {
	if got := partsDirective(1); got != "回复只发一条消息。" {
		t.Errorf("partsDirective(1) = %q", got)
	}
	want := "回复可以像聊天那样分成 1 到 4 条短消息，条与条之间用单独一行的 ||| 分隔；不需要分时就只发一条。"
	if got := partsDirective(4); got != want {
		t.Errorf("partsDirective(4) = %q, want %q", got, want)
	}
}
