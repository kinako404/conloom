package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const cleanupDay = 24 * time.Hour

// seedTaskAt stores a group message from user and a task with status that
// was created and last updated at at.
func (e *testEnv) seedTaskAt(t *testing.T, user, status string, at time.Time) (taskID, msgID string) {
	t.Helper()
	pmid := fmt.Sprint(1000 + e.seq.Add(1))
	msgID, taskID = "msg_"+pmid, ids.New("task")
	_, err := e.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + pmid, EventID: "evt_" + pmid, CreatedAt: at},
		Conversation: &store.ConversationRecord{
			BotInstanceID: testBot, ID: testConvID, Key: testConvKey, Type: conloomapi.ConvGroup,
			Platform: "qq", PlatformConversationID: "10001",
		},
		Message: &store.MessageRecord{
			ID: msgID, BotInstanceID: testBot, ConversationKey: testConvKey, Platform: "qq",
			PlatformMessageID: pmid, ActorID: "user_" + user, PlatformUserID: user, DisplayName: "用户" + user,
			Text: "消息" + pmid, OccurredAt: at, CreatedAt: at,
		},
		NewTask: &store.TaskRecord{
			ID: taskID, BotInstanceID: testBot, ConversationKey: testConvKey, Status: status,
			ActorPlatformUserID: user, TriggerMessageIDs: []string{msgID}, EventOccurredAt: at, CreatedAt: at, UpdatedAt: at,
		},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return taskID, msgID
}

// completeAndAck completes a stored task with one action and acks it, as a
// reply and its delivery would; both are stamped with the wall clock.
func (e *testEnv) completeAndAck(t *testing.T, taskID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	actionID := ids.New("act")
	err := e.st.CompleteTask(ctx, store.TaskCompletion{TaskID: taskID, Status: conloomapi.TaskSucceeded, Actions: []store.OutboxRecord{{
		Action:        conloomapi.Action{ActionID: actionID, Type: conloomapi.ActionMessageSend, Content: conloomapi.ActionContent{Text: "好的"}},
		BotInstanceID: testBot, ConversationKey: testConvKey, Platform: "qq", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}}})
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, err := e.st.AckOutbox(ctx, testBot, actionID, conloomapi.AckRequest{Status: conloomapi.AckSent}, nil, now); err != nil {
		t.Fatalf("AckOutbox: %v", err)
	}
}

func (e *testEnv) taskGone(t *testing.T, id string) bool {
	t.Helper()
	_, err := e.o.GetTask(context.Background(), testBot, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetTask %s: %v", id, err)
	}
	return errors.Is(err, store.ErrNotFound)
}

func (e *testEnv) insertAudit(t *testing.T, id string, at time.Time) {
	t.Helper()
	err := e.st.InsertAudit(context.Background(), store.AuditEvent{ID: id, BotInstanceID: testBot, Kind: "policy.update", CreatedAt: at})
	if err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
}

func TestCleanup_DeletesOnlyExpiredFinishedData(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now})
	ctx := context.Background()

	done, doneMsg := env.seedTaskAt(t, "20001", conloomapi.TaskRunning, time.Now().UTC())
	env.completeAndAck(t, done)
	staleQueued, _ := env.seedTaskAt(t, "20002", conloomapi.TaskQueued, time.Now().UTC())
	env.insertAudit(t, "aud_old", time.Now().UTC())
	clock.advance(91 * cleanupDay)

	now := clock.now().UTC()
	recent, _ := env.seedTaskAt(t, "20001", conloomapi.TaskSucceeded, now.Add(-29*cleanupDay))
	pending, _ := env.seedTaskAt(t, "20002", conloomapi.TaskQueued, now.Add(-10*time.Second))
	env.insertAudit(t, "aud_recent", now.Add(-89*cleanupDay))

	env.o.cleanup()

	if !env.taskGone(t, done) {
		t.Error("task finished 91 days ago kept, want deleted")
	}
	if m, err := env.st.GetMessage(ctx, testBot, doneMsg); err != nil || m.Text == "" {
		t.Errorf("trigger message of the deleted task = %+v, %v; want kept", m, err)
	}
	for _, id := range []string{staleQueued, recent, pending} {
		if env.taskGone(t, id) {
			t.Errorf("task %s deleted, want kept", id)
		}
	}
	for table, want := range map[string]float64{"tasks": 1, "outbox_actions": 1, "audit_events": 1} {
		if got := env.counter(t, "conloom_cleanup_deleted_total", "table", table); got != want {
			t.Errorf("cleanup_deleted_total{%s} = %v, want %v", table, got, want)
		}
	}
	logs := env.logs.String()
	for _, want := range []string{"cleaned up operational records", "tasks=1", "outbox_actions=1", "audit_events=1", "more=false"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}

	if err := env.o.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if task := env.waitTask(t, pending); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("recovered task = %+v, error %+v", task, task.Error)
	}
	// The test clock runs ahead of the wall clock CompleteTask stamps, so the
	// maintenance Recover starts may already have cleaned the stale task up.
	if got := env.hooks.completed(staleQueued); len(got) != 1 || got[0] != conloomapi.TaskFailed {
		t.Fatalf("old unfinished task completions after Recover = %v, want failed (stale)", got)
	}
}

