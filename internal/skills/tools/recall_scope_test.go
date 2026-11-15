package tools

import (
	"context"
	"testing"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// otherDay1113 is the header of groups from 2026-11-13 in another chat of
// the memory scope.
const otherDay1113 = "—— 2026-11-13（另一段聊天）——"

var (
	panelA = store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_a")
	panelB = store.ConversationKey("bot_test", store.PlatformPanel, conloomapi.ConvPrivate, "panel_b")
)

// seedPanelChats stores panel chats A and B of bot_test, as created in the
// panel.
func seedPanelChats(t *testing.T, st store.Store) {
	t.Helper()
	for _, key := range []string{panelA, panelB} {
		id := key[len("bot_test:panel:private:"):]
		if _, err := st.Ingest(context.Background(), store.Ingest{
			Event: store.EventRecord{BotInstanceID: "bot_test", IdempotencyKey: "chat_" + id, RequestHash: "h", EventID: "evt_chat_" + id,
				Response: []byte("{}"), RawEvent: []byte("{}")},
			Conversation: &store.ConversationRecord{BotInstanceID: "bot_test", ID: id, Key: key, Type: conloomapi.ConvPrivate,
				Platform: store.PlatformPanel, PlatformConversationID: id},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecall_PanelScopeShowsStretchesOfOtherChats(t *testing.T) {
	st := newStore(t)
	seedPanelChats(t, st)
	a, b := panelA, panelB
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
	want := otherDay1113 + "\n[11-13 09:01] 小明：周末打算出门逛逛\n[11-13 09:03] 小明：公园吧\n\n" +
		day1113 + "\n[11-13 09:02] 小明：今天好热\n[11-13 09:04] 小明：草莓好吃"
	if got != want {
		t.Fatalf("recall in B =\n%s\nwant A's stretch from A's own messages and B's hit once\n%s", got, want)
	}
	if len(vec.calls) != 1 || vec.calls[0].convKey != b {
		t.Errorf("vector calls = %+v, want one for B", vec.calls)
	}
}

func TestRecall_PanelScopeKeywordsAcrossChats(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedPanelChats(t, st)
	a, b := panelA, panelB
	addMessages(t, st,
		message(a, "panel_admin", "小明", "我下周三考试", at(1)),
		message(a, "panel_admin", "小明", "复习好累", at(2)),
		message(b, "panel_admin", "小明", "今天天气不错", at(3)),
		message(privateKey, "20001", "小明", "考试周到了", at(4)),
	)
	tool := newRecall(st, true)
	inB := invocation(b, "panel_admin", "小明")
	want := otherDay1113 + "\n[11-13 09:01] 小明：我下周三考试\n[11-13 09:02] 小明：复习好累"
	if got := call(t, tool, inB, `{"query":"考试"}`); got != want {
		t.Fatalf("keyword recall in B without semantic search =\n%s\nwant A's message under the other chat's header\n%s", got, want)
	}
	if got, want := call(t, tool, invocation(privateKey, "20001", "小明"), `{"query":"考试"}`),
		day1113+"\n[11-13 09:04] 小明：考试周到了"; got != want {
		t.Errorf("keyword recall in the platform private chat =\n%s\nwant only its own message\n%s", got, want)
	}

	if err := st.ForgetPrivateScope(ctx, memory.ScopeKeyOf(b), []string{a, b}, "panel_admin", at(5)); err != nil {
		t.Fatal(err)
	}
	addMessages(t, st,
		message(a, "panel_admin", "小明", "考试结束了", at(6)),
		message(b, "panel_admin", "小明", "考试加油", at(7)),
	)
	want = otherDay1113 + "\n[11-13 09:06] 小明：考试结束了\n\n" + day1113 + "\n[11-13 09:07] 小明：考试加油"
	if got := call(t, tool, inB, `{"query":"考试"}`); got != want {
		t.Fatalf("keyword recall in B after forgetting at 09:05 =\n%s\nwant only what both chats said since\n%s", got, want)
	}
	if got := call(t, tool, inB, `{"query":"复习"}`); got != recallNotFound {
		t.Errorf("recall of A's message from before the forget = %q, want nothing", got)
	}
}
