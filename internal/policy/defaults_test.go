package policy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// seedConversation stores a conversation of convType so PolicyFollowers can list it, and returns its key.
func seedConversation(t *testing.T, st *sqlite.DB, convType, id string) string {
	t.Helper()
	key := store.ConversationKey(testBot, "qq", convType, id)
	if _, err := st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: testBot, IdempotencyKey: "seed:" + id, EventID: "evt_seed_" + id,
			Response: []byte("{}"), RawEvent: []byte("{}")},
		Conversation: &store.ConversationRecord{BotInstanceID: testBot, ID: id, Key: key, Type: convType,
			Platform: "qq", PlatformConversationID: id},
	}); err != nil {
		t.Fatalf("seed conversation %s: %v", key, err)
	}
	return key
}

func appendVersion(t *testing.T, st store.Store, key string, p conloomapi.GroupPolicy, follows bool) {
	t.Helper()
	if _, err := st.AppendPolicyVersion(context.Background(), store.PolicyVersion{
		ConversationKey: key, Policy: p, Reason: "seed", UpdatedBy: "29999", FollowsDefaults: follows,
	}); err != nil {
		t.Fatalf("AppendPolicyVersion %s: %v", key, err)
	}
}

func withRecent(p conloomapi.GroupPolicy, n int) conloomapi.GroupPolicy {
	p.Context.MaxRecentMessages = n
	return p
}

func withMemory(p conloomapi.GroupPolicy, on bool) conloomapi.GroupPolicy {
	p.Memory.Enabled = on
	return p
}

type transition struct {
	key           string
	before, after conloomapi.GroupPolicy
}

// callRecorder records OnChange and OnTransition calls.
type callRecorder struct {
	mu          sync.Mutex
	changes     []string
	transitions []transition
}

func recordCalls(s *Service) *callRecorder {
	r := &callRecorder{}
	s.OnChange(func(key string) {
		r.mu.Lock()
		r.changes = append(r.changes, key)
		r.mu.Unlock()
	})
	s.OnTransition(func(key string, before, after conloomapi.GroupPolicy) {
		r.mu.Lock()
		r.transitions = append(r.transitions, transition{key, before, after})
		r.mu.Unlock()
	})
	return r
}

func (r *callRecorder) changed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(slices.Values(r.changes))
}

func (r *callRecorder) transitioned() []transition {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.transitions)
}

func newDefaultsService(t *testing.T, st store.Store) *Service {
	t.Helper()
	s := NewService(st, conloomapi.DefaultGroupPolicy())
	t.Cleanup(s.Close)
	return s
}

func TestPolicy_LoadDefaultsFromStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, n := range []int{22, 33} {
		if _, err := st.AppendPolicyDefaults(ctx, store.PolicyDefaults{
			Kind: KindGroup, Policy: withRecent(conloomapi.DefaultGroupPolicy(), n), Reason: "tune", UpdatedBy: "panel:admin",
		}); err != nil {
			t.Fatalf("AppendPolicyDefaults: %v", err)
		}
	}
	s := newDefaultsService(t, st).WithPrivateDefaults(privateDefaults())
	if err := s.LoadDefaults(ctx); err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if d := s.Defaults(KindGroup); d.Version != 2 || d.Kind != KindGroup || d.Policy.Context.MaxRecentMessages != 33 {
		t.Fatalf("Defaults(group) = %+v, want version 2 with 33 recent messages", d)
	}
	pv, err := s.Current(ctx, testConvKey)
	if err != nil || pv.Version != 0 || !pv.FollowsDefaults || pv.Policy.Context.MaxRecentMessages != 33 {
		t.Fatalf("group Current = %+v, %v; want the stored defaults, following", pv, err)
	}
	if d := s.Defaults(KindPrivate); d.Version != 0 || !reflect.DeepEqual(d.Policy, privateDefaults()) {
		t.Fatalf("Defaults(private) = %+v, want the in-memory private defaults", d)
	}
	pv, err = s.Current(ctx, testPrivateKey)
	if err != nil || !pv.FollowsDefaults || !reflect.DeepEqual(pv.Policy, privateDefaults()) {
		t.Fatalf("private Current = %+v, %v; want the in-memory private defaults", pv, err)
	}
	if d := s.Defaults("channel"); d.Kind != KindGroup || d.Version != 2 {
		t.Fatalf("Defaults(channel) = %+v, want the group defaults", d)
	}
}

