package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// sceneAt is when the scenes of these tests start.
var sceneAt = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func sceneKey(userID string) string {
	return store.ConversationKey(testBot, testPlatform, conloomapi.ConvPrivate, userID)
}

func setScene(t *testing.T, db *DB, s store.ConversationScene) bool {
	t.Helper()
	changed, err := db.SetScene(context.Background(), s)
	if err != nil {
		t.Fatalf("SetScene %+v: %v", s, err)
	}
	return changed
}

func getScene(t *testing.T, db *DB, key string) store.ConversationScene {
	t.Helper()
	s, err := db.GetScene(context.Background(), key)
	if err != nil {
		t.Fatalf("GetScene %s: %v", key, err)
	}
	return s
}

// inPerson sets the scene of key in person at 食堂 since and touched at at.
func inPerson(t *testing.T, db *DB, key string, at time.Time) {
	t.Helper()
	if !setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneInPerson, Place: "食堂",
		SinceAt: at, TouchedAt: at}) {
		t.Fatal("SetScene in person reported no change")
	}
}

// checkScene compares the scene's mode, place, times and previous scene.
func checkScene(t *testing.T, got, want store.ConversationScene) {
	t.Helper()
	if got.Mode != want.Mode || got.Place != want.Place || !got.SinceAt.Equal(want.SinceAt) ||
		!got.TouchedAt.Equal(want.TouchedAt) || got.PrevMode != want.PrevMode || got.PrevPlace != want.PrevPlace {
		t.Errorf("scene = %+v, want %+v", got, want)
	}
}

func TestScene_GetMissingIsNotFound(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.GetScene(context.Background(), sceneKey("20001")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetScene without a row: %v, want ErrNotFound", err)
	}
}

func TestScene_SetRecordsPrevAndNoop(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, origin := sceneKey("20001"), sceneKey("29999")
	t1, t2, t3 := sceneAt, sceneAt.Add(10*time.Minute), sceneAt.Add(time.Hour)

	if setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneOnline, SinceAt: t1, TouchedAt: t1}) {
		t.Error("SetScene online without a row reported a change")
	}
	if _, err := db.GetScene(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetScene after an online no-op: %v, want ErrNotFound", err)
	}

	if !setScene(t, db, store.ConversationScene{ConversationKey: key, OriginConversationKey: origin, Mode: conloomapi.SceneInPerson,
		Place: "食堂", SinceAt: t1, TouchedAt: t1, UpdatedAt: t1}) {
		t.Error("SetScene in person reported no change")
	}
	got := getScene(t, db, key)
	checkScene(t, got, store.ConversationScene{Mode: conloomapi.SceneInPerson, Place: "食堂", SinceAt: t1, TouchedAt: t1,
		PrevMode: conloomapi.SceneOnline})
	if got.ConversationKey != key || got.OriginConversationKey != origin || got.Origin() != origin || !got.UpdatedAt.Equal(t1) {
		t.Errorf("scene keys and update = %+v, want set in %s at %v", got, origin, t1)
	}

	if setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneInPerson, Place: "食堂",
		SinceAt: t2, TouchedAt: t2, UpdatedAt: t2}) {
		t.Error("SetScene of the same scene reported a change")
	}
	got = getScene(t, db, key)
	checkScene(t, got, store.ConversationScene{Mode: conloomapi.SceneInPerson, Place: "食堂", SinceAt: t1, TouchedAt: t2,
		PrevMode: conloomapi.SceneOnline})
	if got.OriginConversationKey != "" || got.Origin() != key || !got.UpdatedAt.Equal(t2) {
		t.Errorf("scene after the no-op = %+v, want set in its own conversation at %v", got, t2)
	}

	if !setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneOnline, SinceAt: t3, TouchedAt: t3}) {
		t.Error("SetScene online reported no change")
	}
	checkScene(t, getScene(t, db, key), store.ConversationScene{Mode: conloomapi.SceneOnline, SinceAt: t3, TouchedAt: t3,
		PrevMode: conloomapi.SceneInPerson, PrevPlace: "食堂"})
}

