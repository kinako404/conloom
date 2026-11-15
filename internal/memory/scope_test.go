package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func panelChat(botID, id string) store.ConversationRecord {
	return store.ConversationRecord{BotInstanceID: botID, ID: id, Type: conloomapi.ConvPrivate, Platform: store.PlatformPanel,
		PlatformConversationID: id, Key: store.ConversationKey(botID, store.PlatformPanel, conloomapi.ConvPrivate, id)}
}

func platformConv(botID, platform, convType, id string) store.ConversationRecord {
	return store.ConversationRecord{BotInstanceID: botID, ID: id, Type: convType, Platform: platform,
		PlatformConversationID: id, Key: store.ConversationKey(botID, platform, convType, id)}
}

// seedChat stores conv with a message of user saying text, and returns the
// message's seq.
func seedChat(t *testing.T, st store.Store, conv store.ConversationRecord, user, text string, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id := fmt.Sprintf("msg_%s_%d", strings.ReplaceAll(conv.Key, ":", "_"), at.UnixNano())
	m := store.MessageRecord{ID: id, BotInstanceID: conv.BotInstanceID, ConversationKey: conv.Key, Platform: conv.Platform,
		PlatformMessageID: "p" + id, ActorID: "user_" + user, PlatformUserID: user, DisplayName: "小明", Text: text, OccurredAt: at}
	if user == "" {
		m.ActorID, m.PlatformUserID, m.DisplayName, m.FromBot = "bot_30001", "", "织语", true
	}
	if _, err := st.Ingest(ctx, store.Ingest{
		Event:        store.EventRecord{BotInstanceID: conv.BotInstanceID, IdempotencyKey: "k_" + id, RequestHash: "h", EventID: "evt_" + id},
		Conversation: &conv, Message: &m,
	}); err != nil {
		t.Fatalf("Ingest %s: %v", id, err)
	}
	got, err := st.GetMessage(ctx, conv.BotInstanceID, id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Seq
}

func TestScopeKey(t *testing.T) {
	a, b, other := panelChat("qq-main", "panel_a"), panelChat("qq-main", "panel_b"), panelChat("tg-main", "panel_c")
	const shared = "qq-main:panel:private:~shared"
	if got := ScopeKey(a); got != shared {
		t.Fatalf("ScopeKey(panel chat) = %q, want %q", got, shared)
	}
	if ScopeKey(b) != shared || ScopeKeyOf(a.Key) != shared || ScopeKeyOf(b.Key) != shared || ScopeKeyOf(shared) != shared {
		t.Errorf("panel chats of one bot do not share %q: %q %q %q", shared, ScopeKey(b), ScopeKeyOf(a.Key), ScopeKeyOf(shared))
	}
	if got := ScopeKey(other); got != "tg-main:panel:private:~shared" {
		t.Errorf("another bot's panel chat = %q", got)
	}
	if !IsSharedScope(shared) || IsSharedScope(a.Key) || IsSharedScope(platformConv("qq-main", "qq", conloomapi.ConvPrivate, PanelScopeID).Key) {
		t.Error("IsSharedScope does not tell the shared scope from conversations")
	}
	parts := strings.SplitN(shared, ":", 4)
	if len(parts) != 4 || parts[0] != "qq-main" || parts[1] != store.PlatformPanel || parts[2] != conloomapi.ConvPrivate ||
		convTypeOf(shared) != conloomapi.ConvPrivate || consBotID(shared) != "qq-main" {
		t.Errorf("scope key %q does not parse as a private panel key of qq-main", shared)
	}
	if strings.HasPrefix(PanelScopeID, "panel_") {
		t.Errorf("PanelScopeID %q may collide with a panel chat ID", PanelScopeID)
	}

	st := newTestStore(t)
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	for i, c := range []store.ConversationRecord{b, a, other, platformConv("qq-main", "qq", conloomapi.ConvPrivate, "private_20001"),
		platformConv("qq-main", "qq", conloomapi.ConvGroup, "conv_10001")} {
		seedChat(t, st, c, "20001", "你好", at.Add(time.Duration(i)*time.Minute))
	}
	chats, err := ScopeChats(context.Background(), st, shared)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chats, []string{a.Key, b.Key}) {
		t.Errorf("ScopeChats(%s) = %v, want both panel chats of qq-main in key order", shared, chats)
	}
}

