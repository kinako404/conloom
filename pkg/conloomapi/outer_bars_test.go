package conloomapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestPersonaBars_ValidateKindAndLimits(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PersonaDoc)
		wantErr string
		notErr  string
	}{
		{"kind empty", func(p *PersonaDoc) { p.Bars[0].Kind = "" }, "", ""},
		{"kind inner", func(p *PersonaDoc) { p.Bars[0].Kind = BarKindInner }, "", ""},
		{"kind outer", func(p *PersonaDoc) { p.Bars[0].Kind = BarKindOuter }, "", ""},
		{"max_step 0", func(p *PersonaDoc) { p.Bars[0].MaxStep = 0 }, "", ""},
		{"max_step 1", func(p *PersonaDoc) { p.Bars[0].MaxStep = 1 }, "", ""},
		{"max_step 30 with the default day", func(p *PersonaDoc) { p.Bars[0].MaxStep = 30 }, "", ""},
		{"max_step and max_day 100", func(p *PersonaDoc) { p.Bars[0].MaxStep, p.Bars[0].MaxDay = 100, 100 }, "", ""},
		{"max_day 10 alone", func(p *PersonaDoc) { p.Bars[0].MaxDay = 10 }, "", ""},
		{"max_day equal to max_step", func(p *PersonaDoc) { p.Bars[0].MaxStep, p.Bars[0].MaxDay = 20, 20 }, "", ""},
		{"kind Outer", func(p *PersonaDoc) { p.Bars[0].Kind = "Outer" }, `bars[0].kind must be inner or outer, got "Outer"`, ""},
		{"max_step -1", func(p *PersonaDoc) { p.Bars[0].MaxStep = -1 },
			"bars[0].max_step must be in [1, 100] (0 for the default 10), got -1", ""},
		{"max_step 101", func(p *PersonaDoc) { p.Bars[0].MaxStep = 101 },
			"bars[0].max_step must be in [1, 100] (0 for the default 10), got 101", ""},
		{"max_day 101", func(p *PersonaDoc) { p.Bars[1].MaxDay = 101 },
			"bars[1].max_day must be in [1, 100] (0 for the default 30), got 101", ""},
		{"max_day -1", func(p *PersonaDoc) { p.Bars[1].MaxDay = -1 },
			"bars[1].max_day must be in [1, 100] (0 for the default 30), got -1", ""},
		{"max_step 40 over the default day", func(p *PersonaDoc) { p.Bars[0].MaxStep = 40 },
			"bars[0].max_day must be at least max_step (40), got 30", ""},
		{"max_day 5 under the default step", func(p *PersonaDoc) { p.Bars[0].MaxDay = 5 },
			"bars[0].max_day must be at least max_step (10), got 5", ""},
		{"max_step 101 with max_day 50", func(p *PersonaDoc) { p.Bars[0].MaxStep, p.Bars[0].MaxDay = 101, 50 },
			"bars[0].max_step must be in [1, 100]", "at least max_step"},
		{"max_step 40 with max_day 101", func(p *PersonaDoc) { p.Bars[0].MaxStep, p.Bars[0].MaxDay = 40, 101 },
			"bars[0].max_day must be in [1, 100]", "at least max_step"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPersona()
			p.Bars = testBars()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if tt.notErr != "" && strings.Contains(err.Error(), tt.notErr) {
				t.Fatalf("err = %v, want no %q", err, tt.notErr)
			}
		})
	}
}

