package persona

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/imaging"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ExchangeFormat and ExchangeVersion identify a persona template exchange
// file (see ExchangeFile).
const (
	ExchangeFormat  = "conloom.persona-template"
	ExchangeVersion = 1
)

// ExchangeFile is a file of exported persona templates, which Import reads.
// One template and the whole catalog are exported alike.
type ExchangeFile struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	ExportedAt time.Time          `json:"exported_at"`
	Templates  []ExchangeTemplate `json:"templates"`
}

// ExchangeTemplate is a persona template of an ExchangeFile. Persona keeps
// the ID of its appearance image; AppearanceImage, when set, holds the image
// itself.
type ExchangeTemplate struct {
	Name            string                `json:"name"`
	Persona         conloomapi.PersonaDoc `json:"persona"`
	AppearanceImage *ExchangeImage        `json:"appearance_image,omitempty"`
}

// ExchangeImage is an appearance image as it was uploaded: Data holds its
// bytes in standard base64, MIME their type.
type ExchangeImage struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// ConflictMode is what Import does with a template whose name the catalog
// already has.
type ConflictMode string

const (
	// ConflictFail refuses the import. The zero ConflictMode is ConflictFail.
	ConflictFail ConflictMode = "fail"
	// ConflictOverwrite replaces the template of that name.
	ConflictOverwrite ConflictMode = "overwrite"
	// ConflictRename imports the template under the first free name of
	// name-2, name-3, …, the name cut so that the result has at most 32
	// characters.
	ConflictRename ConflictMode = "rename"
)

// ImportResult is a template Import stored: Requested is its name in the
// file, Name the name it was stored under, and Created whether the catalog
// had no template of that name.
type ImportResult struct {
	Name, Requested string
	Created         bool
}

// ErrImportConflict refuses a ConflictFail import of templates whose names
// the catalog already has. Import returns it as an *ImportConflictError
// naming them.
var ErrImportConflict = errors.New("persona: template names already exist")

// ImportConflictError is ErrImportConflict for the templates Names, in the
// order of the file.
type ImportConflictError struct {
	Names []string
}

func (e *ImportConflictError) Error() string {
	return ErrImportConflict.Error() + ": " + strings.Join(e.Names, ", ")
}

func (e *ImportConflictError) Unwrap() error { return ErrImportConflict }

// Export returns the catalog's templates names as an exchange file, in that
// order and each once; without names, all of them by name, the built-in
// "default" included while none is stored. An appearance image is embedded
// as it was uploaded. A template naming an image that does not exist, as one
// migrated from an old config file may, is exported without it, its ID
// cleared, and a warning logged. An unknown name is store.ErrNotFound.
func (s *Service) Export(ctx context.Context, names []string) (ExchangeFile, error) {
	c := s.catalog.Load()
	if len(names) == 0 {
		names = make([]string, 0, len(c.infos))
		for _, info := range c.infos {
			names = append(names, info.Name)
		}
	}
	f := ExchangeFile{Format: ExchangeFormat, Version: ExchangeVersion, ExportedAt: time.Now().UTC().Truncate(time.Second)}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		doc, ok := c.docs[name]
		if !ok {
			return ExchangeFile{}, fmt.Errorf("%w: persona template %q", store.ErrNotFound, name)
		}
		t, err := s.exportTemplate(ctx, name, doc)
		if err != nil {
			return ExchangeFile{}, err
		}
		f.Templates = append(f.Templates, t)
	}
	return f, nil
}

// exportTemplate returns the template name with persona doc as Export
// exports it.
func (s *Service) exportTemplate(ctx context.Context, name string, doc conloomapi.PersonaDoc) (ExchangeTemplate, error) {
	t := ExchangeTemplate{Name: name, Persona: doc}
	if doc.AppearanceImage == "" {
		return t, nil
	}
	img, err := s.st.GetPersonaImage(ctx, doc.AppearanceImage, true)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.log.Warn("persona template's appearance image does not exist; exporting the template without it",
			"template", name, "image", doc.AppearanceImage)
		t.Persona.AppearanceImage = ""
	case err != nil:
		return ExchangeTemplate{}, fmt.Errorf("persona: export template %q: appearance image: %w", name, err)
	default:
		t.AppearanceImage = &ExchangeImage{MIME: img.MIME, Data: base64.StdEncoding.EncodeToString(img.Original)}
	}
	return t, nil
}

