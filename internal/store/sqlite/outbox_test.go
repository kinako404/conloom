package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var testConvKeyB = store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, "conv_10002")

func convAction(id, convKey string, now time.Time) store.OutboxRecord {
	a := testAction(id, now)
	a.ConversationKey = convKey
	return a
}

func insertActions(t *testing.T, db *DB, recs ...store.OutboxRecord) {
	t.Helper()
	ctx := context.Background()
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		for _, r := range recs {
			if err := insertOutbox(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("insert actions: %v", err)
	}
}

func lease(t *testing.T, db *DB, limit int, now time.Time) []store.OutboxRecord {
	t.Helper()
	recs, err := db.LeaseOutbox(context.Background(), testBot, limit, now, time.Minute)
	if err != nil {
		t.Fatalf("LeaseOutbox: %v", err)
	}
	return recs
}

func actionIDs(recs []store.OutboxRecord) []string {
	ids := []string{}
	for _, r := range recs {
		ids = append(ids, r.Action.ActionID)
	}
	return ids
}

func ack(t *testing.T, db *DB, actionID string, botMsg *store.MessageRecord, now time.Time) bool {
	t.Helper()
	already, err := db.AckOutbox(context.Background(), testBot, actionID,
		conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p_" + actionID}, botMsg, now)
	if err != nil {
		t.Fatalf("AckOutbox %s: %v", actionID, err)
	}
	return already
}

func outboxStatus(t *testing.T, db *DB, actionID string) (string, int) {
	t.Helper()
	o, err := db.GetOutbox(context.Background(), testBot, actionID)
	if err != nil {
		t.Fatalf("GetOutbox %s: %v", actionID, err)
	}
	return o.Status, o.Attempts
}

func TestLeaseOutbox_OnePerConversationInOrder(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	a1 := convAction("a1", testConvKey, now)
	insertActions(t, db, a1, convAction("a2", testConvKey, now), convAction("b1", testConvKeyB, now))

	got := lease(t, db, 10, now)
	if ids := actionIDs(got); !reflect.DeepEqual(ids, []string{"a1", "b1"}) {
		t.Fatalf("first lease = %v, want [a1 b1]", ids)
	}
	r := got[0]
	if r.Status != store.OutboxLeased || r.Attempts != 1 || !r.LeaseUntil.Equal(now.Add(time.Minute)) ||
		r.Platform != testPlatform || r.BotInstanceID != testBot || r.ConversationKey != testConvKey ||
		r.TaskID != "task_1" || r.Seq == 0 || !r.ExpiresAt.Equal(a1.ExpiresAt) || !r.CreatedAt.Equal(a1.CreatedAt) {
		t.Fatalf("leased record = %+v", r)
	}
	if !r.Action.CreatedAt.Equal(a1.Action.CreatedAt) {
		t.Fatalf("action CreatedAt = %v, want %v", r.Action.CreatedAt, a1.Action.CreatedAt)
	}
	r.Action.CreatedAt = a1.Action.CreatedAt
	if !reflect.DeepEqual(r.Action, a1.Action) {
		t.Fatalf("action = %+v\nwant %+v", r.Action, a1.Action)
	}

	stored, err := db.GetOutbox(context.Background(), testBot, "a1")
	if err != nil {
		t.Fatalf("GetOutbox: %v", err)
	}
	if stored.Status != store.OutboxLeased || stored.Attempts != 1 || !stored.LeaseUntil.Equal(r.LeaseUntil) ||
		stored.Platform != testPlatform || stored.Seq != r.Seq {
		t.Fatalf("stored = %+v", stored)
	}

	if ids := actionIDs(lease(t, db, 10, now)); len(ids) != 0 {
		t.Fatalf("lease while heads leased = %v, want none", ids)
	}
	ack(t, db, "a1", nil, now)
	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a2"}) {
		t.Fatalf("lease after ack a1 = %v, want [a2]", ids)
	}
}

func TestLeaseOutbox_Limit(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	keyC := store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, "conv_10003")
	insertActions(t, db, convAction("a1", testConvKey, now), convAction("b1", testConvKeyB, now), convAction("c1", keyC, now))
	if ids := actionIDs(lease(t, db, 2, now)); !reflect.DeepEqual(ids, []string{"a1", "b1"}) {
		t.Fatalf("lease limit 2 = %v, want [a1 b1]", ids)
	}
	if ids := actionIDs(lease(t, db, 2, now)); !reflect.DeepEqual(ids, []string{"c1"}) {
		t.Fatalf("second lease = %v, want [c1]", ids)
	}
}

