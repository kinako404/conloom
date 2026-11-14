package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var (
	writeGroup = store.ConversationRecord{
		BotInstanceID: dataBot, ID: "conv_10001", Type: conloomapi.ConvGroup, Platform: "qq",
		PlatformConversationID: "10001", Key: store.ConversationKey(dataBot, "qq", conloomapi.ConvGroup, "conv_10001"),
	}
	writeChannel = store.ConversationRecord{
		BotInstanceID: dataBot, ID: "conv_10002", Type: "channel", Platform: "qq",
		PlatformConversationID: "10002", Key: store.ConversationKey(dataBot, "qq", "channel", "conv_10002"),
	}
)

type fakeTrigger struct {
	mu   sync.Mutex
	keys []string
}

func (f *fakeTrigger) Trigger(convKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, convKey)
}

func (f *fakeTrigger) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...)
}

// failingAuditDB is a store whose audit writes fail.
type failingAuditDB struct{ *sqlite.DB }

func (failingAuditDB) InsertAudit(context.Context, store.AuditEvent) error {
	return errors.New("database is locked")
}

// writeHarness is a storeHarness that audits to its store and records
// consolidation requests in trigger, with writeGroup and writeChannel stored.
type writeHarness struct {
	*storeHarness
	trigger *fakeTrigger
}

var catPersona = func() conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.PersonaName = "cat"
	p.CharacterName = "小猫"
	return p
}()

func newWriteHarness(t *testing.T, mutate func(*Deps)) *writeHarness {
	t.Helper()
	h := &writeHarness{trigger: &fakeTrigger{}}
	h.storeHarness = newStoreHarness(t,
		[]config.BotConfig{{ID: dataBot, Platform: "qq", Name: "织语", PersonaTemplate: "default"}},
		map[string]conloomapi.PersonaDoc{"cat": catPersona},
		func(d *Deps) {
			d.Audit, d.Memory = d.Store.(*sqlite.DB), h.trigger
			if mutate != nil {
				mutate(d)
			}
		})
	h.storeConv(writeGroup)
	h.storeConv(writeChannel)
	return h
}

// write sends an authenticated request carrying the CSRF token.
func (h *writeHarness) write(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var s string
	switch b := body.(type) {
	case nil:
	case string:
		s = b
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		s = string(raw)
	}
	return h.do(method, path, s, withCookie(h.cookie), withCSRF(h.csrf))
}

func (h *writeHarness) audits(kind string) []store.AuditEvent {
	h.t.Helper()
	events, err := h.db.ListAudit(context.Background(), store.AuditFilter{Kind: kind, Limit: 100})
	if err != nil {
		h.t.Fatalf("ListAudit: %v", err)
	}
	return events
}

func (h *writeHarness) currentPolicy() store.PolicyVersion {
	h.t.Helper()
	pv, err := h.policies.Current(context.Background(), writeGroup.Key)
	if err != nil {
		h.t.Fatalf("Current policy: %v", err)
	}
	return pv
}

type testPersonaView struct {
	Version   int                   `json:"version"`
	Template  string                `json:"template"`
	Persona   conloomapi.PersonaDoc `json:"persona"`
	Reason    string                `json:"reason"`
	UpdatedBy string                `json:"updated_by"`
	CreatedAt *time.Time            `json:"created_at"`
}

