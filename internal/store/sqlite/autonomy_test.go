package sqlite

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// forgetPrivateTables are the autonomy tables ForgetPrivate clears for the
// conversation.
var forgetPrivateTables = []string{"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events",
	"conversation_custom_bars", "conversation_effects"}

func privKey(userID string) string {
	return store.ConversationKey(testBot, testPlatform, conloomapi.ConvPrivate, "private_"+userID)
}

func testTimer(key string, kind store.TimerKind, due time.Time) store.ConversationTimer {
	return store.ConversationTimer{
		ConversationKey: key, Kind: kind, BotInstanceID: testBot, PlatformUserID: "20001", DueAt: due, AnchorSeq: 7,
	}
}

func armTimers(t *testing.T, db *DB, timers ...store.ConversationTimer) {
	t.Helper()
	for _, tm := range timers {
		if err := db.ArmTimer(context.Background(), tm); err != nil {
			t.Fatalf("ArmTimer %s %s: %v", tm.ConversationKey, tm.Kind, err)
		}
	}
}

func listTimers(t *testing.T, db *DB, key string) []store.ConversationTimer {
	t.Helper()
	timers, err := db.ListTimers(context.Background(), key)
	if err != nil {
		t.Fatalf("ListTimers: %v", err)
	}
	return timers
}

func timerKinds(timers []store.ConversationTimer) []store.TimerKind {
	var kinds []store.TimerKind
	for _, tm := range timers {
		kinds = append(kinds, tm.Kind)
	}
	return kinds
}

// seedBuffRows stores a bar value and one change of it, a bot-created bar
// and an effect for the conversation, one row in each table.
func seedBuffRows(t *testing.T, db *DB, key, taskID string, at time.Time) {
	t.Helper()
	ts := fmtTime(at)
	if _, err := db.w.Exec(`INSERT INTO conversation_buffs (conversation_key, name, value, day, day_delta, updated_at)
		VALUES (?, '心情', 65, '2026-11-14', 5, ?)`, key, ts); err != nil {
		t.Fatalf("seed conversation_buffs: %v", err)
	}
	if _, err := db.w.Exec(`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at)
		VALUES (?, '心情', ?, 'model', 60, 65, '聊得开心', ?)`, key, taskID, ts); err != nil {
		t.Fatalf("seed conversation_buff_events: %v", err)
	}
	if _, err := db.w.Exec(`INSERT INTO conversation_custom_bars (conversation_key, name, baseline, half_life_seconds, description, task_id, created_at)
		VALUES (?, '饥饿', 30, 21600, '肚子饿的程度', ?, ?)`, key, taskID, ts); err != nil {
		t.Fatalf("seed conversation_custom_bars: %v", err)
	}
	if _, err := db.w.Exec(`INSERT INTO conversation_effects (conversation_key, name, description, started_at, until_at, task_id)
		VALUES (?, '着凉', '有点冷', ?, ?, ?)`, key, ts, fmtTime(at.Add(2*time.Hour)), taskID); err != nil {
		t.Fatalf("seed conversation_effects: %v", err)
	}
}

func autonomyCounts(t *testing.T, db *DB, key string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range forgetPrivateTables {
		var n int
		if err := db.r.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func TestStatus_SetGetClear(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	if _, err := db.GetStatus(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetStatus without a status err = %v, want ErrNotFound", err)
	}
	start := time.Date(2026, 11, 14, 20, 10, 0, 0, time.UTC)
	set := func(activity, taskID string, until time.Time) {
		t.Helper()
		s := store.ConversationStatus{ConversationKey: key, Activity: activity, TaskID: taskID, StartedAt: start, UntilAt: until}
		ret := &store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until, AnchorSeq: 3}
		if err := db.SetStatus(ctx, s, ret); err != nil {
			t.Fatalf("SetStatus %s: %v", activity, err)
		}
	}
	armTimers(t, db, testTimer(key, store.TimerInitiate, start.Add(5*time.Hour)),
		testTimer(other, store.TimerStatusReturn, start.Add(time.Hour)))

	until := start.Add(20 * time.Minute)
	set("洗澡", "task_1", until)
	got, err := db.GetStatus(ctx, key)
	if err != nil || got.ConversationKey != key || got.Activity != "洗澡" || got.TaskID != "task_1" ||
		!got.StartedAt.Equal(start) || !got.UntilAt.Equal(until) || got.UpdatedAt.IsZero() {
		t.Fatalf("GetStatus = %+v, %v; want 洗澡 until %v with UpdatedAt set", got, err, until)
	}
	timers := listTimers(t, db, key)
	if !reflect.DeepEqual(timerKinds(timers), []store.TimerKind{store.TimerStatusReturn, store.TimerInitiate}) {
		t.Fatalf("timers = %+v, want status_return then initiate", timers)
	}
	ret := timers[0]
	if ret.ConversationKey != key || ret.BotInstanceID != testBot || ret.PlatformUserID != "20001" ||
		!ret.DueAt.Equal(until) || ret.AnchorSeq != 3 || !ret.LeaseUntil.IsZero() || ret.CreatedAt.IsZero() {
		t.Fatalf("status_return timer = %+v", ret)
	}

	until2 := start.Add(40 * time.Minute)
	set("做饭", "task_2", until2)
	if got, err := db.GetStatus(ctx, key); err != nil || got.Activity != "做饭" || got.TaskID != "task_2" || !got.UntilAt.Equal(until2) {
		t.Fatalf("GetStatus after replacing = %+v, %v; want 做饭 until %v", got, err, until2)
	}
	if n := countRows(t, db, "conversation_status"); n != 1 {
		t.Fatalf("conversation_status rows = %d, want 1", n)
	}
	if timers := listTimers(t, db, key); len(timers) != 2 || timers[0].Kind != store.TimerStatusReturn || !timers[0].DueAt.Equal(until2) {
		t.Fatalf("timers after replacing = %+v, want one status_return due %v", timers, until2)
	}

	if ok, err := db.ClearStatus(ctx, key, until); err != nil || ok {
		t.Fatalf("ClearStatus with the replaced until = %v, %v; want false", ok, err)
	}
	if _, err := db.GetStatus(ctx, key); err != nil {
		t.Fatalf("status after a mismatched clear: %v", err)
	}
	if n := len(listTimers(t, db, key)); n != 2 {
		t.Fatalf("timers after a mismatched clear = %d, want 2", n)
	}
	if ok, err := db.ClearStatus(ctx, key, until2); err != nil || !ok {
		t.Fatalf("ClearStatus = %v, %v; want true", ok, err)
	}
	if _, err := db.GetStatus(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetStatus after clearing err = %v, want ErrNotFound", err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerInitiate}) {
		t.Fatalf("timers after clearing = %v, want only initiate", kinds)
	}
	if n := len(listTimers(t, db, other)); n != 1 {
		t.Fatalf("other conversation's timers = %d, want 1", n)
	}
	if ok, err := db.ClearStatus(ctx, key, time.Time{}); err != nil || ok {
		t.Fatalf("ClearStatus without a status = %v, %v; want false", ok, err)
	}

	set("散步", "task_3", until)
	if ok, err := db.ClearStatus(ctx, key, time.Time{}); err != nil || !ok {
		t.Fatalf("unconditional ClearStatus = %v, %v; want true", ok, err)
	}
	if _, err := db.GetStatus(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetStatus after unconditional clear err = %v, want ErrNotFound", err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerInitiate}) {
		t.Fatalf("timers after unconditional clear = %v, want only initiate", kinds)
	}

	set("看书", "task_4", until)
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: key, Activity: "看书", TaskID: "task_5",
		StartedAt: start, UntilAt: until2}, nil); err != nil {
		t.Fatalf("SetStatus without a timer: %v", err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerInitiate}) {
		t.Fatalf("timers after SetStatus without a timer = %v, want only initiate", kinds)
	}

	bad := store.ConversationTimer{ConversationKey: other, BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until}
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: key, Activity: "跑步", StartedAt: start, UntilAt: until},
		&bad); err == nil {
		t.Fatal("SetStatus with another conversation's timer succeeded")
	}
	bad = store.ConversationTimer{Kind: store.TimerInitiate, BotInstanceID: testBot, PlatformUserID: "20001", DueAt: until}
	if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: key, Activity: "跑步", StartedAt: start, UntilAt: until},
		&bad); err == nil {
		t.Fatal("SetStatus with an initiate timer succeeded")
	}
	if got, err := db.GetStatus(ctx, key); err != nil || got.Activity != "看书" {
		t.Fatalf("status after refused SetStatus = %+v, %v; want 看书 kept", got, err)
	}
}

