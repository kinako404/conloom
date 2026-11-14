package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// openPre0005 creates a database with migrations 0001–0004 applied and
// returns its path and an open handle.
func openPre0005(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := sql.Open("sqlite", path+"?"+pragmas)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for v, name := range []string{"0001_init.sql", "0002_ambient.sql", "0003_message_actor_index.sql",
		"0004_persona_version_template.sql"} {
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := old.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := old.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, '2026-11-12T00:00:00.000000000Z')`,
			v+1); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	return path, old
}

func tableInfos(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close tables: %v", err)
	}
	infos := map[string][]string{}
	for _, table := range tables {
		cols, err := db.Query(`SELECT cid, name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("table_info %s: %v", table, err)
		}
		for cols.Next() {
			var cid, notNull, pk int
			var name, typ, dflt string
			if err := cols.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatalf("scan table_info %s: %v", table, err)
			}
			infos[table] = append(infos[table], fmt.Sprintf("%d|%s|%s|%d|%s|%d", cid, name, typ, notNull, dflt, pk))
		}
		if err := cols.Close(); err != nil {
			t.Fatalf("close table_info %s: %v", table, err)
		}
	}
	return infos
}

func TestMigration0005_UpgradeFromExistingData(t *testing.T) {
	ctx := context.Background()
	path, old := openPre0005(t)
	const ts = "2026-11-12T00:00:00.000000000Z"
	for _, stmt := range []string{
		`INSERT INTO bot_instances (id, platform, name, created_at, updated_at) VALUES ('bot_test', 'qq', '织语', '` + ts + `', '` + ts + `')`,
		`INSERT INTO conversations (bot_instance_id, id, key, type, platform, platform_conversation_id, created_at, updated_at)
			VALUES ('bot_test', 'conv_10001', '` + testConvKey + `', 'group', 'qq', '10001', '` + ts + `', '` + ts + `')`,
		`INSERT INTO messages (bot_instance_id, id, conversation_key, platform, platform_message_id, actor_id,
			platform_user_id, display_name, text, reply_to, mentions, attachments, from_bot, occurred_at, created_at)
			VALUES ('bot_test', 'msg_1', '` + testConvKey + `', 'qq', 'p1', 'user_20001', '20001', '小明', '你好', '', '[]', '[]', 0, '` + ts + `', '` + ts + `')`,
		`INSERT INTO conversation_members (conversation_key, platform_user_id, last_engaged_at, proactive_level,
			level_set_by_user, interests, updated_at) VALUES ('` + testConvKey + `', '20001', '` + ts + `', 'low', 0, '{}', '` + ts + `')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		for _, name := range []string{"group_memory", "member_memory", "memory_revisions"} {
			if !tableExists(t, db.r, name) {
				t.Fatalf("%s missing after Open #%d", name, i+1)
			}
		}
		for table, want := range map[string]int{"bot_instances": 1, "conversations": 1, "messages": 1, "conversation_members": 1} {
			if n := countRows(t, db, table); n != want {
				t.Fatalf("%s rows = %d after Open #%d, want %d", table, n, i+1, want)
			}
		}
		if n := countRows(t, db, "schema_migrations"); n != len(files) {
			t.Fatalf("schema_migrations rows = %d, want %d", n, len(files))
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

func TestMigration0005_OnlyAddsTables(t *testing.T) {
	path, old := openPre0005(t)
	before := tableInfos(t, old)
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db, err := open(context.Background(), path, migrationsUpTo(t, 5))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	after := tableInfos(t, db.r)
	for table, cols := range before {
		if !reflect.DeepEqual(after[table], cols) {
			t.Errorf("table %s changed:\n before %v\n after  %v", table, cols, after[table])
		}
	}
	var added []string
	for table := range after {
		if _, ok := before[table]; !ok {
			added = append(added, table)
		}
	}
	for _, name := range []string{"group_memory", "member_memory", "memory_revisions"} {
		if _, ok := after[name]; !ok {
			t.Errorf("table %s not added (added %v)", name, added)
		}
	}
}

func intPtr(v int) *int { return &v }

func TestGroupMemory_PutGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.GetGroupMemory(ctx, testConvKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetGroupMemory missing err = %v, want ErrNotFound", err)
	}
	runAt := time.Date(2026, 11, 12, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	g := store.GroupMemory{ConversationKey: testConvKey, Summary: "大家在聊周末去爬山", CursorSeq: 42, LastRunAt: runAt}
	if err := db.PutGroupMemory(ctx, g); err != nil {
		t.Fatalf("PutGroupMemory: %v", err)
	}
	got, err := db.GetGroupMemory(ctx, testConvKey)
	if err != nil {
		t.Fatalf("GetGroupMemory: %v", err)
	}
	if got.Summary != g.Summary || got.CursorSeq != 42 || !got.LastRunAt.Equal(runAt) || got.LastError != "" || got.UpdatedAt.IsZero() {
		t.Fatalf("group memory = %+v, want %+v with UpdatedAt set", got, g)
	}

	g.CursorSeq, g.LastError = 50, "parse failed"
	if err := db.PutGroupMemory(ctx, g); err != nil {
		t.Fatalf("PutGroupMemory upsert: %v", err)
	}
	got, err = db.GetGroupMemory(ctx, testConvKey)
	if err != nil || got.CursorSeq != 50 || got.LastError != "parse failed" || got.Summary != g.Summary {
		t.Fatalf("after upsert = %+v, %v", got, err)
	}
	if n := countRows(t, db, "group_memory"); n != 1 {
		t.Fatalf("group_memory rows = %d, want 1", n)
	}
	if _, err := db.GetGroupMemory(ctx, testConvKeyB); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other conversation err = %v, want ErrNotFound", err)
	}
}

func TestMemberMemory_RoundTripNullAffinity(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	forgot := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	want20001 := store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", DisplayName: "小明", Note: "喜欢爬山"}
	want20002 := store.MemberMemory{
		ConversationKey: testConvKey, PlatformUserID: "20002", DisplayName: "小红", Note: "养了狗",
		Affinity: intPtr(20), LastInteractionDay: "2026-11-12", ModelDeltaDay: "2026-11-11", ModelDeltaSum: -2,
		DecaySteps: 1, ForgottenAt: forgot,
	}
	conflicts, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey,
		Members: []store.MemberMemory{want20002, want20001}})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("ApplyMemoryWrite = %v, %v", conflicts, err)
	}

	got, err := db.GetMemberMemories(ctx, testConvKey, []string{"20001", "20002", "20003"})
	if err != nil {
		t.Fatalf("GetMemberMemories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetMemberMemories = %d entries, want 2 (20003 absent): %+v", len(got), got)
	}
	if got["20001"].Affinity != nil {
		t.Fatalf("20001 affinity = %v, want nil", *got["20001"].Affinity)
	}
	if a := got["20002"].Affinity; a == nil || *a != 20 {
		t.Fatalf("20002 affinity = %v, want 20", a)
	}
	for _, want := range []store.MemberMemory{want20001, want20002} {
		m := got[want.PlatformUserID]
		if m.Version != 1 || m.UpdatedAt.IsZero() || !m.ForgottenAt.Equal(want.ForgottenAt) {
			t.Fatalf("%s version/updated/forgotten = %d/%v/%v", want.PlatformUserID, m.Version, m.UpdatedAt, m.ForgottenAt)
		}
		want.Version, want.UpdatedAt, want.ForgottenAt = m.Version, m.UpdatedAt, m.ForgottenAt
		if !reflect.DeepEqual(m, want) {
			t.Fatalf("member = %+v, want %+v", m, want)
		}
	}

	list, err := db.ListMemberMemories(ctx, testConvKey)
	if err != nil || len(list) != 2 || list[0].PlatformUserID != "20001" || list[1].PlatformUserID != "20002" {
		t.Fatalf("ListMemberMemories = %+v, %v; want 20001, 20002", list, err)
	}
	if other, err := db.ListMemberMemories(ctx, testConvKeyB); err != nil || len(other) != 0 {
		t.Fatalf("other conversation = %+v, %v; want none", other, err)
	}
	if none, err := db.GetMemberMemories(ctx, testConvKey, nil); err != nil || len(none) != 0 {
		t.Fatalf("GetMemberMemories(nil) = %+v, %v; want none", none, err)
	}
}

func TestApplyMemoryWrite_OptimisticConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey,
		Members: []store.MemberMemory{{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "原笔记"}}}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	conflicts, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{
		ConversationKey: testConvKey,
		Group:           &store.GroupMemory{ConversationKey: testConvKey, Summary: "新摘要", CursorSeq: 7},
		Members: []store.MemberMemory{
			{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "过期的笔记"},
			{ConversationKey: testConvKey, PlatformUserID: "20002", Note: "养了狗"},
		},
		Revisions: []store.MemoryRevision{
			{ConversationKey: testConvKey, PlatformUserID: "20001", Field: store.FieldNote, OldValue: "原笔记",
				NewValue: "过期的笔记", Source: store.RevisionConsolidate},
			{ConversationKey: testConvKey, PlatformUserID: "20002", Field: store.FieldNote, NewValue: "养了狗",
				Source: store.RevisionConsolidate},
			{ConversationKey: testConvKey, Field: store.FieldSummary, NewValue: "新摘要", Source: store.RevisionConsolidate},
		},
	})
	if err != nil {
		t.Fatalf("ApplyMemoryWrite: %v", err)
	}
	if !reflect.DeepEqual(conflicts, []string{"20001"}) {
		t.Fatalf("conflicts = %v, want [20001]", conflicts)
	}
	got, err := db.GetMemberMemories(ctx, testConvKey, []string{"20001", "20002"})
	if err != nil {
		t.Fatalf("GetMemberMemories: %v", err)
	}
	if m := got["20001"]; m.Note != "原笔记" || m.Version != 1 {
		t.Fatalf("20001 = %+v, want untouched note at version 1", m)
	}
	if m := got["20002"]; m.Note != "养了狗" || m.Version != 1 {
		t.Fatalf("20002 = %+v, want written at version 1", m)
	}
	if revs, err := db.ListMemoryRevisions(ctx, testConvKey, "20001"); err != nil || len(revs) != 0 {
		t.Fatalf("20001 revisions = %+v, %v; want none", revs, err)
	}
	revs, err := db.ListMemoryRevisions(ctx, testConvKey, "20002")
	if err != nil || len(revs) != 1 || revs[0].NewValue != "养了狗" || revs[0].Source != store.RevisionConsolidate ||
		revs[0].Field != store.FieldNote || revs[0].CreatedAt.IsZero() {
		t.Fatalf("20002 revisions = %+v, %v", revs, err)
	}
	if revs, err := db.ListMemoryRevisions(ctx, testConvKey, ""); err != nil || len(revs) != 1 || revs[0].Field != store.FieldSummary {
		t.Fatalf("summary revisions = %+v, %v", revs, err)
	}
	if g, err := db.GetGroupMemory(ctx, testConvKey); err != nil || g.Summary != "新摘要" || g.CursorSeq != 7 {
		t.Fatalf("group = %+v, %v; want written despite member conflict", g, err)
	}

	conflicts, err = db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey,
		Members: []store.MemberMemory{{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "新笔记", Version: 1}}})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("write at current version = %v, %v", conflicts, err)
	}
	got, err = db.GetMemberMemories(ctx, testConvKey, []string{"20001"})
	if err != nil || got["20001"].Note != "新笔记" || got["20001"].Version != 2 {
		t.Fatalf("20001 after matching version = %+v, %v; want 新笔记 at version 2", got["20001"], err)
	}
}

func TestApplyMemoryWrite_RejectsOtherConversation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	writes := map[string]store.MemoryWrite{
		"member": {
			ConversationKey: testConvKey,
			Group:           &store.GroupMemory{ConversationKey: testConvKey, Summary: "摘要"},
			Members: []store.MemberMemory{
				{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山"},
				{ConversationKey: testConvKeyB, PlatformUserID: "20002", Note: "养了狗"},
			},
			Revisions: []store.MemoryRevision{{ConversationKey: testConvKey, PlatformUserID: "20001",
				Field: store.FieldNote, NewValue: "喜欢爬山", Source: store.RevisionConsolidate}},
		},
		"revision": {
			ConversationKey: testConvKey,
			Members:         []store.MemberMemory{{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山"}},
			Revisions: []store.MemoryRevision{{ConversationKey: testConvKeyB, PlatformUserID: "20001",
				Field: store.FieldNote, NewValue: "喜欢爬山", Source: store.RevisionConsolidate}},
		},
		"group": {
			ConversationKey: testConvKey,
			Group:           &store.GroupMemory{ConversationKey: testConvKeyB, Summary: "摘要"},
		},
	}
	for name, w := range writes {
		if conflicts, err := db.ApplyMemoryWrite(ctx, w); err == nil {
			t.Errorf("%s: ApplyMemoryWrite = %v, nil; want error", name, conflicts)
		}
	}
	for _, table := range []string{"group_memory", "member_memory", "memory_revisions"} {
		if n := countRows(t, db, table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

func TestApplyMemoryWrite_RejectsDuplicateMember(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	conflicts, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{
		ConversationKey: testConvKey,
		Group:           &store.GroupMemory{ConversationKey: testConvKey, Summary: "摘要"},
		Members: []store.MemberMemory{
			{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山"},
			{ConversationKey: testConvKey, PlatformUserID: "20002", Note: "养了狗"},
			{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢游泳"},
		},
		Revisions: []store.MemoryRevision{{ConversationKey: testConvKey, PlatformUserID: "20001",
			Field: store.FieldNote, NewValue: "喜欢爬山", Source: store.RevisionConsolidate}},
	})
	if err == nil {
		t.Fatalf("ApplyMemoryWrite = %v, nil; want error for 20001 twice", conflicts)
	}
	for _, table := range []string{"group_memory", "member_memory", "memory_revisions"} {
		if n := countRows(t, db, table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

func TestForgetMember_ClearsAndDeletesRevisions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	rev := func(user, field, value string) store.MemoryRevision {
		return store.MemoryRevision{ConversationKey: testConvKey, PlatformUserID: user, Field: field,
			NewValue: value, Source: store.RevisionConsolidate}
	}
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{
		ConversationKey: testConvKey,
		Members: []store.MemberMemory{
			{ConversationKey: testConvKey, PlatformUserID: "20001", DisplayName: "小明", Note: "喜欢爬山",
				Affinity: intPtr(52), LastInteractionDay: "2026-11-12", ModelDeltaDay: "2026-11-12", ModelDeltaSum: 3, DecaySteps: 2},
			{ConversationKey: testConvKey, PlatformUserID: "20002", DisplayName: "小红", Note: "养了狗"},
		},
		Revisions: []store.MemoryRevision{
			rev("20001", store.FieldNote, "喜欢爬山"), rev("20001", store.FieldAffinity, "52"),
			rev("20002", store.FieldNote, "养了狗"), rev("", store.FieldSummary, "摘要"),
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKeyB,
		Revisions: []store.MemoryRevision{{ConversationKey: testConvKeyB, PlatformUserID: "20001", Field: store.FieldNote,
			NewValue: "别的群", Source: store.RevisionConsolidate}}}); err != nil {
		t.Fatalf("seed other conversation: %v", err)
	}

	at := time.Date(2026, 11, 12, 21, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if err := db.ForgetMember(ctx, testConvKey, "20001", at); err != nil {
		t.Fatalf("ForgetMember: %v", err)
	}
	got, err := db.GetMemberMemories(ctx, testConvKey, []string{"20001", "20002"})
	if err != nil {
		t.Fatalf("GetMemberMemories: %v", err)
	}
	m := got["20001"]
	if m.Note != "" || m.DisplayName != "" || m.Affinity != nil || m.LastInteractionDay != "" || m.ModelDeltaDay != "" ||
		m.ModelDeltaSum != 0 || m.DecaySteps != 0 || !m.ForgottenAt.Equal(at) || m.Version != 2 {
		t.Fatalf("forgotten member = %+v", m)
	}
	if got["20002"].Note != "养了狗" {
		t.Fatalf("20002 = %+v, want untouched", got["20002"])
	}
	stale := store.MemberMemory{ConversationKey: testConvKey, PlatformUserID: "20001", Note: "喜欢爬山",
		Affinity: intPtr(53), Version: 1}
	if conflicts, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey,
		Members: []store.MemberMemory{stale}}); err != nil || !reflect.DeepEqual(conflicts, []string{"20001"}) {
		t.Fatalf("write read before forget = %v, %v; want conflict on 20001", conflicts, err)
	}
	if revs, err := db.ListMemoryRevisions(ctx, testConvKey, "20001"); err != nil || len(revs) != 0 {
		t.Fatalf("20001 revisions = %+v, %v; want none", revs, err)
	}
	for _, user := range []string{"20002", ""} {
		if revs, err := db.ListMemoryRevisions(ctx, testConvKey, user); err != nil || len(revs) != 1 {
			t.Fatalf("revisions of %q = %+v, %v; want 1 kept", user, revs, err)
		}
	}
	if revs, err := db.ListMemoryRevisions(ctx, testConvKeyB, "20001"); err != nil || len(revs) != 1 {
		t.Fatalf("other conversation revisions = %+v, %v; want 1 kept", revs, err)
	}

	if err := db.ForgetMember(ctx, testConvKey, "20003", at); err != nil {
		t.Fatalf("ForgetMember absent: %v", err)
	}
	got, err = db.GetMemberMemories(ctx, testConvKey, []string{"20003"})
	if err != nil || !got["20003"].ForgottenAt.Equal(at) || got["20003"].Version != 1 || got["20003"].Affinity != nil {
		t.Fatalf("absent member after forget = %+v, %v; want row with forgotten_at at version 1", got["20003"], err)
	}
	if err := db.ForgetMember(ctx, testConvKey, "", at); err == nil {
		t.Fatal("ForgetMember with empty user = nil, want error")
	}
}

// memInsertMessages stores msgs in order in one transaction.
func memInsertMessages(t testing.TB, db *DB, msgs ...store.MessageRecord) {
	t.Helper()
	err := db.inTx(context.Background(), func(tx *sql.Tx) error {
		for _, m := range msgs {
			if err := insertMessage(context.Background(), tx, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert messages: %v", err)
	}
}

var memMsgCount int

func memMessage(convKey, actorID, userID string, fromBot bool) store.MessageRecord {
	memMsgCount++
	return store.MessageRecord{
		ID: fmt.Sprintf("msg_%d", memMsgCount), BotInstanceID: testBot, ConversationKey: convKey, Platform: testPlatform,
		ActorID: actorID, PlatformUserID: userID, Text: "你好", FromBot: fromBot,
		OccurredAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC),
	}
}

func latestSeq(t *testing.T, db *DB, convKey string) int64 {
	t.Helper()
	var seq int64
	if err := db.r.QueryRow(`SELECT MAX(seq) FROM messages WHERE conversation_key = ?`, convKey).Scan(&seq); err != nil {
		t.Fatalf("max seq: %v", err)
	}
	return seq
}

func TestBacklogCursor_CountsOwnConversationOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var msgs []store.MessageRecord
	for i := 0; i < 1200; i++ {
		msgs = append(msgs, memMessage(testConvKey, "user_20001", "20001", false),
			memMessage(testConvKeyB, "user_20002", "20002", false))
	}
	memInsertMessages(t, db, msgs...)

	cursor, err := db.BacklogCursor(ctx, testConvKey, 1000)
	if err != nil {
		t.Fatalf("BacklogCursor: %v", err)
	}
	if n, err := db.CountMessagesAfter(ctx, testConvKey, cursor); err != nil || n != 1000 {
		t.Fatalf("messages after cursor %d = %d, %v; want 1000", cursor, n, err)
	}
	cursor, err = db.BacklogCursor(ctx, testConvKey, 0)
	if want := latestSeq(t, db, testConvKey); err != nil || cursor != want {
		t.Fatalf("BacklogCursor(0) = %d, %v; want latest seq %d", cursor, err, want)
	}

	small := store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, "conv_10003")
	var few []store.MessageRecord
	for i := 0; i < 5; i++ {
		few = append(few, memMessage(small, "user_20003", "20003", false))
	}
	memInsertMessages(t, db, few...)
	for _, backlog := range []int{5, 1000} {
		if cursor, err := db.BacklogCursor(ctx, small, backlog); err != nil || cursor != 0 {
			t.Fatalf("BacklogCursor(small, %d) = %d, %v; want 0", backlog, cursor, err)
		}
	}
	if cursor, err := db.BacklogCursor(ctx, "bot_test:qq:group:empty", 0); err != nil || cursor != 0 {
		t.Fatalf("BacklogCursor(empty, 0) = %d, %v; want 0", cursor, err)
	}
}

func TestMessagesAfterAndCount(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var mine []store.MessageRecord
	for i := 0; i < 5; i++ {
		m := memMessage(testConvKey, "user_20001", "20001", false)
		mine = append(mine, m)
		memInsertMessages(t, db, m, memMessage(testConvKeyB, "user_20002", "20002", false))
	}
	all, err := db.MessagesAfter(ctx, testConvKey, 0, 100)
	if err != nil || len(all) != 5 {
		t.Fatalf("MessagesAfter(0) = %d, %v; want 5", len(all), err)
	}
	for i, m := range all {
		if m.ID != mine[i].ID || m.ConversationKey != testConvKey {
			t.Fatalf("message %d = %s/%s, want %s", i, m.ID, m.ConversationKey, mine[i].ID)
		}
	}
	got, err := db.MessagesAfter(ctx, testConvKey, all[1].Seq, 2)
	if err != nil || len(got) != 2 || got[0].ID != mine[2].ID || got[1].ID != mine[3].ID {
		t.Fatalf("MessagesAfter(seq2, 2) = %+v, %v; want messages 3 and 4", got, err)
	}
	if got, err := db.MessagesAfter(ctx, testConvKey, all[1].Seq, 0); err != nil || len(got) != 0 {
		t.Fatalf("MessagesAfter limit 0 = %d, %v; want none", len(got), err)
	}
	for after, want := range map[int64]int{0: 5, all[1].Seq: 3, all[4].Seq: 0} {
		if n, err := db.CountMessagesAfter(ctx, testConvKey, after); err != nil || n != want {
			t.Errorf("CountMessagesAfter(%d) = %d, %v; want %d", after, n, err, want)
		}
	}
}

func TestUserIDsForActors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	memInsertMessages(t, db,
		memMessage(testConvKey, "user_20001", "20001", false),
		memMessage(testConvKeyB, "user_20002", "29999", false),
		memMessage(testConvKey, "user_20002", "20002", false),
		memMessage(testConvKeyB, "user_20002", "29999", false),
		memMessage(testConvKey, "bot_30001", "30001", true),
	)
	got, err := db.UserIDsForActors(ctx, testConvKey, []string{"user_20001", "user_20002", "bot_30001", "user_20003"})
	if err != nil {
		t.Fatalf("UserIDsForActors: %v", err)
	}
	if want := map[string]string{"user_20001": "20001", "user_20002": "20002"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UserIDsForActors = %v, want %v", got, want)
	}
	many := make([]string, 2*maxCompoundSelect+1)
	for i := range many {
		many[i] = fmt.Sprintf("user_%d", 40000+i)
	}
	many[len(many)-1] = "user_20002"
	if got, err := db.UserIDsForActors(ctx, testConvKey, many); err != nil || !reflect.DeepEqual(got, map[string]string{"user_20002": "20002"}) {
		t.Fatalf("UserIDsForActors(%d ids) = %v, %v; want only user_20002", len(many), got, err)
	}
	if got, err := db.UserIDsForActors(ctx, testConvKey, nil); err != nil || len(got) != 0 {
		t.Fatalf("UserIDsForActors(nil) = %v, %v; want none", got, err)
	}
}

func TestPurgeMemoryRevisions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	if _, err := db.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: testConvKey, Revisions: []store.MemoryRevision{
		{ConversationKey: testConvKey, PlatformUserID: "20001", Field: store.FieldAffinity, OldValue: "20", NewValue: "19",
			Source: store.RevisionRule, Reason: "decay", CreatedAt: now.Add(-31 * day)},
		{ConversationKey: testConvKey, PlatformUserID: "20001", Field: store.FieldAffinity, OldValue: "19", NewValue: "20",
			Source: store.RevisionRule, Reason: "daily", CreatedAt: now.Add(-29 * day)},
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := db.PurgeMemoryRevisions(ctx, now.Add(-30*day))
	if err != nil || n != 1 {
		t.Fatalf("PurgeMemoryRevisions = %d, %v; want 1", n, err)
	}
	revs, err := db.ListMemoryRevisions(ctx, testConvKey, "20001")
	if err != nil || len(revs) != 1 || revs[0].Reason != "daily" || !revs[0].CreatedAt.Equal(now.Add(-29*day)) {
		t.Fatalf("remaining revisions = %+v, %v; want the 29-day-old one", revs, err)
	}
}

func TestListConversationsByType(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	convs := []store.ConversationRecord{
		{BotInstanceID: testBot, ID: "conv_10002", Type: conloomapi.ConvGroup, Platform: testPlatform, PlatformConversationID: "10002"},
		{BotInstanceID: testBot, ID: "conv_20001", Type: conloomapi.ConvPrivate, Platform: testPlatform, PlatformConversationID: "20001"},
		{BotInstanceID: testBot, ID: testConvID, Type: conloomapi.ConvGroup, Platform: testPlatform, PlatformConversationID: testGroup},
	}
	if err := db.inTx(ctx, func(tx *sql.Tx) error {
		for _, c := range convs {
			if err := upsertConversation(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := db.ListConversations(ctx, conloomapi.ConvGroup)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	var keys []string
	for _, c := range got {
		if c.Type != conloomapi.ConvGroup || c.BotInstanceID != testBot || c.Platform != testPlatform {
			t.Fatalf("conversation = %+v", c)
		}
		keys = append(keys, c.Key)
	}
	if want := []string{testConvKey, testConvKeyB}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("group keys = %v, want %v", keys, want)
	}
	if got, err := db.ListConversations(ctx, "channel"); err != nil || len(got) != 0 {
		t.Fatalf("ListConversations(channel) = %+v, %v; want none", got, err)
	}
}