func decodeCurrent[V any](t *testing.T, w *httptest.ResponseRecorder, status int) V {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, status, w.Body)
	}
	var resp struct {
		Current *V `json:"current"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Current == nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return *resp.Current
}

func policyWith(edit func(*conloomapi.GroupPolicy)) conloomapi.GroupPolicy {
	p := conloomapi.DefaultGroupPolicy()
	edit(&p)
	return p
}

func TestPutPolicy_UpdatesAndAudits(t *testing.T) {
	h := newWriteHarness(t, nil)
	p := policyWith(func(p *conloomapi.GroupPolicy) { p.Context.MaxRecentMessages = 30 })
	w := h.write(http.MethodPut, convPath(writeGroup.Key, "policy"), conloomapi.PolicyPut{Policy: p, Reason: "收紧上下文"})
	got := decodeCurrent[policyVersionResp](t, w, http.StatusOK)
	if got.Version != 1 || got.Reason != "收紧上下文" || got.UpdatedBy != "panel:"+testUser ||
		got.Policy.Context.MaxRecentMessages != 30 || got.CreatedAt == nil || got.CreatedAt.Location() != time.UTC {
		t.Errorf("current = %+v", got)
	}
	if cur := h.currentPolicy(); cur.Version != 1 || cur.Policy.Context.MaxRecentMessages != 30 {
		t.Errorf("Current = v%d with max_recent_messages %d, want v1 with 30", cur.Version, cur.Policy.Context.MaxRecentMessages)
	}
	events := h.audits("policy.update")
	if len(events) != 1 || events[0].Actor != "panel:"+testUser || events[0].BotInstanceID != dataBot || events[0].Target != writeGroup.Key {
		t.Errorf("audit = %+v, want one policy.update by panel:%s", events, testUser)
	}

	bad := policyWith(func(p *conloomapi.GroupPolicy) { p.Context.MaxRecentMessages = -1 })
	want := policy.Validate(bad)
	if want == nil {
		t.Fatal("test policy is valid")
	}
	body := errorBody(t, h.write(http.MethodPut, convPath(writeGroup.Key, "policy"), conloomapi.PolicyPut{Policy: bad}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != want.Error() || body.Retryable {
		t.Errorf("invalid policy error = %+v, want message %q", body, want.Error())
	}
	if cur := h.currentPolicy(); cur.Version != 1 {
		t.Errorf("invalid policy stored v%d", cur.Version)
	}

	errorBody(t, h.write(http.MethodPut, convPath(writeGroup.Key, "policy"), `{"policy":{},"extra":1}`),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodPut, "/panel/api/conversations/qq-main:qq:group:nope/policy", conloomapi.PolicyPut{Policy: p}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.do(http.MethodPut, convPath(writeGroup.Key, "policy"), `{}`, withCookie(h.cookie)),
		http.StatusForbidden, conloomapi.CodeForbidden)

	t.Run("audit failure is still success", func(t *testing.T) {
		h := newWriteHarness(t, func(d *Deps) {
			d.Policies = policy.NewService(failingAuditDB{d.Store.(*sqlite.DB)}, conloomapi.DefaultGroupPolicy())
		})
		got := decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "pause"), nil), http.StatusOK)
		if got.Version != 1 {
			t.Errorf("pause with failing audit = %+v, want v1", got)
		}
	})
}

func TestRestorePolicy(t *testing.T) {
	h := newWriteHarness(t, nil)
	v1 := policyWith(func(p *conloomapi.GroupPolicy) { p.Context.MaxRecentMessages = 30 })
	v2 := policyWith(func(p *conloomapi.GroupPolicy) {
		p.Context.MaxRecentMessages = 40
		p.Proactive.Enabled = false
	})
	for _, p := range []conloomapi.GroupPolicy{v1, v2} {
		if w := h.write(http.MethodPut, convPath(writeGroup.Key, "policy"), conloomapi.PolicyPut{Policy: p}); w.Code != http.StatusOK {
			t.Fatalf("PUT policy: %d %s", w.Code, w.Body)
		}
	}
	stored, err := h.db.ListPolicyVersions(context.Background(), writeGroup.Key)
	if err != nil {
		t.Fatal(err)
	}

	w := h.write(http.MethodPost, convPath(writeGroup.Key, "policy", "restore"), map[string]any{"version": 1, "reason": "回到第一版"})
	got := decodeCurrent[policyVersionResp](t, w, http.StatusOK)
	if got.Version != 3 || got.Reason != "回到第一版" || got.UpdatedBy != "panel:"+testUser {
		t.Errorf("current = %+v, want v3 by panel:%s", got, testUser)
	}
	cur := h.currentPolicy()
	if cur.Version != 3 || !reflect.DeepEqual(cur.Policy, stored[len(stored)-1].Policy) {
		t.Errorf("Current = v%d %+v, want v3 equal to v1 %+v", cur.Version, cur.Policy, stored[len(stored)-1].Policy)
	}
	if !reflect.DeepEqual(got.Policy, cur.Policy) {
		t.Errorf("response policy %+v differs from stored %+v", got.Policy, cur.Policy)
	}

	got = decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "policy", "restore"), map[string]any{"version": 2}),
		http.StatusOK)
	if got.Version != 4 || got.Reason != "restore v2" || got.Policy.Proactive.Enabled {
		t.Errorf("restore v2 = %+v, want v4 with reason \"restore v2\"", got)
	}

	errorBody(t, h.write(http.MethodPost, convPath(writeGroup.Key, "policy", "restore"), map[string]any{"version": 9}),
		http.StatusNotFound, conloomapi.CodeNotFound)
	errorBody(t, h.write(http.MethodPost, convPath(writeGroup.Key, "policy", "restore"), map[string]any{"reason": "x"}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if n := len(h.audits("policy.update")); n != 4 {
		t.Errorf("policy.update audits = %d, want 4", n)
	}
}

func TestPauseResume(t *testing.T) {
	h := newWriteHarness(t, nil)
	got := decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "pause"), nil), http.StatusOK)
	if got.Version != 1 || got.Policy.Concurrency.MaxInteractiveRequests != 0 || got.UpdatedBy != "panel:"+testUser {
		t.Errorf("pause = %+v", got)
	}
	got = decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "pause"), nil), http.StatusOK)
	if got.Version != 1 {
		t.Errorf("pausing again = v%d, want v1 unchanged", got.Version)
	}
	got = decodeCurrent[policyVersionResp](t, h.write(http.MethodPost, convPath(writeGroup.Key, "resume"), nil), http.StatusOK)
	if got.Version != 2 || got.Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Errorf("resume = %+v, want v2 with max_interactive_requests 1", got)
	}
	if cur := h.currentPolicy(); cur.Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Errorf("Current max_interactive_requests = %d, want 1", cur.Policy.Concurrency.MaxInteractiveRequests)
	}
	events := h.audits("policy.update")
	if len(events) != 2 {
		t.Fatalf("policy.update audits = %d, want 2", len(events))
	}
	for _, e := range events {
		if e.Actor != "panel:"+testUser {
			t.Errorf("audit actor = %q", e.Actor)
		}
	}

	fresh := newWriteHarness(t, nil)
	got = decodeCurrent[policyVersionResp](t, fresh.write(http.MethodPost, convPath(writeGroup.Key, "resume"), nil), http.StatusOK)
	if got.Version != 0 || got.CreatedAt != nil || got.Policy.Concurrency.MaxInteractiveRequests != 1 {
		t.Errorf("resume of a running default policy = %+v, want v0 with null created_at", got)
	}
	errorBody(t, h.write(http.MethodPost, "/panel/api/conversations/qq-main:qq:group:nope/pause", nil),
		http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestPutPersona_TemplateAndRollback(t *testing.T) {
	h := newWriteHarness(t, nil)
	own := conloomapi.DefaultPersona()
	own.CharacterName = "小织"
	path := convPath(writeGroup.Key, "persona")

	got := decodeCurrent[testPersonaView](t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Persona: &own, Reason: "改名"}), http.StatusOK)
	if got.Version != 1 || got.Template != "default" || got.Persona.CharacterName != "小织" || got.Reason != "改名" ||
		got.UpdatedBy != "panel:"+testUser || got.CreatedAt == nil {
		t.Errorf("persona put = %+v", got)
	}
	got = decodeCurrent[testPersonaView](t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Template: "cat"}), http.StatusOK)
	if got.Version != 2 || got.Template != "cat" || !reflect.DeepEqual(got.Persona, catPersona) || got.Reason != "template cat" {
		t.Errorf("template put = %+v", got)
	}
	one := 1
	got = decodeCurrent[testPersonaView](t, h.write(http.MethodPut, path, conloomapi.PersonaPut{RollbackTo: &one}), http.StatusOK)
	if got.Version != 3 || got.Template != "default" || !reflect.DeepEqual(got.Persona, own) || got.Reason != "rollback to v1" {
		t.Errorf("rollback = %+v", got)
	}

	events := h.audits("persona.update")
	if len(events) != 3 || events[0].Actor != "panel:"+testUser || events[0].BotInstanceID != dataBot {
		t.Errorf("audit = %+v, want three persona.update by panel:%s", events, testUser)
	}

	body := errorBody(t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Template: "cat", RollbackTo: &one}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != "persona: invalid: exactly one of persona, rollback_to and template must be set" {
		t.Errorf("message = %q", body.Message)
	}
	body = errorBody(t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Template: "dog"}), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != `persona: invalid: unknown template "dog"` {
		t.Errorf("message = %q", body.Message)
	}
	both := own
	both.Adult, both.Minor = true, true
	body = errorBody(t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Persona: &both}), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != "persona: invalid: a minor persona cannot be adult" {
		t.Errorf("message = %q", body.Message)
	}
	nine := 9
	errorBody(t, h.write(http.MethodPut, path, conloomapi.PersonaPut{RollbackTo: &nine}), http.StatusNotFound, conloomapi.CodeNotFound)

	h.cfg.Bots = nil
	errorBody(t, h.write(http.MethodPut, path, conloomapi.PersonaPut{Template: "cat"}), http.StatusNotFound, conloomapi.CodeNotFound)
}

func TestMemberEditAndForget(t *testing.T) {
	h := newWriteHarness(t, nil)
	ctx := context.Background()
	path := convPath(writeGroup.Key, "memory", "members", "20001")

	w := h.write(http.MethodPut, path, map[string]any{"note": "  喜欢猫  ", "affinity": 60, "reason": "面板修正"})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT member: %d %s", w.Code, w.Body)
	}
	var m conloomapi.MemberMemoryState
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.UserID != "20001" || m.Note != "喜欢猫" || m.Affinity == nil || *m.Affinity != 60 || m.Tier == "" {
		t.Errorf("member = %+v", m)
	}
	revs, err := h.db.ListMemoryRevisions(ctx, writeGroup.Key, "20001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %+v, want note and affinity", revs)
	}
	for _, r := range revs {
		if r.Source != store.RevisionPanel || r.Reason != "面板修正" {
			t.Errorf("revision = %+v, want source %q", r, store.RevisionPanel)
		}
	}
	if e := h.audits("memory.update"); len(e) != 1 || e[0].Actor != "panel:"+testUser || e[0].BotInstanceID != dataBot {
		t.Errorf("memory.update audit = %+v", e)
	}

	body := errorBody(t, h.write(http.MethodPut, path, map[string]any{"affinity": 101}), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	if body.Message != "affinity must be in [0, 100], got 101" {
		t.Errorf("message = %q", body.Message)
	}
	errorBody(t, h.write(http.MethodPut, path, `{}`), http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.write(http.MethodPut, convPath(writeChannel.Key, "memory", "members", "20001"), map[string]any{"note": "x"}),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.do(http.MethodPut, path, `{"note":"x"}`, withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
	errorBody(t, h.do(http.MethodDelete, path, "", withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)

	w = h.write(http.MethodDelete, path, nil)
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("DELETE member: %d %q", w.Code, w.Body)
	}
	got, err := h.db.GetMemberMemories(ctx, writeGroup.Key, []string{"20001"})
	if err != nil {
		t.Fatal(err)
	}
	if mm := got["20001"]; mm.Note != "" || mm.Affinity != nil {
		t.Errorf("after forget: %+v", mm)
	}
	if e := h.audits("memory.forget"); len(e) != 1 || e[0].Actor != "panel:"+testUser || e[0].Target != writeGroup.Key {
		t.Errorf("memory.forget audit = %+v", e)
	}
	errorBody(t, h.write(http.MethodDelete, "/panel/api/conversations/qq-main:qq:group:nope/memory/members/20001", nil),
		http.StatusNotFound, conloomapi.CodeNotFound)

	t.Run("audit failure is still success", func(t *testing.T) {
		h := newWriteHarness(t, func(d *Deps) {
			db := d.Store.(*sqlite.DB)
			d.MemoryAdmin = memory.NewAdmin(memory.AdminDeps{Store: failingAuditDB{db}, Policies: d.Policies})
		})
		if w := h.write(http.MethodPut, path, map[string]any{"note": "喜欢猫"}); w.Code != http.StatusOK {
			t.Errorf("PUT with failing audit: %d %s", w.Code, w.Body)
		}
		if w := h.write(http.MethodDelete, path, nil); w.Code != http.StatusNoContent {
			t.Errorf("DELETE with failing audit: %d %s", w.Code, w.Body)
		}
	})
}

func TestConsolidate_TriggersAndAudits(t *testing.T) {
	h := newWriteHarness(t, nil)
	path := convPath(writeGroup.Key, "memory", "consolidate")

	errorBody(t, h.write(http.MethodPost, path, nil), http.StatusConflict, conloomapi.CodeMemoryDisabled)
	on := policyWith(func(p *conloomapi.GroupPolicy) { p.Memory.Enabled = true })
	if w := h.write(http.MethodPut, convPath(writeGroup.Key, "policy"), conloomapi.PolicyPut{Policy: on}); w.Code != http.StatusOK {
		t.Fatalf("enable memory: %d %s", w.Code, w.Body)
	}

	w := h.write(http.MethodPost, path, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("consolidate: %d %s", w.Code, w.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || !reflect.DeepEqual(resp, map[string]any{"accepted": true}) {
		t.Errorf("body = %s, want {\"accepted\":true}", w.Body)
	}
	if got := h.trigger.all(); !reflect.DeepEqual(got, []string{writeGroup.Key}) {
		t.Errorf("triggers = %v, want one for %s", got, writeGroup.Key)
	}
	events := h.audits("memory.consolidate")
	if len(events) != 1 || events[0].Actor != "panel:"+testUser || events[0].BotInstanceID != dataBot || events[0].Target != writeGroup.Key {
		t.Errorf("audit = %+v, want one memory.consolidate by panel:%s", events, testUser)
	}

	if w := h.write(http.MethodPost, convPath(writeGroup.Key, "pause"), nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d", w.Code)
	}
	errorBody(t, h.write(http.MethodPost, path, nil), http.StatusConflict, conloomapi.CodePaused)
	errorBody(t, h.write(http.MethodPost, convPath(writeChannel.Key, "memory", "consolidate"), nil),
		http.StatusBadRequest, conloomapi.CodeInvalidEvent)
	errorBody(t, h.do(http.MethodPost, path, "", withCookie(h.cookie)), http.StatusForbidden, conloomapi.CodeForbidden)
	if n := len(h.trigger.all()); n != 1 {
		t.Errorf("triggers = %d after refused requests, want 1", n)
	}
	if n := len(h.audits("memory.consolidate")); n != 1 {
		t.Errorf("memory.consolidate audits = %d, want 1", n)
	}
}

func TestFail_WriteErrors(t *testing.T) {
	h := newHarness(t, nil)
	for _, tc := range []struct {
		err       error
		status    int
		code      string
		message   string
		retryable bool
	}{
		{fmt.Errorf("%w: %w", policy.ErrInvalid, errors.New("context.max_recent_messages must be positive")),
			http.StatusBadRequest, conloomapi.CodeInvalidEvent, "policy: invalid: context.max_recent_messages must be positive", false},
		{fmt.Errorf("%w: unknown template %q", persona.ErrInvalid, "dog"),
			http.StatusBadRequest, conloomapi.CodeInvalidEvent, `persona: invalid: unknown template "dog"`, false},
		{fmt.Errorf("%w: affinity must be in [0, 100], got 101", memory.ErrInvalid),
			http.StatusBadRequest, conloomapi.CodeInvalidEvent, "affinity must be in [0, 100], got 101", false},
		{fmt.Errorf("update memory of 20001: %w", memory.ErrUpdateConflict),
			http.StatusServiceUnavailable, conloomapi.CodeInternal, "member memory kept changing; try again", true},
		{memory.ErrMemoryDisabled,
			http.StatusConflict, conloomapi.CodeMemoryDisabled, "memory is not enabled for this conversation", false},
		{memory.ErrPaused,
			http.StatusConflict, conloomapi.CodePaused, "the conversation is paused; memory is not consolidated", false},
	} {
		r := httptest.NewRequest(http.MethodPut, "http://"+testHost+"/panel/api/x", nil)
		w := httptest.NewRecorder()
		h.srv.fail(w, r, tc.err)
		if w.Code != tc.status {
			t.Errorf("%v: status %d, want %d", tc.err, w.Code, tc.status)
			continue
		}
		var resp conloomapi.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error.Code != tc.code || resp.Error.Message != tc.message || resp.Error.Retryable != tc.retryable {
			t.Errorf("%v: body %+v, want code %s message %q retryable %v", tc.err, resp.Error, tc.code, tc.message, tc.retryable)
		}
	}
}