func TestTimers_ArmClaimLease(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a, b, c := privKey("20001"), privKey("20002"), privKey("20003")
	armTimers(t, db,
		testTimer(a, store.TimerInitiate, now),
		testTimer(b, store.TimerFollowUp, now.Add(-5*time.Minute)),
		testTimer(c, store.TimerInitiate, now.Add(time.Hour)),
	)
	claim := func(at time.Time, limit int) []store.ConversationTimer {
		t.Helper()
		got, err := db.ClaimDueTimers(ctx, at, 2*time.Minute, limit)
		if err != nil {
			t.Fatalf("ClaimDueTimers: %v", err)
		}
		return got
	}
	keys := func(timers []store.ConversationTimer) []string {
		var out []string
		for _, tm := range timers {
			out = append(out, tm.ConversationKey)
		}
		return out
	}

	got := claim(now, 10)
	if !reflect.DeepEqual(keys(got), []string{b, a}) {
		t.Fatalf("claimed %v, want [%s %s]", keys(got), b, a)
	}
	want := testTimer(b, store.TimerFollowUp, now.Add(-5*time.Minute))
	want.LeaseUntil = now.Add(2 * time.Minute)
	if g := got[0]; g.Kind != want.Kind || g.BotInstanceID != want.BotInstanceID || g.PlatformUserID != want.PlatformUserID ||
		!g.DueAt.Equal(want.DueAt) || g.AnchorSeq != want.AnchorSeq || !g.LeaseUntil.Equal(want.LeaseUntil) || g.CreatedAt.IsZero() {
		t.Fatalf("claimed timer = %+v, want %+v", g, want)
	}
	if stored := listTimers(t, db, a); !stored[0].LeaseUntil.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("stored lease = %v, want %v", stored[0].LeaseUntil, now.Add(2*time.Minute))
	}
	if got := claim(now.Add(time.Minute), 10); len(got) != 0 {
		t.Fatalf("claim while leased = %v, want none", keys(got))
	}
	if got := claim(now.Add(3*time.Minute), 1); !reflect.DeepEqual(keys(got), []string{b}) {
		t.Fatalf("claim after the lease with limit 1 = %v, want [%s]", keys(got), b)
	}
	if got := claim(now.Add(3*time.Minute), 10); !reflect.DeepEqual(keys(got), []string{a}) {
		t.Fatalf("second claim after the lease = %v, want [%s]", keys(got), a)
	}
	if got := claim(now.Add(10*time.Minute), 0); len(got) != 0 {
		t.Fatalf("claim with limit 0 = %v, want none", keys(got))
	}

	armTimers(t, db, testTimer(a, store.TimerInitiate, now.Add(4*time.Minute)))
	if stored := listTimers(t, db, a); len(stored) != 1 || !stored[0].LeaseUntil.IsZero() || !stored[0].DueAt.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("re-armed timer = %+v, want one with the new due time and no lease", stored)
	}
	if got := claim(now.Add(4*time.Minute), 10); !reflect.DeepEqual(keys(got), []string{a}) {
		t.Fatalf("claim of the re-armed timer = %v, want [%s]", keys(got), a)
	}

	if err := db.ArmTimer(ctx, store.ConversationTimer{ConversationKey: a, Kind: "wake", DueAt: now}); err == nil {
		t.Fatal("ArmTimer with an unknown kind succeeded")
	}
	if err := db.ArmTimer(ctx, store.ConversationTimer{Kind: store.TimerInitiate, DueAt: now}); err == nil {
		t.Fatal("ArmTimer without a conversation succeeded")
	}
}

func TestTimers_PostponeOnlyIfUnchanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	key := privKey("20001")
	armTimers(t, db, testTimer(key, store.TimerInitiate, now))
	claimed, err := db.ClaimDueTimers(ctx, now, 2*time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDueTimers = %v, %v", claimed, err)
	}
	ref := store.TimerRef{ConversationKey: key, Kind: store.TimerInitiate, DueAt: claimed[0].DueAt}

	later := now.Add(2 * time.Minute)
	if ok, err := db.PostponeTimer(ctx, ref, later); err != nil || !ok {
		t.Fatalf("PostponeTimer = %v, %v; want true", ok, err)
	}
	stored := listTimers(t, db, key)
	if len(stored) != 1 || !stored[0].DueAt.Equal(later) || !stored[0].LeaseUntil.IsZero() {
		t.Fatalf("postponed timer = %+v, want due %v without a lease", stored, later)
	}
	if got, err := db.ClaimDueTimers(ctx, later, 2*time.Minute, 10); err != nil || len(got) != 1 {
		t.Fatalf("claim of the postponed timer = %v, %v; want it", got, err)
	}
	if ok, err := db.PostponeTimer(ctx, ref, later.Add(time.Minute)); err != nil || ok {
		t.Fatalf("PostponeTimer with the stale due time = %v, %v; want false", ok, err)
	}

	rearmed := now.Add(3 * time.Hour)
	armTimers(t, db, testTimer(key, store.TimerInitiate, rearmed))
	ref.DueAt = later
	if ok, err := db.PostponeTimer(ctx, ref, later.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("PostponeTimer of a re-armed timer = %v, %v; want false", ok, err)
	}
	if stored := listTimers(t, db, key); len(stored) != 1 || !stored[0].DueAt.Equal(rearmed) {
		t.Fatalf("re-armed timer = %+v, want due %v kept", stored, rearmed)
	}
	ref.Kind = store.TimerFollowUp
	ref.DueAt = rearmed
	if ok, err := db.PostponeTimer(ctx, ref, rearmed.Add(time.Minute)); err != nil || ok {
		t.Fatalf("PostponeTimer of another kind = %v, %v; want false", ok, err)
	}
}

func TestTimers_Cancel(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	key, other := privKey("20001"), privKey("20002")
	armTimers(t, db,
		testTimer(key, store.TimerStatusReturn, now.Add(20*time.Minute)),
		testTimer(key, store.TimerInitiate, now.Add(time.Hour)),
		testTimer(key, store.TimerFollowUp, now.Add(2*time.Hour)),
		testTimer(other, store.TimerInitiate, now.Add(time.Hour)),
	)
	if n, err := db.CancelTimers(ctx, key, store.TimerInitiate, store.TimerFollowUp); err != nil || n != 2 {
		t.Fatalf("CancelTimers initiate, follow_up = %d, %v; want 2", n, err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerStatusReturn}) {
		t.Fatalf("timers left = %v, want only status_return", kinds)
	}
	if kinds := timerKinds(listTimers(t, db, other)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerInitiate}) {
		t.Fatalf("other conversation's timers = %v, want its initiate kept", kinds)
	}
	if n, err := db.CancelTimers(ctx, key, store.TimerInitiate); err != nil || n != 0 {
		t.Fatalf("CancelTimers of a cancelled kind = %d, %v; want 0", n, err)
	}
	if n, err := db.CancelTimers(ctx, key); err != nil || n != 0 {
		t.Fatalf("CancelTimers without kinds = %d, %v; want 0", n, err)
	}
	if n, err := db.CancelTimers(ctx, key, store.TimerStatusReturn); err != nil || n != 1 {
		t.Fatalf("CancelTimers status_return = %d, %v; want 1", n, err)
	}
	if timers := listTimers(t, db, key); len(timers) != 0 {
		t.Fatalf("timers left = %+v, want none", timers)
	}
}

func TestTimers_DeleteOnlyIfUnchanged(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	key, other := privKey("20001"), privKey("20002")
	due := now.Add(time.Hour)
	armTimers(t, db,
		testTimer(key, store.TimerInitiate, due),
		testTimer(key, store.TimerFollowUp, due),
		testTimer(other, store.TimerInitiate, due),
	)
	ref := store.TimerRef{ConversationKey: key, Kind: store.TimerInitiate, DueAt: due}

	for _, stale := range []store.TimerRef{
		{ConversationKey: key, Kind: store.TimerInitiate, DueAt: due.Add(time.Minute)},
		{ConversationKey: key, Kind: store.TimerStatusReturn, DueAt: due},
		{ConversationKey: privKey("20003"), Kind: store.TimerInitiate, DueAt: due},
	} {
		if ok, err := db.DeleteTimer(ctx, stale); err != nil || ok {
			t.Fatalf("DeleteTimer(%+v) = %v, %v; want false", stale, ok, err)
		}
	}
	if n := len(listTimers(t, db, key)); n != 2 {
		t.Fatalf("timers after stale deletes = %d, want 2", n)
	}

	rearmed := due.Add(3 * time.Hour)
	armTimers(t, db, testTimer(key, store.TimerInitiate, rearmed))
	if ok, err := db.DeleteTimer(ctx, ref); err != nil || ok {
		t.Fatalf("DeleteTimer of a re-armed timer = %v, %v; want false", ok, err)
	}
	ref.DueAt = rearmed
	if ok, err := db.DeleteTimer(ctx, ref); err != nil || !ok {
		t.Fatalf("DeleteTimer = %v, %v; want true", ok, err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerFollowUp}) {
		t.Fatalf("timers left = %v, want only the follow-up", kinds)
	}
	if kinds := timerKinds(listTimers(t, db, other)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerInitiate}) {
		t.Fatalf("other conversation's timers = %v, want its initiate kept", kinds)
	}
	if ok, err := db.DeleteTimer(ctx, ref); err != nil || ok {
		t.Fatalf("second DeleteTimer = %v, %v; want false", ok, err)
	}
}

// timerIngest is the ingest of a fired timer: an internal event and a task
// without trigger messages.
func timerIngest(eventKey, taskID string, ref store.TimerRef) store.Ingest {
	now := time.Now().UTC()
	return store.Ingest{
		Event: store.EventRecord{
			BotInstanceID: testBot, IdempotencyKey: eventKey, EventID: "evt_" + taskID,
			Response: []byte(`{"task_id":"` + taskID + `"}`), CreatedAt: now,
		},
		NewTask: &store.TaskRecord{
			ID: taskID, BotInstanceID: testBot, ConversationKey: ref.ConversationKey, Status: conloomapi.TaskQueued,
			ActorPlatformUserID: "20001", EventOccurredAt: now,
		},
		FireTimer: &ref,
	}
}

