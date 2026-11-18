package persona

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/store/sqlite"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// testPNG is a w×h PNG whose pixels depend on seed.
func testPNG(t *testing.T, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), seed, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bombPNG is a PNG whose header claims a w×h grayscale image and that has no
// pixel data.
func bombPNG(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		buf.Write(n[:])
		buf.WriteString(typ)
		buf.Write(data)
		binary.BigEndian.PutUint32(n[:], crc32.ChecksumIEEE(append([]byte(typ), data...)))
		buf.Write(n[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return buf.Bytes()
}

// imageID is the ID imaging.Process gives data.
func imageID(t *testing.T, data []byte) string {
	t.Helper()
	p, err := imaging.Process(data)
	if err != nil {
		t.Fatalf("imaging.Process: %v", err)
	}
	return p.ID
}

// exchangeFile is an exchange file of templates.
func exchangeFile(templates ...ExchangeTemplate) ExchangeFile {
	return ExchangeFile{Format: ExchangeFormat, Version: ExchangeVersion, ExportedAt: time.Now().UTC(), Templates: templates}
}

// exchangeTemplate is the template name of an exchange file with doc and,
// unless img is nil, img as its appearance image.
func exchangeTemplate(name string, doc conloomapi.PersonaDoc, img []byte) ExchangeTemplate {
	t := ExchangeTemplate{Name: name, Persona: doc}
	if img != nil {
		t.AppearanceImage = &ExchangeImage{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(img)}
	}
	return t
}

// countImports makes svc count its successful imports.
func countImports(svc *Service) func() int {
	var n int
	svc.SetOnImport(func(context.Context) error {
		n++
		return nil
	})
	return func() int { return n }
}

// checkUnchanged fails unless st stores exactly the templates names and none
// of the images ids, and svc's catalog is names plus default.
func checkUnchanged(t *testing.T, st store.Store, svc *Service, names []string, ids ...string) {
	t.Helper()
	if got := storedTemplateNames(t, st); !slices.Equal(got, names) {
		t.Errorf("stored templates = %v, want %v", got, names)
	}
	catalog := slices.Sorted(slices.Values(append(slices.Clone(names), "default")))
	catalog = slices.Compact(catalog)
	if got := svc.TemplateNames(); !slices.Equal(got, catalog) {
		t.Errorf("catalog = %v, want %v", got, catalog)
	}
	for _, id := range ids {
		if ok, err := st.PersonaImageExists(context.Background(), id); err != nil || ok {
			t.Errorf("image %s stored = %v, %v; want not stored", id, ok, err)
		}
	}
}

func TestExchange_RoundTripSameImageID(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)
	svc := NewService(src)
	loadTemplates(t, svc)
	data := testPNG(t, 1000, 500, 1)
	p, err := imaging.Process(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.PutPersonaImage(ctx, store.PersonaImage{ID: p.ID, MIME: p.MIME, Width: p.Width, Height: p.Height,
		Original: p.Original, ReplyJPEG: p.ReplyJPEG, ReplyWidth: p.ReplyWidth, ReplyHeight: p.ReplyHeight,
		ReplyTokens: p.ReplyTokens, CreatedBy: "panel:admin"}); err != nil {
		t.Fatal(err)
	}
	lighthouse, cat := tagged("灯塔"), tagged("猫")
	lighthouse.AppearanceImage = p.ID
	for name, doc := range map[string]conloomapi.PersonaDoc{"lighthouse": lighthouse, "cat": cat} {
		if err := svc.PutTemplate(ctx, name, doc, "panel:admin"); err != nil {
			t.Fatalf("PutTemplate %s: %v", name, err)
		}
	}

	before := time.Now().Add(-time.Second)
	f, err := svc.Export(ctx, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if f.Format != "conloom.persona-template" || f.Version != 1 || f.ExportedAt.Before(before) || f.ExportedAt.After(time.Now()) ||
		f.ExportedAt.Location() != time.UTC {
		t.Errorf("exported file header = %q v%d at %v", f.Format, f.Version, f.ExportedAt)
	}
	want := ExchangeTemplates{
		{Name: "cat", Persona: cat},
		{Name: "default", Persona: conloomapi.DefaultPersona()},
		{Name: "lighthouse", Persona: lighthouse, AppearanceImage: &ExchangeImage{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data)}},
	}
	if !reflect.DeepEqual(f.Templates, want) {
		t.Fatalf("exported templates = %+v, want %+v", f.Templates, want)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("panel:admin")) || bytes.Contains(raw, []byte("updated")) {
		t.Errorf("exported file carries who stored the templates: %.300s", raw)
	}
	var back ExchangeFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}

	dst := newTestStore(t)
	other := NewService(dst)
	loadTemplates(t, other)
	results, err := other.Import(ctx, back, ConflictFail, "panel:other")
	if err != nil {
		t.Fatalf("Import of the whole export into a fresh store: %v; the built-in default is no conflict", err)
	}
	wantResults := []ImportResult{
		{Name: "cat", Requested: "cat", Created: true},
		{Name: "default", Requested: "default", Created: true},
		{Name: "lighthouse", Requested: "lighthouse", Created: true},
	}
	if !slices.Equal(results, wantResults) {
		t.Errorf("results = %+v, want %+v", results, wantResults)
	}
	for _, w := range want {
		got := info(t, other, w.Name)
		if got.Source != SourceDB || got.UpdatedBy != "panel:other" || !reflect.DeepEqual(got.Doc, w.Persona) {
			t.Errorf("imported %s = %+v, want the exported persona stored by panel:other", w.Name, got)
		}
	}
	img, err := dst.GetPersonaImage(ctx, p.ID, true)
	if err != nil {
		t.Fatalf("GetPersonaImage of the imported image: %v", err)
	}
	if !bytes.Equal(img.Original, data) || img.MIME != "image/png" || img.Width != 1000 || img.Height != 500 ||
		!bytes.Equal(img.ReplyJPEG, p.ReplyJPEG) || img.ReplyWidth != 768 || img.ReplyHeight != 384 ||
		img.ReplyTokens != p.ReplyTokens || img.CreatedBy != "panel:other" {
		t.Errorf("imported image = %s %d×%d reply %d×%d %d tokens by %s, original equal %v",
			img.MIME, img.Width, img.Height, img.ReplyWidth, img.ReplyHeight, img.ReplyTokens, img.CreatedBy, bytes.Equal(img.Original, data))
	}
	restarted := NewService(dst)
	loadTemplates(t, restarted)
	checkInfos(t, restarted.Templates(), other.Templates())
}

func TestExport_BuiltinDefault(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := NewService(st)
	loadTemplates(t, svc)
	builtin := ExchangeTemplates{{Name: "default", Persona: conloomapi.DefaultPersona()}}
	for _, names := range [][]string{nil, {}, {"default"}, {"default", "default"}} {
		f, err := svc.Export(ctx, names)
		if err != nil {
			t.Fatalf("Export(%q): %v", names, err)
		}
		if !reflect.DeepEqual(f.Templates, builtin) {
			t.Errorf("Export(%q) = %+v, want the built-in default once", names, f.Templates)
		}
	}
	if _, err := svc.Export(ctx, []string{"default", "nope"}); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("Export of an unknown template err = %v, want ErrNotFound naming it", err)
	}

	stored := tagged("默认")
	if err := svc.PutTemplate(ctx, "default", stored, "panel:admin"); err != nil {
		t.Fatal(err)
	}
	f, err := svc.Export(ctx, []string{"default"})
	if err != nil || !reflect.DeepEqual(f.Templates, ExchangeTemplates{{Name: "default", Persona: stored}}) {
		t.Errorf("Export of the stored default = %+v, %v; want the stored one", f.Templates, err)
	}
}

