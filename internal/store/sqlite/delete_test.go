package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
}