func TestIngest_FireTimer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	due := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	key := privKey("20001")
	armTimers(t, db, testTimer(key, store.TimerInitiate, due), testTimer(key, store.TimerStatusReturn, due))
	ref := store.TimerRef{ConversationKey: key, Kind: store.TimerInitiate, DueAt: due}

	if _, err := db.Ingest(ctx, timerIngest("conloom:timer:1", "task_w1", ref)); err != nil {
		t.Fatalf("Ingest firing the timer: %v", err)
	}
	if _, err := db.GetTask(ctx, testBot, "task_w1"); err != nil {
		t.Fatalf("task of the fired timer: %v", err)
	}
	if kinds := timerKinds(listTimers(t, db, key)); !reflect.DeepEqual(kinds, []store.TimerKind{store.TimerStatusReturn}) {
		t.Fatalf("timers after firing = %v, want only status_return", kinds)
	}
	if existing, err := db.Ingest(ctx, timerIngest("conloom:timer:1", "task_w1", ref)); err != nil || existing == nil {
		t.Fatalf("Ingest of the same event = %v, %v; want the existing event", existing, err)
	}

	if _, err := db.Ingest(ctx, timerIngest("conloom:timer:2", "task_w2", ref)); !errors.Is(err, store.ErrTimerGone) {
		t.Fatalf("Ingest firing a fired timer err = %v, want ErrTimerGone", err)
	}
	if _, err := db.GetTask(ctx, testBot, "task_w2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("task of the refused ingest err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetEvent(ctx, testBot, "conloom:timer:2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("event of the refused ingest err = %v, want ErrNotFound", err)
	}

	rearmed := due.Add(time.Hour)
	armTimers(t, db, testTimer(key, store.TimerInitiate, rearmed))
	if _, err := db.Ingest(ctx, timerIngest("conloom:timer:3", "task_w3", ref)); !errors.Is(err, store.ErrTimerGone) {
		t.Fatalf("Ingest firing a re-armed timer by its old due time err = %v, want ErrTimerGone", err)
	}
	if timers := listTimers(t, db, key); len(timers) != 2 || !timers[1].DueAt.Equal(rearmed) {
		t.Fatalf("timers after the refused ingest = %+v, want the re-armed initiate kept", timers)
	}
	if n := countRows(t, db, "events"); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	if n := countRows(t, db, "tasks"); n != 1 {
		t.Fatalf("tasks = %d, want 1", n)
	}
}

func TestUserMessageTimes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	msg := func(key string, fromBot bool, at time.Time) store.MessageRecord {
		m := memMessage(key, "user_20001", "20001", fromBot)
		if fromBot {
			m.ActorID, m.PlatformUserID = "bot_30001", "30001"
		}
		m.OccurredAt = at
		return m
	}
	memInsertMessages(t, db,
		msg(key, false, base.Add(-48*time.Hour)),
		msg(key, false, base.Add(-2*time.Hour)),
		msg(key, true, base.Add(-time.Hour)),
		msg(key, false, base.Add(-30*time.Minute)),
		msg(other, false, base.Add(-10*time.Minute)),
		msg(key, false, base),
		msg(key, true, base.Add(time.Minute)),
	)
	times := func(since time.Time, limit int) []time.Time {
		t.Helper()
		got, err := db.UserMessageTimes(ctx, key, since, limit)
		if err != nil {
			t.Fatalf("UserMessageTimes: %v", err)
		}
		return got
	}
	same := func(got []time.Time, want ...time.Time) bool {
		return slices.EqualFunc(got, want, time.Time.Equal)
	}
	if got := times(base.Add(-3*time.Hour), 10); !same(got, base, base.Add(-30*time.Minute), base.Add(-2*time.Hour)) {
		t.Fatalf("times since 3h before = %v", got)
	}
	if got := times(base.Add(-3*time.Hour), 2); !same(got, base, base.Add(-30*time.Minute)) {
		t.Fatalf("times with limit 2 = %v", got)
	}
	if got := times(base.Add(-30*time.Minute), 10); !same(got, base, base.Add(-30*time.Minute)) {
		t.Fatalf("times since a message's own time = %v, want it included", got)
	}
	if got := times(time.Time{}, 10); len(got) != 4 {
		t.Fatalf("times since the beginning = %v, want 4", got)
	}
	if got := times(time.Time{}, 0); len(got) != 0 {
		t.Fatalf("times with limit 0 = %v, want none", got)
	}
}

func TestUserMessageTimes_CountsPanelChannel(t *testing.T) {
	db := newTestDB(t)
	key := store.ConversationKey(testBot, store.PlatformPanel, conloomapi.ConvPrivate, "panel_1")
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	var msgs []store.MessageRecord
	for i, fromBot := range []bool{false, true, false} {
		m := memMessage(key, "panel_admin", "panel_admin", fromBot)
		m.Platform, m.Channel, m.OccurredAt = store.PlatformPanel, store.ChannelPanel, base.Add(time.Duration(i)*time.Hour)
		msgs = append(msgs, m)
	}
	memInsertMessages(t, db, msgs...)
	got, err := db.UserMessageTimes(context.Background(), key, time.Time{}, 10)
	if err != nil {
		t.Fatalf("UserMessageTimes: %v", err)
	}
	if want := []time.Time{base.Add(2 * time.Hour), base}; !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Fatalf("times = %v, want the panel chat user's messages %v", got, want)
	}
}

func TestUserMessageTimes_LooksAtNewestFourTimesLimit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	var msgs []store.MessageRecord
	for _, ago := range []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour} {
		m := memMessage(key, "user_20001", "20001", false)
		m.OccurredAt = base.Add(-ago)
		msgs = append(msgs, m)
	}
	for i := range 10 {
		m := memMessage(key, "bot_30001", "30001", true)
		m.OccurredAt = base.Add(time.Duration(i) * time.Minute)
		msgs = append(msgs, m)
	}
	memInsertMessages(t, db, msgs...)
	for _, tc := range []struct {
		limit int
		want  []time.Time
	}{
		{2, nil},
		{3, []time.Time{base.Add(-time.Hour), base.Add(-2 * time.Hour)}},
		{4, []time.Time{base.Add(-time.Hour), base.Add(-2 * time.Hour), base.Add(-3 * time.Hour)}},
	} {
		got, err := db.UserMessageTimes(ctx, key, time.Time{}, tc.limit)
		if err != nil {
			t.Fatalf("UserMessageTimes limit %d: %v", tc.limit, err)
		}
		if !slices.EqualFunc(got, tc.want, time.Time.Equal) {
			t.Errorf("limit %d (newest %d messages) = %v, want %v", tc.limit, 4*tc.limit, got, tc.want)
		}
	}
}

func TestForgetPrivate_ClearsAutonomy(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	key, other := privKey("20001"), privKey("20002")
	for _, k := range []string{key, other} {
		until := now.Add(20 * time.Minute)
		if err := db.SetStatus(ctx, store.ConversationStatus{ConversationKey: k, Activity: "洗澡", TaskID: "task_s" + k,
			StartedAt: now, UntilAt: until}, &store.ConversationTimer{BotInstanceID: testBot, PlatformUserID: "20001",
			DueAt: until}); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		armTimers(t, db, testTimer(k, store.TimerInitiate, now.Add(-time.Minute)), testTimer(k, store.TimerFollowUp, now.Add(3*time.Hour)))
		seedBuffRows(t, db, k, "task_b"+k, now)
	}
	keep := autonomyCounts(t, db, other)
	for table, n := range autonomyCounts(t, db, key) {
		if n == 0 {
			t.Fatalf("seed left %s empty", table)
		}
	}
	inFlight, err := db.ClaimDueTimers(ctx, now, 2*time.Minute, 10)
	if err != nil || len(inFlight) != 2 {
		t.Fatalf("ClaimDueTimers = %+v, %v; want both initiates", inFlight, err)
	}

	if err := db.ForgetPrivate(ctx, key, "20001", now); err != nil {
		t.Fatalf("ForgetPrivate: %v", err)
	}
	for table, n := range autonomyCounts(t, db, key) {
		if n != 0 {
			t.Errorf("%s rows after ForgetPrivate = %d, want 0", table, n)
		}
	}
	if got := autonomyCounts(t, db, other); !maps.Equal(got, keep) {
		t.Errorf("other conversation's rows = %v, want %v", got, keep)
	}
	for _, tm := range inFlight {
		ref := store.TimerRef{ConversationKey: tm.ConversationKey, Kind: tm.Kind, DueAt: tm.DueAt}
		_, err := db.Ingest(ctx, timerIngest("conloom:timer:"+tm.ConversationKey, "task_w"+tm.ConversationKey, ref))
		switch {
		case tm.ConversationKey == key && !errors.Is(err, store.ErrTimerGone):
			t.Errorf("firing the forgotten conversation's claimed timer err = %v, want ErrTimerGone", err)
		case tm.ConversationKey == other && err != nil:
			t.Errorf("firing the other conversation's claimed timer: %v", err)
		}
	}
}

// setBuff stores value as the bar's new value through UpdateBuff, with the
// event's time as the value's, and reports before as the value it replaced.
func setBuff(t *testing.T, db *DB, key, name string, before, value float64, ev store.BuffEvent) store.BuffValue {
	t.Helper()
	got, err := db.UpdateBuff(context.Background(), key, name, ev, func(cur store.BuffValue, _ bool) (store.BuffValue, float64, error) {
		cur.Value, cur.Day, cur.UpdatedAt = value, "2026-11-14", ev.CreatedAt
		return cur, before, nil
	})
	if err != nil {
		t.Fatalf("UpdateBuff %s %s: %v", key, name, err)
	}
	return got
}

