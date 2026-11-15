// Package persona manages per-conversation persona versions and renders system prompts.
package persona

import (
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
	ErrInvalidTemplate    = fmt.Errorf("%w template", ErrInvalid)
	ErrTemplateInUse      = errors.New("persona: template is in use")
	ErrTemplateConfigOnly = errors.New("persona: template is defined in the config file")
)

// maxTemplateName is the most characters of the name of a template written
// online.
const maxTemplateName = 32

// TemplateSource is where a persona template of the catalog comes from.
type TemplateSource string

const (
	// SourceConfig is a template of the config file, or the built-in
	// "default".
	SourceConfig TemplateSource = "config"
	// SourceConfigOverride is a template of the config file overridden
	// online.
	SourceConfigOverride TemplateSource = "config_override"
	// SourcePanel is a template created online.
	SourcePanel TemplateSource = "panel"
)

// TemplateInfo is a persona template of the catalog: Doc as it takes effect,
// where it comes from and, for an online override, the config file's version
// Config. UpdatedBy and UpdatedAt are of the online write; they are empty for
// SourceConfig.
type TemplateInfo struct {
	Name      string
	Source    TemplateSource
	Doc       conloomapi.PersonaDoc
	Config    *conloomapi.PersonaDoc
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

// newCatalog merges the config layer config with the online layer online, an
// online template replacing the config template of the same name.
func newCatalog(config map[string]conloomapi.PersonaDoc, online map[string]store.PersonaTemplateRecord) *catalog {
	c := &catalog{docs: make(map[string]conloomapi.PersonaDoc, len(config)+len(online))}
	maps.Copy(c.docs, config)
	for name, r := range online {
		c.docs[name] = r.Persona
	}
	for _, name := range slices.Sorted(maps.Keys(c.docs)) {
		info := TemplateInfo{Name: name, Source: SourceConfig, Doc: c.docs[name]}
		cfg, inConfig := config[name]
		if r, ok := online[name]; ok {
			info.Source, info.UpdatedBy, info.UpdatedAt = SourcePanel, r.UpdatedBy, r.UpdatedAt
			if inConfig {
				info.Source, info.Config = SourceConfigOverride, &cfg
			}
		}
		c.infos = append(c.infos, info)
	}
	images := map[string]bool{}
	for _, doc := range config {
		images[doc.AppearanceImage] = true
	}
	for _, r := range online {
		images[r.Persona.AppearanceImage] = true
	}
	delete(images, "")
	c.images = slices.Sorted(maps.Keys(images))
	return c
}

type cachedPrompt struct {
	version int
	botName string
	mode    conloomapi.ContentMode
	prompt  Prompt
}

// Service keeps the conversations' persona versions and the persona template
// catalog: the config file's templates (the config layer) merged with the
// templates written online (the online layer), which override config
// templates of the same name and add new ones. The catalog is replaced as a
// whole on every change, so a reader always sees one whole version of it.
type Service struct {
	st store.Store
	// config is the config layer; it never changes.
	config  map[string]conloomapi.PersonaDoc
	catalog atomic.Pointer[catalog]

	// templateMu serialises LoadTemplates and the template writes; online,
	// which it guards, is the online layer of the published catalog.
	templateMu sync.Mutex
	online     map[string]store.PersonaTemplateRecord

	imageExists   func(ctx context.Context, id string) (bool, error)
	templateUsers func(name string) []string
	log           *slog.Logger

	// writeMu serialises version creation so concurrent callers never append duplicates.
	writeMu sync.Mutex

	promptMu sync.Mutex
	prompts  map[string]cachedPrompt
}

// NewService returns a Service whose config layer is a copy of templates, with
// DefaultPersona as "default" when missing. LoadTemplates adds the online
// layer.
func NewService(st store.Store, templates map[string]conloomapi.PersonaDoc) *Service {
	config := make(map[string]conloomapi.PersonaDoc, len(templates)+1)
	maps.Copy(config, templates)
	if _, ok := config["default"]; !ok {
		config["default"] = conloomapi.DefaultPersona()
	}
	s := &Service{st: st, config: config, online: map[string]store.PersonaTemplateRecord{},
		log: slog.New(slog.DiscardHandler), prompts: map[string]cachedPrompt{}}
	s.catalog.Store(newCatalog(config, s.online))
	return s
}

// SetImageChecker replaces how persona writes check that an appearance image
// was uploaded, by default the store's PersonaImageExists. Call it before the
// Service is shared.
func (s *Service) SetImageChecker(f func(ctx context.Context, id string) (bool, error)) {
	s.imageExists = f
}

// SetTemplateUsers sets how DeleteTemplate finds the users of a template: f
// returns the names of the panel bots whose persona template is name. Without
// it no template is in use. Call it before the Service is shared.
func (s *Service) SetTemplateUsers(f func(name string) []string) {
	s.templateUsers = f
}

// SetLogger sets the logger LoadTemplates reports to; by default it logs
// nothing. Call it before the Service is shared.
func (s *Service) SetLogger(l *slog.Logger) {
	s.log = l
}

// LoadTemplates replaces the online layer with the templates stored in the
// database. A row that cannot be read or is not a valid template (see
// PutTemplate; its image is not checked) is logged as a warning and left out
// of the catalog; the stored row is left as it is.
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
	online := make(map[string]store.PersonaTemplateRecord, len(rows))
	for _, r := range rows {
		if err := s.checkTemplate(r.Name, r.Persona); err != nil {
			s.log.Warn("stored persona template is invalid; leaving it out", "template", r.Name, "error", err)
			continue
		}
		online[r.Name] = r
	}
	s.publish(online)
	s.log.Info("persona templates loaded", "online", len(online))
	return nil
}

