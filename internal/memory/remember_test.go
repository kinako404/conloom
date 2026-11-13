package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestLooksLikeContactOrID(t *testing.T) {
	for _, s := range []string{
		"我的手机号是 13800001111",
		"手机 138-0000-1111",
		"电话 +86 138 0000 1111",
		"邮箱 xiaoming@example.com",
		"身份证 123456789012345678",
		"证件号 12345678901234567X",
		"QQ 号 20001",
		"我的qq是2000123",
		"扣扣：20001",
		"微信 xiaoming_01",
		"vx：xm2026",
		"WeChat id: xiao_ming",
		"我的号码是 2000123456",
	} {
		if !LooksLikeContactOrID(s) {
			t.Errorf("LooksLikeContactOrID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"我下周三考试",
		"我 2026 年毕业",
		"我家猫 3 岁了",
		"我喜欢玩qq飞车",
		"我的微信头像是猫",
		"比分是 3:2",
		"我在学 Go 1.26",
	} {
		if LooksLikeContactOrID(s) {
			t.Errorf("LooksLikeContactOrID(%q) = true, want false", s)
		}
	}
}

func TestWithoutRemembered(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"喜欢猫。", "喜欢猫。"},
		{"喜欢猫。（他让我记住）我下周三考试", "喜欢猫。"},
		{"喜欢猫。 （他让我记住）我下周三考试（他让我记住）周末要搬家", "喜欢猫。"},
		{"（他让我记住）我下周三考试", ""},
		{"", ""},
	} {
		if got := WithoutRemembered(c.in); got != c.want {
			t.Errorf("WithoutRemembered(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemember_SharedOutcomes(t *testing.T) {
	ctx := context.Background()
	rv := review.New(nil, review.Options{})
	block := conloomapi.ReviewPolicy{Action: review.VerdictBlock, BlockedWords: []string{"坏词"}}
	sanitize := conloomapi.ReviewPolicy{Action: review.VerdictSanitize, BlockedWords: []string{"坏词"}}
	for _, c := range []struct {
		name, content string
		pol           conloomapi.ReviewPolicy
		want          RememberStatus
		note          string
	}{
		{"remembered", "  我下周三  考试 ", block, Remembered, RememberMarker + "我下周三 考试"},
		{"empty", "  \n ", block, RememberEmpty, ""},
		{"too long", strings.Repeat("长", RememberMaxRunes+1), block, RememberTooLong, ""},
		{"exactly the limit", strings.Repeat("长", RememberMaxRunes), block, Remembered, RememberMarker + strings.Repeat("长", RememberMaxRunes)},
		{"contact", "我的手机号是 13800001111", block, RememberSensitive, ""},
		{"blocked word", "他是个坏词人", block, RememberBlocked, ""},
		{"sanitized", "他是个坏词人", sanitize, Remembered, RememberMarker + "他是个**人"},
		{"control characters", "我下周\u0007三考试", block, Remembered, RememberMarker + "我下周三考试"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := newTestStore(t)
			got, err := Remember(ctx, RememberDeps{Store: st, Reviewer: rv}, RememberRequest{
				ConversationKey: testConvKey, UserID: "20001", DisplayName: "小明", Content: c.content, Review: c.pol,
			})
			if err != nil || got != c.want {
				t.Fatalf("Remember = %v, %v; want %v", got, err, c.want)
			}
			rows, err := st.GetMemberMemories(ctx, testConvKey, []string{"20001"})
			if err != nil {
				t.Fatal(err)
			}
			m, ok := rows["20001"]
			if c.note == "" {
				if ok {
					t.Fatalf("member written: %+v", m)
				}
				return
			}
			if m.Note != c.note || m.DisplayName != "小明" {
				t.Fatalf("member = %+v, want note %q named 小明", m, c.note)
			}
			revs, err := st.ListMemoryRevisions(ctx, testConvKey, "20001")
			if err != nil || len(revs) != 1 || revs[0].Source != store.RevisionCommand || revs[0].Reason != ReasonRemember ||
				revs[0].Field != store.FieldNote || revs[0].NewValue != c.note {
				t.Fatalf("revisions = %+v, %v", revs, err)
			}
		})
	}
}

func TestRemember_TrimsToTheConversationLimit(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sentence := strings.Repeat("甲", 30) + "。"
	old := strings.Repeat(sentence, PrivateNoteMaxRunes/31)
	putMember(t, st, store.MemberMemory{ConversationKey: testPrivateKey, PlatformUserID: "20001", Note: old})
	content := strings.Repeat("乙", RememberMaxRunes)
	got, err := Remember(ctx, RememberDeps{Store: st, Reviewer: review.New(nil, review.Options{})}, RememberRequest{
		ConversationKey: testPrivateKey, UserID: "20001", Content: content,
	})
	if err != nil || got != Remembered {
		t.Fatalf("Remember = %v, %v", got, err)
	}
	if m := getMember(t, st, testPrivateKey, "20001"); m.Note != AppendRemembered(old, content, PrivateNoteMaxRunes) {
		t.Fatalf("note = %q", m.Note)
	}
}