func TestScene_SwitchTaskID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := sceneKey("20001")
	at := sceneAt
	set := func(mode, task string) {
		t.Helper()
		at = at.Add(time.Minute)
		setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: mode, Place: "",
			SinceAt: at, TouchedAt: at, SwitchTaskID: task})
	}
	want := func(step, task string) {
		t.Helper()
		if got := getScene(t, db, key); got.SwitchTaskID != task {
			t.Errorf("%s: switch task %q, want %q", step, got.SwitchTaskID, task)
		}
	}

	set(conloomapi.SceneInPerson, "task_in")
	want("switch in person", "task_in")
	set(conloomapi.SceneOnline, "task_a")
	want("switch online", "task_a")
	set(conloomapi.SceneOnline, "task_a")
	want("no-op of the same task", "task_a")
	set(conloomapi.SceneOnline, "task_b")
	want("no-op of another task", "")
	set(conloomapi.SceneInPerson, "")
	want("panel switch in person", "")
	set(conloomapi.SceneOnline, "task_c")
	want("switch online again", "task_c")
	set(conloomapi.SceneOnline, "")
	want("panel no-op", "")

	set(conloomapi.SceneInPerson, "task_d")
	touched := at
	if ok, err := db.EndIdleScene(ctx, key, touched, touched.Add(3*time.Hour)); err != nil || !ok {
		t.Fatalf("EndIdleScene = %v, %v", ok, err)
	}
	want("silence fallback", "")
}

func TestScene_SetZeroTimesAreNow(t *testing.T) {
	db := newTestDB(t)
	key := sceneKey("20001")
	before := time.Now()
	setScene(t, db, store.ConversationScene{ConversationKey: key, Mode: conloomapi.SceneInPerson})
	got := getScene(t, db, key)
	if got.SinceAt.Before(before) || !got.TouchedAt.Equal(got.SinceAt) || !got.UpdatedAt.Equal(got.SinceAt) {
		t.Errorf("scene with zero times = %+v, want all now (after %v)", got, before)
	}
}

func TestScene_SetRejectsBadInput(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, s := range []store.ConversationScene{
		{Mode: conloomapi.SceneOnline},
		{ConversationKey: sceneKey("20001"), Mode: "walking"},
		{ConversationKey: sceneKey("20001")},
	} {
		if _, err := db.SetScene(ctx, s); err == nil {
			t.Errorf("SetScene %+v = nil error", s)
		}
	}
	if _, err := db.GetScene(ctx, sceneKey("20001")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetScene after refused writes: %v, want ErrNotFound", err)
	}
}

func TestScene_TouchOnlyInPerson(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	online, near := sceneKey("20001"), sceneKey("29999")
	inPerson(t, db, online, sceneAt)
	setScene(t, db, store.ConversationScene{ConversationKey: online, Mode: conloomapi.SceneOnline, SinceAt: sceneAt, TouchedAt: sceneAt})
	inPerson(t, db, near, sceneAt)
	at := sceneAt.Add(30 * time.Minute)

	if ok, err := db.TouchScene(ctx, online, at); err != nil || ok {
		t.Errorf("TouchScene online = %v, %v; want false", ok, err)
	}
	if got := getScene(t, db, online); !got.TouchedAt.Equal(sceneAt) {
		t.Errorf("online scene touched at %v, want %v unchanged", got.TouchedAt, sceneAt)
	}
	if ok, err := db.TouchScene(ctx, near, at); err != nil || !ok {
		t.Errorf("TouchScene in person = %v, %v; want true", ok, err)
	}
	if got := getScene(t, db, near); !got.TouchedAt.Equal(at) || !got.SinceAt.Equal(sceneAt) {
		t.Errorf("in-person scene = %+v, want touched at %v since %v", got, at, sceneAt)
	}
	if ok, err := db.TouchScene(ctx, near, at.Add(-time.Minute)); err != nil || ok {
		t.Errorf("TouchScene with an older time = %v, %v; want false", ok, err)
	}
	if got := getScene(t, db, near); !got.TouchedAt.Equal(at) {
		t.Errorf("touched at %v after an older touch, want %v", got.TouchedAt, at)
	}
	if ok, err := db.TouchScene(ctx, sceneKey("20002"), at); err != nil || ok {
		t.Errorf("TouchScene without a row = %v, %v; want false", ok, err)
	}
}