func TestLeaseOutbox_LeasedNotRedeliveredUntilExpiry(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now), convAction("a2", testConvKey, now))

	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a1"}) {
		t.Fatalf("first lease = %v, want [a1]", ids)
	}
	if ids := actionIDs(lease(t, db, 10, now.Add(30*time.Second))); len(ids) != 0 {
		t.Fatalf("lease within lease period = %v, want none", ids)
	}
	got := lease(t, db, 10, now.Add(61*time.Second))
	if ids := actionIDs(got); !reflect.DeepEqual(ids, []string{"a1"}) {
		t.Fatalf("lease after lease expiry = %v, want [a1]", ids)
	}
	if got[0].Attempts != 2 || !got[0].LeaseUntil.Equal(now.Add(121*time.Second)) {
		t.Fatalf("re-leased = attempts %d, lease_until %v", got[0].Attempts, got[0].LeaseUntil)
	}
	if status, attempts := outboxStatus(t, db, "a2"); status != store.OutboxPending || attempts != 0 {
		t.Fatalf("a2 = %s/%d, want pending/0", status, attempts)
	}
}

func TestLeaseOutbox_SkipsExpiredTTL(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	stale := convAction("a1", testConvKey, now)
	stale.ExpiresAt = now.Add(-time.Second)
	staleB := convAction("b1", testConvKeyB, now)
	staleB.ExpiresAt = now
	insertActions(t, db, stale, convAction("a2", testConvKey, now), staleB)

	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a2"}) {
		t.Fatalf("lease = %v, want [a2]", ids)
	}
	for _, id := range []string{"a1", "b1"} {
		if status, attempts := outboxStatus(t, db, id); status != store.OutboxPending || attempts != 0 {
			t.Fatalf("%s = %s/%d, want untouched pending/0", id, status, attempts)
		}
	}
}

func TestLeaseOutbox_OtherBotNotLeased(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now))
	got, err := db.LeaseOutbox(context.Background(), "bot_other", 10, now, time.Minute)
	if err != nil || len(got) != 0 {
		t.Fatalf("other bot lease = %v, %v; want none", actionIDs(got), err)
	}
}

func TestAckOutbox_IdempotentAndInsertsBotMessage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now))
	lease(t, db, 10, now)

	botMsg := &store.MessageRecord{
		ID:                "msg_p_a1",
		BotInstanceID:     testBot,
		ConversationKey:   testConvKey,
		Platform:          testPlatform,
		PlatformMessageID: "p_a1",
		ActorID:           "bot",
		PlatformUserID:    "30001",
		Text:              "hi there",
		FromBot:           true,
		OccurredAt:        now,
		CreatedAt:         now,
	}
	ackAt := now.Add(2 * time.Second)
	if already := ack(t, db, "a1", botMsg, ackAt); already {
		t.Fatal("first ack alreadyAcked = true")
	}
	msg, err := db.GetMessage(ctx, testBot, "msg_p_a1")
	if err != nil || !msg.FromBot || msg.Text != "hi there" {
		t.Fatalf("bot message = %+v, %v", msg, err)
	}
	o, err := db.GetOutbox(ctx, testBot, "a1")
	if err != nil || o.Status != store.OutboxAcked || o.PlatformMessageID != "p_a1" {
		t.Fatalf("acked record = %+v, %v", o, err)
	}
	var ackedAt string
	if err := db.r.QueryRow(`SELECT acked_at FROM outbox_actions WHERE action_id = ?`, "a1").Scan(&ackedAt); err != nil ||
		ackedAt != fmtTime(ackAt) {
		t.Fatalf("acked_at = %q, %v; want %q", ackedAt, err, fmtTime(ackAt))
	}

	if already := ack(t, db, "a1", botMsg, ackAt.Add(time.Second)); !already {
		t.Fatal("second ack alreadyAcked = false")
	}
	if n := countRows(t, db, "messages"); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
	if err := db.r.QueryRow(`SELECT acked_at FROM outbox_actions WHERE action_id = ?`, "a1").Scan(&ackedAt); err != nil ||
		ackedAt != fmtTime(ackAt) {
		t.Fatalf("acked_at after re-ack = %q, %v; want unchanged", ackedAt, err)
	}
}

