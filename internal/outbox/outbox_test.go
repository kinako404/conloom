package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	testBot      = "bot_test"
	testPlatform = "qq"
	testGroup    = "10001"
	testConvID   = "conv_10001"
)

var testConvKey = store.ConversationKey(testBot, testPlatform, conloomapi.ConvGroup, testConvID)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fixture struct {
	st      *sqlite.DB
	svc     *Service
	reg     *prometheus.Registry
	metrics *obs.Metrics
	clock   *clock
}

func (f *fixture) counter(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := f.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := prometheus.NewRegistry()
	m := obs.NewMetrics(reg)
	c := &clock{t: time.Now().UTC()}
	svc := New(st, Config{Lease: time.Minute, MaxWait: 5 * time.Second, RecheckEvery: 2 * time.Second}, m, c.now)
	return &fixture{st: st, svc: svc, reg: reg, metrics: m, clock: c}
}

// addActions stores one task with the given message.send actions in the test conversation.
func (f *fixture) addActions(t *testing.T, taskID string, actionIDs ...string) {
	t.Helper()
	if err := f.insertActions(taskID, actionIDs...); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) insertActions(taskID string, actionIDs ...string) error {
	ctx := context.Background()
	now := f.clock.now()
	_, err := f.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{
			BotInstanceID:  testBot,
			IdempotencyKey: "key_" + taskID,
			RequestHash:    "hash_" + taskID,
			EventID:        "evt_" + taskID,
			Response:       []byte(`{}`),
			RawEvent:       []byte(`{}`),
			CreatedAt:      now,
		},
		NewTask: &store.TaskRecord{
			ID:                  taskID,
			BotInstanceID:       testBot,
			ConversationKey:     testConvKey,
			Status:              conloomapi.TaskRunning,
			ActorPlatformUserID: "20001",
			TriggerMessageIDs:   []string{"msg_t1"},
			EventOccurredAt:     now,
		},
	})
	if err != nil {
		return fmt.Errorf("Ingest: %w", err)
	}
	var recs []store.OutboxRecord
	for _, id := range actionIDs {
		recs = append(recs, store.OutboxRecord{
			Action: conloomapi.Action{
				ActionID:               id,
				Type:                   conloomapi.ActionMessageSend,
				ConversationID:         testConvID,
				ConversationType:       conloomapi.ConvGroup,
				PlatformConversationID: testGroup,
				ReplyToMessageID:       "msg_t1",
				Content:                conloomapi.ActionContent{Text: "reply " + id},
				CreatedAt:              now,
			},
			BotInstanceID:   testBot,
			ConversationKey: testConvKey,
			Platform:        testPlatform,
			CreatedAt:       now,
			ExpiresAt:       now.Add(time.Hour),
		})
	}
	if err := f.st.CompleteTask(ctx, store.TaskCompletion{TaskID: taskID, Status: conloomapi.TaskSucceeded, Actions: recs}); err != nil {
		return fmt.Errorf("CompleteTask: %w", err)
	}
	return nil
}

func ids(actions []conloomapi.Action) []string {
	out := []string{}
	for _, a := range actions {
		out = append(out, a.ActionID)
	}
	return out
}

func TestPoll_ReturnsImmediatelyWhenAvailable(t *testing.T) {
	f := newFixture(t)
	f.addActions(t, "task_1", "act_1")

	start := time.Now()
	got, err := f.svc.Poll(context.Background(), testBot, 10, 5*time.Second)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Poll took %v, want immediate", elapsed)
	}
	if len(got) != 1 || got[0].ActionID != "act_1" || got[0].Content.Text != "reply act_1" {
		t.Fatalf("Poll = %+v, want act_1", got)
	}
	if v := f.counter(t, "conloom_outbox_delivered_total"); v != 1 {
		t.Fatalf("delivered = %v, want 1", v)
	}
	if v := f.counter(t, "conloom_outbox_redelivered_total"); v != 0 {
		t.Fatalf("redelivered = %v, want 0", v)
	}
}

