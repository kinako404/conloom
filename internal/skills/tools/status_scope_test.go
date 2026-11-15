package tools

import (
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestSetStatusAndAdjustBuff_PanelChatsShareTheScope(t *testing.T) {
	st := newStore(t)
	a := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	scope := memory.ScopeKeyOf(a)
	status := newSetStatus(st, newPolicies(st, true, true), conloomapi.DefaultPersona())
	inA, inB := invocation(a, "20001", "小明"), invocation(b, "20001", "小明")

	call(t, status, inA, `{"activity":"洗澡","minutes":20}`)
	s, ok := statusOf(t, st, scope)
	if !ok || s.Activity != "洗澡" || s.Origin() != a {
		t.Fatalf("scope status = %+v (found %v), want 洗澡 set in A", s, ok)
	}
	for key, want := range map[string]int{a: 1, b: 0, scope: 0} {
		if n := len(timersOf(t, st, key)); n != want {
			t.Errorf("timers of %s = %d, want %d", key, n, want)
		}
	}
	for _, key := range []string{a, b} {
		if _, ok := statusOf(t, st, key); ok {
			t.Errorf("status stored under the chat %s", key)
		}
	}
	if got := call(t, status, inB, `{"minutes":0}`); got != statusEnded {
		t.Fatalf("set_status 0 in B = %q", got)
	}
	if _, ok := statusOf(t, st, scope); ok || len(timersOf(t, st, a)) != 0 {
		t.Errorf("after ending in B: status found %v, A's timers %d; want both gone", ok, len(timersOf(t, st, a)))
	}

	buff := newAdjustBuff(st, conloomapi.DefaultPersona())
	inB.TaskID = "task_b"
	call(t, buff, inA, `{"name":"心情","delta":5,"reason":"聊得开心"}`)
	call(t, buff, inB, `{"name":"心情","delta":3,"reason":"又聊了一会"}`)
	day := testNow.In(shanghai).Format(time.DateOnly)
	checkBuff(t, st, scope, "心情", 68, day, 8)
	if n := len(buffEvents(t, st, scope)); n != 2 {
		t.Errorf("scope bar changes = %d, want 2", n)
	}
	for _, key := range []string{a, b} {
		if _, ok := buffOf(t, st, key, "心情"); ok {
			t.Errorf("bar stored under the chat %s", key)
		}
	}
}