func TestAckOutbox_ExpiredActionIsAcked(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now))
	lease(t, db, 10, now)
	if _, err := db.ExpireOutbox(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatalf("ExpireOutbox: %v", err)
	}
	if already := ack(t, db, "a1", nil, now.Add(time.Hour)); already {
		t.Fatal("ack of expired action alreadyAcked = true")
	}
	if status, _ := outboxStatus(t, db, "a1"); status != store.OutboxAcked {
		t.Fatalf("status = %s, want acked", status)
	}
}

func TestAckOutbox_WrongBotIsNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertActions(t, db, convAction("a1", testConvKey, now))
	req := conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p9"}
	if _, err := db.AckOutbox(ctx, "bot_other", "a1", req, nil, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong bot ack err = %v, want ErrNotFound", err)
	}
	if _, err := db.AckOutbox(ctx, testBot, "missing", req, nil, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing ack err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetOutbox(ctx, "bot_other", "a1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong bot GetOutbox err = %v, want ErrNotFound", err)
	}
	if status, _ := outboxStatus(t, db, "a1"); status != store.OutboxPending {
		t.Fatalf("status = %s, want pending", status)
	}
}

func TestExpireOutbox(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	past := convAction("pending_past", testConvKey, now)
	past.ExpiresAt = now.Add(-time.Second)
	leasedNow := convAction("leased_now", testConvKeyB, now)
	leasedNow.Status = store.OutboxLeased
	leasedNow.ExpiresAt = now
	future := convAction("pending_future", testConvKey, now)
	acked := convAction("acked_past", testConvKey, now)
	acked.Status = store.OutboxAcked
	acked.ExpiresAt = now.Add(-time.Second)
	insertActions(t, db, past, leasedNow, future, acked)

	n, err := db.ExpireOutbox(context.Background(), now)
	if err != nil || n != 2 {
		t.Fatalf("ExpireOutbox = %d, %v; want 2", n, err)
	}
	for id, want := range map[string]string{
		"pending_past":   store.OutboxExpired,
		"leased_now":     store.OutboxExpired,
		"pending_future": store.OutboxPending,
		"acked_past":     store.OutboxAcked,
	} {
		if status, _ := outboxStatus(t, db, id); status != want {
			t.Fatalf("%s = %s, want %s", id, status, want)
		}
	}
}

func TestAckOutbox_BotMessageConflictStillAcks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		botID      string
		platformID string
	}{
		{name: "same message and platform id", botID: "msg_777", platformID: "777"},
		{name: "same message id only", botID: "msg_777", platformID: "888"},
		{name: "same platform message id only", botID: "msg_bot_a1", platformID: "777"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			if _, err := db.Ingest(ctx, testIngest("k1", "msg_777", "777")); err != nil {
				t.Fatalf("Ingest: %v", err)
			}
			now := time.Now().UTC()
			insertActions(t, db, convAction("a1", testConvKey, now), convAction("a2", testConvKey, now))
			if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a1"}) {
				t.Fatalf("first lease = %v, want [a1]", ids)
			}

			botMsg := &store.MessageRecord{
				ID:                tc.botID,
				BotInstanceID:     testBot,
				ConversationKey:   testConvKey,
				Platform:          testPlatform,
				PlatformMessageID: tc.platformID,
				ActorID:           "bot",
				PlatformUserID:    "30001",
				Text:              "hi there",
				FromBot:           true,
				OccurredAt:        now,
				CreatedAt:         now,
			}
			req := conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: tc.platformID}
			already, err := db.AckOutbox(ctx, testBot, "a1", req, botMsg, now)
			if err != nil || already {
				t.Fatalf("AckOutbox = %v, %v; want false, nil", already, err)
			}
			if status, _ := outboxStatus(t, db, "a1"); status != store.OutboxAcked {
				t.Fatalf("a1 status = %s, want acked", status)
			}
			if n := countRows(t, db, "messages"); n != 1 {
				t.Fatalf("messages = %d, want 1", n)
			}
			msg, err := db.GetMessage(ctx, testBot, "msg_777")
			if err != nil || msg.FromBot || msg.Text != "hello msg_777" {
				t.Fatalf("original message = %+v, %v; want unchanged", msg, err)
			}
			if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a2"}) {
				t.Fatalf("lease after ack a1 = %v, want [a2]", ids)
			}
		})
	}
}

