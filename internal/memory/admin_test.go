package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

var adminConv = store.ConversationRecord{
	BotInstanceID: "qq-main", ID: "group_10001", Type: conloomapi.ConvGroup, Platform: "qq",
	PlatformConversationID: "10001",
	Key:                    store.ConversationKey("qq-main", "qq", conloomapi.ConvGroup, "group_10001"),
}

type adminAuditStore struct {
	store.Store
	mu     sync.Mutex
	events []store.AuditEvent
	fail   error
}

func (r *adminAuditStore) InsertAudit(ctx context.Context, a store.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.events = append(r.events, a)
	return r.Store.InsertAudit(ctx, a)
}

func (r *adminAuditStore) all() []store.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

type adminEnv struct {
	admin *Admin
	st    *adminAuditStore
	reg   *prometheus.Registry
	now   time.Time
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	st := &adminAuditStore{Store: newTestStore(t)}
	e := &adminEnv{st: st, reg: prometheus.NewRegistry(), now: time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)}
	e.admin = NewAdmin(AdminDeps{Store: st, Policies: policy.NewService(st, conloomapi.DefaultGroupPolicy()),
		Metrics: obs.NewMetrics(e.reg), Now: func() time.Time { return e.now }})
	return e
}

func (e *adminEnv) affinityChanges(t *testing.T, source string) float64 {
	t.Helper()
	mfs, err := e.reg.Gather()
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

func strPtr(s string) *string { return &s }

func TestAdmin_EditMember_ValidatesLikeAPI(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		put  conloomapi.MemberMemoryPut
		want string
	}{
		"neither field": {conloomapi.MemberMemoryPut{Reason: "x"}, "at least one of note and affinity is required"},
		"note too long": {conloomapi.MemberMemoryPut{Note: strPtr(strings.Repeat("字", NoteMaxRunes+1))},
			"note must be at most 200 characters, got 201"},
		"affinity 101": {conloomapi.MemberMemoryPut{Affinity: intPtr(101)}, "affinity must be in [0, 100], got 101"},
		"affinity -1":  {conloomapi.MemberMemoryPut{Affinity: intPtr(-1)}, "affinity must be in [0, 100], got -1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.admin.EditMember(ctx, adminConv, "20001", c.put, store.RevisionPanel, "panel:admin")
			if !errors.Is(err, ErrInvalid) || err.Error() != "memory: invalid: "+c.want {
				t.Fatalf("err = %v, want ErrInvalid with %q", err, c.want)
			}
		})
	}
	got, err := e.st.GetMemberMemories(ctx, adminConv.Key, []string{"20001"})
	if err != nil || len(got) != 0 {
		t.Fatalf("rejected edits wrote %+v, %v", got, err)
	}
	if audits := e.st.all(); len(audits) != 0 {
		t.Fatalf("rejected edits audited %+v", audits)
	}

	private := adminConv
	private.Type, private.ID = conloomapi.ConvPrivate, "private_20001"
	private.Key = store.ConversationKey("qq-main", "qq", conloomapi.ConvPrivate, "private_20001")
	long := strings.Repeat("记", PrivateNoteMaxRunes)
	if _, err := e.admin.EditMember(ctx, private, "20001", conloomapi.MemberMemoryPut{Note: &long}, store.RevisionPanel, "panel:admin"); err != nil {
		t.Fatalf("private note of %d runes: %v", PrivateNoteMaxRunes, err)
	}
}

func TestAdmin_EditMember_WritesRevisionAndAudit(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	putMember(t, e.st, store.MemberMemory{ConversationKey: adminConv.Key, PlatformUserID: "20001", DisplayName: "小明",
		Note: "喜欢咖啡", Affinity: intPtr(52)})

	got, err := e.admin.EditMember(ctx, adminConv, "20001",
		conloomapi.MemberMemoryPut{Note: strPtr("  喜欢茶  "), Affinity: intPtr(65), Reason: "更正"}, store.RevisionPanel, "panel:admin")
	if err != nil {
		t.Fatalf("EditMember: %v", err)
	}
	if got.UserID != "20001" || got.Name != "小明" || got.Note != "喜欢茶" || got.Affinity == nil || *got.Affinity != 65 ||
		got.Tier != "亲近" || got.UpdatedAt.IsZero() {
		t.Fatalf("member = %+v", got)
	}
	revs, err := e.st.ListMemoryRevisions(ctx, adminConv.Key, "20001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %+v, want note and affinity", revs)
	}
	for _, r := range revs {
		if r.Source != store.RevisionPanel || r.Reason != "更正" {
			t.Fatalf("revision = %+v", r)
		}
	}
	audits := e.st.all()
	if len(audits) != 1 {
		t.Fatalf("audits = %+v, want 1", audits)
	}
	a := audits[0]
	if a.Kind != "memory.update" || a.Actor != "panel:admin" || a.BotInstanceID != "qq-main" || a.Target != adminConv.Key ||
		a.Detail != `{"user_id":"20001","fields":["note","affinity"]}` || !a.CreatedAt.Equal(e.now) {
		t.Fatalf("audit = %+v", a)
	}
	if n := e.affinityChanges(t, store.RevisionPanel); n != 1 {
		t.Fatalf("affinity changes{panel} = %v, want 1", n)
	}

	if _, err := e.admin.EditMember(ctx, adminConv, "20001", conloomapi.MemberMemoryPut{Affinity: intPtr(65)},
		store.RevisionPanel, "panel:admin"); err != nil {
		t.Fatalf("unchanged EditMember: %v", err)
	}
	if audits := e.st.all(); len(audits) != 1 {
		t.Fatalf("unchanged edit audited: %+v", audits)
	}
	if n := e.affinityChanges(t, store.RevisionPanel); n != 1 {
		t.Fatalf("unchanged edit counted: affinity changes{panel} = %v", n)
	}
}