func TestExport_MissingImage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	gone := tagged("迁移")
	gone.AppearanceImage = fakeImageID("never uploaded")
	putTemplateRow(t, st, "migrated", gone, time.Now())
	svc := NewService(st)
	var logs bytes.Buffer
	svc.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	loadTemplates(t, svc)

	f, err := svc.Export(ctx, []string{"migrated"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	without := gone
	without.AppearanceImage = ""
	if !reflect.DeepEqual(f.Templates, ExchangeTemplates{{Name: "migrated", Persona: without}}) {
		t.Errorf("exported = %+v, want the persona without its missing image", f.Templates)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), gone.AppearanceImage) {
		t.Errorf("no warning about the missing image:\n%s", logs.String())
	}
	other := NewService(newTestStore(t))
	loadTemplates(t, other)
	if _, err := other.Import(ctx, f, ConflictFail, "panel:admin"); err != nil {
		t.Errorf("Import of the exported file: %v", err)
	}
}

func TestImport_AllOrNothing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, map[string]conloomapi.PersonaDoc{"lighthouse": tagged("灯塔")})
	imports := countImports(svc)
	img := testPNG(t, 16, 16, 2)
	warm := tagged("坏")
	warm.Style.Warmth = 2
	f := exchangeFile(
		exchangeTemplate("cat", tagged("猫"), img),
		exchangeTemplate("warm", warm, nil),
		exchangeTemplate("owl", tagged("夜猫"), nil),
		exchangeTemplate("Night Owl", tagged("夜猫"), nil),
	)
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		results, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || results != nil {
			t.Fatalf("Import (%s) = %+v, %v; want ErrInvalidTemplate", mode, results, err)
		}
		for _, part := range []string{`templates[1] "warm"`, "style.warmth", `templates[3] "Night Owl"`, "name must be"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("Import (%s) err = %v, want it to mention %s", mode, err, part)
			}
		}
		if strings.Contains(err.Error(), `"cat"`) || strings.Contains(err.Error(), `"owl"`) {
			t.Errorf("Import (%s) err = %v, names valid templates", mode, err)
		}
		checkUnchanged(t, st, svc, []string{"lighthouse"}, imageID(t, img))
	}
	if n := imports(); n != 0 {
		t.Errorf("OnImport called %d times by refused imports", n)
	}
}

