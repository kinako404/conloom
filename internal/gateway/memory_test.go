package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/gateway"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	memoryToken       = "test-memory-token"
	memoryReaderToken = "test-memory-reader-token"
	memoryPath        = "/api/v1/conversations/group_10001/memory"
)

var memGroupKey = store.ConversationKey("bot_main", "qq", conloomapi.ConvGroup, "group_10001")

type memTrigger struct {
	mu   sync.Mutex
	keys []string
}

func (f *memTrigger) Trigger(convKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, convKey)
}

func (f *memTrigger) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.keys)
}

type memAuditStore struct {
	store.Store
	mu     sync.Mutex
	events []store.AuditEvent
}

func (r *memAuditStore) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	r.mu.Lock()
	r.events = append(r.events, a)
	r.mu.Unlock()
	return r.Store.InsertAudit(ctx, a)
}

func (r *memAuditStore) all() []store.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

type memServer struct {
	*testServer
	trigger *memTrigger
	audits  *memAuditStore
	reg     *prometheus.Registry
}

// newMemServer starts a server with tokens holding the memory scopes and a
// fake consolidation trigger; enabled turns memory on in the default policy.
func newMemServer(t *testing.T, enabled bool) *memServer {
	t.Helper()
	ms := &memServer{trigger: &memTrigger{}, reg: prometheus.NewRegistry()}
	ms.testServer = newTestServerWith(t, serverOpts{
		configure: func(c *config.Config) {
			c.APITokens = append(c.APITokens,
				config.APIToken{Name: "memory-main", Token: memoryToken, BotInstanceID: "bot_main",
					Scopes: []string{conloomapi.ScopeMemoryRead, conloomapi.ScopeMemoryWrite}},
				config.APIToken{Name: "memory-reader", Token: memoryReaderToken, BotInstanceID: "bot_main",
					Scopes: []string{conloomapi.ScopeMemoryRead}},
				config.APIToken{Name: "memory-other", Token: "test-memory-other-token", BotInstanceID: "bot_other",
					Scopes: []string{conloomapi.ScopeMemoryRead, conloomapi.ScopeMemoryWrite}},
			)
			c.DefaultGroupPolicy.Memory.Enabled = enabled
		},
		deps: func(d *gateway.Deps) {
			ms.audits = &memAuditStore{Store: d.Store}
			d.Store = ms.audits
			d.Memory = ms.trigger
			d.Counters = obs.NewMetrics(ms.reg)
		},
	})
	return ms
}

func (s *memServer) seed(t *testing.T, userID, name, note string, affinity *int) {
	t.Helper()
	conflicts, err := s.st.ApplyMemoryWrite(context.Background(), store.MemoryWrite{
		ConversationKey: memGroupKey,
		Members: []store.MemberMemory{{ConversationKey: memGroupKey, PlatformUserID: userID, DisplayName: name,
			Note: note, Affinity: affinity}},
		Revisions: []store.MemoryRevision{{ConversationKey: memGroupKey, PlatformUserID: userID,
			Field: store.FieldNote, NewValue: note, Source: store.RevisionConsolidate}},
	})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("seed %s: conflicts %v, err %v", userID, conflicts, err)
	}
}

func (s *memServer) member(t *testing.T, userID string) store.MemberMemory {
	t.Helper()
	got, err := s.st.GetMemberMemories(context.Background(), memGroupKey, []string{userID})
	if err != nil {
		t.Fatal(err)
	}
	return got[userID]
}

