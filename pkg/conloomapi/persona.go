package conloomapi

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// PersonaDoc is a persona. CharacterName, Description, SpeakingStyle and
// Examples are optional; a set CharacterName is the bot's name in the
// conversation.
type PersonaDoc struct {
	PersonaName     string           `json:"persona_name" yaml:"persona_name"`
	TemplateVersion int              `json:"template_version" yaml:"template_version"`
	Style           PersonaStyle     `json:"style" yaml:"style"`
	Behavior        PersonaBehavior  `json:"behavior" yaml:"behavior"`
	CharacterName   string           `json:"character_name,omitempty" yaml:"character_name,omitempty"`
	Description     string           `json:"description,omitempty" yaml:"description,omitempty"`
	SpeakingStyle   string           `json:"speaking_style,omitempty" yaml:"speaking_style,omitempty"`
	Examples        []PersonaExample `json:"examples,omitempty" yaml:"examples,omitempty"`
}

// PersonaExample is one example exchange: a member's message and the reply.
type PersonaExample struct {
	User  string `json:"user" yaml:"user"`
	Reply string `json:"reply" yaml:"reply"`
}

// Persona text limits, counted in runes.
const (
	MaxCharacterNameRunes = 20
	MaxDescriptionRunes   = 2000
	MaxSpeakingStyleRunes = 1000
	MaxPersonaExamples    = 8
	MaxExampleRunes       = 200
)

type PersonaStyle struct {
	Warmth      float64 `json:"warmth" yaml:"warmth"`
	Humor       float64 `json:"humor" yaml:"humor"`
	Directness  float64 `json:"directness" yaml:"directness"`
	Verbosity   float64 `json:"verbosity" yaml:"verbosity"`
	EmojiUsage  float64 `json:"emoji_usage" yaml:"emoji_usage"`
	Proactivity float64 `json:"proactivity" yaml:"proactivity"`
}

type PersonaBehavior struct {
	DefaultReplyLength    string `json:"default_reply_length" yaml:"default_reply_length"`
	AllowMemes            bool   `json:"allow_memes" yaml:"allow_memes"`
	AskFollowupQuestions  string `json:"ask_followup_questions" yaml:"ask_followup_questions"`
	TechnicalExplanations string `json:"technical_explanations" yaml:"technical_explanations"`
}

type PersonaState struct {
	ConversationID string     `json:"conversation_id" yaml:"conversation_id"`
	Version        int        `json:"version" yaml:"version"`
	Persona        PersonaDoc `json:"persona" yaml:"persona"`
	Reason         string     `json:"reason" yaml:"reason"`
	UpdatedBy      string     `json:"updated_by" yaml:"updated_by"`
	CreatedAt      time.Time  `json:"created_at" yaml:"created_at"`
}

// PersonaPut sets a new persona (Persona) or restores an earlier version (RollbackTo).
type PersonaPut struct {
	Persona    *PersonaDoc `json:"persona,omitempty" yaml:"persona,omitempty"`
	RollbackTo *int        `json:"rollback_to,omitempty" yaml:"rollback_to,omitempty"`
	Reason     string      `json:"reason" yaml:"reason"`
}

func DefaultPersona() PersonaDoc {
	return PersonaDoc{
		PersonaName:     "默认群友人格",
		TemplateVersion: 1,
		Style: PersonaStyle{
			Warmth:      0.75,
			Humor:       0.45,
			Directness:  0.65,
			Verbosity:   0.30,
			EmojiUsage:  0.15,
			Proactivity: 0.20,
		},
		Behavior: PersonaBehavior{
			DefaultReplyLength:    "short",
			AllowMemes:            true,
			AskFollowupQuestions:  "occasionally",
			TechnicalExplanations: "clear_and_practical",
		},
	}
}

// Validate reports every rule violation, each prefixed with its snake_case field path.
func (p PersonaDoc) Validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}
	if strings.TrimSpace(p.PersonaName) == "" {
		fail("persona_name must not be empty")
	}
	s := p.Style
	for _, v := range []struct {
		path string
		v    float64
	}{
		{"style.warmth", s.Warmth},
		{"style.humor", s.Humor},
		{"style.directness", s.Directness},
		{"style.verbosity", s.Verbosity},
		{"style.emoji_usage", s.EmojiUsage},
		{"style.proactivity", s.Proactivity},
	} {
		if !(v.v >= 0 && v.v <= 1) {
			fail("%s must be in [0, 1], got %v", v.path, v.v)
		}
	}
	b := p.Behavior
	if !slices.Contains([]string{"short", "medium", "long"}, b.DefaultReplyLength) {
		fail("behavior.default_reply_length must be one of short|medium|long, got %q", b.DefaultReplyLength)
	}
	if !slices.Contains([]string{"never", "occasionally", "often"}, b.AskFollowupQuestions) {
		fail("behavior.ask_followup_questions must be one of never|occasionally|often, got %q", b.AskFollowupQuestions)
	}
	if !slices.Contains([]string{"brief", "clear_and_practical", "detailed"}, b.TechnicalExplanations) {
		fail("behavior.technical_explanations must be one of brief|clear_and_practical|detailed, got %q", b.TechnicalExplanations)
	}
	maxRunes := func(path, text string, limit int) {
		if n := utf8.RuneCountInString(strings.TrimSpace(text)); n > limit {
			fail("%s must be at most %d characters, got %d", path, limit, n)
		}
	}
	maxRunes("character_name", p.CharacterName, MaxCharacterNameRunes)
	if strings.ContainsAny(strings.TrimSpace(p.CharacterName), "\r\n") {
		fail("character_name must be a single line")
	}
	maxRunes("description", p.Description, MaxDescriptionRunes)
	maxRunes("speaking_style", p.SpeakingStyle, MaxSpeakingStyleRunes)
	if n := len(p.Examples); n > MaxPersonaExamples {
		fail("examples must have at most %d items, got %d", MaxPersonaExamples, n)
	}
	for i, ex := range p.Examples {
		for _, f := range []struct{ name, text string }{{"user", ex.User}, {"reply", ex.Reply}} {
			path := fmt.Sprintf("examples[%d].%s", i, f.name)
			if strings.TrimSpace(f.text) == "" {
				fail("%s must not be empty", path)
				continue
			}
			maxRunes(path, f.text, MaxExampleRunes)
		}
	}
	return errors.Join(errs...)
}
