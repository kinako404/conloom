package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// fakeImage is an image record with fake bytes made from seed, its ID their
// hash.
func fakeImage(seed string, created time.Time) store.PersonaImage {
	original := []byte("original " + seed)
	sum := sha256.Sum256(original)
	return store.PersonaImage{
		ID: hex.EncodeToString(sum[:]), MIME: "image/png", Width: 1000, Height: 500,
		Original: original, ReplyJPEG: []byte("reply " + seed), ReplyWidth: 768, ReplyHeight: 384, ReplyTokens: 394,
		CreatedBy: "panel:admin", CreatedAt: created,
	}
}

func putImage(t *testing.T, db *DB, img store.PersonaImage) {
	t.Helper()
	if err := db.PutPersonaImage(context.Background(), img); err != nil {
		t.Fatalf("PutPersonaImage %s: %v", img.ID, err)
	}
}

func getImage(t *testing.T, db *DB, id string, original bool) store.PersonaImage {
	t.Helper()
	img, err := db.GetPersonaImage(context.Background(), id, original)
	if err != nil {
		t.Fatalf("GetPersonaImage %s: %v", id, err)
	}
	return img
}

func imageExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	ok, err := db.PersonaImageExists(context.Background(), id)
	if err != nil {
		t.Fatalf("PersonaImageExists %s: %v", id, err)
	}
	return ok
}

// checkImage compares got with want, CreatedAt as an instant.
func checkImage(t *testing.T, got, want store.PersonaImage) {
	t.Helper()
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt = want.CreatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("image = %+v, want %+v", got, want)
	}
}

func putTemplate(t *testing.T, db *DB, r store.PersonaTemplateRecord) {
	t.Helper()
	if err := db.PutPersonaTemplate(context.Background(), r); err != nil {
		t.Fatalf("PutPersonaTemplate %s: %v", r.Name, err)
	}
}

func listTemplates(t *testing.T, db *DB) []store.PersonaTemplateRecord {
	t.Helper()
	rs, err := db.ListPersonaTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListPersonaTemplates: %v", err)
	}
	return rs
}

// checkTemplates compares got with want, times as instants.
func checkTemplates(t *testing.T, got, want []store.PersonaTemplateRecord) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("templates = %+v, want %+v", got, want)
	}
	for i := range got {
		g, w := got[i], want[i]
		if !g.CreatedAt.Equal(w.CreatedAt) || !g.UpdatedAt.Equal(w.UpdatedAt) {
			t.Fatalf("template %s times = %v / %v, want %v / %v", g.Name, g.CreatedAt, g.UpdatedAt, w.CreatedAt, w.UpdatedAt)
		}
		g.CreatedAt, g.UpdatedAt = w.CreatedAt, w.UpdatedAt
		if !reflect.DeepEqual(g, w) {
			t.Fatalf("template = %+v, want %+v", g, w)
		}
	}
}

func personaWithImage(id string) conloomapi.PersonaDoc {
	p := conloomapi.DefaultPersona()
	p.AppearanceImage = id
	return p
}

