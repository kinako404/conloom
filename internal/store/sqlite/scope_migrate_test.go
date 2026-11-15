package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// migrateAt is when the scope migrations of these tests run.
var migrateAt = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// legacyRows stores rows under key as an older version kept a panel chat's
// memory: each statement gets key as ?1.
func legacyRows(t *testing.T, db *DB, key string, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.w.Exec(s, key); err != nil {
			t.Fatalf("seed %s: %v", s, err)
		}
	}
}

func groupRow(summary string, cursor int64, runAt time.Time) string {
	return fmt.Sprintf(`INSERT INTO group_memory (conversation_key, summary, cursor_seq, last_run_at, last_error, updated_at)
		VALUES (?1, '%s', %d, '%s', '', '%s')`, summary, cursor, fmtTime(runAt), fmtTime(runAt))
}

func memberRow(user, note string, affinity, version int, updated time.Time) string {
	return fmt.Sprintf(`INSERT INTO member_memory (conversation_key, platform_user_id, display_name, note, affinity,
		last_interaction_day, model_delta_day, model_delta_sum, decay_steps, forgotten_at, version, updated_at)
		VALUES (?1, '%s', '小明', '%s', %d, '2026-11-15', '', 0, 0, '%s', %d, '%s')`,
		user, note, affinity, fmtTime(time.Time{}), version, fmtTime(updated))
}

func revisionRow(user, value string) string {
	return fmt.Sprintf(`INSERT INTO memory_revisions (conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at)
		VALUES (?1, '%s', 'note', '', '%s', 'consolidate', '', '%s')`, user, value, fmtTime(migrateAt.Add(-time.Hour)))
}

func statusRow(activity string, until time.Time) string {
	return fmt.Sprintf(`INSERT INTO conversation_status (conversation_key, activity, started_at, until_at, task_id, updated_at)
		VALUES (?1, '%s', '%s', '%s', 'task_seed', '%s')`, activity, fmtTime(until.Add(-time.Hour)), fmtTime(until),
		fmtTime(until.Add(-time.Hour)))
}

func buffRow(name string, value float64, updated time.Time) string {
	return fmt.Sprintf(`INSERT INTO conversation_buffs (conversation_key, name, value, day, day_delta, updated_at)
		VALUES (?1, '%s', %g, '2026-11-16', 3, '%s')`, name, value, fmtTime(updated))
}

func buffEventRow(name, reason string) string {
	return fmt.Sprintf(`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at)
		VALUES (?1, '%s', '', 'model', 50, 55, '%s', '%s')`, name, reason, fmtTime(migrateAt.Add(-time.Hour)))
}

