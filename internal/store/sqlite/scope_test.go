package sqlite

import (
	"context"
	"errors"
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

func TestConversationType(t *testing.T) {
	for key, want := range map[string]string{
		testConvKey:    conloomapi.ConvGroup,
		testPrivateKey: conloomapi.ConvPrivate,
		store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_a:b"): conloomapi.ConvPrivate,
		"bot_test:qq:group": "",
		"":                  "",
	} {
		if got := store.ConversationType(key); got != want {
			t.Errorf("ConversationType(%q) = %q, want %q", key, got, want)
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
	}
	until := now.Add(20 * time.Minute)
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: scope, OriginConversationKey: a.Key, Activity: "洗澡",
		TaskID: "task_s", StartedAt: now, UntilAt: until},
		&store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until}); err != nil {
		t.Fatal(err)
	}
	armTimers(t, db, testTimer(b.Key, store.TimerStatusReturn, until))
	seedBuffRows(t, db, scope, "task_b", now)
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
	for table, n := range autonomyCounts(t, db, scope) {
		if n != 0 {
			t.Errorf("%s rows of the scope = %d, want 0", table, n)
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

func TestStatus_SharedScopeOrigin(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	seedConversations(t, db, a, b)
	scope := store.MemoryScopeKey(a.Key)
	start := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	set := func(origin string, until time.Time) error {
		return db.SetStatus(ctx, store.ConversationStatus{ConversationKey: scope, OriginConversationKey: origin, Activity: "洗澡",
			TaskID: "task_" + origin, StartedAt: start, UntilAt: until},
			&store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until})
	}
	armTimers(t, db, testTimer(a.Key, store.TimerFollowUp, start.Add(5*time.Hour)))

	untilA := start.Add(20 * time.Minute)
	if err := set(a.Key, untilA); err != nil {
		t.Fatalf("SetStatus in A: %v", err)
	}
	got, err := db.GetStatus(ctx, scope)
	if err != nil || got.ConversationKey != scope || got.OriginConversationKey != a.Key || got.Origin() != a.Key || !got.UntilAt.Equal(untilA) {
		t.Fatalf("GetStatus = %+v, %v; want the scope's status set in A", got, err)
	}
	if kinds := timerKinds(listTimers(t, db, a.Key)); !slices.Equal(kinds, []store.TimerKind{store.TimerStatusReturn, store.TimerFollowUp}) {
		t.Fatalf("timers of A = %v, want its status_return and follow-up", kinds)
	}
	if n := len(listTimers(t, db, scope)) + len(listTimers(t, db, b.Key)); n != 0 {
		t.Fatalf("timers of the scope and B = %d, want none", n)
	}

	untilB := start.Add(40 * time.Minute)
	if err := set(b.Key, untilB); err != nil {
		t.Fatalf("SetStatus in B: %v", err)
	}
	if got, err := db.GetStatus(ctx, scope); err != nil || got.Origin() != b.Key || !got.UntilAt.Equal(untilB) {
		t.Fatalf("GetStatus after B = %+v, %v", got, err)
	}
	if kinds := timerKinds(listTimers(t, db, a.Key)); !slices.Equal(kinds, []store.TimerKind{store.TimerFollowUp}) {
		t.Errorf("timers of A after B's status = %v, want only the follow-up", kinds)
	}
	if timers := listTimers(t, db, b.Key); len(timers) != 1 || timers[0].Kind != store.TimerStatusReturn || !timers[0].DueAt.Equal(untilB) {
		t.Errorf("timers of B = %+v, want its status_return", timers)
	}
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: scope, OriginConversationKey: b.Key, Activity: "跑步",
		StartedAt: start, UntilAt: untilA}, &store.ConversationTimer{ConversationKey: a.Key, BotInstanceID: testBot,
		PlatformUserID: "20001", DueAt: untilA}); err == nil {
		t.Error("SetStatus in B with A's timer succeeded")
	}

	if ok, err := db.ClearStatus(ctx, scope, untilA); err != nil || ok {
		t.Fatalf("ClearStatus with another until = %v, %v", ok, err)
	}
	if n := len(listTimers(t, db, b.Key)); n != 1 {
		t.Fatalf("timers of B after a mismatched clear = %d, want 1", n)
	}
	if ok, err := db.ClearStatus(ctx, scope, untilB); err != nil || !ok {
		t.Fatalf("ClearStatus = %v, %v", ok, err)
	}
	if n := len(listTimers(t, db, b.Key)); n != 0 {
		t.Errorf("timers of B after clearing = %d, want none", n)
	}
	if n := len(listTimers(t, db, a.Key)); n != 1 {
		t.Errorf("timers of A after clearing = %d, want its follow-up", n)
	}
}

func TestDeleteConversation_SharedStatusOfItsChat(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	seedConversations(t, db, a, b, panelChatConv(testBot, "panel_c"))
	scope := store.MemoryScopeKey(a.Key)
	until := time.Date(2026, 11, 14, 20, 20, 0, 0, time.UTC)
	set := func(origin string) {
		t.Helper()
		if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: scope, OriginConversationKey: origin, Activity: "洗澡",
			StartedAt: until.Add(-20 * time.Minute), UntilAt: until},
			&store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until}); err != nil {
			t.Fatal(err)
		}
	}
	seedBuffRows(t, db, scope, "task_b", until)

	set(b.Key)
	if err := db.DeleteConversation(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetStatus(ctx, scope); err != nil || got.Origin() != b.Key {
		t.Fatalf("status after deleting A = %+v, %v; want B's kept", got, err)
	}
	if err := db.DeleteConversation(ctx, b.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetStatus(ctx, scope); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("status after deleting B, where it was set: %v, want ErrNotFound", err)
	}
	if n := len(listTimers(t, db, b.Key)); n != 0 {
		t.Errorf("timers of the deleted B = %d", n)
	}
	if got := autonomyCounts(t, db, scope); got["conversation_buffs"] != 1 || got["conversation_buff_events"] != 1 ||
		got["conversation_custom_bars"] != 1 || got["conversation_effects"] != 1 {
		t.Errorf("scope's bars and effects after deleting chats = %v, want them kept", got)
	}
}

