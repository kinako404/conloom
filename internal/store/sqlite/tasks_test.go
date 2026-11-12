package sqlite

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func ingestTask(t *testing.T, db *DB, n, status string) {
	t.Helper()
	in := testIngest("k"+n, "m"+n, "p"+n)
	in.NewTask = &store.TaskRecord{
		ID: "task_" + n, BotInstanceID: testBot, ConversationKey: testConvKey,
		Status: status, TriggerMessageIDs: []string{"m" + n},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := db.Ingest(context.Background(), in); err != nil {
		t.Fatalf("Ingest task %s: %v", n, err)
	}
}

func testAction(id string, now time.Time) store.OutboxRecord {
	return store.OutboxRecord{
		Action: conloomapi.Action{
			ActionID:                 id,
			Type:                     conloomapi.ActionMessageSend,
			ConversationID:           testConvID,
			ConversationType:         conloomapi.ConvGroup,
			PlatformConversationID:   testGroup,
			ReplyToMessageID:         "m1",
			ReplyToPlatformMessageID: "p1",
			Content:                  conloomapi.ActionContent{Text: "hi there"},
			CreatedAt:                now,
		},
		BotInstanceID:   testBot,
		ConversationKey: testConvKey,
		TaskID:          "task_1",
		Platform:        testPlatform,
		CreatedAt:       now,
		ExpiresAt:       now.Add(10 * time.Minute),
	}
}

func TestCompleteTask_AtomicWithActions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ingestTask(t, db, "1", conloomapi.TaskRunning)
	now := time.Now().UTC()
	act := testAction("act_1", now)
	meta := conloomapi.TaskMeta{SelectedModelRole: "chat", RouteReason: "mention", TraceID: "trace_1"}
	err := db.CompleteTask(ctx, store.TaskCompletion{
		TaskID: "task_1", Status: conloomapi.TaskSucceeded, Meta: meta, Actions: []store.OutboxRecord{act},
	})
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	task, err := db.GetTask(ctx, testBot, "task_1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != conloomapi.TaskSucceeded || task.Meta != meta {
		t.Fatalf("task = %+v", task)
	}
	actions, err := db.ActionsForTask(ctx, "task_1")
	if err != nil {
		t.Fatalf("ActionsForTask: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(actions))
	}
	got := actions[0]
	if !got.CreatedAt.Equal(act.Action.CreatedAt) {
		t.Fatalf("action CreatedAt = %v, want %v", got.CreatedAt, act.Action.CreatedAt)
	}
	got.CreatedAt = act.Action.CreatedAt
	if !reflect.DeepEqual(got, act.Action) {
		t.Fatalf("action = %+v\nwant %+v", got, act.Action)
	}
	var status, platform string
	var attempts int
	err = db.r.QueryRow(`SELECT status, platform, attempts FROM outbox_actions WHERE action_id = ?`, "act_1").
		Scan(&status, &platform, &attempts)
	if err != nil || status != store.OutboxPending || platform != testPlatform || attempts != 0 {
		t.Fatalf("outbox row = %q %q %d, %v", status, platform, attempts, err)
	}

	ingestTask(t, db, "2", conloomapi.TaskRunning)
	dup := testAction("act_1", now)
	dup.TaskID = "task_2"
	err = db.CompleteTask(ctx, store.TaskCompletion{
		TaskID: "task_2", Status: conloomapi.TaskSucceeded, Actions: []store.OutboxRecord{dup},
	})
	if err == nil {
		t.Fatal("CompleteTask with duplicate action_id succeeded")
	}
	task2, err := db.GetTask(ctx, testBot, "task_2")
	if err != nil || task2.Status != conloomapi.TaskRunning {
		t.Fatalf("task_2 after failed completion = %+v, %v; want running", task2, err)
	}

	if err := db.CompleteTask(ctx, store.TaskCompletion{TaskID: "missing", Status: conloomapi.TaskFailed}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CompleteTask missing err = %v, want ErrNotFound", err)
	}
}

func TestListUnfinishedTasks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ingestTask(t, db, "1", conloomapi.TaskQueued)
	ingestTask(t, db, "2", conloomapi.TaskQueued)
	ingestTask(t, db, "3", conloomapi.TaskQueued)
	ingestTask(t, db, "4", conloomapi.TaskQueued)
	ingestTask(t, db, "5", conloomapi.TaskQueued)
	for id, status := range map[string]string{
		"task_2": conloomapi.TaskRunning,
		"task_3": conloomapi.TaskSucceeded,
		"task_4": conloomapi.TaskFailed,
		"task_5": conloomapi.TaskCancelled,
	} {
		if err := db.SetTaskStatus(ctx, id, status); err != nil {
			t.Fatalf("SetTaskStatus %s: %v", id, err)
		}
	}
	if err := db.SetTaskStatus(ctx, "missing", conloomapi.TaskRunning); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetTaskStatus missing err = %v, want ErrNotFound", err)
	}
	tasks, err := db.ListUnfinishedTasks(ctx)
	if err != nil {
		t.Fatalf("ListUnfinishedTasks: %v", err)
	}
	var got []string
	for _, tk := range tasks {
		got = append(got, tk.ID+"="+tk.Status)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"task_1=queued", "task_2=running"}) {
		t.Fatalf("unfinished = %v", got)
	}
}
