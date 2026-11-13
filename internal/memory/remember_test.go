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
		"138 0000 1111",
		"138-0000-1111",
		"打 138.0000.1111 找我",
		"13800001111",
		"QQ 2000 1234",
		"我的 QQ 是 20001234",
		"座机 010-12345678",
		"+86 138 0000 1111",
		"身份证 110101 19900101 123X",
		"2000123456",
		"13800001111 9点后打给我",
		"明天 11:30 13800001111 联系我",
		"13800001111/13900002222",
		"QQ 2000123456 3点后在线",
		"12:30 20001234@qq.com",
		"138-0000-1111-2",
		"1380 0001 1111",
		"138 00001111",
		"010 12345678",
		"021-1234-5678",
		"110101 1990 0101 123X",
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
		"2026-11-20 10点考试",
		"2026 11 13 10 30 开会",
		"11:30 吃饭",
		"2026-11-13 11:30 开会",
		"2026.11.20 出发",
		"下午 3:30 到 5:00",
		"11 月 20 号 10 点",
		"2026/11/20 10:00",
		"2026-11-20 10点",
		"2026 11 13 10 30",
		"11:30",
	} {
		if LooksLikeContactOrID(s) {
			t.Errorf("LooksLikeContactOrID(%q) = true, want false", s)
		}
	}
}

func TestRedactContactOrID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"我的手机号是 13800001111，记得打", "我的［联系方式］，记得打"},
		{"打 138-0000-1111 或 138 0000 2222 都行", "打 ［联系方式］ 或 ［联系方式］ 都行"},
		{"邮箱 xiaoming@example.com 常用", "邮箱 ［联系方式］ 常用"},
		{"身份证 12345678901234567X。", "［联系方式］。"},
		{"加我 vx：xm2026 吧", "加我 ［联系方式］ 吧"},
		{"电话 +86 138 0000 1111", "［联系方式］"},
		{"号码 1234567 和 QQ 20001", "［联系方式］ 和 ［联系方式］"},
		{"我 2026 年毕业，猫 3 岁", "我 2026 年毕业，猫 3 岁"},
		{"2026-11-20 10点考试，电话 138 0000 1111", "2026-11-20 10点考试，［联系方式］"},
		{"11:30 打 13800001111", "11:30 打 ［联系方式］"},
		{"13800001111/13900002222", "［联系方式］/［联系方式］"},
		{"13800001111 9点后打给我", "［联系方式］ 9点后打给我"},
		{"12:30 20001234@qq.com", "12:30 ［联系方式］"},
		{"QQ 2000123456 3点后在线", "［联系方式］ 3点后在线"},
		{"", ""},
	} {
		got := RedactContactOrID(c.in)
		if got != c.want {
			t.Errorf("RedactContactOrID(%q) = %q, want %q", c.in, got, c.want)
		}
		if LooksLikeContactOrID(c.in) != (got != c.in) || LooksLikeContactOrID(got) {
			t.Errorf("RedactContactOrID(%q) = %q disagrees with LooksLikeContactOrID", c.in, got)
		}
	}
}

func TestRemember_DatesAreNotContacts(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	deps := RememberDeps{Store: st, Reviewer: review.New(nil, review.Options{})}
	for content, want := range map[string]RememberStatus{
		"2026-11-20 10点考试":    Remembered,
		"2026 11 13 10 30 开会": Remembered,
		"每天 11:30 吃午饭":        Remembered,
		"我的电话 138 0000 1111":  RememberSensitive,
		"138-0000-1111 是我的新号": RememberSensitive,
	} {
		got, err := Remember(ctx, deps, RememberRequest{ConversationKey: testConvKey, UserID: "20001", Content: content})
		if err != nil || got != want {
			t.Errorf("Remember(%q) = %v, %v; want %v", content, got, err, want)
		}
	}
}

func TestRemember_Reason(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	got, err := Remember(ctx, RememberDeps{Store: st, Reviewer: review.New(nil, review.Options{})}, RememberRequest{
		ConversationKey: testConvKey, UserID: "20001", Content: "我喜欢草莓", Reason: "remember_tool",
	})
	if err != nil || got != Remembered {
		t.Fatalf("Remember = %v, %v", got, err)
	}
	revs, err := st.ListMemoryRevisions(ctx, testConvKey, "20001")
	if err != nil || len(revs) != 1 || revs[0].Source != store.RevisionCommand || revs[0].Reason != "remember_tool" {
		t.Fatalf("revisions = %+v, %v", revs, err)
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
