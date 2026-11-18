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

// PersonaDoc is a persona. CharacterName, Gender, Description, Appearance,
// SpeakingStyle and Examples are optional; a set CharacterName is the bot's
// name in the conversation, Gender is GenderFemale or GenderMale when set.
// AppearanceImage, also optional, is the ID of an appearance reference
// image; it never enters the system prompt. A reply
// request carries the image only when AppearanceImageSkip is not set and the
// model it is routed to declares image support. AvatarImage, also optional,
// is the ID of the image the panel shows as the bot's avatar; it is never
// sent to a model. Adult, unless Minor is also set,
// makes ContentOpen possible (see ModeFor); ContentRules, AdultAppearance and
// MemoryRules only take effect in it. AdultAppearance enters the system
// prompt right after the appearance, and a minor persona cannot have one (see
// ValidateContent). Intimacy, also optional, is the intimacy side (see
// PersonaIntimacy): it too only takes effect in ContentOpen, it never enters
// the system prompt, and a minor persona cannot have one (see
// ValidateContent). Bars, also optional, define the bars of private chats
// (see EffectiveBars); they never enter the system prompt.
type PersonaDoc struct {
	PersonaName         string           `json:"persona_name" yaml:"persona_name"`
	TemplateVersion     int              `json:"template_version" yaml:"template_version"`
	Style               PersonaStyle     `json:"style" yaml:"style"`
	Behavior            PersonaBehavior  `json:"behavior" yaml:"behavior"`
	CharacterName       string           `json:"character_name,omitempty" yaml:"character_name,omitempty"`
	Gender              string           `json:"gender,omitempty" yaml:"gender,omitempty"`
	Description         string           `json:"description,omitempty" yaml:"description,omitempty"`
	Appearance          string           `json:"appearance,omitempty" yaml:"appearance,omitempty"`
	AppearanceImage     string           `json:"appearance_image,omitempty" yaml:"appearance_image,omitempty"`
	AppearanceImageSkip bool             `json:"appearance_image_skip,omitempty" yaml:"appearance_image_skip,omitempty"`
	AvatarImage         string           `json:"avatar_image,omitempty" yaml:"avatar_image,omitempty"`
	SpeakingStyle       string           `json:"speaking_style,omitempty" yaml:"speaking_style,omitempty"`
	Examples            []PersonaExample `json:"examples,omitempty" yaml:"examples,omitempty"`
	Adult               bool             `json:"adult,omitempty" yaml:"adult,omitempty"`
	Minor               bool             `json:"minor,omitempty" yaml:"minor,omitempty"`
	ContentRules        string           `json:"content_rules,omitempty" yaml:"content_rules,omitempty"`
	AdultAppearance     string           `json:"adult_appearance,omitempty" yaml:"adult_appearance,omitempty"`
	Intimacy            *PersonaIntimacy `json:"intimacy,omitempty" yaml:"intimacy,omitempty"`
	MemoryRules         string           `json:"memory_rules,omitempty" yaml:"memory_rules,omitempty"`
	Bars                []PersonaBar     `json:"bars,omitempty" yaml:"bars,omitempty"`
}

// Values of PersonaDoc.Gender.
const (
	GenderFemale = "female"
	GenderMale   = "male"
)

// PersonaExample is one example exchange: a member's message and the reply.
type PersonaExample struct {
	User  string `json:"user" yaml:"user"`
	Reply string `json:"reply" yaml:"reply"`
}

