package tools

import (
	"context"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func TestNow(t *testing.T) {
	tool := NewNow()
	checkSpec(t, tool.Spec(), "now")
	inv := invocation(groupKey, "20001", "小明")
	if got, want := call(t, tool, inv, `{}`), "2026-11-13 星期五 16:30（Asia/Shanghai）"; got != want {
		t.Errorf("now = %q, want %q", got, want)
	}
	inv.Location = nil
	if got, want := call(t, tool, inv, ``), "2026-11-13 星期五 08:30（UTC）"; got != want {
		t.Errorf("now without a location = %q, want %q", got, want)
	}
	inv.Location = time.FixedZone("UTC+9", 9*3600)
	inv.Now = time.Date(2026, 11, 15, 15, 5, 0, 0, time.UTC)
	if got, want := call(t, tool, inv, `{"zone":"Europe/Paris"}`), "2026-11-16 星期一 00:05（UTC+9）"; got != want {
		t.Errorf("now = %q, want %q (arguments ignored)", got, want)
	}
	for _, args := range []string{`not json`, `[1]`, `null`} {
		if got := call(t, tool, invocation(groupKey, "20001", "小明"), args); got != "2026-11-13 星期五 16:30（Asia/Shanghai）" {
			t.Errorf("now(%s) = %q, want arguments ignored", args, got)
		}
	}
}

func TestNow_ZeroInvocationTimeUsesTheClock(t *testing.T) {
	inv := invocation(groupKey, "20001", "小明")
	inv.Now = time.Time{}
	got := call(t, NewNow(), inv, `{}`)
	if len(got) < len("2026-01-01") || got[:4] < "2026" {
		t.Errorf("now with a zero time = %q, want the current time", got)
	}
}

func TestGetAffinity(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	putMember(t, st, store.MemberMemory{ConversationKey: groupKey, PlatformUserID: "20001", Affinity: intPtr(52)})
	putMember(t, st, store.MemberMemory{ConversationKey: otherKey, PlatformUserID: "20002", Affinity: intPtr(90)})
	tool := NewGetAffinity(AffinityDeps{Store: st, Policies: newPolicies(st, true, true)})
	checkSpec(t, tool.Spec(), "get_affinity")
	for _, c := range []struct {
		user, args, want string
	}{
		{"20001", `{}`, "好感：熟悉（52/100）"},
		{"20001", `{"user_id":"20002"}`, "好感：熟悉（52/100）"},
		{"20001", `garbage`, "好感：熟悉（52/100）"},
		{"20002", `{}`, "你们还不太熟，还没有好感度。"},
	} {
		if got := call(t, tool, invocation(groupKey, c.user, "小明"), c.args); got != c.want {
			t.Errorf("get_affinity for %s (%s) = %q, want %q", c.user, c.args, got, c.want)
		}
	}
	off := NewGetAffinity(AffinityDeps{Store: st, Policies: newPolicies(st, false, true)})
	if got := call(t, off, invocation(groupKey, "20001", "小明"), `{}`); got != "这个会话没有开启记忆。" {
		t.Errorf("get_affinity with memory off = %q", got)
	}
	noAffinity := NewGetAffinity(AffinityDeps{Store: st, Policies: newPolicies(st, true, false)})
	if got := call(t, noAffinity, invocation(groupKey, "20001", "小明"), `{}`); got != "这个会话没有开启好感度。" {
		t.Errorf("get_affinity with affinity off = %q", got)
	}
	if _, err := tool.Call(ctx, invocation(groupKey, "20001", "小明"), nil); err != nil {
		t.Errorf("get_affinity with no arguments: %v", err)
	}
}