func TestPoll_WakesOnNotify(t *testing.T) {
	f := newFixture(t)
	notified := make(chan time.Time, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := f.insertActions("task_1", "act_1"); err != nil {
			t.Error(err)
		}
		notified <- time.Now()
		f.svc.Notify(testBot)
	}()

	got, err := f.svc.Poll(context.Background(), testBot, 10, 5*time.Second)
	returned := time.Now()
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if d := returned.Sub(<-notified); d > 100*time.Millisecond {
		t.Fatalf("Poll returned %v after Notify, want within 100ms", d)
	}
	if want := []string{"act_1"}; len(got) != 1 || ids(got)[0] != want[0] {
		t.Fatalf("Poll = %v, want %v", ids(got), want)
	}
}

func TestPoll_TimesOutEmpty(t *testing.T) {
	f := newFixture(t)
	start := time.Now()
	got, err := f.svc.Poll(context.Background(), testBot, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("Poll took %v, want ~100ms", elapsed)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Poll = %#v, want non-nil empty slice", got)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Fatalf("json = %s, want []", b)
	}
}

func TestPoll_WaitCappedAtMaxWait(t *testing.T) {
	f := newFixture(t)
	f.svc = New(f.st, Config{MaxWait: 100 * time.Millisecond}, f.metrics, f.clock.now)
	start := time.Now()
	got, err := f.svc.Poll(context.Background(), testBot, 10, time.Hour)
	if err != nil || len(got) != 0 {
		t.Fatalf("Poll = %v, %v; want empty, nil", ids(got), err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Poll took %v, want capped at MaxWait", elapsed)
	}
}

func TestPoll_RechecksWithoutNotify(t *testing.T) {
	f := newFixture(t)
	f.svc = New(f.st, Config{Lease: time.Minute, MaxWait: 5 * time.Second, RecheckEvery: 50 * time.Millisecond}, f.metrics, f.clock.now)
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := f.insertActions("task_1", "act_1"); err != nil {
			t.Error(err)
		}
	}()
	start := time.Now()
	got, err := f.svc.Poll(context.Background(), testBot, 10, 5*time.Second)
	if err != nil || len(got) != 1 {
		t.Fatalf("Poll = %v, %v; want [act_1]", ids(got), err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Poll took %v, want recheck to find the action", elapsed)
	}
}

func TestPoll_RedeliveryCountsMetric(t *testing.T) {
	f := newFixture(t)
	f.addActions(t, "task_1", "act_1")
	ctx := context.Background()
	if got, err := f.svc.Poll(ctx, testBot, 10, 0); err != nil || len(got) != 1 {
		t.Fatalf("first Poll = %v, %v", ids(got), err)
	}
	if got, err := f.svc.Poll(ctx, testBot, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("Poll within lease = %v, %v; want none", ids(got), err)
	}
	f.clock.advance(61 * time.Second)
	got, err := f.svc.Poll(ctx, testBot, 10, 0)
	if err != nil || len(got) != 1 || got[0].ActionID != "act_1" {
		t.Fatalf("Poll after lease expiry = %v, %v; want [act_1]", ids(got), err)
	}
	if v := f.counter(t, "conloom_outbox_redelivered_total"); v != 1 {
		t.Fatalf("redelivered = %v, want 1", v)
	}
	if v := f.counter(t, "conloom_outbox_delivered_total"); v != 2 {
		t.Fatalf("delivered = %v, want 2", v)
	}
}

func TestPoll_CancelledContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := f.svc.Poll(ctx, testBot, 10, 5*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Poll err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Poll took %v after cancel", elapsed)
	}
}