func getBuffs(t *testing.T, db *DB, key string) []store.BuffValue {
	t.Helper()
	got, err := db.GetBuffs(context.Background(), key)
	if err != nil {
		t.Fatalf("GetBuffs: %v", err)
	}
	return got
}

func listBuffEvents(t *testing.T, db *DB, key string, limit int) []store.BuffEvent {
	t.Helper()
	got, err := db.ListBuffEvents(context.Background(), key, limit)
	if err != nil {
		t.Fatalf("ListBuffEvents: %v", err)
	}
	return got
}

func sameBuffs(got, want []store.BuffValue) bool {
	return slices.EqualFunc(got, want, func(a, b store.BuffValue) bool {
		return a.ConversationKey == b.ConversationKey && a.Name == b.Name && a.Value == b.Value && a.Day == b.Day &&
			a.DayDelta == b.DayDelta && a.UpdatedAt.Equal(b.UpdatedAt)
	})
}

// sameBuffEvents compares events ignoring IDs.
func sameBuffEvents(got, want []store.BuffEvent) bool {
	return slices.EqualFunc(got, want, func(a, b store.BuffEvent) bool {
		return a.ConversationKey == b.ConversationKey && a.Name == b.Name && a.Kind == b.Kind && a.TaskID == b.TaskID &&
			a.Source == b.Source && a.Before == b.Before && a.After == b.After && a.Reason == b.Reason &&
			a.CreatedAt.Equal(b.CreatedAt)
	})
}

func TestBuffs_UpdateAndList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	at := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	if got := getBuffs(t, db, key); len(got) != 0 {
		t.Fatalf("GetBuffs without bars = %+v, want none", got)
	}

	// The event's Before and After come from apply, never from the event
	// passed in; its Kind is always an adjustment.
	first := store.BuffEvent{Kind: store.BuffKindBarAdd, TaskID: "task_1", Source: store.BuffSourceModel, Before: 1, After: 99,
		Reason: "聊得开心", CreatedAt: at}
	got, err := db.UpdateBuff(ctx, key, "心情", first, func(cur store.BuffValue, found bool) (store.BuffValue, float64, error) {
		if found || cur != (store.BuffValue{}) {
			t.Errorf("apply of a new bar got %+v, found %v; want a zero value, not found", cur, found)
		}
		return store.BuffValue{Value: 65, Day: "2026-11-14", DayDelta: 5, UpdatedAt: at}, 60, nil
	})
	want1 := store.BuffValue{ConversationKey: key, Name: "心情", Value: 65, Day: "2026-11-14", DayDelta: 5, UpdatedAt: at}
	if err != nil || !sameBuffs([]store.BuffValue{got}, []store.BuffValue{want1}) {
		t.Fatalf("first UpdateBuff = %+v, %v; want %+v", got, err, want1)
	}

	// A stored bar's event records the before apply reports (here a decayed
	// 63.25), not the stored 65.
	second := store.BuffEvent{TaskID: "task_2", Source: store.BuffSourceModel, Before: 1, Reason: "被夸了", CreatedAt: at.Add(time.Hour)}
	got, err = db.UpdateBuff(ctx, key, "心情", second, func(cur store.BuffValue, found bool) (store.BuffValue, float64, error) {
		if !found || !sameBuffs([]store.BuffValue{cur}, []store.BuffValue{want1}) {
			t.Errorf("apply of a stored bar got %+v, found %v; want %+v, found", cur, found, want1)
		}
		cur.Value, cur.DayDelta, cur.UpdatedAt = 72.5, 12, at.Add(time.Hour)
		return cur, 63.25, nil
	})
	want2 := store.BuffValue{ConversationKey: key, Name: "心情", Value: 72.5, Day: "2026-11-14", DayDelta: 12, UpdatedAt: at.Add(time.Hour)}
	if err != nil || !sameBuffs([]store.BuffValue{got}, []store.BuffValue{want2}) {
		t.Fatalf("second UpdateBuff = %+v, %v; want %+v", got, err, want2)
	}

	panel := store.BuffEvent{Source: store.BuffSourcePanel, Reason: "手动调整", CreatedAt: at.Add(2 * time.Hour)}
	want3 := setBuff(t, db, key, "精力", 70, 40, panel)
	if want3.ConversationKey != key || want3.Name != "精力" || want3.Value != 40 {
		t.Fatalf("UpdateBuff 精力 = %+v", want3)
	}
	otherBuff := setBuff(t, db, other, "心情", 60, 30, store.BuffEvent{Source: store.BuffSourceAPI, Reason: "接口"})
	if otherBuff.UpdatedAt.IsZero() {
		t.Fatalf("UpdateBuff with a zero UpdatedAt returned %+v, want it set to now", otherBuff)
	}
	keep := func(cur store.BuffValue, _ bool) (store.BuffValue, float64, error) { return cur, cur.Value, nil }
	if _, err := db.UpdateBuff(ctx, key, "", panel, keep); err == nil {
		t.Fatal("UpdateBuff without a name succeeded")
	}
	if _, err := db.UpdateBuff(ctx, "", "心情", panel, keep); err == nil {
		t.Fatal("UpdateBuff without a conversation succeeded")
	}

	if got := getBuffs(t, db, key); !sameBuffs(got, []store.BuffValue{want2, want3}) {
		t.Fatalf("GetBuffs = %+v, want 心情 then 精力: %+v, %+v", got, want2, want3)
	}
	if got := getBuffs(t, db, other); len(got) != 1 || got[0].Value != 30 || got[0].UpdatedAt.IsZero() {
		t.Fatalf("other conversation's bars = %+v, want 心情 30 with UpdatedAt set", got)
	}

	adjust := store.BuffKindAdjust
	wantEvents := []store.BuffEvent{
		{ConversationKey: key, Name: "精力", Kind: adjust, Source: store.BuffSourcePanel, Before: 70, After: 40, Reason: "手动调整", CreatedAt: panel.CreatedAt},
		{ConversationKey: key, Name: "心情", Kind: adjust, TaskID: "task_2", Source: store.BuffSourceModel, Before: 63.25, After: 72.5, Reason: "被夸了", CreatedAt: second.CreatedAt},
		{ConversationKey: key, Name: "心情", Kind: adjust, TaskID: "task_1", Source: store.BuffSourceModel, Before: 60, After: 65, Reason: "聊得开心", CreatedAt: at},
	}
	events := listBuffEvents(t, db, key, 10)
	if !sameBuffEvents(events, wantEvents) {
		t.Fatalf("ListBuffEvents = %+v, want newest first %+v", events, wantEvents)
	}
	for _, ev := range events {
		if ev.ID == 0 {
			t.Fatalf("event without an ID: %+v", ev)
		}
	}
	if got := listBuffEvents(t, db, key, 2); !sameBuffEvents(got, wantEvents[:2]) {
		t.Fatalf("ListBuffEvents limit 2 = %+v, want the newest two", got)
	}
	if got := listBuffEvents(t, db, key, 0); len(got) != 0 {
		t.Fatalf("ListBuffEvents limit 0 = %+v, want none", got)
	}
	if got := listBuffEvents(t, db, other, 10); len(got) != 1 || got[0].Source != store.BuffSourceAPI || got[0].Before != 60 ||
		got[0].After != 30 || got[0].CreatedAt.IsZero() {
		t.Fatalf("other conversation's events = %+v, want one api event 60 -> 30 with CreatedAt set", got)
	}
}

func TestBuffs_DuplicateTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	at := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	byTask := store.BuffEvent{TaskID: "task_1", Source: store.BuffSourceModel, Reason: "聊得开心", CreatedAt: at}
	setBuff(t, db, key, "心情", 60, 65, byTask)

	called := false
	_, err := db.UpdateBuff(ctx, key, "心情", byTask, func(cur store.BuffValue, _ bool) (store.BuffValue, float64, error) {
		called = true
		before := cur.Value
		cur.Value = 70
		return cur, before, nil
	})
	if !errors.Is(err, store.ErrBuffDuplicate) {
		t.Fatalf("second UpdateBuff of the task err = %v, want ErrBuffDuplicate", err)
	}
	if called {
		t.Error("apply was called for a duplicate")
	}
	if got := getBuffs(t, db, key); len(got) != 1 || got[0].Value != 65 {
		t.Fatalf("bars after the duplicate = %+v, want 心情 65 kept", got)
	}
	if got := listBuffEvents(t, db, key, 10); len(got) != 1 {
		t.Fatalf("events after the duplicate = %+v, want one", got)
	}

	setBuff(t, db, key, "精力", 70, 75, byTask)
	panel := store.BuffEvent{Source: store.BuffSourcePanel, CreatedAt: at.Add(time.Minute)}
	setBuff(t, db, key, "心情", 65, 50, panel)
	setBuff(t, db, key, "心情", 50, 55, panel)
	if got := getBuffs(t, db, key); len(got) != 2 || got[0].Name != "心情" || got[0].Value != 55 || got[1].Name != "精力" || got[1].Value != 75 {
		t.Fatalf("bars = %+v, want 心情 55 and 精力 75", got)
	}
	if got := listBuffEvents(t, db, key, 10); len(got) != 4 {
		t.Fatalf("events = %+v, want 4", got)
	}
}

