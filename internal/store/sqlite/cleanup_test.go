package sqlite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const day = 24 * time.Hour

// seedOldTask stores a message and a task with status, last updated at
// updated, as left behind by earlier traffic.
func seedOldTask(t *testing.T, db *DB, n, status string, updated time.Time) string {
	t.Helper()
	in := testIngest("k"+n, "m"+n, "p"+n)
	in.Event.CreatedAt = updated
	in.Message.OccurredAt, in.Message.CreatedAt = updated, updated
	id := "task_" + n
	in.NewTask = &store.TaskRecord{
		ID: id, BotInstanceID: testBot, ConversationKey: testConvKey, Status: status,
		TriggerMessageIDs: []string{"m" + n}, EventOccurredAt: updated, CreatedAt: updated, UpdatedAt: updated,
	}
	if _, err := db.Ingest(context.Background(), in); err != nil {
		t.Fatalf("Ingest task %s: %v", n, err)
	}
	return id
}

// taskAction is an action of taskID created at created with status.
func taskAction(id, taskID, status string, created time.Time) store.OutboxRecord {
	a := testAction(id, created)
	a.TaskID, a.Status = taskID, status
	return a
}

func taskExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	_, err := db.GetTask(context.Background(), testBot, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetTask %s: %v", id, err)
	}
	return err == nil
}

func actionExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	_, err := db.GetOutbox(context.Background(), testBot, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetOutbox %s: %v", id, err)
	}
	return err == nil
}

func TestPurgeFinishedTasks_OnlyOldFinishedTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, cutoff := now.Add(-40*day), now.Add(-30*day)

	succeeded := seedOldTask(t, db, "1", conloomapi.TaskSucceeded, old)
	failed := seedOldTask(t, db, "2", conloomapi.TaskFailed, old)
	cancelled := seedOldTask(t, db, "3", conloomapi.TaskCancelled, old)
	queued := seedOldTask(t, db, "4", conloomapi.TaskQueued, old)
	running := seedOldTask(t, db, "5", conloomapi.TaskRunning, old)
	recent := seedOldTask(t, db, "6", conloomapi.TaskSucceeded, now.Add(-29*day))
	livePending := seedOldTask(t, db, "7", conloomapi.TaskSucceeded, old)
	liveLeased := seedOldTask(t, db, "8", conloomapi.TaskSucceeded, old)
	insertActions(t, db,
		taskAction("act_acked", succeeded, store.OutboxAcked, old),
		taskAction("act_expired", failed, store.OutboxExpired, old),
		taskAction("act_recent", recent, store.OutboxAcked, now.Add(-29*day)),
		taskAction("act_pending", livePending, store.OutboxPending, old),
		taskAction("act_leased", liveLeased, store.OutboxLeased, old),
	)

	tasks, actions, err := db.PurgeFinishedTasks(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("PurgeFinishedTasks: %v", err)
	}
	if tasks != 3 || actions != 2 {
		t.Fatalf("PurgeFinishedTasks = %d tasks, %d actions; want 3, 2", tasks, actions)
	}
	for _, id := range []string{succeeded, failed, cancelled} {
		if taskExists(t, db, id) {
			t.Errorf("task %s kept, want deleted", id)
		}
		if got, err := db.ActionsForTask(ctx, id); err != nil || len(got) != 0 {
			t.Errorf("ActionsForTask(%s) = %v, %v; want none", id, got, err)
		}
	}
	for _, id := range []string{queued, running, recent, livePending, liveLeased} {
		if !taskExists(t, db, id) {
			t.Errorf("task %s deleted, want kept", id)
		}
	}
	for _, id := range []string{"act_recent", "act_pending", "act_leased"} {
		if !actionExists(t, db, id) {
			t.Errorf("action %s deleted, want kept", id)
		}
	}
	unfinished, err := db.ListUnfinishedTasks(ctx)
	if err != nil {
		t.Fatalf("ListUnfinishedTasks: %v", err)
	}
	var got []string
	for _, u := range unfinished {
		got = append(got, u.ID)
	}
	if !slices.Equal(got, []string{queued, running}) {
		t.Errorf("unfinished = %v, want %v", got, []string{queued, running})
	}
	if n := countRows(t, db, "messages"); n != 8 {
		t.Errorf("messages = %d, want all 8 kept", n)
	}
}