func TestAck_InsertsBotMessageAndUnblocksNext(t *testing.T) {
	f := newFixture(t)
	f.addActions(t, "task_1", "act_1", "act_2")
	ctx := context.Background()

	got, err := f.svc.Poll(ctx, testBot, 10, 0)
	if err != nil || len(got) != 1 || got[0].ActionID != "act_1" {
		t.Fatalf("first Poll = %v, %v; want [act_1]", ids(got), err)
	}

	type result struct {
		actions []conloomapi.Action
		err     error
	}
	done := make(chan result, 1)
	go func() {
		a, err := f.svc.Poll(ctx, testBot, 10, 5*time.Second)
		done <- result{a, err}
	}()
	time.Sleep(50 * time.Millisecond)

	ackAt := f.clock.now()
	if err := f.svc.Ack(ctx, testBot, "act_1", conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p1"}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil || len(r.actions) != 1 || r.actions[0].ActionID != "act_2" {
			t.Fatalf("Poll after ack = %v, %v; want [act_2]", ids(r.actions), r.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Poll not woken by Ack")
	}

	msg, err := f.st.GetMessage(ctx, testBot, "msg_p1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if !msg.FromBot || msg.Text != "reply act_1" || msg.ConversationKey != testConvKey || msg.Platform != testPlatform ||
		msg.PlatformMessageID != "p1" || msg.BotInstanceID != testBot || msg.ReplyTo != "msg_t1" ||
		!msg.OccurredAt.Equal(ackAt) {
		t.Fatalf("bot message = %+v", msg)
	}
	o, err := f.st.GetOutbox(ctx, testBot, "act_1")
	if err != nil || o.Status != store.OutboxAcked || o.PlatformMessageID != "p1" {
		t.Fatalf("outbox = %+v, %v; want acked p1", o, err)
	}

	if err := f.svc.Ack(ctx, testBot, "act_1", conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p1"}); err != nil {
		t.Fatalf("repeated Ack: %v", err)
	}
}

func TestAck_SentWithoutPlatformID(t *testing.T) {
	f := newFixture(t)
	f.addActions(t, "task_1", "act_1")
	ctx := context.Background()
	if err := f.svc.Ack(ctx, testBot, "act_1", conloomapi.AckRequest{Status: conloomapi.AckSent}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	msg, err := f.st.GetMessage(ctx, testBot, "msg_bot_act_1")
	if err != nil || !msg.FromBot || msg.Text != "reply act_1" {
		t.Fatalf("bot message = %+v, %v", msg, err)
	}
}

func TestAck_UnknownIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.addActions(t, "task_1", "act_1")
	ctx := context.Background()
	req := conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: "p1"}
	if err := f.svc.Ack(ctx, testBot, "missing", req); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Ack missing err = %v, want ErrNotFound", err)
	}
	if err := f.svc.Ack(ctx, "bot_other", "act_1", req); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Ack other bot err = %v, want ErrNotFound", err)
	}
}

func TestAck_Unsupported_NoMessage(t *testing.T) {
	for _, status := range []string{conloomapi.AckUnsupported, conloomapi.AckFailed} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			f.addActions(t, "task_1", "act_1", "act_2")
			ctx := context.Background()
			if got, err := f.svc.Poll(ctx, testBot, 10, 0); err != nil || len(got) != 1 {
				t.Fatalf("Poll = %v, %v", ids(got), err)
			}
			if err := f.svc.Ack(ctx, testBot, "act_1", conloomapi.AckRequest{Status: status, PlatformMessageID: "p1"}); err != nil {
				t.Fatalf("Ack: %v", err)
			}
			for _, id := range []string{"msg_p1", "msg_bot_act_1"} {
				if _, err := f.st.GetMessage(ctx, testBot, id); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("GetMessage(%s) err = %v, want ErrNotFound", id, err)
				}
			}
			o, err := f.st.GetOutbox(ctx, testBot, "act_1")
			if err != nil || o.Status != store.OutboxAcked {
				t.Fatalf("outbox = %+v, %v; want acked", o, err)
			}
			got, err := f.svc.Poll(ctx, testBot, 10, 0)
			if err != nil || len(got) != 1 || got[0].ActionID != "act_2" {
				t.Fatalf("Poll after ack = %v, %v; want [act_2]", ids(got), err)
			}
		})
	}
}

func TestNilMetricsTolerated(t *testing.T) {
	f := newFixture(t)
	f.svc = New(f.st, Config{}, nil, f.clock.now)
	f.addActions(t, "task_1", "act_1")
	ctx := context.Background()
	if got, err := f.svc.Poll(ctx, testBot, 10, 0); err != nil || len(got) != 1 {
		t.Fatalf("Poll = %v, %v", ids(got), err)
	}
	f.clock.advance(2 * time.Minute)
	if got, err := f.svc.Poll(ctx, testBot, 10, 0); err != nil || len(got) != 1 {
		t.Fatalf("redelivery Poll = %v, %v", ids(got), err)
	}
}
