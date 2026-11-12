package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestPersonaVersions_AppendAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var docs []conloomapi.PersonaDoc
	for i := 1; i <= 3; i++ {
		doc := conloomapi.DefaultPersona()
		doc.Style.Warmth = float64(i) / 10
		docs = append(docs, doc)
		pv, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
			ConversationKey: testConvKey, TemplateName: "default", Persona: doc,
			Reason: "edit", UpdatedBy: "29999",
		})
		if err != nil {
			t.Fatalf("AppendPersonaVersion %d: %v", i, err)
		}
		if pv.Version != i || pv.CreatedAt.IsZero() {
			t.Fatalf("appended = version %d, created %v; want version %d", pv.Version, pv.CreatedAt, i)
		}
	}
	if n := countRows(t, db, "persona_instances"); n != 1 {
		t.Fatalf("persona_instances = %d, want 1", n)
	}

	v2, err := db.GetPersonaVersion(ctx, testConvKey, 2)
	if err != nil {
		t.Fatalf("GetPersonaVersion: %v", err)
	}
	if v2.Version != 2 || v2.ConversationKey != testConvKey || v2.TemplateName != "default" ||
		v2.Reason != "edit" || v2.UpdatedBy != "29999" || v2.CreatedAt.IsZero() || !reflect.DeepEqual(v2.Persona, docs[1]) {
		t.Fatalf("version 2 = %+v", v2)
	}
	latest, err := db.LatestPersona(ctx, testConvKey)
	if err != nil || latest.Version != 3 || !reflect.DeepEqual(latest.Persona, docs[2]) {
		t.Fatalf("LatestPersona = %+v, %v; want version 3", latest, err)
	}

	if _, err := db.GetPersonaVersion(ctx, testConvKey, 4); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing version err = %v, want ErrNotFound", err)
	}
	if _, err := db.LatestPersona(ctx, testConvKeyB); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing persona err = %v, want ErrNotFound", err)
	}
	other, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{ConversationKey: testConvKeyB, TemplateName: "default"})
	if err != nil || other.Version != 1 {
		t.Fatalf("other conversation append = %+v, %v; want version 1", other, err)
	}
}

func TestPersonaVersions_TemplatePerVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"default", "lighthouse", "default"} {
		if _, err := db.AppendPersonaVersion(ctx, store.PersonaVersion{
			ConversationKey: testConvKey, TemplateName: name, Persona: conloomapi.DefaultPersona(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for version, want := range map[int]string{1: "default", 2: "lighthouse", 3: "default"} {
		if pv, err := db.GetPersonaVersion(ctx, testConvKey, version); err != nil || pv.TemplateName != want {
			t.Errorf("version %d template = %q, %v; want %q", version, pv.TemplateName, err, want)
		}
	}

	// Versions stored before templates were recorded per version fall back
	// to the conversation's first template.
	if _, err := db.w.ExecContext(ctx, `UPDATE persona_versions SET template_name = '' WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if pv, err := db.GetPersonaVersion(ctx, testConvKey, 2); err != nil || pv.TemplateName != "default" {
		t.Errorf("legacy version template = %q, %v; want default", pv.TemplateName, err)
	}
}

func TestPolicyVersions_ListNewestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 12, 1, 2, 3, 4, time.UTC)
	var policies []conloomapi.GroupPolicy
	for i := 1; i <= 3; i++ {
		p := conloomapi.DefaultGroupPolicy()
		p.Context.MaxRecentMessages = 10 * i
		p.Reply.Debounce = conloomapi.Duration{Duration: time.Duration(i) * time.Second}
		policies = append(policies, p)
		pv, err := db.AppendPolicyVersion(ctx, store.PolicyVersion{
			ConversationKey: testConvKey, Policy: p, Reason: "tune", UpdatedBy: "29999", CreatedAt: created,
		})
		if err != nil {
			t.Fatalf("AppendPolicyVersion %d: %v", i, err)
		}
		if pv.Version != i {
			t.Fatalf("appended version = %d, want %d", pv.Version, i)
		}
	}

	list, err := db.ListPolicyVersions(ctx, testConvKey)
	if err != nil {
		t.Fatalf("ListPolicyVersions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("versions = %d, want 3", len(list))
	}
	for i, pv := range list {
		want := 3 - i
		if pv.Version != want || pv.ConversationKey != testConvKey || pv.Reason != "tune" || pv.UpdatedBy != "29999" ||
			!pv.CreatedAt.Equal(created) || !reflect.DeepEqual(pv.Policy, policies[want-1]) {
			t.Fatalf("list[%d] = %+v, want version %d", i, pv, want)
		}
	}
	latest, err := db.LatestPolicy(ctx, testConvKey)
	if err != nil || latest.Version != 3 || !reflect.DeepEqual(latest.Policy, policies[2]) {
		t.Fatalf("LatestPolicy = %+v, %v; want version 3", latest, err)
	}

	if _, err := db.LatestPolicy(ctx, testConvKeyB); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing policy err = %v, want ErrNotFound", err)
	}
	if list, err := db.ListPolicyVersions(ctx, testConvKeyB); err != nil || len(list) != 0 {
		t.Fatalf("other conversation list = %v, %v; want empty", list, err)
	}
}

func TestInsertAudit(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	err := db.InsertAudit(ctx, store.AuditEvent{
		ID: "aud_1", BotInstanceID: testBot, Kind: "policy.update", Actor: "29999", Target: testConvKey,
		Detail: `{"version":2}`, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}
	var bot, kind, actor, target, detail, created string
	err = db.r.QueryRow(`SELECT bot_instance_id, kind, actor, target, detail, created_at FROM audit_events WHERE id = ?`, "aud_1").
		Scan(&bot, &kind, &actor, &target, &detail, &created)
	if err != nil || bot != testBot || kind != "policy.update" || actor != "29999" || target != testConvKey ||
		detail != `{"version":2}` || created != fmtTime(at) {
		t.Fatalf("audit row = %q %q %q %q %q %q, %v", bot, kind, actor, target, detail, created, err)
	}
	if err := db.InsertAudit(ctx, store.AuditEvent{ID: "aud_1", BotInstanceID: testBot}); err == nil {
		t.Fatal("duplicate audit id inserted")
	}
}
