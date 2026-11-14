// Package persona manages per-conversation persona versions and renders system prompts.
package persona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
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

type cachedPrompt struct {
	version int
	botName string
	prompt  Prompt
}

type Service struct {
	st        store.Store
	templates map[string]conloomapi.PersonaDoc

	// writeMu serialises version creation so concurrent callers never append duplicates.
	writeMu sync.Mutex

	promptMu sync.Mutex
	prompts  map[string]cachedPrompt
}

// NewService copies templates and adds DefaultPersona as "default" when missing.
func NewService(st store.Store, templates map[string]conloomapi.PersonaDoc) *Service {
	t := make(map[string]conloomapi.PersonaDoc, len(templates)+1)
	for name, doc := range templates {
		t[name] = doc
	}
	if _, ok := t["default"]; !ok {
		t["default"] = conloomapi.DefaultPersona()
	}
	return &Service{st: st, templates: t, prompts: map[string]cachedPrompt{}}
}

// Current returns the latest persona version, creating version 1 from templateName when none exists.
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
	doc, ok := s.templates[templateName]
	if !ok {
		return store.PersonaVersion{}, fmt.Errorf("persona: unknown template %q", templateName)
	}
	return s.st.AppendPersonaVersion(ctx, store.PersonaVersion{
		ConversationKey: convKey, TemplateName: templateName, Persona: doc,
		Reason: "init", UpdatedBy: "system",
	})
}

// TemplateNames returns the names of the configured persona templates, sorted.
func (s *Service) TemplateNames() []string {
	return slices.Sorted(maps.Keys(s.templates))
}

// Update appends a version holding put.Persona (recorded with templateName),
// the content and template of version put.RollbackTo, or a copy of template
// put.Template (recorded as the version's template). Exactly one of them must
// be set; an unknown template is ErrInvalid, and so is a resulting persona
// that fails Validate. It records a persona.update audit event; an audit
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
		doc, ok := s.templates[put.Template]
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
// Version, in which the bot is called BotName.
type Prompt struct {
	Text    string
	Version int
	BotName string
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
// (see BotName), the PlatformNameNote when that name is not the configured
// botName, and the rendered persona, separated by blank lines. The prompt is
// cached per conversation, version and configured bot name, which determine
// it, so one version always yields byte-identical output.
func (s *Service) SystemPrompt(ctx context.Context, convKey, templateName, botName string) (Prompt, error) {
	pv, err := s.Current(ctx, convKey, templateName)
	if err != nil {
		return Prompt{}, err
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if c, ok := s.prompts[convKey]; ok && c.version == pv.Version && c.botName == botName {
		return c.prompt, nil
	}
	name := BotName(pv.Persona, botName)
	text := CorePrompt(name) + "\n\n"
	if note := PlatformNameNote(name, botName); note != "" {
		text += note + "\n\n"
	}
	p := Prompt{Text: text + Render(pv.Persona), Version: pv.Version, BotName: name}
	s.prompts[convKey] = cachedPrompt{version: pv.Version, botName: botName, prompt: p}
	return p, nil
}

// botID returns the bot instance part of a "bot:platform:type:id" conversation key.
func botID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}
