package sqlite

import (
	"context"
	"database/sql"
	"errors"
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