// PersonaBar is a bar of private chats: a value in [0, 100] that decays
// toward Baseline, halving its distance every HalfLife. Description tells the
// model what high and low values mean. Kind is BarKindInner (also when empty:
// hidden, shown to the model as a band only, it only affects the tone) or
// BarKindOuter (visible, shown to the model with its value; the bot may show
// it in its descriptions). adjust_buff moves the bar by at most
// EffectiveMaxStep per call and EffectiveMaxDay net per local day.
type PersonaBar struct {
	Name        string   `json:"name" yaml:"name"`
	Baseline    int      `json:"baseline" yaml:"baseline"`
	HalfLife    Duration `json:"half_life" yaml:"half_life"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Kind        string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	MaxStep     int      `json:"max_step,omitempty" yaml:"max_step,omitempty"`
	MaxDay      int      `json:"max_day,omitempty" yaml:"max_day,omitempty"`
}

// PersonaIntimacy is a persona's intimacy side: how the character sounds
// (Tone), feels (Mind) and acts (Behavior) when close to someone. Levels
// says, for each affinity tier from the lowest, how much of it a reply mixes
// in; omitted, it is DefaultIntimacyLevels. Bounds, also one item per
// affinity tier from the lowest when set, replaces the built-in boundary of
// the open-mode relationship hint at each tier whose item is not blank (see
// BoundAt). An intimacy side whose three texts and bounds are all blank is no
// intimacy side (see Empty); one with bounds but blank texts has no side to
// mix in (see HasSide).
type PersonaIntimacy struct {
	Tone     string   `json:"tone,omitempty" yaml:"tone,omitempty"`
	Mind     string   `json:"mind,omitempty" yaml:"mind,omitempty"`
	Behavior string   `json:"behavior,omitempty" yaml:"behavior,omitempty"`
	Levels   []string `json:"levels,omitempty" yaml:"levels,omitempty"`
	Bounds   []string `json:"bounds,omitempty" yaml:"bounds,omitempty"`
}

// Values of PersonaIntimacy.Levels, from mixing in none of the intimacy side
// to mixing in the most.
const (
	IntimacyNone  = "none"
	IntimacyLight = "light"
	IntimacyHalf  = "half"
	IntimacyMajor = "major"
)

// intimacyLevels are the values of PersonaIntimacy.Levels in increasing order.
var intimacyLevels = []string{IntimacyNone, IntimacyLight, IntimacyHalf, IntimacyMajor}

// DefaultIntimacyLevels returns a new slice of the levels an intimacy side
// without Levels uses, one per affinity tier from the lowest.
func DefaultIntimacyLevels() []string {
	return []string{IntimacyNone, IntimacyNone, IntimacyLight, IntimacyHalf, IntimacyMajor}
}

// HasSide reports whether in is not nil and at least one of its three texts
// is not blank.
func (in *PersonaIntimacy) HasSide() bool {
	return in != nil &&
		(strings.TrimSpace(in.Tone) != "" || strings.TrimSpace(in.Mind) != "" || strings.TrimSpace(in.Behavior) != "")
}

// Empty reports whether in is nil, or its three texts and its bounds are all
// blank.
func (in *PersonaIntimacy) Empty() bool {
	return !in.HasSide() &&
		(in == nil || !slices.ContainsFunc(in.Bounds, func(b string) bool { return strings.TrimSpace(b) != "" }))
}

// BoundAt returns the bound of affinity tier tier, 0 being the lowest, with
// leading and trailing white space removed: "" when in is nil, has no Bounds
// or tier is out of range.
func (in *PersonaIntimacy) BoundAt(tier int) string {
	if in == nil || tier < 0 || tier >= len(in.Bounds) {
		return ""
	}
	return strings.TrimSpace(in.Bounds[tier])
}

// LevelAt returns the level of affinity tier tier, 0 being the lowest: from
// Levels, or from DefaultIntimacyLevels when Levels is empty or in is nil.
// A tier out of range is IntimacyNone.
func (in *PersonaIntimacy) LevelAt(tier int) string {
	levels := DefaultIntimacyLevels()
	if in != nil && len(in.Levels) > 0 {
		levels = in.Levels
	}
	if tier < 0 || tier >= len(levels) {
		return IntimacyNone
	}
	return levels[tier]
}

// Persona text limits, counted in runes. MaxIntimacyRunes bounds the three
// texts of an intimacy side together, MaxIntimacyBoundRunes each of its
// bounds.
const (
	MaxCharacterNameRunes   = 20
	MaxDescriptionRunes     = 2000
	MaxAppearanceRunes      = 800
	MaxSpeakingStyleRunes   = 1000
	MaxPersonaExamples      = 8
	MaxExampleRunes         = 200
	MaxContentRulesRunes    = 2000
	MaxAdultAppearanceRunes = 1000
	MaxIntimacyRunes        = 800
	MaxIntimacyBoundRunes   = 150
	MaxMemoryRulesRunes     = 1000
	MaxPersonaBars          = 4
	MaxBarNameRunes         = 6
	MaxBarDescriptionRunes  = 40
)

// Bounds of PersonaBar.HalfLife.
const (
	MinBarHalfLife = 30 * time.Minute
	MaxBarHalfLife = 7 * 24 * time.Hour
)

// Values of PersonaBar.Kind; empty is BarKindInner.
const (
	BarKindInner = "inner"
	BarKindOuter = "outer"
)

// Defaults and bound of PersonaBar.MaxStep and MaxDay.
const (
	DefaultBarMaxStep = 10
	DefaultBarMaxDay  = 30
	MaxBarLimit       = 100
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
// (RollbackTo) or copies a persona template (Template); exactly one
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

// Outer reports whether b is an outer bar (Kind is BarKindOuter).
func (b PersonaBar) Outer() bool {
	return b.Kind == BarKindOuter
}

// EffectiveMaxStep returns MaxStep, or DefaultBarMaxStep when it is 0 or
// less.
func (b PersonaBar) EffectiveMaxStep() int {
	if b.MaxStep <= 0 {
		return DefaultBarMaxStep
	}
	return b.MaxStep
}

// EffectiveMaxDay returns MaxDay, or DefaultBarMaxDay when it is 0 or less.
func (b PersonaBar) EffectiveMaxDay() int {
	if b.MaxDay <= 0 {
		return DefaultBarMaxDay
	}
	return b.MaxDay
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

// ValidBarName reports whether name may name a bar: 1 to MaxBarNameRunes
// runes, none of them whitespace, a control or zero-width character, 【, 】
// or a colon, and not dots only (see dotsOnly).
func ValidBarName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n > 0 && n <= MaxBarNameRunes && !strings.ContainsFunc(name, breaksBarName) && !dotsOnly(name)
}

// dotsOnly reports whether name is made of dots only, a path segment that the
// panel's and the API's …/autonomy/buffs/{name} would never receive.
func dotsOnly(name string) bool {
	return name != "" && strings.Trim(name, ".") == ""
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
	if !slices.Contains([]string{"", GenderFemale, GenderMale}, p.Gender) {
		fail("gender must be one of female|male or empty, got %q", p.Gender)
	}
	maxRunes("description", p.Description, MaxDescriptionRunes)
	maxRunes("appearance", p.Appearance, MaxAppearanceRunes)
	if p.AppearanceImage != "" && !isImageID(p.AppearanceImage) {
		fail("appearance_image must be an image ID of 64 lowercase hex characters")
	}
	if p.AvatarImage != "" && !isImageID(p.AvatarImage) {
		fail("avatar_image must be an image ID of 64 lowercase hex characters")
	}
	maxRunes("speaking_style", p.SpeakingStyle, MaxSpeakingStyleRunes)
	maxRunes("content_rules", p.ContentRules, MaxContentRulesRunes)
	maxRunes("adult_appearance", p.AdultAppearance, MaxAdultAppearanceRunes)
	maxRunes("memory_rules", p.MemoryRules, MaxMemoryRulesRunes)
	if in := p.Intimacy; in != nil {
		total := 0
		for _, text := range []string{in.Tone, in.Mind, in.Behavior} {
			total += utf8.RuneCountInString(strings.TrimSpace(text))
		}
		if total > MaxIntimacyRunes {
			fail("intimacy must be at most %d characters across tone, mind and behavior, got %d", MaxIntimacyRunes, total)
		}
		if n, tiers := len(in.Levels), len(DefaultIntimacyLevels()); n > 0 && n != tiers {
			fail("intimacy.levels must have exactly %d items, got %d", tiers, n)
		}
		for i, level := range in.Levels {
			switch rank := slices.Index(intimacyLevels, level); {
			case rank < 0:
				fail("intimacy.levels[%d] must be one of none|light|half|major, got %q", i, level)
			case i > 0 && rank < slices.Index(intimacyLevels, in.Levels[i-1]):
				fail("intimacy.levels[%d] must not be lower than intimacy.levels[%d], got %s after %s", i, i-1, level, in.Levels[i-1])
			}
		}
		if n, tiers := len(in.Bounds), len(DefaultIntimacyLevels()); n > 0 && n != tiers {
			fail("intimacy.bounds must have exactly %d items, got %d", tiers, n)
		}
		for i, bound := range in.Bounds {
			path := fmt.Sprintf("intimacy.bounds[%d]", i)
			maxRunes(path, bound, MaxIntimacyBoundRunes)
			if strings.ContainsAny(strings.TrimSpace(bound), "\r\n") {
				fail("%s must be a single line", path)
			}
		}
	}
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
		if dotsOnly(bar.Name) {
			fail("%s.name must not be made of dots only", path)
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
		if !slices.Contains([]string{"", BarKindInner, BarKindOuter}, bar.Kind) {
			fail("%s.kind must be inner or outer, got %q", path, bar.Kind)
		}
		limitsOK := true
		for _, l := range []struct {
			field    string
			v, deflt int
		}{{"max_step", bar.MaxStep, DefaultBarMaxStep}, {"max_day", bar.MaxDay, DefaultBarMaxDay}} {
			if l.v < 0 || l.v > MaxBarLimit {
				fail("%s.%s must be in [1, %d] (0 for the default %d), got %d", path, l.field, MaxBarLimit, l.deflt, l.v)
				limitsOK = false
			}
		}
		if step, day := bar.EffectiveMaxStep(), bar.EffectiveMaxDay(); limitsOK && day < step {
			fail("%s.max_day must be at least max_step (%d), got %d", path, step, day)
		}
	}
	return errors.Join(errs...)
}
