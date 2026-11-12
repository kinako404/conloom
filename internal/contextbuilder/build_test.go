package contextbuilder

import (
	"fmt"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var shanghai = time.FixedZone("CST", 8*3600)

func at(hour, minute int) time.Time {
	return time.Date(2026, 11, 12, hour-8, minute, 0, 0, time.UTC)
}

func userMsg(name, text string, ts time.Time) store.MessageRecord {
	return store.MessageRecord{PlatformUserID: "20001", DisplayName: name, Text: text, OccurredAt: ts}
}

func botMsg(text string, ts time.Time) store.MessageRecord {
	return store.MessageRecord{PlatformUserID: "30001", DisplayName: "织语", Text: text, FromBot: true, OccurredAt: ts}
}

func baseInput() Input {
	return Input{
		SystemPrompt:     "你是织语。",
		ConversationType: "group",
		Current:          []store.MessageRecord{userMsg("小明", "今天天气如何", at(12, 5))},
		Budget:           10000,
		MaxRecent:        50,
		TokenFactor:      1,
		Location:         shanghai,
	}
}

func assertNoEmptyContent(t *testing.T, msgs []openai.ChatMessage) {
	t.Helper()
	for i, m := range msgs {
		if m.Content == "" {
			t.Fatalf("message %d (%s) has empty content: %+v", i, m.Role, msgs)
		}
	}
}

func roles(msgs []openai.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

func TestEstimate(t *testing.T) {
	cases := []struct {
		text   string
		factor float64
		want   int
	}{
		{"你好世界", 1, 4},
		{"abcdefgh", 1, 2},
		{"abc", 1, 1},
		{"你好世界", 1.5, 6},
		{"", 1, 0},
		{"你好ab", 1, 3},
		{"abc", 0, 1},
		{"abc", -2, 1},
	}
	for _, c := range cases {
		if got := Estimate(c.text, c.factor); got != c.want {
			t.Errorf("Estimate(%q, %v) = %d, want %d", c.text, c.factor, got, c.want)
		}
	}
}

func TestBuild_OrderAndRoles(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{
		userMsg("小明", "在吗", at(12, 0)),
		botMsg("在", at(12, 1)),
	}
	out := Build(in)
	want := []openai.ChatMessage{
		{Role: "system", Content: "你是织语。"},
		{Role: "user", Content: "[12:00] 小明：在吗"},
		{Role: "assistant", Content: "在"},
		{Role: "user", Content: "【当前消息】发送者：小明\n今天天气如何"},
	}
	if len(out.Messages) != len(want) {
		t.Fatalf("messages = %+v, want %+v", out.Messages, want)
	}
	for i := range want {
		if out.Messages[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, out.Messages[i], want[i])
		}
	}
	if out.IncludedHistory != 2 || out.DroppedHistory != 0 {
		t.Errorf("included=%d dropped=%d, want 2/0", out.IncludedHistory, out.DroppedHistory)
	}
	if out.EstimatedTokens <= 0 {
		t.Errorf("EstimatedTokens = %d, want > 0", out.EstimatedTokens)
	}
}

func TestBuild_MergesAdjacentSameRole(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{
		userMsg("小明", "在吗", at(12, 0)),
		{PlatformUserID: "20002", DisplayName: "小红", Text: "我也在", OccurredAt: at(12, 1)},
	}
	out := Build(in)
	if got := roles(out.Messages); strings.Join(got, ",") != "system,user,user" {
		t.Fatalf("roles = %v, want [system user user]", got)
	}
	if got, want := out.Messages[1].Content, "[12:00] 小明：在吗\n[12:01] 小红：我也在"; got != want {
		t.Errorf("merged content = %q, want %q", got, want)
	}
	if out.IncludedHistory != 2 {
		t.Errorf("IncludedHistory = %d, want 2", out.IncludedHistory)
	}
}

func alternatingHistory(n int) []store.MessageRecord {
	h := make([]store.MessageRecord, n)
	for i := range h {
		text := fmt.Sprintf("第%d条历史消息内容", i)
		if i%2 == 0 {
			h[i] = userMsg("小明", text, at(10, i))
		} else {
			h[i] = botMsg(text, at(10, i))
		}
	}
	return h
}

func TestBuild_TrimsOldestWithinBudget(t *testing.T) {
	in := baseInput()
	in.History = alternatingHistory(20)
	in.Budget = BaseTokens(in) + 60
	out := Build(in)

	if out.IncludedHistory <= 0 || out.IncludedHistory >= 20 {
		t.Fatalf("IncludedHistory = %d, want between 1 and 19", out.IncludedHistory)
	}
	if out.DroppedHistory != 20-out.IncludedHistory {
		t.Errorf("DroppedHistory = %d, want %d", out.DroppedHistory, 20-out.IncludedHistory)
	}
	if out.EstimatedTokens > in.Budget {
		t.Errorf("EstimatedTokens = %d exceeds Budget %d", out.EstimatedTokens, in.Budget)
	}
	joined := ""
	for _, m := range out.Messages {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "第19条历史消息内容") {
		t.Errorf("newest history missing from %q", joined)
	}
	if strings.Contains(joined, "第0条历史消息内容") {
		t.Errorf("oldest history kept in %q", joined)
	}
	assertNoEmptyContent(t, out.Messages)
}

func TestBuild_MaxRecent(t *testing.T) {
	in := baseInput()
	in.History = alternatingHistory(10)
	in.MaxRecent = 3
	out := Build(in)

	if out.IncludedHistory+out.DroppedHistory != 10 {
		t.Fatalf("included=%d dropped=%d, want sum 10", out.IncludedHistory, out.DroppedHistory)
	}
	if out.IncludedHistory > 3 {
		t.Fatalf("IncludedHistory = %d, want <= 3", out.IncludedHistory)
	}
	// Newest three are 7(bot), 8(user), 9(bot); none start with assistant
	// after the leading bot message 7 is dropped.
	if out.IncludedHistory != 2 || out.DroppedHistory != 8 {
		t.Errorf("included=%d dropped=%d, want 2/8", out.IncludedHistory, out.DroppedHistory)
	}
	joined := ""
	for _, m := range out.Messages {
		joined += m.Content + "\n"
	}
	for _, want := range []string{"第8条历史消息内容", "第9条历史消息内容"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if strings.Contains(joined, "第6条历史消息内容") || strings.Contains(joined, "第7条历史消息内容") {
		t.Errorf("older history kept in %q", joined)
	}
}

func TestBuild_CurrentIncludesSpeakerAndReplyTarget(t *testing.T) {
	in := baseInput()
	target := botMsg(strings.Repeat("长", 150), at(11, 0))
	in.ReplyTarget = &target
	in.Current = append(in.Current, userMsg("小明", "补充一句", at(12, 6)))
	out := Build(in)

	last := out.Messages[len(out.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("last role = %q, want user", last.Role)
	}
	lines := strings.Split(last.Content, "\n")
	if len(lines) != 4 {
		t.Fatalf("current lines = %q, want 4 lines", lines)
	}
	if lines[0] != "【当前消息】发送者：小明" {
		t.Errorf("line 0 = %q", lines[0])
	}
	wantReply := "（回复 织语 的消息：" + strings.Repeat("长", 100) + "）"
	if lines[1] != wantReply {
		t.Errorf("line 1 = %q, want %q", lines[1], wantReply)
	}
	if lines[2] != "今天天气如何" || lines[3] != "补充一句" {
		t.Errorf("body lines = %q", lines[2:])
	}
	if !strings.Contains(last.Content, "（回复 织语 的消息：") {
		t.Errorf("reply marker missing in %q", last.Content)
	}
}

func TestBuild_ImageOnlyMessageRendersPlaceholder(t *testing.T) {
	in := baseInput()
	img := []conloomapi.Attachment{{Type: "image", URL: "https://example.invalid/a.png"}}
	in.History = []store.MessageRecord{
		{PlatformUserID: "20002", DisplayName: "小红", Attachments: img, OccurredAt: at(12, 0)},
		{PlatformUserID: "20002", DisplayName: "小红", OccurredAt: at(12, 1)},
		{PlatformUserID: "20002", DisplayName: "小红", Text: "看这个", Attachments: []conloomapi.Attachment{{Type: "file", Name: "a.pdf"}}, OccurredAt: at(12, 2)},
	}
	in.Current = []store.MessageRecord{{PlatformUserID: "20001", DisplayName: "小明", Attachments: img, OccurredAt: at(12, 5)}}
	out := Build(in)

	assertNoEmptyContent(t, out.Messages)
	if got, want := out.Messages[1].Content, "[12:00] 小红：[图片]\n[12:01] 小红：[空消息]\n[12:02] 小红：看这个[文件]"; got != want {
		t.Errorf("history content = %q, want %q", got, want)
	}
	if got, want := out.Messages[len(out.Messages)-1].Content, "【当前消息】发送者：小明\n[图片]"; got != want {
		t.Errorf("current content = %q, want %q", got, want)
	}
}

func TestBuild_NeverStartsWithAssistant(t *testing.T) {
	in := baseInput()
	in.History = []store.MessageRecord{
		botMsg("早上好", at(9, 0)),
		botMsg("有人吗", at(9, 1)),
		userMsg("小明", "在", at(9, 2)),
	}
	out := Build(in)
	if out.Messages[1].Role != "user" {
		t.Fatalf("first non-system role = %q, want user: %+v", out.Messages[1].Role, out.Messages)
	}
	if out.IncludedHistory != 1 || out.DroppedHistory != 2 {
		t.Errorf("included=%d dropped=%d, want 1/2", out.IncludedHistory, out.DroppedHistory)
	}
	assertNoEmptyContent(t, out.Messages)
}

func TestBuild_NeverDropsCurrentEvenIfOverBudget(t *testing.T) {
	in := baseInput()
	in.History = alternatingHistory(5)
	in.Budget = 1
	out := Build(in)
	if got := roles(out.Messages); strings.Join(got, ",") != "system,user" {
		t.Fatalf("roles = %v, want [system user]", got)
	}
	if !strings.HasPrefix(out.Messages[1].Content, "【当前消息】发送者：小明") {
		t.Errorf("current message = %q", out.Messages[1].Content)
	}
	if out.IncludedHistory != 0 || out.DroppedHistory != 5 {
		t.Errorf("included=%d dropped=%d, want 0/5", out.IncludedHistory, out.DroppedHistory)
	}
	if out.EstimatedTokens != BaseTokens(in) {
		t.Errorf("EstimatedTokens = %d, want BaseTokens %d", out.EstimatedTokens, BaseTokens(in))
	}
}

func TestBuild_NilLocationDefaultsToShanghai(t *testing.T) {
	in := baseInput()
	in.Location = nil
	in.History = []store.MessageRecord{userMsg("小明", "在吗", at(12, 0))}
	out := Build(in)
	if got := out.Messages[1].Content; got != "[12:00] 小明：在吗" {
		t.Errorf("content = %q, want display time in Asia/Shanghai", got)
	}
}
