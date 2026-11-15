package tools

import (
	"testing"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestRecall_PanelScopeShowsStretchesOfOtherChats(t *testing.T) {
	st := newStore(t)
	a := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	b := store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
	msgs := []store.MessageRecord{
		message(a, "panel_admin", "小明", "周末打算出门逛逛", at(1)),
		message(b, "panel_admin", "小明", "今天好热", at(2)),
		message(a, "panel_admin", "小明", "公园吧", at(3)),
		message(b, "panel_admin", "小明", "草莓好吃", at(4)),
	}
	addMessages(t, st, msgs...)
	vec := &fakeVectors{hits: []search.Hit{
		{ConversationKey: a, FromSeq: seq(t, st, msgs[0]), ToSeq: seq(t, st, msgs[2]), Score: 0.82},
		{ConversationKey: b, FromSeq: seq(t, st, msgs[3]), ToSeq: seq(t, st, msgs[3]), Score: 0.7},
	}}
	got := call(t, newRecallWith(st, vec), invocation(b, "panel_admin", "小明"), `{"query":"出去玩","keywords":["草莓"]}`)
	want := day1113 + "\n[11-13 09:01] 小明：周末打算出门逛逛\n[11-13 09:03] 小明：公园吧\n\n" +
		day1113 + "\n[11-13 09:02] 小明：今天好热\n[11-13 09:04] 小明：草莓好吃"
	if got != want {
		t.Fatalf("recall in B =\n%s\nwant A's stretch from A's own messages and B's hit once\n%s", got, want)
	}
	if len(vec.calls) != 1 || vec.calls[0].convKey != b {
		t.Errorf("vector calls = %+v, want one for B", vec.calls)
	}
}