func TestScene_EndIdleOnlyIfUntouched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := sceneKey("20001")
	t0, t1 := sceneAt, sceneAt.Add(time.Hour)
	inPerson(t, db, key, t0)
	if ok, err := db.TouchScene(ctx, key, t1); err != nil || !ok {
		t.Fatalf("TouchScene = %v, %v", ok, err)
	}

	if ok, err := db.EndIdleScene(ctx, key, t0, t0.Add(3*time.Hour)); err != nil || ok {
		t.Errorf("EndIdleScene of an older touch = %v, %v; want false", ok, err)
	}
	if got := getScene(t, db, key); got.Mode != conloomapi.SceneInPerson {
		t.Fatalf("scene after a stale EndIdleScene = %+v, want still in person", got)
	}
	since := t1.Add(3 * time.Hour)
	if ok, err := db.EndIdleScene(ctx, key, t1, since); err != nil || !ok {
		t.Errorf("EndIdleScene = %v, %v; want true", ok, err)
	}
	checkScene(t, getScene(t, db, key), store.ConversationScene{Mode: conloomapi.SceneOnline, SinceAt: since, TouchedAt: t1,
		PrevMode: conloomapi.SceneInPerson, PrevPlace: "食堂"})
	if ok, err := db.EndIdleScene(ctx, key, t1, since.Add(time.Minute)); err != nil || ok {
		t.Errorf("EndIdleScene again = %v, %v; want false", ok, err)
	}
	if got := getScene(t, db, key); !got.SinceAt.Equal(since) {
		t.Errorf("online since %v after a second EndIdleScene, want %v", got.SinceAt, since)
	}
}

