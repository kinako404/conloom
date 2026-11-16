package conloomapi

import (
	"strings"
	"testing"
)

func TestModeFor_Table(t *testing.T) {
	open := 0
	for _, adult := range []bool{false, true} {
		for _, minor := range []bool{false, true} {
			for _, convType := range []string{ConvGroup, ConvPrivate} {
				doc := DefaultPersona()
				doc.Adult, doc.Minor = adult, minor
				got := ModeFor(doc, convType)
				want := ContentProtected
				if adult && !minor && convType == ConvPrivate {
					want = ContentOpen
				}
				if got != want {
					t.Errorf("adult=%v minor=%v conv=%s: ModeFor = %q, want %q", adult, minor, convType, got, want)
				}
				if got == ContentOpen {
					open++
				}
			}
		}
	}
	if open != 1 {
		t.Errorf("open combinations = %d, want 1", open)
	}
	adult := DefaultPersona()
	adult.Adult = true
	if got := ModeFor(adult, ""); got != ContentProtected {
		t.Errorf("empty conversation type: ModeFor = %q, want protected", got)
	}
}

func TestValidateContent(t *testing.T) {
	for _, c := range []struct {
		adult, minor bool
		wantErr      bool
	}{
		{false, false, false},
		{true, false, false},
		{false, true, false},
		{true, true, true},
	} {
		doc := DefaultPersona()
		doc.Adult, doc.Minor = c.adult, c.minor
		err := ValidateContent(doc)
		if !c.wantErr && err != nil {
			t.Errorf("adult=%v minor=%v: %v", c.adult, c.minor, err)
		}
		if c.wantErr && (err == nil || err.Error() != "a minor persona cannot be adult") {
			t.Errorf("adult=%v minor=%v: err = %v, want %q", c.adult, c.minor, err, "a minor persona cannot be adult")
		}
	}
}

func TestValidate_ContentRulesLength(t *testing.T) {
	ok := DefaultPersona()
	ok.ContentRules = strings.Repeat("规", MaxContentRulesRunes)
	ok.MemoryRules = strings.Repeat("记", MaxMemoryRulesRunes) + "\n"
	if err := ok.Validate(); err != nil {
		t.Errorf("rules at the limits: %v", err)
	}
	for _, c := range []struct {
		mutate func(*PersonaDoc)
		want   string
	}{
		{func(p *PersonaDoc) { p.ContentRules = strings.Repeat("规", 2001) }, "content_rules must be at most 2000 characters, got 2001"},
		{func(p *PersonaDoc) { p.MemoryRules = strings.Repeat("记", 1001) }, "memory_rules must be at most 1000 characters, got 1001"},
	} {
		p := DefaultPersona()
		c.mutate(&p)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("err = %v, want containing %q", err, c.want)
		}
	}
}

func TestValidateContent_MinorIntimacy(t *testing.T) {
	const minorAdult, minorIntimacy = "a minor persona cannot be adult", "a minor persona cannot have an intimacy side"
	side := &PersonaIntimacy{Mind: "想靠近"}

	minor := DefaultPersona()
	minor.Minor, minor.Intimacy = true, side
	if err := ValidateContent(minor); err == nil || err.Error() != minorIntimacy {
		t.Errorf("minor with an intimacy side: err = %v, want exactly %q", err, minorIntimacy)
	}

	levelsOnly := DefaultPersona()
	levelsOnly.Minor = true
	levelsOnly.Intimacy = &PersonaIntimacy{Tone: " ", Mind: "\n", Levels: DefaultIntimacyLevels()}
	if err := ValidateContent(levelsOnly); err != nil {
		t.Errorf("minor with levels only: %v", err)
	}

	boundsOnly := DefaultPersona()
	boundsOnly.Minor = true
	boundsOnly.Intimacy = &PersonaIntimacy{Bounds: []string{"", "可以牵手", "", "", ""}}
	if err := ValidateContent(boundsOnly); err == nil || err.Error() != minorIntimacy {
		t.Errorf("minor with a bound only: err = %v, want exactly %q", err, minorIntimacy)
	}

	neither := DefaultPersona()
	neither.Intimacy = side
	if err := ValidateContent(neither); err != nil {
		t.Errorf("neither adult nor minor, with an intimacy side: %v", err)
	}
	adult := neither
	adult.Adult = true
	if err := ValidateContent(adult); err != nil {
		t.Errorf("adult with an intimacy side: %v", err)
	}

	both := DefaultPersona()
	both.Minor, both.Adult, both.Intimacy = true, true, side
	err := ValidateContent(both)
	for _, want := range []string{minorAdult, minorIntimacy} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("minor adult with an intimacy side: err = %v, want containing %q", err, want)
		}
	}
}