func TestAdmin_EditMember_AuditFailureKeepsChange(t *testing.T) {
	e := newAdminEnv(t)
	e.st.fail = errors.New("disk full")
	got, err := e.admin.EditMember(context.Background(), adminConv, "20001", conloomapi.MemberMemoryPut{Note: strPtr("喜欢茶")},
		store.RevisionPanel, "panel:admin")
	if !errors.Is(err, ErrAudit) || got.UserID != "20001" || got.Note != "喜欢茶" {
		t.Fatalf("EditMember = %+v, %v; want the member and ErrAudit", got, err)
	}
	if m := getMember(t, e.st, adminConv.Key, "20001"); m.Note != "喜欢茶" {
		t.Fatalf("stored = %+v", m)
	}
}

func TestAdmin_ForgetMember(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	putMember(t, e.st, store.MemberMemory{ConversationKey: adminConv.Key, PlatformUserID: "20001", DisplayName: "小明",
		Note: "喜欢咖啡", Affinity: intPtr(52)})
	putMember(t, e.st, store.MemberMemory{ConversationKey: adminConv.Key, PlatformUserID: "20002", DisplayName: "小红",
		Note: "喜欢猫"})

	if err := e.admin.ForgetMember(ctx, adminConv, "20001", "panel:admin"); err != nil {
		t.Fatalf("ForgetMember: %v", err)
	}
	m := getMember(t, e.st, adminConv.Key, "20001")
	if m.Note != "" || m.Affinity != nil || m.DisplayName != "" || !m.ForgottenAt.Equal(e.now) {
		t.Fatalf("forgotten member = %+v", m)
	}
	if m := getMember(t, e.st, adminConv.Key, "20002"); m.Note != "喜欢猫" {
		t.Fatalf("other member changed: %+v", m)
	}
	audits := e.st.all()
	if len(audits) != 1 {
		t.Fatalf("audits = %+v, want 1", audits)
	}
	a := audits[0]
	if a.Kind != "memory.forget" || a.Actor != "panel:admin" || a.BotInstanceID != "qq-main" || a.Target != adminConv.Key ||
		a.Detail != `{"user_id":"20001"}` {
		t.Fatalf("audit = %+v", a)
	}

	e.st.fail = errors.New("disk full")
	if err := e.admin.ForgetMember(ctx, adminConv, "20002", "panel:admin"); !errors.Is(err, ErrAudit) {
		t.Fatalf("ForgetMember with a failing audit = %v, want ErrAudit", err)
	}
	if m := getMember(t, e.st, adminConv.Key, "20002"); m.Note != "" || m.ForgottenAt.IsZero() {
		t.Fatalf("member not forgotten when the audit failed: %+v", m)
	}
}

func TestAdmin_View(t *testing.T) {
	e := newAdminEnv(t)
	ctx := context.Background()
	got, err := e.admin.View(ctx, adminConv)
	if err != nil {
		t.Fatalf("View of an empty conversation: %v", err)
	}
	if got.ConversationID != "group_10001" || got.Enabled || got.Summary != "" || got.LastRunAt != nil ||
		got.Members == nil || len(got.Members) != 0 {
		t.Fatalf("empty view = %+v", got)
	}

	putMember(t, e.st, store.MemberMemory{ConversationKey: adminConv.Key, PlatformUserID: "20001", DisplayName: "小明",
		Note: "喜欢咖啡", Affinity: intPtr(52)})
	putMember(t, e.st, store.MemberMemory{ConversationKey: adminConv.Key, PlatformUserID: "20003"})
	ran := e.now.Add(-time.Hour)
	if err := e.st.PutGroupMemory(ctx, store.GroupMemory{ConversationKey: adminConv.Key, Summary: "大家在聊咖啡。",
		CursorSeq: 7, LastRunAt: ran}); err != nil {
		t.Fatal(err)
	}
	got, err = e.admin.View(ctx, adminConv)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if got.Summary != "大家在聊咖啡。" || got.CursorSeq != 7 || got.LastRunAt == nil || !got.LastRunAt.Equal(ran) ||
		len(got.Members) != 1 || got.Members[0].UserID != "20001" || got.Members[0].Tier != "熟悉" {
		t.Fatalf("view = %+v", got)
	}
}