func TestPurgeFinishedTasks_Limit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-40 * day)
	for i := range 5 {
		id := seedOldTask(t, db, fmt.Sprint(i), conloomapi.TaskSucceeded, old)
		insertActions(t, db,
			taskAction(fmt.Sprintf("act_%d_a", i), id, store.OutboxAcked, old),
			taskAction(fmt.Sprintf("act_%d_b", i), id, store.OutboxExpired, old))
	}
	for _, want := range []int{2, 2, 1, 0} {
		tasks, actions, err := db.PurgeFinishedTasks(ctx, old.Add(time.Hour), 2)
		if err != nil {
			t.Fatalf("PurgeFinishedTasks: %v", err)
		}
		if tasks != want || actions != 2*want {
			t.Fatalf("PurgeFinishedTasks = %d tasks, %d actions; want %d, %d", tasks, actions, want, 2*want)
		}
	}
	if n := countRows(t, db, "tasks"); n != 0 {
		t.Fatalf("tasks left = %d", n)
	}
	if tasks, actions, err := db.PurgeFinishedTasks(ctx, old.Add(time.Hour), 0); tasks != 0 || actions != 0 || err != nil {
		t.Fatalf("limit 0 = %d, %d, %v; want nothing", tasks, actions, err)
	}
}

func TestPurgeOutbox_OnlyOldFinishedActionsWithoutTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, cutoff := now.Add(-40*day), now.Add(-30*day)
	kept := seedOldTask(t, db, "1", conloomapi.TaskSucceeded, now)
	insertActions(t, db,
		taskAction("orphan_acked", "task_gone", store.OutboxAcked, old),
		taskAction("orphan_expired", "task_gone", store.OutboxExpired, old),
		taskAction("orphan_expired_2", "task_gone_2", store.OutboxExpired, old),
		taskAction("orphan_pending", "task_gone", store.OutboxPending, old),
		taskAction("orphan_leased", "task_gone", store.OutboxLeased, old),
		taskAction("orphan_recent", "task_gone", store.OutboxAcked, now.Add(-29*day)),
		taskAction("with_task", kept, store.OutboxAcked, old),
	)

	n, err := db.PurgeOutbox(ctx, cutoff, 2)
	if err != nil || n != 2 {
		t.Fatalf("PurgeOutbox = %d, %v; want 2 (limit)", n, err)
	}
	if n, err = db.PurgeOutbox(ctx, cutoff, 2); err != nil || n != 1 {
		t.Fatalf("second PurgeOutbox = %d, %v; want 1", n, err)
	}
	if n, err = db.PurgeOutbox(ctx, cutoff, 2); err != nil || n != 0 {
		t.Fatalf("third PurgeOutbox = %d, %v; want 0", n, err)
	}
	for _, id := range []string{"orphan_acked", "orphan_expired", "orphan_expired_2"} {
		if actionExists(t, db, id) {
			t.Errorf("action %s kept, want deleted", id)
		}
	}
	for _, id := range []string{"orphan_pending", "orphan_leased", "orphan_recent", "with_task"} {
		if !actionExists(t, db, id) {
			t.Errorf("action %s deleted, want kept", id)
		}
	}
}

func TestPurgeAudit_OldOnlyWithLimit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i, at := range []time.Time{now.Add(-100 * day), now.Add(-95 * day), now.Add(-91 * day), now.Add(-89 * day), now} {
		err := db.InsertAudit(ctx, store.AuditEvent{
			ID: fmt.Sprintf("aud_%d", i), BotInstanceID: testBot, Kind: "policy.update", Actor: "29999",
			Target: testConvKey, Detail: "{}", CreatedAt: at,
		})
		if err != nil {
			t.Fatalf("InsertAudit: %v", err)
		}
	}
	cutoff := now.Add(-90 * day)
	if n, err := db.PurgeAudit(ctx, cutoff, 2); err != nil || n != 2 {
		t.Fatalf("PurgeAudit = %d, %v; want 2 (limit)", n, err)
	}
	if n, err := db.PurgeAudit(ctx, cutoff, 2); err != nil || n != 1 {
		t.Fatalf("second PurgeAudit = %d, %v; want 1", n, err)
	}
	if n, err := db.PurgeAudit(ctx, cutoff, 2); err != nil || n != 0 {
		t.Fatalf("third PurgeAudit = %d, %v; want 0", n, err)
	}
	if n := countRows(t, db, "audit_events"); n != 2 {
		t.Fatalf("audit events left = %d, want 2", n)
	}
}