func TestCleanup_BoundedBatchesPerRound(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now})
	env.o.cleanupBatch, env.o.cleanupMaxBatches = 2, 2
	old := time.Now().UTC()
	for range 5 {
		env.seedTaskAt(t, "20001", conloomapi.TaskSucceeded, old)
	}
	clock.advance(31 * cleanupDay)

	env.o.cleanup()
	if got := env.counter(t, "conloom_cleanup_deleted_total", "table", "tasks"); got != 4 {
		t.Fatalf("first round deleted %v tasks, want 4 (2 batches of 2)", got)
	}
	if !strings.Contains(env.logs.String(), "more=true") {
		t.Errorf("first round log does not report more rows:\n%s", env.logs.String())
	}
	env.o.cleanup()
	if got := env.counter(t, "conloom_cleanup_deleted_total", "table", "tasks"); got != 5 {
		t.Fatalf("after second round deleted %v tasks, want 5", got)
	}
}

// TestCleanup_KeepsTasksOfReplayableEvents keeps tasks as long as the
// idempotency records whose stored responses name them, so a replayed event
// never points at a deleted task.
func TestCleanup_KeepsTasksOfReplayableEvents(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now, configure: func(c *config.Config) {
		c.Runtime.IdempotencyRetention = conloomapi.Duration{Duration: 60 * cleanupDay}
	}})
	resp := env.submit(t, env.groupEvent("20001", "你好", mentionBot))
	env.waitTask(t, resp.TaskID)
	clock.advance(31 * cleanupDay)
	env.o.expireOutbox()

	env.o.cleanup()
	if env.taskGone(t, resp.TaskID) {
		t.Fatal("task of an event within idempotency_retention deleted")
	}
	clock.advance(30 * cleanupDay)
	env.o.cleanup()
	if !env.taskGone(t, resp.TaskID) {
		t.Fatal("task kept after both retentions passed")
	}
}

func (e *testEnv) insertModelCall(t *testing.T, at time.Time) {
	t.Helper()
	err := e.st.InsertModelCalls(context.Background(), []store.ModelCall{{
		At: at, BotInstanceID: testBot, ConversationKey: testConvKey, Purpose: store.PurposeChat, Role: "short_context",
		Model: "short-model", PromptTokens: 10, CompletionTokens: 5, Outcome: store.OutcomeOK,
	}})
	if err != nil {
		t.Fatalf("InsertModelCalls: %v", err)
	}
}

func (e *testEnv) modelCallTimes(t *testing.T) []time.Time {
	t.Helper()
	calls, err := e.st.ListModelCalls(context.Background(), store.ModelCallFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListModelCalls: %v", err)
	}
	var out []time.Time
	for _, c := range calls {
		out = append(out, c.At)
	}
	return out
}