func TestScope_PlatformUnchanged(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, c := range []store.ConversationRecord{
		platformConv("qq-main", "qq", conloomapi.ConvPrivate, "private_20001"),
		platformConv("tg-main", "telegram", conloomapi.ConvPrivate, "private_29999"),
		platformConv("qq-main", "qq", conloomapi.ConvGroup, "conv_10001"),
		platformConv("tg-main", "telegram", conloomapi.ConvGroup, "conv_10002"),
	} {
		if got := ScopeKey(c); got != c.Key {
			t.Errorf("ScopeKey(%s) = %q, want the conversation's own key", c.Key, got)
		}
		if got := ScopeKeyOf(c.Key); got != c.Key || IsSharedScope(c.Key) {
			t.Errorf("ScopeKeyOf(%s) = %q, shared %v", c.Key, got, IsSharedScope(c.Key))
		}
		chats, err := ScopeChats(ctx, st, c.Key)
		if err != nil || !slices.Equal(chats, []string{c.Key}) {
			t.Errorf("ScopeChats(%s) = %v, %v, want the conversation alone", c.Key, chats, err)
		}
	}

	private, panel := platformConv("qq-main", "qq", conloomapi.ConvPrivate, "private_20001"), panelChat("qq-main", "panel_a")
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	seedChat(t, st, private, "20001", "你好", at)
	seedChat(t, st, panel, "20001", "你好", at.Add(time.Minute))
	if _, err := UpdateMember(ctx, st, private.Key, "20001", store.RevisionCommand, func(m *store.MemberMemory) (string, bool) {
		m.Note = "喜欢猫"
		return "", true
	}); err != nil {
		t.Fatal(err)
	}
	if m := getMember(t, st, private.Key, "20001"); m.Note != "喜欢猫" || m.ConversationKey != private.Key {
		t.Errorf("platform private member = %+v, want the note under its own key", m)
	}
	if rows, err := st.ListMemberMemories(ctx, ScopeKey(panel)); err != nil || len(rows) != 0 {
		t.Errorf("panel scope rows = %+v, %v, want none from the platform chat", rows, err)
	}
	if err := Forget(ctx, st, private.Key, "20001", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := getMember(t, st, private.Key, "20001"); m.Note != "" || m.ForgottenAt.IsZero() {
		t.Errorf("forgotten platform member = %+v", m)
	}
	g, err := st.GetGroupMemory(ctx, private.Key)
	if err != nil || g.CursorSeq == 0 {
		t.Fatalf("platform memory row after forget = %+v, %v", g, err)
	}
	if _, err := st.GetGroupMemory(ctx, ScopeKey(panel)); err == nil {
		t.Error("forgetting in a platform chat wrote the panel scope's memory row")
	}
}

func TestConsolidate_ScopeOncePerRun(t *testing.T) {
	e := newSchedEnv(t)
	ctx := context.Background()
	a, b := panelChat("bot_test", "panel_a"), panelChat("bot_test", "panel_b")
	scopeKey := ScopeKey(a)
	at := time.Now().Add(-time.Hour)
	for i := range 30 {
		seedChat(t, e.st, []store.ConversationRecord{a, b}[i%2], "panel_admin", "你好", at.Add(time.Duration(i)*time.Second))
	}
	e.seed(t, 25, testConvKey)
	for _, key := range []string{a.Key, b.Key, testConvKey} {
		e.setMemory(t, key, true, 1000)
	}

	e.sched.tick(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: testConvKey, pending: 25}, schedCall{convKey: scopeKey, pending: 30})
	if got := e.cursor(t, scopeKey); got != 0 {
		t.Errorf("scope cursor = %d, want 0 with the whole backlog left", got)
	}
	for _, key := range []string{a.Key, b.Key} {
		if _, err := e.st.GetGroupMemory(ctx, key); err == nil {
			t.Errorf("chat %s got a memory row of its own", key)
		}
	}

	e.sched.Trigger(a.Key)
	e.sched.Trigger(b.Key)
	e.sched.Trigger(scopeKey)
	e.sched.runTriggered(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: scopeKey, manual: true, pending: 30})

	// The newest chat's policy decides: memory off in B, where the newest
	// message is, stops the scope; a newer message in A, where it is on,
	// starts it again.
	e.setMemory(t, b.Key, false, 1000)
	e.sched.Trigger(a.Key)
	e.sched.runTriggered(ctx)
	wantCalls(t, e.runner.takeCalls())
	seedChat(t, e.st, a, "panel_admin", "还在吗", at.Add(time.Minute))
	e.sched.Trigger(b.Key)
	e.sched.runTriggered(ctx)
	wantCalls(t, e.runner.takeCalls(), schedCall{convKey: scopeKey, manual: true, pending: 31})
}