func TestBuffs_DuplicateCheckUsesTaskIndex(t *testing.T) {
	db := newTestDB(t)
	var detail string
	err := db.r.QueryRow(`EXPLAIN QUERY PLAN `+buffTaskChangesQuery, "task_1", "心情", store.BuffKindAdjust).
		Scan(new(int), new(int), new(int), &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if want := "SEARCH conversation_buff_events USING COVERING INDEX conversation_buff_events_task (task_id=? AND name=? AND kind=?)"; detail != want {
		t.Fatalf("query plan = %q, want %q", detail, want)
	}
}

func TestBuffs_ApplyErrorRollsBack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	at := time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)
	setBuff(t, db, key, "心情", 60, 65, store.BuffEvent{TaskID: "task_1", Source: store.BuffSourceModel, CreatedAt: at})

	boom := errors.New("boom")
	fail := func(cur store.BuffValue, _ bool) (store.BuffValue, float64, error) {
		cur.Value = 10
		return cur, 65, boom
	}
	ev := store.BuffEvent{TaskID: "task_2", Source: store.BuffSourceModel, Reason: "失败", CreatedAt: at.Add(time.Hour)}
	if _, err := db.UpdateBuff(ctx, key, "心情", ev, fail); !errors.Is(err, boom) {
		t.Fatalf("UpdateBuff with a failing apply err = %v, want boom", err)
	}
	if _, err := db.UpdateBuff(ctx, key, "精力", ev, fail); !errors.Is(err, boom) {
		t.Fatalf("UpdateBuff of a new bar with a failing apply err = %v, want boom", err)
	}
	if got := getBuffs(t, db, key); len(got) != 1 || got[0].Name != "心情" || got[0].Value != 65 || !got[0].UpdatedAt.Equal(at) {
		t.Fatalf("bars after failing applies = %+v, want only 心情 65 kept", got)
	}
	if got := listBuffEvents(t, db, key, 10); len(got) != 1 || got[0].TaskID != "task_1" {
		t.Fatalf("events after failing applies = %+v, want only task_1's", got)
	}
	if got := setBuff(t, db, key, "心情", 65, 70, ev); got.Value != 70 {
		t.Fatalf("retry of the failed task = %+v, want 心情 70", got)
	}
}

func TestBuffs_Purge(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	cutoff := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)
	panel := func(at time.Time) store.BuffEvent {
		return store.BuffEvent{Source: store.BuffSourcePanel, CreatedAt: at}
	}
	setBuff(t, db, key, "心情", 60, 50, panel(cutoff.Add(-time.Hour)))
	setBuff(t, db, key, "心情", 50, 55, panel(cutoff))
	setBuff(t, db, key, "精力", 70, 40, panel(cutoff.Add(time.Hour)))
	setBuff(t, db, other, "心情", 60, 30, panel(cutoff.Add(-2*time.Hour)))

	n, err := db.PurgeBuffEvents(ctx, cutoff)
	if err != nil || n != 2 {
		t.Fatalf("PurgeBuffEvents = %d, %v; want 2", n, err)
	}
	if got := listBuffEvents(t, db, key, 10); len(got) != 2 || !got[0].CreatedAt.Equal(cutoff.Add(time.Hour)) || !got[1].CreatedAt.Equal(cutoff) {
		t.Fatalf("events kept = %+v, want those at and after the cutoff", got)
	}
	if got := listBuffEvents(t, db, other, 10); len(got) != 0 {
		t.Fatalf("other conversation's events = %+v, want none", got)
	}
	if got := getBuffs(t, db, key); len(got) != 2 {
		t.Fatalf("bars after purging = %+v, want both kept", got)
	}
	if got := getBuffs(t, db, other); len(got) != 1 {
		t.Fatalf("other conversation's bars after purging = %+v, want kept", got)
	}
	if n, err := db.PurgeBuffEvents(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("second PurgeBuffEvents = %d, %v; want 0", n, err)
	}
}

