package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// keyedTables are the tables whose rows DeleteConversation removes by
// conversation_key.
var keyedTables = []string{
	"messages", "tasks", "outbox_actions", "persona_instances", "persona_versions", "group_policies",
	"conversation_members", "proactive_log", "group_memory", "member_memory", "memory_revisions",
	"memory_chunks", "memory_chunk_state", "memory_chunk_rebuild", "memory_chunk_forgets",
	"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects", "conversation_scene", "chat_images",
}

type seededConversation struct {
	key       string
	eventKeys []string
	seqs      []int64
}

// seedConversation stores a private conversation with a message from each
// of users (the first starts a task, the others join it) and an observed one
// from the first, a proactive task
// with an internal event, two actions of which one is acked, and a row in
// every other table keyed by conversation, plus a model call and an audit
// event naming it. Each message's text ends with note.
func seedConversation(t *testing.T, db *DB, platform, convID, note string, users ...string) seededConversation {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	key := store.ConversationKey(testBot, platform, conloomapi.ConvPrivate, convID)
	s := seededConversation{key: key}
	channel := ""
	if platform == store.PlatformPanel {
		channel = store.ChannelPanel
	}
	taskID := "task_" + convID
	for i, uid := range append(slices.Clone(users), users[0]) {
		observed := i == len(users)
		msgID := "msg_" + convID + "_" + strconv.Itoa(i)
		ev := conloomapi.Event{
			EventID: "evt_" + msgID, EventType: "message", BotInstanceID: testBot, Platform: platform, OccurredAt: now,
			Conversation: conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: convID, PlatformConversationID: convID},
			Actor:        conloomapi.Actor{ID: "usr_" + uid, PlatformUserID: uid, DisplayName: "小明"},
			Message:      conloomapi.Message{ID: msgID, PlatformMessageID: "p_" + msgID, Text: "周末去爬山 " + note},
		}
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		accepted := conloomapi.EventAccepted{
			EventID: ev.EventID, Disposition: conloomapi.DispositionReply, TaskID: taskID, Merged: i > 0,
		}
		if observed {
			accepted = conloomapi.EventAccepted{EventID: ev.EventID, Disposition: conloomapi.DispositionObserve}
		}
		resp, err := json.Marshal(accepted)
		if err != nil {
			t.Fatal(err)
		}
		in := store.Ingest{
			Event: store.EventRecord{
				BotInstanceID: testBot, IdempotencyKey: "k_" + msgID, RequestHash: "h_" + msgID, EventID: ev.EventID,
				Response: resp, RawEvent: raw, CreatedAt: now,
			},
			Conversation: &store.ConversationRecord{
				BotInstanceID: testBot, ID: convID, Key: key, Type: conloomapi.ConvPrivate, Platform: platform,
				PlatformConversationID: convID,
			},
			User: &store.UserRecord{BotInstanceID: testBot, Platform: platform, PlatformUserID: uid, DisplayName: "小明"},
			Message: &store.MessageRecord{
				ID: msgID, BotInstanceID: testBot, ConversationKey: key, Platform: platform,
				PlatformMessageID: "p_" + msgID, ActorID: "usr_" + uid, PlatformUserID: uid, DisplayName: "小明",
				Text: ev.Message.Text, Channel: channel, OccurredAt: now, CreatedAt: now,
			},
		}
		switch {
		case observed:
		case i == 0:
			in.NewTask = &store.TaskRecord{
				ID: taskID, BotInstanceID: testBot, ConversationKey: key, Status: conloomapi.TaskRunning,
				ActorPlatformUserID: uid, TriggerMessageIDs: []string{msgID}, EventOccurredAt: now,
			}
		default:
			in.MergeIntoTaskID = taskID
		}
		if _, err := db.Ingest(ctx, in); err != nil {
			t.Fatalf("Ingest %s: %v", msgID, err)
		}
		s.eventKeys = append(s.eventKeys, in.Event.IdempotencyKey)
	}

	joinID := "task_join_" + convID
	resp, err := json.Marshal(conloomapi.EventAccepted{Disposition: conloomapi.DispositionReply, TaskID: joinID})
	if err != nil {
		t.Fatal(err)
	}
	join := store.Ingest{
		Event: store.EventRecord{
			BotInstanceID: testBot, IdempotencyKey: "conloom:join:" + joinID, EventID: "join_" + joinID,
			Response: resp, CreatedAt: now,
		},
		NewTask: &store.TaskRecord{
			ID: joinID, BotInstanceID: testBot, ConversationKey: key, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: users[0], TriggerMessageIDs: []string{"msg_" + convID + "_0"}, EventOccurredAt: now,
		},
	}
	if _, err := db.Ingest(ctx, join); err != nil {
		t.Fatalf("Ingest join: %v", err)
	}
	s.eventKeys = append(s.eventKeys, join.Event.IdempotencyKey)

	sent, queued := convAction("act_"+convID+"_1", key, now), convAction("act_"+convID+"_2", key, now)
	sent.Channel, queued.Channel = channel, channel
	sent.TaskID, queued.TaskID = taskID, taskID
	if err := db.CompleteTask(ctx, store.TaskCompletion{
		TaskID: taskID, Status: conloomapi.TaskSucceeded, Actions: []store.OutboxRecord{sent, queued},
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	botMsg := &store.MessageRecord{
		ID: "msg_bot_" + sent.Action.ActionID, BotInstanceID: testBot, ConversationKey: key, Platform: platform,
		Text: "好呀 一起去爬山", Channel: channel, FromBot: true, OccurredAt: now, CreatedAt: now,
	}
	if _, err := db.AckOutbox(ctx, testBot, sent.Action.ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent}, botMsg, now); err != nil {
		t.Fatalf("AckOutbox: %v", err)
	}

	if _, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
		ConversationKey: key, TemplateName: "default", Persona: conloomapi.DefaultPersona(),
		Reason: "test", UpdatedBy: "29999", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendPersonaVersion: %v", err)
	}
	if _, err := db.AppendPolicyVersion(ctx, store.PolicyVersion{
		ConversationKey: key, Policy: conloomapi.DefaultGroupPolicy(), Reason: "test", UpdatedBy: "29999", CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendPolicyVersion: %v", err)
	}
	if err := db.PutMember(ctx, store.MemberRecord{
		ConversationKey: key, PlatformUserID: users[0], LastEngagedAt: now, ProactiveLevel: "low", UpdatedAt: now,
	}); err != nil {
		t.Fatalf("PutMember: %v", err)
	}
	if err := db.AppendProactiveLog(ctx, store.ProactiveLogEntry{
		ConversationKey: key, PlatformUserID: users[0], Kind: store.ProactiveEvaluated, TaskID: joinID, CreatedAt: now,
	}); err != nil {
		t.Fatalf("AppendProactiveLog: %v", err)
	}
	if err := db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: key, Summary: "约了周末爬山", CursorSeq: 1}); err != nil {
		t.Fatalf("PutGroupMemory: %v", err)
	}
	until := now.Add(20 * time.Minute)
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: key, Activity: "洗澡", TaskID: taskID,
		StartedAt: now, UntilAt: until}, &store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: users[0], DueAt: until}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	seedBuffRows(t, db, key, taskID, now)
	if _, err := db.SetScene(ctx, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneInPerson, Place: "食堂",
		SinceAt: now, TouchedAt: now}); err != nil {
		t.Fatalf("SetScene: %v", err)
	}
	seedChatImage(t, db, key, "img_"+convID, now)
	at := fmtTime(now)
	for _, seed := range []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO member_memory (conversation_key, platform_user_id, display_name, note, affinity, last_interaction_day,
			model_delta_day, model_delta_sum, decay_steps, forgotten_at, version, updated_at)
			VALUES (?, '20001', '小明', '喜欢爬山', 3, '', '', 0, 0, '', 1, ?)`, []any{key, at}},
		{`INSERT INTO memory_revisions (conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at)
			VALUES (?, '20001', 'note', '', '喜欢爬山', 'consolidate', 'test', ?)`, []any{key, at}},
		{`INSERT INTO memory_chunks (conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, dims, vector, created_at)
			VALUES (?, 1, 2, ?, ?, 'hash', 'embed-test', 2, x'0000803f00000040', ?)`, []any{key, at, at, at}},
		{`INSERT INTO memory_chunk_state (conversation_key, cursor_seq, model) VALUES (?, 2, 'embed-test')`, []any{key}},
		{`INSERT INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq) VALUES (?, 1, 2)`, []any{key}},
		{`INSERT INTO memory_chunk_forgets (conversation_key, platform_user_id, forgotten_at) VALUES (?, '20001', ?)`, []any{key, at}},
	} {
		if _, err := db.w.ExecContext(ctx, seed.stmt, seed.args...); err != nil {
			t.Fatalf("seed %s: %v", seed.stmt, err)
		}
	}
	if err := db.InsertModelCalls(ctx, []store.ModelCall{{
		At: now, BotInstanceID: testBot, ConversationKey: key, TaskID: taskID, Purpose: "reply", Role: "chat",
		Model: "model-test", Outcome: store.OutcomeOK,
	}}); err != nil {
		t.Fatalf("InsertModelCalls: %v", err)
	}
	if err := db.InsertAudit(ctx, store.AuditEvent{
		ID: "aud_" + convID, BotInstanceID: testBot, Kind: "policy.update", Actor: "29999", Target: key, CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}

	rows, err := db.r.QueryContext(ctx, `SELECT seq FROM messages WHERE conversation_key = ? ORDER BY seq`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		s.seqs = append(s.seqs, seq)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return s
}

// keyedCounts counts each keyed table's rows of the conversation, and of
// model_calls.
func keyedCounts(t *testing.T, db *DB, key string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range append(slices.Clone(keyedTables), "model_calls") {
		var n int
		if err := db.r.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func eventCount(t *testing.T, db *DB, keys []string) int {
	t.Helper()
	n := 0
	for _, k := range keys {
		if _, err := db.GetEvent(context.Background(), testBot, k); err == nil {
			n++
		} else if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetEvent %s: %v", k, err)
		}
	}
	return n
}

func userExists(t *testing.T, db *DB, platform, uid string) bool {
	t.Helper()
	var n int
	if err := db.r.QueryRow(`SELECT COUNT(*) FROM users WHERE bot_instance_id = ? AND platform = ? AND platform_user_id = ?`,
		testBot, platform, uid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func indexedRows(t *testing.T, db *DB, seqs []int64) int {
	t.Helper()
	total := 0
	for _, seq := range seqs {
		var n int
		if err := db.r.QueryRow(`SELECT COUNT(*) FROM message_fts WHERE rowid = ?`, seq).Scan(&n); err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return total
}

func searchHits(t *testing.T, db *DB, key string) int {
	t.Helper()
	hits, err := db.SearchMessages(context.Background(), key, []string{"爬山"}, search.Range{}, 0, 50)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	return len(hits)
}

func TestDeleteConversation_ClearsOnlyThatKey(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := seedConversation(t, db, store.PlatformPanel, "panel_a", "", "panel_admin", "20001")
	// b's raw events hold a's conversation ID in their text.
	b := seedConversation(t, db, store.PlatformPanel, "panel_b", "panel_a", "panel_admin")
	c := seedConversation(t, db, testPlatform, "conv_c", "", "20001")

	for table, n := range keyedCounts(t, db, a.key) {
		if n == 0 {
			t.Fatalf("seed left %s empty for the deleted conversation", table)
		}
	}
	if searchHits(t, db, a.key) == 0 || indexedRows(t, db, a.seqs) != len(a.seqs) {
		t.Fatal("seeded messages are not indexed")
	}
	keep := map[string]map[string]int{b.key: keyedCounts(t, db, b.key), c.key: keyedCounts(t, db, c.key)}
	modelCalls, audits := countRows(t, db, "model_calls"), countRows(t, db, "audit_events")
	bHits, cHits := searchHits(t, db, b.key), searchHits(t, db, c.key)

	if err := db.DeleteConversation(ctx, a.key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}

	for table, n := range keyedCounts(t, db, a.key) {
		want := 0
		if table == "model_calls" {
			want = 1
		}
		if n != want {
			t.Errorf("%s rows of the deleted conversation = %d, want %d", table, n, want)
		}
	}
	if _, err := db.GetConversationByKey(ctx, a.key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted conversation lookup err = %v, want ErrNotFound", err)
	}
	if n := eventCount(t, db, a.eventKeys); n != 0 {
		t.Errorf("events of the deleted conversation = %d, want 0", n)
	}
	if n := indexedRows(t, db, a.seqs); n != 0 {
		t.Errorf("keyword index rows of the deleted messages = %d, want 0", n)
	}
	if n := searchHits(t, db, a.key); n != 0 {
		t.Errorf("search hits in the deleted conversation = %d, want 0", n)
	}
	if !userExists(t, db, store.PlatformPanel, "panel_admin") || !userExists(t, db, testPlatform, "20001") {
		t.Error("user also in another conversation was deleted")
	}
	if got := countRows(t, db, "model_calls"); got != modelCalls {
		t.Errorf("model_calls = %d, want %d", got, modelCalls)
	}
	if got := countRows(t, db, "audit_events"); got != audits {
		t.Errorf("audit_events = %d, want %d", got, audits)
	}

	for _, other := range []seededConversation{b, c} {
		if got := keyedCounts(t, db, other.key); !maps.Equal(got, keep[other.key]) {
			t.Errorf("%s rows = %v, want %v", other.key, got, keep[other.key])
		}
		if _, err := db.GetConversationByKey(ctx, other.key); err != nil {
			t.Errorf("other conversation %s: %v", other.key, err)
		}
		if n := eventCount(t, db, other.eventKeys); n != len(other.eventKeys) {
			t.Errorf("events of %s = %d, want %d", other.key, n, len(other.eventKeys))
		}
		if n := indexedRows(t, db, other.seqs); n != len(other.seqs) {
			t.Errorf("index rows of %s = %d, want %d", other.key, n, len(other.seqs))
		}
	}
	if searchHits(t, db, b.key) != bHits || searchHits(t, db, c.key) != cHits {
		t.Error("search hits of the other conversations changed")
	}
	if got, want := countRows(t, db, "message_fts"), countRows(t, db, "messages"); got != want {
		t.Errorf("index rows = %d, want one per message (%d)", got, want)
	}

	if err := db.DeleteConversation(ctx, a.key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second DeleteConversation err = %v, want ErrNotFound", err)
	}
}

// TestDeleteConversation_CoversEveryKeyedTable fails when a table with a
// conversation_key column is neither deleted from nor deliberately kept.
func TestDeleteConversation_CoversEveryKeyedTable(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.r.Query(`SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p
		WHERE m.type = 'table' AND p.name = 'conversation_key' ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(keyedTables), "model_calls")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("tables with conversation_key = %v, want %v", got, want)
	}
	if tables := slices.Sorted(slices.Values(conversationTables)); !slices.Equal(tables, slices.Sorted(slices.Values(keyedTables))) {
		t.Fatalf("conversationTables = %v, want %v", conversationTables, keyedTables)
	}
}