func TestImport_RejectsBadImage(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	good := testPNG(t, 16, 16, 3)
	for _, c := range []struct {
		name string
		data string
		want error
	}{
		{"not base64", "!!!not-base64!!!", nil},
		{"base64 url encoding", base64.URLEncoding.EncodeToString(append(slices.Repeat([]byte{0xff}, 3), good...)), nil},
		{"gif", base64.StdEncoding.EncodeToString([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")), imaging.ErrUnsupported},
		{"text", base64.StdEncoding.EncodeToString([]byte("不是图片")), imaging.ErrUnsupported},
		{"empty", "", imaging.ErrUnsupported},
		{"broken png", base64.StdEncoding.EncodeToString(good[:len(good)/2]), imaging.ErrUnsupported},
		{"bomb over the edge limit", base64.StdEncoding.EncodeToString(bombPNG(20000, 20000)), imaging.ErrTooLarge},
		{"bomb over the pixel limit", base64.StdEncoding.EncodeToString(bombPNG(6000, 6000)), imaging.ErrTooLarge},
	} {
		f := exchangeFile(exchangeTemplate("cat", tagged("猫"), good),
			ExchangeTemplate{Name: "bad", Persona: tagged("坏"), AppearanceImage: &ExchangeImage{MIME: "image/png", Data: c.data}})
		_, err := svc.Import(ctx, f, ConflictFail, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), `templates[1] "bad"`) ||
			!strings.Contains(err.Error(), "appearance_image") {
			t.Errorf("%s: err = %v, want ErrInvalidTemplate about the appearance image of bad", c.name, err)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if len(c.data) > 8 && strings.Contains(err.Error(), c.data[:8]) {
			t.Errorf("%s: err = %v, quotes the image data", c.name, err)
		}
		checkUnchanged(t, st, svc, []string{}, imageID(t, good))
	}
}

// conflictSetup returns a service whose store has lighthouse, cat, cat-2, a
// template with the longest name allowed and the first rename of it.
func conflictSetup(t *testing.T) (*sqlite.DB, *Service) {
	t.Helper()
	st := newTestStore(t)
	svc := newServiceWith(t, st, map[string]conloomapi.PersonaDoc{
		"lighthouse": tagged("灯塔"), "cat": tagged("猫"), "cat-2": tagged("猫2"),
		longName: tagged("长"), longName[:30] + "-2": tagged("长2"),
	})
	return st, svc
}

const longName = "abcdefghijklmnopqrstuvwxyz012345"

func TestImport_ConflictModes(t *testing.T) {
	ctx := context.Background()
	stored := []string{"abcdefghijklmnopqrstuvwxyz0123-2", longName, "cat", "cat-2", "lighthouse"}
	f := exchangeFile(
		exchangeTemplate("lighthouse", tagged("新灯塔"), nil),
		exchangeTemplate("owl", tagged("夜猫"), nil),
		exchangeTemplate("cat", tagged("新猫"), nil),
		exchangeTemplate("default", tagged("新默认"), nil),
		exchangeTemplate(longName, tagged("新长"), nil),
		exchangeTemplate("lighthouse-2", tagged("灯塔2"), nil),
	)

	t.Run("fail", func(t *testing.T) {
		st, svc := conflictSetup(t)
		imports := countImports(svc)
		results, err := svc.Import(ctx, f, ConflictFail, "panel:admin")
		var conflict *ImportConflictError
		if !errors.Is(err, ErrImportConflict) || !errors.As(err, &conflict) || results != nil {
			t.Fatalf("Import = %+v, %v; want ErrImportConflict", results, err)
		}
		wantNames := []string{"lighthouse", "cat", longName}
		if !slices.Equal(conflict.Names, wantNames) {
			t.Errorf("conflicting names = %v, want %v: the built-in default is no conflict", conflict.Names, wantNames)
		}
		for _, name := range wantNames {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v, want it to name %s", err, name)
			}
		}
		if _, err := svc.Import(ctx, f, "", "panel:admin"); !errors.Is(err, ErrImportConflict) {
			t.Errorf("Import without a mode err = %v, want ErrImportConflict", err)
		}
		checkUnchanged(t, st, svc, stored)
		if doc, _ := svc.Template("lighthouse"); !reflect.DeepEqual(doc, tagged("灯塔")) {
			t.Errorf("lighthouse = %+v, want it unchanged", doc)
		}
		if n := imports(); n != 0 {
			t.Errorf("OnImport called %d times by a refused import", n)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		st, svc := conflictSetup(t)
		imports := countImports(svc)
		results, err := svc.Import(ctx, f, ConflictOverwrite, "panel:other")
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		want := []ImportResult{
			{Name: "lighthouse", Requested: "lighthouse"},
			{Name: "owl", Requested: "owl", Created: true},
			{Name: "cat", Requested: "cat"},
			{Name: "default", Requested: "default", Created: true},
			{Name: longName, Requested: longName},
			{Name: "lighthouse-2", Requested: "lighthouse-2", Created: true},
		}
		if !slices.Equal(results, want) {
			t.Errorf("results = %+v, want %+v", results, want)
		}
		for name, tag := range map[string]string{"lighthouse": "新灯塔", "owl": "夜猫", "cat": "新猫", "default": "新默认",
			longName: "新长", "lighthouse-2": "灯塔2", "cat-2": "猫2"} {
			if got := info(t, svc, name); got.Source != SourceDB || !reflect.DeepEqual(got.Doc, tagged(tag)) {
				t.Errorf("%s after the import = %+v, want tagged %s", name, got, tag)
			}
		}
		if got := storedTemplateNames(t, st); len(got) != len(stored)+3 {
			t.Errorf("stored templates = %v, want the old ones plus default, lighthouse-2 and owl", got)
		}
		if n := imports(); n != 1 {
			t.Errorf("OnImport called %d times, want 1", n)
		}
	})

	t.Run("rename", func(t *testing.T) {
		_, svc := conflictSetup(t)
		results, err := svc.Import(ctx, f, ConflictRename, "panel:other")
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		want := []ImportResult{
			{Name: "lighthouse-3", Requested: "lighthouse", Created: true},
			{Name: "owl", Requested: "owl", Created: true},
			{Name: "cat-3", Requested: "cat", Created: true},
			{Name: "default", Requested: "default", Created: true},
			{Name: "abcdefghijklmnopqrstuvwxyz0123-3", Requested: longName, Created: true},
			{Name: "lighthouse-2", Requested: "lighthouse-2", Created: true},
		}
		if !slices.Equal(results, want) {
			t.Errorf("results = %+v, want %+v", results, want)
		}
		for name, tag := range map[string]string{"lighthouse": "灯塔", "cat": "猫", "cat-2": "猫2", longName: "长",
			"lighthouse-3": "新灯塔", "cat-3": "新猫", "default": "新默认", "abcdefghijklmnopqrstuvwxyz0123-3": "新长", "lighthouse-2": "灯塔2"} {
			if got := info(t, svc, name); got.Source != SourceDB || !reflect.DeepEqual(got.Doc, tagged(tag)) {
				t.Errorf("%s after the import = %+v, want stored and tagged %s", name, got, tag)
			}
		}
		if _, ok := svc.Template("default-2"); ok {
			t.Error("default renamed although only the built-in one existed")
		}
	})

	t.Run("stored default", func(t *testing.T) {
		withDefault := exchangeFile(exchangeTemplate("default", tagged("新默认"), nil))
		for _, c := range []struct {
			mode ConflictMode
			want ImportResult
		}{
			{ConflictOverwrite, ImportResult{Name: "default", Requested: "default"}},
			{ConflictRename, ImportResult{Name: "default-2", Requested: "default", Created: true}},
		} {
			_, svc := conflictSetup(t)
			if err := svc.PutTemplate(ctx, "default", tagged("默认"), "panel:admin"); err != nil {
				t.Fatal(err)
			}
			var conflict *ImportConflictError
			if _, err := svc.Import(ctx, withDefault, ConflictFail, "panel:admin"); !errors.As(err, &conflict) ||
				!slices.Equal(conflict.Names, []string{"default"}) {
				t.Errorf("Import over a stored default err = %v, want a conflict naming default", err)
			}
			results, err := svc.Import(ctx, withDefault, c.mode, "panel:admin")
			if err != nil || !slices.Equal(results, []ImportResult{c.want}) {
				t.Errorf("Import (%s) over a stored default = %+v, %v; want %+v", c.mode, results, err, c.want)
			}
		}
	})

	t.Run("unknown mode", func(t *testing.T) {
		st, svc := conflictSetup(t)
		if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("owl", tagged("夜猫"), nil)), "merge", "panel:admin"); !errors.Is(err, ErrInvalid) {
			t.Errorf("Import with mode merge err = %v, want ErrInvalid", err)
		}
		checkUnchanged(t, st, svc, stored)
	})
}

func TestImport_RejectsWrongFormatOrVersion(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	good := exchangeFile(exchangeTemplate("owl", tagged("夜猫"), nil))
	for _, c := range []struct {
		name string
		edit func(*ExchangeFile)
		want string
	}{
		{"no format", func(f *ExchangeFile) { f.Format = "" }, "format"},
		{"other format", func(f *ExchangeFile) { f.Format = "conloom.persona" }, "format"},
		{"version 0", func(f *ExchangeFile) { f.Version = 0 }, "version"},
		{"version 2", func(f *ExchangeFile) { f.Version = 2 }, "version"},
		{"no templates", func(f *ExchangeFile) { f.Templates = nil }, "no templates"},
	} {
		f := good
		c.edit(&f)
		if _, err := svc.Import(ctx, f, ConflictOverwrite, "panel:admin"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid about %s", c.name, err, c.want)
		}
		checkUnchanged(t, st, svc, []string{})
	}
	if _, err := svc.Import(ctx, good, ConflictFail, "panel:admin"); err != nil {
		t.Errorf("Import of the good file: %v", err)
	}
}

// exchangeJSON is an exchange file whose templates are the JSON values
// entries.
func exchangeJSON(entries []string) []byte {
	return []byte(`{"format":"conloom.persona-template","version":1,"exported_at":"2026-11-15T04:00:00Z","templates":[` +
		strings.Join(entries, ",") + "]}")
}

// tinyEntries are n minimal templates t000, t001, … as JSON values.
func tinyEntries(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`{"name":"t%03d","persona":{}}`, i)
	}
	return out
}

