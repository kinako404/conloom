package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
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
	rs, unreadable, err := db.ListPersonaTemplates(context.Background())
	if err != nil || len(unreadable) != 0 {
		t.Fatalf("ListPersonaTemplates: unreadable %+v, %v", unreadable, err)
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

	again := store.PersonaImage{ID: img.ID, MIME: "image/jpeg", Width: 2, Height: 2, Original: []byte("other original"),
		ReplyJPEG: []byte("other reply"), ReplyWidth: 2, ReplyHeight: 2, ReplyTokens: 3,
		CreatedBy: "panel:other", CreatedAt: created.Add(time.Hour)}
	putImage(t, db, again)
	reuploaded := img
	reuploaded.CreatedBy, reuploaded.CreatedAt = again.CreatedBy, again.CreatedAt
	checkImage(t, getImage(t, db, img.ID, true), reuploaded)
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
	byKeep := fakeImage("kept by the caller", old)
	for _, img := range []store.PersonaImage{byTemplate, byOldVersion, byLatestVersion, unreferenced, atCutoff, young, byKeep} {
		putImage(t, db, img)
	}
	keep := []string{byKeep.ID, fakeImage("never put", old).ID}

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

	purge := func(keep []string, want int) {
		t.Helper()
		if n, err := db.PurgePersonaImages(ctx, cutoff, keep); err != nil || n != want {
			t.Fatalf("PurgePersonaImages keeping %v = %d, %v; want %d", keep, n, err, want)
		}
	}
	checkKept := func(want map[string]bool) {
		t.Helper()
		for name, img := range map[string]store.PersonaImage{
			"by template": byTemplate, "by old version": byOldVersion, "by latest version": byLatestVersion,
			"unreferenced": unreferenced, "at cutoff": atCutoff, "young": young, "by keep": byKeep,
		} {
			if got := imageExists(t, db, img.ID); got != want[name] {
				t.Errorf("image %s kept = %v, want %v", name, got, want[name])
			}
		}
	}
	purge(keep, 1)
	kept := map[string]bool{"by template": true, "by old version": true, "by latest version": true,
		"unreferenced": false, "at cutoff": true, "young": true, "by keep": true}
	checkKept(kept)
	purge(keep, 0)

	if ok, err := db.DeletePersonaTemplate(ctx, "teacher"); err != nil || !ok {
		t.Fatalf("DeletePersonaTemplate = %v, %v; want true", ok, err)
	}
	purge(keep, 1)
	kept["by template"] = false
	checkKept(kept)

	purge(nil, 1)
	kept["by keep"] = false
	checkKept(kept)
	purge([]string{}, 0)
}

func TestPersonaImages_PurgeSkipsMalformedPersonas(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	byVersion, byTemplate, unreferenced := fakeImage("version", old), fakeImage("template", old), fakeImage("unreferenced", old)
	for _, img := range []store.PersonaImage{byVersion, byTemplate, unreferenced} {
		putImage(t, db, img)
	}
	putTemplate(t, db, store.PersonaTemplateRecord{Name: "teacher", Persona: personaWithImage(byTemplate.ID), UpdatedBy: "panel:admin"})
	if _, err := db.w.ExecContext(ctx, `INSERT INTO persona_templates (name, persona, updated_by, created_at, updated_at)
		VALUES ('broken', '{"appearance_image":', 'panel:admin', ?, ?)`, fmtTime(now), fmtTime(now)); err != nil {
		t.Fatalf("insert malformed template: %v", err)
	}
	for _, pv := range []store.PersonaVersion{
		{ConversationKey: privKey("20001"), Persona: personaWithImage(byVersion.ID)},
		{ConversationKey: privKey("29999"), Persona: conloomapi.DefaultPersona()},
	} {
		pv.TemplateName, pv.UpdatedBy = "default", "panel:admin"
		if _, err := db.AppendPersonaVersion(ctx, pv); err != nil {
			t.Fatalf("AppendPersonaVersion: %v", err)
		}
	}
	if _, err := db.w.ExecContext(ctx, `UPDATE persona_versions SET persona = 'not json' WHERE conversation_key = ?`, privKey("29999")); err != nil {
		t.Fatalf("corrupt persona version: %v", err)
	}

	if n, err := db.PurgePersonaImages(ctx, cutoff, nil); err != nil || n != 1 {
		t.Fatalf("PurgePersonaImages with malformed rows = %d, %v; want 1", n, err)
	}
	for name, c := range map[string]struct {
		img  store.PersonaImage
		kept bool
	}{"by version": {byVersion, true}, "by template": {byTemplate, true}, "unreferenced": {unreferenced, false}} {
		if got := imageExists(t, db, c.img.ID); got != c.kept {
			t.Errorf("image %s kept = %v, want %v", name, got, c.kept)
		}
	}
}