// schemaObjects maps every table and index to its SQL (empty for automatic
// indexes).
func schemaObjects(t *testing.T, db *DB) map[string]string {
	t.Helper()
	rows, err := db.r.Query(`SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'index')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		out[name] = sql
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// migrateOnlyAdding opens a database migrated up to version-1, applies the
// migrations up to version and fails the test if they changed or removed a
// table or index. It returns the open database (closed when the test ends),
// the names of the tables and indexes added, sorted, and the columns of
// every table (see tableInfos).
func migrateOnlyAdding(t *testing.T, version int) (*DB, []string, map[string][]string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, version-1))
	if err != nil {
		t.Fatal(err)
	}
	before, beforeObjects := tableInfos(t, old.r), schemaObjects(t, old)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, version))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	after, afterObjects := tableInfos(t, db.r), schemaObjects(t, db)
	for table, cols := range before {
		if !reflect.DeepEqual(after[table], cols) {
			t.Errorf("table %s changed:\n before %v\n after  %v", table, cols, after[table])
		}
	}
	for name := range beforeObjects {
		if _, ok := afterObjects[name]; !ok {
			t.Errorf("%s removed", name)
		}
	}
	var added []string
	for name, sql := range afterObjects {
		prev, ok := beforeObjects[name]
		if !ok {
			added = append(added, name)
		} else if prev != sql {
			t.Errorf("%s changed:\n before %s\n after  %s", name, prev, sql)
		}
	}
	slices.Sort(added)
	return db, added, after
}

func TestMigration0012_OnlyAddsTables(t *testing.T) {
	db, added, _ := migrateOnlyAdding(t, 12)
	want := []string{
		"conversation_buff_events", "conversation_buff_events_conv", "conversation_buff_events_task",
		"conversation_buffs", "conversation_status", "conversation_timers", "conversation_timers_due",
		"sqlite_autoindex_conversation_buffs_1", "sqlite_autoindex_conversation_status_1",
		"sqlite_autoindex_conversation_timers_1",
	}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}

	insert := func(taskID string) error {
		_, err := db.w.Exec(`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at)
			VALUES (?, '精力', ?, 'panel', 70, 60, '', '')`, privKey("20001"), taskID)
		return err
	}
	for _, id := range []string{"", "", "task_1"} {
		if err := insert(id); err != nil {
			t.Fatalf("insert buff event %q: %v", id, err)
		}
	}
	if err := insert("task_1"); !isUniqueViolation(err) {
		t.Fatalf("second event of a task for one bar err = %v, want a unique violation", err)
	}
}

// dynamicAt is the time the bot-created bar and effect tests start at.
var dynamicAt = time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)

// minute returns dynamicAt plus n minutes.
func minute(n int) time.Time {
	return dynamicAt.Add(time.Duration(n) * time.Minute)
}

func hungerBar(key, taskID string, at time.Time) store.CustomBar {
	return store.CustomBar{ConversationKey: key, Name: "饥饿", Description: "肚子饿的程度", TaskID: taskID, Baseline: 30,
		HalfLife: 6 * time.Hour, CreatedAt: at}
}

func testEffect(key, name, taskID string, at time.Time, d time.Duration) store.Effect {
	return store.Effect{ConversationKey: key, Name: name, Description: "有点冷", TaskID: taskID, StartedAt: at, UntilAt: at.Add(d)}
}

func addBar(t *testing.T, db *DB, b store.CustomBar, initial float64, limit int) {
	t.Helper()
	if err := db.AddCustomBar(context.Background(), b, initial, limit); err != nil {
		t.Fatalf("AddCustomBar %s: %v", b.Name, err)
	}
}

func addEffect(t *testing.T, db *DB, e store.Effect, limit int, now time.Time) {
	t.Helper()
	if err := db.AddEffect(context.Background(), e, limit, now); err != nil {
		t.Fatalf("AddEffect %s: %v", e.Name, err)
	}
}

func listCustomBars(t *testing.T, db *DB, key string) []store.CustomBar {
	t.Helper()
	got, err := db.ListCustomBars(context.Background(), key)
	if err != nil {
		t.Fatalf("ListCustomBars: %v", err)
	}
	return got
}

func listEffects(t *testing.T, db *DB, key string, now time.Time) []store.Effect {
	t.Helper()
	got, err := db.ListEffects(context.Background(), key, now)
	if err != nil {
		t.Fatalf("ListEffects: %v", err)
	}
	return got
}

func sameCustomBars(got, want []store.CustomBar) bool {
	return slices.EqualFunc(got, want, func(a, b store.CustomBar) bool {
		return a.ConversationKey == b.ConversationKey && a.Name == b.Name && a.Description == b.Description &&
			a.TaskID == b.TaskID && a.Baseline == b.Baseline && a.HalfLife == b.HalfLife && a.CreatedAt.Equal(b.CreatedAt)
	})
}

func sameEffects(got, want []store.Effect) bool {
	return slices.EqualFunc(got, want, func(a, b store.Effect) bool {
		return a.ConversationKey == b.ConversationKey && a.Name == b.Name && a.Description == b.Description &&
			a.TaskID == b.TaskID && a.StartedAt.Equal(b.StartedAt) && a.UntilAt.Equal(b.UntilAt)
	})
}

// eventSummary lists the conversation's bar changes newest first as
// "kind name task".
func eventSummary(t *testing.T, db *DB, key string) []string {
	t.Helper()
	var out []string
	for _, ev := range listBuffEvents(t, db, key, 50) {
		out = append(out, ev.Kind+" "+ev.Name+" "+ev.TaskID)
	}
	return out
}

func TestCustomBars_AddListRemove(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	hunger := hungerBar(key, "task_1", dynamicAt)
	sleepy := store.CustomBar{ConversationKey: key, Name: "困意", TaskID: "task_2", Baseline: 20, HalfLife: 90 * time.Minute,
		CreatedAt: dynamicAt.Add(time.Minute)}
	addBar(t, db, hunger, 45, 6)
	addBar(t, db, sleepy, 20, 6)

	if got := listCustomBars(t, db, key); !sameCustomBars(got, []store.CustomBar{hunger, sleepy}) {
		t.Fatalf("ListCustomBars = %+v, want 饥饿 then 困意 in the order they were added", got)
	}
	if got := listCustomBars(t, db, other); len(got) != 0 {
		t.Fatalf("other conversation's bars = %+v, want none", got)
	}
	wantValues := []store.BuffValue{
		{ConversationKey: key, Name: "困意", Value: 20, UpdatedAt: sleepy.CreatedAt},
		{ConversationKey: key, Name: "饥饿", Value: 45, UpdatedAt: dynamicAt},
	}
	if got := getBuffs(t, db, key); !sameBuffs(got, wantValues) {
		t.Fatalf("values = %+v, want each bar at its initial value without day bookkeeping: %+v", got, wantValues)
	}
	wantEvents := []store.BuffEvent{
		{ConversationKey: key, Name: "困意", Kind: store.BuffKindBarAdd, TaskID: "task_2", Source: store.BuffSourceModel,
			Before: 20, After: 20, CreatedAt: sleepy.CreatedAt},
		{ConversationKey: key, Name: "饥饿", Kind: store.BuffKindBarAdd, TaskID: "task_1", Source: store.BuffSourceModel,
			Before: 45, After: 45, CreatedAt: dynamicAt},
	}
	if got := listBuffEvents(t, db, key, 10); !sameBuffEvents(got, wantEvents) {
		t.Fatalf("events = %+v, want %+v", got, wantEvents)
	}

	// A bar it did not add, such as a persona bar's value, is no custom bar.
	setBuff(t, db, key, "心情", 60, 65, store.BuffEvent{Source: store.BuffSourcePanel, CreatedAt: dynamicAt})
	if ok, err := db.RemoveCustomBar(ctx, key, "心情", store.BuffSourcePanel, "", false, minute(5)); err != nil || ok {
		t.Fatalf("RemoveCustomBar of a persona bar = %v, %v; want false", ok, err)
	}
	if ok, err := db.RemoveCustomBar(ctx, other, "饥饿", store.BuffSourcePanel, "", false, minute(5)); err != nil || ok {
		t.Fatalf("RemoveCustomBar in another conversation = %v, %v; want false", ok, err)
	}

	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", false, minute(5)); err != nil || !ok {
		t.Fatalf("RemoveCustomBar = %v, %v; want true", ok, err)
	}
	if got := listCustomBars(t, db, key); !sameCustomBars(got, []store.CustomBar{sleepy}) {
		t.Fatalf("bars after removing 饥饿 = %+v, want only 困意", got)
	}
	if got := getBuffs(t, db, key); len(got) != 2 || got[0].Name != "困意" || got[1].Name != "心情" {
		t.Fatalf("values after removing 饥饿 = %+v, want 困意 and 心情", got)
	}
	wantRemoved := []store.BuffEvent{{ConversationKey: key, Name: "饥饿", Kind: store.BuffKindBarRemove,
		Source: store.BuffSourcePanel, Before: 45, After: 45, CreatedAt: minute(5)}}
	if got := listBuffEvents(t, db, key, 1); !sameBuffEvents(got, wantRemoved) {
		t.Fatalf("newest event = %+v, want the panel's removal of 饥饿 at 45: %+v", got, wantRemoved)
	}
	events := len(listBuffEvents(t, db, key, 50))
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", false, minute(6)); err != nil || ok {
		t.Fatalf("second RemoveCustomBar = %v, %v; want false", ok, err)
	}
	if n := len(listBuffEvents(t, db, key, 50)); n != events {
		t.Fatalf("events after removing nothing = %d, want %d", n, events)
	}

	if err := db.AddCustomBar(ctx, store.CustomBar{Name: "饥饿", CreatedAt: dynamicAt}, 30, 6); err == nil {
		t.Fatal("AddCustomBar without a conversation succeeded")
	}
	if err := db.AddCustomBar(ctx, store.CustomBar{ConversationKey: key, CreatedAt: dynamicAt}, 30, 6); err == nil {
		t.Fatal("AddCustomBar without a name succeeded")
	}
}

func TestCustomBars_RemoveKeepingValue(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	addBar(t, db, hungerBar(key, "task_1", dynamicAt), 45, 6)

	// The persona gained a bar 饥饿, which now shows the stored value: the
	// bot's definition goes, the value stays.
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", true, minute(5)); err != nil || !ok {
		t.Fatalf("RemoveCustomBar keeping the value = %v, %v; want true", ok, err)
	}
	if got := listCustomBars(t, db, key); len(got) != 0 {
		t.Fatalf("bars after the removal = %+v, want none", got)
	}
	wantValues := []store.BuffValue{{ConversationKey: key, Name: "饥饿", Value: 45, UpdatedAt: dynamicAt}}
	if got := getBuffs(t, db, key); !sameBuffs(got, wantValues) {
		t.Fatalf("values after the removal = %+v, want %+v kept", got, wantValues)
	}
	wantRemoved := []store.BuffEvent{{ConversationKey: key, Name: "饥饿", Kind: store.BuffKindBarRemove,
		Source: store.BuffSourcePanel, Before: 45, After: 45, CreatedAt: minute(5)}}
	if got := listBuffEvents(t, db, key, 1); !sameBuffEvents(got, wantRemoved) {
		t.Fatalf("newest event = %+v, want %+v", got, wantRemoved)
	}
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", true, minute(6)); err != nil || ok {
		t.Fatalf("second RemoveCustomBar = %v, %v; want false", ok, err)
	}
}

func TestCustomBars_IdempotentPerTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	hunger := hungerBar(key, "task_1", dynamicAt)
	addBar(t, db, hunger, 45, 6)

	again := hunger
	again.Description, again.Baseline, again.CreatedAt = "另一种说法", 80, dynamicAt.Add(time.Minute)
	addBar(t, db, again, 90, 6)
	if got := listCustomBars(t, db, key); !sameCustomBars(got, []store.CustomBar{hunger}) {
		t.Fatalf("bars after the task added 饥饿 again = %+v, want the first kept", got)
	}
	if got := getBuffs(t, db, key); len(got) != 1 || got[0].Value != 45 {
		t.Fatalf("values after the task added 饥饿 again = %+v, want 45 kept", got)
	}
	if got := eventSummary(t, db, key); !slices.Equal(got, []string{"bar_add 饥饿 task_1"}) {
		t.Fatalf("events after the task added 饥饿 again = %v, want one", got)
	}

	other := hungerBar(key, "task_2", dynamicAt.Add(time.Minute))
	if err := db.AddCustomBar(ctx, other, 10, 6); !errors.Is(err, store.ErrBuffNameTaken) {
		t.Fatalf("AddCustomBar of 饥饿 by another task err = %v, want ErrBuffNameTaken", err)
	}

	// A re-run of the task after the bar was removed adds it no more.
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", false, minute(2)); err != nil || !ok {
		t.Fatalf("RemoveCustomBar = %v, %v", ok, err)
	}
	addBar(t, db, hunger, 45, 6)
	if got := listCustomBars(t, db, key); len(got) != 0 {
		t.Fatalf("bars after a re-run of the task = %+v, want none", got)
	}
	if got := eventSummary(t, db, key); !slices.Equal(got, []string{"bar_remove 饥饿 ", "bar_add 饥饿 task_1"}) {
		t.Fatalf("events after a re-run of the task = %v", got)
	}
}

func TestCustomBars_TaskChangesEachKindOnce(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")

	// A task can add a bar, adjust it and remove it, each once.
	addBar(t, db, hungerBar(key, "task_3", minute(0)), 50, 6)
	adjust := store.BuffEvent{TaskID: "task_3", Source: store.BuffSourceModel, Reason: "饿了", CreatedAt: minute(1)}
	setBuff(t, db, key, "饥饿", 50, 60, adjust)
	keep := func(cur store.BuffValue, _ bool) (store.BuffValue, float64, error) { return cur, cur.Value, nil }
	if _, err := db.UpdateBuff(ctx, key, "饥饿", adjust, keep); !errors.Is(err, store.ErrBuffDuplicate) {
		t.Fatalf("second adjustment by the task err = %v, want ErrBuffDuplicate", err)
	}
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourceModel, "task_3", false, minute(2)); err != nil || !ok {
		t.Fatalf("RemoveCustomBar by the task that added it = %v, %v; want true", ok, err)
	}
	want := []string{"bar_remove 饥饿 task_3", "adjust 饥饿 task_3", "bar_add 饥饿 task_3"}
	if got := eventSummary(t, db, key); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}

	// A re-run of the task changes nothing.
	addBar(t, db, hungerBar(key, "task_3", minute(3)), 50, 6)
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourceModel, "task_3", false, minute(3)); err != nil || ok {
		t.Fatalf("RemoveCustomBar in a re-run of the task = %v, %v; want false", ok, err)
	}
	if got := listCustomBars(t, db, key); len(got) != 0 {
		t.Fatalf("bars after a re-run of the task = %+v, want none", got)
	}
	if got := eventSummary(t, db, key); !slices.Equal(got, want) {
		t.Fatalf("events after a re-run of the task = %v, want %v", got, want)
	}

	// Another task adds the bar back; the task that removed it once cannot
	// remove it a second time.
	addBar(t, db, hungerBar(key, "task_4", minute(4)), 50, 6)
	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourceModel, "task_3", false, minute(5)); !errors.Is(err, store.ErrBuffDuplicate) || ok {
		t.Fatalf("second removal by the task = %v, %v; want ErrBuffDuplicate", ok, err)
	}
	if got := listCustomBars(t, db, key); len(got) != 1 || got[0].TaskID != "task_4" {
		t.Fatalf("bars after the refused removal = %+v, want task_4's 饥饿 kept", got)
	}
	for _, ev := range listBuffEvents(t, db, key, 50) {
		if ev.TaskID == "" {
			t.Errorf("event %+v without its task", ev)
		}
	}
	if n := len(eventSummary(t, db, key)); n != 4 {
		t.Fatalf("events = %d, want 4", n)
	}
}

func TestCustomBars_Limit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	bar := func(name, taskID string) store.CustomBar {
		b := hungerBar(key, taskID, dynamicAt)
		b.Name = name
		return b
	}
	addBar(t, db, bar("饥饿", "task_1"), 30, 2)
	addBar(t, db, bar("困意", "task_2"), 30, 2)
	addBar(t, db, store.CustomBar{ConversationKey: other, Name: "口渴", TaskID: "task_9", Baseline: 30, HalfLife: time.Hour,
		CreatedAt: dynamicAt}, 30, 2)

	if err := db.AddCustomBar(ctx, bar("口渴", "task_3"), 30, 2); !errors.Is(err, store.ErrBuffLimit) {
		t.Fatalf("third bar err = %v, want ErrBuffLimit", err)
	}
	if got := listCustomBars(t, db, key); len(got) != 2 {
		t.Fatalf("bars after the refused one = %+v, want two", got)
	}
	if got := getBuffs(t, db, key); len(got) != 2 {
		t.Fatalf("values after the refused bar = %+v, want two", got)
	}
	if got := eventSummary(t, db, key); len(got) != 2 {
		t.Fatalf("events after the refused bar = %v, want two", got)
	}
	// Adding a bar again by the task that added it is no new bar.
	addBar(t, db, bar("困意", "task_2"), 30, 2)
	if err := db.AddCustomBar(ctx, bar("口渴", "task_3"), 30, 0); !errors.Is(err, store.ErrBuffLimit) {
		t.Fatalf("bar with limit 0 err = %v, want ErrBuffLimit", err)
	}

	if ok, err := db.RemoveCustomBar(ctx, key, "饥饿", store.BuffSourcePanel, "", false, minute(1)); err != nil || !ok {
		t.Fatalf("RemoveCustomBar = %v, %v", ok, err)
	}
	addBar(t, db, bar("口渴", "task_3"), 30, 2)
	if got := listCustomBars(t, db, key); len(got) != 2 || got[0].Name != "困意" || got[1].Name != "口渴" {
		t.Fatalf("bars = %+v, want 困意 and 口渴", got)
	}
}

func TestCustomBars_NameInUse(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	addEffect(t, db, testEffect(key, "着凉", "task_1", dynamicAt, time.Hour), 6, dynamicAt)

	cold := hungerBar(key, "task_2", dynamicAt.Add(30*time.Minute))
	cold.Name = "着凉"
	if err := db.AddCustomBar(ctx, cold, 30, 6); !errors.Is(err, store.ErrBuffNameTaken) {
		t.Fatalf("AddCustomBar named as an active effect err = %v, want ErrBuffNameTaken", err)
	}
	cold.CreatedAt = dynamicAt.Add(time.Hour)
	addBar(t, db, cold, 30, 6)
	if got := listCustomBars(t, db, key); len(got) != 1 || got[0].Name != "着凉" {
		t.Fatalf("bars = %+v, want 着凉 once its effect ran out", got)
	}

	// A value left by a bar the persona no longer has starts over.
	setBuff(t, db, key, "精力", 60, 75, store.BuffEvent{Source: store.BuffSourcePanel, CreatedAt: dynamicAt})
	energy := hungerBar(key, "task_3", dynamicAt.Add(2*time.Hour))
	energy.Name = "精力"
	addBar(t, db, energy, 50, 6)
	values := getBuffs(t, db, key)
	if i := slices.IndexFunc(values, func(v store.BuffValue) bool { return v.Name == "精力" }); i < 0 ||
		!sameBuffs(values[i:i+1], []store.BuffValue{{ConversationKey: key, Name: "精力", Value: 50, UpdatedAt: energy.CreatedAt}}) {
		t.Fatalf("values = %+v, want 精力 at 50 without day bookkeeping", values)
	}
}

func TestEffects_AddListRemove(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	cold := testEffect(key, "着凉", "task_1", dynamicAt, 80*time.Minute)
	full := store.Effect{ConversationKey: key, Name: "吃饱了", TaskID: "task_2", StartedAt: dynamicAt.Add(time.Minute),
		UntilAt: dynamicAt.Add(31 * time.Minute)}
	addEffect(t, db, cold, 6, dynamicAt)
	addEffect(t, db, full, 6, dynamicAt.Add(time.Minute))

	if got := listEffects(t, db, key, dynamicAt.Add(2*time.Minute)); !sameEffects(got, []store.Effect{cold, full}) {
		t.Fatalf("ListEffects = %+v, want 着凉 then 吃饱了", got)
	}
	if got := listEffects(t, db, key, full.UntilAt); !sameEffects(got, []store.Effect{cold}) {
		t.Fatalf("ListEffects when 吃饱了 runs out = %+v, want only 着凉", got)
	}
	if got := listEffects(t, db, other, dynamicAt); len(got) != 0 {
		t.Fatalf("other conversation's effects = %+v, want none", got)
	}
	wantEvents := []store.BuffEvent{
		{ConversationKey: key, Name: "吃饱了", Kind: store.BuffKindEffectAdd, TaskID: "task_2", Source: store.BuffSourceModel,
			CreatedAt: dynamicAt.Add(time.Minute)},
		{ConversationKey: key, Name: "着凉", Kind: store.BuffKindEffectAdd, TaskID: "task_1", Source: store.BuffSourceModel,
			CreatedAt: dynamicAt},
	}
	if got := listBuffEvents(t, db, key, 10); !sameBuffEvents(got, wantEvents) {
		t.Fatalf("events = %+v, want %+v", got, wantEvents)
	}
	if got := getBuffs(t, db, key); len(got) != 0 {
		t.Fatalf("values = %+v, want effects to have none", got)
	}

	if ok, err := db.RemoveEffect(ctx, other, "着凉", store.BuffSourcePanel, "", minute(5)); err != nil || ok {
		t.Fatalf("RemoveEffect in another conversation = %v, %v; want false", ok, err)
	}
	if ok, err := db.RemoveEffect(ctx, key, "着凉", store.BuffSourceModel, "task_3", minute(5)); err != nil || !ok {
		t.Fatalf("RemoveEffect = %v, %v; want true", ok, err)
	}
	if got := listEffects(t, db, key, dynamicAt.Add(2*time.Minute)); !sameEffects(got, []store.Effect{full}) {
		t.Fatalf("effects after removing 着凉 = %+v, want only 吃饱了", got)
	}
	wantRemoved := []store.BuffEvent{{ConversationKey: key, Name: "着凉", Kind: store.BuffKindEffectRemove, TaskID: "task_3",
		Source: store.BuffSourceModel, CreatedAt: minute(5)}}
	if got := listBuffEvents(t, db, key, 1); !sameBuffEvents(got, wantRemoved) {
		t.Fatalf("newest event = %+v, want task_3's removal of 着凉: %+v", got, wantRemoved)
	}
	if ok, err := db.RemoveEffect(ctx, key, "着凉", store.BuffSourceModel, "task_4", minute(6)); err != nil || ok {
		t.Fatalf("second RemoveEffect = %v, %v; want false", ok, err)
	}
	if n := len(eventSummary(t, db, key)); n != 3 {
		t.Fatalf("events = %d, want 3", n)
	}

	if err := db.AddEffect(ctx, store.Effect{Name: "着凉", UntilAt: dynamicAt.Add(time.Hour)}, 6, dynamicAt); err == nil {
		t.Fatal("AddEffect without a conversation succeeded")
	}
	if err := db.AddEffect(ctx, store.Effect{ConversationKey: key, UntilAt: dynamicAt.Add(time.Hour)}, 6, dynamicAt); err == nil {
		t.Fatal("AddEffect without a name succeeded")
	}
}

func TestEffects_IdempotentAndExpiredReplaced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	cold := testEffect(key, "着凉", "task_1", dynamicAt, 80*time.Minute)
	addEffect(t, db, cold, 6, dynamicAt)

	again := testEffect(key, "着凉", "task_1", dynamicAt.Add(time.Minute), 3*time.Hour)
	addEffect(t, db, again, 6, dynamicAt.Add(time.Minute))
	if got := listEffects(t, db, key, dynamicAt); !sameEffects(got, []store.Effect{cold}) {
		t.Fatalf("effects after the task added 着凉 again = %+v, want the first kept", got)
	}
	if got := eventSummary(t, db, key); !slices.Equal(got, []string{"effect_add 着凉 task_1"}) {
		t.Fatalf("events after the task added 着凉 again = %v, want one", got)
	}
	if err := db.AddEffect(ctx, testEffect(key, "着凉", "task_2", dynamicAt.Add(time.Minute), time.Hour), 6,
		dynamicAt.Add(time.Minute)); !errors.Is(err, store.ErrBuffNameTaken) {
		t.Fatalf("AddEffect of an active 着凉 by another task err = %v, want ErrBuffNameTaken", err)
	}

	later := cold.UntilAt
	renewed := testEffect(key, "着凉", "task_2", later, 2*time.Hour)
	renewed.Description = "又着凉了"
	addEffect(t, db, renewed, 6, later)
	if got := listEffects(t, db, key, later); !sameEffects(got, []store.Effect{renewed}) {
		t.Fatalf("effects after 着凉 ran out and was added again = %+v, want the new one", got)
	}
	if n := countRows(t, db, "conversation_effects"); n != 1 {
		t.Fatalf("effect rows = %d, want the expired one replaced", n)
	}
	// A re-run of task_1 adds its effect no more.
	addEffect(t, db, cold, 6, later.Add(time.Minute))
	if got := listEffects(t, db, key, later.Add(time.Minute)); !sameEffects(got, []store.Effect{renewed}) {
		t.Fatalf("effects after a re-run of task_1 = %+v, want task_2's kept", got)
	}
	if got := eventSummary(t, db, key); !slices.Equal(got, []string{"effect_add 着凉 task_2", "effect_add 着凉 task_1"}) {
		t.Fatalf("events = %v", got)
	}

	// A task can remove an effect and add it again, each once and under its
	// ID; a re-run of the task changes nothing.
	if ok, err := db.RemoveEffect(ctx, key, "着凉", store.BuffSourceModel, "task_3", later.Add(30*time.Second)); err != nil || !ok {
		t.Fatalf("RemoveEffect = %v, %v", ok, err)
	}
	longer := testEffect(key, "着凉", "task_3", later.Add(time.Minute), 3*time.Hour)
	addEffect(t, db, longer, 6, longer.StartedAt)
	addEffect(t, db, longer, 6, longer.StartedAt.Add(time.Minute))
	if ok, err := db.RemoveEffect(ctx, key, "着凉", store.BuffSourceModel, "task_3",
		longer.StartedAt.Add(time.Minute)); !errors.Is(err, store.ErrBuffDuplicate) || ok {
		t.Fatalf("second removal by task_3 = %v, %v; want ErrBuffDuplicate", ok, err)
	}
	if got := listEffects(t, db, key, longer.StartedAt.Add(time.Minute)); !sameEffects(got, []store.Effect{longer}) {
		t.Fatalf("effects after task_3 added 着凉 again = %+v, want its new one", got)
	}
	want := []string{"effect_add 着凉 task_3", "effect_remove 着凉 task_3", "effect_add 着凉 task_2", "effect_add 着凉 task_1"}
	if got := eventSummary(t, db, key); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestEffects_Limit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key := privKey("20001")
	addEffect(t, db, testEffect(key, "吃饱了", "task_1", dynamicAt, 30*time.Minute), 2, dynamicAt)
	addEffect(t, db, testEffect(key, "着凉", "task_2", dynamicAt, 2*time.Hour), 2, dynamicAt)

	dizzy := testEffect(key, "头晕", "task_3", dynamicAt.Add(time.Minute), time.Hour)
	if err := db.AddEffect(ctx, dizzy, 2, dizzy.StartedAt); !errors.Is(err, store.ErrBuffLimit) {
		t.Fatalf("third active effect err = %v, want ErrBuffLimit", err)
	}
	if n := countRows(t, db, "conversation_effects"); n != 2 {
		t.Fatalf("effect rows after the refused one = %d, want 2", n)
	}
	if n := len(eventSummary(t, db, key)); n != 2 {
		t.Fatalf("events after the refused effect = %d, want 2", n)
	}

	// 吃饱了 ran out, which frees its place.
	dizzy.StartedAt = dynamicAt.Add(30 * time.Minute)
	dizzy.UntilAt = dizzy.StartedAt.Add(time.Hour)
	addEffect(t, db, dizzy, 2, dizzy.StartedAt)
	if got := listEffects(t, db, key, dizzy.StartedAt); len(got) != 2 || got[0].Name != "着凉" || got[1].Name != "头晕" {
		t.Fatalf("effects = %+v, want 着凉 and 头晕", got)
	}

	addBar(t, db, hungerBar(key, "task_4", dynamicAt), 30, 6)
	if err := db.AddEffect(ctx, testEffect(key, "饥饿", "task_5", dynamicAt.Add(time.Hour), time.Hour), 6,
		dynamicAt.Add(time.Hour)); !errors.Is(err, store.ErrBuffNameTaken) {
		t.Fatalf("AddEffect named as a custom bar err = %v, want ErrBuffNameTaken", err)
	}
}

func TestEffects_PurgeExpired(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	key, other := privKey("20001"), privKey("20002")
	addEffect(t, db, testEffect(key, "吃饱了", "task_1", dynamicAt, 30*time.Minute), 6, dynamicAt)
	addEffect(t, db, testEffect(key, "着凉", "task_2", dynamicAt, 2*time.Hour), 6, dynamicAt)
	addEffect(t, db, testEffect(other, "头晕", "task_3", dynamicAt, 10*time.Minute), 6, dynamicAt)
	events := countRows(t, db, "conversation_buff_events")

	n, err := db.PurgeExpiredEffects(ctx, dynamicAt.Add(30*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("PurgeExpiredEffects = %d, %v; want 2", n, err)
	}
	if n := countRows(t, db, "conversation_effects"); n != 1 {
		t.Fatalf("effect rows = %d, want only 着凉 kept", n)
	}
	if got := listEffects(t, db, key, dynamicAt); len(got) != 1 || got[0].Name != "着凉" {
		t.Fatalf("effects = %+v, want 着凉", got)
	}
	if n := countRows(t, db, "conversation_buff_events"); n != events {
		t.Fatalf("events after purging = %d, want %d", n, events)
	}
	if n, err := db.PurgeExpiredEffects(ctx, dynamicAt.Add(30*time.Minute)); err != nil || n != 0 {
		t.Fatalf("second PurgeExpiredEffects = %d, %v; want 0", n, err)
	}
}

func TestMigration0017_AddsTablesAndEventKind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	old, err := open(ctx, path, migrationsUpTo(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	beforeCols, beforeObjects := tableInfos(t, old.r), schemaObjects(t, old)
	key := privKey("20001")
	if _, err := old.w.Exec(`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at)
		VALUES (?, '心情', 'task_1', 'model', 60, 65, '聊得开心', ?)`, key, fmtTime(dynamicAt)); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, path, migrationsUpTo(t, 17))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	afterCols, afterObjects := tableInfos(t, db.r), schemaObjects(t, db)
	for table, cols := range beforeCols {
		want := cols
		if table == "conversation_buff_events" {
			want = append(slices.Clone(cols), "9|kind|TEXT|1|'adjust'|0")
		}
		if !slices.Equal(afterCols[table], want) {
			t.Errorf("%s columns %v, want %v", table, afterCols[table], want)
		}
	}
	// The task index is replaced by one per kind; the table changed only by
	// gaining its column.
	const taskIndex = "CREATE UNIQUE INDEX conversation_buff_events_task ON conversation_buff_events (task_id, name, kind) WHERE task_id <> ''"
	if got := afterObjects["conversation_buff_events_task"]; got != taskIndex {
		t.Errorf("task index = %s, want %s", got, taskIndex)
	}
	var added []string
	for name, sql := range afterObjects {
		prev, ok := beforeObjects[name]
		switch {
		case !ok:
			added = append(added, name)
		case prev != sql && name != "conversation_buff_events" && name != "conversation_buff_events_task":
			t.Errorf("%s changed:\n before %s\n after  %s", name, prev, sql)
		}
	}
	for name := range beforeObjects {
		if _, ok := afterObjects[name]; !ok {
			t.Errorf("%s removed", name)
		}
	}
	slices.Sort(added)
	want := []string{
		"conversation_custom_bars", "conversation_effects", "conversation_effects_until",
		"sqlite_autoindex_conversation_custom_bars_1", "sqlite_autoindex_conversation_effects_1",
	}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	if got := listBuffEvents(t, db, key, 10); len(got) != 1 || got[0].Kind != store.BuffKindAdjust || got[0].After != 65 {
		t.Fatalf("event kept from before = %+v, want an adjustment", got)
	}

	insert := func(kind string) error {
		_, err := db.w.Exec(`INSERT INTO conversation_buff_events (conversation_key, name, task_id, source, before, after, reason, created_at, kind)
			VALUES (?, '心情', 'task_1', 'model', 0, 0, '', '', ?)`, key, kind)
		return err
	}
	if err := insert(store.BuffKindAdjust); !isUniqueViolation(err) {
		t.Fatalf("second adjustment of a task for one bar err = %v, want a unique violation", err)
	}
	for _, kind := range []string{store.BuffKindBarAdd, store.BuffKindBarRemove} {
		if err := insert(kind); err != nil {
			t.Fatalf("%s of the task for the bar it adjusted: %v", kind, err)
		}
	}
}
