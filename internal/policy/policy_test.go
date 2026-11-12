package policy

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestPolicy_CurrentFillsMissingProactiveDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conloom.db")
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacy := withConcurrency(conloomapi.DefaultGroupPolicy(), 0)
	legacy.Reply.ContinuationWindow = conloomapi.Duration{Duration: time.Minute}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "proactive")
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "proactive") {
		t.Fatalf("legacy policy still has proactive: %s", raw)
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `INSERT INTO group_policies
		(conversation_key, version, policy, reason, updated_by, created_at) VALUES (?, 5, ?, 'legacy', '29999', ?)`,
		testConvKey, string(raw), time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00")); err != nil {
		t.Fatalf("insert legacy policy: %v", err)
	}

	defaults := conloomapi.DefaultGroupPolicy()
	svc := NewService(db, defaults)
	for i := range 2 {
		pv, err := svc.Current(ctx, testConvKey)
		if err != nil {
			t.Fatalf("Current #%d: %v", i, err)
		}
		if pv.Version != 5 || !pv.Policy.Proactive.Enabled || pv.Policy.Proactive != defaults.Proactive {
			t.Fatalf("Current #%d proactive = %+v (version %d), want defaults", i, pv.Policy.Proactive, pv.Version)
		}
		want := legacy
		want.Proactive = defaults.Proactive
		if !reflect.DeepEqual(pv.Policy, want) {
			t.Fatalf("Current #%d policy = %+v, want %+v", i, pv.Policy, want)
		}
	}

	resumed, err := svc.Resume(ctx, testConvKey, "29999")
	if err != nil || resumed.Version != 6 || resumed.Policy.Proactive != defaults.Proactive {
		t.Fatalf("Resume = %+v, %v; want version 6 with default proactive", resumed, err)
	}
}

func TestPolicy_UpdateFillsMissingProactive(t *testing.T) {
	ctx := context.Background()
	defaults := conloomapi.DefaultGroupPolicy()
	svc := NewService(newTestStore(t), defaults)
	put := conloomapi.DefaultGroupPolicy()
	put.Proactive = conloomapi.ProactivePolicy{}
	pv, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: put}, "29999")
	if err != nil || pv.Version != 1 || pv.Policy.Proactive != defaults.Proactive {
		t.Fatalf("Update = %+v, %v; want default proactive", pv, err)
	}
	cur, err := svc.Current(ctx, testConvKey)
	if err != nil || cur.Policy.Proactive != defaults.Proactive {
		t.Fatalf("Current = %+v, %v", cur, err)
	}
}

func decodePut(t *testing.T, body string) conloomapi.PolicyPut {
	t.Helper()
	var put conloomapi.PolicyPut
	if err := json.Unmarshal([]byte(body), &put); err != nil {
		t.Fatalf("Unmarshal %s: %v", body, err)
	}
	return put
}

// putBody returns a PUT body for p with the proactive section replaced by proactive, or removed when it is empty.
func putBody(t *testing.T, p conloomapi.GroupPolicy, proactive string) string {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "proactive")
	if proactive != "" {
		doc["proactive"] = json.RawMessage(proactive)
	}
	raw, err = json.Marshal(map[string]any{"policy": doc, "reason": "test"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPolicy_UpdateKeepsCurrentProactiveWhenOmitted(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newTestStore(t), conloomapi.DefaultGroupPolicy())
	off := conloomapi.DefaultGroupPolicy()
	off.Proactive.Enabled = false
	if _, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: off}, "29999"); err != nil {
		t.Fatalf("switch off: %v", err)
	}

	goPut := withConcurrency(conloomapi.DefaultGroupPolicy(), 2)
	goPut.Proactive = conloomapi.ProactivePolicy{}
	pv, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: goPut}, "29999")
	if err != nil || pv.Version != 2 || pv.Policy.Proactive != off.Proactive || pv.Policy.Concurrency.MaxInteractiveRequests != 2 {
		t.Fatalf("Go update without proactive = %+v, %v; want proactive kept off", pv, err)
	}

	pv, err = svc.Update(ctx, testConvKey, decodePut(t, putBody(t, conloomapi.DefaultGroupPolicy(), "")), "29999")
	if err != nil || pv.Version != 3 || pv.Policy.Proactive != off.Proactive || pv.Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Fatalf("JSON update without proactive = %+v, %v; want proactive kept off", pv, err)
	}
	cur, err := svc.Current(ctx, testConvKey)
	if err != nil || cur.Version != 3 || cur.Policy.Proactive.Enabled {
		t.Fatalf("Current = %+v, %v; want proactive off", cur, err)
	}
}

func TestPolicy_UpdateRejectsPresentZeroProactive(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st, conloomapi.DefaultGroupPolicy())
	for _, body := range []string{
		putBody(t, conloomapi.DefaultGroupPolicy(), `{"enabled":false}`),
		putBody(t, conloomapi.DefaultGroupPolicy(), `{}`),
	} {
		pv, err := svc.Update(ctx, testConvKey, decodePut(t, body), "29999")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "proactive.default_level") || pv.Version != 0 {
			t.Fatalf("Update %s = %+v, %v; want ErrInvalid", body, pv, err)
		}
	}
	if list, _ := st.ListPolicyVersions(ctx, testConvKey); len(list) != 0 {
		t.Fatalf("invalid update stored %d versions", len(list))
	}

	if _, err := svc.Update(ctx, testConvKey, conloomapi.PolicyPut{Policy: conloomapi.DefaultGroupPolicy()}, "29999"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pv, err := svc.Update(ctx, testConvKey, decodePut(t, putBody(t, conloomapi.DefaultGroupPolicy(), `{"enabled":false}`)), "29999")
	if !errors.Is(err, ErrInvalid) || pv.Version != 0 {
		t.Fatalf("Update over stored version = %+v, %v; want ErrInvalid without a version", pv, err)
	}
}

func TestPolicy_UpdateAppliesPresentProactive(t *testing.T) {
	ctx := context.Background()
	svc := NewService(newTestStore(t), conloomapi.DefaultGroupPolicy())
	want := conloomapi.ProactivePolicy{
		Enabled: false, DefaultLevel: conloomapi.ProactiveMid,
		EligibilityTTL: conloomapi.Duration{Duration: 48 * time.Hour}, UserCooldown: conloomapi.Duration{Duration: 10 * time.Minute},
		GroupHourlyLimit: 3, DailyLimits: conloomapi.ProactiveDailyLimits{Low: 1, Mid: 2, High: 3},
	}
	section, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := svc.Update(ctx, testConvKey, decodePut(t, putBody(t, conloomapi.DefaultGroupPolicy(), string(section))), "29999")
	if err != nil || pv.Version != 1 || pv.Policy.Proactive != want || pv.Reason != "test" {
		t.Fatalf("Update = %+v, %v; want proactive %+v", pv, err, want)
	}
}

func TestPolicy_UpdateOmittedProactiveNeverConfiguredUsesDefaults(t *testing.T) {
	ctx := context.Background()
	defaults := conloomapi.DefaultGroupPolicy()
	defaults.Proactive.DefaultLevel = conloomapi.ProactiveHigh
	svc := NewService(newTestStore(t), defaults)
	pv, err := svc.Update(ctx, testConvKey, decodePut(t, putBody(t, conloomapi.DefaultGroupPolicy(), "")), "29999")
	if err != nil || pv.Version != 1 || pv.Policy.Proactive != defaults.Proactive {
		t.Fatalf("Update = %+v, %v; want default proactive", pv, err)
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