func TestExchangeFile_DecodeStopsOverMaxTemplates(t *testing.T) {
	var f ExchangeFile
	if err := json.Unmarshal(exchangeJSON(tinyEntries(200)), &f); err != nil || len(f.Templates) != 200 ||
		f.Templates[199].Name != "t199" {
		t.Fatalf("decode of 200 templates = %d templates, %v; want all of them", len(f.Templates), err)
	}

	// Were the template over the limit decoded, its unknown field would be
	// the error, or the wrong type of the one after it.
	over := append(tinyEntries(200), `{"name":"t200","persona":{},"extra":1}`, `{"name":5}`)
	f = ExchangeFile{}
	err := json.Unmarshal(exchangeJSON(over), &f)
	if !errors.Is(err, ErrTooManyTemplates) || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "一个文件最多 200 个模板") {
		t.Errorf("decode of 202 templates err = %v, want ErrTooManyTemplates", err)
	}
	if len(f.Templates) != 0 {
		t.Errorf("decode of 202 templates kept %d of them", len(f.Templates))
	}

	for _, c := range []struct{ name, templates, want string }{
		{"unknown field", `[{"name":"a","persona":{},"extra":1}]`, "unknown field"},
		{"unknown persona field", `[{"name":"a","persona":{"extra":1}}]`, "unknown field"},
		{"wrong type", `[{"name":5}]`, "cannot unmarshal"},
		{"object", `{"name":"a"}`, "templates must be a JSON array"},
		{"string", `"a"`, "templates must be a JSON array"},
	} {
		var f ExchangeFile
		raw := `{"format":"conloom.persona-template","version":1,"templates":` + c.templates + `}`
		if err := json.Unmarshal([]byte(raw), &f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", c.name, err, c.want)
		}
	}
	f = ExchangeFile{Templates: ExchangeTemplates{{Name: "old"}}}
	if err := json.Unmarshal([]byte(`{"templates":null}`), &f); err != nil || f.Templates != nil {
		t.Errorf("decode of null templates = %+v, %v; want none", f.Templates, err)
	}
	if err := json.Unmarshal([]byte(`{"templates":[]}`), &f); err != nil || len(f.Templates) != 0 {
		t.Errorf("decode of no templates = %+v, %v; want none", f.Templates, err)
	}
}

