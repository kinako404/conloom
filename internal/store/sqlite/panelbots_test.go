package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	panelBotA = "panel-k3x9q2ma"
	panelBotB = "panel-b7n4w1zc"
)

func createPanelBot(t *testing.T, db *DB, b store.PanelBot) {
	t.Helper()
	if err := db.CreatePanelBot(context.Background(), b); err != nil {
		t.Fatalf("CreatePanelBot %s: %v", b.ID, err)
	}
}

func listPanelBots(t *testing.T, db *DB) []store.PanelBot {
	t.Helper()
	bots, err := db.ListPanelBots(context.Background())
	if err != nil {
		t.Fatalf("ListPanelBots: %v", err)
	}
	return bots
}

func botInstances(t *testing.T, db *DB) []store.BotInstance {
	t.Helper()
	insts, err := db.ListBotInstances(context.Background())
	if err != nil {
		t.Fatalf("ListBotInstances: %v", err)
	}
	return insts
}

func rowsOfBot(t *testing.T, db *DB, table, botID string) int {
	t.Helper()
	var n int
	if err := db.r.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE bot_instance_id = ?`, botID).Scan(&n); err != nil {
		t.Fatalf("count %s of %s: %v", table, botID, err)
	}
	return n
}

// createBotChat stores a private chat of botID with its user and no messages.
func createBotChat(t *testing.T, db *DB, botID, platform, convID, uid string) string {
	t.Helper()
	ctx := context.Background()
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		if err := upsertConversation(ctx, tx, store.ConversationRecord{
			BotInstanceID: botID, ID: convID, Type: conloomapi.ConvPrivate, Platform: platform, PlatformConversationID: convID,
		}); err != nil {
			return err
		}
		return upsertUser(ctx, tx, store.UserRecord{BotInstanceID: botID, Platform: platform, PlatformUserID: uid, DisplayName: "小明"})
	})
	if err != nil {
		t.Fatalf("create chat %s of %s: %v", convID, botID, err)
	}
	return store.ConversationKey(botID, platform, conloomapi.ConvPrivate, convID)
}

// seedLeftovers leaves botID a user without a chat, an ignored event, a model
// call and an audit event.
func seedLeftovers(t *testing.T, db *DB, botID, platform string) {
	t.Helper()
	ctx := context.Background()
	key := createBotChat(t, db, botID, platform, "chat_"+botID, "20001")
	if err := db.DeleteConversation(ctx, key); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := db.Ingest(ctx, store.Ingest{Event: store.EventRecord{
		BotInstanceID: botID, IdempotencyKey: "ignored_" + botID, RequestHash: "hash", EventID: "evt_ignored",
		Response: []byte(`{"disposition":"ignored"}`), RawEvent: []byte{},
	}}); err != nil {
		t.Fatalf("Ingest ignored event: %v", err)
	}
	if err := db.InsertModelCalls(ctx, []store.ModelCall{{
		At: time.Now(), BotInstanceID: botID, ConversationKey: key, Purpose: store.PurposeChat,
		Role: "chat", Model: "model-test", Outcome: store.OutcomeOK,
	}}); err != nil {
		t.Fatalf("InsertModelCalls: %v", err)
	}
	if err := db.InsertAudit(ctx, store.AuditEvent{
		ID: "aud_" + botID, BotInstanceID: botID, Kind: "panel.chat.delete", Actor: "panel:admin", Target: key, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	for _, table := range []string{"users", "events", "model_calls", "audit_events"} {
		if n := rowsOfBot(t, db, table, botID); n != 1 {
			t.Fatalf("seeded %s rows of %s = %d, want 1", table, botID, n)
		}
	}
}

func TestPanelBots_CRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.UpsertBotInstance(ctx, "qq-main", "qq", "小红"); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 11, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	a := store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default", Skills: []string{"recall"},
		CreatedBy: "admin", CreatedAt: created, UpdatedAt: created}
	b := store.PanelBot{ID: panelBotB, Name: "小红", PersonaTemplate: "default", Skills: []string{},
		CreatedBy: "admin", CreatedAt: created.Add(-time.Minute), UpdatedAt: created.Add(-time.Minute)}
	createPanelBot(t, db, a)
	createPanelBot(t, db, b)

	got := listPanelBots(t, db)
	if len(got) != 2 || got[0].ID != panelBotB || got[1].ID != panelBotA {
		t.Fatalf("ListPanelBots = %+v, want %s then %s by created_at", got, panelBotB, panelBotA)
	}
	if !got[1].CreatedAt.Equal(created) || !got[1].UpdatedAt.Equal(created) {
		t.Fatalf("times = %v / %v, want %v", got[1].CreatedAt, got[1].UpdatedAt, created)
	}
	got[1].CreatedAt, got[1].UpdatedAt = a.CreatedAt, a.UpdatedAt
	if !reflect.DeepEqual(got[1], a) {
		t.Fatalf("bot = %+v, want %+v", got[1], a)
	}
	wantInsts := []store.BotInstance{
		{ID: panelBotB, Platform: store.PlatformPanel, Name: "小红"},
		{ID: panelBotA, Platform: store.PlatformPanel, Name: "小明"},
		{ID: "qq-main", Platform: "qq", Name: "小红"},
	}
	if insts := botInstances(t, db); !reflect.DeepEqual(insts, wantInsts) {
		t.Fatalf("ListBotInstances = %+v, want %+v", insts, wantInsts)
	}

	for _, id := range []string{panelBotA, "qq-main"} {
		dup := store.PanelBot{ID: id, Name: "小刚", PersonaTemplate: "default", CreatedBy: "admin"}
		if err := db.CreatePanelBot(ctx, dup); err == nil {
			t.Fatalf("CreatePanelBot duplicate %s succeeded", id)
		}
	}
	if n := countRows(t, db, "panel_bots"); n != 2 {
		t.Fatalf("panel_bots rows after duplicates = %d, want 2", n)
	}
	if insts := botInstances(t, db); !reflect.DeepEqual(insts, wantInsts) {
		t.Fatalf("ListBotInstances after duplicates = %+v, want %+v", insts, wantInsts)
	}

	updated := created.Add(time.Hour)
	if err := db.UpdatePanelBot(ctx, store.PanelBot{ID: panelBotA, Name: "小刚", PersonaTemplate: "teacher",
		Skills: []string{"recall", "remember"}, UpdatedAt: updated}); err != nil {
		t.Fatalf("UpdatePanelBot: %v", err)
	}
	got = listPanelBots(t, db)
	want := store.PanelBot{ID: panelBotA, Name: "小刚", PersonaTemplate: "teacher", Skills: []string{"recall", "remember"},
		CreatedBy: "admin", CreatedAt: created, UpdatedAt: updated}
	if len(got) != 2 || !got[1].CreatedAt.Equal(want.CreatedAt) || !got[1].UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("ListPanelBots after update = %+v, want %+v second", got, want)
	}
	got[1].CreatedAt, got[1].UpdatedAt = want.CreatedAt, want.UpdatedAt
	if !reflect.DeepEqual(got[1], want) {
		t.Fatalf("updated bot = %+v, want %+v", got[1], want)
	}
	if got[0].Name != "小红" {
		t.Fatalf("other bot name = %q, want 小红", got[0].Name)
	}
	wantInsts[1].Name = "小刚"
	if insts := botInstances(t, db); !reflect.DeepEqual(insts, wantInsts) {
		t.Fatalf("ListBotInstances after update = %+v, want %+v", insts, wantInsts)
	}

	for _, id := range []string{"panel-zzzz0000", "qq-main"} {
		err := db.UpdatePanelBot(ctx, store.PanelBot{ID: id, Name: "小刚", PersonaTemplate: "default"})
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdatePanelBot %s err = %v, want ErrNotFound", id, err)
		}
	}
	if insts := botInstances(t, db); !reflect.DeepEqual(insts, wantInsts) {
		t.Fatalf("ListBotInstances after missing updates = %+v, want %+v", insts, wantInsts)
	}
}

func TestPanelBots_DeleteRefusesWithConversations(t *testing.T) {
	ctx := context.Background()
	for name, seed := range map[string]func(t *testing.T, db *DB){
		"conversation": func(t *testing.T, db *DB) {
			createBotChat(t, db, panelBotA, store.PlatformPanel, "chat_a", "panel_admin")
		},
		"task": func(t *testing.T, db *DB) {
			if _, err := db.w.Exec(`INSERT INTO tasks (id, bot_instance_id, conversation_key, status, actor_platform_user_id,
				command, trigger_message_ids, priority, meta, error_code, event_occurred_at, created_at, updated_at)
				VALUES ('task_a', ?, 'gone', 'running', 'panel_admin', '', '[]', 0, '{}', '', '', '', '')`, panelBotA); err != nil {
				t.Fatal(err)
			}
		},
		"outbox action": func(t *testing.T, db *DB) {
			if _, err := db.w.Exec(`INSERT INTO outbox_actions (action_id, bot_instance_id, conversation_key, task_id, platform,
				status, payload, lease_until, attempts, platform_message_id, created_at, expires_at, acked_at)
				VALUES ('act_a', ?, 'gone', 'task_a', 'panel', 'leased', '{}', '', 1, '', '', '', '')`, panelBotA); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			createPanelBot(t, db, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default", CreatedBy: "admin"})
			seedLeftovers(t, db, panelBotA, store.PlatformPanel)
			seed(t, db)
			if err := db.DeletePanelBot(ctx, panelBotA); !errors.Is(err, store.ErrBotHasData) {
				t.Fatalf("DeletePanelBot err = %v, want ErrBotHasData", err)
			}
			if bots := listPanelBots(t, db); len(bots) != 1 || bots[0].ID != panelBotA {
				t.Fatalf("ListPanelBots = %+v, want the bot kept", bots)
			}
			for _, table := range []string{"users", "events"} {
				if n := rowsOfBot(t, db, table, panelBotA); n == 0 {
					t.Fatalf("%s rows of the bot deleted by a refused delete", table)
				}
			}
		})
	}
}

func TestPanelBots_DeleteSweepsLeftovers(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.UpsertBotInstance(ctx, "qq-main", "qq", "小红"); err != nil {
		t.Fatal(err)
	}
	createPanelBot(t, db, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default", CreatedBy: "admin"})
	createPanelBot(t, db, store.PanelBot{ID: panelBotB, Name: "小红", PersonaTemplate: "default", CreatedBy: "admin"})
	for _, bot := range []struct{ id, platform string }{
		{panelBotA, store.PlatformPanel}, {panelBotB, store.PlatformPanel}, {"qq-main", "qq"},
	} {
		seedLeftovers(t, db, bot.id, bot.platform)
	}
	createBotChat(t, db, panelBotB, store.PlatformPanel, "chat_b", "panel_admin")

	if err := db.DeletePanelBot(ctx, panelBotA); err != nil {
		t.Fatalf("DeletePanelBot: %v", err)
	}
	for table, want := range map[string]int{"users": 0, "events": 0, "model_calls": 1, "audit_events": 1} {
		if n := rowsOfBot(t, db, table, panelBotA); n != want {
			t.Errorf("%s rows of the deleted bot = %d, want %d", table, n, want)
		}
	}
	for _, id := range []string{panelBotB, "qq-main"} {
		for _, table := range []string{"users", "events", "model_calls", "audit_events"} {
			if n := rowsOfBot(t, db, table, id); n == 0 {
				t.Errorf("%s rows of %s deleted", table, id)
			}
		}
	}
	if n := rowsOfBot(t, db, "conversations", panelBotB); n != 1 {
		t.Errorf("conversations of %s = %d, want 1", panelBotB, n)
	}
	if bots := listPanelBots(t, db); len(bots) != 1 || bots[0].ID != panelBotB {
		t.Fatalf("ListPanelBots = %+v, want only %s", bots, panelBotB)
	}
	wantInsts := []store.BotInstance{
		{ID: panelBotB, Platform: store.PlatformPanel, Name: "小红"},
		{ID: panelBotA, Platform: store.PlatformPanel, Name: "小明"},
		{ID: "qq-main", Platform: "qq", Name: "小红"},
	}
	if insts := botInstances(t, db); !reflect.DeepEqual(insts, wantInsts) {
		t.Fatalf("ListBotInstances = %+v, want %+v", insts, wantInsts)
	}

	for _, id := range []string{panelBotA, "qq-main"} {
		if err := db.DeletePanelBot(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("DeletePanelBot %s err = %v, want ErrNotFound", id, err)
		}
	}
	for _, table := range []string{"users", "events"} {
		if n := rowsOfBot(t, db, table, "qq-main"); n != 1 {
			t.Errorf("%s rows of the config bot = %d after a refused delete, want 1", table, n)
		}
	}
	if err := db.CreatePanelBot(ctx, store.PanelBot{ID: panelBotA, Name: "小刚", PersonaTemplate: "default",
		CreatedBy: "admin"}); err == nil {
		t.Fatal("CreatePanelBot reused the deleted bot's ID")
	}
}

func TestPanelBots_SkillsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	rawSkills := func() string {
		t.Helper()
		var s string
		if err := db.r.QueryRow(`SELECT skills FROM panel_bots WHERE id = ?`, panelBotA).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	createPanelBot(t, db, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default", CreatedBy: "admin"})
	if s := rawSkills(); s != "[]" {
		t.Fatalf("stored nil skills = %q, want []", s)
	}
	if bots := listPanelBots(t, db); len(bots) != 1 || len(bots[0].Skills) != 0 {
		t.Fatalf("ListPanelBots = %+v, want one bot without skills", bots)
	}

	skills := []string{"remember", "recall"}
	if err := db.UpdatePanelBot(ctx, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default",
		Skills: skills}); err != nil {
		t.Fatal(err)
	}
	if bots := listPanelBots(t, db); !slices.Equal(bots[0].Skills, skills) {
		t.Fatalf("skills = %v, want %v", bots[0].Skills, skills)
	}

	if err := db.UpdatePanelBot(ctx, store.PanelBot{ID: panelBotA, Name: "小明", PersonaTemplate: "default"}); err != nil {
		t.Fatal(err)
	}
	if s := rawSkills(); s != "[]" {
		t.Fatalf("stored nil skills after update = %q, want []", s)
	}
}

func TestMigration0013_OnlyAddsTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	before, beforeObjects := tableInfos(t, old.r), schemaObjects(t, old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, 13))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	after, afterObjects := tableInfos(t, db.r), schemaObjects(t, db)
	for table, cols := range before {
		if !reflect.DeepEqual(after[table], cols) {
			t.Errorf("table %s changed:\n before %v\n after  %v", table, cols, after[table])
		}
	}
	var added []string
	for name, def := range afterObjects {
		prev, ok := beforeObjects[name]
		if !ok {
			added = append(added, name)
		} else if prev != def {
			t.Errorf("%s changed:\n before %s\n after  %s", name, prev, def)
		}
	}
	slices.Sort(added)
	want := []string{"panel_bots", "sqlite_autoindex_panel_bots_1"}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	if len(beforeObjects)+len(want) != len(afterObjects) {
		t.Fatalf("schema objects %d -> %d, want only additions", len(beforeObjects), len(afterObjects))
	}
	wantCols := []string{"0|id|TEXT|0||1", "1|name|TEXT|1||0", "2|persona_template|TEXT|1||0", "3|skills|TEXT|1||0",
		"4|created_by|TEXT|1||0", "5|created_at|TEXT|1||0", "6|updated_at|TEXT|1||0"}
	if !slices.Equal(after["panel_bots"], wantCols) {
		t.Fatalf("panel_bots columns %v, want %v", after["panel_bots"], wantCols)
	}
}