func TestPersonaImages_PutIdempotentAndGet(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	created := time.Date(2026, 11, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	img := fakeImage("a", created)
	if imageExists(t, db, img.ID) {
		t.Fatal("image exists before it was put")
	}
	if _, err := db.GetPersonaImage(ctx, img.ID, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPersonaImage of a missing image err = %v, want ErrNotFound", err)
	}

	putImage(t, db, img)
	if !imageExists(t, db, img.ID) {
		t.Fatal("image missing after it was put")
	}
	checkImage(t, getImage(t, db, img.ID, true), img)
	withoutOriginal := img
	withoutOriginal.Original = nil
	checkImage(t, getImage(t, db, img.ID, false), withoutOriginal)

	again := img
	again.ReplyJPEG, again.CreatedBy, again.CreatedAt = []byte("other reply"), "panel:other", created.Add(time.Hour)
	putImage(t, db, again)
	checkImage(t, getImage(t, db, img.ID, true), img)
	if n := countRows(t, db, "persona_images"); n != 1 {
		t.Fatalf("persona_images rows after putting the same ID twice = %d, want 1", n)
	}

	bare := store.PersonaImage{ID: fakeImage("b", created).ID, MIME: "image/jpeg", Width: 1, Height: 1,
		ReplyWidth: 1, ReplyHeight: 1, ReplyTokens: 3, CreatedBy: "panel:admin"}
	before := time.Now()
	putImage(t, db, bare)
	got := getImage(t, db, bare.ID, true)
	if len(got.Original) != 0 || len(got.ReplyJPEG) != 0 {
		t.Fatalf("image put without bytes read back with %d / %d bytes", len(got.Original), len(got.ReplyJPEG))
	}
	if got.CreatedAt.Before(before) || got.CreatedAt.After(time.Now()) {
		t.Fatalf("CreatedAt of an image put without one = %v, want now", got.CreatedAt)
	}
	if n := countRows(t, db, "persona_images"); n != 2 {
		t.Fatalf("persona_images rows = %d, want 2", n)
	}
	if imageExists(t, db, fakeImage("c", created).ID) {
		t.Fatal("an image never put exists")
	}
}

func TestPersonaImages_PurgeKeepsReferenced(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	byTemplate := fakeImage("template", old)
	byOldVersion := fakeImage("old version", old)
	byLatestVersion := fakeImage("latest version", old)
	unreferenced := fakeImage("unreferenced", old)
	atCutoff := fakeImage("at cutoff", cutoff)
	young := fakeImage("young", now.Add(-time.Hour))
	for _, img := range []store.PersonaImage{byTemplate, byOldVersion, byLatestVersion, unreferenced, atCutoff, young} {
		putImage(t, db, img)
	}

	putTemplate(t, db, store.PersonaTemplateRecord{Name: "teacher", Persona: personaWithImage(byTemplate.ID), UpdatedBy: "panel:admin"})
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "plain", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin"})
	for _, pv := range []store.PersonaVersion{
		{ConversationKey: privKey("20001"), Persona: personaWithImage(byOldVersion.ID)},
		{ConversationKey: privKey("20001"), Persona: conloomapi.DefaultPersona()},
		{ConversationKey: privKey("29999"), Persona: personaWithImage(byLatestVersion.ID)},
	} {
		pv.TemplateName, pv.UpdatedBy = "default", "panel:admin"
		if _, err := db.AppendPersonaVersion(ctx, pv); err != nil {
			t.Fatalf("AppendPersonaVersion: %v", err)
		}
	}

	purge := func(want int) {
		t.Helper()
		if n, err := db.PurgePersonaImages(ctx, cutoff); err != nil || n != want {
			t.Fatalf("PurgePersonaImages = %d, %v; want %d", n, err, want)
		}
	}
	checkKept := func(want map[string]bool) {
		t.Helper()
		for name, img := range map[string]store.PersonaImage{
			"by template": byTemplate, "by old version": byOldVersion, "by latest version": byLatestVersion,
			"unreferenced": unreferenced, "at cutoff": atCutoff, "young": young,
		} {
			if got := imageExists(t, db, img.ID); got != want[name] {
				t.Errorf("image %s kept = %v, want %v", name, got, want[name])
			}
		}
	}
	purge(1)
	kept := map[string]bool{"by template": true, "by old version": true, "by latest version": true,
		"unreferenced": false, "at cutoff": true, "young": true}
	checkKept(kept)
	purge(0)

	if ok, err := db.DeletePersonaTemplate(ctx, "teacher"); err != nil || !ok {
		t.Fatalf("DeletePersonaTemplate = %v, %v; want true", ok, err)
	}
	purge(1)
	kept["by template"] = false
	checkKept(kept)
}

func TestPersonaTemplates_CRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if got := listTemplates(t, db); len(got) != 0 {
		t.Fatalf("templates of a new database = %+v, want none", got)
	}
	created := time.Date(2026, 11, 15, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	teacher := conloomapi.DefaultPersona()
	teacher.PersonaName, teacher.CharacterName = "老师", "小红"
	teacher.Appearance = "短发，常穿蓝色外套。"
	teacher.AppearanceImage = fakeImage("a", created).ID
	teacher.Examples = []conloomapi.PersonaExample{{User: "你好", Reply: "你好呀"}}
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "teacher", Persona: teacher, UpdatedBy: "panel:admin",
		CreatedAt: created, UpdatedAt: created})
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "default", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin",
		CreatedAt: created.Add(time.Minute), UpdatedAt: created.Add(time.Minute)})
	want := []store.PersonaTemplateRecord{
		{Name: "default", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin",
			CreatedAt: created.Add(time.Minute), UpdatedAt: created.Add(time.Minute)},
		{Name: "teacher", Persona: teacher, UpdatedBy: "panel:admin", CreatedAt: created, UpdatedAt: created},
	}
	checkTemplates(t, listTemplates(t, db), want)

	edited := teacher
	edited.Appearance, edited.AppearanceImage, edited.Examples = "长发，戴眼镜。", "", nil
	updated := created.Add(time.Hour)
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "teacher", Persona: edited, UpdatedBy: "panel:other",
		CreatedAt: updated, UpdatedAt: updated})
	want[1] = store.PersonaTemplateRecord{Name: "teacher", Persona: edited, UpdatedBy: "panel:other",
		CreatedAt: created, UpdatedAt: updated}
	checkTemplates(t, listTemplates(t, db), want)

	before := time.Now()
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "new_1", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin"})
	got := listTemplates(t, db)
	if len(got) != 3 || got[1].Name != "new_1" {
		t.Fatalf("templates = %+v, want new_1 between default and teacher", got)
	}
	if c, u := got[1].CreatedAt, got[1].UpdatedAt; !c.Equal(u) || c.Before(before) || c.After(time.Now()) {
		t.Fatalf("times of a template put without them = %v / %v, want both now", c, u)
	}
	checkTemplates(t, []store.PersonaTemplateRecord{got[0], got[2]}, want)

	for _, tc := range []struct {
		name string
		want bool
	}{{"teacher", true}, {"teacher", false}, {"missing", false}} {
		if ok, err := db.DeletePersonaTemplate(ctx, tc.name); err != nil || ok != tc.want {
			t.Fatalf("DeletePersonaTemplate %s = %v, %v; want %v", tc.name, ok, err, tc.want)
		}
	}
	var names []string
	for _, r := range listTemplates(t, db) {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"default", "new_1"}) {
		t.Fatalf("templates after deleting = %v, want [default new_1]", names)
	}
}

func TestMigration0014_OnlyAddsTables(t *testing.T) {
	_, added, after := migrateOnlyAdding(t, 14)
	want := []string{"persona_images", "persona_templates",
		"sqlite_autoindex_persona_images_1", "sqlite_autoindex_persona_templates_1"}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	for table, cols := range map[string][]string{
		"persona_images": {"0|id|TEXT|0||1", "1|mime|TEXT|1||0", "2|width|INTEGER|1||0", "3|height|INTEGER|1||0",
			"4|original|BLOB|1||0", "5|reply_jpeg|BLOB|1||0", "6|reply_width|INTEGER|1||0", "7|reply_height|INTEGER|1||0",
			"8|reply_tokens|INTEGER|1||0", "9|created_by|TEXT|1||0", "10|created_at|TEXT|1||0"},
		"persona_templates": {"0|name|TEXT|0||1", "1|persona|TEXT|1||0", "2|updated_by|TEXT|1||0",
			"3|created_at|TEXT|1||0", "4|updated_at|TEXT|1||0"},
	} {
		if !slices.Equal(after[table], cols) {
			t.Errorf("%s columns %v, want %v", table, after[table], cols)
		}
	}
}