// Template returns the persona template name as it takes effect.
func (s *Service) Template(name string) (conloomapi.PersonaDoc, bool) {
	doc, ok := s.catalog.Load().docs[name]
	return doc, ok
}

// Templates lists the persona templates by name.
func (s *Service) Templates() []TemplateInfo {
	infos := slices.Clone(s.catalog.Load().infos)
	for i, info := range infos {
		if info.Config != nil {
			cfg := *info.Config
			infos[i].Config = &cfg
		}
	}
	return infos
}

// ReferencedImages returns the IDs of the appearance images the persona
// templates name, sorted: those of the online templates and of all config
// templates, also one overridden online, whose image a reset brings back.
func (s *Service) ReferencedImages() []string {
	return slices.Clone(s.catalog.Load().images)
}

// PutTemplate stores doc as the persona template name for actor and
// publishes it: it overrides the config template of that name or adds a
// panel template. Conversations whose first version is made afterwards, and
// a template applied with Update, get it; existing versions are kept. The
// name of a new template must be 1 to 32 lowercase letters, digits, - or _;
// a config template keeps the name it has. An invalid name, a doc that fails
// Validate and an appearance image that was not uploaded are
// ErrInvalidTemplate.
func (s *Service) PutTemplate(ctx context.Context, name string, doc conloomapi.PersonaDoc, actor string) error {
	if err := s.checkTemplate(name, doc); err != nil {
		return err
	}
	if err := s.checkImage(ctx, doc, ErrInvalidTemplate); err != nil {
		return err
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	r := store.PersonaTemplateRecord{Name: name, Persona: doc, UpdatedBy: actor, UpdatedAt: time.Now().UTC()}
	if err := s.st.PutPersonaTemplate(ctx, r); err != nil {
		return fmt.Errorf("persona: store template %q: %w", name, err)
	}
	online := maps.Clone(s.online)
	online[name] = r
	s.publish(online)
	return nil
}

// ResetTemplate deletes the online override of the config template name, so
// that the config file's version takes effect again; without an override it
// does nothing. A panel template is ErrInvalidTemplate, an unknown name
// store.ErrNotFound.
func (s *Service) ResetTemplate(ctx context.Context, name string) error {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if _, ok := s.config[name]; !ok {
		if _, ok := s.online[name]; ok {
			return fmt.Errorf("%w: %q is not a template of the config file; delete it instead", ErrInvalidTemplate, name)
		}
		return fmt.Errorf("%w: persona template %q", store.ErrNotFound, name)
	}
	if _, err := s.st.DeletePersonaTemplate(ctx, name); err != nil {
		return fmt.Errorf("persona: delete template %q: %w", name, err)
	}
	online := maps.Clone(s.online)
	delete(online, name)
	s.publish(online)
	return nil
}

// DeleteTemplate deletes the panel template name, or a stored row of that
// name LoadTemplates left out. A config template, overridden online or not,
// is ErrTemplateConfigOnly; a template panel bots use (see SetTemplateUsers)
// is ErrTemplateInUse naming them; an unknown name is store.ErrNotFound.
func (s *Service) DeleteTemplate(ctx context.Context, name string) error {
	if _, ok := s.config[name]; ok {
		return fmt.Errorf("%w: %q can only be reset", ErrTemplateConfigOnly, name)
	}
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if s.templateUsers != nil {
		if users := s.templateUsers(name); len(users) > 0 {
			return fmt.Errorf("%w: 面板 bot %s 还在用这个模板", ErrTemplateInUse, strings.Join(users, "、"))
		}
	}
	deleted, err := s.st.DeletePersonaTemplate(ctx, name)
	if err != nil {
		return fmt.Errorf("persona: delete template %q: %w", name, err)
	}
	if !deleted {
		return fmt.Errorf("%w: persona template %q", store.ErrNotFound, name)
	}
	online := maps.Clone(s.online)
	delete(online, name)
	s.publish(online)
	return nil
}

// publish makes online the online layer and publishes the catalog it makes
// with the config layer. The caller holds templateMu.
func (s *Service) publish(online map[string]store.PersonaTemplateRecord) {
	s.online = online
	s.catalog.Store(newCatalog(s.config, online))
}

// checkTemplate returns ErrInvalidTemplate when doc fails Validate or name may
// not name a template written online: a config template's name always may,
// others must be 1 to maxTemplateName lowercase letters, digits, - or _.
func (s *Service) checkTemplate(name string, doc conloomapi.PersonaDoc) error {
	if _, ok := s.config[name]; !ok && !validTemplateName(name) {
		return fmt.Errorf("%w: name must be 1 to %d lowercase letters, digits, - or _, got %q", ErrInvalidTemplate, maxTemplateName, name)
	}
	if err := errors.Join(doc.Validate(), conloomapi.ValidateContent(doc)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}
	return nil
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
	exists := s.imageExists
	if exists == nil {
		exists = s.st.PersonaImageExists
	}
	ok, err := exists(ctx, p.AppearanceImage)
	switch {
	case err != nil:
		return fmt.Errorf("persona: check appearance image: %w", err)
	case !ok:
		return fmt.Errorf("%w: unknown appearance_image %q", invalid, p.AppearanceImage)
	}
	return nil
}

// Current returns the latest persona version, creating version 1 from the
// catalog's template templateName when none exists.
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
		return store.PersonaVersion{}, fmt.Errorf("persona: unknown template %q", templateName)
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

// Update appends a version holding put.Persona (recorded with templateName),
// the content and template of version put.RollbackTo, or a copy of the
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
// botName, and the rendered persona, separated by blank lines. The prompt is
// cached per conversation, version, content mode and configured bot name,
// which determine it, so one version always yields byte-identical output.
func (s *Service) SystemPrompt(ctx context.Context, convKey, templateName, botName string) (Prompt, error) {
	pv, err := s.Current(ctx, convKey, templateName)
	if err != nil {
		return Prompt{}, err
	}
	mode := conloomapi.ModeFor(pv.Persona, convType(convKey))
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if c, ok := s.prompts[convKey]; ok && c.version == pv.Version && c.botName == botName && c.mode == mode {
		return c.prompt, nil
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
	s.prompts[convKey] = cachedPrompt{version: pv.Version, botName: botName, mode: mode, prompt: p}
	return p, nil
}

// Forget drops the conversation's cached system prompt.
func (s *Service) Forget(convKey string) {
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	delete(s.prompts, convKey)
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