func TestImport_TooManyTemplates(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	imports := countImports(svc)
	// Every template is wrong, but the file is refused for its size before
	// any of them is looked at.
	bad := ExchangeTemplate{Name: "Bad Name", Persona: tagged("坏"), AppearanceImage: &ExchangeImage{MIME: "image/png", Data: "!!!"}}
	f := exchangeFile(slices.Repeat([]ExchangeTemplate{bad}, MaxExchangeTemplates+1)...)
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		results, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrTooManyTemplates) || !errors.Is(err, ErrInvalid) || errors.Is(err, ErrInvalidTemplate) || results != nil {
			t.Fatalf("Import (%s) of 201 templates = %+v, %v; want ErrTooManyTemplates", mode, results, err)
		}
		if strings.Contains(err.Error(), "templates[") || len(err.Error()) > 100 {
			t.Errorf("Import (%s) of 201 templates err = %v, want no template checked", mode, err)
		}
	}
	checkUnchanged(t, st, svc, []string{})
	if n := imports(); n != 0 {
		t.Errorf("OnImport called %d times by refused imports", n)
	}
}

func TestImport_ProblemListCapped(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	for _, c := range []struct {
		n    int
		more string
	}{{20, ""}, {21, "等 21 处"}, {MaxExchangeTemplates, "等 200 处"}} {
		templates := make([]ExchangeTemplate, c.n)
		for i := range templates {
			templates[i] = exchangeTemplate(fmt.Sprintf("Bad%d", i), tagged("坏"), nil)
		}
		_, err := svc.Import(ctx, exchangeFile(templates...), ConflictFail, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Import of %d bad templates err = %v, want ErrInvalidTemplate", c.n, err)
		}
		msg := err.Error()
		if got := strings.Count(msg, "name must be"); got != 20 {
			t.Errorf("Import of %d bad templates lists %d problems, want the first 20: %s", c.n, got, msg)
		}
		if !strings.Contains(msg, `templates[19] "Bad19"`) || strings.Contains(msg, "templates[20]") {
			t.Errorf("Import of %d bad templates err = %s, want templates[0] to [19]", c.n, msg)
		}
		if c.more == "" && strings.Contains(msg, "等") || c.more != "" && !strings.HasSuffix(msg, "\n"+c.more) {
			t.Errorf("Import of %d bad templates err ends %q, want %q", c.n, msg[max(0, len(msg)-40):], c.more)
		}
	}
	checkUnchanged(t, st, svc, []string{})
}

func TestImport_DuplicateNamesInFile(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	f := exchangeFile(
		exchangeTemplate("cat", tagged("猫"), nil),
		exchangeTemplate("owl", tagged("夜猫"), nil),
		exchangeTemplate("cat", tagged("猫2"), nil),
	)
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), `templates[2] "cat"`) ||
			!strings.Contains(err.Error(), "templates[0]") {
			t.Errorf("Import (%s) err = %v, want ErrInvalidTemplate naming both cats", mode, err)
		}
		checkUnchanged(t, st, svc, []string{})
	}
}