// seedScopeRows stores one row under key in every table of scopeKeyedTables.
func seedScopeRows(t *testing.T, db *DB, key string) {
	t.Helper()
	at := fmtTime(time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC))
	for _, seed := range []string{
		`INSERT INTO group_memory (conversation_key, summary, cursor_seq, last_run_at, last_error, updated_at)
			VALUES (?1, '在学钢琴', 5, ?2, '', ?2)`,
		`INSERT INTO member_memory (conversation_key, platform_user_id, display_name, note, affinity, last_interaction_day,
			model_delta_day, model_delta_sum, decay_steps, forgotten_at, version, updated_at)
			VALUES (?1, '20001', '小明', '喜欢猫', 40, '', '', 0, 0, ?2, 1, ?2)`,
		`INSERT INTO memory_revisions (conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at)
			VALUES (?1, '20001', 'note', '', '喜欢猫', 'consolidate', 'test', ?2)`,
		`INSERT INTO memory_chunks (conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, dims, vector, created_at)
			VALUES (?1, 1, 2, ?2, ?2, 'hash', 'm1', 2, x'0000803f00000040', ?2)`,
		`INSERT INTO memory_chunk_state (conversation_key, cursor_seq, model) VALUES (?1, 2, 'm1')`,
		`INSERT INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq) VALUES (?1, 1, 2)`,
		`INSERT INTO memory_chunk_forgets (conversation_key, platform_user_id, forgotten_at) VALUES (?1, '20001', ?2)`,
		`INSERT INTO conversation_status (conversation_key, activity, started_at, until_at, task_id, updated_at, origin_conversation_key)
			VALUES (?1, '洗澡', ?2, ?2, '', ?2, '')`,
		`INSERT INTO conversation_timers (conversation_key, kind, bot_instance_id, platform_user_id, due_at, anchor_seq, lease_until, created_at)
			VALUES (?1, 'status_return', 'bot_test', '20001', ?2, 0, ?2, ?2)`,
		`INSERT INTO conversation_buffs (conversation_key, name, value, day, day_delta, updated_at) VALUES (?1, '心情', 65, '2026-11-14', 5, ?2)`,
		`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at)
			VALUES (?1, '心情', '', 'panel', 60, 65, '', ?2)`,
		`INSERT INTO conversation_custom_bars (conversation_key, name, baseline, half_life_seconds, description, task_id, created_at)
			VALUES (?1, '饥饿', 30, 21600, '', 'task_1', ?2)`,
		`INSERT INTO conversation_effects (conversation_key, name, description, started_at, until_at, task_id)
			VALUES (?1, '着凉', '', ?2, ?2, 'task_2')`,
	} {
		if _, err := db.w.Exec(seed, key, at); err != nil {
			t.Fatalf("seed %s: %v", seed, err)
		}
	}
}

// scopeKeyedTables are the tables seedScopeRows stores a row in, which
// DeleteScope deletes from.
var scopeKeyedTables = []string{
	"group_memory", "member_memory", "memory_revisions",
	"memory_chunks", "memory_chunk_state", "memory_chunk_rebuild", "memory_chunk_forgets",
	"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects",
}

// scopeRowCounts counts the rows under key in every table of scopeKeyedTables.
func scopeRowCounts(t *testing.T, db *DB, key string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range scopeKeyedTables {
		var n int
		if err := db.r.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func TestDeleteScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, other := panelChatConv(testBot, "panel_a"), panelChatConv("bot_other", "panel_c")
	seedConversations(t, db, a, other)
	scope, otherScope := store.MemoryScopeKey(a.Key), store.MemoryScopeKey(other.Key)
	for _, key := range []string{scope, otherScope, testPrivateKey} {
		seedScopeRows(t, db, key)
	}
	all := func(key string, want int) {
		t.Helper()
		for table, n := range scopeRowCounts(t, db, key) {
			if n != want {
				t.Errorf("%s rows of %s = %d, want %d", table, key, n, want)
			}
		}
	}

	if err := db.DeleteScope(ctx, scope); !errors.Is(err, store.ErrBotHasData) {
		t.Fatalf("DeleteScope while the bot has a panel chat: %v, want ErrBotHasData", err)
	}
	all(scope, 1)
	for _, key := range []string{testPrivateKey, a.Key} {
		if err := db.DeleteScope(ctx, key); err == nil || errors.Is(err, store.ErrBotHasData) {
			t.Errorf("DeleteScope(%s) = %v, want refused as no shared scope", key, err)
		}
	}
	all(testPrivateKey, 1)

	if err := db.DeleteConversation(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteScope(ctx, scope); err != nil {
		t.Fatalf("DeleteScope: %v", err)
	}
	all(scope, 0)
	all(otherScope, 1)
	all(testPrivateKey, 1)
}
