package conloomapi

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// PersonaDoc is a persona. CharacterName, Description, Appearance,
// SpeakingStyle and Examples are optional; a set CharacterName is the bot's
// name in the conversation. AppearanceImage, also optional, is the ID of an
// appearance reference image; it never enters the system prompt. A reply
// request carries the image only when AppearanceImageSkip is not set and the
// model it is routed to declares image support. Adult, unless Minor is also set,
// makes ContentOpen possible (see ModeFor); ContentRules and MemoryRules only
// take effect in it. Bars, also optional, define the hidden bars of private
// chats (see EffectiveBars); they never enter the system prompt.
type PersonaDoc struct {
	PersonaName         string           `json:"persona_name" yaml:"persona_name"`
	TemplateVersion     int              `json:"template_version" yaml:"template_version"`
	Style               PersonaStyle     `json:"style" yaml:"style"`
	Behavior            PersonaBehavior  `json:"behavior" yaml:"behavior"`
	CharacterName       string           `json:"character_name,omitempty" yaml:"character_name,omitempty"`
	Description         string           `json:"description,omitempty" yaml:"description,omitempty"`
	Appearance          string           `json:"appearance,omitempty" yaml:"appearance,omitempty"`
	AppearanceImage     string           `json:"appearance_image,omitempty" yaml:"appearance_image,omitempty"`
	AppearanceImageSkip bool             `json:"appearance_image_skip,omitempty" yaml:"appearance_image_skip,omitempty"`
	SpeakingStyle       string           `json:"speaking_style,omitempty" yaml:"speaking_style,omitempty"`
	Examples            []PersonaExample `json:"examples,omitempty" yaml:"examples,omitempty"`
	Adult               bool             `json:"adult,omitempty" yaml:"adult,omitempty"`
	Minor               bool             `json:"minor,omitempty" yaml:"minor,omitempty"`
	ContentRules        string           `json:"content_rules,omitempty" yaml:"content_rules,omitempty"`
	MemoryRules         string           `json:"memory_rules,omitempty" yaml:"memory_rules,omitempty"`
	Bars                []PersonaBar     `json:"bars,omitempty" yaml:"bars,omitempty"`
}

// PersonaExample is one example exchange: a member's message and the reply.
type PersonaExample struct {
	User  string `json:"user" yaml:"user"`
	Reply string `json:"reply" yaml:"reply"`
}

// PersonaBar is a hidden bar of private chats: a value in [0, 100] that
// decays toward Baseline, halving its distance every HalfLife. Description
// tells the model what high and low values mean.
type PersonaBar struct {
	Name        string   `json:"name" yaml:"name"`
	Baseline    int      `json:"baseline" yaml:"baseline"`
	HalfLife    Duration `json:"half_life" yaml:"half_life"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
}

// Persona text limits, counted in runes.
const (
	MaxCharacterNameRunes  = 20
	MaxDescriptionRunes    = 2000
	MaxAppearanceRunes     = 800
	MaxSpeakingStyleRunes  = 1000
	MaxPersonaExamples     = 8
	MaxExampleRunes        = 200
	MaxContentRulesRunes   = 2000
	MaxMemoryRulesRunes    = 1000
	MaxPersonaBars         = 4
	MaxBarNameRunes        = 6
	MaxBarDescriptionRunes = 40
)

// Bounds of PersonaBar.HalfLife.
const (
	MinBarHalfLife = 30 * time.Minute
	MaxBarHalfLife = 7 * 24 * time.Hour
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

// PersonaPut sets a new persona (Persona), restores an earlier version
// (RollbackTo) or copies a configured persona template (Template); exactly one
// of them is set.
type PersonaPut struct {
	Persona    *PersonaDoc `json:"persona,omitempty" yaml:"persona,omitempty"`
	RollbackTo *int        `json:"rollback_to,omitempty" yaml:"rollback_to,omitempty"`
	Template   string      `json:"template,omitempty" yaml:"template,omitempty"`
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

// DefaultBars returns the bars of a persona that defines none.
func DefaultBars() []PersonaBar {
	return []PersonaBar{
		{Name: "心情", Baseline: 60, HalfLife: Duration{6 * time.Hour}, Description: "高=开心放松，低=低落烦躁"},
		{Name: "精力", Baseline: 70, HalfLife: Duration{4 * time.Hour}, Description: "高=有精神，低=累、困"},
	}
}

// EffectiveBars returns a copy of p's bars, or DefaultBars when p defines
// none.
func (p PersonaDoc) EffectiveBars() []PersonaBar {
	if len(p.Bars) > 0 {
		return slices.Clone(p.Bars)
	}
	return DefaultBars()
}

// breaksName reports whether r must not appear in a character name: a line or
// paragraph separator, another control character, or a zero-width character.
func breaksName(r rune) bool {
	switch r {
	case '\u2028', '\u2029', '\u200b', '\u200c', '\u200d', '\ufeff', '\u2060':
		return true
	}
	return unicode.IsControl(r)
}

// breaksBarName reports whether r must not appear in a bar name: whitespace,
// a character breaksName rejects, a lenticular bracket or a colon.
func breaksBarName(r rune) bool {
	switch r {
	case '【', '】', '：', ':':
		return true
	}
	return unicode.IsSpace(r) || breaksName(r)
}

// isImageID reports whether s has the form of an image ID: 64 lowercase
// hexadecimal characters.
func isImageID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
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
	if strings.ContainsFunc(strings.TrimSpace(p.CharacterName), breaksName) {
		fail("character_name must be a single line without control or zero-width characters")
	}
	maxRunes("description", p.Description, MaxDescriptionRunes)
	maxRunes("appearance", p.Appearance, MaxAppearanceRunes)
	if p.AppearanceImage != "" && !isImageID(p.AppearanceImage) {
		fail("appearance_image must be an image ID of 64 lowercase hex characters")
	}
	maxRunes("speaking_style", p.SpeakingStyle, MaxSpeakingStyleRunes)
	maxRunes("content_rules", p.ContentRules, MaxContentRulesRunes)
	maxRunes("memory_rules", p.MemoryRules, MaxMemoryRulesRunes)
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
	if n := len(p.Bars); n > MaxPersonaBars {
		fail("bars must have at most %d items, got %d", MaxPersonaBars, n)
	}
	seen := make(map[string]int, len(p.Bars))
	for i, bar := range p.Bars {
		path := fmt.Sprintf("bars[%d]", i)
		switch n := utf8.RuneCountInString(bar.Name); {
		case n == 0:
			fail("%s.name must not be empty", path)
		case n > MaxBarNameRunes:
			fail("%s.name must be at most %d characters, got %d", path, MaxBarNameRunes, n)
		}
		if strings.ContainsFunc(bar.Name, breaksBarName) {
			fail("%s.name must not contain whitespace, control or zero-width characters, 【, 】 or colons", path)
		}
		if j, dup := seen[bar.Name]; dup {
			fail("%s.name duplicates bars[%d].name %q", path, j, bar.Name)
		} else if bar.Name != "" {
			seen[bar.Name] = i
		}
		if bar.Baseline < 0 || bar.Baseline > 100 {
			fail("%s.baseline must be in [0, 100], got %d", path, bar.Baseline)
		}
		if h := bar.HalfLife.Duration; h < MinBarHalfLife || h > MaxBarHalfLife {
			fail("%s.half_life must be in [30m, 168h], got %s", path, h)
		}
		maxRunes(path+".description", bar.Description, MaxBarDescriptionRunes)
	}
	return errors.Join(errs...)
}