func TestConsolidate_PanelScopeMaterial(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	ctx := context.Background()
	a, b := panelChat("bot_test", "panel_a"), panelChat("bot_test", "panel_b")
	scopeKey := ScopeKey(a)
	seedChat(t, e.st, a, "panel_admin", "我在学钢琴", consAt(0))
	seedChat(t, e.st, b, "panel_admin", "我养了一只猫", consAt(1))
	seedChat(t, e.st, a, "", "加油", consAt(2))
	last := seedChat(t, e.st, b, "panel_admin", "它叫团子", consAt(3))

	pv, err := e.c.d.Policies.Current(ctx, b.Key)
	if err != nil {
		t.Fatal(err)
	}
	p := pv.Policy
	p.Memory.SummaryMaxChars = 2000
	if _, err := e.c.d.Policies.Update(ctx, b.Key, conloomapi.PolicyPut{Policy: p, MemoryPresent: true}, "test"); err != nil {
		t.Fatal(err)
	}
	rules := "只记爱好。"
	doc := conloomapi.DefaultPersona()
	doc.Adult, doc.MemoryRules = true, rules
	if _, err := e.c.d.Personas.Update(ctx, b.Key, "default", conloomapi.PersonaPut{Persona: &doc}, "test"); err != nil {
		t.Fatal(err)
	}

	e.long.answer(`{"group_summary":"学钢琴，养了猫团子","members":[{"ref":"P1","note":"学钢琴，养猫"}]}`)
	if _, err := e.c.Run(ctx, a.Key, false); err != nil {
		t.Fatal(err)
	}
	if n := e.long.calls(); n != 1 {
		t.Fatalf("model calls = %d, want one for both chats", n)
	}
	req := e.long.request(t, 0)
	lines := consBatchLines(t, req)
	want := []struct{ label, text string }{{"〔对话1〕", "我在学钢琴"}, {"〔对话2〕", "我养了一只猫"}, {"〔对话1〕", "加油"}, {"〔对话2〕", "它叫团子"}}
	if len(lines) != len(want) {
		t.Fatalf("batch lines = %q, want %d", lines, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w.label) || !strings.Contains(lines[i], w.text) {
			t.Errorf("line %d = %q, want %s and %q", i, lines[i], w.label, w.text)
		}
	}
	system := req.Messages[0].Content
	if !strings.Contains(system, consSharedNote) || !strings.Contains(system, "\n6. "+rules+"\n") || !strings.Contains(system, "不超过 2000 字") {
		t.Errorf("system prompt does not follow B, the newest chat, or lacks the note on several chats:\n%s", system)
	}
	text := consText(req)
	consAssertNoIDs(t, text)
	if strings.Contains(text, "panel_") || strings.Contains(text, PanelScopeID) {
		t.Errorf("consolidation input shows a chat ID:\n%s", text)
	}

	g, err := e.st.GetGroupMemory(ctx, scopeKey)
	if err != nil || g.Summary != "学钢琴，养了猫团子" || g.CursorSeq != last {
		t.Fatalf("scope memory = %+v, %v, want the summary with the cursor at %d", g, err, last)
	}
	if m := getMember(t, e.st, scopeKey, "panel_admin"); m.Note != "学钢琴，养猫" {
		t.Errorf("scope member = %+v", m)
	}
	for _, key := range []string{a.Key, b.Key} {
		if _, err := e.st.GetGroupMemory(ctx, key); err == nil {
			t.Errorf("chat %s got a memory row of its own", key)
		}
	}

	seedChat(t, e.st, a, "panel_admin", "今天弹了一首曲子", consAt(4))
	e.long.answer(`{"group_summary":"学钢琴，养了猫团子","members":[]}`)
	if _, err := e.c.Run(ctx, scopeKey, false); err != nil {
		t.Fatal(err)
	}
	system = e.long.request(t, 1).Messages[0].Content
	if !strings.Contains(system, privateProtectedRule6) || strings.Contains(system, rules) || !strings.Contains(system, "不超过 3000 字") {
		t.Errorf("run after A became the newest chat does not follow A:\n%s", system)
	}
	if lines := consBatchLines(t, e.long.request(t, 1)); len(lines) != 1 || !strings.HasPrefix(lines[0], "〔对话1〕") {
		t.Errorf("second batch = %q, want only A's new message", lines)
	}
}

func TestConsolidate_PanelScopeOfOneChatIsUnlabelled(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	a := panelChat("bot_test", "panel_a")
	seedChat(t, e.st, a, "panel_admin", "我在学钢琴", consAt(0))
	seedChat(t, e.st, a, "", "加油", consAt(1))
	e.long.answer(`{"group_summary":"学钢琴","members":[]}`)
	if _, err := e.c.Run(context.Background(), a.Key, false); err != nil {
		t.Fatal(err)
	}
	req := e.long.request(t, 0)
	if system := req.Messages[0].Content; system != consStyleFor(testPrivateKey).system {
		t.Errorf("a scope of one chat is not consolidated like a private chat:\n%s", system)
	}
	for _, line := range consBatchLines(t, req) {
		if strings.HasPrefix(line, "〔") {
			t.Errorf("line %q is labelled with its chat", line)
		}
	}
	if g, err := e.st.GetGroupMemory(context.Background(), ScopeKey(a)); err != nil || g.Summary != "学钢琴" {
		t.Errorf("scope memory = %+v, %v", g, err)
	}
}
