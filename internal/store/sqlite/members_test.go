package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master %s: %v", name, err)
	}
	return n > 0
}

func TestMigration0002Applies(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh", func(t *testing.T) {
		db := newTestDB(t)
		for _, name := range []string{"conversation_members", "proactive_log", "proactive_log_conversation_created"} {
			if !tableExists(t, db.r, name) {
				t.Fatalf("%s missing on fresh database", name)
			}
		}
		if n := countRows(t, db, "schema_migrations WHERE version = 2"); n != 1 {
			t.Fatalf("schema_migrations version 2 rows = %d, want 1", n)
		}
	})

	t.Run("upgrade from 0001", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "conloom.db")
		old, err := sql.Open("sqlite", path+"?"+pragmas)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		body, err := migrations.ReadFile("migrations/0001_init.sql")
		if err != nil {
			t.Fatalf("read 0001: %v", err)
		}
		for _, stmt := range []string{
			`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
			string(body),
			`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-11-12T00:00:00.000000000Z')`,
			`INSERT INTO bot_instances (id, platform, name, created_at, updated_at)
				VALUES ('bot_test', 'qq', 'old', '2026-11-12T00:00:00.000000000Z', '2026-11-12T00:00:00.000000000Z')`,
		} {
			if _, err := old.Exec(stmt); err != nil {
				t.Fatalf("seed 0001 database: %v", err)
			}
		}
		if err := old.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		for i := 0; i < 2; i++ {
			db, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open #%d on 0001 database: %v", i+1, err)
			}
			if !tableExists(t, db.r, "conversation_members") || !tableExists(t, db.r, "proactive_log") {
				t.Fatalf("0002 tables missing after upgrade #%d", i+1)
			}
			if n := countRows(t, db, "schema_migrations"); n != 2 {
				t.Fatalf("schema_migrations rows = %d, want 2", n)
			}
			if n := countRows(t, db, "bot_instances"); n != 1 {
				t.Fatalf("bot_instances = %d, want existing row kept", n)
			}
			if err := db.PutMember(ctx, store.MemberRecord{ConversationKey: testConvKey, PlatformUserID: "20001",
				ProactiveLevel: "low"}); err != nil {
				t.Fatalf("PutMember after upgrade: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		}
	})
}

func TestMember_GetMissingIsNotFound(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.GetMember(context.Background(), testConvKey, "20001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMember missing err = %v, want ErrNotFound", err)
	}
}

func TestMember_PutGetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	engaged := time.Date(2026, 11, 12, 1, 2, 3, 456, time.FixedZone("CST", 8*3600))
	updated := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	want := store.MemberRecord{
		ConversationKey: testConvKey, PlatformUserID: "20001", LastEngagedAt: engaged,
		ProactiveLevel: "mid", LevelSetByUser: true,
		Interests: map[string]float64{"golang": 2.71, "原神": 1, "周末": 0.9},
		UpdatedAt: updated,
	}
	if err := db.PutMember(ctx, want); err != nil {
		t.Fatalf("PutMember: %v", err)
	}
	got, err := db.GetMember(ctx, testConvKey, "20001")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if !got.LastEngagedAt.Equal(engaged) || got.LastEngagedAt.Location() != time.UTC || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("times = %v / %v, want %v / %v (UTC)", got.LastEngagedAt, got.UpdatedAt, engaged, updated)
	}
	got.LastEngagedAt, got.UpdatedAt = want.LastEngagedAt, want.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("member = %+v, want %+v", got, want)
	}
	var stored string
	if err := db.r.QueryRow(`SELECT last_engaged_at FROM conversation_members WHERE conversation_key = ? AND platform_user_id = ?`,
		testConvKey, "20001").Scan(&stored); err != nil || stored != fmtTime(engaged) {
		t.Fatalf("stored last_engaged_at = %q, %v; want %q", stored, err, fmtTime(engaged))
	}

	if _, err := db.GetMember(ctx, testConvKeyB, "20001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other conversation err = %v, want ErrNotFound", err)
	}

	if err := db.PutMember(ctx, store.MemberRecord{ConversationKey: testConvKey, PlatformUserID: "20002", ProactiveLevel: "low"}); err != nil {
		t.Fatalf("PutMember empty: %v", err)
	}
	empty, err := db.GetMember(ctx, testConvKey, "20002")
	if err != nil {
		t.Fatalf("GetMember empty: %v", err)
	}
	if empty.Interests == nil || len(empty.Interests) != 0 || !empty.LastEngagedAt.IsZero() || empty.UpdatedAt.IsZero() {
		t.Fatalf("empty member = %+v; want non-nil empty interests, zero engagement, set UpdatedAt", empty)
	}
}