func TestPolicy_KindOfAndBuiltin(t *testing.T) {
	if k := KindOf(testPrivateKey); k != KindPrivate {
		t.Errorf("KindOf(%s) = %q, want private", testPrivateKey, k)
	}
	for _, key := range []string{testConvKey, "bot_test:qq:private", "garbage"} {
		if k := KindOf(key); k != KindGroup {
			t.Errorf("KindOf(%s) = %q, want group", key, k)
		}
	}
	if !reflect.DeepEqual(Builtin(KindGroup), conloomapi.DefaultGroupPolicy()) ||
		!reflect.DeepEqual(Builtin(KindPrivate), conloomapi.DefaultPrivatePolicy()) {
		t.Error("Builtin does not return the built-in defaults of each kind")
	}
}

func TestPolicy_SetDefaultsClearsCache(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	never := testConvKey
	follows := store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10002")
	own := store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "10003")
	appendVersion(t, st, follows, withRecent(conloomapi.DefaultGroupPolicy(), 70), true)
	appendVersion(t, st, own, withRecent(conloomapi.DefaultGroupPolicy(), 40), false)
	s := newDefaultsService(t, st)
	for _, key := range []string{never, follows, own} {
		if _, err := s.Current(ctx, key); err != nil {
			t.Fatalf("Current(%s): %v", key, err)
		}
	}
	d, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "tune", "panel:admin", "update")
	if err != nil || d.Version != 1 || d.Kind != KindGroup || d.UpdatedBy != "panel:admin" || d.Reason != "tune" {
		t.Fatalf("SetDefaults = %+v, %v", d, err)
	}
	for _, key := range []string{never, follows} {
		pv, err := s.Current(ctx, key)
		if err != nil || !pv.FollowsDefaults || pv.Policy.Context.MaxRecentMessages != 33 {
			t.Errorf("Current(%s) = %+v, %v; want the new defaults", key, pv, err)
		}
	}
	if pv, err := s.Current(ctx, follows); err != nil || pv.Version != 1 || pv.Reason != "seed" {
		t.Errorf("following Current = %+v, %v; want its stored version 1", pv, err)
	}
	if pv, err := s.Current(ctx, own); err != nil || pv.FollowsDefaults || pv.Policy.Context.MaxRecentMessages != 40 {
		t.Errorf("individual Current = %+v, %v; want its own 40", pv, err)
	}
	s.Wait()
}

func TestPolicy_SetDefaultsNotifiesFollowersOnly(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := seedConversation(t, st, conloomapi.ConvGroup, "10001")
	b := seedConversation(t, st, conloomapi.ConvGroup, "10002")
	c := seedConversation(t, st, conloomapi.ConvGroup, "10003")
	p := seedConversation(t, st, conloomapi.ConvPrivate, "20001")
	appendVersion(t, st, b, conloomapi.DefaultGroupPolicy(), false)
	appendVersion(t, st, c, conloomapi.DefaultGroupPolicy(), false)
	appendVersion(t, st, c, conloomapi.DefaultGroupPolicy(), true)
	s := newDefaultsService(t, st)
	rec := recordCalls(s)

	if _, err := s.SetDefaults(ctx, KindGroup, withMemory(conloomapi.DefaultGroupPolicy(), true), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	s.Wait()
	if got, want := rec.changed(), []string{a, c}; !slices.Equal(got, want) {
		t.Fatalf("OnChange calls = %v, want %v", got, want)
	}
	var keys []string
	for _, tr := range rec.transitioned() {
		keys = append(keys, tr.key)
		if tr.before.Memory.Enabled || !tr.after.Memory.Enabled {
			t.Errorf("transition of %s: memory %v -> %v, want off -> on", tr.key, tr.before.Memory.Enabled, tr.after.Memory.Enabled)
		}
	}
	if slices.Sort(keys); !slices.Equal(keys, []string{a, c}) {
		t.Fatalf("OnTransition keys = %v, want [%s %s]", keys, a, c)
	}
	for _, key := range []string{b, p} {
		if slices.Contains(rec.changed(), key) {
			t.Errorf("%s was notified", key)
		}
	}
}

func TestPolicy_SetDefaultsRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seedConversation(t, st, conloomapi.ConvGroup, "10001")
	s := newDefaultsService(t, st).WithReservedContextTokens(4096)
	rec := recordCalls(s)
	before := s.Defaults(KindGroup)
	capped := conloomapi.DefaultGroupPolicy()
	capped.Context.MaxContextTokens = 2000
	for _, c := range []struct {
		name, kind string
		p          conloomapi.GroupPolicy
	}{
		{"concurrency 3", KindGroup, withConcurrency(conloomapi.DefaultGroupPolicy(), 3)},
		{"context cap within reservation", KindGroup, capped},
		{"unknown kind", "channel", conloomapi.DefaultGroupPolicy()},
	} {
		if d, err := s.SetDefaults(ctx, c.kind, c.p, "bad", "panel:admin", "update"); !errors.Is(err, ErrInvalid) || d.Version != 0 {
			t.Errorf("%s: SetDefaults = %+v, %v; want ErrInvalid", c.name, d, err)
		}
	}
	s.Wait()
	for _, kind := range []string{KindGroup, KindPrivate, "channel"} {
		if list, err := st.ListPolicyDefaults(ctx, kind, 0); err != nil || len(list) != 0 {
			t.Errorf("ListPolicyDefaults(%s) = %v, %v; want empty", kind, list, err)
		}
	}
	if after := s.Defaults(KindGroup); !reflect.DeepEqual(after, before) {
		t.Errorf("Defaults(group) = %+v, want unchanged %+v", after, before)
	}
	if len(rec.changed()) != 0 || len(rec.transitioned()) != 0 {
		t.Errorf("callbacks called: %v, %v", rec.changed(), rec.transitioned())
	}
}

