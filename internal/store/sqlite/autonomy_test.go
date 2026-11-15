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
var forgetPrivateTables = []string{"conversation_status", "conversation_timers", "conversation_buffs", "conversation_buff_events"}

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

// seedBuffRows stores a bar value and one change of it for the conversation,
// as the bar methods would.
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

func TestUserMessageTimes_SkipsPanelMessages(t *testing.T) {
	db := newTestDB(t)
	key := privKey("20001")
	base := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	user := memMessage(key, "user_20001", "20001", false)
	user.OccurredAt = base
	// The panel takes over the chat and writes as its user.
	panel := memMessage(key, "user_20001", "20001", false)
	panel.OccurredAt, panel.Channel = base.Add(time.Hour), store.ChannelPanel
	memInsertMessages(t, db, user, panel)
	got, err := db.UserMessageTimes(context.Background(), key, time.Time{}, 10)
	if err != nil {
		t.Fatalf("UserMessageTimes: %v", err)
	}
	if !slices.EqualFunc(got, []time.Time{base}, time.Time.Equal) {
		t.Fatalf("times = %v, want only the platform message at %v", got, base)
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
		return a.ConversationKey == b.ConversationKey && a.Name == b.Name && a.TaskID == b.TaskID && a.Source == b.Source &&
			a.Before == b.Before && a.After == b.After && a.Reason == b.Reason && a.CreatedAt.Equal(b.CreatedAt)
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

	// The event's Before and After come from apply, never from the event passed in.
	first := store.BuffEvent{TaskID: "task_1", Source: store.BuffSourceModel, Before: 1, After: 99, Reason: "聊得开心", CreatedAt: at}
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

	wantEvents := []store.BuffEvent{
		{ConversationKey: key, Name: "精力", Source: store.BuffSourcePanel, Before: 70, After: 40, Reason: "手动调整", CreatedAt: panel.CreatedAt},
		{ConversationKey: key, Name: "心情", TaskID: "task_2", Source: store.BuffSourceModel, Before: 63.25, After: 72.5, Reason: "被夸了", CreatedAt: second.CreatedAt},
		{ConversationKey: key, Name: "心情", TaskID: "task_1", Source: store.BuffSourceModel, Before: 60, After: 65, Reason: "聊得开心", CreatedAt: at},
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
	err := db.r.QueryRow(`EXPLAIN QUERY PLAN `+buffTaskChangesQuery, "task_1", "心情").
		Scan(new(int), new(int), new(int), &detail)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if want := "SEARCH conversation_buff_events USING COVERING INDEX conversation_buff_events_task (task_id=? AND name=?)"; detail != want {
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
