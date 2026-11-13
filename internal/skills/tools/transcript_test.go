package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func transcriptOf(t *testing.T, st store.Store, convKey, convType string, pol conloomapi.GroupPolicy,
	isCommand func(text, prefix string) bool) ([]store.MessageRecord, []TranscriptLine) {
	t.Helper()
	ctx := context.Background()
	msgs, err := st.MessagesAfter(ctx, convKey, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	members, err := st.ListMemberMemories(ctx, convKey)
	if err != nil {
		t.Fatal(err)
	}
	return msgs, Transcript(msgs, members, convType, pol, review.New(nil, review.Options{}), isCommand, shanghai)
}

func lineTexts(lines []TranscriptLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func TestTranscript_FollowsRecallsRules(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "/记忆", at(0)),
		message(groupKey, "", "", "我记得关于你的：喜欢草莓蛋糕", at(0).Add(10*time.Second)),
		message(groupKey, "20002", "小红", "我电话 139 0000 2222", at(2)),
		message(groupKey, "20003", "", "草莓坏词好吃", at(4)),
		message(groupKey, "", "", "草莓很甜", at(5)),
		message(groupKey, "20002", "  小红\n", "草莓\n三号"+strings.Repeat("长", 210), at(6)),
	)
	pol := conloomapi.DefaultGroupPolicy()
	pol.Review.BlockedWords, pol.Review.Action = []string{"坏词"}, review.VerdictBlock

	msgs, lines := transcriptOf(t, st, groupKey, conloomapi.ConvGroup, pol, nil)
	want := "[11-13 09:02] 小红：我［联系方式］\n" +
		"[11-13 09:05] 你：草莓很甜\n" +
		"[11-13 09:06] 小红：草莓 三号" + strings.Repeat("长", 195) + "…\n"
	if got := lineTexts(lines); got != want {
		t.Fatalf("transcript =\n%s\nwant\n%s", got, want)
	}
	assertNoIDs(t, lineTexts(lines))
	for i, wantSeq := range []int64{msgs[2].Seq, msgs[4].Seq, msgs[5].Seq} {
		if lines[i].Seq != wantSeq || !lines[i].At.Equal(msgs[[]int{2, 4, 5}[i]].OccurredAt) {
			t.Errorf("line %d = seq %d at %s, want seq %d", i, lines[i].Seq, lines[i].At, wantSeq)
		}
	}

	pol.Review.Action = review.VerdictSanitize
	_, lines = transcriptOf(t, st, groupKey, conloomapi.ConvGroup, pol, nil)
	if got := lineTexts(lines); !strings.Contains(got, "一位群友：草莓**好吃") || strings.Contains(got, "坏词") {
		t.Fatalf("sanitized transcript =\n%s", got)
	}
}

func TestTranscript_UsesTheCommandRecognizer(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "/记住 我喜欢草莓", at(0)),
		message(groupKey, "20001", "小明", "/草莓派真好吃", at(1)),
	)
	isCommand := func(text, prefix string) bool { return strings.HasPrefix(strings.TrimSpace(text), prefix+"记住") }
	_, lines := transcriptOf(t, st, groupKey, conloomapi.ConvGroup, conloomapi.DefaultGroupPolicy(), isCommand)
	if got := lineTexts(lines); got != "[11-13 09:01] 小明：/草莓派真好吃\n" {
		t.Fatalf("transcript =\n%s", got)
	}
}

func TestTranscript_GroupForgetting(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20002", "小红", "我也喜欢草莓", at(0)),
		message(groupKey, "20001", "小明", "草莓真甜", at(10)),
		message(groupKey, "", "", "是呀", at(11)),
		message(groupKey, "20002", "小红", "今天又买了草莓", at(30)),
	)
	if err := st.ForgetMember(ctx, groupKey, "20002", at(20)); err != nil {
		t.Fatal(err)
	}
	_, lines := transcriptOf(t, st, groupKey, conloomapi.ConvGroup, conloomapi.DefaultGroupPolicy(), nil)
	want := "[11-13 09:10] 小明：草莓真甜\n[11-13 09:11] 你：是呀\n[11-13 09:30] 小红：今天又买了草莓\n"
	if got := lineTexts(lines); got != want {
		t.Fatalf("transcript =\n%s\nwant\n%s", got, want)
	}
}

func TestTranscript_PrivateForgetting(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addMessages(t, st,
		message(privateKey, "20001", "小明", "我喜欢草莓", at(0)),
		message(privateKey, "", "", "草莓很甜", at(1)),
	)
	if err := st.ForgetPrivate(ctx, privateKey, "20001", at(5)); err != nil {
		t.Fatal(err)
	}
	addMessages(t, st,
		message(privateKey, "20001", "小明", "今天吃了草莓", at(6)),
		message(privateKey, "", "", "真好", at(7)),
	)
	_, lines := transcriptOf(t, st, privateKey, conloomapi.ConvPrivate, conloomapi.DefaultGroupPolicy(), nil)
	if got, want := lineTexts(lines), "[11-13 09:06] 小明：今天吃了草莓\n[11-13 09:07] 你：真好\n"; got != want {
		t.Fatalf("transcript =\n%s\nwant\n%s", got, want)
	}
}

func TestMemoryOn(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		memory, long, want bool
	}{{true, true, true}, {false, true, false}, {true, false, false}} {
		p := conloomapi.DefaultGroupPolicy()
		p.Memory.Enabled, p.Concurrency.AllowLongRequests = c.memory, c.long
		pv, on, err := MemoryOn(ctx, constPolicy{p}, groupKey)
		if err != nil || on != c.want || pv.Policy.Memory.Enabled != c.memory {
			t.Errorf("MemoryOn(memory=%v, long=%v) = %v, %v", c.memory, c.long, on, err)
		}
	}
}

type constPolicy struct{ p conloomapi.GroupPolicy }

func (c constPolicy) Current(context.Context, string) (store.PolicyVersion, error) {
	return store.PolicyVersion{Policy: c.p}, nil
}