// Import stores the templates of f for actor, all of them or none, and
// returns them in the order of f. mode decides what happens to a template
// whose name the catalog already has, the built-in "default" included; in
// ConflictFail mode such templates refuse the import with an
// *ImportConflictError naming them.
//
// The whole file is checked first. A wrong format or version, a file without
// templates and an unknown mode are ErrInvalid. Problems of templates are
// ErrInvalidTemplate, all reported together: a name that breaks the rule for
// new templates (see PutTemplate) or that the file used before, an embedded
// image that is not standard base64 or that imaging.Process refuses (its
// declared MIME is not used), a persona that fails Validate, and one naming
// an image neither embedded in the file nor uploaded. An embedded image is
// stored as uploaded, and its ID, the sha256 of its bytes, replaces the
// persona's appearance_image.
//
// The images and templates are stored in one transaction and published
// together; then the function set with SetOnImport is called.
func (s *Service) Import(ctx context.Context, f ExchangeFile, mode ConflictMode, actor string) ([]ImportResult, error) {
	if mode == "" {
		mode = ConflictFail
	}
	if !slices.Contains([]ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename}, mode) {
		return nil, fmt.Errorf("%w: on_conflict must be fail, overwrite or rename, got %.40q", ErrInvalid, string(mode))
	}
	switch {
	case f.Format != ExchangeFormat:
		return nil, fmt.Errorf("%w: format must be %q, got %.40q", ErrInvalid, ExchangeFormat, f.Format)
	case f.Version != ExchangeVersion:
		return nil, fmt.Errorf("%w: version must be %d, got %d", ErrInvalid, ExchangeVersion, f.Version)
	case len(f.Templates) == 0:
		return nil, fmt.Errorf("%w: the file has no templates", ErrInvalid)
	}
	templates, images, err := s.checkImport(ctx, f.Templates)
	if err != nil {
		return nil, err
	}
	results, err := s.storeImport(ctx, templates, images, mode, actor)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	s.log.Info("persona templates imported", "templates", names, "on_conflict", string(mode), "actor", actor)
	if s.onImport != nil {
		if err := s.onImport(context.WithoutCancel(ctx)); err != nil {
			s.log.Warn("persona templates imported but bots not refreshed", "error", err)
		}
	}
	return results, nil
}

// importTemplate is a checked template of an import: its name in the file
// and its persona, whose appearance_image names its embedded image.
type importTemplate struct {
	name string
	doc  conloomapi.PersonaDoc
}