func TestMember_PutUpserts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := store.MemberRecord{
		ConversationKey: testConvKey, PlatformUserID: "20001",
		LastEngagedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ProactiveLevel: "low",
		Interests: map[string]float64{"golang": 1, "咖啡": 1},
	}
	if err := db.PutMember(ctx, first); err != nil {
		t.Fatalf("PutMember first: %v", err)
	}
	second := store.MemberRecord{
		ConversationKey: testConvKey, PlatformUserID: "20001",
		LastEngagedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), ProactiveLevel: "off", LevelSetByUser: true,
		Interests: map[string]float64{"golang": 1.9},
	}
	if err := db.PutMember(ctx, second); err != nil {
		t.Fatalf("PutMember second: %v", err)
	}
	if n := countRows(t, db, "conversation_members"); n != 1 {
		t.Fatalf("conversation_members = %d, want 1", n)
	}
	got, err := db.GetMember(ctx, testConvKey, "20001")
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if !got.LastEngagedAt.Equal(second.LastEngagedAt) || got.ProactiveLevel != "off" || !got.LevelSetByUser ||
		!reflect.DeepEqual(got.Interests, second.Interests) {
		t.Fatalf("member after upsert = %+v, want %+v", got, second)
	}
}

func appendLog(t *testing.T, db *DB, convKey, user, kind string, at time.Time) {
	t.Helper()
	if err := db.AppendProactiveLog(context.Background(), store.ProactiveLogEntry{
		ConversationKey: convKey, PlatformUserID: user, Kind: kind, TaskID: "tsk_" + user, CreatedAt: at,
	}); err != nil {
		t.Fatalf("AppendProactiveLog: %v", err)
	}
}

func TestProactiveLog_CountByUserKindAndWindow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	appendLog(t, db, testConvKey, "20001", store.ProactiveEvaluated, base.Add(-2*time.Hour))
	appendLog(t, db, testConvKey, "20001", store.ProactiveEvaluated, base.Add(-30*time.Minute))
	appendLog(t, db, testConvKey, "20001", store.ProactiveEvaluated, base)
	appendLog(t, db, testConvKey, "20001", store.ProactiveSent, base.Add(-10*time.Minute))
	appendLog(t, db, testConvKey, "20002", store.ProactiveEvaluated, base.Add(-5*time.Minute))
	appendLog(t, db, testConvKeyB, "20001", store.ProactiveEvaluated, base.Add(-5*time.Minute))

	cases := []struct {
		name  string
		user  string
		kind  string
		since time.Time
		want  int
	}{
		{"evaluated last hour", "20001", store.ProactiveEvaluated, base.Add(-time.Hour), 2},
		{"evaluated window inclusive", "20001", store.ProactiveEvaluated, base.Add(-30 * time.Minute), 2},
		{"evaluated all day", "20001", store.ProactiveEvaluated, base.Add(-24 * time.Hour), 3},
		{"sent last hour", "20001", store.ProactiveSent, base.Add(-time.Hour), 1},
		{"sent other user", "20002", store.ProactiveSent, base.Add(-time.Hour), 0},
		{"future window", "20001", store.ProactiveEvaluated, base.Add(time.Second), 0},
		{"local zone since", "20001", store.ProactiveEvaluated, base.Add(-time.Hour).In(time.FixedZone("CST", 8*3600)), 2},
	}
	for _, c := range cases {
		n, err := db.CountProactive(ctx, testConvKey, c.user, c.kind, c.since)
		if err != nil || n != c.want {
			t.Errorf("%s: CountProactive = %d, %v; want %d", c.name, n, err, c.want)
		}
	}
}

func TestProactiveLog_CountAllUsersWhenEmptyUser(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	appendLog(t, db, testConvKey, "20001", store.ProactiveSent, base.Add(-10*time.Minute))
	appendLog(t, db, testConvKey, "20002", store.ProactiveSent, base.Add(-20*time.Minute))
	appendLog(t, db, testConvKey, "20002", store.ProactiveSent, base.Add(-2*time.Hour))
	appendLog(t, db, testConvKey, "20002", store.ProactiveEvaluated, base.Add(-20*time.Minute))
	appendLog(t, db, testConvKeyB, "20001", store.ProactiveSent, base.Add(-10*time.Minute))

	n, err := db.CountProactive(ctx, testConvKey, "", store.ProactiveSent, base.Add(-time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("group sent last hour = %d, %v; want 2", n, err)
	}
	n, err = db.CountProactive(ctx, testConvKey, "", store.ProactiveEvaluated, base.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("group evaluated last hour = %d, %v; want 1", n, err)
	}
}

func TestProactiveLog_Purge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	appendLog(t, db, testConvKey, "20001", store.ProactiveSent, base.Add(-8*24*time.Hour))
	appendLog(t, db, testConvKey, "20002", store.ProactiveEvaluated, base.Add(-7*24*time.Hour-time.Second))
	appendLog(t, db, testConvKey, "20001", store.ProactiveEvaluated, base.Add(-7*24*time.Hour))
	appendLog(t, db, testConvKey, "20001", store.ProactiveSent, base)

	n, err := db.PurgeProactiveLog(ctx, base.Add(-7*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("PurgeProactiveLog = %d, %v; want 2", n, err)
	}
	if left := countRows(t, db, "proactive_log"); left != 2 {
		t.Fatalf("proactive_log rows = %d, want 2", left)
	}
	n, err = db.CountProactive(ctx, testConvKey, "", store.ProactiveSent, time.Time{})
	if err != nil || n != 1 {
		t.Fatalf("remaining sent = %d, %v; want 1", n, err)
	}
}