// createEmptyChat stores a conversation and its user without any message, as
// creating a panel chat does.
func createEmptyChat(t *testing.T, db *DB, platform, convID, uid string) {
	t.Helper()
	ctx := context.Background()
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertConversation(ctx, tx, store.ConversationRecord{
			BotInstanceID: testBot, ID: convID, Type: conloomapi.ConvPrivate, Platform: platform, PlatformConversationID: convID,
		}); err != nil {
			return err
		}
		return upsertUser(ctx, tx, store.UserRecord{BotInstanceID: testBot, Platform: platform, PlatformUserID: uid, DisplayName: "小明"})
	})
	if err != nil {
		t.Fatalf("create chat %s: %v", convID, err)
	}
}

func TestDeleteConversation_UserRows(t *testing.T) {
	ctx := context.Background()

	db := newTestDB(t)
	a := seedConversation(t, db, store.PlatformPanel, "panel_a", "", "panel_admin")
	createEmptyChat(t, db, store.PlatformPanel, "panel_b", "panel_admin")
	if err := db.DeleteConversation(ctx, a.key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if !userExists(t, db, store.PlatformPanel, "panel_admin") {
		t.Error("panel user of another panel chat without messages was deleted")
	}

	db = newTestDB(t)
	a = seedConversation(t, db, store.PlatformPanel, "panel_a", "", "panel_admin")
	createEmptyChat(t, db, testPlatform, "conv_b", "panel_admin")
	if err := db.DeleteConversation(ctx, a.key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if userExists(t, db, store.PlatformPanel, "panel_admin") {
		t.Error("panel user of the only panel chat is kept")
	}

	db = newTestDB(t)
	x := seedConversation(t, db, testPlatform, "conv_x", "", "20003")
	seedConversation(t, db, testPlatform, "conv_y", "", "20004")
	if err := db.DeleteConversation(ctx, x.key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if userExists(t, db, testPlatform, "20003") {
		t.Error("platform user only in the deleted conversation is kept")
	}
	if !userExists(t, db, testPlatform, "20004") {
		t.Error("platform user of another conversation was deleted")
	}

	db = newTestDB(t)
	x = seedConversation(t, db, testPlatform, "conv_x", "", "20003", "20005")
	seedConversation(t, db, testPlatform, "conv_y", "", "20005")
	if err := db.DeleteConversation(ctx, x.key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if userExists(t, db, testPlatform, "20003") {
		t.Error("platform user only in the deleted conversation is kept")
	}
	if !userExists(t, db, testPlatform, "20005") {
		t.Error("platform user who also wrote in another conversation was deleted")
	}
}

// seedPanelMessage stores a message of the panel user panel_admin with text in
// the panel chat convKey (ID convID), storing the chat too.
func seedPanelMessage(t *testing.T, db *DB, convKey, convID, msgID, text string) {
	t.Helper()
	now := time.Now().UTC()
	in := store.Ingest{
		Event: store.EventRecord{
			BotInstanceID: testBot, IdempotencyKey: "k_" + msgID, RequestHash: "h_" + msgID,
			EventID: "evt_" + msgID, Response: []byte(`{}`), RawEvent: []byte(`{}`), CreatedAt: now,
		},
		Conversation: &store.ConversationRecord{
			BotInstanceID: testBot, ID: convID, Key: convKey, Type: conloomapi.ConvPrivate,
			Platform: store.PlatformPanel, PlatformConversationID: convID,
		},
		User: &store.UserRecord{BotInstanceID: testBot, Platform: store.PlatformPanel,
			PlatformUserID: "panel_admin", DisplayName: "admin"},
		Message: &store.MessageRecord{
			ID: msgID, BotInstanceID: testBot, ConversationKey: convKey, Platform: store.PlatformPanel,
			PlatformMessageID: "p_" + msgID, ActorID: "panel_admin", PlatformUserID: "panel_admin",
			DisplayName: "admin", Text: text, Channel: store.ChannelPanel, OccurredAt: now, CreatedAt: now,
		},
	}
	if _, err := db.Ingest(context.Background(), in); err != nil {
		t.Fatalf("Ingest %s: %v", msgID, err)
	}
}

func TestDeleteMessagesFrom_CleansMessagesFTSChunksAndTimers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	target := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_del")
	other := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_keep")

	for i := 1; i <= 6; i++ {
		seedPanelMessage(t, db, target, "panel_del", fmt.Sprintf("m%d", i), fmt.Sprintf("爬山 %d", i))
	}
	seedPanelMessage(t, db, other, "panel_keep", "m7", "爬山 7")
	seedPanelMessage(t, db, other, "panel_keep", "m8", "爬山 8")

	at := fmtTime(now)
	seedChunk := func(key string, r [2]int64) {
		if _, err := db.w.ExecContext(ctx, `INSERT INTO memory_chunks
			(conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, dims, vector, created_at)
			VALUES (?, ?, ?, ?, ?, 'hash', 'embed-test', 2, x'0000803f00000040', ?)`,
			key, r[0], r[1], at, at, at); err != nil {
			t.Fatalf("seed chunk %v: %v", r, err)
		}
	}
	seedRebuild := func(key string, r [2]int64) {
		if _, err := db.w.ExecContext(ctx, `INSERT INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq)
			VALUES (?, ?, ?)`, key, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	seedTimer := func(key, kind string, anchor int64) {
		if _, err := db.w.ExecContext(ctx, `INSERT INTO conversation_timers
			(conversation_key, kind, bot_instance_id, platform_user_id, due_at, anchor_seq, lease_until, created_at)
			VALUES (?, ?, ?, 'panel_admin', ?, ?, '', ?)`,
			key, kind, testBot, at, anchor, at); err != nil {
			t.Fatalf("seed timer %s: %v", kind, err)
		}
	}
	for _, r := range [][2]int64{{1, 3}, {2, 5}, {4, 6}} {
		seedChunk(target, r)
	}
	seedRebuild(target, [2]int64{4, 6})
	seedTimer(target, "status_return", 3)
	seedTimer(target, "initiate", 5)
	// The other chat's rows lie past the cut too (its seqs are 7 and 8), so
	// only the conversation key keeps them.
	seedChunk(other, [2]int64{7, 8})
	seedRebuild(other, [2]int64{7, 8})
	seedTimer(other, "initiate", 7)

	n, err := db.DeleteMessagesFrom(ctx, target, 4)
	if err != nil || n != 3 {
		t.Fatalf("DeleteMessagesFrom = %d, %v; want 3, nil", n, err)
	}

	seqs := func(key string) []int64 {
		rows, err := db.r.QueryContext(ctx, `SELECT seq FROM messages WHERE conversation_key = ? ORDER BY seq`, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var seq int64
			if err := rows.Scan(&seq); err != nil {
				t.Fatal(err)
			}
			out = append(out, seq)
		}
		return out
	}
	if got := seqs(target); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("target seqs = %v, want [1 2 3]", got)
	}
	if got := seqs(other); !slices.Equal(got, []int64{7, 8}) {
		t.Errorf("other seqs = %v, want [7 8]", got)
	}
	if got := indexedRows(t, db, []int64{1, 2, 3}); got != 3 {
		t.Errorf("target index rows = %d, want 3", got)
	}
	if got := indexedRows(t, db, []int64{4, 5, 6}); got != 0 {
		t.Errorf("deleted index rows = %d, want 0", got)
	}
	if got := indexedRows(t, db, []int64{7, 8}); got != 2 {
		t.Errorf("other index rows = %d, want 2", got)
	}
	if got := searchHits(t, db, target); got != 3 {
		t.Errorf("target search hits = %d, want 3", got)
	}

	chunkRanges := func(table string, key string) [][2]int64 {
		rows, err := db.r.QueryContext(ctx, `SELECT start_seq, end_seq FROM `+table+
			` WHERE conversation_key = ? ORDER BY start_seq, end_seq`, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][2]int64
		for rows.Next() {
			var r [2]int64
			if err := rows.Scan(&r[0], &r[1]); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	if got := chunkRanges("memory_chunks", target); !slices.Equal(got, [][2]int64{{1, 3}}) {
		t.Errorf("chunks = %v, want [[1 3]]", got)
	}
	if got := chunkRanges("memory_chunk_rebuild", target); !slices.Equal(got, [][2]int64{{2, 5}}) {
		t.Errorf("rebuild = %v, want [[2 5]]", got)
	}
	if got := chunkRanges("memory_chunks", other); !slices.Equal(got, [][2]int64{{7, 8}}) {
		t.Errorf("other chat's chunks = %v, want [[7 8]] untouched", got)
	}
	if got := chunkRanges("memory_chunk_rebuild", other); !slices.Equal(got, [][2]int64{{7, 8}}) {
		t.Errorf("other chat's rebuild = %v, want [[7 8]] untouched", got)
	}
	if got := timerAnchors(t, db, target); !slices.Equal(got, []int64{3}) {
		t.Errorf("timer anchors = %v, want [3]", got)
	}
	if got := timerAnchors(t, db, other); !slices.Equal(got, []int64{7}) {
		t.Errorf("other chat's timer anchors = %v, want [7] untouched", got)
	}
}

// timerAnchors lists the anchor seqs of the conversation's timers in order.
func timerAnchors(t *testing.T, db *DB, convKey string) []int64 {
	t.Helper()
	rows, err := db.r.QueryContext(context.Background(), `SELECT anchor_seq FROM conversation_timers
		WHERE conversation_key = ? ORDER BY anchor_seq`, convKey)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var anchors []int64
	for rows.Next() {
		var anchor int64
		if err := rows.Scan(&anchor); err != nil {
			t.Fatal(err)
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return anchors
}

func TestDeleteMessagesFrom_UnknownChatAndBadSeq(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_bad")
	if _, err := db.DeleteMessagesFrom(ctx, key, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteMessagesFrom of a missing chat err = %v, want ErrNotFound", err)
	}
	seedPanelMessage(t, db, key, "panel_bad", "b1", "爬山")
	for _, from := range []int64{0, -1} {
		if n, err := db.DeleteMessagesFrom(ctx, key, from); err == nil {
			t.Errorf("DeleteMessagesFrom(%d) = %d, nil; want an error", from, n)
		}
	}
	if n := countRows(t, db, "messages"); n != 1 {
		t.Errorf("messages = %d, want 1 after refused deletes", n)
	}
}

func TestIngest_DeleteMessagesWithNewTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_regen")
	for i := 1; i <= 4; i++ {
		seedPanelMessage(t, db, key, "panel_regen", fmt.Sprintf("g%d", i), fmt.Sprintf("爬山 %d", i))
	}
	at := fmtTime(time.Now().UTC())
	if _, err := db.w.ExecContext(ctx, `INSERT INTO memory_chunks
		(conversation_key, start_seq, end_seq, start_at, end_at, text_hash, model, dims, vector, created_at)
		VALUES (?, 3, 4, ?, ?, 'hash', 'embed-test', 2, x'0000803f00000040', ?)`, key, at, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.ExecContext(ctx, `INSERT INTO conversation_timers
		(conversation_key, kind, bot_instance_id, platform_user_id, due_at, anchor_seq, lease_until, created_at)
		VALUES (?, 'initiate', ?, 'panel_admin', ?, 3, '', ?)`, key, testBot, at, at); err != nil {
		t.Fatal(err)
	}
	stamp, err := db.ForgetStamp(ctx, key)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	task := store.TaskRecord{ID: "task_regen", BotInstanceID: testBot, ConversationKey: key, Status: conloomapi.TaskQueued,
		ActorPlatformUserID: "panel_admin", TriggerMessageIDs: []string{"g1", "g2"}, EventOccurredAt: now, CreatedAt: now, UpdatedAt: now}
	if _, err := db.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "conloom:regen:task_regen", EventID: "regen_task_regen",
			Response: []byte(`{"task_id":"task_regen"}`), CreatedAt: now},
		NewTask:        &task,
		DeleteMessages: &store.MessageDelete{ConversationKey: key, FromSeq: 3},
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	msgs, err := db.MessagesAfter(ctx, key, 0, 10)
	if err != nil || len(msgs) != 2 || msgs[0].ID != "g1" || msgs[1].ID != "g2" {
		t.Fatalf("messages = %+v, %v; want g1 and g2", msgs, err)
	}
	if got := indexedRows(t, db, []int64{3, 4}); got != 0 {
		t.Errorf("deleted index rows = %d, want 0", got)
	}
	if n := countRows(t, db, "memory_chunks"); n != 0 {
		t.Errorf("chunks = %d, want the one in the range deleted", n)
	}
	if got := timerAnchors(t, db, key); len(got) != 0 {
		t.Errorf("timer anchors = %v, want the one in the range deleted", got)
	}
	if got, err := db.GetTask(ctx, testBot, "task_regen"); err != nil || !slices.Equal(got.TriggerMessageIDs, []string{"g1", "g2"}) {
		t.Errorf("task = %+v, %v; want it stored with its triggers", got, err)
	}
	if _, err := db.GetEvent(ctx, testBot, "conloom:regen:task_regen"); err != nil {
		t.Errorf("event: %v", err)
	}
	if s, _ := db.ForgetStamp(ctx, key); s == stamp {
		t.Error("forget stamp unchanged by the delete")
	}
}

func TestIngest_DeleteMessagesOfAnotherConversationRefused(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	target := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_t")
	other := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_o")
	seedPanelMessage(t, db, target, "panel_t", "t1", "爬山")
	seedPanelMessage(t, db, other, "panel_o", "o1", "爬山")
	now := time.Now().UTC()
	_, err := db.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "conloom:regen:task_x", EventID: "regen_task_x",
			Response: []byte(`{}`), CreatedAt: now},
		NewTask: &store.TaskRecord{ID: "task_x", BotInstanceID: testBot, ConversationKey: other, Status: conloomapi.TaskQueued,
			TriggerMessageIDs: []string{"o1"}, EventOccurredAt: now, CreatedAt: now, UpdatedAt: now},
		DeleteMessages: &store.MessageDelete{ConversationKey: target, FromSeq: 1},
	})
	if err == nil {
		t.Fatal("Ingest deleting one chat's messages with another chat's task succeeded")
	}
	if n := countRows(t, db, "messages"); n != 2 {
		t.Errorf("messages = %d, want both kept", n)
	}
	if _, err := db.GetTask(ctx, testBot, "task_x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task err = %v, want ErrNotFound", err)
	}
}

// The indexer reads a chat and embeds it before it writes the chunks, so a
// withdraw can come in between: nothing cut from the withdrawn messages may
// be stored then, and a rebuild under way leaves the queue as the withdraw
// left it.
func TestWriteChunks_SkippedAfterWithdraw(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_race")
	for i := 1; i <= 6; i++ {
		seedPanelMessage(t, db, key, "panel_race", fmt.Sprintf("r%d", i), fmt.Sprintf("爬山 %d", i))
	}
	if _, err := db.w.ExecContext(ctx, `INSERT INTO memory_chunk_rebuild (conversation_key, start_seq, end_seq)
		VALUES (?, 2, 5)`, key); err != nil {
		t.Fatal(err)
	}

	stamp, err := db.ForgetStamp(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := db.ChunkRebuilds(ctx, key, 10)
	if err != nil || len(queue) != 1 {
		t.Fatalf("ChunkRebuilds = %+v, %v", queue, err)
	}
	if n, err := db.DeleteMessagesFrom(ctx, key, 4); err != nil || n != 3 {
		t.Fatalf("DeleteMessagesFrom = %d, %v; want 3, nil", n, err)
	}

	ok, err := db.WriteChunks(ctx, store.ChunkWrite{ConversationKey: key, Model: "m1", ForgetStamp: stamp, CursorSeq: 6,
		Chunks: []store.MemoryChunk{testChunk(key, 1, 3, chunkAt(0), chunkAt(2), "m1"), testChunk(key, 4, 6, chunkAt(3), chunkAt(5), "m1")}})
	if err != nil || ok {
		t.Fatalf("forward WriteChunks read before the withdraw = %v, %v; want skipped", ok, err)
	}
	ok, err = db.WriteChunks(ctx, store.ChunkWrite{ConversationKey: key, Model: "m1", ForgetStamp: stamp, Rebuilt: queue,
		Chunks: []store.MemoryChunk{testChunk(key, 2, 5, chunkAt(1), chunkAt(4), "m1")}})
	if err != nil || ok {
		t.Fatalf("rebuild WriteChunks read before the withdraw = %v, %v; want skipped", ok, err)
	}
	if got := chunkRanges(t, db, key, "m1"); len(got) != 0 {
		t.Errorf("chunks = %v, want none of the withdrawn messages", got)
	}
	if got := rebuildRanges(t, db, key); !slices.Equal(got, [][2]int64{{2, 5}}) {
		t.Errorf("rebuild queue = %v, want [[2 5]] kept for the next round", got)
	}
	if st, _ := db.ChunkState(ctx, key); st.CursorSeq != 0 {
		t.Errorf("cursor = %d, want unchanged", st.CursorSeq)
	}

	fresh, err := db.ForgetStamp(ctx, key)
	if err != nil || fresh == stamp {
		t.Fatalf("ForgetStamp after a withdraw = %q (was %q), %v; want a new stamp", fresh, stamp, err)
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: key, Model: "m1", ForgetStamp: fresh, CursorSeq: 3, Rebuilt: queue,
		Chunks: []store.MemoryChunk{testChunk(key, 1, 3, chunkAt(0), chunkAt(2), "m1")}})
	if got := chunkRanges(t, db, key, "m1"); !slices.Equal(got, [][2]int64{{1, 3}}) {
		t.Errorf("chunks cut after the withdraw = %v, want [[1 3]]", got)
	}
}
