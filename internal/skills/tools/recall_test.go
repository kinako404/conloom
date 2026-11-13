package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/store"
)

// at is 09:00 plus minutes on 2026-11-13 in Asia/Shanghai.
func at(minutes int) time.Time {
	return time.Date(2026, 11, 13, 1, minutes, 0, 0, time.UTC)
}

func newRecall(st store.Store, memoryOn bool) *recallTool {
	return NewRecall(RecallDeps{Store: st, Policies: newPolicies(st, memoryOn, true)}).(*recallTool)
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

func TestRecall_Spec(t *testing.T) {
	checkSpec(t, newRecall(newStore(t), true).Spec(), "recall", "query")
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
	msgs = append(msgs, message(groupKey, "", "", "好的", at(30)), message(groupKey, "20001", "小明", "我以前说过喜欢草莓吗", at(31)))
	addMessages(t, st, msgs...)
	addMessages(t, st, message(otherKey, "20001", "小明", "我喜欢草莓", at(1)))

	got := call(t, newRecall(st, true), invocation(groupKey, "20001", "小明"), `{"query":"喜欢草莓"}`)
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
	addMessages(t, st,
		message(groupKey, "20001", "小明", "草莓一号", at(0)),
		message(groupKey, "20002", "小红", "草莓二号", at(1)),
		message(groupKey, "20001", "小明", "草莓三号", at(2)),
		message(groupKey, "20001", "小明", "草莓四号", at(3)),
	)
	tool := newRecall(st, true)
	inv := invocation(groupKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{"query":"草莓"}`), "[11-13 09:00] 小明：草莓一号\n[11-13 09:01] 小红：草莓二号"; got != want {
		t.Fatalf("recall before the bot spoke =\n%s\nwant\n%s", got, want)
	}

	addMessages(t, st,
		message(groupKey, "", "", "草莓五号", at(4)),
		message(groupKey, "20001", "小明", "草莓六号", at(5)),
		message(groupKey, "20003", "小刚", "草莓七号", at(6)),
		message(groupKey, "20001", "小明", "草莓八号", at(7)),
	)
	got := call(t, tool, inv, `{"query":"草莓"}`)
	if strings.Contains(got, "六号") || strings.Contains(got, "八号") || !strings.Contains(got, "七号") || !strings.Contains(got, "四号") {
		t.Fatalf("recall skipped the wrong messages of the current turn:\n%s", got)
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
	addMessages(t, st,
		message(privateKey, "20001", "小明", "今天吃了草莓", at(6)),
		message(privateKey, "", "", "真好", at(7)),
		message(privateKey, "20001", "小明", "我说过喜欢草莓吗", at(8)),
	)
	got := call(t, newRecall(st, true), invocation(privateKey, "20001", "小明"), `{"query":"草莓"}`)
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
	)
	got := call(t, newRecall(st, true), invocation(groupKey, "20001", "小明"), `{"query":"草莓"}`)
	want := "[11-13 09:00] 一位群友：草莓一号\n[11-13 09:01] 一位群友：草莓二号\n[11-13 09:02] 小红：草莓 三号\n[11-13 09:03] 你：草莓四号"
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

func TestRecall_LooksAtTheNewest5000(t *testing.T) {
	st := newStore(t)
	msgs := []store.MessageRecord{message(groupKey, "20001", "小明", "很久以前说过草莓", at(0))}
	for i := range 5000 {
		msgs = append(msgs, message(groupKey, "20001", "小明", "闲聊", at(1).Add(time.Duration(i)*time.Second)))
	}
	addMessages(t, st, msgs...)
	if got := call(t, newRecall(st, true), invocation(groupKey, "20002", "小红"), `{"query":"草莓"}`); got != "没有找到相关的聊天记录。" {
		t.Fatalf("recall beyond the newest 5000 = %q", got)
	}
}

func TestRecall_NotFoundDisabledAndArguments(t *testing.T) {
	st := newStore(t)
	addMessages(t, st, message(groupKey, "20001", "小明", "今天天气不错", at(0)))
	tool := newRecall(st, true)
	inv := invocation(groupKey, "20002", "小红")
	for _, c := range []struct{ args, want string }{
		{`{"query":"草莓"}`, "没有找到相关的聊天记录。"},
		{`{"query":"？！"}`, "没有找到相关的聊天记录。"},
		{`{}`, "需要提供要查找的内容（query）。"},
		{`{"query":["草莓"]}`, "需要提供要查找的内容（query）。"},
		{`oops`, "需要提供要查找的内容（query）。"},
		{`{"query":"  "}`, "要查找的内容为空。"},
		{`{"query":"` + strings.Repeat("长", 101) + `"}`, "要查找的内容超过 100 字。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("recall(%.30s) = %q, want %q", c.args, got, c.want)
		}
	}
	if got := call(t, newRecall(st, false), inv, `{"query":"天气"}`); got != "这个会话没有开启记忆。" {
		t.Errorf("recall with memory off = %q", got)
	}
}
