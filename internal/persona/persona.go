// Package persona manages per-conversation persona versions and renders system prompts.
package persona

import (
	"cmp"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrInvalid wraps every persona validation failure.
var ErrInvalid = errors.New("persona: invalid")

// Validate checks p against conloomapi.PersonaDoc.Validate and
// conloomapi.ValidateContent.
func Validate(p conloomapi.PersonaDoc) error {
	if err := errors.Join(p.Validate(), conloomapi.ValidateContent(p)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// Errors of the template catalog. ErrInvalidTemplate wraps ErrInvalid.
var (
	ErrInvalidTemplate = fmt.Errorf("%w template", ErrInvalid)
	ErrDefaultTemplate = errors.New("persona: the default template cannot be deleted")
)

const (
	// defaultTemplate is the template the catalog always has: the stored one,
	// or the built-in conloomapi.DefaultPersona.
	defaultTemplate = "default"
	// maxTemplateName is the most characters of the name of a new template.
	maxTemplateName = 32
)

// TemplateSource is where a persona template of the catalog comes from.
type TemplateSource string

const (
	// SourceDB is a template stored in the database.
	SourceDB TemplateSource = "db"
	// SourceBuiltin is the built-in "default", in the catalog while the
	// database has none.
	SourceBuiltin TemplateSource = "builtin"
)

// TemplateInfo is a persona template of the catalog: Doc, where it comes from
// and, for SourceDB, who stored it last and when.
type TemplateInfo struct {
	Name      string
	Source    TemplateSource
	Doc       conloomapi.PersonaDoc
	UpdatedBy string
	UpdatedAt time.Time
}

// catalog is one version of the template catalog; it never changes once
// published.
type catalog struct {
	docs   map[string]conloomapi.PersonaDoc
	infos  []TemplateInfo // by name
	images []string       // see Service.ReferencedImages
}

// newCatalog makes the catalog of the stored templates stored, with the
// built-in "default" when stored has none.
func newCatalog(stored map[string]store.PersonaTemplateRecord) *catalog {
	c := &catalog{docs: make(map[string]conloomapi.PersonaDoc, len(stored)+1)}
	images := map[string]bool{}
	for name, r := range stored {
		c.docs[name] = r.Persona
		images[r.Persona.AppearanceImage] = true
	}
	if _, ok := stored[defaultTemplate]; !ok {
		c.docs[defaultTemplate] = conloomapi.DefaultPersona()
	}
	for _, name := range slices.Sorted(maps.Keys(c.docs)) {
		info := TemplateInfo{Name: name, Source: SourceBuiltin, Doc: c.docs[name]}
		if r, ok := stored[name]; ok {
			info.Source, info.UpdatedBy, info.UpdatedAt = SourceDB, r.UpdatedBy, r.UpdatedAt
		}
		c.infos = append(c.infos, info)
	}
	delete(images, "")
	c.images = slices.Sorted(maps.Keys(images))
	return c
}

// maxCachedPrompts bounds the system prompts a Service caches, one per
// conversation; the least recently used goes first.
const maxCachedPrompts = 1024

type cachedPrompt struct {
	convKey string
	version int
	botName string
	mode    conloomapi.ContentMode
	prompt  Prompt
}

// Service keeps the conversations' persona versions and the persona template
// catalog: the templates stored in the database and, while none of them is
// "default", the built-in default persona as "default". The catalog is
// replaced as a whole on every change, so a reader always sees one whole
// version of it.
type Service struct {
	st      store.Store
	catalog atomic.Pointer[catalog]

	// templateMu serialises LoadTemplates and the template writes; stored,
	// which it guards, holds the stored templates of the published catalog.
	templateMu sync.Mutex
	stored     map[string]store.PersonaTemplateRecord

	imageExists   func(ctx context.Context, id string) (bool, error)
	templateUsers func(name string) []string
	onImport      func(ctx context.Context) error
	log           *slog.Logger

	// writeMu serialises version creation so concurrent callers never append duplicates.
	writeMu sync.Mutex

	// promptMu guards prompts, the cached prompts by conversation, and
	// promptOrder, their elements from most to least recently used; at most
	// promptLimit are cached.
	promptMu    sync.Mutex
	prompts     map[string]*list.Element
	promptOrder *list.List
	promptLimit int
}

// NewService returns a Service whose catalog holds only the built-in
// "default" until LoadTemplates loads the stored templates.
func NewService(st store.Store) *Service {
	s := &Service{st: st, stored: map[string]store.PersonaTemplateRecord{}, log: slog.New(slog.DiscardHandler),
		prompts: map[string]*list.Element{}, promptOrder: list.New(), promptLimit: maxCachedPrompts}
	s.catalog.Store(newCatalog(s.stored))
	return s
}

// SetImageChecker replaces how persona writes check that an appearance image
// was uploaded, by default the store's PersonaImageExists. Call it before the
// Service is shared.
func (s *Service) SetImageChecker(f func(ctx context.Context, id string) (bool, error)) {
	s.imageExists = f
}

// SetTemplateUsers sets how DeleteTemplate finds the bots a deleted template
// leaves without their persona template: f returns the IDs of the bots,
// config and panel bots alike, whose persona template is name. Without it
// DeleteTemplate reports none. Call it before the Service is shared.
func (s *Service) SetTemplateUsers(f func(name string) []string) {
	s.templateUsers = f
}

// SetOnImport sets f, which Import calls after it stored and published
// templates, with a context not cancelled with Import's; the orchestrator
// publishes the bots again there, so that a bot whose template was missing
// loses its problem. An error of f is only logged. Call it before Import is
// first called.
func (s *Service) SetOnImport(f func(ctx context.Context) error) {
	s.onImport = f
}

// SetLogger sets the logger LoadTemplates, Export and Import report to; by
// default it logs nothing. Call it before the Service is shared.
func (s *Service) SetLogger(l *slog.Logger) {
	s.log = l
}

// LoadTemplates replaces the catalog's stored templates with the templates
// stored in the database. A row that cannot be read or whose persona fails
// Validate is logged as a warning and left out of the catalog; the stored
// row is left as it is. A row's name and image are not checked: a template
// migrated from an old config file keeps the name it had there.
func (s *Service) LoadTemplates(ctx context.Context) error {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	rows, unreadable, err := s.st.ListPersonaTemplates(ctx)
	if err != nil {
		return fmt.Errorf("persona: load templates: %w", err)
	}
	for _, u := range unreadable {
		s.log.Warn("stored persona template cannot be read; leaving it out", "template", u.Name, "error", u.Err)
	}
	stored := make(map[string]store.PersonaTemplateRecord, len(rows))
	for _, r := range rows {
		if err := checkTemplateDoc(r.Persona); err != nil {
			s.log.Warn("stored persona template is invalid; leaving it out", "template", r.Name, "error", err)
			continue
		}
		stored[r.Name] = r
	}
	s.publish(stored)
	s.log.Info("persona templates loaded", "stored", len(stored))
	return nil
}

// Template returns the persona template name.
func (s *Service) Template(name string) (conloomapi.PersonaDoc, bool) {
	doc, ok := s.catalog.Load().docs[name]
	return doc, ok
}

// Templates lists the persona templates by name.
func (s *Service) Templates() []TemplateInfo {
	return slices.Clone(s.catalog.Load().infos)
}

// ReferencedImages returns the IDs of the appearance images the stored
// persona templates name, sorted.
func (s *Service) ReferencedImages() []string {
	return slices.Clone(s.catalog.Load().images)
}

// PutTemplate stores doc as the persona template name for actor and
// publishes it, replacing the stored template of that name or adding a new
// one; a stored "default" takes the place of the built-in one. Conversations
// whose first version is made afterwards, and a template applied with
// Update, get it; existing versions are kept. The name of a new template must
// be 1 to 32 lowercase letters, digits, - or _, and none of
// reservedTemplateNames; a stored template keeps the name it has. An invalid
// name, a doc that fails Validate and an appearance image that was not
// uploaded are ErrInvalidTemplate.
func (s *Service) PutTemplate(ctx context.Context, name string, doc conloomapi.PersonaDoc, actor string) error {
	if err := checkTemplateDoc(doc); err != nil {
		return err
	}
	if err := s.checkImage(ctx, doc, ErrInvalidTemplate); err != nil {
		return err
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if _, ok := s.stored[name]; !ok {
		if !validTemplateName(name) {
			return fmt.Errorf("%w: name must be 1 to %d lowercase letters, digits, - or _, got %q", ErrInvalidTemplate, maxTemplateName, name)
		}
		if reservedTemplateName(name) {
			return fmt.Errorf("%w: %w", ErrInvalidTemplate, errReservedTemplateName)
		}
	}
	r := store.PersonaTemplateRecord{Name: name, Persona: doc, UpdatedBy: actor, UpdatedAt: time.Now().UTC()}
	if err := s.st.PutPersonaTemplate(ctx, r); err != nil {
		return fmt.Errorf("persona: store template %q: %w", name, err)
	}
	stored := maps.Clone(s.stored)
	stored[name] = r
	s.publish(stored)
	return nil
}

// ResetTemplate deletes the stored "default", so that the built-in default
// persona takes its place again; without one it does nothing. Any other name
// is ErrInvalidTemplate.
func (s *Service) ResetTemplate(ctx context.Context, name string) error {
	if name != defaultTemplate {
		return fmt.Errorf("%w: only %q can be reset, to the built-in default persona; got %q", ErrInvalidTemplate, defaultTemplate, name)
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if _, err := s.st.DeletePersonaTemplate(ctx, name); err != nil {
		return fmt.Errorf("persona: delete template %q: %w", name, err)
	}
	stored := maps.Clone(s.stored)
	delete(stored, name)
	s.publish(stored)
	return nil
}

// DeleteTemplate deletes the stored template name, also a row LoadTemplates
// left out, even while bots use it, and returns those bots' IDs (see
// SetTemplateUsers). It does not change them: publishing the bots again
// (core.PanelBotService.Load) makes them use "default", noting the problem,
// and until then Current starts their new conversations from "default".
// "default" is ErrDefaultTemplate (see ResetTemplate); an unknown name is
// store.ErrNotFound.
func (s *Service) DeleteTemplate(ctx context.Context, name string) ([]string, error) {
	if name == defaultTemplate {
		return nil, fmt.Errorf("%w; reset it to go back to the built-in default persona", ErrDefaultTemplate)
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	var users []string
	if s.templateUsers != nil {
		users = s.templateUsers(name)
	}
	deleted, err := s.st.DeletePersonaTemplate(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("persona: delete template %q: %w", name, err)
	}
	if !deleted {
		return nil, fmt.Errorf("%w: persona template %q", store.ErrNotFound, name)
	}
	stored := maps.Clone(s.stored)
	delete(stored, name)
	s.publish(stored)
	return users, nil
}

// publish makes stored the catalog's stored templates and publishes the
// catalog. The caller holds templateMu.
func (s *Service) publish(stored map[string]store.PersonaTemplateRecord) {
	s.stored = stored
	s.catalog.Store(newCatalog(stored))
}

// checkTemplateDoc returns ErrInvalidTemplate when doc fails Validate.
func checkTemplateDoc(doc conloomapi.PersonaDoc) error {
	if err := errors.Join(doc.Validate(), conloomapi.ValidateContent(doc)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}
	return nil
}

// reservedTemplateNames are names no new template may have, as the panel's
// API uses persona-templates/export and persona-templates/import as paths of
// their own. Templates stored with them before keep them.
var reservedTemplateNames = []string{"export", "import"}

// errReservedTemplateName is why a new template cannot have one of
// reservedTemplateNames.
var errReservedTemplateName = errors.New("模板名不能是 export 或 import，这两个名字留给面板的导出和导入")

// reservedTemplateName reports whether name is one of reservedTemplateNames.
func reservedTemplateName(name string) bool {
	return slices.Contains(reservedTemplateNames, name)
}

// validTemplateName reports whether name is 1 to maxTemplateName lowercase
// letters, digits, - or _.
func validTemplateName(name string) bool {
	if name == "" || len(name) > maxTemplateName {
		return false
	}
	for _, c := range []byte(name) {
		if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// checkImage returns invalid, wrapped, when p names an appearance image that
// was not uploaded, and an error of its own when the check fails.
func (s *Service) checkImage(ctx context.Context, p conloomapi.PersonaDoc, invalid error) error {
	if p.AppearanceImage == "" {
		return nil
	}
	ok, err := s.imageUploaded(ctx, p.AppearanceImage)
	switch {
	case err != nil:
		return fmt.Errorf("persona: check appearance image: %w", err)
	case !ok:
		return fmt.Errorf("%w: unknown appearance_image %q", invalid, p.AppearanceImage)
	}
	return nil
}

// imageUploaded reports whether the appearance image id was uploaded (see
// SetImageChecker).
func (s *Service) imageUploaded(ctx context.Context, id string) (bool, error) {
	exists := s.imageExists
	if exists == nil {
		exists = s.st.PersonaImageExists
	}
	return exists(ctx, id)
}

// Current returns the latest persona version, creating version 1 from the
// catalog's template templateName when none exists. Without such a template,
// as for a bot whose template was deleted before the bots were published
// again, version 1 is made from "default", which the catalog always has, and
// records it as its template; this is logged as a warning.
func (s *Service) Current(ctx context.Context, convKey, templateName string) (store.PersonaVersion, error) {
	pv, err := s.st.LatestPersona(ctx, convKey)
	if !errors.Is(err, store.ErrNotFound) {
		return pv, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	pv, err = s.st.LatestPersona(ctx, convKey)
	if !errors.Is(err, store.ErrNotFound) {
		return pv, err
	}
	doc, ok := s.Template(templateName)
	if !ok {
		s.log.Warn("persona template does not exist; the new conversation starts from default",
			"conversation", convKey, "template", templateName)
		templateName = defaultTemplate
		doc, _ = s.Template(defaultTemplate)
	}
	return s.st.AppendPersonaVersion(ctx, store.PersonaVersion{
		ConversationKey: convKey, TemplateName: templateName, Persona: doc,
		Reason: "init", UpdatedBy: "system",
	})
}

// TemplateNames returns the names of the persona templates, sorted.
func (s *Service) TemplateNames() []string {
	infos := s.catalog.Load().infos
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	return names
}

// Update appends a version holding put.Persona (recorded with the template
// of the latest version, which it edits, or templateName when there is none
// or it records no template), the content and template of version
// put.RollbackTo, or a copy of the
// catalog's template put.Template (recorded as the version's template).
// Exactly one of them must be set; an unknown template is ErrInvalid, and so
// is a resulting persona that fails Validate or names an appearance image
// that was not uploaded. It records a persona.update audit event; an audit
// failure is returned together with the already stored version.
func (s *Service) Update(ctx context.Context, convKey, templateName string, put conloomapi.PersonaPut, by string) (store.PersonaVersion, error) {
	set := 0
	for _, ok := range []bool{put.Persona != nil, put.RollbackTo != nil, put.Template != ""} {
		if ok {
			set++
		}
	}
	if set != 1 {
		return store.PersonaVersion{}, fmt.Errorf("%w: exactly one of persona, rollback_to and template must be set", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	next := store.PersonaVersion{ConversationKey: convKey, TemplateName: templateName, Reason: put.Reason, UpdatedBy: by}
	if put.Template != "" {
		doc, ok := s.Template(put.Template)
		if !ok {
			return store.PersonaVersion{}, fmt.Errorf("%w: unknown template %q", ErrInvalid, put.Template)
		}
		next.Persona, next.TemplateName = doc, put.Template
		if next.Reason == "" {
			next.Reason = "template " + put.Template
		}
	} else if put.RollbackTo != nil {
		old, err := s.st.GetPersonaVersion(ctx, convKey, *put.RollbackTo)
		if err != nil {
			return store.PersonaVersion{}, fmt.Errorf("persona: rollback to v%d: %w", *put.RollbackTo, err)
		}
		next.Persona, next.TemplateName = old.Persona, old.TemplateName
		if next.Reason == "" {
			next.Reason = fmt.Sprintf("rollback to v%d", *put.RollbackTo)
		}
	} else {
		next.Persona = *put.Persona
		latest, err := s.st.LatestPersona(ctx, convKey)
		switch {
		case err == nil:
			next.TemplateName = cmp.Or(latest.TemplateName, templateName)
		case !errors.Is(err, store.ErrNotFound):
			return store.PersonaVersion{}, err
		}
	}
	if err := Validate(next.Persona); err != nil {
		return store.PersonaVersion{}, err
	}
	if err := s.checkImage(ctx, next.Persona, ErrInvalid); err != nil {
		return store.PersonaVersion{}, err
	}
	pv, err := s.st.AppendPersonaVersion(ctx, next)
	if err != nil {
		return store.PersonaVersion{}, err
	}
	detail, _ := json.Marshal(struct {
		Version    int    `json:"version"`
		RollbackTo *int   `json:"rollback_to,omitempty"`
		Template   string `json:"template,omitempty"`
	}{pv.Version, put.RollbackTo, put.Template})
	if err := s.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID(convKey), Kind: "persona.update",
		Actor: by, Target: convKey, Detail: string(detail), CreatedAt: time.Now().UTC(),
	}); err != nil {
		return pv, fmt.Errorf("persona: audit: %w", err)
	}
	return pv, nil
}

// Prompt is a conversation's system prompt: Text built from persona version
// Version under content mode Mode, in which the bot is called BotName.
type Prompt struct {
	Text    string
	Version int
	BotName string
	Mode    conloomapi.ContentMode
}

// BotName is the bot's name under persona p: its character name when set,
// otherwise botName (the configured bot name).
func BotName(p conloomapi.PersonaDoc, botName string) string {
	if name := strings.TrimSpace(p.CharacterName); name != "" {
		return name
	}
	return botName
}

// PlatformNameNote tells a bot called name that members may also call it by
// its platform name botName. It is empty when the two are the same.
func PlatformNameNote(name, botName string) string {
	if botName == "" || name == botName {
		return ""
	}
	return "群里的人也可能用你在平台上的名字“" + botName + "”称呼你，那也是你。"
}

// SystemPrompt returns CorePrompt of the bot's name under the current persona
// (see BotName), the content rules of the conversation's content mode (see
// contentRules), the PlatformNameNote when that name is not the configured
// botName, and the rendered persona, separated by blank lines. Version,
// content mode and configured bot name determine the prompt, so one version
// always yields byte-identical output; it is cached per conversation for the
// maxCachedPrompts conversations most recently asked for.
func (s *Service) SystemPrompt(ctx context.Context, convKey, templateName, botName string) (Prompt, error) {
	pv, err := s.Current(ctx, convKey, templateName)
	if err != nil {
		return Prompt{}, err
	}
	mode := conloomapi.ModeFor(pv.Persona, convType(convKey))
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if e, ok := s.prompts[convKey]; ok {
		if c := e.Value.(cachedPrompt); c.version == pv.Version && c.botName == botName && c.mode == mode {
			s.promptOrder.MoveToFront(e)
			return c.prompt, nil
		}
	}
	name := BotName(pv.Persona, botName)
	text := CorePrompt(name) + "\n\n"
	if rules := contentRules(pv.Persona, mode); rules != "" {
		text += rules + "\n\n"
	}
	if note := PlatformNameNote(name, botName); note != "" {
		text += note + "\n\n"
	}
	p := Prompt{Text: text + Render(pv.Persona), Version: pv.Version, BotName: name, Mode: mode}
	s.cachePrompt(cachedPrompt{convKey: convKey, version: pv.Version, botName: botName, mode: mode, prompt: p})
	return p, nil
}

// cachePrompt caches c as its conversation's prompt, the most recently used,
// dropping the least recently used one when promptLimit are cached. The
// caller holds promptMu.
func (s *Service) cachePrompt(c cachedPrompt) {
	if e, ok := s.prompts[c.convKey]; ok {
		e.Value = c
		s.promptOrder.MoveToFront(e)
		return
	}
	if s.promptOrder.Len() >= s.promptLimit {
		oldest := s.promptOrder.Back()
		s.promptOrder.Remove(oldest)
		delete(s.prompts, oldest.Value.(cachedPrompt).convKey)
	}
	s.prompts[c.convKey] = s.promptOrder.PushFront(c)
}

// Forget drops the conversation's cached system prompt.
func (s *Service) Forget(convKey string) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if e, ok := s.prompts[convKey]; ok {
		s.promptOrder.Remove(e)
		delete(s.prompts, convKey)
	}
}

// botID returns the bot instance part of a "bot:platform:type:id" conversation key.
func botID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}

// convType returns the type part of a "bot:platform:type:id" conversation
// key, or "" when the key is malformed.
func convType(convKey string) string {
	if parts := strings.SplitN(convKey, ":", 4); len(parts) == 4 {
		return parts[2]
	}
	return ""
}