func (s *memServer) affinityChanges(t *testing.T, source string) float64 {
	t.Helper()
	mfs, err := s.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "conloom_memory_affinity_changes_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "source" && l.GetValue() == source {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func intPtr(v int) *int { return &v }

func strPtr(s string) *string { return &s }

func TestMemoryAPI_ScopesRequired(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	member := memoryPath + "/members/20001"
	expectError(t, s.do(t, http.MethodGet, memoryPath, adminToken, nil, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, member, conloomapi.MemberMemoryPut{Note: strPtr("喜欢咖啡")}},
		{http.MethodDelete, member, nil},
		{http.MethodPost, memoryPath + "/consolidate", nil},
	} {
		expectError(t, s.do(t, c.method, c.path, memoryReaderToken, c.body, nil), http.StatusForbidden, conloomapi.CodeForbidden)
	}
	if r := s.do(t, http.MethodGet, memoryPath, memoryReaderToken, nil, nil); r.status != http.StatusOK {
		t.Fatalf("GET with memory:read: status %d, body %s", r.status, r.body)
	}
	if m := s.member(t, "20001"); m.Version != 0 {
		t.Fatalf("forbidden requests wrote memory: %+v", m)
	}
	if calls := s.trigger.calls(); len(calls) != 0 {
		t.Fatalf("forbidden consolidate triggered %v", calls)
	}
}

func TestMemoryAPI_GetShape(t *testing.T) {
	s := newMemServer(t, false)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	s.seed(t, "20001", "小明", "喜欢咖啡", intPtr(52))
	s.seed(t, "20002", "小红", "", intPtr(21))
	s.seed(t, "20003", "", "", nil)
	if err := s.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: memGroupKey,
		Summary: "大家在聊咖啡。", CursorSeq: 7, LastError: "parse failed"}); err != nil {
		t.Fatal(err)
	}

	r := s.do(t, http.MethodGet, memoryPath, memoryToken, nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET memory: status %d, body %s", r.status, r.body)
	}
	var raw map[string]any
	decode(t, r.body, &raw)
	for _, k := range []string{"conversation_id", "enabled", "summary", "cursor_seq", "last_run_at", "last_error", "members"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("body %s lacks %q", r.body, k)
		}
	}
	var got conloomapi.MemoryState
	decode(t, r.body, &got)
	if got.ConversationID != "group_10001" || got.Enabled || got.Summary != "大家在聊咖啡。" || got.CursorSeq != 7 ||
		got.LastRunAt != nil || got.LastError != "parse failed" {
		t.Errorf("state = %+v", got)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %+v, want 20001 and 20002 only", got.Members)
	}
	m0, m1 := got.Members[0], got.Members[1]
	if m0.UserID != "20001" || m0.Name != "小明" || m0.Note != "喜欢咖啡" || m0.Affinity == nil || *m0.Affinity != 52 ||
		m0.Tier != "熟悉" || m0.UpdatedAt.IsZero() {
		t.Errorf("member 20001 = %+v", m0)
	}
	if m1.UserID != "20002" || m1.Name != "小红" || m1.Note != "" || m1.Affinity == nil || *m1.Affinity != 21 || m1.Tier != "认识" {
		t.Errorf("member 20002 = %+v", m1)
	}
}

func TestMemoryAPI_GetEmpty(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	r := s.do(t, http.MethodGet, memoryPath, memoryToken, nil, nil)
	var got conloomapi.MemoryState
	decode(t, r.body, &got)
	if r.status != http.StatusOK || !got.Enabled || got.Summary != "" || got.LastRunAt != nil {
		t.Fatalf("GET memory: status %d, body %s", r.status, r.body)
	}
	if !strings.Contains(string(r.body), `"members":[]`) {
		t.Errorf("members must encode as an empty array: %s", r.body)
	}
}