func TestPersonaImages_ReuploadRestartsGrace(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	reuploaded, untouched := fakeImage("reuploaded", old), fakeImage("untouched", old)
	putImage(t, db, reuploaded)
	putImage(t, db, untouched)

	again := reuploaded
	again.ReplyJPEG, again.CreatedBy, again.CreatedAt = []byte("other reply"), "panel:other", now.Add(-time.Hour)
	putImage(t, db, again)
	if n, err := db.PurgePersonaImages(ctx, cutoff, nil); err != nil || n != 1 {
		t.Fatalf("PurgePersonaImages = %d, %v; want 1", n, err)
	}
	if imageExists(t, db, untouched.ID) {
		t.Fatal("old unreferenced image kept")
	}
	want := reuploaded
	want.CreatedBy, want.CreatedAt = again.CreatedBy, again.CreatedAt
	checkImage(t, getImage(t, db, reuploaded.ID, true), want)
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

func TestPersonaTemplates_ListReportsUnreadableRows(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	good := store.PersonaTemplateRecord{Name: "b_good", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin", CreatedAt: at, UpdatedAt: at}
	putTemplate(t, db, good)
	persona, err := json.Marshal(conloomapi.DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ name, persona, created, updated string }{
		{"a_json", `{"persona_name":`, fmtTime(at), fmtTime(at)},
		{"c_created", string(persona), "yesterday", fmtTime(at)},
		{"d_updated", string(persona), fmtTime(at), "now"},
	} {
		if _, err := db.w.ExecContext(ctx, `INSERT INTO persona_templates (name, persona, updated_by, created_at, updated_at)
			VALUES (?, ?, 'panel:admin', ?, ?)`, row.name, row.persona, row.created, row.updated); err != nil {
			t.Fatalf("insert %s: %v", row.name, err)
		}
	}
	got, unreadable, err := db.ListPersonaTemplates(ctx)
	if err != nil {
		t.Fatalf("ListPersonaTemplates: %v", err)
	}
	checkTemplates(t, got, []store.PersonaTemplateRecord{good})
	var names []string
	for _, u := range unreadable {
		if u.Err == nil {
			t.Errorf("unreadable row %s has no error", u.Name)
		}
		names = append(names, u.Name)
	}
	if !slices.Equal(names, []string{"a_json", "c_created", "d_updated"}) {
		t.Fatalf("unreadable rows = %+v, want a_json, c_created and d_updated", unreadable)
	}
}

func TestPersonaTemplates_ImportOneTransaction(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	at := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	kept := fakeImage("kept", at)
	putImage(t, db, kept)
	old := store.PersonaTemplateRecord{Name: "teacher", Persona: conloomapi.DefaultPersona(), UpdatedBy: "panel:admin",
		CreatedAt: at, UpdatedAt: at}
	putTemplate(t, db, old)

	later := at.Add(time.Hour)
	again := kept
	again.ReplyJPEG, again.CreatedBy, again.CreatedAt = []byte("other reply"), "panel:other", later
	added := fakeImage("added", later)
	added.CreatedBy = "panel:other"
	teacher := store.PersonaTemplateRecord{Name: "teacher", Persona: personaWithImage(kept.ID), UpdatedBy: "panel:other", UpdatedAt: later}
	student := store.PersonaTemplateRecord{Name: "student", Persona: personaWithImage(added.ID), UpdatedBy: "panel:other", UpdatedAt: later}
	broken := student
	broken.Name = "broken"
	broken.Persona.Style.Warmth = math.NaN()

	err := db.ImportPersonaTemplates(ctx, []store.PersonaImage{again, added}, []store.PersonaTemplateRecord{teacher, broken})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("ImportPersonaTemplates with a row that cannot be stored err = %v, want one naming it", err)
	}
	checkImage(t, getImage(t, db, kept.ID, true), kept)
	if imageExists(t, db, added.ID) {
		t.Fatal("image of a failed import stored")
	}
	checkTemplates(t, listTemplates(t, db), []store.PersonaTemplateRecord{old})

	if err := db.ImportPersonaTemplates(ctx, []store.PersonaImage{again, added}, []store.PersonaTemplateRecord{teacher, student}); err != nil {
		t.Fatalf("ImportPersonaTemplates: %v", err)
	}
	reuploaded := kept
	reuploaded.CreatedBy, reuploaded.CreatedAt = again.CreatedBy, again.CreatedAt
	checkImage(t, getImage(t, db, kept.ID, true), reuploaded)
	checkImage(t, getImage(t, db, added.ID, true), added)
	teacher.CreatedAt, student.CreatedAt = at, later
	checkTemplates(t, listTemplates(t, db), []store.PersonaTemplateRecord{student, teacher})
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