// checkImport checks the templates of an import (see Import) and returns
// them with their embedded images, each image once.
func (s *Service) checkImport(ctx context.Context, templates []ExchangeTemplate) ([]importTemplate, []imaging.Processed, error) {
	imageIDs := make([]string, len(templates))
	imageErrs := make([]error, len(templates))
	var images []imaging.Processed
	embedded := map[string]bool{}
	for i, t := range templates {
		if t.AppearanceImage == nil {
			continue
		}
		p, err := processImage(t.AppearanceImage.Data)
		if err != nil {
			imageErrs[i] = err
			continue
		}
		imageIDs[i] = p.ID
		if !embedded[p.ID] {
			embedded[p.ID] = true
			images = append(images, p)
		}
	}

	var problems []error
	first := map[string]int{}
	out := make([]importTemplate, len(templates))
	for i, t := range templates {
		var errs []error
		if !validTemplateName(t.Name) {
			errs = append(errs, fmt.Errorf("name must be 1 to %d lowercase letters, digits, - or _", maxTemplateName))
		} else if j, ok := first[t.Name]; ok {
			errs = append(errs, fmt.Errorf("name already used by templates[%d]", j))
		} else {
			first[t.Name] = i
		}
		doc := t.Persona
		if t.AppearanceImage != nil {
			doc.AppearanceImage = imageIDs[i]
			if imageErrs[i] != nil {
				errs = append(errs, fmt.Errorf("appearance_image: %w", imageErrs[i]))
			}
		}
		if err := errors.Join(doc.Validate(), conloomapi.ValidateContent(doc)); err != nil {
			errs = append(errs, err)
		} else if id := doc.AppearanceImage; id != "" && !embedded[id] {
			ok, err := s.imageUploaded(ctx, id)
			if err != nil {
				return nil, nil, fmt.Errorf("persona: check appearance image: %w", err)
			}
			if !ok {
				errs = append(errs, fmt.Errorf("unknown appearance_image %q", id))
			}
		}
		if len(errs) > 0 {
			problems = append(problems, fmt.Errorf("templates[%d] %.40q: %w", i, t.Name, errors.Join(errs...)))
		}
		out[i] = importTemplate{name: t.Name, doc: doc}
	}
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidTemplate, errors.Join(problems...))
	}
	return out, images, nil
}

// processImage checks data, an image in standard base64, with
// imaging.Process.
func processImage(data string) (imaging.Processed, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return imaging.Processed{}, fmt.Errorf("data is not standard base64: %w", err)
	}
	return imaging.Process(raw)
}

// storeImport names the checked templates by mode, stores them with images
// for actor and publishes them.
func (s *Service) storeImport(ctx context.Context, templates []importTemplate, images []imaging.Processed,
	mode ConflictMode, actor string) ([]ImportResult, error) {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	catalog := s.catalog.Load().docs
	reserved := make(map[string]bool, len(templates))
	for _, t := range templates {
		reserved[t.name] = true
	}
	taken := func(name string) bool {
		_, ok := catalog[name]
		return ok || reserved[name]
	}
	now := time.Now().UTC()
	var conflicts []string
	results := make([]ImportResult, len(templates))
	rows := make([]store.PersonaTemplateRecord, len(templates))
	for i, t := range templates {
		_, exists := catalog[t.name]
		r := ImportResult{Name: t.name, Requested: t.name, Created: !exists}
		if exists {
			switch mode {
			case ConflictFail:
				conflicts = append(conflicts, t.name)
			case ConflictRename:
				r.Name, r.Created = renamed(t.name, taken), true
				reserved[r.Name] = true
			}
		}
		results[i] = r
		rows[i] = store.PersonaTemplateRecord{Name: r.Name, Persona: t.doc, UpdatedBy: actor, UpdatedAt: now}
	}
	if len(conflicts) > 0 {
		return nil, &ImportConflictError{Names: conflicts}
	}
	records := make([]store.PersonaImage, len(images))
	for i, p := range images {
		records[i] = store.PersonaImage{
			ID: p.ID, MIME: p.MIME, Width: p.Width, Height: p.Height, Original: p.Original, ReplyJPEG: p.ReplyJPEG,
			ReplyWidth: p.ReplyWidth, ReplyHeight: p.ReplyHeight, ReplyTokens: p.ReplyTokens, CreatedBy: actor, CreatedAt: now,
		}
	}
	if err := s.st.ImportPersonaTemplates(ctx, records, rows); err != nil {
		return nil, fmt.Errorf("persona: import templates: %w", err)
	}
	stored := maps.Clone(s.stored)
	for _, r := range rows {
		stored[r.Name] = r
	}
	s.publish(stored)
	return results, nil
}

// renamed returns the first of name-2, name-3, … that is not taken, name cut
// so that the result has at most maxTemplateName characters.
func renamed(name string, taken func(string) bool) string {
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		candidate := name[:min(len(name), maxTemplateName-len(suffix))] + suffix
		if !taken(candidate) {
			return candidate
		}
	}
}
