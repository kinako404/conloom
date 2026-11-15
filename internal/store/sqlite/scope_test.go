package sqlite

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func panelChatConv(botID, id string) store.ConversationRecord {
	return store.ConversationRecord{BotInstanceID: botID, ID: id, Type: conloomapi.ConvPrivate, Platform: store.PlatformPanel,
		PlatformConversationID: id, Key: store.ConversationKey(botID, store.PlatformPanel, conloomapi.ConvPrivate, id)}
}

func TestMemoryScopeKey(t *testing.T) {
	a := panelChatConv(testBot, "panel_a")
	scope := store.MemoryScopeKey(a.Key)
	if scope != testBot+":panel:private:~shared" || store.MemoryScopeKey(scope) != scope || !store.IsSharedScope(scope) {
		t.Errorf("scope of %s = %q", a.Key, scope)
	}
	for _, key := range []string{testConvKey, testPrivateKey, a.Key} {
		if store.IsSharedScope(key) {
			t.Errorf("IsSharedScope(%s) = true", key)
		}
	}
	for _, key := range []string{testConvKey, testPrivateKey, store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvGroup, "panel_g")} {
		if got := store.MemoryScopeKey(key); got != key {
			t.Errorf("MemoryScopeKey(%s) = %q, want the key itself", key, got)
		}
	}
}

