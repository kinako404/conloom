package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	testBot      = "bot_test"
	testPlatform = "qq"
	testGroup    = "10001"
	testConvID   = "conv_10001"
)

var testConvKey = store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, testConvID)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countRows(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	if err := db.r.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// testIngest builds an ingest of one group message from user 20001.
func testIngest(key, msgID, platformMsgID string) store.Ingest {
	now := time.Now().UTC()
	return store.Ingest{
		Event: store.EventRecord{
			BotInstanceID:  testBot,
			IdempotencyKey: key,
			RequestHash:    "hash-" + key,
			EventID:        "evt_" + key,
			Response:       []byte(`{"event_id":"evt_` + key + `"}`),
			RawEvent:       []byte(`{"raw":true}`),
			CreatedAt:      now,
		},
		Conversation: &store.ConversationRecord{
			BotInstanceID:          testBot,
			ID:                     testConvID,
			Key:                    testConvKey,
			Type:                   conloomapi.ConvGroup,
			Platform:               testPlatform,
			PlatformConversationID: testGroup,
		},
		User: &store.UserRecord{
			BotInstanceID:  testBot,
			Platform:       testPlatform,
			PlatformUserID: "20001",
			DisplayName:    "Alice",
		},
		Message: &store.MessageRecord{
			ID:                msgID,
			BotInstanceID:     testBot,
			ConversationKey:   testConvKey,
			Platform:          testPlatform,
			PlatformMessageID: platformMsgID,
			ActorID:           "usr_20001",
			PlatformUserID:    "20001",
			DisplayName:       "Alice",
			Text:              "hello " + msgID,
			OccurredAt:        now,
			CreatedAt:         now,
		},
	}
}

func TestOpen_MigratesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conloom.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db.Close()
	if n := countRows(t, db, "schema_migrations"); n != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", n)
	}
	var mode string
	if err := db.r.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
}

func TestUpsertBotInstance(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.UpsertBotInstance(ctx, testBot, testPlatform, "old"); err != nil {
		t.Fatalf("UpsertBotInstance: %v", err)
	}
	if err := db.UpsertBotInstance(ctx, testBot, testPlatform, "new"); err != nil {
		t.Fatalf("UpsertBotInstance again: %v", err)
	}
	var name string
	if err := db.r.QueryRow(`SELECT name FROM bot_instances WHERE id = ?`, testBot).Scan(&name); err != nil || name != "new" {
		t.Fatalf("name = %q, %v; want new", name, err)
	}
	if n := countRows(t, db, "bot_instances"); n != 1 {
		t.Fatalf("bot_instances = %d, want 1", n)
	}
}

func TestIngest_StoresEventMessageTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	in := testIngest("k1", "msg_1", "p1")
	in.Message.ReplyTo = "msg_0"
	in.Message.Mentions = []conloomapi.Mention{{Type: "bot", ID: "30001"}}
	in.Message.Attachments = []conloomapi.Attachment{{Type: "image", URL: "https://example.invalid/a.png", Size: 12}}
	in.NewTask = &store.TaskRecord{
		ID:                  "task_1",
		BotInstanceID:       testBot,
		ConversationKey:     testConvKey,
		Status:              conloomapi.TaskQueued,
		ActorPlatformUserID: "20001",
		Command:             "help",
		TriggerMessageIDs:   []string{"msg_1"},
		Priority:            2,
		Meta:                conloomapi.TaskMeta{TraceID: "trace_1"},
		EventOccurredAt:     in.Message.OccurredAt,
		CreatedAt:           in.Message.CreatedAt,
		UpdatedAt:           in.Message.CreatedAt,
	}
	existing, err := db.Ingest(ctx, in)
	if err != nil || existing != nil {
		t.Fatalf("Ingest = %v, %v; want nil, nil", existing, err)
	}

	ev, err := db.GetEvent(ctx, testBot, "k1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if ev.EventID != "evt_k1" || ev.RequestHash != "hash-k1" || string(ev.Response) != string(in.Event.Response) ||
		string(ev.RawEvent) != `{"raw":true}` || !ev.CreatedAt.Equal(in.Event.CreatedAt) {
		t.Fatalf("GetEvent = %+v", ev)
	}

	conv, err := db.GetConversation(ctx, testBot, testConvID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if conv.Key != store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, testConvID) ||
		conv.PlatformConversationID != testGroup || conv.Type != conloomapi.ConvGroup || conv.Platform != testPlatform {
		t.Fatalf("GetConversation = %+v", conv)
	}

	msg, err := db.GetMessage(ctx, testBot, "msg_1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	want := *in.Message
	want.Seq = msg.Seq
	if msg.Seq == 0 || !msg.OccurredAt.Equal(want.OccurredAt) || !msg.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("GetMessage seq/times = %+v", msg)
	}
	msg.OccurredAt, msg.CreatedAt = want.OccurredAt, want.CreatedAt
	if !reflect.DeepEqual(msg, want) {
		t.Fatalf("GetMessage = %+v\nwant %+v", msg, want)
	}

	task, err := db.GetTask(ctx, testBot, "task_1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != conloomapi.TaskQueued || task.Command != "help" || task.Priority != 2 ||
		task.ActorPlatformUserID != "20001" || task.Meta.TraceID != "trace_1" ||
		!reflect.DeepEqual(task.TriggerMessageIDs, []string{"msg_1"}) || !task.EventOccurredAt.Equal(in.NewTask.EventOccurredAt) {
		t.Fatalf("GetTask = %+v", task)
	}

	if _, err := db.GetTask(ctx, "other_bot", "task_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetTask other bot err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetMessage(ctx, testBot, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetMessage missing err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetEvent(ctx, testBot, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetEvent missing err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetConversation(ctx, testBot, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetConversation missing err = %v, want ErrNotFound", err)
	}
}

