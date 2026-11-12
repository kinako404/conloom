// Package persona manages per-conversation persona versions and renders system prompts.
package persona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ErrInvalid wraps every persona validation failure.
var ErrInvalid = errors.New("persona: invalid")

// Validate checks p against conloomapi.PersonaDoc.Validate.
func Validate(p conloomapi.PersonaDoc) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

type cachedPrompt struct {
	version int
	botName string
	prompt  string
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

// Update appends a version holding either put.Persona or the content of version put.RollbackTo.
// Exactly one of them must be set. It records a persona.update audit event; an audit failure is
// returned together with the already stored version.
func (s *Service) Update(ctx context.Context, convKey, templateName string, put conloomapi.PersonaPut, by string) (store.PersonaVersion, error) {
	if (put.Persona == nil) == (put.RollbackTo == nil) {
		return store.PersonaVersion{}, fmt.Errorf("%w: exactly one of persona and rollback_to must be set", ErrInvalid)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	next := store.PersonaVersion{ConversationKey: convKey, TemplateName: templateName, Reason: put.Reason, UpdatedBy: by}
	if put.RollbackTo != nil {
		old, err := s.st.GetPersonaVersion(ctx, convKey, *put.RollbackTo)
		if err != nil {
			return store.PersonaVersion{}, fmt.Errorf("persona: rollback to v%d: %w", *put.RollbackTo, err)
		}
		next.Persona = old.Persona
		if next.Reason == "" {
			next.Reason = fmt.Sprintf("rollback to v%d", *put.RollbackTo)
		}
	} else {
		if err := Validate(*put.Persona); err != nil {
			return store.PersonaVersion{}, err
		}
		next.Persona = *put.Persona
	}
	pv, err := s.st.AppendPersonaVersion(ctx, next)
	if err != nil {
		return store.PersonaVersion{}, err
	}
	detail, _ := json.Marshal(struct {
		Version    int  `json:"version"`
		RollbackTo *int `json:"rollback_to,omitempty"`
	}{pv.Version, put.RollbackTo})
	if err := s.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID(convKey), Kind: "persona.update",
		Actor: by, Target: convKey, Detail: string(detail), CreatedAt: time.Now().UTC(),
	}); err != nil {
		return pv, fmt.Errorf("persona: audit: %w", err)
	}
	return pv, nil
}

// SystemPrompt returns CorePrompt(botName) followed by the rendered current persona. The prompt is
// cached per conversation and version, so one version always yields byte-identical output.
func (s *Service) SystemPrompt(ctx context.Context, convKey, templateName, botName string) (prompt string, version int, err error) {
	pv, err := s.Current(ctx, convKey, templateName)
	if err != nil {
		return "", 0, err
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if c, ok := s.prompts[convKey]; ok && c.version == pv.Version && c.botName == botName {
		return c.prompt, c.version, nil
	}
	prompt = CorePrompt(botName) + "\n\n" + Render(pv.Persona)
	s.prompts[convKey] = cachedPrompt{version: pv.Version, botName: botName, prompt: prompt}
	return prompt, pv.Version, nil
}

// botID returns the bot instance part of a "bot:platform:type:id" conversation key.
func botID(convKey string) string {
	id, _, _ := strings.Cut(convKey, ":")
	return id
}