func sceneRows(t *testing.T, db *DB, key string) int {
	t.Helper()
	var n int
	if err := db.r.QueryRow(`SELECT COUNT(*) FROM conversation_scene WHERE conversation_key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestForgetPrivate_ClearsScene(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := sceneKey("20001"), sceneKey("20002")
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	seedConversations(t, db, a, b)
	scope := store.MemoryScopeKey(a.Key)
	for _, k := range []string{key, other, scope} {
		inPerson(t, db, k, sceneAt)
	}

	if err := db.ForgetPrivate(ctx, key, "20001", sceneAt); err != nil {
		t.Fatalf("ForgetPrivate: %v", err)
	}
	if err := db.ForgetPrivateScope(ctx, scope, []string{a.Key, b.Key}, "20001", sceneAt); err != nil {
		t.Fatalf("ForgetPrivateScope: %v", err)
	}
	for _, k := range []string{key, scope} {
		if n := sceneRows(t, db, k); n != 0 {
			t.Errorf("scene rows of %s after forgetting = %d, want 0", k, n)
		}
	}
	if n := sceneRows(t, db, other); n != 1 {
		t.Errorf("scene rows of the other conversation = %d, want 1", n)
	}
}

func TestDeleteConversation_DeletesSceneSetInTheChat(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, b, c := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b"), panelChatConv(testBot, "panel_c")
	seedConversations(t, db, a, b, c)
	scope := store.MemoryScopeKey(a.Key)
	set := func(origin string) {
		t.Helper()
		setScene(t, db, store.ConversationScene{ConversationKey: scope, OriginConversationKey: origin,
			Mode: conloomapi.SceneInPerson, Place: "图书馆", SinceAt: sceneAt, TouchedAt: sceneAt})
	}

	set(b.Key)
	if err := db.DeleteConversation(ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetScene(ctx, scope); err != nil || got.Origin() != b.Key {
		t.Fatalf("scene after deleting A = %+v, %v; want the one set in B kept", got, err)
	}
	if err := db.DeleteConversation(ctx, b.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetScene(ctx, scope); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scene after deleting B, where it was set: %v, want ErrNotFound", err)
	}
}

func sceneRow(mode, place string, updated time.Time) string {
	return `INSERT INTO conversation_scene (conversation_key, mode, place, since_at, touched_at, prev_mode, prev_place, updated_at)
		VALUES (?1, '` + mode + `', '` + place + `', '` + fmtTime(updated) + `', '` + fmtTime(updated) + `', 'online', '', '` +
		fmtTime(updated) + `')`
}

func TestMigrateScope_KeepsNewestScene(t *testing.T) {
	db := newTestDB(t)
	a, b := panelChatConv(testBot, "panel_a"), panelChatConv(testBot, "panel_b")
	seedConversations(t, db, a, b)
	scope := store.MemoryScopeKey(a.Key)
	early, late := migrateAt.Add(-3*time.Hour), migrateAt.Add(-time.Hour)
	legacyRows(t, db, a.Key, sceneRow(conloomapi.SceneInPerson, "食堂", late))
	legacyRows(t, db, b.Key, sceneRow(conloomapi.SceneInPerson, "图书馆", early))

	merged, ran := migrateScope(t, db)
	if want := []store.ScopeMerge{{BotInstanceID: testBot, Chats: []string{a.Key, b.Key}}}; !ran || !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %+v, %v; want %+v", merged, ran, want)
	}
	got := getScene(t, db, scope)
	if got.Place != "食堂" || !got.UpdatedAt.Equal(late) || got.OriginConversationKey != a.Key || got.Origin() != a.Key {
		t.Errorf("scope scene = %+v, want A's (updated last) set in A", got)
	}
	for _, chat := range []string{a.Key, b.Key} {
		if n := sceneRows(t, db, chat); n != 0 {
			t.Errorf("scene rows left under %s = %d", chat, n)
		}
	}
}

func TestMigration0022_AddsSceneTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	beforeCols := tableInfos(t, old.r)
	until := fmtTime(migrateAt)
	if _, err := old.w.Exec(`INSERT INTO conversation_status (conversation_key, activity, started_at, until_at, task_id, updated_at)
		VALUES (?, '洗澡', ?, ?, 'task_seed', ?)`, testPrivateKey, until, until, until); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, 22))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	afterCols := tableInfos(t, db.r)
	for table, cols := range beforeCols {
		if !slices.Equal(afterCols[table], cols) {
			t.Errorf("%s columns %v, want %v", table, afterCols[table], cols)
		}
	}
	want := []string{"0|conversation_key|TEXT|0||1", "1|mode|TEXT|1||0", "2|place|TEXT|1|''|0", "3|since_at|TEXT|1||0",
		"4|touched_at|TEXT|1||0", "5|prev_mode|TEXT|1|''|0", "6|prev_place|TEXT|1|''|0", "7|updated_at|TEXT|1||0",
		"8|origin_conversation_key|TEXT|1|''|0", "9|switch_task_id|TEXT|1|''|0"}
	if !slices.Equal(afterCols["conversation_scene"], want) {
		t.Errorf("conversation_scene columns %v, want %v", afterCols["conversation_scene"], want)
	}
	if st, err := db.GetStatus(ctx, testPrivateKey); err != nil || st.Activity != "洗澡" {
		t.Fatalf("status kept from before = %+v, %v", st, err)
	}
	if _, err := db.GetScene(ctx, testPrivateKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scene after 0022 = %v, want ErrNotFound", err)
	}
}