func TestImport_ReservedNames(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	// A stored template of a reserved name does not make the name importable.
	svc := newServiceWith(t, st, map[string]conloomapi.PersonaDoc{"export": tagged("旧")})
	f := exchangeFile(
		exchangeTemplate("owl", tagged("夜猫"), nil),
		exchangeTemplate("export", tagged("甲"), nil),
		exchangeTemplate("import", tagged("乙"), nil),
	)
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), `templates[1] "export": 模板名不能是 export 或 import`) ||
			!strings.Contains(err.Error(), `templates[2] "import": 模板名不能是 export 或 import`) ||
			strings.Contains(err.Error(), "templates[0]") {
			t.Errorf("Import (%s) err = %v, want ErrInvalidTemplate naming export and import only", mode, err)
		}
		checkUnchanged(t, st, svc, []string{"export"})
	}
	if doc, _ := svc.Template("export"); !reflect.DeepEqual(doc, tagged("旧")) {
		t.Errorf("stored export = %+v, want it untouched", doc)
	}
}

func TestRenamed_SkipsReservedNames(t *testing.T) {
	free := func(string) bool { return false }
	if got := renamed("export", free); got != "export-2" {
		t.Errorf("renamed(export) = %q, want export-2", got)
	}
	// Were a reserved name of the form name-N, a rename would pass it by.
	defer func(old []string) { reservedTemplateNames = old }(reservedTemplateNames)
	reservedTemplateNames = append(slices.Clone(reservedTemplateNames), "owl-2")
	if got := renamed("owl", free); got != "owl-3" {
		t.Errorf("renamed(owl) with owl-2 reserved = %q, want owl-3", got)
	}
}

func TestImport_ProtectedModeRule(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	minorAdult := tagged("甲")
	minorAdult.Minor, minorAdult.Adult = true, true
	f := exchangeFile(exchangeTemplate("owl", tagged("夜猫"), nil), exchangeTemplate("both", minorAdult, nil))
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), "a minor persona cannot be adult") ||
			!strings.Contains(err.Error(), `"both"`) {
			t.Errorf("Import (%s) of a minor adult persona err = %v, want ErrInvalidTemplate", mode, err)
		}
		checkUnchanged(t, st, svc, []string{})
	}
	adult, minor := tagged("乙"), tagged("丙")
	adult.Adult, minor.Minor = true, true
	if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("grownup", adult, nil), exchangeTemplate("young", minor, nil)),
		ConflictFail, "panel:admin"); err != nil {
		t.Errorf("Import of an adult and a minor persona: %v", err)
	}
}

func TestImport_MinorIntimacy(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	young := tagged("甲")
	young.Minor = true
	young.Intimacy = &conloomapi.PersonaIntimacy{Mind: "想靠近对方"}
	raw, err := json.Marshal(exchangeFile(exchangeTemplate("owl", tagged("夜猫"), nil), exchangeTemplate("young", young, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"minor":true,"intimacy":{"mind":"想靠近对方"}`)) {
		t.Fatalf("exchange file %s does not carry the intimacy side", raw)
	}
	var f ExchangeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := svc.Import(ctx, f, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !errors.Is(err, ErrInvalid) ||
			!strings.Contains(err.Error(), "a minor persona cannot have an intimacy side") || !strings.Contains(err.Error(), `"young"`) {
			t.Errorf("Import (%s) of a minor persona with an intimacy side err = %v, want ErrInvalidTemplate", mode, err)
		}
		checkUnchanged(t, st, svc, []string{})
	}

	grownup, plain, levelsOnly := tagged("乙"), tagged("丙"), tagged("丁")
	grownup.Adult = true
	grownup.Intimacy = &conloomapi.PersonaIntimacy{Tone: "说话放软一些", Mind: "想靠近对方", Behavior: "会多陪对方聊一会儿",
		Levels: []string{conloomapi.IntimacyNone, conloomapi.IntimacyLight, conloomapi.IntimacyLight, conloomapi.IntimacyHalf, conloomapi.IntimacyMajor}}
	plain.Intimacy = &conloomapi.PersonaIntimacy{Tone: "说话放软一些"}
	levelsOnly.Minor = true
	levelsOnly.Intimacy = &conloomapi.PersonaIntimacy{Levels: conloomapi.DefaultIntimacyLevels()}
	if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("grownup", grownup, nil), exchangeTemplate("plain", plain, nil),
		exchangeTemplate("levels", levelsOnly, nil)), ConflictFail, "panel:admin"); err != nil {
		t.Fatalf("Import of an adult, a plain and a levels-only minor persona with intimacy sides: %v", err)
	}
	for name, want := range map[string]conloomapi.PersonaDoc{"grownup": grownup, "plain": plain, "levels": levelsOnly} {
		if got, _ := svc.Template(name); !reflect.DeepEqual(got, want) {
			t.Errorf("imported %s = %+v, want %+v", name, got, want)
		}
	}
}

func TestExchange_AdultAppearance(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWith(t, newTestStore(t), nil)
	grownup := tagged("乙")
	grownup.Description = "成年的工程师，做事认真。"
	grownup.Adult, grownup.AdultAppearance = true, "身材高挑，常穿深色风衣。"
	if err := svc.PutTemplate(ctx, "grownup", grownup, "panel:admin"); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	f, err := svc.Export(ctx, []string{"grownup"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"adult_appearance":"身材高挑，常穿深色风衣。"`)) {
		t.Fatalf("exchange file %s does not carry the adult appearance", raw)
	}
	var back ExchangeFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	st := newTestStore(t)
	other := newServiceWith(t, st, nil)
	if _, err := other.Import(ctx, back, ConflictFail, "panel:other"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got, _ := other.Template("grownup"); !reflect.DeepEqual(got, grownup) {
		t.Errorf("imported grownup = %+v, want %+v", got, grownup)
	}

	young := tagged("甲")
	young.Minor, young.AdultAppearance = true, grownup.AdultAppearance
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := other.Import(ctx, exchangeFile(exchangeTemplate("young", young, nil)), mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) ||
			!strings.Contains(err.Error(), "a minor persona cannot have an adult appearance") || !strings.Contains(err.Error(), `"young"`) {
			t.Errorf("Import (%s) of a minor persona with an adult appearance err = %v, want ErrInvalidTemplate", mode, err)
		}
		checkUnchanged(t, st, other, []string{"grownup"})
	}
}

func TestImport_AppearanceImageID(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	stored := putTestImage(t, st, "stored")
	shared, other := testPNG(t, 20, 10, 4), testPNG(t, 10, 20, 5)
	sharedID, otherID := imageID(t, shared), imageID(t, other)

	claimsOther := tagged("甲")
	claimsOther.AppearanceImage = fakeImageID("what the file says")
	keepsStored := tagged("乙")
	keepsStored.AppearanceImage = stored
	namesEmbedded := tagged("丙")
	namesEmbedded.AppearanceImage = otherID
	results, err := svc.Import(ctx, exchangeFile(
		exchangeTemplate("claims", claimsOther, shared),
		exchangeTemplate("bare", tagged("丁"), shared),
		exchangeTemplate("stored", keepsStored, nil),
		exchangeTemplate("refers", namesEmbedded, nil),
		exchangeTemplate("other", tagged("戊"), other),
	), ConflictFail, "panel:admin")
	if err != nil {
		t.Fatalf("Import: %v (results %+v)", err, results)
	}
	for name, id := range map[string]string{"claims": sharedID, "bare": sharedID, "stored": stored, "refers": otherID, "other": otherID} {
		if doc, _ := svc.Template(name); doc.AppearanceImage != id {
			t.Errorf("%s appearance_image = %q, want %q", name, doc.AppearanceImage, id)
		}
	}
	for _, id := range []string{sharedID, otherID} {
		if img, err := st.GetPersonaImage(ctx, id, true); err != nil || imageID(t, img.Original) != id {
			t.Errorf("image %s = %v; want stored with its bytes", id, err)
		}
	}

	unknown := tagged("己")
	unknown.AppearanceImage = fakeImageID("never uploaded")
	if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("unknown", unknown, nil)), ConflictFail, "panel:admin"); !errors.Is(err, ErrInvalidTemplate) ||
		!strings.Contains(err.Error(), "unknown appearance_image") {
		t.Errorf("Import of a persona naming an image neither embedded nor stored err = %v, want ErrInvalidTemplate", err)
	}
	if _, ok := svc.Template("unknown"); ok {
		t.Error("template unknown imported")
	}
}