// tableDump returns every row under key of every table that keeps rows by
// conversation_key, as text.
func tableDump(t *testing.T, db *DB, key string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range conversationTables {
		rows, err := db.r.Query(`SELECT * FROM `+table+` WHERE conversation_key = ? ORDER BY rowid`, key)
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprint(vals...))
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func migrateScope(t *testing.T, db *DB) ([]store.ScopeMerge, bool) {
	t.Helper()
	merged, ran, err := db.MigratePanelMemoryScope(context.Background(), migrateAt)
	if err != nil {
		t.Fatalf("MigratePanelMemoryScope: %v", err)
	}
	return merged, ran
}

func TestMigrateScope_MergeRules(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b, quiet := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b"), panelChatConv(testBot, "panel_c")
	single := panelChatConv("bot_other", "panel_d")
	seedConversations(t, db, a, b, quiet, single)
	scope, singleScope := store.MemoryScopeKey(a.Key), store.MemoryScopeKey(single.Key)
	early, late := migrateAt.Add(-3*time.Hour), migrateAt.Add(-time.Hour)

	legacyRows(t, db, a.Key,
		groupRow("A 里聊了钢琴", 40, early),
		memberRow("20001", "A 的笔记", 30, 3, early), memberRow("29999", "小红的笔记", 10, 1, early),
		revisionRow("20001", "A 的笔记"), revisionRow("", "A 里聊了钢琴"),
		statusRow("洗澡", migrateAt.Add(10*time.Minute)),
		buffRow("心情", 40, early), buffRow("精力", 20, early),
		buffEventRow("心情", "A 里的事"))
	legacyRows(t, db, b.Key,
		groupRow("B 里聊了考试", 25, late),
		memberRow("20001", "B 的笔记", 50, 2, late),
		revisionRow("20001", "B 的笔记"),
		statusRow("做饭", migrateAt.Add(30*time.Minute)),
		buffRow("心情", 80, late),
		buffEventRow("心情", "B 里的事"))
	legacyRows(t, db, single.Key, groupRow("只有一个聊天", 9, early), memberRow("20001", "唯一的笔记", 60, 4, early))
	for _, chat := range []string{a.Key, b.Key} {
		armTimers(t, db, testTimer(chat, store.TimerStatusReturn, migrateAt.Add(10*time.Minute)))
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: a.Key, Model: "m1",
		Chunks: []store.MemoryChunk{testChunk(a.Key, 1, 2, chunkAt(0), chunkAt(1), "m1")}})

	merged, ran := migrateScope(t, db)
	want := []store.ScopeMerge{{BotInstanceID: "bot_other", Chats: []string{single.Key}},
		{BotInstanceID: testBot, Chats: []string{a.Key, b.Key}}}
	if !ran || !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %+v, %v; want %+v", merged, ran, want)
	}

	if g, err := db.GetGroupMemory(ctx, scope); err != nil || g.Summary != "B 里聊了考试" || g.CursorSeq != 40 || !g.LastRunAt.Equal(late) {
		t.Errorf("scope summary = %+v, %v; want B's (consolidated last) with A's larger cursor 40", g, err)
	}
	members, err := db.GetMemberMemories(ctx, scope, []string{"20001", "29999"})
	if err != nil {
		t.Fatal(err)
	}
	if m := members["20001"]; m.Note != "B 的笔记" || m.Affinity == nil || *m.Affinity != 50 || m.Version != 2 {
		t.Errorf("小明 in the scope = %+v, want B's row (updated last)", m)
	}
	if m := members["29999"]; m.Note != "小红的笔记" {
		t.Errorf("小红 in the scope = %+v, want A's only row", m)
	}
	if revs, err := db.ListMemoryRevisions(ctx, scope, "20001"); err != nil || len(revs) != 2 {
		t.Errorf("小明's scope revisions = %+v, %v; want both kept", revs, err)
	}
	if revs, err := db.ListMemoryRevisions(ctx, scope, ""); err != nil || len(revs) != 1 {
		t.Errorf("summary revisions = %+v, %v; want A's kept", revs, err)
	}
	st, err := db.GetStatus(ctx, scope)
	if err != nil || st.Activity != "做饭" || st.Origin() != b.Key || st.OriginConversationKey != b.Key {
		t.Errorf("scope status = %+v, %v; want B's (ending last) set in B", st, err)
	}
	if kinds := timerKinds(listTimers(t, db, a.Key)); len(kinds) != 0 {
		t.Errorf("timers of A = %v, want its status_return gone with its status", kinds)
	}
	if kinds := timerKinds(listTimers(t, db, b.Key)); !slices.Equal(kinds, []store.TimerKind{store.TimerStatusReturn}) {
		t.Errorf("timers of B = %v, want its status_return kept", kinds)
	}
	bars, err := db.GetBuffs(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, bar := range bars {
		got[bar.Name] = bar.Value
	}
	if !reflect.DeepEqual(got, map[string]float64{"心情": 80, "精力": 20}) {
		t.Errorf("scope bars = %v, want 心情 from B (updated last) and 精力 from A", got)
	}
	if events, err := db.ListBuffEvents(ctx, scope, 10); err != nil || len(events) != 2 {
		t.Errorf("scope bar changes = %+v, %v; want both kept", events, err)
	}
	for _, chat := range []string{a.Key, b.Key, quiet.Key, single.Key} {
		for _, table := range scopeMergeTables {
			var n int
			if err := db.r.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_key = ?`, chat).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%s rows left under %s = %d", table, chat, n)
			}
		}
	}
	if got := chunkRanges(t, db, a.Key, "m1"); len(got) != 1 {
		t.Errorf("chunks of A = %v, want them kept with the chat", got)
	}

	if g, err := db.GetGroupMemory(ctx, singleScope); err != nil || g.Summary != "只有一个聊天" || g.CursorSeq != 9 {
		t.Errorf("single chat's scope summary = %+v, %v", g, err)
	}
	if m, err := db.GetMemberMemories(ctx, singleScope, []string{"20001"}); err != nil || m["20001"].Note != "唯一的笔记" {
		t.Errorf("single chat's scope member = %+v, %v", m, err)
	}
}

func TestMigrateScope_Once(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a := panelChatConv(testBot, "panel_a")
	seedConversations(t, db, a)
	legacyRows(t, db, a.Key, groupRow("第一次", 3, migrateAt.Add(-time.Hour)))

	if merged, ran := migrateScope(t, db); !ran || len(merged) != 1 {
		t.Fatalf("first migration = %+v, %v", merged, ran)
	}
	if v, ok, err := db.GetMeta(ctx, store.MetaPanelMemoryScoped); err != nil || !ok || v != fmtTime(migrateAt) {
		t.Fatalf("GetMeta = %q, %v, %v; want the migration time", v, ok, err)
	}
	legacyRows(t, db, a.Key, groupRow("迁移之后", 5, migrateAt))
	before := tableDump(t, db, a.Key)
	if merged, ran := migrateScope(t, db); ran || merged != nil {
		t.Fatalf("second migration = %+v, %v; want nothing done", merged, ran)
	}
	if after := tableDump(t, db, a.Key); !reflect.DeepEqual(before, after) {
		t.Errorf("second migration changed A's rows:\n%v\n%v", before, after)
	}
	if g, err := db.GetGroupMemory(ctx, store.MemoryScopeKey(a.Key)); err != nil || g.Summary != "第一次" {
		t.Errorf("scope summary = %+v, %v; want the first migration's", g, err)
	}
}

func TestMigrateScope_PlatformUntouched(t *testing.T) {
	db := newTestDB(t)
	platformGroup := store.ConversationRecord{BotInstanceID: testBot, ID: testConvID, Key: testConvKey, Type: conloomapi.ConvGroup,
		Platform: testPlatform, PlatformConversationID: testGroup}
	platformPrivate := store.ConversationRecord{BotInstanceID: testBot, ID: "conv_20001", Key: testPrivateKey,
		Type: conloomapi.ConvPrivate, Platform: testPlatform, PlatformConversationID: "20001"}
	a := panelChatConv(testBot, "panel_a")
	seedConversations(t, db, platformGroup, platformPrivate, a)
	for _, key := range []string{testConvKey, testPrivateKey, a.Key} {
		legacyRows(t, db, key, groupRow("聊了很多", 7, migrateAt.Add(-time.Hour)),
			memberRow("20001", "笔记", 20, 1, migrateAt.Add(-time.Hour)), revisionRow("20001", "笔记"),
			statusRow("洗澡", migrateAt.Add(time.Hour)), buffRow("心情", 70, migrateAt), buffEventRow("心情", "开心"))
		armTimers(t, db, testTimer(key, store.TimerStatusReturn, migrateAt.Add(time.Hour)))
	}
	writeChunks(t, db, store.ChunkWrite{ConversationKey: testPrivateKey, Model: "m1",
		Chunks: []store.MemoryChunk{testChunk(testPrivateKey, 1, 2, chunkAt(0), chunkAt(1), "m1")}})
	before := map[string]map[string][]string{}
	for _, key := range []string{testConvKey, testPrivateKey} {
		before[key] = tableDump(t, db, key)
	}

	if merged, ran := migrateScope(t, db); !ran || len(merged) != 1 || merged[0].BotInstanceID != testBot ||
		!slices.Equal(merged[0].Chats, []string{a.Key}) {
		t.Fatalf("merged = %+v, %v; want only the panel chat", merged, ran)
	}
	for key, rows := range before {
		after := tableDump(t, db, key)
		if !reflect.DeepEqual(rows, after) {
			t.Errorf("rows of %s changed:\nbefore %v\nafter  %v", key, rows, after)
		}
		if n := len(after["conversation_status"]) + len(after["group_memory"]) + len(after["conversation_timers"]); n != 3 {
			t.Errorf("rows of %s = %v", key, after)
		}
	}
	for _, key := range []string{testConvKey, testPrivateKey} {
		if strings.Contains(fmt.Sprint(tableDump(t, db, store.MemoryScopeKey(a.Key))), key) {
			t.Errorf("the scope holds rows naming %s", key)
		}
	}
}

func TestMigration0016_StatusOrigin(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/conloom.db"
	old, err := open(ctx, path, migrationsUpTo(t, 15))
	if err != nil {
		t.Fatal(err)
	}
	before := tableInfos(t, old.r)
	until := fmtTime(migrateAt)
	if _, err := old.w.Exec(`INSERT INTO conversation_status (conversation_key, activity, started_at, until_at, task_id, updated_at)
		VALUES (?, '洗澡', ?, ?, 'task_seed', ?)`, testPrivateKey, until, until, until); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := tableInfos(t, db.r)
	for table, cols := range before {
		want := cols
		if table == "conversation_status" {
			want = append(slices.Clone(cols), "6|origin_conversation_key|TEXT|1|''|0")
		}
		if !slices.Equal(after[table], want) {
			t.Errorf("%s columns %v, want %v", table, after[table], want)
		}
	}
	st, err := db.GetStatus(ctx, testPrivateKey)
	if err != nil || st.Activity != "洗澡" || st.OriginConversationKey != "" || st.Origin() != testPrivateKey {
		t.Fatalf("status kept from before = %+v, %v; want its own conversation as origin", st, err)
	}
}
