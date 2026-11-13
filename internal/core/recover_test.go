package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// seedTask stores a group message from user and a task triggered by it, as
// left behind by a process that stopped before finishing the task.
func (e *testEnv) seedTask(t *testing.T, user, text, command, status string, occurred time.Time) (taskID, msgID string) {
	t.Helper()
	pmid := strconv.FormatInt(1000+e.seq.Add(1), 10)
	msgID = "msg_" + pmid
	taskID = ids.New("task")
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + pmid, EventID: "evt_" + pmid},
		Conversation: &store.ConversationRecord{
			BotInstanceID: testBot, ID: testConvID, Key: testConvKey, Type: conloomapi.ConvGroup,
			Platform: "qq", PlatformConversationID: "10001",
		},
		Message: &store.MessageRecord{
			ID: msgID, BotInstanceID: testBot, ConversationKey: testConvKey, Platform: "qq",
			PlatformMessageID: pmid, ActorID: "user_" + user, PlatformUserID: user, DisplayName: "用户" + user,
			Text: text, OccurredAt: occurred,
		},
		NewTask: &store.TaskRecord{
			ID: taskID, BotInstanceID: testBot, ConversationKey: testConvKey, Status: status,
			ActorPlatformUserID: user, Command: command, TriggerMessageIDs: []string{msgID}, EventOccurredAt: occurred,
		},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return taskID, msgID
}

func TestRecover_RequeuesFreshAndFailsStale(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	now := time.Now().UTC()
	fresh, freshMsg := env.seedTask(t, "20001", "还在吗", "", conloomapi.TaskRunning, now.Add(-10*time.Second))
	stale, _ := env.seedTask(t, "20002", "早就过时了", "", conloomapi.TaskQueued, now.Add(-5*time.Minute))
	help, _ := env.seedTask(t, "20002", "/help", "help", conloomapi.TaskQueued, now.Add(-5*time.Second))

	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	task := env.waitTask(t, fresh)
	if task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 || task.Actions[0].ReplyToMessageID != freshMsg {
		t.Fatalf("fresh task = %+v", task)
	}
	st := env.getTask(t, stale)
	if st.Status != conloomapi.TaskFailed || st.Error == nil || st.Error.Code != "STALE" || len(st.Actions) != 0 {
		t.Fatalf("stale task = %+v", st)
	}
	ht := env.waitTask(t, help)
	if ht.Status != conloomapi.TaskSucceeded || len(ht.Actions) != 1 || ht.Actions[0].Content.Text != CopyHelp("织语") {
		t.Fatalf("command task = %+v", ht)
	}
	time.Sleep(50 * time.Millisecond)
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
}

func TestClose_LeavesRunningTaskForRecovery(t *testing.T) {
	release := make(chan struct{})
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { <-release; return 200, fakeReply }})
	t.Cleanup(func() { close(release) })
	resp := env.submit(t, env.groupEvent("20001", "你好", mentionBot))
	waitUntil(t, "model called", func() bool { return env.short.Calls() == 1 })
	env.o.Close()
	rec, err := env.st.GetTask(context.Background(), testBot, resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != conloomapi.TaskRunning {
		t.Fatalf("status after Close = %q, want running", rec.Status)
	}
	if _, _, err := env.submitRaw(t, "after-close", env.groupEvent("20001", "还在吗", mentionBot)); err == nil {
		t.Fatal("Submit after Close succeeded")
	}
}

func TestRecover_SecondCallFails(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: func(c *config.Config) {
		c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 2
	}})
	id, _ := env.seedTask(t, "20001", "还在吗", "", conloomapi.TaskRunning, time.Now().UTC().Add(-10*time.Second))
	ctx := context.Background()
	if err := env.o.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := env.o.Recover(ctx); !errors.Is(err, ErrAlreadyRecovered) {
		t.Fatalf("second Recover: err = %v, want ErrAlreadyRecovered", err)
	}
	env.waitTask(t, id)
	time.Sleep(100 * time.Millisecond)
	if task := env.getTask(t, id); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if got := env.hooks.completed(id); len(got) != 1 {
		t.Fatalf("completions = %v, want one", got)
	}
	if n := env.short.Calls(); n != 1 {
		t.Fatalf("model calls = %d, want 1", n)
	}
}

