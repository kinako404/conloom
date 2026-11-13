package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

// day1113 is the date header of groups from 2026-11-13.
const day1113 = "—— 2026-11-13 ——"

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

func TestRecall_Spec(t *testing.T) {
	spec := newRecall(newStore(t), true).Spec()
	checkSpec(t, spec, "recall", "query")
	for _, want := range []string{`"keywords"`, `"after"`, `"before"`, "YYYY-MM-DD"} {
		if !strings.Contains(string(spec.Parameters), want) {
			t.Errorf("recall parameters lack %s: %s", want, spec.Parameters)
		}
	}
	for _, want := range []string{"出去玩", "after", "before", "——"} {
		if !strings.Contains(spec.Description, want) {
			t.Errorf("recall description lacks %q: %s", want, spec.Description)
		}
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
		day1113,
		"[11-13 09:03] 一位群友：闲聊第3句",
		"[11-13 09:04] 一位群友：闲聊第4句",
		"[11-13 09:05] 小明：我最喜欢草莓蛋糕",
		"[11-13 09:06] 你：草莓蛋糕很好吃",
		"[11-13 09:07] 一位群友：闲聊第7句",
		"[11-13 09:08] 一位群友：闲聊第8句",
		"",
		day1113,
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
	want := day1113 + "\n[11-13 09:00] 小明：下周去旅行\n[11-13 09:01] 小红：周末一起出去玩吧"
	if got := call(t, tool, inv, `{"query":"旅游计划","keywords":["旅行","出去玩"]}`); got != want {
		t.Fatalf("recall with keywords =\n%s\nwant\n%s", got, want)
	}
	if got := call(t, tool, inv, `{"query":"旅游计划","keywords":null}`); got != recallNotFound {
		t.Fatalf("recall with null keywords = %q", got)
	}
	long := `{"query":"我们之前在群里讨论过的那个暑假计划安排","keywords":["旅行","出去玩"]}`
	if got := call(t, tool, inv, long); got != want {
		t.Fatalf("recall with a long query and keywords =\n%s\nwant\n%s", got, want)
	}
}

func TestRecall_SkipsBotRepliesToCommands(t *testing.T) {
	ctx := context.Background()
	isCommand := func(text, prefix string) bool {
		for _, c := range []string{"记忆", "好感度", "忘记我"} {
			if strings.HasPrefix(strings.TrimSpace(text), prefix+c) {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"prefix fallback", "command recognizer"} {
		st := newStore(t)
		addMessages(t, st,
			message(groupKey, "20001", "小明", "/记忆", at(0)),
			message(groupKey, "", "", "我记得关于你的：喜欢草莓蛋糕，养了一只叫团子的猫", at(0).Add(10*time.Second)),
			message(groupKey, "20001", "小明", "/忘记我", at(1)),
			message(groupKey, "", "", "好，我把关于你的都忘了。", at(1).Add(5*time.Second)),
		)
		if err := st.ForgetMember(ctx, groupKey, "20001", at(2)); err != nil {
			t.Fatal(err)
		}
		deps := RecallDeps{Store: st, Policies: newPolicies(st, true, true), Reviewer: review.New(nil, review.Options{})}
		if name == "command recognizer" {
			deps.IsCommand = isCommand
		}
		inv := invocation(groupKey, "20002", "小红")
		for _, q := range []string{`{"query":"团子"}`, `{"query":"草莓蛋糕"}`, `{"query":"忘了"}`} {
			if got := call(t, NewRecall(deps), inv, q); got != recallNotFound {
				t.Errorf("%s: recall%s found a reply to a command:\n%s", name, q, got)
			}
		}
	}
}

func TestRecall_KeepsOrdinaryBotReplies(t *testing.T) {
	st := newStore(t)
	addMessages(t, st,
		message(groupKey, "20001", "小明", "我喜欢草莓蛋糕", at(0)),
		message(groupKey, "", "", "草莓蛋糕很好吃", at(0).Add(10*time.Second)),
		message(groupKey, "20003", "", "/记忆", at(10)),
		message(groupKey, "20003", "", "/好感度", at(10).Add(time.Second)),
		message(groupKey, "", "", "我记得关于你的：喜欢草莓蛋糕和秘密一", at(10).Add(5*time.Second)),
		message(groupKey, "", "", "好感：熟悉（52/100）草莓蛋糕和秘密二", at(10).Add(6*time.Second)),
		message(groupKey, "", "", "草莓蛋糕要配茶", at(11)),
		message(groupKey, "20003", "", "/好感度", at(20)),
		message(groupKey, "", "", "说起草莓蛋糕，我也想吃", at(23)),
	)
	got := call(t, newRecall(st, true), invocation(groupKey, "20002", "小红"), `{"query":"草莓蛋糕"}`)
	for _, kept := range []string{"小明：我喜欢草莓蛋糕", "你：草莓蛋糕很好吃", "你：草莓蛋糕要配茶", "你：说起草莓蛋糕，我也想吃"} {
		if !strings.Contains(got, kept) {
			t.Errorf("recall lacks the ordinary reply %q:\n%s", kept, got)
		}
	}
	if strings.Contains(got, "秘密") || strings.Contains(got, "好感") {
		t.Errorf("recall shows a reply to a command:\n%s", got)
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
	want := day1113 + "\n[11-13 09:02] 小红：我［联系方式］\n[11-13 09:03] 小红：草莓店的［联系方式］\n[11-13 09:05] 你：草莓很甜"
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
	if want := day1113 + "\n[11-13 09:00] 小明：闲聊甲\n[11-13 09:02] 小明：草莓真好\n[11-13 09:04] 小明：闲聊乙"; got != want {
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
	if want := day1113 + "\n[11-13 09:06] 小明：今天吃了草莓\n[11-13 09:07] 你：真好"; got != want {
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
		day1113,
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
		{`{"query":"草莓","after":"2026/03/01"}`, recallBadDate},
		{`{"query":"草莓","after":"2026-02-30"}`, recallBadDate},
		{`{"query":"草莓","before":20260301}`, recallBadDate},
		{`{"query":"草莓","before":"2026-3-1"}`, recallBadDate},
		{`{"query":"草莓","after":"2026-03-05","before":"2026-03-01"}`, "after 不能晚于 before。"},
	}
	tool := newRecall(st, true)
	for _, c := range append(argCases,
		struct{ args, want string }{`{"query":"草莓"}`, recallNotFound},
		struct{ args, want string }{`{"query":"天气","after":"","before":null}`, day1113 + "\n[11-13 09:00] 小明：今天天气不错"},
		struct{ args, want string }{`{"query":"天气","after":"2026-11-13","before":"2026-11-13"}`, day1113 + "\n[11-13 09:00] 小明：今天天气不错"},
		struct{ args, want string }{`{"query":"天气","after":"2026-11-14"}`, recallNotFound},
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