func TestPolicy_SetDefaultsAudits(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if _, err := st.AppendPolicyDefaults(ctx, store.PolicyDefaults{
		Kind: KindGroup, Policy: conloomapi.DefaultGroupPolicy(), Reason: "内置默认值", UpdatedBy: "system",
	}); err != nil {
		t.Fatal(err)
	}
	rec := &auditRecorder{Store: st}
	s := newDefaultsService(t, rec)
	if err := s.LoadDefaults(ctx); err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if _, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "tune", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults update: %v", err)
	}
	if _, err := s.SetDefaults(ctx, KindPrivate, conloomapi.DefaultPrivatePolicy(), "back", "panel:admin", "restore"); err != nil {
		t.Fatalf("SetDefaults restore: %v", err)
	}
	s.Wait()
	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("audit events = %+v, want 2", events)
	}
	type detail struct {
		Kind    string `json:"kind"`
		Version int    `json:"version"`
		Action  string `json:"action"`
	}
	for i, want := range []detail{{KindGroup, 2, "update"}, {KindPrivate, 1, "restore"}} {
		ev := events[i]
		var got detail
		if err := json.Unmarshal([]byte(ev.Detail), &got); err != nil {
			t.Fatalf("detail %s: %v", ev.Detail, err)
		}
		if got != want || ev.Kind != "settings.policy.update" || ev.Actor != "panel:admin" || ev.Target != want.Kind ||
			ev.BotInstanceID != "" || ev.ID == "" || ev.CreatedAt.IsZero() {
			t.Errorf("audit event %d = %+v (detail %+v), want %+v", i, ev, got, want)
		}
	}
}

func TestPolicy_FollowDefaultsUnpauses(t *testing.T) {
	ctx := context.Background()
	rec := &auditRecorder{Store: newTestStore(t)}
	s := newDefaultsService(t, rec)
	if _, err := s.Pause(ctx, testConvKey, "29999"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	calls := recordCalls(s)
	pv, err := s.FollowDefaults(ctx, testConvKey, "", "panel:admin")
	if err != nil || pv.Version != 2 || !pv.FollowsDefaults || pv.Reason != "改回跟随默认" || pv.UpdatedBy != "panel:admin" {
		t.Fatalf("FollowDefaults = %+v, %v; want version 2 following the defaults", pv, err)
	}
	cur, err := s.Current(ctx, testConvKey)
	if err != nil || cur.Version != 2 || !cur.FollowsDefaults || cur.Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Fatalf("Current = %+v, %v; want the defaults' concurrency 1", cur, err)
	}
	if got := calls.changed(); !slices.Equal(got, []string{testConvKey}) {
		t.Fatalf("OnChange calls = %v, want one", got)
	}
	trs := calls.transitioned()
	if len(trs) != 1 || trs[0].key != testConvKey || trs[0].before.Concurrency.MaxInteractiveRequests != 0 ||
		trs[0].after.Concurrency.MaxInteractiveRequests != 1 {
		t.Fatalf("OnTransition calls = %+v, want concurrency 0 -> 1", trs)
	}
	events := rec.all()
	last := events[len(events)-1]
	if last.Kind != "policy.update" || last.Actor != "panel:admin" || last.Detail != `{"action":"follow_defaults","version":2}` {
		t.Fatalf("audit event = %+v, want policy.update follow_defaults", last)
	}

	again, err := s.FollowDefaults(ctx, testConvKey, "again", "panel:admin")
	if err != nil || again.Version != 2 || !again.FollowsDefaults {
		t.Fatalf("second FollowDefaults = %+v, %v; want unchanged version 2", again, err)
	}
	if list, _ := rec.ListPolicyVersions(ctx, testConvKey); len(list) != 2 {
		t.Fatalf("stored %d versions, want 2", len(list))
	}
	if n := len(calls.changed()); n != 1 {
		t.Fatalf("OnChange calls after a second FollowDefaults = %d, want 1", n)
	}
}