func TestScopeQueries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b, other := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b"), panelChatConv("bot_other", "panel_c")
	seedConversations(t, db, b, a, other, store.ConversationRecord{BotInstanceID: testBot, ID: "conv_20001", Key: testPrivateKey,
		Type: conloomapi.ConvPrivate, Platform: testPlatform, PlatformConversationID: "20001"})
	if keys, err := db.PanelChatKeys(ctx, testBot); err != nil || !slices.Equal(keys, []string{a.Key, b.Key}) {
		t.Fatalf("PanelChatKeys = %v, %v; want the bot's two panel chats", keys, err)
	}
	if latest, err := db.LatestChat(ctx, []string{a.Key, b.Key}); err != nil || latest != "" {
		t.Fatalf("LatestChat without messages = %q, %v", latest, err)
	}
	var seqs []int64
	for _, key := range []string{a.Key, b.Key, testConvKey, a.Key, b.Key} {
		memInsertMessages(t, db, memMessage(key, "user_20001", "20001", false))
		if key != testConvKey {
			seqs = append(seqs, latestSeq(t, db, key))
		}
	}
	scope := []string{a.Key, b.Key}
	got, err := db.MessagesAfterIn(ctx, scope, seqs[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	var gotSeqs []int64
	for _, m := range got {
		gotSeqs = append(gotSeqs, m.Seq)
	}
	if !slices.Equal(gotSeqs, seqs[1:]) {
		t.Errorf("MessagesAfterIn = %v, want %v in seq order without the group's", gotSeqs, seqs[1:])
	}
	if got, _ := db.MessagesAfterIn(ctx, scope, 0, 2); len(got) != 2 || got[0].Seq != seqs[0] {
		t.Errorf("MessagesAfterIn limited to 2 = %+v", got)
	}
	if got, _ := db.MessagesAfterIn(ctx, nil, 0, 10); got != nil {
		t.Errorf("MessagesAfterIn of no chats = %+v", got)
	}
	if n, err := db.CountMessagesAfterIn(ctx, scope, seqs[1]); err != nil || n != 2 {
		t.Errorf("CountMessagesAfterIn = %d, %v; want 2", n, err)
	}
	if c, err := db.BacklogCursorIn(ctx, scope, 1); err != nil || c != seqs[2] {
		t.Errorf("BacklogCursorIn(1) = %d, %v; want %d", c, err, seqs[2])
	}
	if c, _ := db.BacklogCursorIn(ctx, scope, 10); c != 0 {
		t.Errorf("BacklogCursorIn(10) = %d, want 0", c)
	}
	for _, c := range []struct {
		keys []string
		want string
	}{{scope, b.Key}, {[]string{a.Key}, a.Key}, {[]string{other.Key}, ""}, {nil, ""}} {
		if latest, err := db.LatestChat(ctx, c.keys); err != nil || latest != c.want {
			t.Errorf("LatestChat(%v) = %q, %v; want %q", c.keys, latest, err, c.want)
		}
	}
}

func TestForgetPrivateScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	scope := store.MemoryScopeKey(a.Key)
	seedConversations(t, db, a, b)
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: scope,
		Group:   &store.GroupMemory{ConversationKey: scope, Summary: "最近在聊钢琴", CursorSeq: 1},
		Members: []store.MemberMemory{{ConversationKey: scope, PlatformUserID: "20001", Note: "喜欢猫", Affinity: intPtrSQLite(40)}},
		Revisions: []store.MemoryRevision{
			{ConversationKey: scope, Field: store.FieldSummary, NewValue: "最近在聊钢琴", Source: store.RevisionConsolidate},
			{ConversationKey: scope, PlatformUserID: "20001", Field: store.FieldNote, NewValue: "喜欢猫", Source: store.RevisionConsolidate},
		},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	for _, conv := range []store.ConversationRecord{a, b} {
		seqs := chunkMessages(t, db, conv.Key, "20001", "", "20001")
		writeChunks(t, db, store.ChunkWrite{ConversationKey: conv.Key, Model: "m1", Chunks: []store.MemoryChunk{
			testChunk(conv.Key, seqs[0], seqs[1], chunkAt(0), chunkAt(1), "m1"),
			testChunk(conv.Key, seqs[2], seqs[2], chunkAt(40), chunkAt(40), "m1"),
		}})
		until := now.Add(20 * time.Minute)
		if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: conv.Key, Activity: "洗澡", TaskID: "task_s" + conv.ID,
			StartedAt: now, UntilAt: until}, &store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until}); err != nil {
			t.Fatal(err)
		}
		seedBuffRows(t, db, conv.Key, "task_b"+conv.ID, now)
	}
	stamp, err := db.ForgetStamp(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.ForgetPrivateScope(ctx, scope, []string{a.Key, b.Key}, "20001", chunkAt(20)); err != nil {
		t.Fatalf("ForgetPrivateScope: %v", err)
	}
	m, err := db.GetMemberMemories(ctx, scope, []string{"20001"})
	if got := m["20001"]; err != nil || got.Note != "" || got.Affinity != nil || !got.ForgottenAt.Equal(chunkAt(20)) {
		t.Errorf("scope member = %+v, %v", got, err)
	}
	newest := max(latestSeq(t, db, a.Key), latestSeq(t, db, b.Key))
	if g, err := db.GetGroupMemory(ctx, scope); err != nil || g.Summary != "" || g.CursorSeq != newest {
		t.Errorf("scope memory = %+v, %v; want the summary cleared and the cursor at %d", g, err, newest)
	}
	for _, user := range []string{"", "20001"} {
		if revs, err := db.ListMemoryRevisions(ctx, scope, user); err != nil || len(revs) != 0 {
			t.Errorf("scope revisions of %q = %+v, %v", user, revs, err)
		}
	}
	for _, conv := range []store.ConversationRecord{a, b} {
		if got := chunkRanges(t, db, conv.Key, "m1"); len(got) != 1 {
			t.Errorf("chunks of %s = %v, want only the one after the forget", conv.Key, got)
		}
		for table, n := range autonomyCounts(t, db, conv.Key) {
			if n != 0 {
				t.Errorf("%s rows of %s = %d, want 0", table, conv.Key, n)
			}
		}
		if s, _ := db.ForgetStamp(ctx, conv.Key); s == stamp {
			t.Errorf("forget stamp of %s unchanged by forgetting in the scope", conv.Key)
		}
	}
	if n := reconcile(t, db); n != 0 {
		t.Errorf("reconciled %d members forgotten by ForgetPrivateScope, want none", n)
	}
}

func TestReconcileForgottenChunks_SharedScope(t *testing.T) {
	db := newTestDB(t)
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	seedConversations(t, db, a, b)
	for _, conv := range []store.ConversationRecord{a, b} {
		writeChunks(t, db, store.ChunkWrite{ConversationKey: conv.Key, Model: "m1", Chunks: []store.MemoryChunk{
			testChunk(conv.Key, 1, 2, chunkAt(0), chunkAt(1), "m1"),
			testChunk(conv.Key, 3, 4, chunkAt(40), chunkAt(41), "m1"),
		}})
	}
	oldForget(t, db, store.MemoryScopeKey(a.Key), "20001", chunkAt(20))
	if n := reconcile(t, db); n != 1 {
		t.Fatalf("reconciled %d members, want the scope's one", n)
	}
	for _, conv := range []store.ConversationRecord{a, b} {
		if got, want := chunkRanges(t, db, conv.Key, "m1"), [][2]int64{{3, 4}}; !slices.Equal(got, want) {
			t.Errorf("chunks of %s = %v, want %v", conv.Key, got, want)
		}
	}
	if n := reconcile(t, db); n != 0 {
		t.Errorf("reconciling again handled %d members", n)
	}
}
