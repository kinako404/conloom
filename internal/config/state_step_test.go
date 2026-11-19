package config

import (
	"strings"
	"testing"
)

func TestLoad_SkillsStateStep(t *testing.T) {
	cases := []struct {
		name, extra, want string
	}{
		{"default", "", StateStepAfterReply},
		{"inline", "skills:\n  state_step: inline\n", StateStepInline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := loadWith(t, c.extra)
			if cfg.Skills.StateStep != c.want {
				t.Errorf("Skills.StateStep = %q, want %q", cfg.Skills.StateStep, c.want)
			}
			if err := cfg.ValidateServe(); err != nil {
				t.Fatalf("ValidateServe: %v", err)
			}
		})
	}
}

func TestValidate_SkillsStateStep(t *testing.T) {
	for _, v := range []string{"Inline", "later"} {
		cfg := loadWith(t, "")
		cfg.Skills.StateStep = v
		want := `skills.state_step: must be after_reply or inline, got "` + v + `"`
		if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("StateStep %q: ValidateServe = %v, want %q", v, err, want)
		}
	}
	for _, v := range []string{"", StateStepAfterReply, StateStepInline} {
		cfg := loadWith(t, "")
		cfg.Skills.StateStep = v
		if err := cfg.ValidateServe(); err != nil && strings.Contains(err.Error(), "skills.state_step") {
			t.Errorf("StateStep %q: ValidateServe = %v, want no skills.state_step problem", v, err)
		}
	}
}
