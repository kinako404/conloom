package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
)

const notAsked = "只有对方自己明确让你记住时才能记。"

func newRemember(st store.Store, memoryOn bool) *rememberTool {
	return NewRemember(RememberDeps{Store: st, Policies: newPolicies(st, memoryOn, true),
		Reviewer: review.New(nil, review.Options{})}).(*rememberTool)
}

// asked stores text as the user's message in convKey and returns an
// invocation answering it.
func asked(t *testing.T, st store.Store, convKey, user, name, text string) skills.Invocation {
	t.Helper()
	m := message(convKey, user, name, text, testNow)
	addMessages(t, st, m)
	inv := invocation(convKey, user, name)
	inv.TriggerMessageIDs = []string{m.ID}
	return inv
}

func noteOf(t *testing.T, st store.Store, convKey, user string) (store.MemberMemory, bool) {
	t.Helper()
	rows, err := st.GetMemberMemories(context.Background(), convKey, []string{user})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := rows[user]
	return m, ok
}

func TestRemember_Spec(t *testing.T) {
	checkSpec(t, newRemember(newStore(t), true).Spec(), "remember", "content")
}

func TestRemember_WritesLikeTheCommand(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{groupKey, privateKey} {
		st := newStore(t)
		tool := newRemember(st, true)
		inv := asked(t, st, key, "20001", "小明", "帮我记住：我喜欢草莓")
		if got := call(t, tool, inv, `{"content":" 我喜欢草莓 ","user_id":"20002"}`); got != "已记住。" {
			t.Fatalf("%s: remember = %q", key, got)
		}
		m, ok := noteOf(t, st, key, "20001")
		if !ok || m.Note != memory.RememberMarker+"我喜欢草莓" || m.DisplayName != "小明" {
			t.Fatalf("%s: member = %+v", key, m)
		}
		if _, ok := noteOf(t, st, key, "20002"); ok {
			t.Fatalf("%s: an argument chose another member", key)
		}
		revs, err := st.ListMemoryRevisions(ctx, key, "20001")
		if err != nil || len(revs) != 1 || revs[0].Source != store.RevisionCommand || revs[0].Reason != memory.ReasonRememberTool {
			t.Fatalf("%s: revisions = %+v, %v", key, revs, err)
		}
	}
}

func TestRemember_RefusalsAndArguments(t *testing.T) {
	st := newStore(t)
	tool := newRemember(st, true)
	inv := asked(t, st, groupKey, "20001", "小明", "记住这个")
	tooLong := `{"content":"` + strings.Repeat("长", memory.RememberMaxRunes+1) + `"}`
	argCases := []struct{ args, want string }{
		{`{}`, "需要提供要记住的内容（content）。"},
		{`{"content":5}`, "需要提供要记住的内容（content）。"},
		{`{"content":null}`, "需要提供要记住的内容（content）。"},
		{`not json`, "需要提供要记住的内容（content）。"},
		{``, "需要提供要记住的内容（content）。"},
		{`{"content":"   "}`, "内容为空，没有记下。"},
		{tooLong, "内容超过 100 字，没有记下。"},
	}
	for _, c := range append(argCases, struct{ args, want string }{
		`{"content":"我的手机号是 13800001111"}`, "联系方式、证件号这类信息不能记，没有记下。"}) {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("remember(%.40s) = %q, want %q", c.args, got, c.want)
		}
	}
	if m, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatalf("refused calls wrote %+v", m)
	}

	off := newRemember(st, false)
	for _, c := range argCases {
		if got := call(t, off, inv, c.args); got != c.want {
			t.Errorf("remember(%.40s) with memory off = %q, want the argument checked first: %q", c.args, got, c.want)
		}
	}
	if got := call(t, off, inv, `{"content":"我喜欢草莓"}`); got != "这个会话没有开启记忆，没有记下。" {
		t.Errorf("remember with memory off = %q", got)
	}
	if _, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatal("memory off still wrote")
	}
}

func TestRemember_OnlyWhenTheSpeakerAsked(t *testing.T) {
	st := newStore(t)
	tool := newRemember(st, true)
	args := `{"content":"我喜欢香菜"}`

	other := message(groupKey, "20002", "小红", "记住小明喜欢香菜", testNow)
	own := message(groupKey, "20001", "小明", "哈哈", testNow)
	elsewhere := message(otherKey, "20001", "小明", "记住我喜欢香菜", testNow)
	addMessages(t, st, other, own, elsewhere)
	for _, c := range []struct {
		name     string
		triggers []string
	}{
		{"no triggers", nil},
		{"own message without asking", []string{own.ID}},
		{"another member asked", []string{other.ID, own.ID}},
		{"asked in another conversation", []string{elsewhere.ID}},
		{"unknown message", []string{"msg_missing"}},
	} {
		inv := invocation(groupKey, "20001", "小明")
		inv.TriggerMessageIDs = c.triggers
		if got := call(t, tool, inv, args); got != notAsked {
			t.Errorf("%s: remember = %q, want %q", c.name, got, notAsked)
		}
	}
	if m, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatalf("remember without being asked wrote %+v", m)
	}

	for _, text := range []string{"记一下，我下周考试", "别忘了我喜欢猫", "帮我记个事", "Please REMEMBER this", "记住哦",
		"记住我喜欢草莓", "把这个记下来。", "你记得吗？算了，记住我喜欢猫",
		"帮我记一下好吗", "你能记住我生日是5月1日吗", "麻烦记住我叫小明可以吗"} {
		if got := call(t, tool, asked(t, st, groupKey, "20001", "小明", text), `{"content":"我喜欢猫"}`); got != "已记住。" {
			t.Errorf("remember after %q = %q", text, got)
		}
	}
}

func TestRemember_QuestionsDoNotAsk(t *testing.T) {
	st := newStore(t)
	tool := newRemember(st, true)
	for _, text := range []string{
		"你还记着我说的吗", "记住了吗", "你记得吗", "记住了吗？我喜欢猫", "记住了没", "你记下来了吗？",
		"别忘了吗", "Do you remember me?", "你记着啊", "我记下了",
		"记住没有", "你记不记得我喜欢什么", "记没记住我的生日", "记得过吗", "记住过吗？",
	} {
		if got := call(t, tool, asked(t, st, groupKey, "20001", "小明", text), `{"content":"我喜欢猫"}`); got != notAsked {
			t.Errorf("remember after %q = %q, want %q", text, got, notAsked)
		}
	}
	if m, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatalf("a question wrote %+v", m)
	}
}

func TestRemember_BlockedByReview(t *testing.T) {
	st := newStore(t)
	policies := newPolicies(st, true, true)
	pv, err := policies.Current(context.Background(), groupKey)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Review.BlockedWords, p.Review.Action = []string{"坏词"}, review.VerdictBlock
	if _, err := policies.Update(context.Background(), groupKey, putOf(p), "admin"); err != nil {
		t.Fatal(err)
	}
	tool := NewRemember(RememberDeps{Store: st, Policies: policies, Reviewer: review.New(nil, review.Options{})})
	inv := asked(t, st, groupKey, "20001", "小明", "记住他是个坏词人")
	if got := call(t, tool, inv, `{"content":"他是个坏词人"}`); got != "这句话不能记，没有记下。" {
		t.Errorf("remember of a blocked word = %q", got)
	}
}
