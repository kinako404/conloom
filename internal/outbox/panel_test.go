package outbox

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func (f *fixture) status(t *testing.T, actionID string) string {
	t.Helper()
	rec, err := f.st.GetOutbox(context.Background(), testBot, actionID)
	if err != nil {
		t.Fatalf("GetOutbox(%s): %v", actionID, err)
	}
	return rec.Status
}

// addPanelParts stores one task with a panel action per delay, each not
// before the fixture's clock plus its delay.
func (f *fixture) addPanelParts(t *testing.T, taskID string, delays ...time.Duration) {
	t.Helper()
	ctx := context.Background()
	now := f.clock.now()
	if _, err := f.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "key_" + taskID, EventID: "evt_" + taskID,
			Response: []byte(`{}`), RawEvent: []byte(`{}`), CreatedAt: now},
		NewTask: &store.TaskRecord{ID: taskID, BotInstanceID: testBot, ConversationKey: testConvKey,
			Status: conloomapi.TaskRunning, ActorPlatformUserID: "20001", TriggerMessageIDs: []string{"msg_t1"}, EventOccurredAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	var recs []store.OutboxRecord
	for i, d := range delays {
		id := fmt.Sprintf("act_p%d", i+1)
		recs = append(recs, store.OutboxRecord{
			Action: conloomapi.Action{ActionID: id, Type: conloomapi.ActionMessageSend, ConversationID: testConvID,
				ConversationType: conloomapi.ConvGroup, PlatformConversationID: testGroup,
				Content: conloomapi.ActionContent{Text: "part " + id}, CreatedAt: now, PartIndex: i + 1, PartCount: len(delays)},
			BotInstanceID: testBot, ConversationKey: testConvKey, Platform: testPlatform, Channel: store.ChannelPanel,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), NotBefore: now.Add(d),
		})
	}
	if err := f.st.CompleteTask(ctx, store.TaskCompletion{TaskID: taskID, Status: conloomapi.TaskSucceeded, Actions: recs}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPanelDeliverer_AcksPanelActionsOnly(t *testing.T) {
	f := newFixture(t)
	if err := f.insertChannelActions("task_p", store.ChannelPanel, "act_p1", "act_p2"); err != nil {
		t.Fatal(err)
	}
	f.addActions(t, "task_1", "act_1")

	d := NewPanelDeliverer(f.svc, f.st, time.Hour, f.clock.now, nil)
	d.Start()
	defer d.Close()
	waitFor(t, "both panel actions acked", func() bool {
		return f.status(t, "act_p1") == store.OutboxAcked && f.status(t, "act_p2") == store.OutboxAcked
	})
	for _, id := range []string{"act_p1", "act_p2"} {
		msg, err := f.st.GetMessage(context.Background(), testBot, "msg_panel_"+id)
		if err != nil {
			t.Fatalf("bot message of %s: %v", id, err)
		}
		if !msg.FromBot || msg.Channel != store.ChannelPanel || msg.PlatformMessageID != "panel_"+id {
			t.Errorf("bot message of %s = %+v", id, msg)
		}
	}
	if s := f.status(t, "act_1"); s != store.OutboxPending {
		t.Errorf("platform action status = %q, want pending for the adapter", s)
	}
}

func TestPanelDeliverer_WaitsForNotBeforeAndWakes(t *testing.T) {
	f := newFixture(t)
	f.addPanelParts(t, "task_p", 0, 10*time.Second)

	d := NewPanelDeliverer(f.svc, f.st, time.Hour, f.clock.now, nil)
	d.Start()
	defer d.Close()
	waitFor(t, "first part acked", func() bool { return f.status(t, "act_p1") == store.OutboxAcked })
	time.Sleep(50 * time.Millisecond)
	d.Wake()
	time.Sleep(50 * time.Millisecond)
	if s := f.status(t, "act_p2"); s != store.OutboxPending {
		t.Fatalf("second part status = %q before its not_before, want pending", s)
	}

	f.clock.advance(10 * time.Second)
	woken := time.Now()
	d.Wake()
	waitFor(t, "second part acked after Wake", func() bool { return f.status(t, "act_p2") == store.OutboxAcked })
	if took := time.Since(woken); took > time.Second {
		t.Errorf("second part acked %v after Wake, want at once", took)
	}
}

func TestPanelDeliverer_PollsEveryInterval(t *testing.T) {
	f := newFixture(t)
	d := NewPanelDeliverer(f.svc, f.st, 20*time.Millisecond, f.clock.now, nil)
	d.Start()
	defer d.Close()
	time.Sleep(50 * time.Millisecond)
	if err := f.insertChannelActions("task_p", store.ChannelPanel, "act_p1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "panel action acked without Wake", func() bool { return f.status(t, "act_p1") == store.OutboxAcked })
}

func TestPanelDeliverer_CloseStopsLoop(t *testing.T) {
	f := newFixture(t)
	NewPanelDeliverer(f.svc, f.st, time.Millisecond, nil, nil).Close()

	before := runtime.NumGoroutine()
	d := NewPanelDeliverer(f.svc, f.st, time.Millisecond, nil, nil)
	d.Start()
	d.Start()
	time.Sleep(20 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		d.Close()
		d.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return")
	}
	d.Wake()
	waitFor(t, "the delivery goroutine to exit", func() bool { return runtime.NumGoroutine() <= before })
	if err := f.insertChannelActions("task_p", store.ChannelPanel, "act_p1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if s := f.status(t, "act_p1"); s != store.OutboxPending {
		t.Errorf("action status after Close = %q, want pending", s)
	}
}