func TestMemoryAPI_PutPartialWritesRevision(t *testing.T) {
	s := newMemServer(t, false)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	s.seed(t, "20001", "小明", "喜欢咖啡", intPtr(52))
	path := memoryPath + "/members/20001"

	r := s.do(t, http.MethodPut, path, memoryToken, `{"note":"  喜欢茶  ","reason":"更正"}`, nil)
	if r.status != http.StatusOK {
		t.Fatalf("PUT note: status %d, body %s", r.status, r.body)
	}
	var got conloomapi.MemberMemoryState
	decode(t, r.body, &got)
	if got.UserID != "20001" || got.Name != "小明" || got.Note != "喜欢茶" || got.Affinity == nil || *got.Affinity != 52 ||
		got.Tier != "熟悉" || got.UpdatedAt.IsZero() {
		t.Fatalf("PUT note response = %+v", got)
	}
	m := s.member(t, "20001")
	if m.Note != "喜欢茶" || m.Affinity == nil || *m.Affinity != 52 || m.DisplayName != "小明" {
		t.Fatalf("stored = %+v", m)
	}
	revs, err := s.st.ListMemoryRevisions(context.Background(), memGroupKey, "20001")
	if err != nil {
		t.Fatal(err)
	}
	last := revs[len(revs)-1]
	if len(revs) != 2 || last.Field != store.FieldNote || last.OldValue != "喜欢咖啡" || last.NewValue != "喜欢茶" ||
		last.Source != store.RevisionAPI || last.Reason != "更正" {
		t.Fatalf("revisions = %+v", revs)
	}
	audits := s.audits.all()
	if len(audits) != 1 {
		t.Fatalf("audits = %+v, want 1", audits)
	}
	a := audits[0]
	var detail map[string]any
	if err := json.Unmarshal([]byte(a.Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if a.Kind != "memory.update" || a.BotInstanceID != "bot_main" || a.Actor != "memory-main" || a.Target != memGroupKey ||
		len(detail) != 2 || detail["user_id"] != "20001" || !slices.Equal(anyStrings(detail["fields"]), []string{"note"}) {
		t.Fatalf("audit = %+v", a)
	}
	if n := s.affinityChanges(t, store.RevisionAPI); n != 0 {
		t.Fatalf("note change counted %v affinity changes", n)
	}

	r = s.do(t, http.MethodPut, memoryPath+"/members/20002", memoryToken, conloomapi.MemberMemoryPut{Affinity: intPtr(65)}, nil)
	decode(t, r.body, &got)
	if r.status != http.StatusOK || got.UserID != "20002" || got.Affinity == nil || *got.Affinity != 65 || got.Tier != "亲近" || got.Note != "" {
		t.Fatalf("PUT new member: status %d, body %s", r.status, r.body)
	}
	if m := s.member(t, "20002"); m.Affinity == nil || *m.Affinity != 65 || m.Version != 1 {
		t.Fatalf("new member stored = %+v", m)
	}
	if n := s.affinityChanges(t, store.RevisionAPI); n != 1 {
		t.Fatalf("affinity changes{api} = %v, want 1", n)
	}
	if audits := s.audits.all(); len(audits) != 2 || !strings.Contains(audits[1].Detail, `"fields":["affinity"]`) {
		t.Fatalf("audits = %+v", audits)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

func TestMemoryAPI_PutValidation(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	path := memoryPath + "/members/20001"
	for name, body := range map[string]any{
		"note of 301 runes": conloomapi.MemberMemoryPut{Note: strPtr(strings.Repeat("字", memory.NoteMaxRunes+1))},
		"affinity 101":      conloomapi.MemberMemoryPut{Affinity: intPtr(101)},
		"affinity -1":       conloomapi.MemberMemoryPut{Affinity: intPtr(-1)},
		"empty object":      `{}`,
		"reason only":       `{"reason":"x"}`,
		"empty body":        "",
		"malformed":         `{"note":`,
	} {
		t.Run(name, func(t *testing.T) {
			expectError(t, s.do(t, http.MethodPut, path, memoryToken, body, nil), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
		})
	}
	if m := s.member(t, "20001"); m.Version != 0 {
		t.Fatalf("rejected PUTs wrote %+v", m)
	}
	r := s.do(t, http.MethodPut, path, memoryToken,
		conloomapi.MemberMemoryPut{Note: strPtr(" " + strings.Repeat("字", 200) + " "), Affinity: intPtr(100)}, nil)
	if r.status != http.StatusOK {
		t.Fatalf("200 runes and affinity 100: status %d, body %s", r.status, r.body)
	}
	if audits := s.audits.all(); len(audits) != 1 || !strings.Contains(audits[0].Detail, `"fields":["note","affinity"]`) {
		t.Fatalf("audits = %+v", audits)
	}
}

func TestMemoryAPI_DeleteEqualsForget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := newMemServer(t, enabled)
		s.accept(t, adapterToken, s.event("20001", "大家好"))
		s.seed(t, "20001", "小明", "喜欢咖啡", intPtr(52))
		s.seed(t, "20002", "小红", "喜欢猫", intPtr(30))

		r := s.do(t, http.MethodDelete, memoryPath+"/members/20001", memoryToken, nil, nil)
		if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"ok":true}` {
			t.Fatalf("enabled=%v DELETE: status %d, body %s", enabled, r.status, r.body)
		}
		m := s.member(t, "20001")
		if m.Note != "" || m.Affinity != nil || m.DisplayName != "" || m.ForgottenAt.IsZero() {
			t.Fatalf("enabled=%v forgotten member = %+v", enabled, m)
		}
		if revs, err := s.st.ListMemoryRevisions(context.Background(), memGroupKey, "20001"); err != nil || len(revs) != 0 {
			t.Fatalf("enabled=%v revisions after DELETE = %+v, %v", enabled, revs, err)
		}
		if m := s.member(t, "20002"); m.Note != "喜欢猫" {
			t.Fatalf("enabled=%v other member changed: %+v", enabled, m)
		}
		audits := s.audits.all()
		if len(audits) != 1 || audits[0].Kind != "memory.forget" || audits[0].Actor != "memory-main" ||
			audits[0].Target != memGroupKey || audits[0].Detail != `{"user_id":"20001"}` {
			t.Fatalf("enabled=%v audits = %+v", enabled, audits)
		}
		if n := s.affinityChanges(t, store.RevisionAPI); n != 1 {
			t.Fatalf("enabled=%v affinity changes{api} = %v, want 1", enabled, n)
		}

		if r := s.do(t, http.MethodDelete, memoryPath+"/members/20003", memoryToken, nil, nil); r.status != http.StatusOK {
			t.Fatalf("enabled=%v DELETE of a member without memory: status %d, body %s", enabled, r.status, r.body)
		}
		if m := s.member(t, "20003"); m.ForgottenAt.IsZero() {
			t.Fatalf("enabled=%v member without memory not marked forgotten: %+v", enabled, m)
		}
		if n := s.affinityChanges(t, store.RevisionAPI); n != 1 {
			t.Fatalf("enabled=%v affinity changes{api} after forgetting 20003 = %v, want 1", enabled, n)
		}
	}
}

func TestMemoryAPI_ConsolidateDisabledAndAccepted(t *testing.T) {
	s := newMemServer(t, false)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	path := memoryPath + "/consolidate"
	expectError(t, s.do(t, http.MethodPost, path, memoryToken, nil, nil), http.StatusConflict, conloomapi.CodeMemoryDisabled)
	if calls := s.trigger.calls(); len(calls) != 0 {
		t.Fatalf("disabled consolidate triggered %v", calls)
	}

	const policyPath = "/api/v1/conversations/group_10001/policy"
	var cur conloomapi.PolicyState
	decode(t, s.do(t, http.MethodGet, policyPath, adminToken, nil, nil).body, &cur)
	p := cur.Policy
	p.Memory.Enabled = true
	if r := s.do(t, http.MethodPut, policyPath, adminToken, conloomapi.PolicyPut{Policy: p, Reason: "开启记忆"}, nil); r.status != http.StatusOK {
		t.Fatalf("enable memory: status %d, body %s", r.status, r.body)
	}

	r := s.do(t, http.MethodPost, path, memoryToken, nil, nil)
	if r.status != http.StatusAccepted || strings.TrimSpace(string(r.body)) != `{"accepted":true}` {
		t.Fatalf("POST consolidate: status %d, body %s", r.status, r.body)
	}
	if calls := s.trigger.calls(); !slices.Equal(calls, []string{memGroupKey}) {
		t.Fatalf("trigger calls = %v, want [%s]", calls, memGroupKey)
	}
}

func TestMemoryAPI_ConsolidatePausedConflict(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	const policyPath = "/api/v1/conversations/group_10001/policy"
	setInteractive := func(n int) {
		t.Helper()
		var cur conloomapi.PolicyState
		decode(t, s.do(t, http.MethodGet, policyPath, adminToken, nil, nil).body, &cur)
		p := cur.Policy
		p.Concurrency.MaxInteractiveRequests = n
		if r := s.do(t, http.MethodPut, policyPath, adminToken, conloomapi.PolicyPut{Policy: p, Reason: "暂停"}, nil); r.status != http.StatusOK {
			t.Fatalf("set max_interactive_requests %d: status %d, body %s", n, r.status, r.body)
		}
	}
	setInteractive(0)
	path := memoryPath + "/consolidate"
	expectError(t, s.do(t, http.MethodPost, path, memoryToken, nil, nil), http.StatusConflict, conloomapi.CodePaused)
	if calls := s.trigger.calls(); len(calls) != 0 {
		t.Fatalf("paused consolidate triggered %v", calls)
	}
	setInteractive(1)
	if r := s.do(t, http.MethodPost, path, memoryToken, nil, nil); r.status != http.StatusAccepted {
		t.Fatalf("POST consolidate after resuming: status %d, body %s", r.status, r.body)
	}
	if calls := s.trigger.calls(); !slices.Equal(calls, []string{memGroupKey}) {
		t.Fatalf("trigger calls = %v, want [%s]", calls, memGroupKey)
	}
}

func TestMemoryAPI_ConsolidateWithoutTrigger(t *testing.T) {
	s := newTestServerWith(t, serverOpts{configure: func(c *config.Config) {
		c.APITokens = append(c.APITokens, config.APIToken{Name: "memory-main", Token: memoryToken,
			BotInstanceID: "bot_main", Scopes: []string{conloomapi.ScopeMemoryWrite}})
		c.DefaultGroupPolicy.Memory.Enabled = true
	}})
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	if r := s.do(t, http.MethodPost, memoryPath+"/consolidate", memoryToken, nil, nil); r.status != http.StatusAccepted {
		t.Fatalf("POST consolidate without a trigger: status %d, body %s", r.status, r.body)
	}
	if r := s.do(t, http.MethodPut, memoryPath+"/members/20001", memoryToken,
		conloomapi.MemberMemoryPut{Affinity: intPtr(40)}, nil); r.status != http.StatusOK {
		t.Fatalf("PUT without counters: status %d, body %s", r.status, r.body)
	}
}

func TestMemoryAPI_OtherBotConversation404(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, otherToken, s.event("20001", "大家好", forBot("bot_other")))
	member := memoryPath + "/members/20001"
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, memoryPath, nil},
		{http.MethodPut, member, conloomapi.MemberMemoryPut{Note: strPtr("喜欢咖啡")}},
		{http.MethodDelete, member, nil},
		{http.MethodPost, memoryPath + "/consolidate", nil},
		{http.MethodGet, "/api/v1/conversations/group_99999/memory", nil},
	} {
		expectError(t, s.do(t, c.method, c.path, memoryToken, c.body, nil), http.StatusNotFound, conloomapi.CodeNotFound)
	}
	otherKey := store.ConversationKey("bot_other", "qq", conloomapi.ConvGroup, "group_10001")
	if got, err := s.st.GetMemberMemories(context.Background(), otherKey, []string{"20001"}); err != nil || len(got) != 0 {
		t.Fatalf("other bot's memory written: %+v, %v", got, err)
	}
	if calls := s.trigger.calls(); len(calls) != 0 {
		t.Fatalf("other bot's conversation triggered %v", calls)
	}
}

func TestMemoryAPI_PrivateConversation(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("29999", "在吗", func(ev *conloomapi.Event) {
		ev.Conversation = conloomapi.Conversation{Type: conloomapi.ConvPrivate, ID: "private_29999", PlatformConversationID: "29999"}
	}))
	privKey := store.ConversationKey("bot_main", "qq", conloomapi.ConvPrivate, "private_29999")
	ctx := context.Background()
	if _, err := s.st.ApplyMemoryWrite(ctx, store.MemoryWrite{ConversationKey: privKey,
		Group: &store.GroupMemory{ConversationKey: privKey, Summary: "最近在聊钢琴。", CursorSeq: 5},
		Revisions: []store.MemoryRevision{{ConversationKey: privKey, Field: store.FieldSummary, NewValue: "最近在聊钢琴。",
			Source: store.RevisionConsolidate}},
	}); err != nil {
		t.Fatal(err)
	}
	const base = "/api/v1/conversations/private_29999/memory"

	r := s.do(t, http.MethodGet, base, memoryToken, nil, nil)
	var state conloomapi.MemoryState
	decode(t, r.body, &state)
	if r.status != http.StatusOK || !state.Enabled || state.Summary != "最近在聊钢琴。" || state.ConversationID != "private_29999" {
		t.Fatalf("GET private memory: status %d, state %+v", r.status, state)
	}

	note := strings.Repeat("记", memory.PrivateNoteMaxRunes)
	if r := s.do(t, http.MethodPut, base+"/members/29999", memoryToken, conloomapi.MemberMemoryPut{Note: &note, Affinity: intPtr(30)}, nil); r.status != http.StatusOK {
		t.Fatalf("PUT a %d-rune private note: status %d, body %s", memory.PrivateNoteMaxRunes, r.status, r.body)
	}
	long := note + "多"
	expectError(t, s.do(t, http.MethodPut, base+"/members/29999", memoryToken, conloomapi.MemberMemoryPut{Note: &long}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	got, err := s.st.GetMemberMemories(ctx, privKey, []string{"29999"})
	if err != nil || got["29999"].Note != note || got["29999"].Affinity == nil || *got["29999"].Affinity != 30 {
		t.Fatalf("private member = %+v, %v", got["29999"], err)
	}

	if r := s.do(t, http.MethodPost, base+"/consolidate", memoryToken, nil, nil); r.status != http.StatusAccepted {
		t.Fatalf("POST private consolidate: status %d, body %s", r.status, r.body)
	}
	if calls := s.trigger.calls(); !slices.Equal(calls, []string{privKey}) {
		t.Fatalf("trigger calls = %v, want [%s]", calls, privKey)
	}

	if r := s.do(t, http.MethodDelete, base+"/members/29999", memoryToken, nil, nil); r.status != http.StatusOK {
		t.Fatalf("DELETE private member: status %d, body %s", r.status, r.body)
	}
	got, err = s.st.GetMemberMemories(ctx, privKey, []string{"29999"})
	if m := got["29999"]; err != nil || m.Note != "" || m.Affinity != nil || m.ForgottenAt.IsZero() {
		t.Fatalf("forgotten private member = %+v, %v", m, err)
	}
	if g, err := s.st.GetGroupMemory(ctx, privKey); err != nil || g.Summary != "" || g.CursorSeq != 5 {
		t.Fatalf("private summary after DELETE = %+v, %v; want cleared with the cursor kept", g, err)
	}
	if revs, err := s.st.ListMemoryRevisions(ctx, privKey, ""); err != nil || len(revs) != 0 {
		t.Fatalf("private summary revisions after DELETE = %+v, %v; want none", revs, err)
	}
}

func TestMemoryAPI_GroupNoteLimitUnchanged(t *testing.T) {
	s := newMemServer(t, true)
	s.accept(t, adapterToken, s.event("20001", "大家好"))
	long := strings.Repeat("记", memory.NoteMaxRunes+1)
	expectError(t, s.do(t, http.MethodPut, memoryPath+"/members/20001", memoryToken, conloomapi.MemberMemoryPut{Note: &long}, nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if err := s.st.PutGroupMemory(context.Background(), store.GroupMemory{ConversationKey: memGroupKey, Summary: "大家在聊咖啡。"}); err != nil {
		t.Fatal(err)
	}
	if r := s.do(t, http.MethodDelete, memoryPath+"/members/20001", memoryToken, nil, nil); r.status != http.StatusOK {
		t.Fatalf("DELETE: status %d", r.status)
	}
	if g, err := s.st.GetGroupMemory(context.Background(), memGroupKey); err != nil || g.Summary != "大家在聊咖啡。" {
		t.Fatalf("group summary after DELETE = %+v, %v; want kept", g, err)
	}
}