func TestLeaseOutbox_NonPositiveLimitLeasesNothing(t *testing.T) {
	for _, limit := range []int{0, -1} {
		db := newTestDB(t)
		now := time.Now().UTC()
		insertActions(t, db, convAction("a1", testConvKey, now), convAction("b1", testConvKeyB, now))
		if ids := actionIDs(lease(t, db, limit, now)); len(ids) != 0 {
			t.Fatalf("lease limit %d = %v, want none", limit, ids)
		}
		for _, id := range []string{"a1", "b1"} {
			if status, attempts := outboxStatus(t, db, id); status != store.OutboxPending || attempts != 0 {
				t.Fatalf("limit %d: %s = %s/%d, want pending/0", limit, id, status, attempts)
			}
		}
	}
}

func TestMigrations_OutboxTaskIndex(t *testing.T) {
	db := newTestDB(t)
	var detail string
	err := db.r.QueryRow(`EXPLAIN QUERY PLAN SELECT * FROM outbox_actions WHERE task_id = ?`, "task_1").
		Scan(new(int), new(int), new(int), &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if detail != "SEARCH outbox_actions USING INDEX outbox_actions_task (task_id=?)" {
		t.Fatalf("query plan = %q, want search using outbox_actions_task", detail)
	}
}

func TestLeaseOutbox_NotBeforeBlocksHeadOnly(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	a1 := convAction("a1", testConvKey, now)
	a1.NotBefore = now.Add(2 * time.Second)
	a1.Action.PartIndex, a1.Action.PartCount = 2, 3
	insertActions(t, db, a1, convAction("a2", testConvKey, now), convAction("b1", testConvKeyB, now))

	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"b1"}) {
		t.Fatalf("lease before a1 is due = %v, want [b1]", ids)
	}
	ack(t, db, "b1", nil, now)
	if ids := actionIDs(lease(t, db, 10, now.Add(time.Second))); len(ids) != 0 {
		t.Fatalf("lease while a1 not due = %v, want none", ids)
	}
	for _, id := range []string{"a1", "a2"} {
		if status, attempts := outboxStatus(t, db, id); status != store.OutboxPending || attempts != 0 {
			t.Fatalf("%s = %s/%d, want pending/0", id, status, attempts)
		}
	}
	got := lease(t, db, 10, now.Add(2*time.Second))
	if ids := actionIDs(got); !reflect.DeepEqual(ids, []string{"a1"}) {
		t.Fatalf("lease when a1 is due = %v, want [a1]", ids)
	}
	if !got[0].NotBefore.Equal(a1.NotBefore) || got[0].Action.PartIndex != 2 || got[0].Action.PartCount != 3 {
		t.Fatalf("leased a1 = not_before %v, part %d/%d; want %v, 2/3",
			got[0].NotBefore, got[0].Action.PartIndex, got[0].Action.PartCount, a1.NotBefore)
	}
	ack(t, db, "a1", nil, now.Add(2*time.Second))
	if ids := actionIDs(lease(t, db, 10, now.Add(2*time.Second))); !reflect.DeepEqual(ids, []string{"a2"}) {
		t.Fatalf("lease after ack a1 = %v, want [a2]", ids)
	}
}

