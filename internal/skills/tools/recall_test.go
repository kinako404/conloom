package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

// at is 09:00 plus minutes on 2026-11-13 in Asia/Shanghai.
func at(minutes int) time.Time {
	return time.Date(2026, 11, 13, 1, minutes, 0, 0, time.UTC)
}

func newRecall(st store.Store, memoryOn bool) *recallTool {
	return NewRecall(RecallDeps{Store: st, Policies: newPolicies(st, memoryOn, true),
		Reviewer: review.New(nil, review.Options{})}).(*recallTool)
}

// answering is an invocation for user in convKey answering triggers.
func answering(convKey, user, name string, triggers ...store.MessageRecord) skills.Invocation {
	inv := invocation(convKey, user, name)
	for _, m := range triggers {
		inv.TriggerMessageIDs = append(inv.TriggerMessageIDs, m.ID)
	}
	return inv
}

func assertNoIDs(t *testing.T, text string) {
	t.Helper()
	for _, id := range []string{"20001", "20002", "20003", "30001", "user_", "bot_", "msg_", "group_", "private_"} {
		if strings.Contains(text, id) {
			t.Errorf("recall result shows %q:\n%s", id, text)
		}
	}
}

func TestRecallTokens(t *testing.T) {
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"我喜欢草莓", []string{"我喜", "喜欢", "欢草", "草莓"}},
		{"喜欢 Go 1.26 和 SQLite", []string{"喜欢", "go", "1", "26", "sqlite"}},
		{"猫", []string{"猫"}},
		{"一二三四五六七八九十", []string{"一二", "二三", "三四", "四五", "五六", "六七", "七八", "八九"}},
		{"草莓草莓 GO go", []string{"草莓", "莓草", "go"}},
		{"？？！", nil},
	} {
		if got := recallTokens(c.query); !slices.Equal(got, c.want) {
			t.Errorf("recallTokens(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

func TestSearchTerms(t *testing.T) {
	for _, c := range []struct {
		query    string
		keywords []string
		want     []string
	}{
		{"旅游", nil, []string{"旅游"}},
		{"旅游", []string{"出去玩", "旅游", "Go 语言", "旅行"}, []string{"旅游", "出去玩", "go 语言", "旅行"}},
		{"？", []string{"出去玩"}, []string{"出去玩"}},
		{"一二三四五六七八", []string{"出去玩"}, []string{"一二", "二三", "三四", "四五", "五六", "六七", "七八", "出去玩"}},
		{"一二三四五六七八九", []string{"出去玩"}, []string{"一二", "二三", "三四", "四五", "五六", "六七", "七八", "八九"}},
	} {
		if got := searchTerms(c.query, c.keywords); !slices.Equal(got, c.want) {
			t.Errorf("searchTerms(%q, %q) = %q, want %q", c.query, c.keywords, got, c.want)
		}
	}
}

func TestRecall_Spec(t *testing.T) {
	spec := newRecall(newStore(t), true).Spec()
	checkSpec(t, spec, "recall", "query")
	if !strings.Contains(string(spec.Parameters), `"keywords"`) || !strings.Contains(spec.Description, "出去玩") {
		t.Errorf("recall spec lacks the keywords argument or its guidance: %+v", spec)
	}
}

func TestRecall_RanksMergesContextAndSkipsTheCurrentTurn(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	for i := range 30 {
		switch i {
		case 5:
			msgs = append(msgs, message(groupKey, "20001", "小明", "我最喜欢草莓蛋糕", at(i)))
		case 6:
			msgs = append(msgs, message(groupKey, "", "", "草莓蛋糕很好吃", at(i)))
		case 20:
			msgs = append(msgs, message(groupKey, "20002", "小红", "我不喜欢香菜", at(i)))
		default:
			msgs = append(msgs, message(groupKey, "20003", "", fmt.Sprintf("闲聊第%d句", i), at(i)))
		}
	}
	trigger := message(groupKey, "20001", "小明", "我以前说过喜欢草莓吗", at(31))
	msgs = append(msgs, message(groupKey, "", "", "好的", at(30)), trigger)
	addMessages(t, st, msgs...)
	addMessages(t, st, message(otherKey, "20001", "小明", "我喜欢草莓", at(1)))

	got := call(t, newRecall(st, true), answering(groupKey, "20001", "小明", trigger), `{"query":"喜欢草莓"}`)
	want := strings.Join([]string{
		"[11-13 09:03] 一位群友：闲聊第3句",
		"[11-13 09:04] 一位群友：闲聊第4句",
		"[11-13 09:05] 小明：我最喜欢草莓蛋糕",
		"[11-13 09:06] 你：草莓蛋糕很好吃",
		"[11-13 09:07] 一位群友：闲聊第7句",
		"[11-13 09:08] 一位群友：闲聊第8句",
		"",
		"[11-13 09:18] 一位群友：闲聊第18句",
		"[11-13 09:19] 一位群友：闲聊第19句",
		"[11-13 09:20] 小红：我不喜欢香菜",
		"[11-13 09:21] 一位群友：闲聊第21句",
		"[11-13 09:22] 一位群友：闲聊第22句",
	}, "\n")
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}
	assertNoIDs(t, got)
}

func TestRecall_CurrentTurn(t *testing.T) {
	st := newStore(t)
	tool := newRecall(st, true)
	query := `{"query":"草莓"}`

	long := testNow.Add(-5 * time.Hour)
	addMessages(t, st,
		message(groupKey, "", "", "大家好", long.Add(-time.Hour)),
		message(groupKey, "20001", "小明", "我喜欢草莓", long),
	)
	earlier := message(groupKey, "20001", "小明", "草莓季到了", testNow.Add(-30*time.Second))
	trigger := message(groupKey, "20001", "小明", "我说过喜欢草莓吗", testNow.Add(-10*time.Second))
	addMessages(t, st, earlier, trigger)

	got := call(t, tool, answering(groupKey, "20001", "小明", trigger), query)
	if !strings.Contains(got, "小明：我喜欢草莓") || !strings.Contains(got, "草莓季到了") || strings.Contains(got, "我说过喜欢草莓吗") {
		t.Fatalf("recall answering one trigger, with the bot silent for hours:\n%s", got)
	}

	addMessages(t, st, message(groupKey, "20002", "小红", "草莓好吃", testNow.Add(-time.Minute)),
		message(groupKey, "20001", "小明", "三分钟前说的草莓", testNow.Add(-3*time.Minute)))
	got = call(t, tool, invocation(groupKey, "20001", "小明"), query)
	for _, kept := range []string{"小明：我喜欢草莓", "小红：草莓好吃", "三分钟前说的草莓"} {
		if !strings.Contains(got, kept) {
			t.Errorf("recall without triggers lacks %q:\n%s", kept, got)
		}
	}
	if strings.Contains(got, "草莓季到了") || strings.Contains(got, "我说过喜欢草莓吗") {
		t.Errorf("recall without triggers shows the speaker's last two minutes:\n%s", got)
	}
}

func TestRecall_Keywords(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "下周去旅行", at(0)),
		message(groupKey, "20002", "小红", "周末一起出去玩吧", at(1)),
	)
	tool := newRecall(st, true)
	inv := invocation(groupKey, "20003", "小刚")
	if got := call(t, tool, inv, `{"query":"旅游计划"}`); got != recallNotFound {
		t.Fatalf("recall without keywords = %q", got)
	}
	got := call(t, tool, inv, `{"query":"旅游计划","keywords":["旅行","出去玩"]}`)
	if want := "[11-13 09:00] 小明：下周去旅行\n[11-13 09:01] 小红：周末一起出去玩吧"; got != want {
		t.Fatalf("recall with keywords =\n%s\nwant\n%s", got, want)
	}
	if got := call(t, tool, inv, `{"query":"旅游计划","keywords":null}`); got != recallNotFound {
		t.Fatalf("recall with null keywords = %q", got)
	}
}

