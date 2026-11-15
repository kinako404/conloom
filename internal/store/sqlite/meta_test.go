package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestGetMeta_Missing(t *testing.T) {
	db := newTestDB(t)
	if v, ok, err := db.GetMeta(context.Background(), store.MetaConfigTemplatesMigrated); err != nil || ok || v != "" {
		t.Fatalf("GetMeta of a new database = %q, %v, %v; want nothing", v, ok, err)
	}
}

func TestMigrateConfigTemplates_StoresMissingKeepsStoredOnce(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	stored := conloomapi.DefaultPersona()
	stored.PersonaName = "在线灯塔"
	online := store.PersonaTemplateRecord{Name: "lighthouse", Persona: stored, UpdatedBy: "panel:admin", CreatedAt: at, UpdatedAt: at}
	putTemplate(t, db, online)

	legacy := func(name string) conloomapi.PersonaDoc {
		p := conloomapi.DefaultPersona()
		p.PersonaName = "配置" + name
		return p
	}
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	rows := []store.PersonaTemplateRecord{
		{Name: "default", Persona: legacy("default"), UpdatedBy: "system"},
		{Name: "lighthouse", Persona: legacy("lighthouse"), UpdatedBy: "system"},
		{Name: "Classic One", Persona: legacy("Classic One"), UpdatedBy: "system"},
	}
	imported, skipped, err := db.MigrateConfigTemplates(ctx, rows, now)
	if err != nil {
		t.Fatalf("MigrateConfigTemplates: %v", err)
	}
	if !slices.Equal(imported, []string{"default", "Classic One"}) || !slices.Equal(skipped, []string{"lighthouse"}) {
		t.Fatalf("imported %v, skipped %v; want default and Classic One imported, lighthouse skipped", imported, skipped)
	}
	checkTemplates(t, listTemplates(t, db), []store.PersonaTemplateRecord{
		{Name: "Classic One", Persona: legacy("Classic One"), UpdatedBy: "system", CreatedAt: now, UpdatedAt: now},
		{Name: "default", Persona: legacy("default"), UpdatedBy: "system", CreatedAt: now, UpdatedAt: now},
		online,
	})
	if v, ok, err := db.GetMeta(ctx, store.MetaConfigTemplatesMigrated); err != nil || !ok || v != fmtTime(now) {
		t.Fatalf("GetMeta = %q, %v, %v; want the migration time %q", v, ok, err, fmtTime(now))
	}

	again := []store.PersonaTemplateRecord{{Name: "cat", Persona: legacy("cat"), UpdatedBy: "system"}}
	imported, skipped, err = db.MigrateConfigTemplates(ctx, again, now.Add(time.Hour))
	if !errors.Is(err, store.ErrAlreadyMigrated) || imported != nil || skipped != nil {
		t.Fatalf("second MigrateConfigTemplates = %v, %v, %v; want ErrAlreadyMigrated", imported, skipped, err)
	}
	if got := listTemplates(t, db); len(got) != 3 {
		t.Fatalf("templates after a refused migration = %+v, want the three from before", got)
	}
	if v, _, _ := db.GetMeta(ctx, store.MetaConfigTemplatesMigrated); v != fmtTime(now) {
		t.Fatalf("migration time after a refused migration = %q, want %q", v, fmtTime(now))
	}
}

func TestMigrateConfigTemplates_NoRowsStillRecords(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if imported, skipped, err := db.MigrateConfigTemplates(ctx, nil, now); err != nil || imported != nil || skipped != nil {
		t.Fatalf("MigrateConfigTemplates(nil) = %v, %v, %v", imported, skipped, err)
	}
	if _, ok, err := db.GetMeta(ctx, store.MetaConfigTemplatesMigrated); err != nil || !ok {
		t.Fatalf("GetMeta = %v, %v; want the migration recorded", ok, err)
	}
}

func TestMigration0015_OnlyAddsTables(t *testing.T) {
	_, added, after := migrateOnlyAdding(t, 15)
	if want := []string{"app_meta", "sqlite_autoindex_app_meta_1"}; !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	if want := []string{"0|key|TEXT|0||1", "1|value|TEXT|1||0", "2|updated_at|TEXT|1||0"}; !slices.Equal(after["app_meta"], want) {
		t.Errorf("app_meta columns %v, want %v", after["app_meta"], want)
	}
}