func TestCancelPendingActions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertActions(t, db,
		taskAction("a0", "task_1", store.OutboxAcked, now),
		taskAction("a1", "task_1", store.OutboxPending, now),
		taskAction("a2", "task_1", store.OutboxPending, now),
		taskAction("a3", "task_1", store.OutboxPending, now),
		convAction("b1", testConvKeyB, now),
	)
	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a1", "b1"}) {
		t.Fatalf("lease = %v, want [a1 b1]", ids)
	}
	other := taskAction("c1", "task_2", store.OutboxPending, now)
	other.ConversationKey = store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, "conv_10003")
	insertActions(t, db, other)

	cancelAt := now.Add(time.Second)
	n, err := db.CancelPendingActions(ctx, "task_1", cancelAt)
	if err != nil || n != 2 {
		t.Fatalf("CancelPendingActions = %d, %v; want 2", n, err)
	}
	for id, want := range map[string]string{
		"a0": store.OutboxAcked,
		"a1": store.OutboxLeased,
		"a2": store.OutboxCancelled,
		"a3": store.OutboxCancelled,
		"b1": store.OutboxLeased,
		"c1": store.OutboxPending,
	} {
		if status, _ := outboxStatus(t, db, id); status != want {
			t.Fatalf("%s = %s, want %s", id, status, want)
		}
	}
	var closedAt string
	if err := db.r.QueryRow(`SELECT acked_at FROM outbox_actions WHERE action_id = ?`, "a2").Scan(&closedAt); err != nil ||
		closedAt != fmtTime(cancelAt) {
		t.Fatalf("a2 acked_at = %q, %v; want %q", closedAt, err, fmtTime(cancelAt))
	}

	ack(t, db, "a1", nil, now)
	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"c1"}) {
		t.Fatalf("lease after cancel = %v, want [c1]", ids)
	}
	if ids := actionIDs(lease(t, db, 10, now.Add(time.Hour))); len(ids) != 0 {
		t.Fatalf("later lease = %v, want none", ids)
	}
	if n, err := db.CancelPendingActions(ctx, "task_1", cancelAt); err != nil || n != 0 {
		t.Fatalf("second CancelPendingActions = %d, %v; want 0", n, err)
	}
}

func TestPurgeOutbox_IncludesCancelled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, cutoff := now.Add(-40*day), now.Add(-30*day)
	kept := seedOldTask(t, db, "1", conloomapi.TaskCancelled, now)
	insertActions(t, db,
		taskAction("orphan_cancelled", "task_gone", store.OutboxCancelled, old),
		taskAction("recent_cancelled", "task_gone", store.OutboxCancelled, now.Add(-29*day)),
		taskAction("with_task_cancelled", kept, store.OutboxCancelled, old),
	)
	if n, err := db.PurgeOutbox(ctx, cutoff, 10); err != nil || n != 1 {
		t.Fatalf("PurgeOutbox = %d, %v; want 1", n, err)
	}
	if actionExists(t, db, "orphan_cancelled") {
		t.Error("orphan_cancelled kept, want deleted")
	}
	for _, id := range []string{"recent_cancelled", "with_task_cancelled"} {
		if !actionExists(t, db, id) {
			t.Errorf("action %s deleted, want kept", id)
		}
	}
}

func TestMigration0010_ExistingActionsDeliverable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload, err := json.Marshal(testAction("a1", now).Action)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.w.Exec(`INSERT INTO outbox_actions (action_id, bot_instance_id, conversation_key, task_id, platform,
		status, payload, lease_until, attempts, platform_message_id, created_at, expires_at, acked_at)
		VALUES (?, ?, ?, 'task_1', ?, ?, ?, ?, 0, '', ?, ?, '')`,
		"a1", testBot, testConvKey, testPlatform, store.OutboxPending, string(payload),
		fmtTime(time.Time{}), fmtTime(now), fmtTime(now.Add(10*time.Minute))); err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var notBefore string
	if err := db.r.QueryRow(`SELECT not_before FROM outbox_actions WHERE action_id = 'a1'`).Scan(&notBefore); err != nil ||
		notBefore != "" {
		t.Fatalf("migrated not_before = %q, %v; want empty", notBefore, err)
	}
	got := lease(t, db, 10, now)
	if ids := actionIDs(got); !reflect.DeepEqual(ids, []string{"a1"}) {
		t.Fatalf("lease = %v, want [a1]", ids)
	}
	if !got[0].NotBefore.IsZero() || got[0].Action.PartIndex != 0 || got[0].Action.PartCount != 0 {
		t.Fatalf("migrated action = not_before %v, part %d/%d; want zero",
			got[0].NotBefore, got[0].Action.PartIndex, got[0].Action.PartCount)
	}
}