func TestExchange_AvatarRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := newTestStore(t)
	svc := newServiceWith(t, src, nil)
	look, face := testPNG(t, 40, 20, 6), testPNG(t, 512, 512, 7)
	for _, data := range [][]byte{look, face} {
		p, err := imaging.Process(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := src.PutPersonaImage(ctx, store.PersonaImage{ID: p.ID, MIME: p.MIME, Width: p.Width, Height: p.Height,
			Original: p.Original, ReplyJPEG: p.ReplyJPEG, ReplyWidth: p.ReplyWidth, ReplyHeight: p.ReplyHeight,
			ReplyTokens: p.ReplyTokens, CreatedBy: "panel:admin"}); err != nil {
			t.Fatal(err)
		}
	}
	lighthouse, onlyAvatar := tagged("灯塔"), tagged("头像")
	lighthouse.AppearanceImage, lighthouse.AvatarImage = imageID(t, look), imageID(t, face)
	onlyAvatar.AvatarImage = imageID(t, face)
	for name, doc := range map[string]conloomapi.PersonaDoc{"lighthouse": lighthouse, "face": onlyAvatar} {
		if err := svc.PutTemplate(ctx, name, doc, "panel:admin"); err != nil {
			t.Fatalf("PutTemplate %s: %v", name, err)
		}
	}
	f, err := svc.Export(ctx, []string{"lighthouse", "face"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	embed := func(data []byte) *ExchangeImage {
		return &ExchangeImage{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data)}
	}
	want := ExchangeTemplates{
		{Name: "lighthouse", Persona: lighthouse, AppearanceImage: embed(look), AvatarImage: embed(face)},
		{Name: "face", Persona: onlyAvatar, AvatarImage: embed(face)},
	}
	if !reflect.DeepEqual(f.Templates, want) {
		t.Fatalf("exported templates = %+v, want %+v", f.Templates, want)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"avatar_image":{"mime":"image/png","data":"`)) {
		t.Errorf("exported file has no embedded avatar_image: %.300s", raw)
	}
	var back ExchangeFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.Templates[0].Persona.AvatarImage = fakeImageID("what the file says")
	back.Templates[1].Persona.AvatarImage = ""

	dst := newTestStore(t)
	other := newServiceWith(t, dst, nil)
	if _, err := other.Import(ctx, back, ConflictFail, "panel:other"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	for name, w := range map[string]conloomapi.PersonaDoc{"lighthouse": lighthouse, "face": onlyAvatar} {
		if got, _ := other.Template(name); !reflect.DeepEqual(got, w) {
			t.Errorf("imported %s = %+v, want %+v", name, got, w)
		}
	}
	img, err := dst.GetPersonaImage(ctx, imageID(t, face), true)
	if err != nil || !bytes.Equal(img.Original, face) || img.CreatedBy != "panel:other" {
		t.Errorf("imported avatar = %v by %q, original equal %v", err, img.CreatedBy, bytes.Equal(img.Original, face))
	}
	if got, want := other.ReferencedImages(), slices.Sorted(slices.Values([]string{imageID(t, look), imageID(t, face)})); !slices.Equal(got, want) {
		t.Errorf("ReferencedImages after import = %v, want %v", got, want)
	}
}

func TestExport_MissingAvatar(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	gone := tagged("迁移")
	gone.AvatarImage = fakeImageID("avatar never uploaded")
	putTemplateRow(t, st, "migrated", gone, time.Now())
	svc := NewService(st)
	var logs bytes.Buffer
	svc.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	loadTemplates(t, svc)

	f, err := svc.Export(ctx, []string{"migrated"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	without := gone
	without.AvatarImage = ""
	if !reflect.DeepEqual(f.Templates, ExchangeTemplates{{Name: "migrated", Persona: without}}) {
		t.Errorf("exported = %+v, want the persona without its missing avatar", f.Templates)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "avatar image") ||
		!strings.Contains(logs.String(), gone.AvatarImage) {
		t.Errorf("no warning about the missing avatar:\n%s", logs.String())
	}
}

func TestImport_BadOrUnknownAvatar(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	svc := newServiceWith(t, st, nil)
	good := testPNG(t, 16, 16, 8)
	bad := ExchangeTemplate{Name: "bad", Persona: tagged("坏"), AvatarImage: &ExchangeImage{MIME: "image/png", Data: "!!!"}}
	_, err := svc.Import(ctx, exchangeFile(exchangeTemplate("cat", tagged("猫"), good), bad), ConflictFail, "panel:admin")
	if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), `templates[1] "bad"`) ||
		!strings.Contains(err.Error(), "avatar_image") {
		t.Errorf("Import with a broken avatar err = %v, want ErrInvalidTemplate about the avatar of bad", err)
	}
	checkUnchanged(t, st, svc, []string{}, imageID(t, good))

	unknown := tagged("己")
	unknown.AvatarImage = fakeImageID("never uploaded")
	if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("unknown", unknown, nil)), ConflictFail, "panel:admin"); !errors.Is(err, ErrInvalidTemplate) ||
		!strings.Contains(err.Error(), "unknown avatar_image") {
		t.Errorf("Import of a persona naming an avatar neither embedded nor stored err = %v, want ErrInvalidTemplate", err)
	}
	refers := tagged("庚")
	refers.AvatarImage = imageID(t, good)
	if _, err := svc.Import(ctx, exchangeFile(exchangeTemplate("refers", refers, nil), exchangeTemplate("cat", tagged("猫"), good)),
		ConflictFail, "panel:admin"); err != nil {
		t.Errorf("Import of an avatar embedded as another template's appearance image: %v", err)
	}
}

// importHook is a store whose ImportPersonaTemplates fails with err when set
// and otherwise stores, then calls after.
type importHook struct {
	store.Store
	err   error
	after func()
}

func (s importHook) ImportPersonaTemplates(ctx context.Context, images []store.PersonaImage, rows []store.PersonaTemplateRecord) error {
	if s.err != nil {
		return s.err
	}
	err := s.Store.ImportPersonaTemplates(ctx, images, rows)
	if s.after != nil {
		s.after()
	}
	return err
}

func TestImport_OnImport(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	f := exchangeFile(exchangeTemplate("owl", tagged("夜猫"), nil))

	broken := newServiceWith(t, importHook{Store: st, err: errors.New("disk full")}, nil)
	imports := countImports(broken)
	if results, err := broken.Import(ctx, f, ConflictFail, "panel:admin"); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Import with a failing store = %+v, %v; want its error", results, err)
	}
	checkUnchanged(t, st, broken, []string{})
	if n := imports(); n != 0 {
		t.Errorf("OnImport called %d times after a failed import", n)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	svc := newServiceWith(t, importHook{Store: st, after: cancel}, nil)
	var logs bytes.Buffer
	svc.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	var seen []string
	svc.SetOnImport(func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Errorf("OnImport context is done: %v", ctx.Err())
		}
		seen = svc.TemplateNames()
		return errors.New("bots not refreshed")
	})
	results, err := svc.Import(reqCtx, f, ConflictFail, "panel:admin")
	if err != nil || !slices.Equal(results, []ImportResult{{Name: "owl", Requested: "owl", Created: true}}) {
		t.Fatalf("Import = %+v, %v; want owl created despite the failing OnImport", results, err)
	}
	if !slices.Equal(seen, []string{"default", "owl"}) {
		t.Errorf("OnImport saw the catalog %v, want it to hold owl", seen)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "bots not refreshed") {
		t.Errorf("failing OnImport not logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "persona templates imported") || !strings.Contains(logs.String(), "owl") {
		t.Errorf("import not logged:\n%s", logs.String())
	}
}