func TestRecall_SkipsCommandsAndHidesSensitiveText(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	policies := newPolicies(st, true, true)
	pv, err := policies.Current(ctx, groupKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	if _, err := policies.Update(ctx, groupKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	addMessages(t, st,
		message(groupKey, "20001", "小明", "/记住 我喜欢草莓", at(0)),
		message(groupKey, "20001", "小明", " /remember 草莓 strawberry", at(1)),
		message(groupKey, "20002", "小红", "我电话 139 0000 2222", at(2)),
		message(groupKey, "20002", "小红", "草莓店的号码是 13800001111", at(3)),
		message(groupKey, "20003", "", "草莓坏词好吃", at(4)),
		message(groupKey, "", "", "草莓很甜", at(5)),
		message(groupKey, "20001", "小明", "/草莓派真好吃", at(6)),
	)
	inv := invocation(groupKey, "20003", "")
	got := call(t, NewRecall(RecallDeps{Store: st, Policies: policies, Reviewer: review.New(nil, review.Options{})}), inv, `{"query":"草莓"}`)
	want := "[11-13 09:02] 小红：我［联系方式］\n[11-13 09:03] 小红：草莓店的［联系方式］\n[11-13 09:05] 你：草莓很甜"
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}

	isCommand := func(text, prefix string) bool {
		return strings.HasPrefix(strings.TrimSpace(text), prefix+"记住") || strings.HasPrefix(strings.TrimSpace(text), prefix+"remember")
	}
	got = call(t, NewRecall(RecallDeps{Store: st, Policies: policies, Reviewer: review.New(nil, review.Options{}), IsCommand: isCommand}),
		inv, `{"query":"草莓"}`)
	if !strings.Contains(got, "小明：/草莓派真好吃") || strings.Contains(got, "记住") || strings.Contains(got, "remember") {
		t.Fatalf("recall with a command recognizer =\n%s", got)
	}

	p.Review.Action = review.VerdictSanitize
	if _, err := policies.Update(ctx, groupKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	got = call(t, NewRecall(RecallDeps{Store: st, Policies: policies, Reviewer: review.New(nil, review.Options{})}), inv, `{"query":"好吃"}`)
	if !strings.Contains(got, "草莓**好吃") || strings.Contains(got, "坏词") {
		t.Fatalf("recall with sanitized blocked words =\n%s", got)
	}
	if got := call(t, newRecall(st, true), inv, `{"query":"13800001111"}`); got != recallNotFound {
		t.Fatalf("recall found a redacted number:\n%s", got)
	}
}

func TestRecall_TopSixHits(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	for i := range 40 {
		text := fmt.Sprintf("闲聊第%d句", i)
		if i%5 == 0 {
			text = fmt.Sprintf("第%d次说到草莓", i)
		}
		msgs = append(msgs, message(groupKey, "20001", "小明", text, at(i)))
	}
	addMessages(t, st, msgs...)
	got := call(t, newRecall(st, true), invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`)
	hits := strings.Count(got, "说到草莓")
	if hits != 6 || strings.Contains(got, "第0次") || strings.Contains(got, "第5次") || !strings.Contains(got, "第35次") {
		t.Fatalf("recall kept %d hits, want the newest 6 of 8:\n%s", hits, got)
	}
}

func TestRecall_GroupHidesForgottenMembersEarlierMessages(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20002", "小红", "我也喜欢草莓", at(0)),
		message(groupKey, "20001", "小明", "草莓真甜", at(10)),
		message(groupKey, "20002", "小红", "今天又买了草莓", at(30)),
	)
	if err := st.ForgetMember(ctx, groupKey, "20002", at(20)); err != nil {
		t.Fatal(err)
	}
	got := call(t, newRecall(st, true), invocation(groupKey, "20003", ""), `{"query":"草莓"}`)
	if strings.Contains(got, "我也喜欢草莓") || !strings.Contains(got, "小明：草莓真甜") || !strings.Contains(got, "今天又买了草莓") {
		t.Fatalf("recall after a member was forgotten:\n%s", got)
	}
}

func TestRecall_HiddenMessagesAreNotContext(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "闲聊甲", at(0)),
		message(groupKey, "20002", "小红", "秘密一", at(1)),
		message(groupKey, "20001", "小明", "草莓真好", at(2)),
		message(groupKey, "20002", "小红", "秘密二", at(3)),
		message(groupKey, "20001", "小明", "闲聊乙", at(4)),
	)
	if err := st.ForgetMember(ctx, groupKey, "20002", at(3).Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	got := call(t, newRecall(st, true), invocation(groupKey, "20003", ""), `{"query":"草莓"}`)
	if want := "[11-13 09:00] 小明：闲聊甲\n[11-13 09:02] 小明：草莓真好\n[11-13 09:04] 小明：闲聊乙"; got != want {
		t.Fatalf("recall next to hidden messages =\n%s\nwant\n%s", got, want)
	}
}

func TestRecall_PrivateHidesEverythingBeforeForgetting(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	addMessages(t, st,
		message(privateKey, "20001", "小明", "我喜欢草莓", at(0)),
		message(privateKey, "", "", "草莓很甜", at(1)),
	)
	if err := st.ForgetPrivate(ctx, privateKey, "20001", at(5)); err != nil {
		t.Fatal(err)
	}
	trigger := message(privateKey, "20001", "小明", "我说过喜欢草莓吗", at(8))
	addMessages(t, st,
		message(privateKey, "20001", "小明", "今天吃了草莓", at(6)),
		message(privateKey, "", "", "真好", at(7)),
		trigger,
	)
	got := call(t, newRecall(st, true), answering(privateKey, "20001", "小明", trigger), `{"query":"草莓"}`)
	if want := "[11-13 09:06] 小明：今天吃了草莓\n[11-13 09:07] 你：真好"; got != want {
		t.Fatalf("private recall after forgetting =\n%s\nwant\n%s", got, want)
	}
}

func TestRecall_NamesWithoutIDs(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20003", "", "草莓一号", at(0)),
		message(groupKey, "20003", "20003", "草莓二号", at(1)),
		message(groupKey, "20002", "  小红\n", "草莓\n三号", at(2)),
		message(groupKey, "", "", "草莓四号", at(3)),
		message(groupKey, "20002", strings.Repeat("长", 25), "草莓\u200b五\u0007号\u202e", at(4)),
		message(groupKey, "20003", "\u200b\u200d", "草莓六号", at(5)),
	)
	got := call(t, newRecall(st, true), invocation(groupKey, "20001", "小明"), `{"query":"草莓"}`)
	want := strings.Join([]string{
		"[11-13 09:00] 一位群友：草莓一号",
		"[11-13 09:01] 一位群友：草莓二号",
		"[11-13 09:02] 小红：草莓 三号",
		"[11-13 09:03] 你：草莓四号",
		"[11-13 09:04] " + strings.Repeat("长", 20) + "：草莓五号",
		"[11-13 09:05] 一位群友：草莓六号",
	}, "\n")
	if got != want {
		t.Fatalf("recall =\n%s\nwant\n%s", got, want)
	}
	assertNoIDs(t, got)
}

func TestRecall_LengthCap(t *testing.T) {
	st := newStore(t)
	var msgs []store.MessageRecord
	for i := range 40 {
		msgs = append(msgs, message(groupKey, "20001", "小明", "草莓"+strings.Repeat("长", 400), at(i)))
	}
	addMessages(t, st, msgs...)
	got := call(t, newRecall(st, true), invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`)
	if n := utf8.RuneCountInString(got); n > 1500 || n < 1000 {
		t.Fatalf("recall has %d runes, want close to but at most 1500", n)
	}
	for _, line := range strings.Split(got, "\n") {
		if utf8.RuneCountInString(line) > 230 {
			t.Fatalf("line of %d runes, want each body capped", utf8.RuneCountInString(line))
		}
	}
}

// limitStore serves msgs as a conversation's history and records the limits
// asked for.
type limitStore struct {
	msgs   []store.MessageRecord
	limits []int
}

func (s *limitStore) RecentMessages(_ context.Context, _ string, limit int) ([]store.MessageRecord, error) {
	s.limits = append(s.limits, limit)
	return slices.Clone(s.msgs[max(len(s.msgs)-limit, 0):]), nil
}

func (s *limitStore) ListMemberMemories(context.Context, string) ([]store.MemberMemory, error) {
	return nil, nil
}

func TestRecall_LooksAtTheNewest5000(t *testing.T) {
	fake := &limitStore{msgs: []store.MessageRecord{message(groupKey, "20001", "小明", "很久以前说过草莓", at(0))}}
	for i := range 5000 {
		fake.msgs = append(fake.msgs, message(groupKey, "20001", "小明", "闲聊", at(1).Add(time.Duration(i)*time.Second)))
	}
	st := newStore(t)
	tool := NewRecall(RecallDeps{Store: fake, Policies: newPolicies(st, true, true), Reviewer: review.New(nil, review.Options{})})
	if got := call(t, tool, invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`); got != recallNotFound {
		t.Fatalf("recall beyond the newest 5000 = %q", got)
	}
	if !slices.Equal(fake.limits, []int{5000}) {
		t.Fatalf("RecentMessages limits = %v, want [5000]", fake.limits)
	}
}

func TestRecall_NotFoundDisabledAndArguments(t *testing.T) {
	st := newStore(t)
	addMessages(t, st, message(groupKey, "20001", "小明", "今天天气不错", at(0)))
	inv := invocation(groupKey, "20002", "小红")
	argCases := []struct{ args, want string }{
		{`{}`, "需要提供要查找的内容（query）。"},
		{`{"query":["草莓"]}`, "需要提供要查找的内容（query）。"},
		{`oops`, "需要提供要查找的内容（query）。"},
		{`{"query":"  "}`, "要查找的内容为空。"},
		{`{"query":"` + strings.Repeat("长", 101) + `"}`, "要查找的内容超过 100 字。"},
		{`{"query":"草莓","keywords":"草莓"}`, "keywords 需要是字符串列表。"},
		{`{"query":"草莓","keywords":["草莓",3]}`, "keywords 需要是字符串列表。"},
		{`{"query":"草莓","keywords":["1","2","3","4","5","6","7","8","9"]}`, "keywords 最多 8 个。"},
		{`{"query":"草莓","keywords":[" "]}`, "keywords 里每个词要 1 到 20 字。"},
		{`{"query":"草莓","keywords":["` + strings.Repeat("长", 21) + `"]}`, "keywords 里每个词要 1 到 20 字。"},
	}
	tool := newRecall(st, true)
	for _, c := range append(argCases,
		struct{ args, want string }{`{"query":"草莓"}`, recallNotFound},
		struct{ args, want string }{`{"query":"？！"}`, recallNotFound}) {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("recall(%.30s) = %q, want %q", c.args, got, c.want)
		}
	}
	off := newRecall(st, false)
	for _, c := range argCases {
		if got := call(t, off, inv, c.args); got != c.want {
			t.Errorf("recall(%.30s) with memory off = %q, want the argument checked first: %q", c.args, got, c.want)
		}
	}
	if got := call(t, off, inv, `{"query":"天气"}`); got != "这个会话没有开启记忆。" {
		t.Errorf("recall with memory off = %q", got)
	}
}
