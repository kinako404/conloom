package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
)

func newRemember(st store.Store, memoryOn bool) *rememberTool {
	return NewRemember(RememberDeps{Store: st, Policies: newPolicies(st, memoryOn, true),
		Reviewer: review.New(nil, review.Options{})}).(*rememberTool)
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
		if got := call(t, tool, invocation(key, "20001", "小明"), `{"content":" 我喜欢草莓 ","user_id":"20002"}`); got != "已记住。" {
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
		if err != nil || len(revs) != 1 || revs[0].Source != store.RevisionCommand || revs[0].Reason != memory.ReasonRemember {
			t.Fatalf("%s: revisions = %+v, %v", key, revs, err)
		}
	}
}

func TestRemember_RefusalsAndArguments(t *testing.T) {
	st := newStore(t)
	tool := newRemember(st, true)
	inv := invocation(groupKey, "20001", "小明")
	for _, c := range []struct{ args, want string }{
		{`{}`, "需要提供要记住的内容（content）。"},
		{`{"content":5}`, "需要提供要记住的内容（content）。"},
		{`{"content":null}`, "需要提供要记住的内容（content）。"},
		{`not json`, "需要提供要记住的内容（content）。"},
		{``, "需要提供要记住的内容（content）。"},
		{`{"content":"   "}`, "内容为空，没有记下。"},
		{`{"content":"` + strings.Repeat("长", memory.RememberMaxRunes+1) + `"}`, "内容超过 100 字，没有记下。"},
		{`{"content":"我的手机号是 13800001111"}`, "联系方式、证件号这类信息不能记，没有记下。"},
	} {
		if got := call(t, tool, inv, c.args); got != c.want {
			t.Errorf("remember(%.40s) = %q, want %q", c.args, got, c.want)
		}
	}
	if m, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatalf("refused calls wrote %+v", m)
	}

	off := newRemember(st, false)
	if got := call(t, off, inv, `{"content":"我喜欢草莓"}`); got != "这个会话没有开启记忆，没有记下。" {
		t.Errorf("remember with memory off = %q", got)
	}
	if _, ok := noteOf(t, st, groupKey, "20001"); ok {
		t.Fatal("memory off still wrote")
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
	if got := call(t, tool, invocation(groupKey, "20001", "小明"), `{"content":"他是个坏词人"}`); got != "这句话不能记，没有记下。" {
		t.Errorf("remember of a blocked word = %q", got)
	}
}