// TestPurge_KeepsLongTermData runs every cleanup with a cutoff after all
// rows were written: only tasks, their actions and audit events go; messages,
// memory, policies, personas and member state stay.
func TestPurge_KeepsLongTermData(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-400 * day)
	id := seedOldTask(t, db, "1", conloomapi.TaskSucceeded, old)
	insertActions(t, db, taskAction("act_1", id, store.OutboxAcked, old))
	ingestTask(t, db, "2", conloomapi.TaskQueued)
	if err := db.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: testConvKey, Summary: "大家在聊周末出游", CursorSeq: 1, LastRunAt: old}); err != nil {
		t.Fatalf("PutGroupMemory: %v", err)
	}
	if err := db.ForgetMember(ctx, testConvKey, "20002", old); err != nil {
		t.Fatalf("ForgetMember: %v", err)
	}
	if _, err := db.w.ExecContext(ctx, `INSERT INTO memory_revisions
		(conversation_key, platform_user_id, field, old_value, new_value, source, reason, created_at)
		VALUES (?, '20001', 'note', '', '喜欢爬山', 'model', 'test', ?)`, testConvKey, fmtTime(old)); err != nil {
		t.Fatalf("insert memory revision: %v", err)
	}
	if _, err := db.AppendPolicyVersion(ctx, store.PolicyVersion{
		ConversationKey: testConvKey, Policy: conloomapi.DefaultGroupPolicy(), Reason: "test", UpdatedBy: "29999", CreatedAt: old,
	}); err != nil {
		t.Fatalf("AppendPolicyVersion: %v", err)
	}
	if _, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
		ConversationKey: testConvKey, TemplateName: "default", Persona: conloomapi.DefaultPersona(),
		Reason: "test", UpdatedBy: "29999", CreatedAt: old,
	}); err != nil {
		t.Fatalf("AppendPersonaVersion: %v", err)
	}
	if err := db.PutMember(ctx, store.MemberRecord{
		ConversationKey: testConvKey, PlatformUserID: "20001", LastEngagedAt: old, ProactiveLevel: "low", UpdatedAt: old,
	}); err != nil {
		t.Fatalf("PutMember: %v", err)
	}
	if err := db.InsertAudit(ctx, store.AuditEvent{ID: "aud_1", BotInstanceID: testBot, Kind: "policy.update", CreatedAt: old}); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}

	kept := []string{
		"messages", "conversations", "users", "events", "group_memory", "member_memory", "memory_revisions",
		"group_policies", "persona_instances", "persona_versions", "conversation_members",
	}
	before := map[string]int{}
	for _, table := range kept {
		before[table] = countRows(t, db, table)
		if before[table] == 0 {
			t.Fatalf("table %s has no rows to protect", table)
		}
	}

	cutoff := time.Now().UTC().Add(time.Hour)
	if _, _, err := db.PurgeFinishedTasks(ctx, cutoff, 1000); err != nil {
		t.Fatalf("PurgeFinishedTasks: %v", err)
	}
	if _, err := db.PurgeOutbox(ctx, cutoff, 1000); err != nil {
		t.Fatalf("PurgeOutbox: %v", err)
	}
	if _, err := db.PurgeAudit(ctx, cutoff, 1000); err != nil {
		t.Fatalf("PurgeAudit: %v", err)
	}

	for _, table := range kept {
		if got := countRows(t, db, table); got != before[table] {
			t.Errorf("%s rows = %d, want %d", table, got, before[table])
		}
	}
	if got := countRows(t, db, "tasks"); got != 1 {
		t.Errorf("tasks = %d, want only the queued one", got)
	}
	if got := countRows(t, db, "outbox_actions") + countRows(t, db, "audit_events"); got != 0 {
		t.Errorf("outbox actions and audit events left = %d, want 0", got)
	}
	if m, err := db.GetMessage(ctx, testBot, "m1"); err != nil || m.Text != "hello m1" {
		t.Errorf("trigger message of a purged task = %+v, %v", m, err)
	}
}