func TestRecover_LeavesLiveTasksAlone(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) { <-release; return 200, fakeReply },
		configure: func(c *config.Config) {
			c.DefaultGroupPolicy.Concurrency.MaxInteractiveRequests = 2
			c.DefaultGroupPolicy.Reply.Debounce = conloomapi.Duration{Duration: 300 * time.Millisecond}
		},
	})
	t.Cleanup(unblock)
	running := env.submit(t, env.groupEvent("20001", "第一个问题", mentionBot))
	waitUntil(t, "first task running", func() bool { return env.short.Calls() == 1 })
	queued := env.submit(t, env.groupEvent("20002", "第二个问题", mentionBot))
	merged := env.submit(t, env.groupEvent("20002", "补充一句"))
	if !merged.Merged || merged.TaskID != queued.TaskID {
		t.Fatalf("follow-up = %+v, want merged into %s", merged, queued.TaskID)
	}

	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	unblock()

	for _, id := range []string{running.TaskID, queued.TaskID} {
		env.waitTask(t, id)
	}
	time.Sleep(200 * time.Millisecond)
	for _, id := range []string{running.TaskID, queued.TaskID} {
		if task := env.getTask(t, id); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("task %s = %+v", id, task)
		}
		if got := env.hooks.completed(id); len(got) != 1 {
			t.Fatalf("task %s completions = %v, want one", id, got)
		}
	}
	if n := env.short.Calls(); n != 2 {
		t.Fatalf("model calls = %d, want 2", n)
	}
}

// gaugeValue is the value of the gauge name in reg.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name && len(mf.GetMetric()) == 1 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return -1
}

func TestRecover_BackfillsTheKeywordIndexInTheBackground(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	var mu sync.Mutex
	var batches []int
	remaining := []int{1200, 700, 200, 0}
	fail := true
	env.hooks.backfill = func(batch int) (int, int, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			fail = false
			return 0, 0, errors.New("test: database busy")
		}
		batches = append(batches, batch)
		if len(batches) > len(remaining) {
			return 0, 0, nil
		}
		return 500, remaining[len(batches)-1], nil
	}
	env.o.ftsRetry = 10 * time.Millisecond
	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	waitUntil(t, "backfill caught up", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(batches) >= len(remaining)
	})
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	got := slices.Clone(batches)
	mu.Unlock()
	if len(got) != len(remaining) || got[0] != 500 {
		t.Fatalf("batches = %v, want %d batches of 500 and then a pause", got, len(remaining))
	}
	if v := gaugeValue(t, env.reg, "conloom_fts_backfill_remaining"); v != 0 {
		t.Fatalf("conloom_fts_backfill_remaining = %v, want 0", v)
	}
	logs := env.logs.String()
	if !strings.Contains(logs, "keyword index backfill failed") || !strings.Contains(logs, "keyword index backfilled") {
		t.Fatalf("logs lack the backfill failure and completion:\n%s", logs)
	}
}

func TestRecover_SkipsCommandInProgress(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	entered, release := env.blockFirstCompletion(t, func(c store.TaskCompletion) bool { return len(c.Actions) > 0 })
	ev := env.groupEvent("20001", "/help")
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		resp conloomapi.EventAccepted
		err  error
	}
	submitted := make(chan result, 1)
	go func() {
		resp, _, err := env.o.Submit(context.Background(), testBot, "help", body, ev)
		submitted <- result{resp, err}
	}()
	id := waitEntered(t, entered)

	if err := env.o.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	release()
	if r := <-submitted; r.err != nil || r.resp.TaskID != id {
		t.Fatalf("Submit = %+v, %v (paused task %s)", r.resp, r.err, id)
	}
	time.Sleep(100 * time.Millisecond)
	if task := env.getTask(t, id); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("command task = %+v", task)
	}
	if got := env.hooks.completed(id); len(got) != 1 {
		t.Fatalf("completions = %v, want one", got)
	}
}