func TestPersonaBars_ValidateKindAndLimitsCollected(t *testing.T) {
	p := DefaultPersona()
	p.Bars = testBars()
	p.Bars[0].Kind, p.Bars[0].MaxStep = "visible", 101
	p.Bars[1].MaxDay = 5
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate = nil, want errors")
	}
	for _, want := range []string{"bars[0].kind", "bars[0].max_step", "bars[1].max_day must be at least max_step (10), got 5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestPersonaBar_EffectiveLimits(t *testing.T) {
	for _, c := range []struct {
		bar       PersonaBar
		step, day int
		outer     bool
	}{
		{PersonaBar{}, DefaultBarMaxStep, DefaultBarMaxDay, false},
		{PersonaBar{Kind: BarKindOuter, MaxStep: 40, MaxDay: 100}, 40, 100, true},
		{PersonaBar{Kind: BarKindInner}, 10, 30, false},
		{PersonaBar{MaxStep: -3, MaxDay: -1}, 10, 30, false},
	} {
		if got := c.bar.EffectiveMaxStep(); got != c.step {
			t.Errorf("%+v EffectiveMaxStep = %d, want %d", c.bar, got, c.step)
		}
		if got := c.bar.EffectiveMaxDay(); got != c.day {
			t.Errorf("%+v EffectiveMaxDay = %d, want %d", c.bar, got, c.day)
		}
		if got := c.bar.Outer(); got != c.outer {
			t.Errorf("%+v Outer = %v, want %v", c.bar, got, c.outer)
		}
	}
	if DefaultBarMaxStep != 10 || DefaultBarMaxDay != 30 || MaxBarLimit != 100 {
		t.Errorf("limits = %d/%d/%d, want 10/30/100", DefaultBarMaxStep, DefaultBarMaxDay, MaxBarLimit)
	}
	if BarKindInner != "inner" || BarKindOuter != "outer" {
		t.Errorf("kinds = %q/%q, want inner/outer", BarKindInner, BarKindOuter)
	}
}

func TestPersonaBars_KindAndLimitsJSONAndYAML(t *testing.T) {
	b, err := json.Marshal(DefaultBars())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"kind"`, `"max_step"`, `"max_day"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("default bars encode %s: %s", key, b)
		}
	}

	bar := PersonaBar{Name: "肚子", Baseline: 20, HalfLife: Duration{2 * time.Hour}, Description: "高=很饱",
		Kind: BarKindOuter, MaxStep: 40, MaxDay: 100}
	b, err = json.Marshal(bar)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"肚子","baseline":20,"half_life":"2h0m0s","description":"高=很饱","kind":"outer","max_step":40,"max_day":100}`; string(b) != want {
		t.Errorf("JSON = %s, want %s", b, want)
	}
	var back PersonaBar
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, bar) {
		t.Errorf("JSON round trip = %+v, want %+v", back, bar)
	}

	y, err := yaml.Marshal(bar)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: outer\n", "max_step: 40\n", "max_day: 100\n"} {
		if !strings.Contains(string(y), want) {
			t.Errorf("YAML %s missing %q", y, want)
		}
	}
	back = PersonaBar{}
	if err := yaml.Unmarshal(y, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, bar) {
		t.Errorf("YAML round trip = %+v, want %+v", back, bar)
	}
	y, err = yaml.Marshal(DefaultBars())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kind:", "max_step:", "max_day:"} {
		if strings.Contains(string(y), key) {
			t.Errorf("default bars encode %s in YAML:\n%s", key, y)
		}
	}
}

func TestAutonomyBar_JSON(t *testing.T) {
	bar := AutonomyBar{Name: "心情", Value: 62, Band: "偏高", Baseline: 60, Description: "高=开心放松，低=低落烦躁"}
	b, err := json.Marshal(bar)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"kind"`, `"max_step"`, `"max_day"`} {
		if strings.Contains(string(b), key) {
			t.Errorf("inner bar with default limits encodes %s: %s", key, b)
		}
	}
	bar.Kind, bar.MaxStep, bar.MaxDay = BarKindOuter, 40, 100
	b, err = json.Marshal(bar)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"custom":false,"kind":"outer","max_step":40,"max_day":100}`; !strings.HasSuffix(string(b), want) {
		t.Errorf("JSON = %s, want it to end with %s", b, want)
	}
}