func TestAckOutbox_CancelledActionStaysCancelled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertActions(t, db, taskAction("a1", "task_1", store.OutboxPending, now))
	if n, err := db.CancelPendingActions(ctx, "task_1", now); err != nil || n != 1 {
		t.Fatalf("CancelPendingActions = %d, %v; want 1", n, err)
	}
	botMsg := &store.MessageRecord{
		ID: "msg_p_a1", BotInstanceID: testBot, ConversationKey: testConvKey, Platform: testPlatform,
		PlatformMessageID: "p_a1", ActorID: "bot", Text: "hi there", FromBot: true, OccurredAt: now, CreatedAt: now,
	}
	if already := ack(t, db, "a1", botMsg, now.Add(time.Second)); !already {
		t.Fatal("ack of cancelled action alreadyAcked = false, want true")
	}
	o, err := db.GetOutbox(ctx, testBot, "a1")
	if err != nil || o.Status != store.OutboxCancelled || o.PlatformMessageID != "" {
		t.Fatalf("record after ack = %+v, %v; want cancelled without platform message", o, err)
	}
	if n := countRows(t, db, "messages"); n != 0 {
		t.Fatalf("messages = %d, want 0", n)
	}
}

func TestCancelPendingActions_NextTaskOfConversationLeased(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	insertActions(t, db,
		taskAction("a1", "task_1", store.OutboxPending, now),
		taskAction("a2", "task_1", store.OutboxPending, now),
		taskAction("a3", "task_1", store.OutboxPending, now),
	)
	if ids := actionIDs(lease(t, db, 10, now)); !reflect.DeepEqual(ids, []string{"a1"}) {
		t.Fatalf("lease = %v, want [a1]", ids)
	}
	ack(t, db, "a1", nil, now)
	if n, err := db.CancelPendingActions(ctx, "task_1", now); err != nil || n != 2 {
		t.Fatalf("CancelPendingActions = %d, %v; want 2", n, err)
	}
	insertActions(t, db, taskAction("b1", "task_2", store.OutboxPending, now))
	got := lease(t, db, 10, now)
	if ids := actionIDs(got); !reflect.DeepEqual(ids, []string{"b1"}) {
		t.Fatalf("lease after cancel = %v, want [b1]", ids)
	}
	if got[0].ConversationKey != testConvKey || got[0].TaskID != "task_2" {
		t.Fatalf("leased = %s/%s, want %s/task_2", got[0].ConversationKey, got[0].TaskID, testConvKey)
	}
}

func TestLastReplyTask_NewestReplyOfActorInConversation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	err := db.inTx(ctx, func(tx *sql.Tx) error {
		for _, task := range []store.TaskRecord{
			{ID: "task_old", ActorPlatformUserID: "20001", ConversationKey: testConvKey},
			{ID: "task_new", ActorPlatformUserID: "20001", ConversationKey: testConvKey},
			{ID: "task_help", ActorPlatformUserID: "20001", ConversationKey: testConvKey, Command: "help"},
			{ID: "task_other", ActorPlatformUserID: "20002", ConversationKey: testConvKey},
			{ID: "task_elsewhere", ActorPlatformUserID: "20001", ConversationKey: testConvKeyB},
		} {
			task.BotInstanceID, task.Status = testBot, conloomapi.TaskSucceeded
			if err := insertTask(ctx, tx, task); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.LastReplyTask(ctx, testBot, testConvKey, "20001"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LastReplyTask without actions err = %v, want ErrNotFound", err)
	}
	elsewhere := taskAction("e1", "task_elsewhere", store.OutboxPending, now)
	elsewhere.ConversationKey = testConvKeyB
	insertActions(t, db,
		taskAction("o1", "task_old", store.OutboxAcked, now),
		taskAction("n1", "task_new", store.OutboxAcked, now),
		taskAction("n2", "task_new", store.OutboxPending, now),
		taskAction("h1", "task_help", store.OutboxPending, now),
		taskAction("x1", "task_other", store.OutboxPending, now),
		elsewhere,
	)
	got, err := db.LastReplyTask(ctx, testBot, testConvKey, "20001")
	if err != nil || got != "task_new" {
		t.Fatalf("LastReplyTask = %q, %v; want task_new", got, err)
	}
}