func TestIngest_SameKeyReturnsExistingWithoutWrites(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := testIngest("k1", "msg_1", "p1")
	if _, err := db.Ingest(ctx, first); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	second := testIngest("k1", "msg_2", "p2")
	second.Event.Response = []byte(`{"different":true}`)
	existing, err := db.Ingest(ctx, second)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if existing == nil || string(existing.Response) != string(first.Event.Response) {
		t.Fatalf("existing = %+v, want response %s", existing, first.Event.Response)
	}
	if n := countRows(t, db, "messages"); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	if n := countRows(t, db, "events"); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}

func TestIngest_DuplicatePlatformMessageDifferentKey(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Ingest(ctx, testIngest("k1", "msg_1", "p1")); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	_, err := db.Ingest(ctx, testIngest("k2", "msg_2", "p1"))
	if !errors.Is(err, store.ErrDuplicateMessage) {
		t.Fatalf("err = %v, want ErrDuplicateMessage", err)
	}
	if n := countRows(t, db, "events"); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	if _, err := db.Ingest(ctx, testIngest("k3", "msg_3", "")); err != nil {
		t.Fatalf("empty platform id Ingest: %v", err)
	}
	if _, err := db.Ingest(ctx, testIngest("k4", "msg_4", "")); err != nil {
		t.Fatalf("second empty platform id Ingest: %v", err)
	}
}

func TestIngest_MergeAppendsTrigger(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	first := testIngest("k1", "m1", "p1")
	first.NewTask = &store.TaskRecord{
		ID: "task_1", BotInstanceID: testBot, ConversationKey: testConvKey,
		Status: conloomapi.TaskQueued, TriggerMessageIDs: []string{"m1"},
	}
	if _, err := db.Ingest(ctx, first); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	second := testIngest("k2", "m2", "p2")
	second.MergeIntoTaskID = "task_1"
	if _, err := db.Ingest(ctx, second); err != nil {
		t.Fatalf("merge Ingest: %v", err)
	}
	task, err := db.GetTask(ctx, testBot, "task_1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !reflect.DeepEqual(task.TriggerMessageIDs, []string{"m1", "m2"}) {
		t.Fatalf("TriggerMessageIDs = %v, want [m1 m2]", task.TriggerMessageIDs)
	}

	third := testIngest("k3", "m3", "p3")
	third.MergeIntoTaskID = "task_missing"
	if _, err := db.Ingest(ctx, third); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("merge into missing task err = %v, want ErrNotFound", err)
	}
	if n := countRows(t, db, "events"); n != 2 {
		t.Fatalf("events = %d, want 2 after rolled back merge", n)
	}
}

func TestIngest_UpdatesDisplayName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if _, err := db.Ingest(ctx, testIngest("k1", "m1", "p1")); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	in := testIngest("k2", "m2", "p2")
	in.User.DisplayName = "Alice2"
	if _, err := db.Ingest(ctx, in); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if n := countRows(t, db, "users"); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	var name string
	err := db.r.QueryRow(`SELECT display_name FROM users WHERE bot_instance_id = ? AND platform = ? AND platform_user_id = ?`,
		testBot, testPlatform, "20001").Scan(&name)
	if err != nil || name != "Alice2" {
		t.Fatalf("display_name = %q, %v; want Alice2", name, err)
	}
}

func TestRecentMessages_LimitAndOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		in := testIngest(fmt.Sprintf("k%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("p%d", i))
		if _, err := db.Ingest(ctx, in); err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}
	msgs, err := db.RecentMessages(ctx, testConvKey, 3)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	var got []string
	for i, m := range msgs {
		got = append(got, m.ID)
		if i > 0 && m.Seq <= msgs[i-1].Seq {
			t.Fatalf("seq not ascending: %d after %d", m.Seq, msgs[i-1].Seq)
		}
	}
	if !reflect.DeepEqual(got, []string{"m3", "m4", "m5"}) {
		t.Fatalf("RecentMessages = %v, want [m3 m4 m5]", got)
	}

	byID, err := db.GetMessages(ctx, testBot, []string{"m4", "m1", "missing"})
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(byID) != 2 || byID[0].ID != "m1" || byID[1].ID != "m4" {
		t.Fatalf("GetMessages = %+v, want [m1 m4]", byID)
	}
}

func TestPurgeEvents(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := testIngest("old", "m1", "p1")
	old.Event.CreatedAt = now.Add(-48 * time.Hour)
	fresh := testIngest("fresh", "m2", "p2")
	fresh.Event.CreatedAt = now
	for _, in := range []store.Ingest{old, fresh} {
		if _, err := db.Ingest(ctx, in); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	n, err := db.PurgeEvents(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeEvents = %d, %v; want 1", n, err)
	}
	if _, err := db.GetEvent(ctx, testBot, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old event err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetEvent(ctx, testBot, "fresh"); err != nil {
		t.Fatalf("fresh event: %v", err)
	}
	if n := countRows(t, db, "messages"); n != 2 {
		t.Fatalf("messages = %d, want 2 (purge only touches events)", n)
	}
}

func TestConcurrentIngest_NoBusyErrors(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 50*20)
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				id := fmt.Sprintf("%d_%d", g, i)
				if _, err := db.Ingest(ctx, testIngest("k"+id, "m"+id, "p"+id)); err != nil {
					errs <- err
					return
				}
				if _, err := db.RecentMessages(ctx, testConvKey, 5); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Ingest: %v", err)
	}
	if n := countRows(t, db, "messages"); n != 1000 {
		t.Fatalf("messages = %d, want 1000", n)
	}
}