func TestPolicy_FollowerUpdateBecomesIndividual(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	key := seedConversation(t, st, conloomapi.ConvGroup, "10001")
	s := newDefaultsService(t, st)
	pv, err := s.Update(ctx, key, conloomapi.PolicyPut{Policy: withRecent(conloomapi.DefaultGroupPolicy(), 40)}, "29999")
	if err != nil || pv.FollowsDefaults || pv.Version != 1 {
		t.Fatalf("Update = %+v, %v; want an individual version 1", pv, err)
	}
	if followers, err := st.PolicyFollowers(ctx, KindGroup); err != nil || len(followers) != 0 {
		t.Fatalf("PolicyFollowers = %v, %v; want none", followers, err)
	}
	calls := recordCalls(s)
	if _, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	cur, err := s.Current(ctx, key)
	if err != nil || cur.FollowsDefaults || cur.Policy.Context.MaxRecentMessages != 40 {
		t.Fatalf("Current = %+v, %v; want its own 40", cur, err)
	}
	s.Wait()
	if len(calls.changed()) != 0 || len(calls.transitioned()) != 0 {
		t.Fatalf("individual conversation notified: %v, %v", calls.changed(), calls.transitioned())
	}
}

func TestPolicy_CloseStopsFanOut(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	seedConversation(t, st, conloomapi.ConvGroup, "10001")
	s := NewService(st, conloomapi.DefaultGroupPolicy())
	calls := recordCalls(s)
	s.Close()
	done := make(chan error, 1)
	go func() {
		_, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "", "panel:admin", "update")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetDefaults after Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetDefaults after Close blocked")
	}
	s.Wait()
	s.Close()
	if len(calls.changed()) != 0 || len(calls.transitioned()) != 0 {
		t.Fatalf("notified after Close: %v, %v", calls.changed(), calls.transitioned())
	}
	if d := s.Defaults(KindGroup); d.Version != 1 || d.Policy.Context.MaxRecentMessages != 33 {
		t.Fatalf("Defaults(group) = %+v, want version 1 stored", d)
	}
}

func TestPolicy_SetDefaultsFanOutInBatches(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	var want []string
	for i := range defaultsBatch + 5 {
		want = append(want, seedConversation(t, st, conloomapi.ConvGroup, strconv.Itoa(10001+i)))
	}
	slices.Sort(want)
	s := newDefaultsService(t, st)
	calls := recordCalls(s)
	if _, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 33), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	if _, err := s.SetDefaults(ctx, KindGroup, withRecent(conloomapi.DefaultGroupPolicy(), 44), "", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	s.Wait()
	trs := calls.transitioned()
	if len(trs) != 2*len(want) {
		t.Fatalf("transitions = %d, want %d", len(trs), 2*len(want))
	}
	last := map[string]int{}
	for _, tr := range trs {
		if prev, ok := last[tr.key]; ok && tr.before.Context.MaxRecentMessages != prev {
			t.Fatalf("transition of %s out of order: before %d, previous after %d", tr.key, tr.before.Context.MaxRecentMessages, prev)
		}
		last[tr.key] = tr.after.Context.MaxRecentMessages
	}
	for _, key := range want {
		if last[key] != 44 {
			t.Fatalf("last transition of %s ends at %d, want 44", key, last[key])
		}
	}
}
