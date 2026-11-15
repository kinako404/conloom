package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestMigrateScope_OnceAtStartup(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t, configParams{modelURL: "http://127.0.0.1:1"})
	bot := cfg.Bots[0].ID
	db := openDB(t, cfg)
	var chats []string
	for i, id := range []string{"panel_a", "panel_b"} {
		key := store.ConversationKey(bot, store.PlatformPanel, conloomapi.ConvPrivate, id)
		chats = append(chats, key)
		if _, err := db.Ingest(ctx, store.Ingest{
			Event: store.EventRecord{BotInstanceID: bot, IdempotencyKey: id, EventID: "evt_" + id, Response: []byte("{}"),
				RawEvent: []byte("{}"), CreatedAt: time.Now()},
			Conversation: &store.ConversationRecord{BotInstanceID: bot, ID: id, Key: key, Type: conloomapi.ConvPrivate,
				Platform: store.PlatformPanel, PlatformConversationID: id},
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: key, Summary: "聊了钢琴", CursorSeq: int64(i + 1),
			LastRunAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	audits := func() []store.AuditEvent {
		t.Helper()
		db := openDB(t, cfg)
		defer db.Close()
		events, err := db.ListAudit(ctx, store.AuditFilter{Kind: "memory.scope.migrate", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return events
	}
	logs, err := startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	if n := logLines(logs, "INFO", `msg="panel chat memory merged into the bot's shared scope"`, "bot="+bot, "chats=2"); n != 1 {
		t.Errorf("merge log lines = %d, want one for the bot:\n%s", n, logs)
	}
	got := audits()
	if len(got) != 1 || got[0].Actor != "system" || !strings.Contains(got[0].Detail, `{"bot":"`+bot+`","chats":2}`) {
		t.Fatalf("migration audits = %+v, want one naming the bot and its 2 chats", got)
	}
	db = openDB(t, cfg)
	scope := store.MemoryScopeKey(chats[0])
	if g, err := db.GetGroupMemory(ctx, scope); err != nil || g.Summary != "聊了钢琴" || g.CursorSeq != 2 {
		t.Errorf("scope memory = %+v, %v", g, err)
	}
	if _, ok, err := db.GetMeta(ctx, store.MetaPanelMemoryScoped); err != nil || !ok {
		t.Errorf("migration marker = %v, %v; want it set", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	logs, err = startAndClose(t, cfg)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if strings.Contains(logs.String(), "memory merged") {
		t.Errorf("second start merged again:\n%s", logs)
	}
	if got := audits(); len(got) != 1 {
		t.Errorf("migration audits after the second start = %d, want 1", len(got))
	}
}
