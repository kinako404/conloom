package policy

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testBot = "bot_test"

var testConvKey = store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10001")

func newTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "conloom.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type auditRecorder struct {
	store.Store
	mu     sync.Mutex
	events []store.AuditEvent
}

func (r *auditRecorder) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, a)
	r.mu.Unlock()
	return r.Store.InsertAudit(ctx, a)
}

func (r *auditRecorder) all() []store.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func withConcurrency(p conloomapi.GroupPolicy, n int) conloomapi.GroupPolicy {
	p.Concurrency.MaxInteractiveRequests = n
	return p
}

func TestPolicy_CurrentDefaultsWithoutWrite(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, conloomapi.DefaultGroupPolicy())
	pv, err := svc.Current(ctx, testConvKey)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if pv.Version != 0 || pv.ConversationKey != testConvKey || !reflect.DeepEqual(pv.Policy, conloomapi.DefaultGroupPolicy()) {
		t.Fatalf("Current = %+v", pv)
	}
	list, err := st.ListPolicyVersions(ctx, testConvKey)
	if err != nil || len(list) != 0 {
		t.Fatalf("ListPolicyVersions = %v, %v; want empty", list, err)
	}
}

func TestPolicy_UpdateStoresVersionAndRefreshesCache(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	rec := &auditRecorder{Store: st}
	svc := NewService(rec, conloomapi.DefaultGroupPolicy())
	if _, err := svc.Current(ctx, testConvKey); err != nil {
		t.Fatalf("Current: %v", err)
	}
	want := conloomapi.DefaultGroupPolicy()
	want.Reply.Debounce = conloomapi.Duration{Duration: 3 * time.Second}
	pv, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: want, Reason: "slow down"}, "29999")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if pv.Version != 1 || pv.Reason != "slow down" || pv.UpdatedBy != "29999" || !reflect.DeepEqual(pv.Policy, want) {
		t.Fatalf("Update = %+v", pv)
	}
	cur, err := svc.Current(ctx, testConvKey)
	if err != nil || cur.Version != 1 || !reflect.DeepEqual(cur.Policy, want) {
		t.Fatalf("Current after update = %+v, %v", cur, err)
	}
	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("audit events = %+v, want 1", events)
	}
	if ev := events[0]; ev.ID == "" || ev.BotInstanceID != testBot || ev.Kind != "policy.update" || ev.Actor != "29999" ||
		ev.Target != testConvKey || ev.Detail == "" || strings.Contains(ev.Detail, "slow down") {
		t.Errorf("audit event = %+v", ev)
	}
}

func TestPolicy_PauseResumeRestoresPrevious(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, conloomapi.DefaultGroupPolicy())
	if _, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: withConcurrency(conloomapi.DefaultGroupPolicy(), 2)}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	paused, err := svc.Pause(ctx, testConvKey, "29999")
	if err != nil || paused.Version != 2 || paused.Policy.Concurrency.MaxInteractiveRequests != 0 {
		t.Fatalf("Pause = %+v, %v", paused, err)
	}
	again, err := svc.Pause(ctx, testConvKey, "29999")
	if err != nil || again.Version != 2 {
		t.Fatalf("second Pause = %+v, %v; want unchanged version 2", again, err)
	}
	resumed, err := svc.Resume(ctx, testConvKey, "29999")
	if err != nil || resumed.Version != 3 || resumed.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("Resume = %+v, %v", resumed, err)
	}
	cur, err := svc.Current(ctx, testConvKey)
	if err != nil || cur.Version != 3 || cur.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("Current = %+v, %v", cur, err)
	}
	if again, err := svc.Resume(ctx, testConvKey, "29999"); err != nil || again.Version != 3 {
		t.Fatalf("Resume when running = %+v, %v; want unchanged version 3", again, err)
	}
}

func TestPolicy_ResumeWithoutHistoryUsesDefaults(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		defaults    int
		wantResumed int
	}{
		{"default concurrency", 2, 2},
		{"default paused", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			svc := NewService(st, withConcurrency(conloomapi.DefaultGroupPolicy(), tc.defaults))
			if tc.defaults != 0 {
				if _, err := svc.Pause(ctx, testConvKey, "29999"); err != nil {
					t.Fatalf("Pause: %v", err)
				}
			}
			pv, err := svc.Resume(ctx, testConvKey, "29999")
			if err != nil || pv.Policy.Concurrency.MaxInteractiveRequests != tc.wantResumed {
				t.Fatalf("Resume = %+v, %v; want concurrency %d", pv, err, tc.wantResumed)
			}
		})
	}
}

func TestPolicy_ValidateRejectsConcurrency3(t *testing.T) {
	if err := Validate(conloomapi.DefaultGroupPolicy()); err != nil {
		t.Fatalf("default policy: %v", err)
	}
	err := Validate(withConcurrency(conloomapi.DefaultGroupPolicy(), 3))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "max_interactive_requests") {
		t.Fatalf("Validate concurrency 3 = %v", err)
	}

	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, conloomapi.DefaultGroupPolicy())
	_, err = svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: withConcurrency(conloomapi.DefaultGroupPolicy(), 3)}, "29999")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Update concurrency 3: err = %v, want ErrInvalid", err)
	}
	if list, _ := st.ListPolicyVersions(ctx, testConvKey); len(list) != 0 {
		t.Fatalf("invalid update stored %d versions", len(list))
	}
}

func TestPolicy_OnChangeCalled(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, conloomapi.DefaultGroupPolicy())
	var mu sync.Mutex
	var got []string
	svc.OnChange(func(convKey string) {
		mu.Lock()
		got = append(got, convKey)
		mu.Unlock()
	})
	if _, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: conloomapi.DefaultGroupPolicy()}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := svc.Pause(ctx, testConvKey, "29999"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := svc.Pause(ctx, testConvKey, "29999"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := svc.Resume(ctx, testConvKey, "29999"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{testConvKey, testConvKey, testConvKey}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OnChange calls = %v, want %v", got, want)
	}
}