func (e *testEnv) createPanelSession(t *testing.T, token string, lastSeen, expires time.Time) {
	t.Helper()
	err := e.st.CreatePanelSession(context.Background(), store.PanelSession{
		TokenHash: token, User: "admin", CSRFToken: "csrf-" + token, CredFingerprint: "fp",
		CreatedAt: lastSeen, LastSeenAt: lastSeen, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("CreatePanelSession: %v", err)
	}
}

func (e *testEnv) panelSessionKept(t *testing.T, token string) bool {
	t.Helper()
	_, err := e.st.GetPanelSession(context.Background(), token)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPanelSession %s: %v", token, err)
	}
	return err == nil
}

func TestCleanup_PurgesModelCallsAndPanelSessions(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now, configure: func(c *config.Config) {
		c.Runtime.UsageRetention = conloomapi.Duration{Duration: 30 * cleanupDay}
		c.Panel = &config.PanelConfig{
			SessionTTL:  conloomapi.Duration{Duration: 7 * cleanupDay},
			IdleTimeout: conloomapi.Duration{Duration: time.Hour},
		}
	}})
	now := clock.now().UTC()
	recent := now.Add(-29 * cleanupDay)
	env.insertModelCall(t, now.Add(-31*cleanupDay))
	env.insertModelCall(t, recent)
	env.createPanelSession(t, "expired", now.Add(-time.Minute), now.Add(-time.Second))
	env.createPanelSession(t, "idle", now.Add(-2*time.Hour), now.Add(cleanupDay))
	env.createPanelSession(t, "live", now.Add(-time.Minute), now.Add(cleanupDay))

	env.o.cleanup()

	if got := env.modelCallTimes(t); len(got) != 1 || !got[0].Equal(recent) {
		t.Errorf("model calls left = %v, want only the one at %v", got, recent)
	}
	for token, want := range map[string]bool{"expired": false, "idle": false, "live": true} {
		if got := env.panelSessionKept(t, token); got != want {
			t.Errorf("panel session %s kept = %v, want %v", token, got, want)
		}
	}
	for table, want := range map[string]float64{"model_calls": 1, "panel_sessions": 2} {
		if got := env.counter(t, "conloom_cleanup_deleted_total", "table", table); got != want {
			t.Errorf("cleanup_deleted_total{%s} = %v, want %v", table, got, want)
		}
	}
	logs := env.logs.String()
	for _, want := range []string{"model_calls=1", "panel_sessions=2"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestCleanup_DefaultUsageRetentionAndNoPanel(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now})
	now := clock.now().UTC()
	kept := now.Add(-399 * cleanupDay)
	env.insertModelCall(t, now.Add(-401*cleanupDay))
	env.insertModelCall(t, kept)
	env.createPanelSession(t, "leftover", now.Add(-time.Minute), now.Add(cleanupDay))

	env.o.cleanup()

	if got := env.modelCallTimes(t); len(got) != 1 || !got[0].Equal(kept) {
		t.Errorf("model calls left = %v, want only the one at %v (default usage_retention 400 days)", got, kept)
	}
	if env.panelSessionKept(t, "leftover") {
		t.Error("panel session kept without a panel section, want deleted")
	}
}

func TestCleanup_ZeroRetentionsFallBackToDefaults(t *testing.T) {
	clock := &offsetClock{}
	env := newTestOrchestrator(t, testOpts{now: clock.now, configure: func(c *config.Config) {
		c.Runtime.OpsRetention, c.Runtime.AuditRetention = conloomapi.Duration{}, conloomapi.Duration{}
	}})
	id, _ := env.seedTaskAt(t, "20001", conloomapi.TaskSucceeded, time.Now().UTC())
	env.insertAudit(t, "aud_1", time.Now().UTC())
	clock.advance(29 * cleanupDay)
	env.o.cleanup()
	if env.taskGone(t, id) {
		t.Fatal("task deleted within the default ops_retention")
	}
	clock.advance(2 * cleanupDay)
	env.o.cleanup()
	if !env.taskGone(t, id) {
		t.Fatal("task kept after the default ops_retention")
	}
	if got := env.counter(t, "conloom_cleanup_deleted_total", "table", "audit_events"); got != 0 {
		t.Fatalf("audit event deleted within the default audit_retention")
	}
}
